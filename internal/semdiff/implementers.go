package semdiff

import (
	"regexp"
	"strings"

	"huginn/internal/ast"
)

// typeSite is a type declaration in the tree.
type typeSite struct {
	sym  *ast.Symbol
	file *ast.File
	pkg  string
}

// methodSet maps a type's method names to their unqualified MethodKey.
type methodSet map[string]string

// qualifier matches the package qualifier of a type name, "io." in
// "io.Reader". It is dropped before comparing method signatures, since an
// interface's own package writes Node where an implementer elsewhere writes
// index.Node.
var qualifier = regexp.MustCompile(`\b[A-Za-z_][A-Za-z0-9_]*\.`)

func unqualified(key string) string { return qualifier.ReplaceAllString(key, "") }

// implementers returns the named types in the tree that implement interface
// old, declared in package pkg, as it was before the commit: the types that
// declare every method of old with the same name and parameter and result
// types. Like name-only method calls, they are only searched in pkg itself
// and in the files that import it. When cur, the interface after the
// commit, adds only methods, types that already declare them are left out:
// they were updated with it.
//
// The search is approximate. There is no type checking, so methods
// promoted from embedded fields are not seen, pointer and value receivers
// are not told apart, and types are compared by name without their package.
// The methods of interfaces embedded in old are ignored; an interface
// without methods of its own has no implementers listed.
func (idx *index) implementers(old, cur *ast.Symbol, pkg string) []Caller {
	want := ifaceMethods(old)
	if len(want) == 0 {
		return nil
	}
	var added methodSet
	if cur != nil && !hasEmbedded(cur) {
		added = methodSet{}
		for name, key := range ifaceMethods(cur) {
			if want[name] != key {
				added[name] = key
			}
		}
	}

	importable := strings.TrimSuffix(pkg, "_test")
	var out []Caller
	for _, ts := range idx.types {
		if ts.pkg == pkg && ts.sym.Name == old.Name {
			continue
		}
		id := symID(ts.pkg, ts.sym.Name)
		if ts.pkg != pkg && !idx.imports[ts.file][importable] && !idx.importsAny(idx.methodFiles[id], importable) {
			continue
		}
		have := idx.methodSets[id]
		if !have.covers(want) || len(added) > 0 && have.covers(added) {
			continue
		}
		out = append(out, Caller{
			Package:     ts.pkg,
			Name:        qualified(ts.sym),
			Path:        ts.file.Path,
			Line:        ts.sym.StartLine,
			Approximate: true,
			Test:        strings.HasSuffix(ts.file.Path, "_test.go"),
		})
	}
	sortCallers(out)
	return out
}

// importsAny reports whether one of files imports the package at path.
func (idx *index) importsAny(files []*ast.File, path string) bool {
	for _, f := range files {
		if idx.imports[f][path] {
			return true
		}
	}
	return false
}

func (ms methodSet) covers(want methodSet) bool {
	for name, key := range want {
		if k, ok := ms[name]; !ok || k != key {
			return false
		}
	}
	return true
}

// ifaceMethods returns the methods an interface type declares itself.
func ifaceMethods(s *ast.Symbol) methodSet {
	out := methodSet{}
	for i := range s.Members {
		if m := &s.Members[i]; m.Kind == ast.MemberMethod {
			out[m.Name] = unqualified(m.MethodKey())
		}
	}
	return out
}

func hasEmbedded(s *ast.Symbol) bool {
	for _, m := range s.Members {
		if m.Kind == ast.MemberInterface || m.Kind == ast.MemberTerm {
			return true
		}
	}
	return false
}
