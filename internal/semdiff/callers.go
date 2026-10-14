package semdiff

import (
	"strings"

	"huginn/internal/ast"
)

// site is one declaration's references to one target.
type site struct {
	caller *ast.Symbol
	file   *ast.File
	pkg    string // the caller's package import path
	uses   int
	approx bool
}

type callerID struct{ pkg, path, key string }

// index maps the symbols of a tree to the declarations that refer to them.
type index struct {
	exact    map[string][]site // by symbol id, see symID
	byMethod map[string][]site // calls x.Name() with unknown x, by Name
	imports  map[*ast.File]map[string]bool
}

// symID identifies a symbol across a tree: its package import path and its
// key without the "#n" suffix for repeated names.
func symID(pkg, key string) string {
	key, _, _ = strings.Cut(key, "#")
	return pkg + "\x00" + key
}

func newIndex(tree []*ast.File, pkgOf func(path, pkg string) string) *index {
	idx := &index{
		exact:    map[string][]site{},
		byMethod: map[string][]site{},
		imports:  map[*ast.File]map[string]bool{},
	}
	methods := map[string]bool{}
	for _, f := range tree {
		pkg := pkgOf(f.Path, f.Package)
		for _, s := range f.Symbols {
			if s.Kind == ast.KindMethod {
				methods[symID(pkg, s.Key)] = true
			}
		}
	}

	for _, f := range tree {
		pkg := pkgOf(f.Path, f.Package)
		ims := map[string]bool{}
		for _, im := range f.Imports {
			ims[im.Path] = true
		}
		idx.imports[f] = ims

		for _, s := range f.Symbols {
			for _, ref := range s.Refs {
				st := site{caller: s, file: f, pkg: pkg, uses: ref.Count}
				switch ref.Kind {
				case ast.RefLocal:
					id := symID(pkg, ref.Name)
					idx.exact[id] = append(idx.exact[id], st)
				case ast.RefImport:
					id := symID(ref.Pkg, ref.Name)
					idx.exact[id] = append(idx.exact[id], st)
				case ast.RefMethod:
					// A call on the receiver resolves to the receiver's own
					// method when it has one; otherwise it may be promoted
					// from an embedded field or be a func-typed field, and
					// only the name is known.
					if id := symID(pkg, ref.Recv+"."+ref.Name); ref.Recv != "" && methods[id] {
						idx.exact[id] = append(idx.exact[id], st)
						continue
					}
					st.approx = true
					idx.byMethod[ref.Name] = append(idx.byMethod[ref.Name], st)
				}
			}
		}
	}
	return idx
}

// callers returns the declarations referring to s, which lives in package
// pkg, and whether any were matched approximately. record, if not nil, is
// called for each caller.
//
// A method call whose receiver type is unknown is matched by name, and only
// from code that can plausibly hold a value of the method's type: the
// method's own package and the files that import it.
func (idx *index) callers(s *ast.Symbol, pkg string, record func(callerID, Caller)) ([]Caller, bool) {
	sites := idx.exact[symID(pkg, s.Key)]
	if s.Kind == ast.KindMethod {
		importable := strings.TrimSuffix(pkg, "_test")
		for _, st := range idx.byMethod[s.Name] {
			if st.pkg == pkg || idx.imports[st.file][importable] {
				sites = append(sites, st)
			}
		}
	}

	byCaller := map[callerID]*Caller{}
	var order []callerID
	for _, st := range sites {
		id := callerID{st.pkg, st.file.Path, st.caller.Key}
		if id.pkg == pkg && st.caller.Key == s.Key {
			continue // recursion
		}
		c := byCaller[id]
		if c == nil {
			c = &Caller{
				Package: st.pkg,
				Name:    qualified(st.caller),
				Path:    st.file.Path,
				Line:    st.caller.StartLine,
				Test:    strings.HasSuffix(st.file.Path, "_test.go"),
			}
			byCaller[id] = c
			order = append(order, id)
		}
		c.Calls += st.uses
		c.Approximate = c.Approximate || st.approx
	}

	out := make([]Caller, 0, len(order))
	approx := false
	for _, id := range order {
		c := *byCaller[id]
		out = append(out, c)
		approx = approx || c.Approximate
		if record != nil {
			record(id, c)
		}
	}
	sortCallers(out)
	return out, approx
}
