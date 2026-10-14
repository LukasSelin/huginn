// Package semdiff turns the symbol-level changes of one commit into a report
// for the semantic commit diff screen: what changed per symbol, which files
// only changed in formatting, and which callers elsewhere in the repository
// are affected.
package semdiff

import (
	"cmp"
	"fmt"
	goast "go/ast"
	"path"
	"slices"
	"strings"

	"huginn/internal/ast"
)

// Kind is one kind of semantic change, as shown on a filter chip.
type Kind uint8

const (
	KindSignature Kind = iota + 1
	KindAdded
	KindBody
	KindRenamed
	KindMoved
	KindDeleted
)

// Kinds lists every kind in display order.
var Kinds = []Kind{KindSignature, KindAdded, KindBody, KindRenamed, KindMoved, KindDeleted}

func (k Kind) String() string {
	switch k {
	case KindSignature:
		return "signature"
	case KindAdded:
		return "added"
	case KindBody:
		return "body edit"
	case KindRenamed:
		return "renamed"
	case KindMoved:
		return "moved"
	case KindDeleted:
		return "deleted"
	}
	return "unknown"
}

// kindsOf returns the kinds a change carries, in display order.
func kindsOf(f ast.ChangeFlags) []Kind {
	switch {
	case f.Has(ast.Added):
		return []Kind{KindAdded}
	case f.Has(ast.Deleted):
		return []Kind{KindDeleted}
	}
	var out []Kind
	for _, x := range []struct {
		flag ast.ChangeFlags
		kind Kind
	}{
		{ast.SignatureChanged, KindSignature},
		{ast.BodyChanged, KindBody},
		{ast.Renamed, KindRenamed},
		{ast.Moved, KindMoved},
	} {
		if f.Has(x.flag) {
			out = append(out, x.kind)
		}
	}
	slices.Sort(out)
	return out
}

// primaryKind is the kind a change's card is labeled with when it carries
// several: the one that matters most to a reader of the API.
func primaryKind(f ast.ChangeFlags) Kind {
	for _, x := range []struct {
		flag ast.ChangeFlags
		kind Kind
	}{
		{ast.Added, KindAdded},
		{ast.Deleted, KindDeleted},
		{ast.SignatureChanged, KindSignature},
		{ast.Renamed, KindRenamed},
		{ast.Moved, KindMoved},
	} {
		if f.Has(x.flag) {
			return x.kind
		}
	}
	return KindBody
}

// FileDiff is one Go file a commit touched.
type FileDiff struct {
	// Old and New are the file before and after the commit. Either is nil
	// when the file did not exist on that side or could not be parsed at all.
	Old, New *ast.File

	// Added and Removed count text lines, as git reports them.
	Added, Removed int
}

// Input is everything a report is built from.
type Input struct {
	// Files are the Go files the commit touched.
	Files []FileDiff

	// Tree is every Go file of the repository at the commit, test files
	// included. Callers are searched for here.
	Tree []*ast.File

	// GoMod holds the contents of the go.mod files at the commit, keyed by
	// their slash-separated repository path ("go.mod", "tools/go.mod"). They
	// map directories to import paths so that pkg.Name references in other
	// packages resolve.
	GoMod map[string][]byte

	// IncludeGenerated diffs generated files like hand-written ones. By
	// default a generated side of a file is treated as absent, as the git
	// walker does, and a generated file without other changes is noise.
	IncludeGenerated bool
}

// Report is the semantic diff of one commit.
type Report struct {
	// Added and Removed count text lines over all Go files touched.
	Added, Removed int

	// Symbols is the number of symbols that changed; Changes has one entry
	// for each. SemanticChanges counts their kinds, so a symbol that was
	// renamed and edited counts twice.
	Symbols         int
	SemanticChanges int
	Counts          map[Kind]int

	Files   []FileSummary // files with semantic changes or non-noise edits, by path
	Changes []Change      // ordered by file and line
	Blast   BlastRadius
	Noise   []NoiseFile // files whose changes do not matter semantically, by path
}

// Headline summarizes the report in one line:
// "+214 −131 lines → 6 semantic changes across 4 symbols".
func (r *Report) Headline() string {
	return fmt.Sprintf("+%d −%d lines → %s across %s", r.Added, r.Removed,
		plural(r.SemanticChanges, "semantic change"), plural(r.Symbols, "symbol"))
}

// FileSummary is one file in the file list.
type FileSummary struct {
	Path    string
	OldPath string // set when the file was renamed
	Kinds   []Kind // kinds of the changes in the file, in display order
	Changes int    // changed symbols in the file
	Added   int
	Removed int
}

// NoiseFile is a file that changed without changing any symbol.
type NoiseFile struct {
	Path           string
	Reason         string // "formatting or comments", or "generated"
	Added, Removed int
}

// Change is one changed symbol: one card on the screen.
type Change struct {
	Kind  Kind   // the label of the card
	Kinds []Kind // every kind the change carries, in display order
	Flags ast.ChangeFlags

	Package string // import path of the package declaring the symbol
	Name    string // qualified with the package name: "ast.(*Indexer).Visit"
	OldName string // the name before a rename; empty otherwise
	Symbol  string // kind of declaration: "func", "method", "type", ...

	// Path and Line locate the symbol after the commit, or before it for a
	// deletion. OldPath and OldLine locate it before, empty for additions.
	Path    string
	Line    int
	OldPath string
	OldLine int

	OldSignature, NewSignature string // empty on the side the symbol is absent

	StmtsBefore, StmtsAfter           int
	ComplexityBefore, ComplexityAfter int // cyclomatic; 0 for non-functions
	Summary                           string

	// Exported reports that the symbol is exported on either side. APIChange
	// reports that the exported API changed in a way that can break callers:
	// an exported symbol was deleted, renamed, moved to another package or
	// had its signature changed.
	Exported  bool
	APIChange bool

	// Callers are the declarations in Tree that refer to the symbol (to its
	// old identity for a deletion), most uses first. CallSites sums their
	// uses. Approximate is set when some were matched by method name only.
	Callers     []Caller
	CallSites   int
	Approximate bool
	Impact      string // "17 call sites · exported"

	Similarity float64 // body similarity, below 1 for fuzzy rename or move matches

	// Members are the member-level changes of a struct or interface type
	// whose definition changed, in declaration order. A breaking member
	// change of an exported type sets APIChange.
	Members []MemberChange
}

// Caller is a declaration that refers to a changed symbol.
type Caller struct {
	Package string // import path
	Name    string // qualified with the package name: "server.(*Server).Handle"
	Path    string
	Line    int

	// Calls counts the references the declaration makes: calls, and also
	// uses as a value or type.
	Calls int

	// Approximate is set when some references were resolved by method name
	// only, without knowing the receiver's type.
	Approximate bool
	Test        bool // declared in a _test.go file
}

// BlastRadius is what the commit's changes reach outside themselves. Added
// symbols are left out: their callers are new code of the same commit.
type BlastRadius struct {
	CallSites int
	Packages  []string // import paths of the callers' packages, sorted
	Callers   []Caller // callers of all changed symbols, uses summed, most first

	// APIChanges names the symbols whose exported API changed, in change
	// order. A non-empty list is worth a warning.
	APIChanges  []string
	Approximate bool
}

// ExportedAPIChanged reports that some exported API changed.
func (b *BlastRadius) ExportedAPIChanged() bool { return len(b.APIChanges) > 0 }

// Build builds the report for one commit.
func Build(in Input) *Report {
	mods := newModules(in.GoMod)
	pkgOf := func(filePath, pkgName string) string {
		p := mods.importPath(path.Dir(filePath))
		if strings.HasSuffix(pkgName, "_test") {
			p += "_test"
		}
		return p
	}

	side := func(f *ast.File) *ast.File {
		if f == nil || f.Generated && !in.IncludeGenerated {
			return nil
		}
		return f
	}
	var olds, news []*ast.File
	for _, fd := range in.Files {
		olds = append(olds, side(fd.Old))
		news = append(news, side(fd.New))
	}

	r := &Report{Counts: map[Kind]int{}}
	idx := newIndex(in.Tree, pkgOf)
	blast := map[callerID]*Caller{}
	pkgs := map[string]bool{}

	for _, c := range ast.Diff(olds, news) {
		rc := newChange(c, pkgOf)
		rc.Members = memberChanges(c, pkgOf, idx)
		for _, m := range rc.Members {
			rc.APIChange = rc.APIChange || m.Breaking
		}
		target := c.New
		if target == nil {
			target = c.Old
		}
		rc.Callers, rc.Approximate = idx.callers(target, rc.Package, blastFor(c, blast, pkgs))
		for _, cl := range rc.Callers {
			rc.CallSites += cl.Calls
		}
		rc.Impact = impact(rc)

		r.Changes = append(r.Changes, rc)
		r.Symbols++
		for _, k := range rc.Kinds {
			r.Counts[k]++
			r.SemanticChanges++
		}
		if rc.APIChange {
			r.Blast.APIChanges = append(r.Blast.APIChanges, rc.Name)
		}
	}

	for _, cl := range blast {
		r.Blast.Callers = append(r.Blast.Callers, *cl)
		r.Blast.CallSites += cl.Calls
		r.Blast.Approximate = r.Blast.Approximate || cl.Approximate
	}
	sortCallers(r.Blast.Callers)
	for p := range pkgs {
		r.Blast.Packages = append(r.Blast.Packages, p)
	}
	slices.Sort(r.Blast.Packages)

	r.summarizeFiles(in)
	return r
}

// blastFor returns the function that records a change's callers in the blast
// radius, or nil for changes that do not count towards it.
func blastFor(c ast.Change, blast map[callerID]*Caller, pkgs map[string]bool) func(callerID, Caller) {
	if c.Flags.Has(ast.Added) {
		return nil
	}
	return func(id callerID, cl Caller) {
		pkgs[cl.Package] = true
		if b := blast[id]; b != nil {
			b.Calls += cl.Calls
			b.Approximate = b.Approximate || cl.Approximate
			return
		}
		blast[id] = &cl
	}
}

func newChange(c ast.Change, pkgOf func(path, pkg string) string) Change {
	cur := c.New
	if cur == nil {
		cur = c.Old
	}
	rc := Change{
		Kind:     primaryKind(c.Flags),
		Kinds:    kindsOf(c.Flags),
		Flags:    c.Flags,
		Package:  pkgOf(cur.Path, cur.Package),
		Name:     qualified(cur),
		Symbol:   cur.Kind.String(),
		Path:     cur.Path,
		Line:     cur.StartLine,
		Exported: c.Old != nil && c.Old.Exported || c.New != nil && c.New.Exported,

		Similarity: c.Similarity,
	}
	if o := c.Old; o != nil {
		rc.OldPath, rc.OldLine = o.Path, o.StartLine
		rc.OldSignature = o.Signature
		rc.StmtsBefore, rc.ComplexityBefore = o.Stmts, o.Complexity
		if c.Flags.Has(ast.Renamed) {
			rc.OldName = qualified(o)
		}
		movedPkg := c.New != nil && pkgOf(o.Path, o.Package) != rc.Package
		rc.APIChange = isAPI(o) && (c.Flags.Has(ast.Deleted) || c.Flags.Has(ast.Renamed) ||
			c.Flags.Has(ast.SignatureChanged) || movedPkg)
	}
	if n := c.New; n != nil {
		rc.NewSignature = n.Signature
		rc.StmtsAfter, rc.ComplexityAfter = n.Stmts, n.Complexity
	}
	rc.Summary = summary(c)
	return rc
}

// isAPI reports whether a symbol can be used from other packages.
func isAPI(s *ast.Symbol) bool {
	if !s.Exported || s.Package == "main" || strings.HasSuffix(s.Package, "_test") {
		return false
	}
	return s.Recv == "" || goast.IsExported(s.Recv)
}

func qualified(s *ast.Symbol) string { return s.Package + "." + s.QualifiedName() }

// summary describes a change in one line: "+4 stmts · cyclomatic 7 → 9".
func summary(c ast.Change) string {
	isFunc := func(s *ast.Symbol) bool { return s.Kind == ast.KindFunc || s.Kind == ast.KindMethod }
	var parts []string
	switch {
	case c.Flags.Has(ast.Added):
		if !isFunc(c.New) {
			return "new " + c.New.Kind.String()
		}
		return plural(c.New.Stmts, "stmt") + fmt.Sprintf(" · cyclomatic %d", c.New.Complexity)
	case c.Flags.Has(ast.Deleted):
		if !isFunc(c.Old) {
			return c.Old.Kind.String() + " removed"
		}
		return stmtDelta(-c.Old.Stmts)
	}

	o, n := c.Old, c.New
	if c.Flags.Has(ast.Renamed) {
		parts = append(parts, "renamed from "+o.QualifiedName())
	}
	if c.Flags.Has(ast.Moved) {
		parts = append(parts, "moved from "+o.Path)
	}
	switch {
	case c.Flags.Has(ast.BodyChanged) && isFunc(n):
		var body []string
		if d := n.Stmts - o.Stmts; d != 0 {
			body = append(body, stmtDelta(d))
		}
		if o.Complexity != n.Complexity {
			body = append(body, fmt.Sprintf("cyclomatic %d → %d", o.Complexity, n.Complexity))
		}
		if body == nil {
			body = []string{"body edited, same size"}
		}
		parts = append(parts, body...)
	case c.Flags.Has(ast.BodyChanged) && len(c.Members) > 0:
		parts = append(parts, memberSummary(c.Members))
	case c.Flags.Has(ast.BodyChanged):
		parts = append(parts, "definition changed")
	case c.Flags.Has(ast.SignatureChanged):
		parts = append(parts, "body unchanged")
	}
	return strings.Join(parts, " · ")
}

// impact describes a change's reach: "17 call sites · 4 field uses · exported".
func impact(c Change) string {
	sites := "no callers"
	if c.CallSites > 0 {
		sites = plural(c.CallSites, "call site")
		if c.Approximate {
			sites += " (approx.)"
		}
	}
	uses, approx := 0, false
	for _, m := range c.Members {
		uses += m.Uses
		approx = approx || m.Approximate
	}
	if uses > 0 {
		sites += " · " + plural(uses, "field use")
		if approx {
			sites += " (approx.)"
		}
	}
	if c.Exported {
		return sites + " · exported"
	}
	return sites
}

// summarizeFiles fills in the line counts, the file list and the noise list.
func (r *Report) summarizeFiles(in Input) {
	type touch struct {
		kinds   map[Kind]bool
		changes int
	}
	touched := map[string]*touch{}
	at := func(p string) *touch {
		t := touched[p]
		if t == nil {
			t = &touch{kinds: map[Kind]bool{}}
			touched[p] = t
		}
		return t
	}
	for _, c := range r.Changes {
		t := at(c.Path)
		t.changes++
		for _, k := range c.Kinds {
			t.kinds[k] = true
		}
		if c.OldPath != "" && c.OldPath != c.Path {
			at(c.OldPath).kinds[KindMoved] = true
		}
	}

	for _, fd := range in.Files {
		r.Added += fd.Added
		r.Removed += fd.Removed

		var fs FileSummary
		if fd.New != nil {
			fs.Path = fd.New.Path
		}
		if fd.Old != nil {
			if fs.Path == "" {
				fs.Path = fd.Old.Path
			} else if fd.Old.Path != fs.Path {
				fs.OldPath = fd.Old.Path
			}
		}
		if fs.Path == "" {
			continue
		}
		fs.Added, fs.Removed = fd.Added, fd.Removed

		var ts []*touch
		for _, p := range []string{fs.Path, fs.OldPath} {
			if t := touched[p]; p != "" && t != nil {
				ts = append(ts, t)
			}
		}
		if len(ts) == 0 {
			if reason := noiseReason(fd, in.IncludeGenerated); reason != "" {
				r.Noise = append(r.Noise, NoiseFile{Path: fs.Path, Reason: reason, Added: fd.Added, Removed: fd.Removed})
				continue
			}
		}
		for _, t := range ts {
			fs.Changes += t.changes
			for _, k := range Kinds {
				if t.kinds[k] && !slices.Contains(fs.Kinds, k) {
					fs.Kinds = append(fs.Kinds, k)
				}
			}
		}
		slices.Sort(fs.Kinds)
		r.Files = append(r.Files, fs)
	}
	slices.SortFunc(r.Files, func(a, b FileSummary) int { return cmp.Compare(a.Path, b.Path) })
	slices.SortFunc(r.Noise, func(a, b NoiseFile) int { return cmp.Compare(a.Path, b.Path) })
}

// noiseReason says why a file without symbol changes is noise, or returns ""
// when its edit still matters, e.g. a changed import list or package clause.
func noiseReason(fd FileDiff, includeGenerated bool) string {
	if !includeGenerated && (fd.Old != nil && fd.Old.Generated || fd.New != nil && fd.New.Generated) {
		return "generated"
	}
	if fd.Old == nil || fd.New == nil || fd.Old.Package != fd.New.Package {
		return ""
	}
	if !slices.Equal(sortedImports(fd.Old), sortedImports(fd.New)) {
		return ""
	}
	return "formatting or comments"
}

func sortedImports(f *ast.File) []ast.Import {
	ims := slices.Clone(f.Imports)
	slices.SortFunc(ims, func(a, b ast.Import) int { return cmp.Or(cmp.Compare(a.Path, b.Path), cmp.Compare(a.Name, b.Name)) })
	return ims
}

func sortCallers(cs []Caller) {
	slices.SortFunc(cs, func(a, b Caller) int {
		return cmp.Or(cmp.Compare(b.Calls, a.Calls), cmp.Compare(a.Package, b.Package), cmp.Compare(a.Path, b.Path), cmp.Compare(a.Line, b.Line))
	})
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// stmtDelta formats a change in statement count with an explicit sign,
// using a true minus sign: "+4 stmts", "−1 stmt".
func stmtDelta(d int) string {
	if d < 0 {
		return "−" + plural(-d, "stmt")
	}
	return "+" + plural(d, "stmt")
}
