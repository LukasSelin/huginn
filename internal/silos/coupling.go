package silos

import (
	"fmt"
	"math"
	"slices"
	"strings"

	"huginn/internal/knowledge"
	"huginn/internal/lineage"
	"huginn/internal/semdiff"
)

// Pair is two units that are coupled, by references or by changing
// together, but known by different people: a change to one is likely to
// need someone who knows the other.
type Pair struct {
	A, B               Unit
	AKnowers, BKnowers []Knower // the top three of each

	Refs        int  // references between them, both ways
	Approximate bool // all of them matched by method name only

	// CoChanges counts the commits that changed both. Logical weighs each
	// of them by 1/(n-1) for the n units it changed, so a commit touching
	// everything says little, against the commits changing the less often
	// changed of the two: 1 when every change to it came with the other
	// alone. It is 0 below two co-changes.
	CoChanges int
	Logical   float64

	// Overlap is the cosine similarity of their knowers' weights: 0 when
	// nobody knows both, 1 when the same people know both alike.
	Overlap float64

	Score   float64 // (log2(1+Refs) + 3×Logical) × (1−Overlap)
	Reasons []string
}

const (
	maxOverlap     = 0.25 // nearly disjoint
	minCoChanges   = 2
	logicalWeight  = 3
	maxCommitSyms  = 20 // commits changing more symbols are left out of co-change, which also bounds its pairs
	maxCommitDirs  = 10 // and those changing more packages
	pairKnowersTop = 3
)

// link is the coupling between two units.
type link struct {
	refs  int
	exact bool // some references matched exactly
	co    int
	w     float64 // co-changes weighted by 1/(n-1)
}

func key(i, j int) [2]int { return [2]int{min(i, j), max(i, j)} }

// addLink counts use's references between units i and j.
func addLink(links map[[2]int]*link, i, j int, use semdiff.Use) {
	if i == j {
		return
	}
	l := get(links, key(i, j))
	l.refs += use.Calls
	l.exact = l.exact || !use.Approximate
}

func get(links map[[2]int]*link, k [2]int) *link {
	l := links[k]
	if l == nil {
		l = &link{}
		links[k] = l
	}
	return l
}

// couplings ranks the symbol and package pairs coupled across knowledge
// boundaries.
func (a *analysis) couplings(h *lineage.History) (syms, pkgs []Pair) {
	symRevs := make([][]int, h.Revisions()) // per revision, the live symbols it changed
	pkgRevs := make([][]int, h.Revisions())
	for i, u := range a.syms {
		for _, e := range h.Symbol(u.ID).Events {
			rev, ok := h.Position(e.SHA)
			if !ok || isTest(latestPath(&e)) {
				continue
			}
			symRevs[rev] = append(symRevs[rev], i)
			if !slices.Contains(pkgRevs[rev], u.pkg) {
				pkgRevs[rev] = append(pkgRevs[rev], u.pkg)
			}
		}
	}
	symChanges := cochange(symRevs, maxCommitSyms, a.symLinks)
	pkgChanges := cochange(pkgRevs, maxCommitDirs, a.pkgLinks)
	return a.pairs(a.syms, a.symLinks, symChanges), a.pairs(a.pkgs, a.pkgLinks, pkgChanges)
}

// cochange counts, for revisions changing at most limit units, how often
// each unit changed and how often each pair changed together.
func cochange(revs [][]int, limit int, links map[[2]int]*link) map[int]int {
	changes := map[int]int{}
	for _, us := range revs {
		n := len(us)
		if n == 0 || n > limit {
			continue
		}
		for _, u := range us {
			changes[u]++
		}
		for i := range us {
			for j := i + 1; j < n; j++ {
				l := get(links, key(us[i], us[j]))
				l.co++
				l.w += 1 / float64(n-1)
			}
		}
	}
	return changes
}

// pairs ranks the linked units of us that nearly nobody knows both of.
func (a *analysis) pairs(us []*unit, links map[[2]int]*link, changes map[int]int) []Pair {
	out := []Pair{}
	for k, l := range links {
		x, y := us[k[0]], us[k[1]]
		logical := 0.0
		if l.co >= minCoChanges {
			logical = min(1, l.w/float64(min(changes[k[0]], changes[k[1]])))
		}
		strength := math.Log2(1+float64(l.refs)) + logicalWeight*logical
		if strength <= 0 || knowledge.Total(x.ws) <= 0 || knowledge.Total(y.ws) <= 0 {
			continue
		}
		ov := overlap(x.ws, y.ws)
		if ov > maxOverlap {
			continue
		}
		p := Pair{A: x.Unit, B: y.Unit, Refs: l.refs, Approximate: l.refs > 0 && !l.exact,
			CoChanges: l.co, Logical: logical, Overlap: ov, Score: strength * (1 - ov)}
		p.AKnowers = a.spread(x.ws, a.departed).Knowers
		p.BKnowers = a.spread(y.ws, a.departed).Knowers
		p.AKnowers, p.BKnowers = p.AKnowers[:min(len(p.AKnowers), pairKnowersTop)], p.BKnowers[:min(len(p.BKnowers), pairKnowersTop)]
		p.Reasons = p.reasons()
		out = append(out, p)
	}
	sortBy(out, func(p Pair) (float64, string) { return p.Score, p.A.key() + "\x00" + p.B.key() })
	return out[:min(len(out), a.opts.Top)]
}

func (p Pair) reasons() []string {
	var out []string
	if p.Refs > 0 {
		r := plural(p.Refs, "call site") + " between them"
		if p.Approximate {
			r += " (approx.)"
		}
		out = append(out, r)
	}
	if p.Logical > 0 {
		out = append(out, fmt.Sprintf("changed together in %d commits (%s)", p.CoChanges, pct(p.Logical)))
	}
	return append(out, fmt.Sprintf("knowledge overlap %s: %s · %s", pct(p.Overlap), names(p.AKnowers), names(p.BKnowers)))
}

func names(ks []Knower) string {
	var out []string
	for _, k := range ks[:min(len(ks), 2)] {
		out = append(out, k.Name)
	}
	return strings.Join(out, ", ")
}

// overlap is the cosine similarity of two weight vectors sorted by author.
func overlap(x, y []knowledge.Weight) float64 {
	dot, nx, ny := 0.0, 0.0, 0.0
	for _, w := range x {
		nx += w.W * w.W
	}
	for _, w := range y {
		ny += w.W * w.W
	}
	for i, j := 0, 0; i < len(x) && j < len(y); {
		switch {
		case x[i].Author < y[j].Author:
			i++
		case x[i].Author > y[j].Author:
			j++
		default:
			dot += x[i].W * y[j].W
			i++
			j++
		}
	}
	if nx == 0 || ny == 0 {
		return 0
	}
	return min(1, dot/math.Sqrt(nx*ny))
}
