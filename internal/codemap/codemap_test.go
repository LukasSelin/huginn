package codemap

import (
	"slices"
	"testing"
	"time"

	"huginn/internal/ast"
	"huginn/internal/lineage"
)

// sym declares a function spanning lines [start, end].
func sym(path, name string, start, end, complexity int) *ast.Symbol {
	return &ast.Symbol{Kind: ast.KindFunc, Name: name, Key: name, Path: path, StartLine: start, EndLine: end, Complexity: complexity}
}

func day(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t.Add(12 * time.Hour)
}

// fixture is a small history across two packages:
//
//	2026-03-02 (Mon, W10) a: add pkg/a.F, pkg/a.G, pkg/a.TestF
//	2026-03-04 (Wed, W10) b: edit F (complexity 1 → 3), rename G → H     (bob)
//	2026-03-09 (Mon, W11) c: add pkg/b.K, edit TestF                    (ann)
//	2026-03-10 (Tue, W11) d: move H to pkg/b, delete K, edit F (3 → 2)  (bob)
func fixture() *lineage.History {
	f1, g, tf := sym("pkg/a/a.go", "F", 3, 10, 1), sym("pkg/a/a.go", "G", 12, 14, 1), sym("pkg/a/a_test.go", "TestF", 5, 9, 1)
	f2, hh := sym("pkg/a/a.go", "F", 3, 14, 3), sym("pkg/a/a.go", "H", 16, 18, 1)
	k, tf2 := sym("pkg/b/b.go", "K", 1, 4, 1), sym("pkg/a/a_test.go", "TestF", 5, 12, 2)
	f3, h2 := sym("pkg/a/a.go", "F", 3, 12, 2), sym("pkg/b/h.go", "H", 1, 3, 1)

	h := lineage.New()
	h.Apply(lineage.Revision{SHA: "a", Time: day("2026-03-02"), Author: "ann", Changes: []ast.Change{
		{Flags: ast.Added, New: f1}, {Flags: ast.Added, New: g}, {Flags: ast.Added, New: tf},
	}})
	h.Apply(lineage.Revision{SHA: "b", Time: day("2026-03-04"), Author: "bob", Changes: []ast.Change{
		{Flags: ast.BodyChanged, Old: f1, New: f2}, {Flags: ast.Renamed, Old: g, New: hh},
	}})
	h.Apply(lineage.Revision{SHA: "c", Time: day("2026-03-09"), Author: "ann", Changes: []ast.Change{
		{Flags: ast.Added, New: k}, {Flags: ast.BodyChanged, Old: tf, New: tf2},
	}})
	h.Apply(lineage.Revision{SHA: "d", Time: day("2026-03-10"), Author: "bob", Changes: []ast.Change{
		{Flags: ast.Moved, Old: hh, New: h2}, {Flags: ast.Deleted, Old: k}, {Flags: ast.BodyChanged, Old: f2, New: f3},
	}})
	return h
}

func kinds(added, deleted, renamed, moved, signature, body int) Kinds {
	return Kinds{added, deleted, renamed, moved, signature, body}
}

func TestWeekOf(t *testing.T) {
	for in, want := range map[string]string{
		"2026-03-02": "2026-03-02", // Monday
		"2026-03-08": "2026-03-02", // Sunday ends the ISO week
		"2026-03-09": "2026-03-09",
		"2026-01-01": "2025-12-29", // week 1 of 2026 starts in 2025
	} {
		if got := WeekOf(day(in)).Format("2006-01-02"); got != want {
			t.Errorf("WeekOf(%s) = %s, want %s", in, got, want)
		}
	}
	// Times are bucketed in UTC whatever their zone.
	late := time.Date(2026, 3, 8, 23, 30, 0, 0, time.FixedZone("", -2*3600)) // Mon 01:30 UTC
	if got := WeekOf(late).Format("2006-01-02"); got != "2026-03-09" {
		t.Errorf("WeekOf(%v) = %s, want 2026-03-09", late, got)
	}
}

func TestDirs(t *testing.T) {
	m := Build(fixture())
	var got []string
	for _, d := range m.Dirs() {
		got = append(got, d.Path)
	}
	if want := []string{"pkg/a", "pkg/b"}; !slices.Equal(got, want) {
		t.Fatalf("Dirs = %q, want %q", got, want)
	}
	if m.Dir("pkg/c") != nil {
		t.Error(`Dir("pkg/c") != nil`)
	}
}

func TestCurrent(t *testing.T) {
	m := Build(fixture())
	for _, tc := range []struct {
		dir   string
		tests bool
		want  Current
	}{
		{"pkg/a", false, Current{Symbols: 1, LOC: 10, Complexity: 2}}, // F
		{"pkg/a", true, Current{Symbols: 2, LOC: 18, Complexity: 4}},  // F, TestF
		{"pkg/b", false, Current{Symbols: 1, LOC: 3, Complexity: 1}},  // H; K is deleted
		{"", true, Current{Symbols: 3, LOC: 21, Complexity: 5}},       // whole repo
		{"", false, Current{Symbols: 2, LOC: 13, Complexity: 3}},      // whole repo without tests
	} {
		d := m.Repo()
		if tc.dir != "" {
			d = m.Dir(tc.dir)
		}
		if got := d.Current(tc.tests); got != tc.want {
			t.Errorf("%q Current(tests=%v) = %+v, want %+v", tc.dir, tc.tests, got, tc.want)
		}
	}
}

func TestWeeks(t *testing.T) {
	m := Build(fixture())
	w10, w11 := day("2026-03-02").Add(-12*time.Hour), day("2026-03-09").Add(-12*time.Hour)
	for _, tc := range []struct {
		dir   string
		tests bool
		want  []Week
	}{
		{"pkg/a", true, []Week{
			{Start: w10, Edits: kinds(3, 0, 1, 0, 0, 1), Commits: 2, Authors: 2, ComplexityDelta: 1 + 1 + 1 + 2},
			{Start: w11, Edits: kinds(0, 0, 0, 0, 0, 2), Commits: 2, Authors: 2, ComplexityDelta: 1 - 1},
		}},
		{"pkg/a", false, []Week{
			{Start: w10, Edits: kinds(2, 0, 1, 0, 0, 1), Commits: 2, Authors: 2, ComplexityDelta: 1 + 1 + 2},
			{Start: w11, Edits: kinds(0, 0, 0, 0, 0, 1), Commits: 1, Authors: 1, ComplexityDelta: -1},
		}},
		// The move lands in pkg/b, the directory H moved to; pkg/b is born in W11.
		{"pkg/b", false, []Week{
			{Start: w11, Edits: kinds(1, 1, 0, 1, 0, 0), Commits: 2, Authors: 2, ComplexityDelta: 1 - 1},
		}},
		{"", false, []Week{
			{Start: w10, Edits: kinds(2, 0, 1, 0, 0, 1), Commits: 2, Authors: 2, ComplexityDelta: 4},
			{Start: w11, Edits: kinds(1, 1, 0, 1, 0, 1), Commits: 2, Authors: 2, ComplexityDelta: -1},
		}},
	} {
		d := m.Repo()
		if tc.dir != "" {
			d = m.Dir(tc.dir)
		}
		got := d.Weeks(tc.tests)
		if !slices.EqualFunc(got, tc.want, func(a, b Week) bool {
			return a.Start.Equal(b.Start) && a.Edits == b.Edits && a.Commits == b.Commits && a.Authors == b.Authors && a.ComplexityDelta == b.ComplexityDelta
		}) {
			t.Errorf("%q Weeks(tests=%v) =\n%+v\nwant\n%+v", tc.dir, tc.tests, got, tc.want)
		}
	}
}

func TestWindow(t *testing.T) {
	h := fixture()
	m := Build(h)
	a := m.Dir("pkg/a")
	f := h.Lookup("pkg/a/a.go", "F").ID

	all := a.Window(time.Time{}, time.Time{}, true, 2)
	if want := kinds(3, 0, 1, 0, 0, 3); all.Edits != want {
		t.Errorf("all Edits = %v, want %v", all.Edits, want)
	}
	if all.Commits != 4 || all.Authors != 2 || all.ComplexityDelta != 5 || !all.Last.Equal(day("2026-03-10")) {
		t.Errorf("all = %+v", all)
	}
	if len(all.Hot) != 2 || all.Hot[0] != (Hot{f, 3}) || all.Hot[1].Edits != 2 {
		t.Errorf("all Hot = %+v, want F×3 first, then a symbol with 2 edits", all.Hot)
	}

	// [from, to) is half-open: commit d at noon on the 10th falls outside.
	w := a.Window(day("2026-03-04"), day("2026-03-10"), false, 5)
	if want := kinds(0, 0, 1, 0, 0, 1); w.Edits != want || w.Commits != 1 || w.Authors != 1 {
		t.Errorf("window = %+v, want one rename and one body edit by bob", w)
	}
	if !w.Last.Equal(day("2026-03-04")) {
		t.Errorf("window Last = %v", w.Last)
	}

	empty := a.Window(day("2027-01-01"), time.Time{}, true, 5)
	if empty.Edits.Total() != 0 || empty.Commits != 0 || !empty.Last.IsZero() || len(empty.Hot) != 0 {
		t.Errorf("empty window = %+v", empty)
	}
}

func TestTouchesSortedByTime(t *testing.T) {
	// Author times can go backwards along history, e.g. after a rebase.
	h := lineage.New()
	x, y := sym("p/x.go", "X", 1, 2, 1), sym("p/x.go", "Y", 4, 5, 1)
	h.Apply(lineage.Revision{SHA: "1", Time: day("2026-05-20"), Author: "ann", Changes: []ast.Change{{Flags: ast.Added, New: x}}})
	h.Apply(lineage.Revision{SHA: "2", Time: day("2026-05-01"), Author: "ann", Changes: []ast.Change{{Flags: ast.Added, New: y}}})
	d := Build(h).Dir("p")
	var shas []string
	for tc := range d.Touches(time.Time{}, time.Time{}, false) {
		shas = append(shas, tc.Event.SHA)
	}
	if !slices.Equal(shas, []string{"2", "1"}) {
		t.Errorf("touch order = %q, want oldest first", shas)
	}
	if ws := d.Weeks(false); len(ws) != 2 || !ws[0].Start.Before(ws[1].Start) {
		t.Errorf("weeks = %+v, want two, oldest first", ws)
	}
	if n := d.Window(day("2026-05-10"), time.Time{}, false, 1).Edits.Total(); n != 1 {
		t.Errorf("edits since 2026-05-10 = %d, want 1", n)
	}
}
