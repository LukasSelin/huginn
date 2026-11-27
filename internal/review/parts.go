package review

import (
	"cmp"
	"slices"
	"strings"

	"huginn/internal/semdiff"
)

// Part is a group of the changed functions to read together, so that a
// large change can be read a part at a time: the functions of one package
// that call one another, with the functions linked to them alone.
type Part struct {
	// Functions are the part's functions and methods in reading order:
	// depth first from those no other function of the part calls, each
	// before the functions of the part it calls.
	Functions []PartFunction

	// Hub indexes Report.Changes: the function of the part with the most
	// calls to and from the others, which names it.
	Hub int

	// Loose says the part holds the changed functions linked to no other
	// changed function, called through an interface, only from tests or not
	// at all. It comes last.
	Loose bool
}

// PartFunction is one function of a part.
type PartFunction struct {
	Change int // index into Report.Changes
	Depth  int // in the part's call tree: 0 for one no other function of the part calls
}

// minPart is the fewest functions of one package calling one another that
// make a part of their own; fewer join the part they are linked to.
const minPart = 3

// parts splits the changed functions and methods of r, test files left
// out, into parts, in reading order: a part before those it calls, those
// that code the change leaves alone calls first, then the largest.
//
// The functions of one package that call one another make a part when
// there are at least minPart of them. The rest, grouped by the calls among
// them, join the one part they are linked to; a single function linked to
// several joins the one it makes the most calls with. A group linked to
// several parts or none makes a part of its own, and a function linked to
// nothing at all goes to the loose part.
func parts(r *semdiff.Report) []Part {
	g := newCallGraph(r)
	if len(g.nodes) == 0 {
		return nil
	}
	groups, partOf, rest := g.packageGroups()
	groups, loose := g.placeRest(rest, groups, partOf)
	out := make([]Part, 0, len(groups)+1)
	for _, i := range g.partOrder(groups, partOf) {
		out = append(out, g.part(groups[i]))
	}
	if len(loose) > 0 {
		p := g.part(loose)
		p.Loose = true
		out = append(out, p)
	}
	return out
}

// packageGroups groups the functions of one package that call one another.
// It returns the groups of at least minPart, the group of each of their
// functions by change index, and the functions of the smaller groups.
func (g *callGraph) packageGroups() (groups [][]int, partOf map[int]int, rest []int) {
	same := newUnionFind(g.nodes)
	for _, a := range g.nodes {
		for _, b := range g.out[a] {
			if g.pkg[a] == g.pkg[b] {
				same.union(a, b)
			}
		}
	}
	partOf = map[int]int{}
	for _, gr := range same.groups(g.nodes) {
		if len(gr) < minPart {
			rest = append(rest, gr...)
			continue
		}
		for _, f := range gr {
			partOf[f] = len(groups)
		}
		groups = append(groups, gr)
	}
	return groups, partOf, rest
}

// placeRest groups the rest by the calls among them and places each group:
// in the one group it is linked to, a single function linked to several in
// the one it makes the most calls with, a function linked to nothing in
// loose, and any other group on its own. It records where each function
// went in partOf, and returns the groups and loose.
func (g *callGraph) placeRest(rest []int, groups [][]int, partOf map[int]int) (_ [][]int, loose []int) {
	inRest := map[int]bool{}
	for _, f := range rest {
		inRest[f] = true
	}
	small := newUnionFind(rest)
	for _, a := range rest {
		for _, b := range g.out[a] {
			if inRest[b] {
				small.union(a, b)
			}
		}
	}
	for _, gr := range small.groups(rest) {
		links := g.links(gr, partOf)
		var p int
		switch {
		case len(links) == 1 || len(gr) == 1 && len(links) > 1:
			p = strongest(links, groups)
			groups[p] = append(groups[p], gr...)
		case len(gr) == 1:
			loose = append(loose, gr[0])
			continue
		default:
			p = len(groups)
			groups = append(groups, gr)
		}
		for _, f := range gr {
			partOf[f] = p
		}
	}
	return groups, loose
}

// links counts the calls between the functions fs and each group.
func (g *callGraph) links(fs []int, partOf map[int]int) map[int]int {
	links := map[int]int{}
	for _, f := range fs {
		for _, o := range g.neighbours(f) {
			if p, ok := partOf[o]; ok {
				links[p]++
			}
		}
	}
	return links
}

// strongest returns the group links names the most calls with, the
// largest of those tied, then the first.
func strongest(links map[int]int, groups [][]int) int {
	best := -1
	for p, n := range links {
		if best < 0 || cmp.Or(cmp.Compare(n, links[best]), cmp.Compare(len(groups[p]), len(groups[best])), cmp.Compare(best, p)) > 0 {
			best = p
		}
	}
	return best
}

// callGraph is the calls between the changed functions and methods of a
// report outside test files, by index into Report.Changes, each pair once:
// the calls the change starts, stops and keeps.
type callGraph struct {
	nodes   []int // in report order
	pkg     map[int]string
	out, in map[int][]int // sorted
	seen    map[[2]int]bool
	entered map[int]bool // called by code outside tests that the change leaves alone
}

func newCallGraph(r *semdiff.Report) *callGraph {
	g := &callGraph{pkg: map[int]string{}, out: map[int][]int{}, in: map[int][]int{}, seen: map[[2]int]bool{}, entered: map[int]bool{}}
	find := g.addFunctions(r)
	for _, i := range g.nodes {
		g.addCalls(&r.Changes[i], i, find)
	}
	for _, m := range []map[int][]int{g.out, g.in} {
		for _, fs := range m {
			slices.Sort(fs)
		}
	}
	return g
}

// addFunctions adds the changed functions and methods of r outside test
// files, and returns the function finding one by its package and name, or
// by its name before a rename.
func (g *callGraph) addFunctions(r *semdiff.Report) func(pkg, name string) (int, bool) {
	byName, byOld := map[[2]string]int{}, map[string]int{}
	for i := range r.Changes {
		c := &r.Changes[i]
		if c.Symbol != "func" && c.Symbol != "method" || inTestFile(c) {
			continue
		}
		k := [2]string{c.Package, c.Name}
		if _, dup := byName[k]; dup {
			continue // a second init of the package
		}
		byName[k] = i
		if c.OldName != "" {
			byOld[c.OldName] = i
		}
		g.nodes = append(g.nodes, i)
		g.pkg[i] = c.Package
	}
	return func(pkg, name string) (int, bool) {
		if i, ok := byName[[2]string{pkg, name}]; ok {
			return i, true
		}
		i, ok := byOld[name]
		return i, ok
	}
}

// addCalls adds the calls between change c, at index i, and the other
// changed functions, as its callees and its callers outside tests name
// them, and notes whether code the change leaves alone calls it.
func (g *callGraph) addCalls(c *semdiff.Change, i int, find func(pkg, name string) (int, bool)) {
	for _, ce := range c.Callees {
		if j, ok := find(ce.Package, ce.Name); ok {
			g.call(i, j)
		}
	}
	for _, cl := range c.Callers {
		if cl.Test {
			continue
		}
		if j, ok := find(cl.Package, cl.Name); ok {
			g.call(j, i)
		} else {
			g.entered[i] = true
		}
	}
}

// call adds the call from a to b, once.
func (g *callGraph) call(a, b int) {
	if a != b && !g.seen[[2]int{a, b}] {
		g.seen[[2]int{a, b}] = true
		g.out[a] = append(g.out[a], b)
		g.in[b] = append(g.in[b], a)
	}
}

// inTestFile reports whether a change is to a test file, on either side.
func inTestFile(c *semdiff.Change) bool {
	return strings.HasSuffix(c.Path, "_test.go") || strings.HasSuffix(c.OldPath, "_test.go")
}

// neighbours returns the functions f calls or is called by.
func (g *callGraph) neighbours(f int) []int {
	return append(slices.Clone(g.out[f]), g.in[f]...)
}

// part puts a part's functions in reading order and finds its hub.
func (g *callGraph) part(members []int) Part {
	in := map[int]bool{}
	for _, f := range members {
		in[f] = true
	}
	slices.Sort(members)
	p := Part{Functions: g.readingOrder(members, in)}
	p.Hub = g.hub(p.Functions, in)
	return p
}

// readingOrder walks a part depth first from the functions no other
// function of it calls, and then from the first not reached, on a loop.
func (g *callGraph) readingOrder(members []int, in map[int]bool) []PartFunction {
	var out []PartFunction
	done := map[int]bool{}
	var visit func(f, depth int)
	visit = func(f, depth int) {
		done[f] = true
		out = append(out, PartFunction{Change: f, Depth: depth})
		for _, c := range g.out[f] {
			if in[c] && !done[c] {
				visit(c, depth+1)
			}
		}
	}
	for _, f := range members {
		if inside(g.in[f], in) == 0 {
			visit(f, 0)
		}
	}
	for _, f := range members {
		if !done[f] {
			visit(f, 0)
		}
	}
	return out
}

// hub returns the function of a part with the most calls to and from the
// others, the first in reading order of those tied.
func (g *callGraph) hub(fs []PartFunction, in map[int]bool) int {
	hub, most := fs[0].Change, -1
	for _, pf := range fs {
		if n := inside(g.out[pf.Change], in) + inside(g.in[pf.Change], in); n > most {
			hub, most = pf.Change, n
		}
	}
	return hub
}

// inside counts the functions of fs in a part.
func inside(fs []int, in map[int]bool) int {
	n := 0
	for _, f := range fs {
		if in[f] {
			n++
		}
	}
	return n
}

// partOrder returns the order to read the groups in: each before the
// groups it calls where it can, and of those free to come next, first one
// that code the change leaves alone calls, then the largest, then the
// first.
func (g *callGraph) partOrder(groups [][]int, partOf map[int]int) []int {
	calls, waits, entered := g.groupCalls(groups, partOf)
	before := func(a, b int) bool {
		return cmp.Or(trueFirst(waits[a] == 0, waits[b] == 0), trueFirst(entered[a], entered[b]),
			cmp.Compare(len(groups[a]), len(groups[b])), cmp.Compare(b, a)) > 0
	}
	order := make([]int, 0, len(groups))
	placed := make([]bool, len(groups))
	for range groups {
		next := -1
		for i := range groups {
			if !placed[i] && (next < 0 || before(i, next)) {
				next = i
			}
		}
		placed[next] = true
		order = append(order, next)
		for q := range calls[next] {
			waits[q]--
		}
	}
	return order
}

// groupCalls returns, for each group, the other groups it calls, how many
// groups call it, and whether code the change leaves alone calls into it.
func (g *callGraph) groupCalls(groups [][]int, partOf map[int]int) (calls []map[int]bool, waits []int, entered []bool) {
	calls, waits, entered = make([]map[int]bool, len(groups)), make([]int, len(groups)), make([]bool, len(groups))
	for i, gr := range groups {
		calls[i] = map[int]bool{}
		for _, f := range gr {
			entered[i] = entered[i] || g.entered[f]
			for _, c := range g.out[f] {
				if q, ok := partOf[c]; ok && q != i && !calls[i][q] {
					calls[i][q] = true
					waits[q]++
				}
			}
		}
	}
	return calls, waits, entered
}

// trueFirst compares two flags so that true comes first.
func trueFirst(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return 1
	}
	return -1
}

// unionFind groups the functions that are linked.
type unionFind map[int]int

func newUnionFind(fs []int) unionFind {
	u := unionFind{}
	for _, f := range fs {
		u[f] = f
	}
	return u
}

func (u unionFind) find(f int) int {
	for u[f] != f {
		u[f] = u[u[f]]
		f = u[f]
	}
	return f
}

func (u unionFind) union(a, b int) { u[u.find(a)] = u.find(b) }

// groups returns the groups of fs, each in the order of fs, in the order
// of their first function.
func (u unionFind) groups(fs []int) [][]int {
	at := map[int]int{}
	var out [][]int
	for _, f := range fs {
		r := u.find(f)
		i, ok := at[r]
		if !ok {
			i = len(out)
			at[r] = i
			out = append(out, nil)
		}
		out[i] = append(out[i], f)
	}
	return out
}
