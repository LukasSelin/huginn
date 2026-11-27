package semdiff

import (
	"cmp"
	goast "go/ast"
	"path"
	"slices"
	"strings"

	"huginn/internal/ast"
)

// Relations are the imports between the repository's packages, and the
// references between declarations of different packages, that a change
// starts and stops. Test files are left out, since nothing imports them, and
// so are packages from outside the repository.
type Relations struct {
	// NewPackages and GonePackages are the import paths of the packages
	// the change adds and removes, sorted.
	NewPackages, GonePackages []string

	// AddedDeps and RemovedDeps are the imports between packages that the
	// change starts and stops, sorted by importer, then imported.
	AddedDeps, RemovedDeps []Dependency

	// AddedRefs and RemovedRefs are the references from a changed
	// declaration to a declaration of another package that the change
	// starts and stops, sorted by their packages, the exact ones before
	// those matched by method name only, then by name. A
	// reference to a symbol the change renames or moves is matched with
	// the reference to its old identity, so it counts as neither.
	AddedRefs, RemovedRefs []Reference
}

// Empty reports that the change starts and stops no relations.
func (r *Relations) Empty() bool {
	return len(r.NewPackages)+len(r.GonePackages)+len(r.AddedDeps)+len(r.RemovedDeps)+
		len(r.AddedRefs)+len(r.RemovedRefs) == 0
}

// Dependency is one package of the repository importing another.
type Dependency struct {
	From, To string   // import paths
	Files    []string // the files of From that import To, on the side where it does
	Crossing Crossing
}

// Reference is a declaration referring to one in another package.
type Reference struct {
	From, To               string // qualified with the package name: "api.Load", "store.(*DB).Close"
	FromPackage, ToPackage string // import paths

	// Path and Line locate the first occurrence in From, on the side
	// where the reference exists.
	Path string
	Line int

	Uses        int  // occurrences in From
	Approximate bool // matched by method name only, without knowing the receiver's type
	Crossing    Crossing
}

// Crossing says how a relation crosses Input.Scope.
type Crossing uint8

const (
	Within  Crossing = iota // both ends are in the scope, or there is no scope
	Outward                 // from inside the scope to outside it
	Inward                  // from outside the scope into it
)

func (c Crossing) String() string {
	switch c {
	case Within:
		return "within"
	case Outward:
		return "outward"
	case Inward:
		return "inward"
	}
	return "unknown"
}

// MarshalText encodes a crossing by its name.
func (c Crossing) MarshalText() ([]byte, error) { return []byte(c.String()), nil }

// crossing returns how a relation from an end in the scope or not to an end
// in the scope or not crosses it, and whether it touches the scope at all.
func crossing(fromIn, toIn bool) (Crossing, bool) {
	switch {
	case fromIn && toIn:
		return Within, true
	case fromIn:
		return Outward, true
	case toIn:
		return Inward, true
	}
	return 0, false
}

// revision is one side of a change, indexed to resolve references forward:
// from a declaration to what it refers to.
type revision struct {
	files   map[string]*ast.File // by path
	dirs    map[string]string    // package import path → directory
	decls   map[string]decl      // by symID
	methods map[string][]string  // method name → symIDs of the methods so named
	deps    map[[2]string][]string
}

type decl struct {
	sym *ast.Symbol
	pkg string
}

// target is what one declaration refers to in another package.
type target struct {
	decl
	uses, line int
	approx     bool
}

func newRevision(tree []*ast.File, pkgOf func(path, pkg string) string) *revision {
	rv := &revision{
		files:   map[string]*ast.File{},
		dirs:    map[string]string{},
		decls:   map[string]decl{},
		methods: map[string][]string{},
		deps:    map[[2]string][]string{},
	}
	var files []*ast.File
	for _, f := range tree {
		if f == nil || strings.HasSuffix(f.Path, "_test.go") {
			continue
		}
		files = append(files, f)
		rv.files[f.Path] = f
		pkg := pkgOf(f.Path, f.Package)
		rv.dirs[pkg] = path.Dir(f.Path)
		for _, s := range f.Symbols {
			id := symID(pkg, s.Key)
			rv.decls[id] = decl{s, pkg}
			if s.Kind == ast.KindMethod {
				rv.methods[s.Name] = append(rv.methods[s.Name], id)
			}
		}
	}
	for _, f := range files {
		from := pkgOf(f.Path, f.Package)
		for _, im := range f.Imports {
			if _, ok := rv.dirs[im.Path]; ok && im.Path != from {
				k := [2]string{from, im.Path}
				if fs := rv.deps[k]; len(fs) == 0 || fs[len(fs)-1] != f.Path {
					rv.deps[k] = append(fs, f.Path)
				}
			}
		}
	}
	return rv
}

// targets returns what s, declared in package pkg, refers to in other
// packages of the revision, by symID.
//
// A method call whose receiver type is unknown is matched by name with the
// methods of the packages s's file imports, as callers matches it the other
// way round.
func (rv *revision) targets(s *ast.Symbol, pkg string) map[string]target {
	out := map[string]target{}
	add := func(id string, ref ast.Ref, approx bool) {
		d, ok := rv.decls[id]
		if !ok || d.pkg == pkg {
			return
		}
		t, seen := out[id]
		if !seen {
			t = target{decl: d, line: ref.Line, approx: true}
		}
		t.uses += ref.Count
		t.line = min(t.line, ref.Line)
		t.approx = t.approx && approx
		out[id] = t
	}
	var imports map[string]bool
	for _, ref := range s.Refs {
		switch ref.Kind {
		case ast.RefLocal:
			continue // its own package: not a target in another
		case ast.RefImport:
			add(symID(ref.Pkg, ref.Name), ref, false)
		case ast.RefMethod:
			if _, own := rv.decls[symID(pkg, ref.Recv+"."+ref.Name)]; ref.Recv != "" && own {
				continue // the receiver's own method, in this package
			}
			if imports == nil {
				imports = map[string]bool{}
				if f := rv.files[s.Path]; f != nil {
					for _, im := range f.Imports {
						imports[im.Path] = true
					}
				}
			}
			if !goast.IsExported(ref.Name) {
				continue // only callable in its own package
			}
			for _, id := range rv.methods[ref.Name] {
				if imports[rv.decls[id].pkg] {
					add(id, ref, true)
				}
			}
		}
	}
	return out
}

// relations works out what the changes start and stop between before and
// after, the trees on either side of them. inScope reports whether a file
// or directory is in Input.Scope; relations touching the scope at neither
// end are left out.
func relations(changes []ast.Change, before, after []*ast.File, pkgOf func(path, pkg string) string, inScope func(string) bool) Relations {
	old, cur := newRevision(before, pkgOf), newRevision(after, pkgOf)
	var r Relations

	for pkg, dir := range cur.dirs {
		if _, ok := old.dirs[pkg]; !ok && inScope(dir) {
			r.NewPackages = append(r.NewPackages, pkg)
		}
	}
	for pkg, dir := range old.dirs {
		if _, ok := cur.dirs[pkg]; !ok && inScope(dir) {
			r.GonePackages = append(r.GonePackages, pkg)
		}
	}
	slices.Sort(r.NewPackages)
	slices.Sort(r.GonePackages)

	r.AddedDeps = depsOnlyIn(cur, old, inScope)
	r.RemovedDeps = depsOnlyIn(old, cur, inScope)

	// The new identity of every symbol the changes rename or move, so that
	// a reference that follows it is not reported as stopped and started.
	renamed := map[string]string{}
	for _, c := range changes {
		if c.Old != nil && c.New != nil {
			o, n := symID(pkgOf(c.Old.Path, c.Old.Package), c.Old.Key), symID(pkgOf(c.New.Path, c.New.Package), c.New.Key)
			if o != n {
				renamed[o] = n
			}
		}
	}
	for _, c := range changes {
		var was, now map[string]target
		if s := c.Old; s != nil && !strings.HasSuffix(s.Path, "_test.go") {
			was = map[string]target{}
			for id, t := range old.targets(s, pkgOf(s.Path, s.Package)) {
				was[cmp.Or(renamed[id], id)] = t
			}
		}
		if s := c.New; s != nil && !strings.HasSuffix(s.Path, "_test.go") {
			now = cur.targets(s, pkgOf(s.Path, s.Package))
		}
		for id, t := range now {
			if _, ok := was[id]; !ok {
				r.AddedRefs = appendRef(r.AddedRefs, c.New, pkgOf(c.New.Path, c.New.Package), t, inScope)
			}
		}
		for id, t := range was {
			if _, ok := now[id]; !ok {
				r.RemovedRefs = appendRef(r.RemovedRefs, c.Old, pkgOf(c.Old.Path, c.Old.Package), t, inScope)
			}
		}
	}
	sortRefs(r.AddedRefs)
	sortRefs(r.RemovedRefs)
	return r
}

// depsOnlyIn returns the imports between packages that a has and b has not.
func depsOnlyIn(a, b *revision, inScope func(string) bool) []Dependency {
	var out []Dependency
	for k, files := range a.deps {
		if _, ok := b.deps[k]; ok {
			continue
		}
		fromIn := slices.ContainsFunc(files, inScope)
		cr, ok := crossing(fromIn, inScope(a.dirs[k[1]]))
		if !ok {
			continue
		}
		out = append(out, Dependency{From: k[0], To: k[1], Files: files, Crossing: cr})
	}
	slices.SortFunc(out, func(x, y Dependency) int { return cmp.Or(cmp.Compare(x.From, y.From), cmp.Compare(x.To, y.To)) })
	return out
}

// appendRef appends the reference from s, declared in package pkg, to t if
// it touches the scope.
func appendRef(refs []Reference, s *ast.Symbol, pkg string, t target, inScope func(string) bool) []Reference {
	cr, ok := crossing(inScope(s.Path), inScope(t.sym.Path))
	if !ok {
		return refs
	}
	return append(refs, Reference{
		From:        qualified(s),
		To:          qualified(t.sym),
		FromPackage: pkg,
		ToPackage:   t.pkg,
		Path:        s.Path,
		Line:        t.line,
		Uses:        t.uses,
		Approximate: t.approx,
		Crossing:    cr,
	})
}

// sortRefs sorts references by their packages, the exact ones first, then
// by name.
func sortRefs(rs []Reference) {
	approx := func(r Reference) int {
		if r.Approximate {
			return 1
		}
		return 0
	}
	slices.SortFunc(rs, func(a, b Reference) int {
		return cmp.Or(cmp.Compare(a.FromPackage, b.FromPackage), cmp.Compare(a.ToPackage, b.ToPackage),
			cmp.Compare(approx(a), approx(b)), cmp.Compare(a.From, b.From), cmp.Compare(a.To, b.To))
	})
}

// TreeBefore returns the tree as it was before the files changed: tree with
// each file's new side replaced by its old one.
func TreeBefore(tree []*ast.File, files []FileDiff) []*ast.File {
	changed := map[string]bool{}
	for _, fd := range files {
		if fd.New != nil {
			changed[fd.New.Path] = true
		}
		if fd.Old != nil {
			changed[fd.Old.Path] = true
		}
	}
	var out []*ast.File
	for _, f := range tree {
		if !changed[f.Path] {
			out = append(out, f)
		}
	}
	for _, fd := range files {
		if fd.Old != nil {
			out = append(out, fd.Old)
		}
	}
	return out
}

// newScope returns the function reporting whether a slash-separated
// repository path is in one of the directories or files in scope, or nil
// when scope is empty or names the root, so that everything is in it.
func newScope(scope []string) func(string) bool {
	var dirs []string
	for _, s := range scope {
		d := cleanDir(s)
		if d == "" {
			return nil
		}
		dirs = append(dirs, d)
	}
	if len(dirs) == 0 {
		return nil
	}
	return func(p string) bool {
		p = cleanDir(p)
		for _, d := range dirs {
			if p == d || strings.HasPrefix(p, d+"/") {
				return true
			}
		}
		return false
	}
}
