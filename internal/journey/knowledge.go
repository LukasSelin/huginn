package journey

import (
	"cmp"
	"math"
	"path"
	"slices"
	"time"

	"huginn/internal/ast"
	"huginn/internal/lineage"
)

// Knowledge model.
//
// Every live symbol keeps a weight per author: how much of it that author
// knows. Adding a symbol gives its author the symbol's full weight, one plus
// its statements. A body edit gives the editor the share of the symbol it
// rewrote, the statements it changed (at least one) against the size of the
// symbol, and takes the same share from everyone else. Renames, moves and
// signature-only edits add a little.
//
// Knowledge fades with a half-life, so weights are stored scaled up by
// 2^(t/halfLife) from the first revision: comparing them compares weights
// decayed to any one moment, and nothing has to be decayed frame by frame.
//
// An author departs when they have not committed for awayAfter; their
// knowledge is then orphaned until they commit again.
const (
	halfLife  = 182 * 24 * time.Hour // about six months
	awayAfter = 182 * 24 * time.Hour
	touch     = 0.5 // weight a rename, move or signature-only edit adds

	// maxKnowers is how many knowers a package state lists.
	maxKnowers = 5
)

// Knower is an author's share of what is known of a package.
type Knower struct {
	Author   int     // index into Journey.Authors
	Share    float64 // of the package's knowledge, 0 to 1
	Departed bool    // no commits for six months at the end of the frame
}

// knowledge tracks author weights during a replay.
type knowledge struct {
	epoch   time.Time
	authors []string       // sorted
	index   map[string]int // author → index
	away    [][]span       // per author, when they count as departed

	weights map[lineage.ID][]weight // per live symbol, by author
	inDir   map[string]map[lineage.ID]bool
	dirty   map[string]bool
	sums    map[string][]weight // per directory, by author, as of the last snapshot
}

type weight struct {
	author int
	w      float64
}

type span struct{ from, to time.Time } // to is zero for "still"

func newKnowledge(h *lineage.History) *knowledge {
	k := &knowledge{index: map[string]int{}, weights: map[lineage.ID][]weight{}, inDir: map[string]map[lineage.ID]bool{},
		dirty: map[string]bool{}, sums: map[string][]weight{}}
	n := h.Revisions()
	if n == 0 {
		return k
	}
	_, k.epoch = h.RevisionAt(0)
	commits := map[string][]time.Time{}
	for i := range n {
		if a := h.AuthorAt(i); a != "" {
			_, t := h.RevisionAt(i)
			commits[a] = append(commits[a], t)
		}
	}
	for a := range commits {
		k.authors = append(k.authors, a)
	}
	slices.Sort(k.authors)
	k.away = make([][]span, len(k.authors))
	for i, a := range k.authors {
		k.index[a] = i
		ts := commits[a]
		slices.SortFunc(ts, func(x, y time.Time) int { return x.Compare(y) })
		for j, t := range ts {
			from := t.Add(awayAfter)
			if j+1 < len(ts) {
				if ts[j+1].After(from) {
					k.away[i] = append(k.away[i], span{from, ts[j+1]})
				}
				continue
			}
			k.away[i] = append(k.away[i], span{from: from})
		}
	}
	return k
}

// departed reports whether author a counts as departed at t: their latest
// commit at or before t is more than awayAfter old.
func (k *knowledge) departed(a int, t time.Time) bool {
	for _, s := range k.away[a] {
		if t.After(s.from) && (s.to.IsZero() || t.Before(s.to)) {
			return true
		}
	}
	return false
}

// scale is the stored size of one unit of knowledge gained at t.
func (k *knowledge) scale(t time.Time) float64 {
	return math.Exp2(float64(t.Sub(k.epoch)) / float64(halfLife))
}

// apply updates the knowledge of symbol id for event e. old is the symbol's
// counted declaration before e, nil if it was not live; e.New counts only
// when cur is true.
func (k *knowledge) apply(id lineage.ID, e *lineage.Event, old *ast.Symbol, cur bool) {
	if old != nil {
		k.leave(id, path.Dir(old.Path))
	}
	if !cur {
		delete(k.weights, id)
		return
	}
	dir := path.Dir(e.New.Path)
	if k.inDir[dir] == nil {
		k.inDir[dir] = map[lineage.ID]bool{}
	}
	k.inDir[dir][id] = true
	k.dirty[dir] = true

	a, ok := k.index[e.Author]
	if !ok {
		if old == nil {
			delete(k.weights, id)
		}
		return
	}
	s := k.scale(e.Time)
	full := float64(1 + e.New.Stmts)
	ws := k.weights[id]
	switch {
	case old == nil:
		ws = []weight{{a, full * s}}
	case e.Flags.Has(ast.BodyChanged):
		ws = rewrite(ws, a, bodyShare(old, e.New), full*s)
	case e.Flags.Has(ast.Renamed) || e.Flags.Has(ast.Moved) || e.Flags.Has(ast.SignatureChanged):
		ws = gain(ws, a, touch*s)
	}
	k.weights[id] = ws
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
func rewrite(ws []weight, a int, f, full float64) []weight {
	out := ws[:0:0]
	for _, w := range ws {
		if w.author != a {
			w.w *= 1 - f
		}
		if w.w > 0 {
			out = append(out, w)
		}
	}
	return gain(out, a, f*full)
}

// gain adds x to author a's weight, keeping ws sorted by author.
func gain(ws []weight, a int, x float64) []weight {
	i, found := slices.BinarySearchFunc(ws, a, func(w weight, a int) int { return cmp.Compare(w.author, a) })
	if found {
		out := slices.Clone(ws)
		out[i].w += x
		return out
	}
	return slices.Insert(slices.Clone(ws), i, weight{a, x})
}

func (k *knowledge) leave(id lineage.ID, dir string) {
	delete(k.inDir[dir], id)
	k.dirty[dir] = true
}

// state returns a directory's knowers, orphaned share and bus factor at t.
// Directories whose symbols changed since the last call are summed again,
// symbol by symbol in ID order so the result does not depend on map order.
func (k *knowledge) state(dir string, t time.Time) (knowers []Knower, orphaned float64, bus int) {
	if k.dirty[dir] {
		delete(k.dirty, dir)
		ids := make([]lineage.ID, 0, len(k.inDir[dir]))
		for id := range k.inDir[dir] {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		by := map[int]float64{}
		for _, id := range ids {
			for _, w := range k.weights[id] {
				by[w.author] += w.w
			}
		}
		sum := make([]weight, 0, len(by))
		for a, w := range by {
			sum = append(sum, weight{a, w})
		}
		slices.SortFunc(sum, func(x, y weight) int { return cmp.Or(cmp.Compare(y.w, x.w), cmp.Compare(x.author, y.author)) })
		k.sums[dir] = sum
	}
	sum := k.sums[dir]
	total := 0.0
	for _, w := range sum {
		total += w.w
	}
	if total <= 0 {
		return nil, 0, 0
	}
	held := 0.0
	for i, w := range sum {
		share := w.w / total
		gone := k.departed(w.author, t)
		if gone {
			orphaned += share
		}
		if i < maxKnowers {
			knowers = append(knowers, Knower{Author: w.author, Share: share, Departed: gone})
		}
		if held <= 0.5 {
			held += share
			bus++
		}
	}
	return knowers, min(1, orphaned), bus
}
