package journey

import (
	"math"
	"reflect"
	"testing"
	"time"

	"huginn/internal/ast"
	"huginn/internal/codemap"
	"huginn/internal/lineage"
)

func fn(path, name string, imports ...string) *ast.Symbol {
	s := &ast.Symbol{Kind: ast.KindFunc, Name: name, Key: name, Path: path, Exported: true, StartLine: 1, EndLine: 3}
	for _, imp := range imports {
		s.Refs = append(s.Refs, ast.Ref{Kind: ast.RefImport, Pkg: imp, Name: "X", Count: 1})
	}
	return s
}

func day(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t.Add(12 * time.Hour)
}

// fixture is three packages of module example.com/m:
//
//	2026-03-02 W10 a: add a.F, a.G (a.G imports b and fmt), a/a_test.TestF
//	2026-03-03 W10 b: add b.H, c.K (c.K imports go/ast)
//	2026-03-10 W11 c: edit a.F, delete c.K (c dies)
//	2026-04-06 W15 d: rename b.H → b.I, which now imports a
func fixture() *lineage.History {
	f, g, tf := fn("a/a.go", "F"), fn("a/a.go", "G", "example.com/m/b", "fmt"), fn("a/a_test.go", "TestF")
	hh, k := fn("b/b.go", "H"), fn("c/c.go", "K", "go/ast")
	f2 := fn("a/a.go", "F")
	f2.StartLine = 5
	i := fn("b/b.go", "I", "example.com/m/a")

	h := lineage.New()
	h.Apply(lineage.Revision{SHA: "a", Time: day("2026-03-02"), Changes: []ast.Change{
		{Flags: ast.Added, New: f}, {Flags: ast.Added, New: g}, {Flags: ast.Added, New: tf},
	}})
	h.Apply(lineage.Revision{SHA: "b", Time: day("2026-03-03"), Changes: []ast.Change{
		{Flags: ast.Added, New: hh}, {Flags: ast.Added, New: k},
	}})
	h.Apply(lineage.Revision{SHA: "c", Time: day("2026-03-10"), Changes: []ast.Change{
		{Flags: ast.BodyChanged, Old: f, New: f2}, {Flags: ast.Deleted, Old: k},
	}})
	h.Apply(lineage.Revision{SHA: "d", Time: day("2026-04-06"), Changes: []ast.Change{
		{Flags: ast.Renamed | ast.SignatureChanged, Old: hh, New: i},
	}})
	return h
}

func build(step Step) *Journey {
	h := fixture()
	return Build(h, codemap.Build(h), step, Options{})
}

// state flattens a frame to package path → live symbols and total edits.
func state(j *Journey, f Frame) map[string][2]int {
	out := map[string][2]int{}
	for _, p := range f.Packages {
		out[j.Packages[p.Package].Path] = [2]int{p.Live, p.Edits.Total()}
	}
	return out
}

func deps(j *Journey, f Frame) []string {
	var out []string
	for _, d := range f.Deps {
		out = append(out, j.Packages[d.From].Path+"→"+j.Packages[d.To].Path)
	}
	return out
}

func TestCommitFrames(t *testing.T) {
	j := build(Commit)
	if got := len(j.Frames); got != 4 {
		t.Fatalf("%d frames, want 4", got)
	}
	var paths []string
	for _, p := range j.Packages {
		paths = append(paths, p.Path)
	}
	if want := []string{"a", "b", "c"}; !reflect.DeepEqual(paths, want) {
		t.Errorf("packages %v, want %v (tests left out)", paths, want)
	}
	tests := []struct {
		live, total int
		state       map[string][2]int
		deps        []string
	}{
		{2, 1, map[string][2]int{"a": {2, 2}}, nil},
		{4, 2, map[string][2]int{"a": {2, 0}, "b": {1, 1}, "c": {1, 1}}, []string{"a→b"}},
		{3, 3, map[string][2]int{"a": {2, 1}, "b": {1, 0}, "c": {0, 1}}, []string{"a→b"}},
		{3, 4, map[string][2]int{"a": {2, 0}, "b": {1, 1}}, []string{"a→b", "b→a"}},
	}
	for i, tt := range tests {
		f := j.Frames[i]
		if f.Live != tt.live || f.Total != tt.total || f.Commits != 1 {
			t.Errorf("frame %d: live %d total %d commits %d, want %d %d 1", i, f.Live, f.Total, f.Commits, tt.live, tt.total)
		}
		if got := state(j, f); !reflect.DeepEqual(got, tt.state) {
			t.Errorf("frame %d: packages %v, want %v", i, got, tt.state)
		}
		if got := deps(j, f); !reflect.DeepEqual(got, tt.deps) {
			t.Errorf("frame %d: deps %v, want %v", i, got, tt.deps)
		}
	}
	if j.Frames[3].SHA != "d" {
		t.Errorf("frame 3 SHA %q", j.Frames[3].SHA)
	}
}

func TestWeekFrames(t *testing.T) {
	j := build(Week)
	// W10 through W15, quiet weeks included.
	if got := len(j.Frames); got != 6 {
		t.Fatalf("%d frames, want 6", got)
	}
	if !j.Frames[0].Start.Equal(day("2026-03-02").Truncate(24 * time.Hour)) {
		t.Errorf("first frame starts %v", j.Frames[0].Start)
	}
	commits := []int{2, 1, 0, 0, 0, 1}
	for i, f := range j.Frames {
		if f.Commits != commits[i] {
			t.Errorf("frame %d: %d commits, want %d", i, f.Commits, commits[i])
		}
		if !f.End.Equal(f.Start.AddDate(0, 0, 7)) {
			t.Errorf("frame %d: spans %v to %v", i, f.Start, f.End)
		}
	}
	if got, want := state(j, j.Frames[0]), map[string][2]int{"a": {2, 2}, "b": {1, 1}, "c": {1, 1}}; !reflect.DeepEqual(got, want) {
		t.Errorf("W10 %v, want %v", got, want)
	}
	if got, want := state(j, j.Frames[3]), map[string][2]int{"a": {2, 0}, "b": {1, 0}}; !reflect.DeepEqual(got, want) {
		t.Errorf("quiet W13 %v, want %v", got, want)
	}
	if f := j.Frames[5]; f.Total != 4 || f.Live != 3 {
		t.Errorf("last frame total %d live %d", f.Total, f.Live)
	}
}

func TestMonthFrames(t *testing.T) {
	j := build(Month)
	if got := len(j.Frames); got != 2 {
		t.Fatalf("%d frames, want 2", got)
	}
	if j.Frames[0].Commits != 3 || j.Frames[1].Commits != 1 || j.Frames[1].Start.Month() != time.April {
		t.Errorf("frames %+v", j.Frames)
	}
}

func TestTestsCount(t *testing.T) {
	h := fixture()
	j := Build(h, codemap.Build(h), Commit, Options{Tests: true})
	if got := j.Frames[0].Live; got != 3 {
		t.Errorf("live %d with tests, want 3", got)
	}
}

func TestLayout(t *testing.T) {
	a, b := build(Week), build(Week)
	if !reflect.DeepEqual(a.Packages, b.Packages) {
		t.Fatalf("layout differs between builds:\n%+v\n%+v", a.Packages, b.Packages)
	}
	if w := build(Commit); !reflect.DeepEqual(w.Packages, a.Packages) {
		t.Errorf("layout depends on the step:\n%+v\n%+v", w.Packages, a.Packages)
	}
	ps := a.Packages
	for i, p := range ps {
		if p.X-p.R < -1e-9 || p.X+p.R > 1+1e-9 || p.Y-p.R < -1e-9 || p.Y+p.R > 1+1e-9 || p.R <= 0 {
			t.Errorf("%s outside the unit square: %+v", p.Path, p)
		}
		for _, q := range ps[i+1:] {
			if d := math.Hypot(p.X-q.X, p.Y-q.Y); d < p.R+q.R {
				t.Errorf("%s and %s overlap: distance %.3f, radii %.3f + %.3f", p.Path, q.Path, d, p.R, q.R)
			}
		}
	}
	if ps[0].Peak != 2 || ps[1].Peak != 1 || ps[2].Peak != 1 {
		t.Errorf("peaks %+v", ps)
	}
}

func TestOutOfOrderTimes(t *testing.T) {
	h := lineage.New()
	h.Apply(lineage.Revision{SHA: "a", Time: day("2026-03-10"), Changes: []ast.Change{{Flags: ast.Added, New: fn("a/a.go", "F")}}})
	h.Apply(lineage.Revision{SHA: "b", Time: day("2026-03-02"), Changes: []ast.Change{{Flags: ast.Added, New: fn("a/a.go", "G")}}})
	j := Build(h, codemap.Build(h), Week, Options{})
	if len(j.Frames) != 1 || j.Frames[0].Commits != 2 || j.Frames[0].Live != 2 {
		t.Errorf("frames %+v, want one week with both commits", j.Frames)
	}
}

func TestEmpty(t *testing.T) {
	h := lineage.New()
	if j := Build(h, codemap.Build(h), Week, Options{}); len(j.Frames) != 0 || len(j.Packages) != 0 {
		t.Errorf("%+v", j)
	}
}
