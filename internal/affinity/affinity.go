// Package affinity looks for the seams inside a type that does too much, by
// asking what is already grouped together in it from four sides: the fields
// its methods read and write (the affinity matrix), the methods that change
// in the same commits, the callers that use it in different roles, and the
// words its members' names share. Where they agree, there is a type to
// extract.
//
// The matrix is reordered into blocks: fields written by the same methods
// form the core and other state, configuration and collaborators form
// service blocks with the methods that use them, and methods that only call
// others are orchestration. Every placement says why.
//
// Unlike the other analyses, whose reports the web package mirrors in JSON
// structs of its own, a Report carries its JSON shape itself: the command's
// -json output and the web page read the same document.
package affinity

import (
	"cmp"
	"errors"
	"fmt"
	"math"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"huginn/internal/ast"
	"huginn/internal/lineage"
	"huginn/internal/modpath"
	"huginn/internal/semdiff"
)

// Options tune an analysis. Zero values take the defaults.
type Options struct {
	// Months is the window of history read for change coupling, counted
	// back from the last commit; 0 reads all of it.
	Months int

	// HubShare is the share of the methods using fields above which a
	// configuration or collaborator field is a hub, left out of clustering
	// because it would join everything; default 0.5.
	HubShare float64

	// MinCoChanges is how many commits two methods must have changed in
	// together for the pair to count as strong; default 2.
	MinCoChanges int

	// Top caps the candidates and the symbols outside the type listed per
	// block; default 20 and 5.
	Top int
}

func (o Options) withDefaults() Options {
	if o.HubShare <= 0 {
		o.HubShare = 0.5
	}
	if o.MinCoChanges <= 0 {
		o.MinCoChanges = 2
	}
	if o.Top <= 0 {
		o.Top = 20
	}
	if o.Months < 0 {
		o.Months = 0
	}
	return o
}

// ErrNotFound is returned for a name or ID that is no live type.
var ErrNotFound = errors.New("no type")

// AmbiguousError is returned for a name that several types answer to.
type AmbiguousError struct {
	Name    string
	Matches []string // each as dir.Type
}

func (e *AmbiguousError) Error() string {
	return fmt.Sprintf("%s is ambiguous: %s", e.Name, strings.Join(e.Matches, ", "))
}

// Analyzer analyzes the live types of one history. It is safe for
// concurrent use once New returns; the history must not change.
type Analyzer struct {
	h     *lineage.History
	tests bool

	files []*ast.File // the live tree
	all   []*ast.Symbol
	idOf  map[*ast.Symbol]lineage.ID
	mods  *modpath.Resolver

	types    []*ast.Symbol               // live type declarations
	methods  map[recvKey][]*ast.Symbol   // by receiver, in source order
	ifaces   map[string][]*ast.Symbol    // live interface types by name
	fieldsIn map[string]map[string]bool  // names of the struct fields declared in each directory
	ctor     map[typeKey]map[string]bool // fields set where a type is constructed

	revs [][]lineage.ID // per revision, the live symbols it changed

	idxOnce sync.Once
	idx     *semdiff.Index
}

type recvKey struct{ dir, pkg, recv string }

type typeKey struct{ pkg, name string } // import path and type name

// New prepares the analysis of h's live types. Symbols declared in _test.go
// files are left out unless tests is set.
func New(h *lineage.History, tests bool) *Analyzer {
	a := &Analyzer{
		h: h, tests: tests,
		idOf:     map[*ast.Symbol]lineage.ID{},
		methods:  map[recvKey][]*ast.Symbol{},
		ifaces:   map[string][]*ast.Symbol{},
		fieldsIn: map[string]map[string]bool{},
		ctor:     map[typeKey]map[string]bool{},
		revs:     make([][]lineage.ID, h.Revisions()),
	}
	keep := func(s *ast.Symbol) bool { return tests || !isTest(s.Path) }
	a.files = h.LiveFiles(keep)
	a.collectLive(keep)
	a.mods = modpath.Infer(a.all)
	a.signatureImports()

	for _, s := range a.all {
		a.collectDecl(s)
	}
	for _, ms := range a.methods {
		slices.SortFunc(ms, func(x, y *ast.Symbol) int {
			return cmp.Or(cmp.Compare(x.Path, y.Path), cmp.Compare(x.StartLine, y.StartLine))
		})
	}
	return a
}

// collectLive records the live symbols that keep accepts, in their latest
// version, and the revisions that changed each.
func (a *Analyzer) collectLive(keep func(*ast.Symbol) bool) {
	for sym := range a.h.All() {
		if !sym.Alive() || !keep(sym.Latest()) {
			continue
		}
		cur := sym.Latest()
		a.idOf[cur] = sym.ID
		a.all = append(a.all, cur)
		a.collectRevisions(sym)
	}
}

// collectRevisions adds sym to each revision that changed it.
func (a *Analyzer) collectRevisions(sym *lineage.Symbol) {
	for _, e := range sym.Events {
		if !counted(e) {
			continue
		}
		if i, ok := a.h.Position(e.SHA); ok {
			a.revs[i] = append(a.revs[i], sym.ID)
		}
	}
}

// collectDecl files a live declaration by kind, types with their fields
// and methods by receiver, and records the fields it sets where it
// constructs a type.
func (a *Analyzer) collectDecl(s *ast.Symbol) {
	dir := path.Dir(s.Path)
	switch s.Kind {
	case ast.KindType:
		a.types = append(a.types, s)
		if isInterface(s) {
			a.ifaces[s.Name] = append(a.ifaces[s.Name], s)
		}
		a.collectFields(dir, s)
	case ast.KindMethod:
		k := recvKey{dir, s.Package, s.Recv}
		a.methods[k] = append(a.methods[k], s)
	case ast.KindFunc, ast.KindVar, ast.KindConst:
		// Only their struct literals matter, below.
	}
	a.noteCtor(s)
}

// collectFields records the names of the fields type s declares in dir.
func (a *Analyzer) collectFields(dir string, s *ast.Symbol) {
	for _, m := range s.Members {
		if m.Kind != ast.MemberField && m.Kind != ast.MemberEmbedded {
			continue
		}
		if a.fieldsIn[dir] == nil {
			a.fieldsIn[dir] = map[string]bool{}
		}
		a.fieldsIn[dir][m.Name] = true
	}
}

// signatureImports adds to each file of the live tree the repository
// packages its declarations' signatures name. The history keeps no imports,
// and LiveFiles infers them from the references in bodies, but a function
// taking an *order.Order can call its methods without naming the package
// in its body, and the caller index only matches such calls from files
// that import it.
func (a *Analyzer) signatureImports() {
	byName := map[string][]string{} // package name → import paths
	for _, s := range a.all {
		p := a.pkgOf(s)
		if !slices.Contains(byName[s.Package], p) {
			byName[s.Package] = append(byName[s.Package], p)
		}
	}
	for _, f := range a.files {
		for _, s := range f.Symbols {
			for _, m := range qualified.FindAllStringSubmatch(s.Signature, -1) {
				for _, p := range byName[m[1]] {
					if !slices.ContainsFunc(f.Imports, func(im ast.Import) bool { return im.Path == p }) {
						f.Imports = append(f.Imports, ast.Import{Path: p})
					}
				}
			}
		}
	}
}

// qualified matches a qualified identifier, pkg.Name.
var qualified = regexp.MustCompile(`\b([A-Za-z_]\w*)\.[A-Za-z_]\w*`)

// noteCtor records the fields that s sets where it constructs a type: the
// keys of its struct literals, and, in a function of the type's package
// that has one, the fields it assigns by name, as in s.f = v after
// s := &T{}.
func (a *Analyzer) noteCtor(s *ast.Symbol) {
	var lits []typeKey
	for _, fr := range s.FieldRefs {
		if fr.Type == "" || fr.Name == "" || fr.Pkg == "" && s.Kind == ast.KindMethod && fr.Type == s.Recv {
			continue // a selector on the receiver, or an unkeyed literal
		}
		k := typeKey{fr.Pkg, fr.Type}
		if k.pkg == "" {
			k.pkg = a.mods.PackageOf(s.Path, s.Package)
		}
		a.setCtor(k, fr.Name)
		if !slices.Contains(lits, k) {
			lits = append(lits, k)
		}
	}
	own := a.mods.PackageOf(s.Path, s.Package)
	for _, k := range lits {
		if k.pkg != own {
			continue
		}
		for _, fr := range s.FieldRefs {
			if fr.Approximate() && fr.Writes > 0 {
				a.setCtor(k, fr.Name)
			}
		}
	}
}

func (a *Analyzer) setCtor(k typeKey, field string) {
	if a.ctor[k] == nil {
		a.ctor[k] = map[string]bool{}
	}
	a.ctor[k][field] = true
}

// counted reports whether an event changes the symbol itself: anything but
// a pure move to another file.
func counted(e lineage.Event) bool { return e.Flags&^ast.Moved != 0 }

func isTest(p string) bool { return strings.HasSuffix(p, "_test.go") }

func (a *Analyzer) pkgOf(s *ast.Symbol) string { return a.mods.PackageOf(s.Path, s.Package) }

func (a *Analyzer) sym(s *ast.Symbol) Sym {
	return Sym{ID: a.idOf[s], Name: s.QualifiedName(), Path: s.Path, Line: s.StartLine}
}

// index returns the caller index of the live tree, building it on first use.
func (a *Analyzer) index() *semdiff.Index {
	a.idxOnce.Do(func() { a.idx = semdiff.NewIndex(a.files, a.mods.PackageOf) })
	return a.idx
}

// typeBody returns what a type declaration defines, after its name and type
// parameters: "struct{...}", "interface{...}", "int".
func typeBody(s *ast.Symbol) string {
	rest := strings.TrimPrefix(s.Signature, "type "+s.Name)
	if strings.HasPrefix(rest, "[") {
		depth := 0
		for i, r := range rest {
			switch r {
			case '[':
				depth++
			case ']':
				depth--
			}
			if depth == 0 {
				rest = rest[i+1:]
				break
			}
		}
	}
	rest = strings.TrimPrefix(strings.TrimSpace(rest), "= ")
	return strings.TrimSpace(rest)
}

func isStruct(s *ast.Symbol) bool    { return strings.HasPrefix(typeBody(s), "struct{") }
func isInterface(s *ast.Symbol) bool { return strings.HasPrefix(typeBody(s), "interface{") }

// Lookup returns the live type a name stands for: "Server", qualified by
// package name, directory or import path ("web.Server",
// "internal/web.Server", "huginn/internal/web.Server"), or one of its
// methods ("(*Server).Index", "Server.Index"), which stands for its
// receiver's type.
func (a *Analyzer) Lookup(name string) (lineage.ID, error) {
	name = strings.TrimSpace(name)
	found := a.typesNamed(name)
	if len(found) == 0 {
		found = a.receiversNamed(name)
	}
	switch len(found) {
	case 0:
		return 0, fmt.Errorf("%w named %s", ErrNotFound, name)
	case 1:
		return a.idOf[found[0]], nil
	}
	e := &AmbiguousError{Name: name}
	for _, t := range found {
		e.Matches = append(e.Matches, path.Dir(t.Path)+"."+t.Name)
	}
	slices.Sort(e.Matches)
	return 0, e
}

// typesNamed returns the live types that answer to name.
func (a *Analyzer) typesNamed(name string) []*ast.Symbol {
	var found []*ast.Symbol
	for _, t := range a.types {
		if slices.Contains(a.names(t, t.Name), name) {
			found = append(found, t)
		}
	}
	return found
}

// receiversNamed returns the live types with a method that answers to name.
func (a *Analyzer) receiversNamed(name string) []*ast.Symbol {
	var found []*ast.Symbol
	for k, ms := range a.methods {
		for _, m := range ms {
			if a.methodNamed(m, name) {
				found = a.appendReceiver(found, k)
			}
		}
	}
	return found
}

// methodNamed reports whether name is a way to write method m, as
// "(*Server).Index" or "Server.Index".
func (a *Analyzer) methodNamed(m *ast.Symbol, name string) bool {
	return slices.Contains(a.names(m, m.QualifiedName()), name) || slices.Contains(a.names(m, m.Recv+"."+m.Name), name)
}

// appendReceiver appends to found the live types the methods under k
// belong to, those it does not hold yet.
func (a *Analyzer) appendReceiver(found []*ast.Symbol, k recvKey) []*ast.Symbol {
	for _, t := range a.types {
		if t.Name == k.recv && t.Package == k.pkg && path.Dir(t.Path) == k.dir && !slices.Contains(found, t) {
			found = append(found, t)
		}
	}
	return found
}

// names returns the ways to write name, declared by s: bare, and qualified
// by package name, directory and import path.
func (a *Analyzer) names(s *ast.Symbol, name string) []string {
	out := []string{name, s.Package + "." + name, a.pkgOf(s) + "." + name}
	if dir := path.Dir(s.Path); dir != "." {
		out = append(out, dir+"."+name)
	}
	return out
}

// typeOf returns the live type declaration with the given ID.
func (a *Analyzer) typeOf(id lineage.ID) (*ast.Symbol, error) {
	sym := a.h.Symbol(id)
	if sym == nil || !sym.Alive() {
		return nil, fmt.Errorf("%w with ID %d", ErrNotFound, id)
	}
	cur := sym.Latest()
	if cur.Kind != ast.KindType || a.idOf[cur] != id {
		return nil, fmt.Errorf("%w with ID %d", ErrNotFound, id)
	}
	return cur, nil
}

// Report analyzes the type with the given ID.
func (a *Analyzer) Report(id lineage.ID, opts Options) (*Report, error) {
	typ, err := a.typeOf(id)
	if err != nil {
		return nil, err
	}
	opts = opts.withDefaults()
	b := a.newBuild(typ, opts)
	b.cluster()
	b.order()
	b.findings()
	b.coupling()
	b.names()
	b.clients()
	b.suggest()
	return b.r, nil
}

// Candidates ranks the live struct types with methods by how much they
// look like they do too much: Split × log2(1+methods) × log2(1+fields) ×
// log2(2+edits in the window).
func (a *Analyzer) Candidates(opts Options) []Candidate {
	opts = opts.withDefaults()
	_, _, in := a.window(opts.Months)
	out := []Candidate{}
	for _, t := range a.types {
		if !isStruct(t) || len(a.methods[recvKey{path.Dir(t.Path), t.Package, t.Name}]) == 0 {
			continue
		}
		b := a.newBuild(t, opts)
		b.cluster()
		b.order()
		c := Candidate{Sym: a.sym(t), Dir: path.Dir(t.Path), Methods: len(b.r.Rows), Fields: len(b.r.Columns),
			Split: b.r.Split, LCOM4: b.lcom4()}
		for _, s := range b.syms {
			for _, e := range a.h.Symbol(a.idOf[s]).Events {
				if counted(e) && in(e.Time) {
					c.Edits++
				}
			}
		}
		c.Score = float64(c.Split) * math.Log2(1+float64(c.Methods)) * math.Log2(1+float64(c.Fields)) * math.Log2(2+float64(c.Edits))
		out = append(out, c)
	}
	slices.SortStableFunc(out, func(x, y Candidate) int {
		return cmp.Or(cmp.Compare(y.Score, x.Score), cmp.Compare(y.Methods, x.Methods), cmp.Compare(x.Dir+"."+x.Name, y.Dir+"."+y.Name))
	})
	return out[:min(len(out), opts.Top)]
}

// window returns the span of history a number of months back from the last
// commit covers, all of it for 0, and whether a time falls in it. Commit
// times need not grow along history, so only the start is a bound.
func (a *Analyzer) window(months int) (from, to time.Time, in func(time.Time) bool) {
	first, last := a.h.Span()
	if months <= 0 {
		return first, last, func(time.Time) bool { return true }
	}
	from = last.AddDate(0, -months, 0)
	return from, last, func(t time.Time) bool { return !t.Before(from) }
}
