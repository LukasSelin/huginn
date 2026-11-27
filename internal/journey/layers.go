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
	t := newLayering(n, deps)
	for v := range n {
		if t.index[v] == 0 {
			t.visit(v)
		}
	}

	layer = make([]int, n)
	for v := range n {
		layer[v] = t.compLayer[t.comp[v]]
	}
	cyclic = make([]bool, len(deps))
	for i, d := range deps {
		cyclic[i] = t.comp[d.From] == t.comp[d.To]
	}
	return layer, cyclic
}

// layering runs Tarjan's algorithm over the packages of a frame. It finds
// the cycles, as strongly connected components, and finishes each
// component after every component it refers to, so each can be placed as
// soon as it is found.
type layering struct {
	out       [][]int // the packages each package refers to
	comp      []int   // component per package, -1 until it is found
	compLayer []int   // layer per component

	index, low []int
	onStack    []bool
	stack      []int
	next       int // index 0 means not visited yet
}

func newLayering(n int, deps []Dep) *layering {
	t := &layering{
		out: make([][]int, n), comp: make([]int, n),
		index: make([]int, n), low: make([]int, n), onStack: make([]bool, n), next: 1,
	}
	for _, d := range deps {
		t.out[d.From] = append(t.out[d.From], d.To)
	}
	for i := range t.comp {
		t.comp[i] = -1
	}
	return t
}

// visit walks the packages reachable from v and places every component it
// finishes.
func (t *layering) visit(v int) {
	t.index[v], t.low[v] = t.next, t.next
	t.next++
	t.stack = append(t.stack, v)
	t.onStack[v] = true
	for _, w := range t.out[v] {
		if t.index[w] == 0 {
			t.visit(w)
			t.low[v] = min(t.low[v], t.low[w])
		} else if t.onStack[w] {
			t.low[v] = min(t.low[v], t.index[w])
		}
	}
	if t.low[v] != t.index[v] {
		return
	}
	t.place(t.pop(v))
}

// pop takes the component rooted at v off the stack, numbers it and
// returns its members.
func (t *layering) pop(v int) []int {
	c := len(t.compLayer)
	var members []int
	for {
		w := t.stack[len(t.stack)-1]
		t.stack = t.stack[:len(t.stack)-1]
		t.onStack[w] = false
		t.comp[w] = c
		members = append(members, w)
		if w == v {
			return members
		}
	}
}

// place puts the component just popped a layer above the highest component
// its members refer to; every one of those is already placed.
func (t *layering) place(members []int) {
	c := len(t.compLayer)
	l := 0
	for _, m := range members {
		for _, w := range t.out[m] {
			if t.comp[w] != c {
				l = max(l, t.compLayer[t.comp[w]]+1)
			}
		}
	}
	t.compLayer = append(t.compLayer, l)
}
