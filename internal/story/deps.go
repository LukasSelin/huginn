package story

import (
	"math"
	"slices"

	"huginn/internal/journey"
)

// depMoments finds where a package starts or stops depending on another,
// and where the first dependency cycle appears or the last one goes. A
// package that is born or dies in the commit is left to its birth or death.
func depMoments(j *journey.Journey, pkgs []string, l life) []Moment {
	var out []Moment
	prev := map[[2]int]int{}
	cyclic := false
	for rev, f := range j.Frames {
		cur := depsOf(f)
		changed := len(cur) != len(prev)
		if rev > 0 {
			ms, diff := depChanges(f, rev, pkgs, l, prev, cur)
			out = append(out, ms...)
			changed = changed || diff
		}
		prev = cur
		if !changed && rev > 0 {
			continue
		}
		cycle := firstCycle(f.Deps, len(pkgs))
		if m, ok := cycleMoment(f, rev, pkgs, cycle, cyclic); ok {
			out = append(out, m)
		}
		cyclic = cycle != nil
	}
	return out
}

// depsOf returns the references of a frame's dependencies, by from and to.
func depsOf(f journey.Frame) map[[2]int]int {
	cur := make(map[[2]int]int, len(f.Deps))
	for _, d := range f.Deps {
		cur[[2]int{d.From, d.To}] = d.Refs
	}
	return cur
}

// depChanges finds the dependencies frame f of revision rev added to prev
// and removed from it, leaving cur, and reports whether there were any.
// Those of packages born or dying in the revision make no moment.
func depChanges(f journey.Frame, rev int, pkgs []string, l life, prev, cur map[[2]int]int) (out []Moment, changed bool) {
	skip := bornOrDied(l, rev)
	for _, d := range f.Deps {
		k := [2]int{d.From, d.To}
		if _, ok := prev[k]; ok {
			continue
		}
		changed = true
		if !skip[d.From] && !skip[d.To] {
			out = append(out, depMoment(DepAdded, f, rev, pkgs, k, d.Refs))
		}
	}
	for k, n := range prev {
		if _, ok := cur[k]; ok {
			continue
		}
		changed = true
		if !skip[k[0]] && !skip[k[1]] {
			out = append(out, depMoment(DepRemoved, f, rev, pkgs, k, n))
		}
	}
	return out, changed
}

// bornOrDied returns the packages born or dying in revision rev.
func bornOrDied(l life, rev int) map[int]bool {
	skip := map[int]bool{}
	for _, p := range l.born[rev] {
		skip[p] = true
	}
	for _, p := range l.died[rev] {
		skip[p] = true
	}
	return skip
}

func depMoment(k MomentKind, f journey.Frame, rev int, pkgs []string, d [2]int, refs int) Moment {
	verb := " starts depending on "
	if k == DepRemoved {
		verb = " stops depending on "
	}
	return Moment{Kind: k, Time: f.Start, Rev: rev, SHA: f.SHA, Package: pkgs[d[0]], Count: refs,
		Text: pkgName(pkgs[d[0]]) + verb + pkgName(pkgs[d[1]]), Weight: 1.2 + 0.5*math.Log1p(float64(refs))}
}

// cycleMoment tells of a dependency cycle appearing in frame f of revision
// rev when there was none before, cyclic being whether there was, or of the
// last one going. It reports false when neither happened.
func cycleMoment(f journey.Frame, rev int, pkgs []string, cycle []int, cyclic bool) (Moment, bool) {
	switch {
	case cycle != nil && !cyclic:
		ns := make([]string, len(cycle))
		for i, p := range cycle {
			ns[i] = pkgs[p]
		}
		return Moment{Kind: CycleIn, Time: f.Start, Rev: rev, SHA: f.SHA, Package: ns[0], Count: len(ns),
			Text: "A dependency cycle appears between " + names(ns), Weight: 3.5}, true
	case cycle == nil && cyclic:
		return Moment{Kind: CycleOut, Time: f.Start, Rev: rev, SHA: f.SHA,
			Text: "No dependency cycle remains", Weight: 3.5}, true
	}
	return Moment{}, false
}

// firstCycle returns the packages of a strongly connected component of more
// than one package, sorted, the one holding the lowest package index; nil
// if the graph has no cycle.
func firstCycle(deps []journey.Dep, n int) []int {
	t := newTarjan(deps, n)
	for v := range n {
		if t.index[v] < 0 && len(t.adj[v]) > 0 {
			t.visit(v)
		}
	}
	return t.best
}

// tarjan finds the strongly connected components of a dependency graph
// with Tarjan's algorithm, and keeps the cycle firstCycle returns.
type tarjan struct {
	adj        [][]int // the packages each depends on, itself left out
	index, low []int   // -1 in index for packages not visited yet
	on         []bool  // whether a package is on the stack
	stack      []int
	next       int // the index of the next package visited
	best       []int
}

// newTarjan readies a search of the dependency graph of n packages.
func newTarjan(deps []journey.Dep, n int) *tarjan {
	t := &tarjan{adj: make([][]int, n), index: make([]int, n), low: make([]int, n), on: make([]bool, n)}
	for _, d := range deps {
		if d.From != d.To {
			t.adj[d.From] = append(t.adj[d.From], d.To)
		}
	}
	for i := range t.index {
		t.index[i] = -1
	}
	return t
}

// visit searches from v, and keeps the component v is the root of once
// everything it reaches has been searched.
func (t *tarjan) visit(v int) {
	t.index[v], t.low[v] = t.next, t.next
	t.next++
	t.stack = append(t.stack, v)
	t.on[v] = true
	for _, w := range t.adj[v] {
		switch {
		case t.index[w] < 0:
			t.visit(w)
			t.low[v] = min(t.low[v], t.low[w])
		case t.on[w]:
			t.low[v] = min(t.low[v], t.index[w])
		}
	}
	if t.low[v] != t.index[v] {
		return
	}
	t.keep(t.pop(v))
}

// pop takes the component rooted at v off the stack.
func (t *tarjan) pop(v int) []int {
	var scc []int
	for {
		w := t.stack[len(t.stack)-1]
		t.stack = t.stack[:len(t.stack)-1]
		t.on[w] = false
		scc = append(scc, w)
		if w == v {
			return scc
		}
	}
}

// keep keeps a component of more than one package, sorted, when it holds a
// lower package than the one kept so far.
func (t *tarjan) keep(scc []int) {
	if len(scc) < 2 {
		return
	}
	slices.Sort(scc)
	if t.best == nil || scc[0] < t.best[0] {
		t.best = scc
	}
}
