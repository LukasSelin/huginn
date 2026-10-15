package journey

import (
	"fmt"
	"reflect"
	"testing"

	"huginn/internal/ast"
	"huginn/internal/codemap"
	"huginn/internal/lineage"
)

// moves is two packages of module example.com/m:
//
//	2026-03-02 a: add p.F (2 stmts), p.G, q.Q
//	2026-03-03 b: move p.G to q; rename p.F → p.F2, with a new signature and 3 stmts
//	2026-03-04 c: delete p.F2
func moves() *lineage.History {
	f, g, q := fn("p/x.go", "F"), fn("p/x.go", "G"), fn("q/q.go", "Q")
	f.Stmts, f.Signature = 2, "func F()"
	g.StartLine = 5
	g2 := fn("q/g.go", "G")
	f2 := fn("p/x.go", "F2")
	f2.Stmts, f2.Signature = 3, "func F2(n int)"

	h := lineage.New()
	h.Apply(lineage.Revision{SHA: "a", Time: day("2026-03-02"), Changes: []ast.Change{
		{Flags: ast.Added, New: f}, {Flags: ast.Added, New: g}, {Flags: ast.Added, New: q},
	}})
	h.Apply(lineage.Revision{SHA: "b", Time: day("2026-03-03"), Changes: []ast.Change{
		{Flags: ast.Moved, Old: g, New: g2},
		{Flags: ast.Renamed | ast.SignatureChanged | ast.BodyChanged, Old: f, New: f2},
	}})
	h.Apply(lineage.Revision{SHA: "c", Time: day("2026-03-04"), Changes: []ast.Change{
		{Flags: ast.Deleted, Old: f2},
	}})
	return h
}

// flat renders a view's frames for comparison: per frame, the sizes set,
// the symbols gone and the events, by symbol name.
func flat(v *SymbolView) []string {
	var out []string
	for _, f := range v.Frames {
		s := ""
		for _, z := range f.Set {
			s += fmt.Sprintf("+%s:%d ", v.Symbols[z.Symbol].Name, z.Stmts)
		}
		for _, k := range f.Gone {
			s += fmt.Sprintf("-%s ", v.Symbols[k].Name)
		}
		for _, e := range f.Events {
			s += fmt.Sprintf("[%d %s %s %s%s%s%s] ", e.Rev, e.Kind, e.Name, e.From, e.To, e.OldSig, e.Sig)
		}
		out = append(out, s)
	}
	return out
}

func TestSymbolsCommitFrames(t *testing.T) {
	h := moves()
	j := Build(h, codemap.Build(h), Commit, Options{})

	p := j.Symbols(h, "p", SymbolOptions{})
	want := []string{
		"+F:2 +G:0 [0 born F ] [0 born G ] ",
		"+F:3 -G [1 renamed F2 F] [1 signature F2 func F()func F2(n int)] [1 out G q] ",
		"-F [2 died F2 ] ",
	}
	if got := flat(p); !reflect.DeepEqual(got, want) {
		t.Errorf("p frames:\n%q\nwant\n%q", got, want)
	}
	if p.Total != 2 || p.Truncated || p.Symbols[0].File != "x.go" || p.Symbols[0].PeakStmts != 3 || p.Symbols[0].Kind != ast.KindFunc {
		t.Errorf("p = %+v", p)
	}
	for _, s := range p.Symbols {
		if d := (s.X-0.5)*(s.X-0.5) + (s.Y-0.5)*(s.Y-0.5); d > 0.25 {
			t.Errorf("%s at (%g, %g) is off the disc", s.Name, s.X, s.Y)
		}
	}

	q := j.Symbols(h, "q", SymbolOptions{})
	want = []string{
		"+Q:0 [0 born Q ] ",
		"+G:0 [1 in G p] ",
		"",
	}
	if got := flat(q); !reflect.DeepEqual(got, want) {
		t.Errorf("q frames:\n%q\nwant\n%q", got, want)
	}

	if v := j.Symbols(h, "nowhere", SymbolOptions{}); v.Total != 0 || len(v.Symbols) != 0 || len(v.Frames) != len(j.Frames) {
		t.Errorf("nowhere = %+v", v)
	}
}

func TestSymbolsWeekFrame(t *testing.T) {
	h := moves()
	j := Build(h, codemap.Build(h), Week, Options{})
	p := j.Symbols(h, "p", SymbolOptions{})
	// One week: everything happens in one frame and nothing is left.
	want := []string{"[0 born F ] [0 born G ] [1 renamed F2 F] [1 signature F2 func F()func F2(n int)] [1 out G q] [2 died F2 ] "}
	if got := flat(p); !reflect.DeepEqual(got, want) {
		t.Errorf("p frames:\n%q\nwant\n%q", got, want)
	}
}

func TestSymbolsLimit(t *testing.T) {
	h := moves()
	j := Build(h, codemap.Build(h), Commit, Options{})

	// F has the most statements.
	v := j.Symbols(h, "p", SymbolOptions{Limit: 1})
	if v.Total != 2 || !v.Truncated || len(v.Symbols) != 1 || v.Symbols[0].Name != "F" {
		t.Fatalf("limited = %+v", v)
	}
	if got := flat(v); got[1] != "+F:3 [1 renamed F2 F] [1 signature F2 func F()func F2(n int)] " {
		t.Errorf("limited frame 1 = %q", got[1])
	}

	// Keep wins over size.
	g := h.Find("G")[0].ID
	v = j.Symbols(h, "p", SymbolOptions{Limit: 1, Keep: g})
	if len(v.Symbols) != 1 || v.Symbols[0].ID != g {
		t.Errorf("kept = %+v", v.Symbols)
	}
}
