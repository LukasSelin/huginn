// Package moves finds functions that live in the wrong package: ones that
// only another package refers to, and that could move there together with
// the unexported helpers only they use.
//
// It works on the working tree, not on history: it loads the module with
// go/packages, builds a reference graph between package-level symbols from
// the type checker's uses, and adds the calls that interface dispatch may
// make from a class hierarchy analysis of the module's SSA form.
package moves

import (
	"context"
	"errors"
	"fmt"
	goast "go/ast"
	"go/token"
	"go/types"
	"sort"

	"golang.org/x/tools/go/callgraph/cha"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// Graph is the package-level symbols of a set of packages and the
// references between them.
type Graph struct {
	Fset *token.FileSet
	Pkgs map[string]*packages.Package // loaded packages by import path
	Syms map[types.Object]*Sym

	fieldOwner map[*types.Var]*Sym
}

// Sym is one package-level declaration: a function, method, type, var or
// const.
type Sym struct {
	Obj     types.Object
	Pkg     *packages.Package
	Owner   *Sym   // the receiver's type for a method; nil otherwise
	Methods []*Sym // a type's methods
	In      []Use
	Out     []Use
}

// Name returns the symbol's name qualified by its receiver, e.g. "Parse" or
// "(*Indexer).Visit".
func (s *Sym) Name() string {
	fn, ok := s.Obj.(*types.Func)
	if !ok || s.Owner == nil {
		return s.Obj.Name()
	}
	recv := s.Owner.Obj.Name()
	if _, ptr := fn.Signature().Recv().Type().(*types.Pointer); ptr {
		recv = "*" + recv
	}
	return "(" + recv + ")." + s.Obj.Name()
}

// Exported reports whether the symbol can be named outside its package. A
// method counts as exported only if its type is too.
func (s *Sym) Exported() bool {
	if s.Owner != nil && !s.Owner.Exported() {
		return false
	}
	return s.Obj.Exported()
}

// Use is one reference from a declaration to a symbol.
type Use struct {
	From, To *Sym
	// Obj is the object named: To.Obj, or a field of To's struct type.
	Obj types.Object
	// Dynamic marks a call through an interface that may reach To. It needs
	// neither an import nor access to To, so it never blocks a move.
	Dynamic bool
}

// Mode is what Load asks go/packages for.
const Mode = packages.NeedName | packages.NeedFiles | packages.NeedImports |
	packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo | packages.NeedModule

// Load loads the packages matching patterns in dir and builds their graph.
// Test files are not loaded.
func Load(ctx context.Context, dir string, patterns ...string) (*Graph, error) {
	cfg := &packages.Config{Context: ctx, Dir: dir, Mode: Mode}
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		return nil, err
	}
	var errs []error
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		for _, e := range p.Errors {
			errs = append(errs, e)
		}
	})
	if len(errs) > 0 {
		return nil, fmt.Errorf("loading packages: %w", errors.Join(errs...))
	}
	if len(pkgs) == 0 {
		return nil, fmt.Errorf("no packages match %v", patterns)
	}
	return Build(pkgs), nil
}

// Build builds the graph of pkgs, which must have been loaded with at least
// Mode and without type errors.
func Build(pkgs []*packages.Package) *Graph {
	g := &Graph{
		Fset:       pkgs[0].Fset,
		Pkgs:       map[string]*packages.Package{},
		Syms:       map[types.Object]*Sym{},
		fieldOwner: map[*types.Var]*Sym{},
	}
	for _, p := range pkgs {
		g.Pkgs[p.PkgPath] = p
	}
	for _, p := range pkgs {
		g.declare(p)
	}
	for _, p := range pkgs {
		for _, f := range p.Syntax {
			for _, d := range f.Decls {
				g.walkDecl(p, d)
			}
		}
	}
	g.dispatch(pkgs)
	for _, s := range g.Syms {
		sortUses(s.In, func(u Use) *Sym { return u.From })
		sortUses(s.Out, func(u Use) *Sym { return u.To })
	}
	return g
}

// declare adds a symbol for each package-level object of p and each method
// of its named types, and records which type owns each struct field.
func (g *Graph) declare(p *packages.Package) {
	scope := p.Types.Scope()
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		s := g.add(obj, p, nil)
		tn, ok := obj.(*types.TypeName)
		if !ok || tn.IsAlias() {
			continue
		}
		named, ok := tn.Type().(*types.Named)
		if !ok {
			continue
		}
		for i := 0; i < named.NumMethods(); i++ {
			g.add(named.Method(i), p, s)
		}
		switch u := named.Underlying().(type) {
		case *types.Interface:
			for i := 0; i < u.NumExplicitMethods(); i++ {
				g.add(u.ExplicitMethod(i), p, s)
			}
		case *types.Struct:
			g.ownFields(u, s)
		}
	}
}

// ownFields records owner as the owner of st's fields, and of the fields of
// any struct literal types nested in them.
func (g *Graph) ownFields(st *types.Struct, owner *Sym) {
	for i := 0; i < st.NumFields(); i++ {
		f := st.Field(i)
		g.fieldOwner[f] = owner
		if inner, ok := f.Type().(*types.Struct); ok {
			g.ownFields(inner, owner)
		}
	}
}

func (g *Graph) add(obj types.Object, p *packages.Package, owner *Sym) *Sym {
	if s := g.Syms[obj]; s != nil {
		return s
	}
	s := &Sym{Obj: obj, Pkg: p, Owner: owner}
	g.Syms[obj] = s
	if owner != nil {
		owner.Methods = append(owner.Methods, s)
	}
	return s
}

// walkDecl records what one top-level declaration refers to. Each name of a
// var or const spec is charged with everything the whole spec refers to.
func (g *Graph) walkDecl(p *packages.Package, d goast.Decl) {
	switch d := d.(type) {
	case *goast.FuncDecl:
		// init functions are not in the package scope, so add them here.
		from := g.add(p.TypesInfo.Defs[d.Name], p, nil)
		g.walk(p, from, d)
	case *goast.GenDecl:
		for _, spec := range d.Specs {
			switch sp := spec.(type) {
			case *goast.TypeSpec:
				g.walk(p, g.Syms[p.TypesInfo.Defs[sp.Name]], sp)
			case *goast.ValueSpec:
				for _, name := range sp.Names {
					if obj := p.TypesInfo.Defs[name]; obj != nil {
						g.walk(p, g.Syms[obj], sp)
					}
				}
			}
		}
	}
}

func (g *Graph) walk(p *packages.Package, from *Sym, root goast.Node) {
	if from == nil {
		return
	}
	goast.Inspect(root, func(n goast.Node) bool {
		switch n := n.(type) {
		case *goast.Ident:
			if obj := p.TypesInfo.Uses[n]; obj != nil {
				g.use(from, obj)
			}
		case *goast.SelectorExpr:
			// Promoted fields and methods reach through embedded fields that
			// the source never names.
			if sel := p.TypesInfo.Selections[n]; sel != nil {
				for _, f := range embeddedPath(sel) {
					g.use(from, f)
				}
			}
		}
		return true
	})
}

// use records that from refers to obj, if obj is a field or a symbol of one
// of the graph's packages.
func (g *Graph) use(from *Sym, obj types.Object) {
	var to *Sym
	switch o := obj.(type) {
	case *types.Var:
		if o.IsField() {
			obj = o.Origin()
			to = g.fieldOwner[obj.(*types.Var)]
		} else {
			to = g.Syms[o]
		}
	case *types.Func:
		obj = o.Origin()
		to = g.Syms[obj]
	default:
		to = g.Syms[obj]
	}
	if to == nil || to == from {
		return
	}
	g.link(Use{From: from, To: to, Obj: obj})
}

func (g *Graph) link(u Use) {
	for _, have := range u.From.Out {
		if have == u {
			return
		}
	}
	u.From.Out = append(u.From.Out, u)
	u.To.In = append(u.To.In, u)
}

// embeddedPath returns the embedded fields sel passes through implicitly on
// its way to the field or method it selects.
func embeddedPath(sel *types.Selection) []*types.Var {
	var path []*types.Var
	t := sel.Recv()
	idx := sel.Index()
	for _, i := range idx[:len(idx)-1] {
		if ptr, ok := t.(*types.Pointer); ok {
			t = ptr.Elem()
		}
		st, ok := t.Underlying().(*types.Struct)
		if !ok {
			break
		}
		f := st.Field(i)
		path = append(path, f)
		t = f.Type()
	}
	return path
}

// dispatch adds the calls that interface method calls may make, as found by
// class hierarchy analysis: every method of every type that implements the
// interface may be the callee.
func (g *Graph) dispatch(pkgs []*packages.Package) {
	prog, _ := ssautil.Packages(pkgs, ssa.InstantiateGenerics)
	prog.Build()
	cg := cha.CallGraph(prog)
	for fn, node := range cg.Nodes {
		from := g.funcSym(fn)
		if from == nil {
			continue
		}
		for _, e := range node.Out {
			if e.Site == nil || !e.Site.Common().IsInvoke() {
				continue
			}
			if to := g.funcSym(e.Callee.Func); to != nil && to != from {
				g.link(Use{From: from, To: to, Obj: to.Obj, Dynamic: true})
			}
		}
	}
}

// funcSym returns the symbol of the declaration fn's code belongs to, or nil
// if it has none, as for package initializers and synthetic wrappers.
func (g *Graph) funcSym(fn *ssa.Function) *Sym {
	if fn == nil {
		return nil
	}
	for fn.Parent() != nil {
		fn = fn.Parent()
	}
	if o := fn.Origin(); o != nil {
		fn = o
	}
	if fn.Synthetic != "" || fn.Object() == nil {
		return nil
	}
	return g.Syms[fn.Object()]
}

// Pos returns where s is declared.
func (g *Graph) Pos(s *Sym) token.Position { return g.Fset.Position(s.Obj.Pos()) }

func sortUses(us []Use, end func(Use) *Sym) {
	sort.SliceStable(us, func(i, j int) bool {
		a, b := end(us[i]), end(us[j])
		if a.Pkg.PkgPath != b.Pkg.PkgPath {
			return a.Pkg.PkgPath < b.Pkg.PkgPath
		}
		return a.Obj.Pos() < b.Obj.Pos()
	})
}
