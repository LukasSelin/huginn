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
	type members struct{ rows, cols []int }
	byWord := map[string]*members{}
	var order []string
	note := func(name string, add func(*members)) {
		seen := map[string]bool{}
		for _, w := range words(name) {
			if seen[w] {
				continue
			}
			seen[w] = true
			if byWord[w] == nil {
				byWord[w] = &members{}
				order = append(order, w)
			}
			add(byWord[w])
		}
	}
	for j, c := range r.Columns {
		note(c.Name, func(m *members) { m.cols = append(m.cols, j) })
	}
	for i, row := range r.Rows {
		note(row.Method, func(m *members) { m.rows = append(m.rows, i) })
	}
	r.Names = Names{Words: []Word{}, Clumps: b.clumps(), Flags: []Flag{}, Groups: []Group{}}
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
		r.Names.Words = append(r.Names.Words, word)
	}
	slices.SortStableFunc(r.Names.Words, func(x, y Word) int {
		return cmp.Compare(len(y.Rows)+len(y.Cols), len(x.Rows)+len(x.Cols))
	})

	// A block's word is the one most of its members share, the first seen
	// on a tie.
	for n := range r.Blocks {
		bl := &r.Blocks[n]
		if bl.Kind == Shared {
			continue
		}
		count := map[string]int{}
		var seen []string
		for _, j := range bl.Columns {
			for _, w := range uniq(words(r.Columns[j].Name)) {
				if count[w] == 0 {
					seen = append(seen, w)
				}
				count[w]++
			}
		}
		for _, i := range bl.Rows {
			for _, w := range uniq(words(r.Rows[i].Method)) {
				if count[w] == 0 {
					seen = append(seen, w)
				}
				count[w]++
			}
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

	for j, c := range r.Columns {
		why := ""
		lower := strings.ToLower(c.Name)
		switch {
		case c.Type == "bool":
			why = "a flag: each value may be a hidden type or strategy"
		case slices.ContainsFunc(modeSuffixes, func(s string) bool { return strings.HasSuffix(lower, s) }):
			why = "named like a mode or state: each value may be a hidden type, with fields valid only in it"
		}
		if why == "" || c.Class == Infra {
			continue
		}
		f := Flag{Col: j, Readers: []int{}, Why: why}
		for i := range r.Rows {
			if b.cell[i][j].r {
				f.Readers = append(f.Readers, i)
			}
		}
		r.Names.Flags = append(r.Names.Flags, f)
	}

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
	for _, n := range groups {
		g := byGroup[n]
		slices.SortFunc(g.Cols, func(x, y int) int { return cmp.Compare(r.Columns[x].Decl, r.Columns[y].Decl) })
		slices.Sort(g.Blocks)
		g.Agrees = len(g.Blocks) <= 1
		r.Names.Groups = append(r.Names.Groups, *g)
	}
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
