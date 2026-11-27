package affinity

import (
	"cmp"
	"slices"
	"strings"
	"unicode"
)

var stopWords = set("get", "set", "is", "has", "new", "to", "from", "of", "by", "for", "with", "in", "on", "at",
	"and", "or", "the", "a", "an", "do", "make", "must", "try")

var modeSuffixes = []string{"kind", "mode", "state", "status", "type", "typ", "phase", "stage"}

// names reads what the members' names say: the words they share, the
// fields that travel together, the fields that look like modes, and the
// groups the fields were declared in. A block's most shared word labels it.
func (b *build) names() {
	r := b.r
	r.Names = Names{Words: b.sharedWords(), Clumps: b.clumps(), Flags: b.modeFlags(), Groups: b.fieldGroups()}
	for n := range r.Blocks {
		b.blockWord(&r.Blocks[n])
	}
}

// wordMembers are the rows and columns whose names hold a word.
type wordMembers struct{ rows, cols []int }

// membersByWord gathers the members whose names hold each word, and the
// words in the order first seen, the columns' before the rows'.
func (b *build) membersByWord() (order []string, byWord map[string]*wordMembers) {
	byWord = map[string]*wordMembers{}
	note := func(name string, add func(*wordMembers)) {
		for _, w := range uniq(words(name)) {
			if byWord[w] == nil {
				byWord[w] = &wordMembers{}
				order = append(order, w)
			}
			add(byWord[w])
		}
	}
	for j, c := range b.r.Columns {
		note(c.Name, func(m *wordMembers) { m.cols = append(m.cols, j) })
	}
	for i, row := range b.r.Rows {
		note(row.Method, func(m *wordMembers) { m.rows = append(m.rows, i) })
	}
	return order, byWord
}

// sharedWords lists the words the names of two or more members share, the
// most shared first, each with the blocks of those members.
func (b *build) sharedWords() []Word {
	r := b.r
	order, byWord := b.membersByWord()
	out := []Word{}
	for _, w := range order {
		m := byWord[w]
		if len(m.rows)+len(m.cols) < 2 {
			continue
		}
		word := Word{Word: w, Rows: orEmpty(m.rows), Cols: orEmpty(m.cols), Blocks: []int{}}
		for _, i := range m.rows {
			word.Blocks = addBlock(word.Blocks, r.Rows[i].Block)
		}
		for _, j := range m.cols {
			word.Blocks = addBlock(word.Blocks, r.Columns[j].Block)
		}
		slices.Sort(word.Blocks)
		out = append(out, word)
	}
	slices.SortStableFunc(out, func(x, y Word) int {
		return cmp.Compare(len(y.Rows)+len(y.Cols), len(x.Rows)+len(x.Cols))
	})
	return out
}

// blockWord gives a block its word and, unless it is the core, makes that
// its label; a shared block gets none. A block's word is the one most of
// its members share, the first seen on a tie.
func (b *build) blockWord(bl *Block) {
	if bl.Kind == Shared {
		return
	}
	r := b.r
	count := map[string]int{}
	var seen []string
	tally := func(name string) {
		for _, w := range uniq(words(name)) {
			if count[w] == 0 {
				seen = append(seen, w)
			}
			count[w]++
		}
	}
	for _, j := range bl.Columns {
		tally(r.Columns[j].Name)
	}
	for _, i := range bl.Rows {
		tally(r.Rows[i].Method)
	}
	for _, w := range seen {
		if count[w] >= 2 && (bl.Word == "" || count[w] > count[bl.Word]) {
			bl.Word = w
		}
	}
	if bl.Word != "" && bl.Kind != Core {
		bl.Label = bl.Word
	}
}

// modeFlags lists the fields other than infrastructure that look like a
// flag or a mode, with the methods reading them.
func (b *build) modeFlags() []Flag {
	r := b.r
	out := []Flag{}
	for j, c := range r.Columns {
		why := modeWhy(c.Name, c.Type)
		if why == "" || c.Class == Infra {
			continue
		}
		f := Flag{Col: j, Readers: []int{}, Why: why}
		for i := range r.Rows {
			if b.cell[i][j].r {
				f.Readers = append(f.Readers, i)
			}
		}
		out = append(out, f)
	}
	return out
}

// modeWhy says why a field of the given name and type looks like a flag or
// a mode; "" when it does not.
func modeWhy(name, typ string) string {
	lower := strings.ToLower(name)
	switch {
	case typ == "bool":
		return "a flag: each value may be a hidden type or strategy"
	case slices.ContainsFunc(modeSuffixes, func(s string) bool { return strings.HasSuffix(lower, s) }):
		return "named like a mode or state: each value may be a hidden type, with fields valid only in it"
	}
	return ""
}

// fieldGroups lists the runs of fields declared together, in order, and
// whether each lies within one block.
func (b *build) fieldGroups() []Group {
	r := b.r
	byGroup := map[int]*Group{}
	var groups []int
	for j, c := range r.Columns {
		g := byGroup[c.Group]
		if g == nil {
			g = &Group{Group: c.Group, Cols: []int{}, Blocks: []int{}}
			byGroup[c.Group] = g
			groups = append(groups, c.Group)
		}
		g.Cols = append(g.Cols, j)
		g.Blocks = addBlock(g.Blocks, c.Block)
	}
	slices.Sort(groups)
	out := []Group{}
	for _, n := range groups {
		g := byGroup[n]
		slices.SortFunc(g.Cols, func(x, y int) int { return cmp.Compare(r.Columns[x].Decl, r.Columns[y].Decl) })
		slices.Sort(g.Blocks)
		g.Agrees = len(g.Blocks) <= 1
		out = append(out, *g)
	}
	return out
}

func addBlock(blocks []int, bl int) []int {
	if bl < 0 || slices.Contains(blocks, bl) {
		return blocks
	}
	return append(blocks, bl)
}

func uniq(ws []string) []string {
	var out []string
	for _, w := range ws {
		if !slices.Contains(out, w) {
			out = append(out, w)
		}
	}
	return out
}

// words splits an identifier into lower-case words, minus plural s and
// stop words: "detailURLs" → detail, url; "HTTPServer" → http, server.
func words(name string) []string {
	rs := []rune(name)
	var parts []string
	start := 0
	cut := func(i int) {
		if i > start {
			parts = append(parts, string(rs[start:i]))
		}
		start = i
	}
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch {
		case c == '_' || unicode.IsDigit(c):
			cut(i)
			start = i + 1
		case i > 0 && unicode.IsUpper(c) && unicode.IsLower(rs[i-1]):
			cut(i)
		case i > 0 && unicode.IsUpper(c) && unicode.IsUpper(rs[i-1]) && i+1 < len(rs) && unicode.IsLower(rs[i+1]) &&
			(rs[i+1] != 's' || i+2 < len(rs) && unicode.IsLower(rs[i+2])):
			cut(i) // the end of an acronym: HTTP|Server, but URLs stays whole
		}
	}
	cut(len(rs))
	var out []string
	for _, p := range parts {
		w := strings.ToLower(p)
		if len(w) > 3 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") && !strings.HasSuffix(w, "us") && !strings.HasSuffix(w, "is") {
			w = w[:len(w)-1]
		}
		if w != "" && !stopWords[w] {
			out = append(out, w)
		}
	}
	return out
}
