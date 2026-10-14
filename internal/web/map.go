package web

import (
	"net/http"
	"slices"
	"strconv"
	"time"

	"huginn/internal/ast"
	"huginn/internal/codemap"
	"huginn/internal/lineage"
)

// kindNames name the codemap.Kinds slots in JSON.
var kindNames = [codemap.NumKinds]string{}

func init() {
	for _, f := range []ast.ChangeFlags{ast.Added, ast.Deleted, ast.Renamed, ast.Moved, ast.SignatureChanged, ast.BodyChanged} {
		kindNames[codemap.KindIndex(f)] = f.String()
	}
}

func kindsJSON(k codemap.Kinds) map[string]int {
	out := map[string]int{}
	for i, n := range k {
		if n > 0 {
			out[kindNames[i]] = n
		}
	}
	return out
}

// mapQuery is the window and filters a map request asks for.
type mapQuery struct {
	from, to time.Time // zero for an open end
	tests    bool
}

func (s *Server) mapQuery(r *http.Request) mapQuery {
	q := r.URL.Query()
	unix := func(k string) time.Time {
		n, err := strconv.ParseInt(q.Get(k), 10, 64)
		if err != nil || n <= 0 {
			return time.Time{}
		}
		return time.Unix(n, 0)
	}
	// Tests can only be shown when the index kept them.
	return mapQuery{from: unix("from"), to: unix("to"), tests: s.opts.Tests && q.Get("tests") == "1"}
}

type tileJSON struct {
	Dir             string         `json:"dir"`
	Symbols         int            `json:"symbols"`
	LOC             int            `json:"loc"`
	Complexity      int            `json:"complexity"`
	Edits           int            `json:"edits"`
	Kinds           map[string]int `json:"kinds"`
	Commits         int            `json:"commits"`
	Authors         int            `json:"authors"`
	ComplexityDelta int            `json:"complexityDelta"`
	Last            int64          `json:"last,omitempty"` // unix seconds of the newest edit in the window
	Hot             []hotJSON      `json:"hot"`

	// With series=1: when the directory appeared, as a week index, and its
	// edits per week as [week, edits] pairs for weeks with edits.
	Born   *int     `json:"born,omitempty"`
	Series [][2]int `json:"series,omitempty"`
}

type hotJSON struct {
	ID    lineage.ID `json:"id"`
	Name  string     `json:"name"`
	Edits int        `json:"edits"`
}

func (ix *index) tile(d *codemap.Dir, q mapQuery, hot int) tileJSON {
	cur, st := d.Current(q.tests), d.Window(q.from, q.to, q.tests, hot)
	t := tileJSON{
		Dir: d.Path, Symbols: cur.Symbols, LOC: cur.LOC, Complexity: cur.Complexity,
		Edits: st.Edits.Total(), Kinds: kindsJSON(st.Edits), Commits: st.Commits, Authors: st.Authors,
		ComplexityDelta: st.ComplexityDelta, Hot: []hotJSON{},
	}
	if !st.Last.IsZero() {
		t.Last = st.Last.Unix()
	}
	for _, h := range st.Hot {
		t.Hot = append(t.Hot, hotJSON{ID: h.Symbol, Name: ix.cmap.Symbol(h.Symbol).Latest().QualifiedName(), Edits: h.Edits})
	}
	return t
}

// weekIndex numbers weeks from the repository's first one.
func (ix *index) weekIndex(w time.Time) int {
	first, _ := ix.h.Span()
	return int(w.Sub(codemap.WeekOf(first)).Hours() / (24 * 7))
}

type mapJSON struct {
	From  int64      `json:"from,omitempty"`
	To    int64      `json:"to,omitempty"`
	Tests bool       `json:"tests"`
	Can   filterJSON `json:"can"` // which filters the index supports
	Dirs  []tileJSON `json:"dirs"`

	// With series=1: the timeline's weeks, and commits with semantic
	// changes per week.
	WeekStart int64 `json:"weekStart,omitempty"` // unix seconds, Monday 00:00 UTC
	Weeks     int   `json:"weeks,omitempty"`
	Commits   []int `json:"commits,omitempty"`
}

type filterJSON struct {
	Tests     bool `json:"tests"`
	Generated bool `json:"generated"`
}

// codeMap serves one tile per directory with live code or edits, stats
// restricted to the ?from=&to= window (unix seconds, half-open).
func (s *Server) codeMap(ix *index, w http.ResponseWriter, r *http.Request) {
	q := s.mapQuery(r)
	// Generated code never reaches the index (git.Options.IncludeGenerated
	// is off), and symbols do not record it, so it cannot be toggled.
	out := mapJSON{Tests: q.tests, Can: filterJSON{Tests: s.opts.Tests}, Dirs: []tileJSON{}}
	if !q.from.IsZero() {
		out.From = q.from.Unix()
	}
	if !q.to.IsZero() {
		out.To = q.to.Unix()
	}
	series := r.URL.Query().Get("series") == "1"
	for _, d := range ix.cmap.Dirs() {
		weeks := d.Weeks(q.tests)
		if len(weeks) == 0 && d.Current(q.tests).Symbols == 0 {
			continue // only test code, and tests are off
		}
		t := ix.tile(d, q, 2)
		if series && len(weeks) > 0 {
			born := ix.weekIndex(weeks[0].Start)
			t.Born = &born
			for _, wk := range weeks {
				t.Series = append(t.Series, [2]int{ix.weekIndex(wk.Start), wk.Edits.Total()})
			}
		}
		out.Dirs = append(out.Dirs, t)
	}
	if series {
		first, last := ix.h.Span()
		out.WeekStart = codemap.WeekOf(first).Unix()
		out.Weeks = ix.weekIndex(codemap.WeekOf(last)) + 1
		out.Commits = make([]int, out.Weeks)
		for _, wk := range ix.cmap.Repo().Weeks(q.tests) {
			if i := ix.weekIndex(wk.Start); i >= 0 && i < out.Weeks {
				out.Commits[i] = wk.Commits
			}
		}
	}
	writeJSON(w, out)
}

type mapDirJSON struct {
	tileJSON
	Added   int          `json:"added"`
	Removed int          `json:"removed"`
	Recent  []recentJSON `json:"recent"` // newest first
}

type recentJSON struct {
	ID      lineage.ID `json:"id"`
	Name    string     `json:"name"`
	Kind    string     `json:"kind"`
	Summary string     `json:"summary"`
	T       int64      `json:"t"`
	SHA     string     `json:"sha"`
	Author  string     `json:"author"`
}

// mapDir serves the inspector for one directory: its tile with more hot
// symbols, and its most recent semantic changes in the window. The root
// directory is "/api/map/" or "/api/map/.".
func (s *Server) mapDir(ix *index, w http.ResponseWriter, r *http.Request) {
	p := r.PathValue("dir")
	if p == "" {
		p = "."
	}
	d := ix.cmap.Dir(p)
	if d == nil {
		http.Error(w, "no such directory", http.StatusNotFound)
		return
	}
	q := s.mapQuery(r)
	st := d.Window(q.from, q.to, q.tests, 0)
	out := mapDirJSON{tileJSON: ix.tile(d, q, 8), Added: st.Edits.Of(ast.Added), Removed: st.Edits.Of(ast.Deleted), Recent: []recentJSON{}}
	var recent []codemap.Touch
	for t := range d.Touches(q.from, q.to, q.tests) {
		recent = append(recent, t)
	}
	slices.Reverse(recent)
	for _, t := range recent[:min(len(recent), 30)] {
		out.Recent = append(out.Recent, recentJSON{
			ID: t.Symbol, Name: ix.cmap.Symbol(t.Symbol).Latest().QualifiedName(), Kind: t.Kind.String(),
			Summary: t.Event.Summary(), T: t.Event.Time.Unix(), SHA: t.Event.SHA, Author: t.Event.Author,
		})
	}
	writeJSON(w, out)
}
