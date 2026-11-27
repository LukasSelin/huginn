package affinity

import (
	"cmp"
	"fmt"
	"slices"
)

// suggest orders the extractions and notes where the signals disagree.
//
// Free methods go first: they are the safest move. Then the blocks of state
// and services, those owning their writes first, then the most often
// changed, then those with the fewest crossings: the safest moves with the
// biggest payoff. Shared blocks are narrowed rather than extracted, and the
// core remains with the coordinators.
func (b *build) suggest() {
	r := b.r
	if len(b.free) > 0 {
		r.Steps = append(r.Steps, b.freeStep())
	}
	for _, n := range b.extractOrder() {
		r.Steps = append(r.Steps, b.extractStep(n))
	}
	for n, bl := range r.Blocks {
		if bl.Kind == Shared {
			r.Steps = append(r.Steps, Step{Kind: "narrow", Block: n, Rows: bl.Rows, Signals: []string{},
				Text: fmt.Sprintf("Give %s only what they read (%s)", b.rowNames(bl.Rows, 4), bl.Label)})
		}
	}
	core := slices.IndexFunc(r.Blocks, func(bl Block) bool { return bl.Kind == Core })
	if core >= 0 || len(b.bridges) > 0 {
		r.Steps = append(r.Steps, b.remainsStep(core))
	}
	b.notes()
}

// freeStep moves out the free methods.
func (b *build) freeStep() Step {
	s := Step{Kind: "free", Block: -1, Rows: b.free, Signals: []string{},
		Text: "Move out " + b.rowNames(b.free, 4) + ": they touch no field and call no method"}
	if p := b.sharedParam(b.free); p != "" && len(b.free) > 1 {
		s.Signals = append(s.Signals, "all take "+p)
	}
	return s
}

// extractOrder returns the blocks of state and services in the order to
// extract them: those owning their writes first, then the most often
// changed, then those with the fewest crossings.
func (b *build) extractOrder() []int {
	r := b.r
	var extract []int
	for n, bl := range r.Blocks {
		if bl.Kind == State || bl.Kind == Service {
			extract = append(extract, n)
		}
	}
	slices.SortStableFunc(extract, func(x, y int) int {
		bx, by := r.Blocks[x], r.Blocks[y]
		return cmp.Or(-cmp.Compare(btoi(bx.Exclusive), btoi(by.Exclusive)), cmp.Compare(by.Edits, bx.Edits),
			cmp.Compare(len(bx.Crossings), len(by.Crossings)), cmp.Compare(x, y))
	})
	return extract
}

// extractStep extracts block n, with the signals that speak for it.
func (b *build) extractStep(n int) Step {
	bl := b.r.Blocks[n]
	s := Step{Kind: "extract", Block: n, Rows: bl.Rows, Signals: b.extractSignals(bl),
		Text: fmt.Sprintf("Extract %s (%s): %s", bl.Letter, bl.Label, b.colNames(bl.Columns))}
	if len(bl.Rows) > 0 {
		s.Text += " with " + b.rowNames(bl.Rows, 4)
	}
	return s
}

// extractSignals lists what each side says of a block to extract: who
// writes its fields, how often it changed and how much together, how its
// clients use it, what it is named and how often it is crossed.
func (b *build) extractSignals(bl Block) []string {
	signals := []string{}
	if bl.Exclusive {
		signals = append(signals, "owns its writes")
	} else {
		signals = append(signals, "shares writes with another block")
	}
	signals = append(signals, fmt.Sprintf("%s in %s", plural(bl.Edits, "edit"), plural(bl.Commits, "commit")))
	if bl.Cohesion != nil {
		signals = append(signals, fmt.Sprintf("changes together %.0f%%", 100**bl.Cohesion))
	}
	if bl.Clients > 0 {
		signals = append(signals, onlyClientsSignal(bl))
	}
	if bl.Word != "" {
		signals = append(signals, "named "+bl.Word)
	}
	if len(bl.Crossings) > 0 {
		signals = append(signals, plural(len(bl.Crossings), "crossing"))
	}
	return signals
}

// onlyClientsSignal says how many of a block's clients use only it; the
// block must have clients.
func onlyClientsSignal(bl Block) string {
	switch only := int(*bl.Agreement*float64(bl.Clients) + 0.5); {
	case bl.Clients == 1 && only == 1:
		return "its client uses only it"
	case only == bl.Clients:
		return fmt.Sprintf("all %d clients use only it", only)
	default:
		return fmt.Sprintf("%d of its %d clients use only it", only, bl.Clients)
	}
}

// remainsStep leaves the core block, -1 for none, with the coordinators.
func (b *build) remainsStep(core int) Step {
	s := Step{Kind: "remains", Block: core, Rows: orEmpty(b.bridges), Signals: []string{}}
	switch {
	case core >= 0 && len(b.bridges) > 0:
		s.Text = fmt.Sprintf("What remains: %s (core) and the coordinators %s", b.r.Blocks[core].Letter, b.rowNames(b.bridges, 4))
	case core >= 0:
		s.Text = fmt.Sprintf("What remains: %s (core)", b.r.Blocks[core].Letter)
	default:
		s.Text = "What remains: the coordinators " + b.rowNames(b.bridges, 4)
	}
	return s
}

// notes reports where the signals disagree.
func (b *build) notes() {
	// Git: blocks that change together, and blocks whose methods do not.
	b.noteGitTogether()
	b.noteGitApart()

	// Clients: one block in several roles, one role across blocks.
	b.noteClientsSplit()
	b.noteClientsTogether()

	// Names that cross blocks, and declaration groups that do.
	b.noteNamesTogether()
	b.noteDeclGroups()
}

// addNote adds a note, with empty rather than nil lists.
func (b *build) addNote(n Note) {
	n.Blocks, n.Rows = orEmpty(n.Blocks), orEmpty(n.Rows)
	b.r.Notes = append(b.r.Notes, n)
}

// noteGitTogether notes each pair of blocks the matrix splits whose methods
// change together, once.
func (b *build) noteGitTogether() {
	r := b.r
	seen := map[[2]int]bool{}
	for _, p := range r.Coupling.Pairs {
		if !p.Strong || !p.Cross {
			continue
		}
		k := [2]int{min(b.rowBlock[p.A], b.rowBlock[p.B]), max(b.rowBlock[p.A], b.rowBlock[p.B])}
		if seen[k] {
			continue
		}
		seen[k] = true
		explain := map[string]string{
			"boundary":   "the boundary may be in the wrong place",
			"duplicated": "a rule may live in both",
			"concept":    "a concept may be missing between them",
		}[p.Kind]
		b.addNote(Note{Kind: "gitTogether", Blocks: k[:], Rows: []int{p.A, p.B},
			Text: fmt.Sprintf("The matrix splits %s and %s, but %s and %s change together: %s, or don't split", letter(k[0]), letter(k[1]), r.Rows[p.A].Method, r.Rows[p.B].Method, explain)})
	}
}

// noteGitApart notes the blocks of three or more methods, other than
// shared ones, whose methods change only with other blocks'.
func (b *build) noteGitApart() {
	for n, bl := range b.r.Blocks {
		if len(bl.Rows) < 3 || bl.Kind == Shared {
			continue
		}
		if b.changesOnlyWithOthers(n) {
			b.addNote(Note{Kind: "gitApart", Blocks: []int{n},
				Text: fmt.Sprintf("The methods of %s never change together, only with other blocks': it may hold together by accident", bl.Letter)})
		}
	}
}

// changesOnlyWithOthers reports whether the methods of block n change
// together with other blocks' methods but never with each other.
func (b *build) changesOnlyWithOthers(n int) bool {
	var inside, across bool
	for _, p := range b.r.Coupling.Pairs {
		x, y := b.rowBlock[p.A] == n, b.rowBlock[p.B] == n
		inside = inside || x && y
		across = across || x != y && (x || y)
	}
	return !inside && across
}

// noteClientsSplit notes the blocks, other than shared ones, used in two
// or more roles.
func (b *build) noteClientsSplit() {
	r := b.r
	for n, bl := range r.Blocks {
		var roles []int
		for x, role := range r.Clients.Roles {
			if slices.Contains(role.Blocks, n) {
				roles = append(roles, x)
			}
		}
		if len(roles) >= 2 && bl.Kind != Shared {
			b.addNote(Note{Kind: "clientsSplit", Blocks: []int{n},
				Text: fmt.Sprintf("%s is used in %d roles that share nothing: segregate its interfaces, keep one implementation", bl.Letter, len(roles))})
		}
	}
}

// noteClientsTogether notes the roles that span two or more blocks.
func (b *build) noteClientsTogether() {
	for _, role := range b.r.Clients.Roles {
		if len(role.Blocks) >= 2 {
			b.addNote(Note{Kind: "clientsTogether", Blocks: role.Blocks, Rows: role.Rows,
				Text: fmt.Sprintf("%s: one role across %s, so give it a facade rather than splitting along its lines", role.Text, b.letters(role.Blocks))})
		}
	}
}

// noteNamesTogether notes the words that name members of two or more
// blocks.
func (b *build) noteNamesTogether() {
	for _, w := range b.r.Names.Words {
		if blocks := b.namesTogetherBlocks(w); len(blocks) >= 2 {
			b.addNote(Note{Kind: "namesTogether", Blocks: blocks, Rows: w.Rows,
				Text: fmt.Sprintf("%q names members of %s: the code may be ahead of the vocabulary; rename first", w.Word, b.letters(blocks))})
		}
	}
}

// namesTogetherBlocks returns, in order, the blocks holding at least two of
// the members a word names.
func (b *build) namesTogetherBlocks(w Word) []int {
	per := map[int]int{}
	for _, i := range w.Rows {
		per[b.r.Rows[i].Block]++
	}
	for _, j := range w.Cols {
		per[b.r.Columns[j].Block]++
	}
	var blocks []int
	for bl, k := range per {
		if bl >= 0 && k >= 2 {
			blocks = append(blocks, bl)
		}
	}
	slices.Sort(blocks)
	return blocks
}

// noteDeclGroups notes the declaration groups that fall in several blocks,
// when there is more than one group.
func (b *build) noteDeclGroups() {
	groups := b.r.Names.Groups
	for _, g := range groups {
		if !g.Agrees && len(groups) > 1 {
			b.addNote(Note{Kind: "declGroups", Blocks: g.Blocks,
				Text: fmt.Sprintf("%s were declared together but fall in %s", b.colNames(g.Cols), b.letters(g.Blocks))})
		}
	}
}

func btoi(x bool) int {
	if x {
		return 1
	}
	return 0
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
