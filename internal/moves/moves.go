package moves

import (
	"fmt"
	goast "go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

// Move suggests moving a function to the one other package that refers to
// it.
type Move struct {
	Func *Sym
	To   *packages.Package
	// Users are the declarations in To that refer to Func.
	Users []*Sym
	// With are the unexported symbols of Func's package that only Func and
	// each other refer to, so they move with it. A type brings its methods.
	With []*Sym
	// Blocked says why the move would not compile as it stands: unexported
	// symbols it shares with code that stays, or an import cycle or internal
	// package rule it would break. Empty for a clean move.
	Blocked []string
	// TestFiles are _test.go files in Func's directory that name Func or
	// one of With. Test files are not type-checked, so these are found by
	// name only.
	TestFiles []string
}

// Find returns a move for every function that only one other package refers
// to, ordered by package and position. Methods are left alone: they cannot
// leave their receiver's package.
func Find(g *Graph) []Move {
	var moves []Move
	for _, s := range g.Syms {
		if m, ok := g.candidate(s); ok {
			moves = append(moves, m)
		}
	}
	sort.Slice(moves, func(i, j int) bool {
		a, b := moves[i].Func, moves[j].Func
		if a.Pkg.PkgPath != b.Pkg.PkgPath {
			return a.Pkg.PkgPath < b.Pkg.PkgPath
		}
		return a.Obj.Pos() < b.Obj.Pos()
	})
	return moves
}

func (g *Graph) candidate(s *Sym) (Move, bool) {
	fn, ok := s.Obj.(*types.Func)
	if !ok || s.Owner != nil || fn.Name() == "init" || fn.Name() == "main" {
		return Move{}, false
	}
	var to *packages.Package
	var users []*Sym
	for _, u := range s.In {
		if u.Dynamic {
			continue
		}
		if to != nil && u.From.Pkg != to {
			return Move{}, false
		}
		to = u.From.Pkg
		users = append(users, u.From)
	}
	if to == nil || to == s.Pkg {
		return Move{}, false
	}
	m := Move{Func: s, To: to, Users: users}
	group := closure(s)
	for _, x := range group.order[1:] {
		if x.Owner == nil {
			m.With = append(m.With, x)
		}
	}
	m.Blocked = g.blockers(m, group)
	m.TestFiles = testFiles(s.Pkg, group)
	return m, true
}

// group is a set of symbols that move together.
type group struct {
	in    map[*Sym]bool
	order []*Sym // the function first, then what joined, methods after their type
}

// closure returns s and the unexported symbols of its package that only s
// and each other refer to statically, so that nothing left behind needs
// them.
func closure(s *Sym) *group {
	gr := &group{in: map[*Sym]bool{s: true}, order: []*Sym{s}}
	for changed := true; changed; {
		changed = false
		for i := 0; i < len(gr.order); i++ {
			for _, u := range gr.order[i].Out {
				unit := u.To
				if unit.Owner != nil {
					unit = unit.Owner
				}
				if u.Dynamic || gr.in[unit] || unit.Pkg != s.Pkg || unit.Obj.Exported() {
					continue
				}
				if gr.onlyUsedBy(unit) {
					gr.join(unit)
					changed = true
				}
			}
		}
	}
	return gr
}

// onlyUsedBy reports whether everything that refers statically to unit or
// one of its methods is in the group or part of unit itself.
func (gr *group) onlyUsedBy(unit *Sym) bool {
	for _, x := range unitSyms(unit) {
		for _, u := range x.In {
			if u.Dynamic || gr.in[u.From] || u.From == unit || u.From.Owner == unit {
				continue
			}
			return false
		}
	}
	return true
}

func (gr *group) join(unit *Sym) {
	for _, x := range unitSyms(unit) {
		gr.in[x] = true
		gr.order = append(gr.order, x)
	}
}

// unitSyms returns unit and, if it is a type, its methods.
func unitSyms(unit *Sym) []*Sym { return append([]*Sym{unit}, unit.Methods...) }

// blockers says what keeps m's group from moving to m.To as it stands.
func (g *Graph) blockers(m Move, gr *group) []string {
	var why []string
	from := m.Func.Pkg
	shared := map[string]bool{}
	deps := map[string]bool{}
	for _, x := range gr.order {
		for _, u := range x.Out {
			if u.Dynamic || gr.in[u.To] {
				continue
			}
			if u.Obj.Pkg() == from.Types && !u.Obj.Exported() {
				shared[describe(u)] = true
			}
			// Only package-level names are written qualified, so only they
			// need an import once the group lives in m.To.
			if u.Obj == u.To.Obj && u.To.Owner == nil && u.To.Pkg != m.To {
				deps[u.To.Pkg.PkgPath] = true
			}
		}
	}
	for _, name := range sorted(shared) {
		why = append(why, fmt.Sprintf("uses unexported %s, which code staying in %s also uses", name, from.Name))
	}
	// As long as m.To uses the function, it imports from, which imports
	// every dependency already, so none of them can lead back to m.To. The
	// check guards targets that do not import from yet.
	for _, dep := range sorted(deps) {
		if path := g.importPath(dep, m.To.PkgPath); path != nil {
			why = append(why, fmt.Sprintf("import cycle: %s", strings.Join(append([]string{m.To.PkgPath}, path...), " -> ")))
		}
		if !internalAllowed(m.To.PkgPath, dep) {
			why = append(why, fmt.Sprintf("%s may not import internal package %s", m.To.PkgPath, dep))
		}
	}
	return why
}

// describe names what a use refers to, e.g. "helper" or "field T.name".
func describe(u Use) string {
	if v, ok := u.Obj.(*types.Var); ok && v.IsField() {
		return "field " + u.To.Name() + "." + v.Name()
	}
	return u.To.Name()
}

// importPath returns the chain of imports leading from package from to
// package to, from first and to last, or nil if from does not import to
// even indirectly. Only the graph's packages are followed: no other package
// can import one of them.
func (g *Graph) importPath(from, to string) []string {
	prev := map[string]string{from: ""}
	queue := []string{from}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		if p == to {
			var path []string
			for ; p != ""; p = prev[p] {
				path = append([]string{p}, path...)
			}
			return path
		}
		pkg := g.Pkgs[p]
		if pkg == nil {
			continue
		}
		for _, imp := range sorted(pkg.Imports) {
			if _, seen := prev[imp]; !seen && g.Pkgs[imp] != nil {
				prev[imp] = p
				queue = append(queue, imp)
			}
		}
	}
	return nil
}

// internalAllowed reports whether the package importer may import dep under
// the internal package rule.
func internalAllowed(importer, dep string) bool {
	var parent string
	if i := strings.LastIndex(dep, "/internal/"); i >= 0 {
		parent = dep[:i]
	} else if p, ok := strings.CutSuffix(dep, "/internal"); ok {
		parent = p
	} else {
		return true
	}
	return importer == parent || strings.HasPrefix(importer, parent+"/")
}

// testFiles returns the _test.go files in pkg's directory that name one of
// the group's symbols, either plainly or qualified by pkg's name.
func testFiles(pkg *packages.Package, gr *group) []string {
	if len(pkg.GoFiles) == 0 {
		return nil
	}
	names := map[string]bool{}
	for _, x := range gr.order {
		if x.Owner == nil {
			names[x.Obj.Name()] = true
		}
	}
	paths, _ := filepath.Glob(filepath.Join(filepath.Dir(pkg.GoFiles[0]), "*_test.go"))
	var found []string
	fset := token.NewFileSet()
	for _, p := range paths {
		f, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if err != nil {
			continue
		}
		external := f.Name.Name != pkg.Name
		hit := false
		goast.Inspect(f, func(n goast.Node) bool {
			if hit {
				return false
			}
			switch n := n.(type) {
			case *goast.SelectorExpr:
				if x, ok := n.X.(*goast.Ident); ok && external && x.Name == pkg.Name && names[n.Sel.Name] {
					hit = true
				}
			case *goast.Ident:
				if !external && names[n.Name] {
					hit = true
				}
			}
			return true
		})
		if hit {
			found = append(found, p)
		}
	}
	return found
}

func sorted[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
