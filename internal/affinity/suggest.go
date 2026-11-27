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
		s := Step{Kind: "free", Block: -1, Rows: b.free, Signals: []string{},
			Text: "Move out " + b.rowNames(b.free, 4) + ": they touch no field and call no method"}
		if p := b.sharedParam(b.free); p != "" && len(b.free) > 1 {
			s.Signals = append(s.Signals, "all take "+p)
		}
		r.Steps = append(r.Steps, s)
	}
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
	for _, n := range extract {
		bl := r.Blocks[n]
		s := Step{Kind: "extract", Block: n, Rows: bl.Rows, Signals: []string{},
			Text: fmt.Sprintf("Extract %s (%s): %s", bl.Letter, bl.Label, b.colNames(bl.Columns))}
		if len(bl.Rows) > 0 {
			s.Text += " with " + b.rowNames(bl.Rows, 4)
		}
		if bl.Exclusive {
			s.Signals = append(s.Signals, "owns its writes")
		} else {
			s.Signals = append(s.Signals, "shares writes with another block")
		}
		s.Signals = append(s.Signals, fmt.Sprintf("%s in %s", plural(bl.Edits, "edit"), plural(bl.Commits, "commit")))
		if bl.Cohesion != nil {
			s.Signals = append(s.Signals, fmt.Sprintf("changes together %.0f%%", 100**bl.Cohesion))
		}
		if bl.Clients > 0 {
			switch only := int(*bl.Agreement*float64(bl.Clients) + 0.5); {
			case bl.Clients == 1 && only == 1:
				s.Signals = append(s.Signals, "its client uses only it")
			case only == bl.Clients:
				s.Signals = append(s.Signals, fmt.Sprintf("all %d clients use only it", only))
			default:
				s.Signals = append(s.Signals, fmt.Sprintf("%d of its %d clients use only it", only, bl.Clients))
			}
		}
		if bl.Word != "" {
			s.Signals = append(s.Signals, "named "+bl.Word)
		}
		if len(bl.Crossings) > 0 {
			s.Signals = append(s.Signals, plural(len(bl.Crossings), "crossing"))
		}
		r.Steps = append(r.Steps, s)
	}
	for n, bl := range r.Blocks {
		if bl.Kind == Shared {
			r.Steps = append(r.Steps, Step{Kind: "narrow", Block: n, Rows: bl.Rows, Signals: []string{},
				Text: fmt.Sprintf("Give %s only what they read (%s)", b.rowNames(bl.Rows, 4), bl.Label)})
		}
	}
	core := slices.IndexFunc(r.Blocks, func(bl Block) bool { return bl.Kind == Core })
	if core >= 0 || len(b.bridges) > 0 {
		s := Step{Kind: "remains", Block: core, Rows: orEmpty(b.bridges), Signals: []string{}}
		switch {
		case core >= 0 && len(b.bridges) > 0:
			s.Text = fmt.Sprintf("What remains: %s (core) and the coordinators %s", r.Blocks[core].Letter, b.rowNames(b.bridges, 4))
		case core >= 0:
			s.Text = fmt.Sprintf("What remains: %s (core)", r.Blocks[core].Letter)
		default:
			s.Text = "What remains: the coordinators " + b.rowNames(b.bridges, 4)
		}
		r.Steps = append(r.Steps, s)
	}
	b.notes()
}

// notes reports where the signals disagree.
func (b *build) notes() {
	r := b.r
	add := func(n Note) {
		n.Blocks, n.Rows = orEmpty(n.Blocks), orEmpty(n.Rows)
		r.Notes = append(r.Notes, n)
	}

	// Git: blocks that change together, and blocks whose methods do not.
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
		add(Note{Kind: "gitTogether", Blocks: k[:], Rows: []int{p.A, p.B},
			Text: fmt.Sprintf("The matrix splits %s and %s, but %s and %s change together: %s, or don't split", letter(k[0]), letter(k[1]), r.Rows[p.A].Method, r.Rows[p.B].Method, explain)})
	}
	for n, bl := range r.Blocks {
		if len(bl.Rows) < 3 || bl.Kind == Shared {
			continue
		}
		var inside, across bool
		for _, p := range r.Coupling.Pairs {
			x, y := b.rowBlock[p.A] == n, b.rowBlock[p.B] == n
			inside = inside || x && y
			across = across || x != y && (x || y)
		}
		if !inside && across {
			add(Note{Kind: "gitApart", Blocks: []int{n},
				Text: fmt.Sprintf("The methods of %s never change together, only with other blocks': it may hold together by accident", bl.Letter)})
		}
	}

	// Clients: one block in several roles, one role across blocks.
	for n, bl := range r.Blocks {
		var roles []int
		for x, role := range r.Clients.Roles {
			if slices.Contains(role.Blocks, n) {
				roles = append(roles, x)
			}
		}
		if len(roles) >= 2 && bl.Kind != Shared {
			add(Note{Kind: "clientsSplit", Blocks: []int{n},
				Text: fmt.Sprintf("%s is used in %d roles that share nothing: segregate its interfaces, keep one implementation", bl.Letter, len(roles))})
		}
	}
	for _, role := range r.Clients.Roles {
		if len(role.Blocks) >= 2 {
			add(Note{Kind: "clientsTogether", Blocks: role.Blocks, Rows: role.Rows,
				Text: fmt.Sprintf("%s: one role across %s, so give it a facade rather than splitting along its lines", role.Text, b.letters(role.Blocks))})
		}
	}

	// Names that cross blocks, and declaration groups that do.
	for _, w := range r.Names.Words {
		per := map[int]int{}
		for _, i := range w.Rows {
			per[r.Rows[i].Block]++
		}
		for _, j := range w.Cols {
			per[r.Columns[j].Block]++
		}
		var blocks []int
		for bl, k := range per {
			if bl >= 0 && k >= 2 {
				blocks = append(blocks, bl)
			}
		}
		if len(blocks) >= 2 {
			slices.Sort(blocks)
			add(Note{Kind: "namesTogether", Blocks: blocks, Rows: w.Rows,
				Text: fmt.Sprintf("%q names members of %s: the code may be ahead of the vocabulary; rename first", w.Word, b.letters(blocks))})
		}
	}
	for _, g := range r.Names.Groups {
		if !g.Agrees && len(r.Names.Groups) > 1 {
			add(Note{Kind: "declGroups", Blocks: g.Blocks,
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
