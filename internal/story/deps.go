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
		cur := make(map[[2]int]int, len(f.Deps))
		for _, d := range f.Deps {
			cur[[2]int{d.From, d.To}] = d.Refs
		}
		changed := len(cur) != len(prev)
		if rev > 0 {
			skip := map[int]bool{}
			for _, p := range l.born[rev] {
				skip[p] = true
			}
			for _, p := range l.died[rev] {
				skip[p] = true
			}
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
		}
		prev = cur
		if !changed && rev > 0 {
			continue
		}
		cycle := firstCycle(f.Deps, len(pkgs))
		switch {
		case cycle != nil && !cyclic:
			ns := make([]string, len(cycle))
			for i, p := range cycle {
				ns[i] = pkgs[p]
			}
			out = append(out, Moment{Kind: CycleIn, Time: f.Start, Rev: rev, SHA: f.SHA, Package: ns[0], Count: len(ns),
				Text: "A dependency cycle appears between " + names(ns), Weight: 3.5})
		case cycle == nil && cyclic:
			out = append(out, Moment{Kind: CycleOut, Time: f.Start, Rev: rev, SHA: f.SHA,
				Text: "No dependency cycle remains", Weight: 3.5})
		}
		cyclic = cycle != nil
	}
	return out
}

func depMoment(k MomentKind, f journey.Frame, rev int, pkgs []string, d [2]int, refs int) Moment {
	verb := " starts depending on "
	if k == DepRemoved {
		verb = " stops depending on "
	}
	return Moment{Kind: k, Time: f.Start, Rev: rev, SHA: f.SHA, Package: pkgs[d[0]], Count: refs,
		Text: pkgName(pkgs[d[0]]) + verb + pkgName(pkgs[d[1]]), Weight: 1.2 + 0.5*math.Log1p(float64(refs))}
}

// firstCycle returns the packages of a strongly connected component of more
// than one package, sorted, the one holding the lowest package index; nil
// if the graph has no cycle.
func firstCycle(deps []journey.Dep, n int) []int {
	adj := make([][]int, n)
	for _, d := range deps {
		if d.From != d.To {
			adj[d.From] = append(adj[d.From], d.To)
		}
	}
	// Tarjan's algorithm.
	index := make([]int, n)
	low := make([]int, n)
	on := make([]bool, n)
	for i := range index {
		index[i] = -1
	}
	var stack []int
	var best []int
	next := 0
	var visit func(v int)
	visit = func(v int) {
		index[v], low[v] = next, next
		next++
		stack = append(stack, v)
		on[v] = true
		for _, w := range adj[v] {
			switch {
			case index[w] < 0:
				visit(w)
				low[v] = min(low[v], low[w])
			case on[w]:
				low[v] = min(low[v], index[w])
			}
		}
		if low[v] != index[v] {
			return
		}
		var scc []int
		for {
			w := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			on[w] = false
			scc = append(scc, w)
			if w == v {
				break
			}
		}
		if len(scc) > 1 {
			slices.Sort(scc)
			if best == nil || scc[0] < best[0] {
				best = scc
			}
		}
	}
	for v := range n {
		if index[v] < 0 && len(adj[v]) > 0 {
			visit(v)
		}
	}
	return best
}
