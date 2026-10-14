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

	fields      map[string][]site // field uses by symID of "Type.Field"
	fieldByName map[string][]site // field uses x.Name with unknown x, by Name

	types       []typeSite             // type declarations, for implementer search
	methodSets  map[string]methodSet   // methods by symID of the receiver base type
	methodFiles map[string][]*ast.File // files declaring them, by the same id
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

		fields:      map[string][]site{},
		fieldByName: map[string][]site{},

		methodSets:  map[string]methodSet{},
		methodFiles: map[string][]*ast.File{},
	}
	methods := map[string]bool{}
	for _, f := range tree {
		pkg := pkgOf(f.Path, f.Package)
		for _, s := range f.Symbols {
			switch s.Kind {
			case ast.KindMethod:
				methods[symID(pkg, s.Key)] = true
				id := symID(pkg, s.Recv)
				if idx.methodSets[id] == nil {
					idx.methodSets[id] = methodSet{}
				}
				idx.methodSets[id][s.Name] = unqualified(s.MethodKey())
				if fs := idx.methodFiles[id]; len(fs) == 0 || fs[len(fs)-1] != f {
					idx.methodFiles[id] = append(fs, f)
				}
			case ast.KindType:
				idx.types = append(idx.types, typeSite{sym: s, file: f, pkg: pkg})
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
			for _, ref := range s.FieldRefs {
				st := site{caller: s, file: f, pkg: pkg, uses: ref.Count}
				switch {
				case ref.Approximate():
					st.approx = true
					idx.fieldByName[ref.Name] = append(idx.fieldByName[ref.Name], st)
				case ref.Pkg != "":
					id := symID(ref.Pkg, ref.Type+"."+ref.Name)
					idx.fields[id] = append(idx.fields[id], st)
				default:
					id := symID(pkg, ref.Type+"."+ref.Name)
					idx.fields[id] = append(idx.fields[id], st)
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

// unkeyedLiterals counts the unkeyed literals T{1, "x"} of type typ,
// declared in package pkg. Their type is spelled out, so the count is exact
// as far as names go.
func (idx *index) unkeyedLiterals(typ, pkg string) (n int) {
	for _, st := range idx.fields[symID(pkg, typ+".")] {
		n += st.uses
	}
	return n
}

// fieldUses counts the uses of field name of struct type typ, declared in
// package pkg, and reports whether some were matched by field name only.
// Like method calls, such uses are only searched in pkg itself and in the
// files that import it.
func (idx *index) fieldUses(typ, name, pkg string) (uses int, approx bool) {
	for _, st := range idx.fields[symID(pkg, typ+"."+name)] {
		uses += st.uses
	}
	importable := strings.TrimSuffix(pkg, "_test")
	for _, st := range idx.fieldByName[name] {
		if st.pkg == pkg || idx.imports[st.file][importable] {
			uses += st.uses
			approx = true
		}
	}
	return uses, approx
}
