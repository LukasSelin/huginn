// Package knowledge models who knows which live symbol of a history, and how
// well.
//
// Every live symbol keeps a weight per author: how much of it that author
// knows. Adding a symbol gives its author the symbol's full weight, one plus
// its statements. A body edit gives the editor the share of the symbol it
// rewrote, the statements it changed (at least one) against the size of the
// symbol, and takes the same share from everyone else. Renames, moves and
// signature-only edits add a little.
//
// Knowledge fades with a half-life, so weights are stored scaled up by
// 2^(t/HalfLife) from the first revision: comparing them compares weights
// decayed to any one moment, and nothing has to be decayed step by step.
//
// An author departs when they have not committed for AwayAfter; their
// knowledge is then orphaned until they commit again.
package knowledge

import (
	"cmp"
	"math"
	"slices"
	"time"

	"huginn/internal/ast"
	"huginn/internal/lineage"
)

const (
	HalfLife  = 182 * 24 * time.Hour // about six months
	AwayAfter = 182 * 24 * time.Hour
	Touch     = 0.5 // weight a rename, move or signature-only edit adds
)

// Weight is how much one author knows of something, scaled as the model
// stores it: only comparisons and shares are meaningful.
type Weight struct {
	Author int // index into Model.Authors
	W      float64
}

// Model tracks author weights per live symbol, fed one event at a time in
// the order the history applied them.
type Model struct {
	epoch   time.Time
	authors []string       // sorted
	index   map[string]int // author → index
	away    [][]span       // per author, when they count as departed

	weights map[lineage.ID][]Weight // per live symbol, by author
}

type span struct{ from, to time.Time } // to is zero for "still"

// New returns an empty model for h's authors and revisions.
func New(h *lineage.History) *Model {
	m := &Model{index: map[string]int{}, weights: map[lineage.ID][]Weight{}}
	n := h.Revisions()
	if n == 0 {
		return m
	}
	_, m.epoch = h.RevisionAt(0)
	commits := map[string][]time.Time{}
	for i := range n {
		if a := h.AuthorAt(i); a != "" {
			_, t := h.RevisionAt(i)
			commits[a] = append(commits[a], t)
		}
	}
	for a := range commits {
		m.authors = append(m.authors, a)
	}
	slices.Sort(m.authors)
	m.away = make([][]span, len(m.authors))
	for i, a := range m.authors {
		m.index[a] = i
		ts := commits[a]
		slices.SortFunc(ts, func(x, y time.Time) int { return x.Compare(y) })
		for j, t := range ts {
			from := t.Add(AwayAfter)
			if j+1 < len(ts) {
				if ts[j+1].After(from) {
					m.away[i] = append(m.away[i], span{from, ts[j+1]})
				}
				continue
			}
			m.away[i] = append(m.away[i], span{from: from})
		}
	}
	return m
}

// Replay returns the model after every event of h, counting the
// declarations counts accepts. Each symbol's knowledge depends only on its
// own events, so they are replayed symbol by symbol.
func Replay(h *lineage.History, counts func(*ast.Symbol) bool) *Model {
	m := New(h)
	for sym := range h.All() {
		var old *ast.Symbol
		for i := range sym.Events {
			e := &sym.Events[i]
			if _, ok := h.Position(e.SHA); !ok {
				continue
			}
			cur := e.New != nil && counts(e.New)
			m.Apply(sym.ID, e, old, cur)
			old = nil
			if cur {
				old = e.New
			}
		}
	}
	return m
}

// Authors returns every commit author, sorted; Weight.Author indexes it.
func (m *Model) Authors() []string { return m.authors }

// Author returns the index of the author called name.
func (m *Model) Author(name string) (int, bool) {
	a, ok := m.index[name]
	return a, ok
}

// Departed reports whether author a counts as departed at t: their latest
// commit at or before t is more than AwayAfter old.
func (m *Model) Departed(a int, t time.Time) bool {
	for _, s := range m.away[a] {
		if t.After(s.from) && (s.to.IsZero() || t.Before(s.to)) {
			return true
		}
	}
	return false
}

// Weights returns what each author knows of the live symbol id, sorted by
// author. The slice must not be modified.
func (m *Model) Weights(id lineage.ID) []Weight { return m.weights[id] }

// scale is the stored size of one unit of knowledge gained at t.
func (m *Model) scale(t time.Time) float64 {
	return math.Exp2(float64(t.Sub(m.epoch)) / float64(HalfLife))
}

// Apply updates the knowledge of symbol id for event e. old is the symbol's
// counted declaration before e, nil if it was not live; e.New counts only
// when cur is true.
func (m *Model) Apply(id lineage.ID, e *lineage.Event, old *ast.Symbol, cur bool) {
	if !cur {
		delete(m.weights, id)
		return
	}
	a, ok := m.index[e.Author]
	if !ok {
		if old == nil {
			delete(m.weights, id)
		}
		return
	}
	s := m.scale(e.Time)
	full := float64(1 + e.New.Stmts)
	ws := m.weights[id]
	switch {
	case old == nil:
		ws = []Weight{{a, full * s}}
	case e.Flags.Has(ast.BodyChanged):
		ws = rewrite(ws, a, bodyShare(old, e.New), full*s)
	case e.Flags.Has(ast.Renamed) || e.Flags.Has(ast.Moved) || e.Flags.Has(ast.SignatureChanged):
		ws = gain(ws, a, Touch*s)
	}
	m.weights[id] = ws
}

// bodyShare is the share of a symbol a body edit rewrote: the statements
// it changed, at least one, against the size of the symbol.
func bodyShare(old, cur *ast.Symbol) float64 {
	d := max(1, abs(cur.Stmts-old.Stmts))
	return min(1, float64(d)/float64(max(1, old.Stmts, cur.Stmts)))
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// rewrite scales everyone but author a down by share f and gives a f of
// full.
func rewrite(ws []Weight, a int, f, full float64) []Weight {
	out := ws[:0:0]
	for _, w := range ws {
		if w.Author != a {
			w.W *= 1 - f
		}
		if w.W > 0 {
			out = append(out, w)
		}
	}
	return gain(out, a, f*full)
}

// gain adds x to author a's weight, keeping ws sorted by author.
func gain(ws []Weight, a int, x float64) []Weight {
	i, found := slices.BinarySearchFunc(ws, a, func(w Weight, a int) int { return cmp.Compare(w.Author, a) })
	if found {
		out := slices.Clone(ws)
		out[i].W += x
		return out
	}
	return slices.Insert(slices.Clone(ws), i, Weight{a, x})
}

// Sum adds ws into by, per author.
func Sum(by map[int]float64, ws []Weight) {
	for _, w := range ws {
		by[w.Author] += w.W
	}
}

// Rank returns the weights in by, most first, then by author.
func Rank(by map[int]float64) []Weight {
	out := make([]Weight, 0, len(by))
	for a, w := range by {
		out = append(out, Weight{a, w})
	}
	return Ranked(out)
}

// Ranked sorts ws most first, then by author, in place, and returns it.
func Ranked(ws []Weight) []Weight {
	slices.SortFunc(ws, func(x, y Weight) int { return cmp.Or(cmp.Compare(y.W, x.W), cmp.Compare(x.Author, y.Author)) })
	return ws
}

// Total is the sum of ws.
func Total(ws []Weight) float64 {
	t := 0.0
	for _, w := range ws {
		t += w.W
	}
	return t
}

// BusFactor is the fewest of ranked, most first, who together hold more
// than half of their total; 0 when they hold nothing.
func BusFactor(ranked []Weight) int {
	total := Total(ranked)
	if total <= 0 {
		return 0
	}
	held, bus := 0.0, 0
	for _, w := range ranked {
		if held > total/2 {
			break
		}
		held += w.W
		bus++
	}
	return bus
}
