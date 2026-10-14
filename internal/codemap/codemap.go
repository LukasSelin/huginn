// Package codemap aggregates a lineage.History per package directory and per
// ISO week, for the code map: how big each directory is now, and how much of
// its code changed semantically when.
//
// Every event in the history is attributed to one directory: the directory
// of the symbol after the event, or before it for a deletion. A symbol moved
// across directories therefore counts as an edit of the directory it moved
// to only.
package codemap

import (
	"cmp"
	"iter"
	"math/bits"
	"path"
	"slices"
	"strings"
	"time"

	"huginn/internal/ast"
	"huginn/internal/lineage"
)

// NumKinds is the number of kinds of semantic edit, one per ast.ChangeFlags
// bit.
const NumKinds = 6

// Kinds counts edits per kind, indexed by the bit position of the event's
// primary flag: added, deleted, renamed, moved, signature, body.
type Kinds [NumKinds]int

// KindIndex returns the Kinds index of a single flag.
func KindIndex(f ast.ChangeFlags) int { return bits.TrailingZeros8(uint8(f)) }

// Of returns the count for a single flag.
func (k Kinds) Of(f ast.ChangeFlags) int { return k[KindIndex(f)] }

// Total returns the number of edits of any kind.
func (k Kinds) Total() int {
	n := 0
	for _, c := range k {
		n += c
	}
	return n
}

// Touch is one event, attributed to a directory.
type Touch struct {
	Symbol lineage.ID
	Event  *lineage.Event // shared with the history
	Kind   ast.ChangeFlags
	Test   bool // the symbol is declared in a _test.go file

	// ComplexityDelta is the change in cyclomatic complexity, counting a
	// missing side as 0.
	ComplexityDelta int
}

// Week is one ISO week of edits in a directory.
type Week struct {
	Start           time.Time // Monday 00:00 UTC
	Edits           Kinds
	Commits         int
	Authors         int
	ComplexityDelta int
}

// Current is a directory's live code after the last revision.
type Current struct {
	Symbols    int
	LOC        int // sum of the live symbols' line spans
	Complexity int
}

// Dir is the aggregate for one directory, or for the whole repository.
type Dir struct {
	Path    string
	touches []Touch // by time

	// Indexed by whether test files count.
	cur   [2]Current
	weeks [2][]Week
}

// Current returns the directory's live code, with or without test files.
func (d *Dir) Current(tests bool) Current { return d.cur[b2i(tests)] }

// Weeks returns the weeks the directory had edits in, oldest first. Weeks
// without edits are left out; the first one is when the directory appeared.
func (d *Dir) Weeks(tests bool) []Week { return d.weeks[b2i(tests)] }

// Touches yields the directory's events in [from, to), oldest first. A zero
// from or to leaves that end open.
func (d *Dir) Touches(from, to time.Time, tests bool) iter.Seq[Touch] {
	return func(yield func(Touch) bool) {
		i := 0
		if !from.IsZero() {
			i, _ = slices.BinarySearchFunc(d.touches, from, func(t Touch, x time.Time) int { return t.Event.Time.Compare(x) })
		}
		for ; i < len(d.touches); i++ {
			t := d.touches[i]
			if !to.IsZero() && !t.Event.Time.Before(to) {
				return
			}
			if (tests || !t.Test) && !yield(t) {
				return
			}
		}
	}
}

// Stats summarizes a directory's edits in a time window.
type Stats struct {
	Edits           Kinds
	Commits         int
	Authors         int
	ComplexityDelta int
	Last            time.Time // of the newest edit; zero without edits
	Hot             []Hot     // most edited symbols, most edits first
}

// Hot is a symbol and how often it was edited.
type Hot struct {
	Symbol lineage.ID
	Edits  int
}

// Window summarizes the edits in [from, to), keeping the hot most edited
// symbols.
func (d *Dir) Window(from, to time.Time, tests bool, hot int) Stats {
	var s Stats
	commits, authors := map[string]bool{}, map[string]bool{}
	per := map[lineage.ID]int{}
	for t := range d.Touches(from, to, tests) {
		s.Edits[KindIndex(t.Kind)]++
		s.ComplexityDelta += t.ComplexityDelta
		commits[t.Event.SHA] = true
		authors[t.Event.Author] = true
		per[t.Symbol]++
		if t.Event.Time.After(s.Last) {
			s.Last = t.Event.Time
		}
	}
	s.Commits, s.Authors = len(commits), len(authors)
	for id, n := range per {
		s.Hot = append(s.Hot, Hot{id, n})
	}
	slices.SortFunc(s.Hot, func(a, b Hot) int { return cmp.Or(cmp.Compare(b.Edits, a.Edits), cmp.Compare(a.Symbol, b.Symbol)) })
	if len(s.Hot) > hot {
		s.Hot = s.Hot[:hot]
	}
	return s
}

// Map is the aggregate of a whole history.
type Map struct {
	h    *lineage.History
	dirs map[string]*Dir
	repo *Dir
}

// Build aggregates h. h must not change afterwards.
func Build(h *lineage.History) *Map {
	m := &Map{h: h, dirs: map[string]*Dir{}, repo: &Dir{}}
	dir := func(p string) *Dir {
		d := m.dirs[p]
		if d == nil {
			d = &Dir{Path: p}
			m.dirs[p] = d
		}
		return d
	}
	for sym := range h.All() {
		for i := range sym.Events {
			e := &sym.Events[i]
			if e.Primary() == 0 {
				continue
			}
			s := e.New
			if s == nil {
				s = e.Old
			}
			t := Touch{Symbol: sym.ID, Event: e, Kind: e.Primary(), Test: isTest(s.Path), ComplexityDelta: complexity(e.New) - complexity(e.Old)}
			d := dir(path.Dir(s.Path))
			d.touches = append(d.touches, t)
			m.repo.touches = append(m.repo.touches, t)
		}
		if sym.Alive() {
			cur := sym.Latest()
			d := dir(path.Dir(cur.Path))
			for _, tests := range []bool{false, true} {
				if !tests && isTest(cur.Path) {
					continue
				}
				for _, c := range []*Current{&d.cur[b2i(tests)], &m.repo.cur[b2i(tests)]} {
					c.Symbols++
					c.LOC += cur.EndLine - cur.StartLine + 1
					c.Complexity += cur.Complexity
				}
			}
		}
	}
	for _, d := range m.dirs {
		d.finish()
	}
	m.repo.finish()
	return m
}

// finish sorts the touches and precomputes the weekly series.
func (d *Dir) finish() {
	// Author times are not monotonic along history, so sort explicitly.
	slices.SortStableFunc(d.touches, func(a, b Touch) int { return a.Event.Time.Compare(b.Event.Time) })
	for _, tests := range []bool{false, true} {
		var weeks []Week
		var commits, authors map[string]bool
		for t := range d.Touches(time.Time{}, time.Time{}, tests) {
			start := WeekOf(t.Event.Time)
			if len(weeks) == 0 || !weeks[len(weeks)-1].Start.Equal(start) {
				weeks = append(weeks, Week{Start: start})
				commits, authors = map[string]bool{}, map[string]bool{}
			}
			w := &weeks[len(weeks)-1]
			w.Edits[KindIndex(t.Kind)]++
			w.ComplexityDelta += t.ComplexityDelta
			commits[t.Event.SHA], authors[t.Event.Author] = true, true
			w.Commits, w.Authors = len(commits), len(authors)
		}
		d.weeks[b2i(tests)] = weeks
	}
}

// Dir returns the aggregate for one directory, or nil.
func (m *Map) Dir(p string) *Dir { return m.dirs[p] }

// Dirs returns every directory that ever held a symbol, sorted by path.
func (m *Map) Dirs() []*Dir {
	out := make([]*Dir, 0, len(m.dirs))
	for _, d := range m.dirs {
		out = append(out, d)
	}
	slices.SortFunc(out, func(a, b *Dir) int { return cmp.Compare(a.Path, b.Path) })
	return out
}

// Repo returns the aggregate over the whole repository. Its Path is empty.
func (m *Map) Repo() *Dir { return m.repo }

// Symbol returns the symbol with the given ID from the aggregated history.
func (m *Map) Symbol(id lineage.ID) *lineage.Symbol { return m.h.Symbol(id) }

// WeekOf returns the start of t's ISO week: Monday 00:00 UTC.
func WeekOf(t time.Time) time.Time {
	t = t.UTC()
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	return day.AddDate(0, 0, -(int(day.Weekday())+6)%7)
}

func complexity(s *ast.Symbol) int {
	if s == nil {
		return 0
	}
	return s.Complexity
}

func isTest(p string) bool { return strings.HasSuffix(p, "_test.go") }

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
