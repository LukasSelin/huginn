// Package health summarizes how a whole codebase evolved, from a
// lineage.History: how its size, exported surface and complexity grew, how
// much of each stretch of work restructured code rather than adding it, how
// long new code survives, where edits and complexity pile up, and how
// concentrated authorship is.
//
// Test code (_test.go files) is kept apart: it never counts as source, and
// only adds to the test line counts when the history kept it.
package health

import (
	"cmp"
	"path"
	"slices"
	"strings"
	"time"

	"huginn/internal/ast"
	"huginn/internal/codemap"
	"huginn/internal/lineage"
)

// Options tunes Build.
type Options struct {
	// Buckets is the most points a series has; default 40. Histories with
	// fewer revisions get one bucket per revision.
	Buckets int

	// Hotspots is how many hotspot files to keep; default 10.
	Hotspots int
}

// Report is the health summary of one history.
type Report struct {
	Buckets  []Bucket  // oldest first
	Hotspots []Hotspot // highest score first
	Owners   []Owner   // most live symbols first
	Authors  int       // everyone who ever changed a symbol

	// BusFactor is the fewest authors who last touched more than half of
	// the live symbols; 0 without live symbols.
	BusFactor int
}

// Bucket is a run of consecutive revisions.
type Bucket struct {
	First, Last string    // SHAs of the first and last revision
	From, To    time.Time // their times
	Revisions   int

	// The live source code after the bucket's last revision.
	Symbols    int
	Exported   int
	Lines      int // sum of the live symbols' line spans
	Complexity int // sum of the live functions' cyclomatic complexity
	Funcs      int // live functions and methods
	TestLines  int // as Lines, in _test.go files; 0 unless tests were kept

	// Edits in the bucket, by the event's primary kind.
	Edits codemap.Kinds

	// Added counts the symbols first seen in the bucket, Surviving those of
	// them still alive after the last revision, and Fixed those of them a
	// later fix or revert edited.
	Added, Surviving, Fixed int

	// What the bucket's commit messages say they were for (see ReadIntent).
	// Commits leaves out plain branch merges; Fixes counts reverts too.
	Commits, Features, Fixes, Reverts int

	// FixEdits counts the bucket's edits made by fixes and reverts.
	FixEdits int
}

// Restructured counts the bucket's renames, moves and signature changes:
// edits that reshape existing code rather than add, delete or fill it in.
func (b Bucket) Restructured() int {
	return b.Edits.Of(ast.Renamed) + b.Edits.Of(ast.Moved) + b.Edits.Of(ast.SignatureChanged)
}

// Hotspot is a file of live code that is both often edited and complex.
type Hotspot struct {
	Path       string
	Edits      int // semantic edits over the whole history
	Fixes      int // of them, made by fixes and reverts
	Authors    int // who made them
	Complexity int // of its live functions
	Lines      int
	Score      int // Edits × Complexity

	// Top is its most complex live symbol.
	Top           lineage.ID
	TopName       string
	TopComplexity int
}

// Owner is an author and how many live symbols they touched last.
type Owner struct {
	Name    string
	Symbols int
}

// Build summarizes h. h must not change afterwards.
func Build(h *lineage.History, opts Options) *Report {
	if opts.Buckets <= 0 {
		opts.Buckets = 40
	}
	if opts.Hotspots <= 0 {
		opts.Hotspots = 10
	}
	r := &Report{}
	intents := make([]Intent, h.Revisions())
	for i := range intents {
		intents[i] = ReadIntent(h.MessageAt(i))
	}
	r.Buckets = buckets(h, intents, opts.Buckets)
	r.Hotspots = hotspots(h, intents, opts.Hotspots)
	r.Owners, r.Authors, r.BusFactor = owners(h)
	return r
}

// step is one event placed in the line of revisions.
type step struct {
	rev   int
	sym   *lineage.Symbol
	event *lineage.Event
	first bool // the symbol's first event
}

// buckets replays every event in revision order, splitting the revisions
// into at most n buckets of near-equal size.
func buckets(h *lineage.History, intents []Intent, n int) []Bucket {
	revs := h.Revisions()
	if revs == 0 {
		return []Bucket{}
	}
	n = min(n, revs)
	steps := sortedSteps(h)

	out := make([]Bucket, n)
	var cur Bucket // running state of the live code
	next := 0
	for b := range out {
		lo, hi := b*revs/n, (b+1)*revs/n // revisions [lo, hi)
		bk := &out[b]
		bk.Revisions = hi - lo
		bk.First, bk.From = h.RevisionAt(lo)
		bk.Last, bk.To = h.RevisionAt(hi - 1)
		countIntents(bk, intents[lo:hi])
		for ; next < len(steps) && steps[next].rev < hi; next++ {
			replayStep(h, intents, bk, &cur, steps[next])
		}
		bk.Symbols, bk.Exported, bk.Lines, bk.Complexity, bk.Funcs, bk.TestLines =
			cur.Symbols, cur.Exported, cur.Lines, cur.Complexity, cur.Funcs, cur.TestLines
	}
	return out
}

// sortedSteps places every event of h at its revision, oldest first. Events
// of the same revision keep the order the history gives them.
func sortedSteps(h *lineage.History) []step {
	var steps []step
	for sym := range h.All() {
		for i := range sym.Events {
			e := &sym.Events[i]
			rev, ok := h.Position(e.SHA)
			if !ok {
				continue
			}
			steps = append(steps, step{rev: rev, sym: sym, event: e, first: i == 0})
		}
	}
	slices.SortStableFunc(steps, func(a, b step) int { return cmp.Compare(a.rev, b.rev) })
	return steps
}

// countIntents adds what the bucket's commit messages say they were for.
func countIntents(bk *Bucket, intents []Intent) {
	for _, in := range intents {
		if in == Merge {
			continue
		}
		bk.Commits++
		switch in {
		case Feature:
			bk.Features++
		case Fix:
			bk.Fixes++
		case Revert:
			bk.Fixes++
			bk.Reverts++
		case Other, Merge:
			// Counted as commits only; merges were skipped above.
		}
	}
}

// replayStep applies one event to the live code in cur and counts it in bk.
// Test code changes only the test line count.
func replayStep(h *lineage.History, intents []Intent, bk, cur *Bucket, st step) {
	e := st.event
	if e.Old != nil {
		applyLive(cur, e.Old, -1)
	}
	if e.New != nil {
		applyLive(cur, e.New, +1)
	}
	if isTest(latestPath(e)) {
		return
	}
	if k := e.Primary(); k != 0 {
		bk.Edits[codemap.KindIndex(k)]++
		if intents[st.rev].fixes() {
			bk.FixEdits++
		}
	}
	if st.first && e.Old == nil {
		bk.Added++
		if st.sym.Alive() {
			bk.Surviving++
		}
		if fixedLater(h, intents, st.sym) {
			bk.Fixed++
		}
	}
}

// applyLive adds s to the live code in cur when sign is +1, or takes it away
// when sign is -1.
func applyLive(cur *Bucket, s *ast.Symbol, sign int) {
	span := (s.EndLine - s.StartLine + 1) * sign
	if isTest(s.Path) {
		cur.TestLines += span
		return
	}
	cur.Symbols += sign
	cur.Lines += span
	cur.Complexity += s.Complexity * sign
	if s.Exported {
		cur.Exported += sign
	}
	if s.Kind == ast.KindFunc || s.Kind == ast.KindMethod {
		cur.Funcs += sign
	}
}

// fixedLater reports whether a fix or revert edited sym after adding it.
func fixedLater(h *lineage.History, intents []Intent, sym *lineage.Symbol) bool {
	for _, e := range sym.Events[1:] {
		if rev, ok := h.Position(e.SHA); ok && e.Primary() != 0 && intents[rev].fixes() {
			return true
		}
	}
	return false
}

// hotspots ranks the files of live source code by edits × complexity.
func hotspots(h *lineage.History, intents []Intent, n int) []Hotspot {
	files := hotFiles{}
	for sym := range h.All() {
		files.addEdits(h, intents, sym)
		files.addLive(sym)
	}
	return files.ranked(n)
}

// hotFile is a hotspot being gathered, with the set of its authors.
type hotFile struct {
	Hotspot
	authors map[string]bool
}

// hotFiles gathers hotspots by file path.
type hotFiles map[string]*hotFile

// get returns the hotspot of path p, starting it on first use.
func (files hotFiles) get(p string) *hotFile {
	f := files[p]
	if f == nil {
		f = &hotFile{Hotspot: Hotspot{Path: p}, authors: map[string]bool{}}
		files[p] = f
	}
	return f
}

// addEdits counts sym's semantic edits against the files they left it in.
func (files hotFiles) addEdits(h *lineage.History, intents []Intent, sym *lineage.Symbol) {
	for i := range sym.Events {
		e := &sym.Events[i]
		p := latestPath(e)
		if e.Primary() == 0 || isTest(p) {
			continue
		}
		f := files.get(p)
		f.Edits++
		f.authors[e.Author] = true
		if rev, ok := h.Position(e.SHA); ok && intents[rev].fixes() {
			f.Fixes++
		}
	}
}

// addLive adds sym's size and complexity to its file, if it is live source.
func (files hotFiles) addLive(sym *lineage.Symbol) {
	if !sym.Alive() {
		return
	}
	cur := sym.Latest()
	if isTest(cur.Path) {
		return
	}
	f := files.get(cur.Path)
	f.Complexity += cur.Complexity
	f.Lines += cur.EndLine - cur.StartLine + 1
	if f.Top == 0 || cur.Complexity > f.TopComplexity {
		f.Top, f.TopName, f.TopComplexity = sym.ID, cur.QualifiedName(), cur.Complexity
	}
}

// ranked scores the files and keeps the n highest above zero; ties go by
// path.
func (files hotFiles) ranked(n int) []Hotspot {
	out := []Hotspot{}
	for _, f := range files {
		f.Authors = len(f.authors)
		f.Score = f.Edits * f.Complexity
		if f.Score > 0 {
			out = append(out, f.Hotspot)
		}
	}
	slices.SortFunc(out, func(a, b Hotspot) int { return cmp.Or(cmp.Compare(b.Score, a.Score), cmp.Compare(a.Path, b.Path)) })
	return out[:min(len(out), n)]
}

// owners credits each live source symbol to whoever changed it last.
func owners(h *lineage.History) (out []Owner, authors, busFactor int) {
	all := map[string]bool{}
	last := map[string]int{}
	live := 0
	for sym := range h.All() {
		for _, e := range sym.Events {
			if !isTest(latestPath(&e)) {
				all[e.Author] = true
			}
		}
		if !sym.Alive() || isTest(sym.Latest().Path) {
			continue
		}
		last[sym.Events[len(sym.Events)-1].Author]++
		live++
	}
	out = []Owner{}
	for name, n := range last {
		out = append(out, Owner{name, n})
	}
	slices.SortFunc(out, func(a, b Owner) int {
		return cmp.Or(cmp.Compare(b.Symbols, a.Symbols), strings.Compare(a.Name, b.Name))
	})
	covered := 0
	for _, o := range out {
		if 2*covered > live {
			break
		}
		covered += o.Symbols
		busFactor++
	}
	return out, len(all), busFactor
}

// latestPath is where the event leaves the symbol: its file after the event,
// or before it for a deletion.
func latestPath(e *lineage.Event) string {
	if e.New != nil {
		return e.New.Path
	}
	return e.Old.Path
}

func isTest(p string) bool { return strings.HasSuffix(path.Base(p), "_test.go") }
