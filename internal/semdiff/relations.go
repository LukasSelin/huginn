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
		rv.addDecls(f, pkgOf(f.Path, f.Package))
	}
	for _, f := range files {
		rv.addDeps(f, pkgOf(f.Path, f.Package))
	}
	return rv
}

// addDecls indexes f, a file of package pkg, and its declarations.
func (rv *revision) addDecls(f *ast.File, pkg string) {
	rv.files[f.Path] = f
	rv.dirs[pkg] = path.Dir(f.Path)
	for _, s := range f.Symbols {
		id := symID(pkg, s.Key)
		rv.decls[id] = decl{s, pkg}
		if s.Kind == ast.KindMethod {
			rv.methods[s.Name] = append(rv.methods[s.Name], id)
		}
	}
}

// addDeps records the imports of f, a file of package from, of other
// packages of the revision.
func (rv *revision) addDeps(f *ast.File, from string) {
	for _, im := range f.Imports {
		if _, ok := rv.dirs[im.Path]; ok && im.Path != from {
			k := [2]string{from, im.Path}
			if fs := rv.deps[k]; len(fs) == 0 || fs[len(fs)-1] != f.Path {
				rv.deps[k] = append(fs, f.Path)
			}
		}
	}
}

// targets returns what s, declared in package pkg, refers to in other
// packages of the revision, by symID.
//
// A method call whose receiver type is unknown is matched by name with the
// methods of the packages s's file imports, as callers matches it the other
// way round.
func (rv *revision) targets(s *ast.Symbol, pkg string) map[string]target {
	out := map[string]target{}
	var imports map[string]bool
	for _, ref := range s.Refs {
		switch ref.Kind {
		case ast.RefLocal:
			continue // its own package: not a target in another
		case ast.RefImport:
			rv.addTarget(out, pkg, symID(ref.Pkg, ref.Name), ref, false)
		case ast.RefMethod:
			if _, own := rv.decls[symID(pkg, ref.Recv+"."+ref.Name)]; ref.Recv != "" && own {
				continue // the receiver's own method, in this package
			}
			if imports == nil {
				imports = rv.importsOf(s.Path)
			}
			rv.addMethodTargets(out, pkg, ref, imports)
		}
	}
	return out
}

// importsOf returns the import paths of the file at filePath, none when the
// revision has no such file.
func (rv *revision) importsOf(filePath string) map[string]bool {
	imports := map[string]bool{}
	if f := rv.files[filePath]; f != nil {
		for _, im := range f.Imports {
			imports[im.Path] = true
		}
	}
	return imports
}

// addMethodTargets adds to out the methods ref may call when its receiver
// type is unknown: the methods so named in the packages in imports.
func (rv *revision) addMethodTargets(out map[string]target, pkg string, ref ast.Ref, imports map[string]bool) {
	if !goast.IsExported(ref.Name) {
		return // only callable in its own package
	}
	for _, id := range rv.methods[ref.Name] {
		if imports[rv.decls[id].pkg] {
			rv.addTarget(out, pkg, id, ref, true)
		}
	}
}

// addTarget adds ref, a reference to the declaration id, to out, unless the
// declaration is not in the revision or is in pkg, the package of the
// declaration making the reference. A target is approximate only when every
// reference to it is.
func (rv *revision) addTarget(out map[string]target, pkg, id string, ref ast.Ref, approx bool) {
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

// relations works out what the changes start and stop between before and
// after, the trees on either side of them. inScope reports whether a file
// or directory is in Input.Scope; relations touching the scope at neither
// end are left out.
func relations(changes []ast.Change, before, after []*ast.File, pkgOf func(path, pkg string) string, inScope func(string) bool) Relations {
	old, cur := newRevision(before, pkgOf), newRevision(after, pkgOf)
	r := Relations{
		NewPackages:  packagesOnlyIn(cur, old, inScope),
		GonePackages: packagesOnlyIn(old, cur, inScope),
		AddedDeps:    depsOnlyIn(cur, old, inScope),
		RemovedDeps:  depsOnlyIn(old, cur, inScope),
	}

	renamed := renames(changes, pkgOf)
	for _, c := range changes {
		was := renamedTargets(old.sideTargets(c.Old, pkgOf), renamed)
		now := cur.sideTargets(c.New, pkgOf)
		r.AddedRefs = appendRefsOnlyIn(r.AddedRefs, now, was, c.New, pkgOf, inScope)
		r.RemovedRefs = appendRefsOnlyIn(r.RemovedRefs, was, now, c.Old, pkgOf, inScope)
	}
	sortRefs(r.AddedRefs)
	sortRefs(r.RemovedRefs)
	return r
}

// packagesOnlyIn returns the import paths of the packages in scope that a
// has and b has not, sorted.
func packagesOnlyIn(a, b *revision, inScope func(string) bool) []string {
	var out []string
	for pkg, dir := range a.dirs {
		if _, ok := b.dirs[pkg]; !ok && inScope(dir) {
			out = append(out, pkg)
		}
	}
	slices.Sort(out)
	return out
}

// renames returns the new identity of every symbol the changes rename or
// move, by symID, so that a reference that follows it is not reported as
// stopped and started.
func renames(changes []ast.Change, pkgOf func(path, pkg string) string) map[string]string {
	renamed := map[string]string{}
	for _, c := range changes {
		if c.Old != nil && c.New != nil {
			o, n := symID(pkgOf(c.Old.Path, c.Old.Package), c.Old.Key), symID(pkgOf(c.New.Path, c.New.Package), c.New.Key)
			if o != n {
				renamed[o] = n
			}
		}
	}
	return renamed
}

// sideTargets returns the targets of s, one side of a change, or nil when s
// is absent or declared in a test file.
func (rv *revision) sideTargets(s *ast.Symbol, pkgOf func(path, pkg string) string) map[string]target {
	if s == nil || strings.HasSuffix(s.Path, "_test.go") {
		return nil
	}
	return rv.targets(s, pkgOf(s.Path, s.Package))
}

// renamedTargets returns ts with each renamed symID replaced by its new
// identity, or nil when ts is.
func renamedTargets(ts map[string]target, renamed map[string]string) map[string]target {
	if ts == nil {
		return nil
	}
	out := map[string]target{}
	for id, t := range ts {
		out[cmp.Or(renamed[id], id)] = t
	}
	return out
}

// appendRefsOnlyIn appends the references from s to the targets that a has
// and b has not.
func appendRefsOnlyIn(refs []Reference, a, b map[string]target, s *ast.Symbol, pkgOf func(path, pkg string) string, inScope func(string) bool) []Reference {
	for id, t := range a {
		if _, ok := b[id]; !ok {
			refs = appendRef(refs, s, pkgOf(s.Path, s.Package), t, inScope)
		}
	}
	return refs
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
