// Package modpath maps the import paths that a history's symbols refer to
// onto the repository's directories.
package modpath

import (
	"cmp"
	"path"
	"slices"
	"strings"

	"huginn/internal/ast"
)

// Resolver maps the import paths in symbol references to repository
// directories.
//
// The history does not record go.mod, so the module path is inferred: it is
// the prefix that turns the most directories into import paths that the
// code actually imports. A repository that renamed its module, as
// github.com/dotcloud/docker became github.com/docker/docker and then
// github.com/moby/moby/v2, imports itself under each name in turn, so more
// prefixes follow: each the one that resolves the most imports the earlier
// ones left, while it resolves two or more and at least a tenth as many as
// the first. That covers the main module of a repository; imports of nested
// modules are resolved only if they are as common.
type Resolver struct {
	modules  []string // longest first
	dirs     map[string]bool
	cache    map[string]string
	imported map[string]bool // every import path referred to
}

// Infer returns the resolver for the import paths syms refer to.
func Infer(syms []*ast.Symbol) *Resolver {
	r := &Resolver{dirs: map[string]bool{}, cache: map[string]string{}, imported: map[string]bool{}}
	imports := r.collect(syms)
	first := 0
	for len(imports) > 0 {
		mod, best := bestCandidate(r.candidates(imports))
		if best == 0 || first > 0 && (best < 2 || best*10 < first) {
			break
		}
		first = max(first, best)
		r.modules = append(r.modules, mod)
		imports = slices.DeleteFunc(imports, func(imp string) bool { return r.under(mod, imp) != "" })
	}
	slices.SortStableFunc(r.modules, func(a, b string) int { return cmp.Compare(len(b), len(a)) })
	return r
}

// collect records the directories of syms and every import path they refer
// to, and returns those import paths sorted, each once.
func (r *Resolver) collect(syms []*ast.Symbol) []string {
	var imports []string
	for _, s := range syms {
		r.dirs[path.Dir(s.Path)] = true
		for _, ref := range s.Refs {
			if ref.Kind == ast.RefImport && !r.imported[ref.Pkg] {
				r.imported[ref.Pkg] = true
				imports = append(imports, ref.Pkg)
			}
		}
	}
	slices.Sort(imports)
	return imports
}

// candidates counts, for each module path the imports suggest, how many of
// them it would resolve.
func (r *Resolver) candidates(imports []string) map[string]int {
	// Every import ending in a directory other than the root makes the
	// rest of it a candidate.
	matches := map[string]int{}
	for _, imp := range imports {
		for i := range len(imp) {
			if imp[i] == '/' && r.dirs[imp[i+1:]] && imp[i+1:] != "." {
				matches[imp[:i]]++
			}
		}
	}
	return matches
}

// bestCandidate returns the candidate that resolves the most imports, the
// first in sort order on a tie, and how many it resolves: 0 without
// candidates. The tie-break keeps the choice independent of map order.
func bestCandidate(matches map[string]int) (mod string, best int) {
	for m, n := range matches {
		if n > best || n == best && m < mod {
			mod, best = m, n
		}
	}
	return mod, best
}

// under returns the directory of the package imported as imp if it is in
// module mod, or "".
func (r *Resolver) under(mod, imp string) string {
	d := ""
	switch {
	case imp == mod:
		d = "."
	case strings.HasPrefix(imp, mod+"/"):
		d = imp[len(mod)+1:]
	}
	if !r.dirs[d] {
		return ""
	}
	return d
}

// Dir returns the directory of the package imported as imp, or "".
func (r *Resolver) Dir(imp string) string {
	if d, ok := r.cache[imp]; ok {
		return d
	}
	d := ""
	for _, mod := range r.modules {
		if d = r.under(mod, imp); d != "" {
			break
		}
	}
	r.cache[imp] = d
	return d
}

// Targets returns the directories s refers to, each once, sorted.
func (r *Resolver) Targets(s *ast.Symbol) []string {
	var out []string
	for _, ref := range s.Refs {
		if ref.Kind != ast.RefImport {
			continue
		}
		if d := r.Dir(ref.Pkg); d != "" && !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	slices.Sort(out)
	return out
}

// PackageOf returns the import path of the package declared by the file at
// path in the repository; the package name is not needed. It has the shape
// semdiff.NewIndex asks for.
func (r *Resolver) PackageOf(file, _ string) string { return r.ImportPath(path.Dir(file)) }

// ImportPath returns the import path of the package in directory dir: the
// first module path under which the symbols import it, or under which they
// could when none do. Without a module it is dir itself, which still tells
// the repository's packages apart.
func (r *Resolver) ImportPath(dir string) string {
	join := func(mod string) string {
		if dir == "." {
			return mod
		}
		return mod + "/" + dir
	}
	for _, mod := range r.modules {
		if r.imported[join(mod)] {
			return join(mod)
		}
	}
	if len(r.modules) > 0 {
		return join(r.modules[0])
	}
	return dir
}
