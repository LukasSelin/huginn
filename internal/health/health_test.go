package health

import (
	"slices"
	"testing"
	"time"

	"huginn/internal/ast"
	"huginn/internal/codemap"
	"huginn/internal/lineage"
)

// fn declares a function spanning lines [start, end].
func fn(path, name string, start, end, complexity int) *ast.Symbol {
	exported := name[0] >= 'A' && name[0] <= 'Z'
	return &ast.Symbol{Kind: ast.KindFunc, Name: name, Key: name, Path: path, Exported: exported,
		StartLine: start, EndLine: end, Complexity: complexity}
}

var t0 = time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC)

// fixture is a small history across two packages:
//
//	a (ann): add pkg/a.F, pkg/a.g, pkg/a.TestF
//	b (bob): edit F (complexity 1 → 3), rename g → H
//	c (ann): add pkg/b.K, edit TestF
//	d (bob): move H to pkg/b, delete K, edit F (3 → 2)
func fixture() *lineage.History {
	f1, g, tf := fn("pkg/a/a.go", "F", 3, 10, 1), fn("pkg/a/a.go", "g", 12, 14, 1), fn("pkg/a/a_test.go", "TestF", 5, 9, 1)
	f2, hh := fn("pkg/a/a.go", "F", 3, 14, 3), fn("pkg/a/a.go", "H", 16, 18, 1)
	k, tf2 := fn("pkg/b/b.go", "K", 1, 4, 1), fn("pkg/a/a_test.go", "TestF", 5, 12, 2)
	f3, h2 := fn("pkg/a/a.go", "F", 3, 12, 2), fn("pkg/b/h.go", "H", 1, 3, 1)

	h := lineage.New()
	h.Apply(lineage.Revision{SHA: "a", Time: t0, Author: "ann", Changes: []ast.Change{
		{Flags: ast.Added, New: f1}, {Flags: ast.Added, New: g}, {Flags: ast.Added, New: tf},
	}})
	h.Apply(lineage.Revision{SHA: "b", Time: t0.Add(time.Hour), Author: "bob", Changes: []ast.Change{
		{Flags: ast.BodyChanged, Old: f1, New: f2}, {Flags: ast.Renamed, Old: g, New: hh},
	}})
	h.Apply(lineage.Revision{SHA: "c", Time: t0.Add(2 * time.Hour), Author: "ann", Changes: []ast.Change{
		{Flags: ast.Added, New: k}, {Flags: ast.BodyChanged, Old: tf, New: tf2},
	}})
	h.Apply(lineage.Revision{SHA: "d", Time: t0.Add(3 * time.Hour), Author: "bob", Changes: []ast.Change{
		{Flags: ast.Moved, Old: hh, New: h2}, {Flags: ast.Deleted, Old: k}, {Flags: ast.BodyChanged, Old: f2, New: f3},
	}})
	return h
}

func kinds(added, deleted, renamed, moved, signature, body int) codemap.Kinds {
	return codemap.Kinds{added, deleted, renamed, moved, signature, body}
}

func TestBucketsPerRevision(t *testing.T) {
	r := Build(fixture(), Options{})
	at := func(i int) time.Time { return t0.Add(time.Duration(i) * time.Hour) }
	want := []Bucket{
		{First: "a", Last: "a", From: at(0), To: at(0), Revisions: 1,
			Symbols: 2, Exported: 1, Lines: 11, Complexity: 2, Funcs: 2, TestLines: 5,
			Edits: kinds(2, 0, 0, 0, 0, 0), Added: 2, Surviving: 2}, // g lives on as H
		{First: "b", Last: "b", From: at(1), To: at(1), Revisions: 1,
			Symbols: 2, Exported: 2, Lines: 15, Complexity: 4, Funcs: 2, TestLines: 5,
			Edits: kinds(0, 0, 1, 0, 0, 1)},
		{First: "c", Last: "c", From: at(2), To: at(2), Revisions: 1,
			Symbols: 3, Exported: 3, Lines: 19, Complexity: 5, Funcs: 3, TestLines: 8,
			Edits: kinds(1, 0, 0, 0, 0, 0), Added: 1, Surviving: 0}, // K is deleted in d
		{First: "d", Last: "d", From: at(3), To: at(3), Revisions: 1,
			Symbols: 2, Exported: 2, Lines: 13, Complexity: 3, Funcs: 2, TestLines: 8,
			Edits: kinds(0, 1, 0, 1, 0, 1)},
	}
	if !slices.Equal(r.Buckets, want) {
		t.Errorf("buckets =\n%+v\nwant\n%+v", r.Buckets, want)
	}
	if got := []int{r.Buckets[1].Restructured(), r.Buckets[3].Restructured()}; !slices.Equal(got, []int{1, 1}) {
		t.Errorf("restructured = %v, want [1 1]", got)
	}
}

func TestBucketsMerge(t *testing.T) {
	r := Build(fixture(), Options{Buckets: 2})
	if len(r.Buckets) != 2 {
		t.Fatalf("%d buckets, want 2", len(r.Buckets))
	}
	b0, b1 := r.Buckets[0], r.Buckets[1]
	if b0.First != "a" || b0.Last != "b" || b0.Revisions != 2 || b1.First != "c" || b1.Last != "d" {
		t.Errorf("bucket ranges = %s..%s (%d), %s..%s", b0.First, b0.Last, b0.Revisions, b1.First, b1.Last)
	}
	// The state is taken after each bucket's last revision; edits add up.
	if b0.Symbols != 2 || b0.Complexity != 4 || b0.Edits != kinds(2, 0, 1, 0, 0, 1) || b0.Added != 2 {
		t.Errorf("first bucket = %+v", b0)
	}
	if b1.Symbols != 2 || b1.Complexity != 3 || b1.Edits != kinds(1, 1, 0, 1, 0, 1) || b1.Added != 1 || b1.Surviving != 0 {
		t.Errorf("second bucket = %+v", b1)
	}
}

func TestHotspots(t *testing.T) {
	h := fixture()
	r := Build(h, Options{})
	f := h.Lookup("pkg/a/a.go", "F")
	hh := h.Lookup("pkg/b/h.go", "H")
	want := []Hotspot{
		// F added and edited twice, g added and renamed; the move of H counts
		// for the file it moved to.
		{Path: "pkg/a/a.go", Edits: 5, Authors: 2, Complexity: 2, Lines: 10, Score: 10, Top: f.ID, TopName: "F", TopComplexity: 2},
		{Path: "pkg/b/h.go", Edits: 1, Authors: 1, Complexity: 1, Lines: 3, Score: 1, Top: hh.ID, TopName: "H", TopComplexity: 1},
		// pkg/b/b.go has edits but no live code, so no complexity.
	}
	if !slices.Equal(r.Hotspots, want) {
		t.Errorf("hotspots =\n%+v\nwant\n%+v", r.Hotspots, want)
	}
	if r := Build(h, Options{Hotspots: 1}); len(r.Hotspots) != 1 {
		t.Errorf("Hotspots: 1 kept %d", len(r.Hotspots))
	}
}

func TestOwners(t *testing.T) {
	r := Build(fixture(), Options{})
	if want := []Owner{{"bob", 2}}; !slices.Equal(r.Owners, want) {
		t.Errorf("owners = %+v, want %+v", r.Owners, want)
	}
	if r.Authors != 2 || r.BusFactor != 1 {
		t.Errorf("authors %d, bus factor %d; want 2, 1", r.Authors, r.BusFactor)
	}
}

func TestBusFactorNeedsMoreThanHalf(t *testing.T) {
	h := lineage.New()
	for i, who := range []string{"ann", "bob", "cy", "dee"} {
		h.Apply(lineage.Revision{SHA: who, Time: t0.Add(time.Duration(i) * time.Hour), Author: who, Changes: []ast.Change{
			{Flags: ast.Added, New: fn("p/p.go", "F"+who, 1, 2, 1)},
		}})
	}
	// Two of four authors cover exactly half, which is not more than half.
	if r := Build(h, Options{}); r.BusFactor != 3 {
		t.Errorf("bus factor = %d, want 3", r.BusFactor)
	}
}

func TestEmpty(t *testing.T) {
	r := Build(lineage.New(), Options{})
	if len(r.Buckets) != 0 || len(r.Hotspots) != 0 || len(r.Owners) != 0 || r.BusFactor != 0 {
		t.Errorf("empty history gave %+v", r)
	}
}
