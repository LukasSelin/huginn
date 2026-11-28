package review

import (
	"cmp"
	"slices"
	"strings"

	"huginn/internal/ast"
	"huginn/internal/journey"
	"huginn/internal/semdiff"
)

// Layers places the packages a change touches on the layers of the whole
// codebase, as the layers page stacks them: a package importing no other
// package of the repository on layer 0, and every other one a layer above
// the highest package it imports. The layers are those of the whole tree
// at the merge base and at the head, so a package sits where it sits in
// the architecture, not where the change alone would put it. Test files
// are left out, since nothing imports them.
type Layers struct {
	// Packages are the packages the change touches: those whose code it
	// changes, those at either end of an import it starts or stops, and
	// those whose layer it moves. The highest at the head come first, then
	// by import path.
	Packages []LayerPackage

	// Imports are the imports between those packages, at the merge base,
	// at the head or both, by importer, then imported.
	Imports []LayerImport

	// Sizes counts the packages of the whole codebase on each layer at the
	// head, layer 0 first.
	Sizes []int
}

// LayerPackage is a package the change touches, with its layer on either
// side.
type LayerPackage struct {
	Package       string // import path
	Before, After int    // its layer at the merge base and at the head, -1 where it does not exist

	// Changes counts the changes to its code outside test files: 0 for a
	// package the change only imports, stops importing or moves.
	Changes int
}

// LayerImport is an import between two of the packages shown.
type LayerImport struct {
	From, To      string // import paths
	Before, After bool   // it exists at the merge base, at the head
}

// layersOf places the packages r touches on the layers of before and after,
// the whole trees at the merge base and at the head.
func layersOf(r *semdiff.Report, before, after []*ast.File, pkgOf func(path, pkg string) string) *Layers {
	old, cur := newPackageGraph(before, pkgOf), newPackageGraph(after, pkgOf)
	was, now := old.stack(), cur.stack()

	touched := map[string]int{} // package → changes to its code
	touch := func(p string) {
		if _, ok := touched[p]; !ok {
			touched[p] = 0
		}
	}
	for i := range r.Changes {
		if c := &r.Changes[i]; !inTestFile(c) {
			touched[c.Package]++
		}
	}
	rel := &r.Relations
	for _, d := range slices.Concat(rel.AddedDeps, rel.RemovedDeps) {
		touch(d.From)
		touch(d.To)
	}
	for _, p := range slices.Concat(rel.NewPackages, rel.GonePackages) {
		touch(p)
	}
	for p, l := range now {
		if b, ok := was[p]; ok && b != l {
			touch(p)
		}
	}
	if len(touched) == 0 {
		return nil
	}

	out := &Layers{Sizes: make([]int, 0)}
	for _, l := range now {
		for len(out.Sizes) <= l {
			out.Sizes = append(out.Sizes, 0)
		}
		out.Sizes[l]++
	}
	layer := func(s map[string]int, p string) int {
		if l, ok := s[p]; ok {
			return l
		}
		return -1
	}
	for p, n := range touched {
		out.Packages = append(out.Packages, LayerPackage{Package: p, Before: layer(was, p), After: layer(now, p), Changes: n})
	}
	slices.SortFunc(out.Packages, func(a, b LayerPackage) int {
		return cmp.Or(cmp.Compare(max(b.After, b.Before), max(a.After, a.Before)), cmp.Compare(a.Package, b.Package))
	})
	out.Imports = importsBetween(touched, old, cur)
	return out
}

// importsBetween returns the imports at the merge base and at the head
// between the packages in shown, by importer, then imported.
func importsBetween(shown map[string]int, old, cur *packageGraph) []LayerImport {
	at := map[[2]string]*LayerImport{}
	for _, side := range []struct {
		g     *packageGraph
		after bool
	}{{old, false}, {cur, true}} {
		for k := range side.g.imports {
			from, to := side.g.names[k[0]], side.g.names[k[1]]
			if _, ok := shown[from]; !ok {
				continue
			}
			if _, ok := shown[to]; !ok {
				continue
			}
			im := at[[2]string{from, to}]
			if im == nil {
				im = &LayerImport{From: from, To: to}
				at[[2]string{from, to}] = im
			}
			im.Before = im.Before || !side.after
			im.After = im.After || side.after
		}
	}
	out := make([]LayerImport, 0, len(at))
	for _, im := range at {
		out = append(out, *im)
	}
	slices.SortFunc(out, func(a, b LayerImport) int {
		return cmp.Or(cmp.Compare(a.From, b.From), cmp.Compare(a.To, b.To))
	})
	return out
}

// packageGraph is the imports between the packages of one tree, test files
// left out.
type packageGraph struct {
	names   []string // import paths
	index   map[string]int
	imports map[[2]int]bool
}

func newPackageGraph(files []*ast.File, pkgOf func(path, pkg string) string) *packageGraph {
	g := &packageGraph{index: map[string]int{}, imports: map[[2]int]bool{}}
	var kept []*ast.File
	for _, f := range files {
		if f == nil || strings.HasSuffix(f.Path, "_test.go") {
			continue
		}
		kept = append(kept, f)
		p := pkgOf(f.Path, f.Package)
		if _, ok := g.index[p]; !ok {
			g.index[p] = len(g.names)
			g.names = append(g.names, p)
		}
	}
	for _, f := range kept {
		from := g.index[pkgOf(f.Path, f.Package)]
		for _, im := range f.Imports {
			if to, ok := g.index[im.Path]; ok && to != from {
				g.imports[[2]int{from, to}] = true
			}
		}
	}
	return g
}

// stack returns the layer of each package, as journey.Layers stacks them.
func (g *packageGraph) stack() map[string]int {
	deps := make([]journey.Dep, 0, len(g.imports))
	for k := range g.imports {
		deps = append(deps, journey.Dep{From: k[0], To: k[1], Refs: 1})
	}
	layer, _ := journey.Layers(len(g.names), deps)
	out := make(map[string]int, len(g.names))
	for i, p := range g.names {
		out[p] = layer[i]
	}
	return out
}
