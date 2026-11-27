package review

import (
	"cmp"
	"path"
	"slices"
	"strings"
	"time"

	"huginn/internal/ast"
	"huginn/internal/cohesion"
	"huginn/internal/lineage"
	"huginn/internal/semdiff"
)

// TypeReview is the seams of one struct type the merge request touches,
// before and after it: whether the change keeps the type's groups of
// fields and methods apart, or ties them together.
type TypeReview struct {
	Package string // import path
	Name    string // qualified with the package name: "shop.Order"
	Path    string // the file declaring it, after the change or before a deletion
	Line    int

	// Changed names the members the change touched: its methods, and the
	// type itself as "type" when its definition changed.
	Changed []string

	// Before and After are the type's seams on either side of the change;
	// either is nil where the type does not exist. After includes who uses
	// its methods, and with a history, which of them change together.
	Before, After *cohesion.Report

	Notes []cohesion.Note // what the change does to the seams, warnings first
}

// Types are only reviewed with at least minTypeMethods methods on one side,
// and at most maxTypes of them, the largest first.
const (
	minTypeMethods = 3
	maxTypes       = 12

	// Change coupling reads a year of history and leaves out commits that
	// change more than maxCommitSymbols symbols, such as mass renames.
	couplingWindow   = 365 * 24 * time.Hour
	maxCommitSymbols = 40
)

// touchedType is a struct type a change touched, by its directory and name.
type touchedType struct {
	dir, name string
	changed   []string
}

// types reviews the seams of the struct types the report's changes touch.
func types(r *semdiff.Report, in semdiff.Input, h *lineage.History) []TypeReview {
	var touched []*touchedType
	at := map[[2]string]*touchedType{}
	for _, ch := range r.Changes {
		if strings.HasSuffix(ch.Path, "_test.go") {
			continue
		}
		_, qn, _ := strings.Cut(ch.Name, ".") // drop the package name
		var recv, member string
		switch ch.Symbol {
		case "method":
			i := strings.LastIndex(qn, ".")
			recv, member = strings.TrimSuffix(strings.TrimPrefix(qn[:i], "(*"), ")"), qn[i+1:]
		case "type":
			recv, member = qn, "type"
		default:
			continue
		}
		k := [2]string{path.Dir(ch.Path), recv}
		t := at[k]
		if t == nil {
			t = &touchedType{dir: k[0], name: recv}
			at[k] = t
			touched = append(touched, t)
		}
		if !slices.Contains(t.changed, member) {
			t.changed = append(t.changed, member)
		}
	}
	if len(touched) == 0 {
		return nil
	}

	pkgOf := semdiff.PackagePaths(in.GoMod)
	after, before := newTree(in.Tree), newTree(semdiff.TreeBefore(in.Tree, in.Files))
	idx := semdiff.NewIndex(in.Tree, pkgOf)
	changes := commitChanges(h)
	ifaces := interfaceMethods(in.Tree)

	var out []TypeReview
	for _, t := range touched {
		tr := TypeReview{Changed: t.changed}
		var sizes []int
		if typ, methods := before.find(t.dir, t.name); typ != nil {
			tr.Before = cohesion.Analyze(cohesion.Input{Type: typ, Methods: methods})
			tr.Package, tr.Name, tr.Path, tr.Line = pkgOf(typ.Path, typ.Package), typ.Package+"."+typ.Name, typ.Path, typ.StartLine
			sizes = append(sizes, len(methods))
		}
		if typ, methods := after.find(t.dir, t.name); typ != nil {
			pkg := pkgOf(typ.Path, typ.Package)
			ci := cohesion.Input{Type: typ, Methods: methods, Clients: clients(idx, typ, pkg), Interfaces: ifaces}
			if changes != nil {
				ci.Changes = changes(methods)
			}
			tr.After = cohesion.Analyze(ci)
			tr.Package, tr.Name, tr.Path, tr.Line = pkg, typ.Package+"."+typ.Name, typ.Path, typ.StartLine
			sizes = append(sizes, len(methods))
		}
		if len(sizes) == 0 || slices.Max(sizes) < minTypeMethods {
			continue
		}
		tr.Notes = cohesion.Compare(tr.Before, tr.After)
		out = append(out, tr)
	}
	slices.SortStableFunc(out, func(a, b TypeReview) int {
		size := func(t TypeReview) int {
			n := 0
			for _, r := range []*cohesion.Report{t.Before, t.After} {
				if r != nil {
					n = max(n, len(r.Methods))
				}
			}
			return n
		}
		return cmp.Compare(size(b), size(a))
	})
	if len(out) > maxTypes {
		out = out[:maxTypes]
	}
	return out
}

// tree finds struct types and their methods in one revision's files, by
// directory.
type tree map[string][]*ast.File

func newTree(files []*ast.File) tree {
	t := tree{}
	for _, f := range files {
		if !strings.HasSuffix(f.Path, "_test.go") {
			d := path.Dir(f.Path)
			t[d] = append(t[d], f)
		}
	}
	return t
}

// find returns the struct type named name in dir and its methods, or nil
// when there is none.
func (t tree) find(dir, name string) (*ast.Symbol, []*ast.Symbol) {
	var typ *ast.Symbol
	for _, f := range t[dir] {
		for _, s := range f.Symbols {
			if s.Kind == ast.KindType && s.Name == name {
				typ = s
			}
		}
	}
	if typ == nil || !slices.ContainsFunc(typ.Members, func(m ast.Member) bool {
		return m.Kind == ast.MemberField || m.Kind == ast.MemberEmbedded
	}) {
		return nil, nil // not a struct, or one without fields
	}
	var methods []*ast.Symbol
	for _, f := range t[dir] {
		if f.Package != typ.Package {
			continue
		}
		for _, s := range f.Symbols {
			if s.Kind == ast.KindMethod && s.Recv == name {
				methods = append(methods, s)
			}
		}
	}
	return typ, methods
}

// interfaceMethods returns the names of the methods the interfaces in the
// tree declare.
func interfaceMethods(files []*ast.File) map[string]bool {
	out := map[string]bool{}
	for _, f := range files {
		for _, s := range f.Symbols {
			if s.Kind != ast.KindType {
				continue
			}
			for _, m := range s.Members {
				if m.Kind == ast.MemberMethod {
					out[m.Name] = true
				}
			}
		}
	}
	return out
}

// clients returns the function listing the declarations outside type typ,
// of package pkg, that use one of its methods.
func clients(idx *semdiff.Index, typ *ast.Symbol, pkg string) func(*ast.Symbol) []cohesion.Client {
	return func(m *ast.Symbol) []cohesion.Client {
		var out []cohesion.Client
		for _, u := range idx.Uses(m, pkg) {
			c := u.Caller
			if c.Kind == ast.KindMethod && c.Recv == typ.Name && u.Package == pkg {
				continue // the type's own methods
			}
			out = append(out, cohesion.Client{
				Package: u.Package, Name: c.Package + "." + c.QualifiedName(), Calls: u.Calls,
				Test: strings.HasSuffix(c.Path, "_test.go"), Approximate: u.Approximate,
			})
		}
		return out
	}
}

// commitChanges returns the function listing, for methods, the commits of
// the last year of history that changed each, by name, leaving out commits
// that changed many symbols at once. It returns nil without a history.
func commitChanges(h *lineage.History) func([]*ast.Symbol) map[string][]string {
	if h == nil || h.Revisions() == 0 {
		return nil
	}
	_, last := h.RevisionAt(h.Revisions() - 1)
	since := last.Add(-couplingWindow)
	size := map[string]int{}
	for s := range h.All() {
		for _, e := range s.Events {
			size[e.SHA]++
		}
	}
	return func(methods []*ast.Symbol) map[string][]string {
		out := map[string][]string{}
		for _, m := range methods {
			s := h.Lookup(m.Path, m.Key)
			if s == nil {
				continue
			}
			for _, e := range s.Events {
				if e.Time.Before(since) || size[e.SHA] > maxCommitSymbols {
					continue
				}
				out[m.Name] = append(out[m.Name], e.SHA)
			}
		}
		return out
	}
}
