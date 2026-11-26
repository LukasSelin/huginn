package journey

// Layers stacks the packages of one frame by what they refer to: a package
// that refers to no other package sits on layer 0, and every other one a
// layer above the highest package it refers to, so that every reference
// points down and a package's layer is the longest chain of references
// below it.
//
// Go forbids import cycles, but a directory's tests or build variants can
// still make packages refer to each other in a circle. The packages of
// such a cycle share a layer, and cyclic marks the deps that run between
// them: the ones that cannot point down.
//
// layer has one entry per package index below n; a package without deps is
// on layer 0. cyclic is parallel to deps.
func Layers(n int, deps []Dep) (layer []int, cyclic []bool) {
	out := make([][]int, n)
	for _, d := range deps {
		out[d.From] = append(out[d.From], d.To)
	}

	// Tarjan's algorithm finds the cycles, as strongly connected
	// components, and finishes each component after every component it
	// refers to, so each can be placed as soon as it is found.
	comp := make([]int, n)
	for i := range comp {
		comp[i] = -1
	}
	var compLayer []int
	index, low := make([]int, n), make([]int, n)
	onStack := make([]bool, n)
	var stack []int
	next := 1 // index 0 means not visited yet
	var visit func(v int)
	visit = func(v int) {
		index[v], low[v] = next, next
		next++
		stack = append(stack, v)
		onStack[v] = true
		for _, w := range out[v] {
			if index[w] == 0 {
				visit(w)
				low[v] = min(low[v], low[w])
			} else if onStack[w] {
				low[v] = min(low[v], index[w])
			}
		}
		if low[v] != index[v] {
			return
		}
		c := len(compLayer)
		var members []int
		for {
			w := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			onStack[w] = false
			comp[w] = c
			members = append(members, w)
			if w == v {
				break
			}
		}
		l := 0
		for _, m := range members {
			for _, w := range out[m] {
				if comp[w] != c {
					l = max(l, compLayer[comp[w]]+1)
				}
			}
		}
		compLayer = append(compLayer, l)
	}
	for v := range n {
		if index[v] == 0 {
			visit(v)
		}
	}

	layer = make([]int, n)
	for v := range n {
		layer[v] = compLayer[comp[v]]
	}
	cyclic = make([]bool, len(deps))
	for i, d := range deps {
		cyclic[i] = comp[d.From] == comp[d.To]
	}
	return layer, cyclic
}
