// Package journey replays a lineage.History as a sequence of frames for the
// journey view: per frame, how many symbols each package directory holds,
// how it was edited, and which packages refer to which.
//
// Edits per directory come from the code map's aggregation (codemap), so a
// symbol moved across directories counts as an edit of the directory it
// moved to, as on the code map. Live symbols and dependencies come from
// replaying the history revision by revision.
package journey

import (
	"cmp"
	"math"
	"path"
	"slices"
	"strings"
	"time"

	"huginn/internal/ast"
	"huginn/internal/codemap"
	"huginn/internal/lineage"
)

// Step is how much history one frame covers.
type Step uint8

const (
	Commit Step = iota + 1 // one revision per frame
	Week                   // one ISO week per frame, Monday 00:00 UTC
	Month                  // one calendar month per frame, UTC
)

// ParseStep parses "commit", "week" or "month".
func ParseStep(s string) (Step, bool) {
	switch s {
	case "commit":
		return Commit, true
	case "week":
		return Week, true
	case "month":
		return Month, true
	}
	return 0, false
}

func (s Step) String() string {
	switch s {
	case Commit:
		return "commit"
	case Week:
		return "week"
	case Month:
		return "month"
	}
	return "unknown"
}

// bucket returns the start of the week or month t falls in.
func (s Step) bucket(t time.Time) time.Time {
	if s == Month {
		t = t.UTC()
		return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
	}
	return codemap.WeekOf(t)
}

func (s Step) next(t time.Time) time.Time {
	if s == Month {
		return t.AddDate(0, 1, 0)
	}
	return t.AddDate(0, 0, 7)
}

// Package is a directory that held live symbols at some point.
type Package struct {
	Path string
	Peak int // most live symbols it ever held

	// X, Y and R place the package's bubble at its peak size in the unit
	// square. The layout covers the whole history, so it never moves.
	X, Y, R float64
	Group   int // the innermost group around it, an index into Journey.Groups; -1 for none
}

// PackageState is one package in one frame.
type PackageState struct {
	Package int // index into Journey.Packages
	Live    int // symbols at the end of the frame
	Edits   codemap.Kinds

	// Who knows the package at the end of the frame, for one with live
	// symbols: the top knowers, most first; the share held by departed
	// authors; and the fewest authors who together hold more than half.
	Knowers   []Knower
	Orphaned  float64
	BusFactor int
}

// Dep is a dependency between two packages: Refs live symbols in From refer
// to something in To.
type Dep struct {
	From, To int // indexes into Journey.Packages
	Refs     int
}

// Frame is one step of the history.
type Frame struct {
	// Start and End bound the frame, [Start, End). For a commit frame both
	// are the commit's time.
	Start, End time.Time
	SHA        string // for a commit frame

	Commits int // revisions in the frame
	Total   int // revisions up to the end of the frame
	Live    int // symbols at the end of the frame

	// Packages holds the packages with live symbols or edits, by index.
	Packages []PackageState
	Deps     []Dep // at the end of the frame, by From then To
}

// Journey is a history cut into frames.
type Journey struct {
	Step     Step
	Packages []Package // sorted by path
	Groups   []Group   // outermost first, so every parent comes before its children
	Authors  []string  // commit authors, sorted
	Frames   []Frame
}

// Options configure Build.
type Options struct {
	// Tests counts symbols declared in _test.go files. The history must
	// have been built with them for this to make a difference.
	Tests bool
}

// Build replays h in frames of the given step. m must be codemap.Build(h).
//
// Revisions are replayed in the order they were applied. Author times need
// not grow along that order; a revision dated before the frame its
// predecessor fell in is counted in that frame instead, so the frames stay
// in order.
func Build(h *lineage.History, m *codemap.Map, step Step, opts Options) *Journey {
	r := newReplay(h, opts.Tests)
	j := &Journey{Step: step, Authors: r.know.authors}
	if h.Revisions() == 0 {
		return j
	}

	type window struct {
		start, end time.Time
		sha        string
		from, to   int // revisions [from, to)
	}
	var windows []window
	n := h.Revisions()
	if step == Commit {
		for i := range n {
			sha, t := h.RevisionAt(i)
			windows = append(windows, window{start: t, end: t, sha: sha, from: i, to: i + 1})
		}
	} else {
		_, first := h.RevisionAt(0)
		cur := step.bucket(first)
		w := window{start: cur, end: step.next(cur)}
		for i := range n {
			_, t := h.RevisionAt(i)
			for !t.Before(w.end) {
				w.to = i
				windows = append(windows, w)
				w = window{start: w.end, end: step.next(w.end), from: i}
			}
		}
		w.to = n
		windows = append(windows, w)
	}

	var frames []Frame
	var now time.Time
	for _, w := range windows {
		for i := w.from; i < w.to; i++ {
			r.apply(i)
		}
		f := Frame{Start: w.start, End: w.end, SHA: w.sha, Commits: w.to - w.from, Total: w.to, Live: r.total}
		edits := map[string]codemap.Kinds{}
		for _, d := range m.Dirs() {
			var k codemap.Kinds
			if step == Commit {
				// Touches are sorted by time, so look in the commit's
				// second for its own.
				for t := range d.Touches(w.start, w.start.Add(time.Second), opts.Tests) {
					if t.Event.SHA == w.sha {
						k[codemap.KindIndex(t.Kind)]++
					}
				}
			} else {
				k = d.Window(w.start, w.end, opts.Tests, 0).Edits
			}
			if k.Total() > 0 {
				edits[d.Path] = k
			}
		}
		// Departures are judged at the frame's end, or at the latest
		// commit so far when author times run backwards.
		now = later(now, w.end)
		f.Packages = r.snapshot(edits, now)
		f.Deps = r.deps()
		frames = append(frames, f)
	}

	// Index packages by path, now that every one is known.
	paths := slices.Sorted(func(yield func(string) bool) {
		for p := range r.peak {
			if !yield(p) {
				return
			}
		}
	})
	idx := map[string]int{}
	for i, p := range paths {
		idx[p] = i
		j.Packages = append(j.Packages, Package{Path: p, Peak: r.peak[p]})
	}
	for fi := range frames {
		f := &frames[fi]
		for k := range f.Packages {
			f.Packages[k].Package = idx[r.names[f.Packages[k].Package]]
		}
		slices.SortFunc(f.Packages, func(a, b PackageState) int { return cmp.Compare(a.Package, b.Package) })
		for k := range f.Deps {
			f.Deps[k].From, f.Deps[k].To = idx[r.names[f.Deps[k].From]], idx[r.names[f.Deps[k].To]]
		}
		slices.SortFunc(f.Deps, func(a, b Dep) int { return cmp.Or(cmp.Compare(a.From, b.From), cmp.Compare(a.To, b.To)) })
	}
	j.Frames = frames
	layout(j, r.coupling(idx))
	return j
}

// replay walks a history revision by revision, keeping the live symbols per
// directory and the references between directories.
type replay struct {
	tests  bool
	byRev  [][]change // events per revision position
	syms   map[lineage.ID]*ast.Symbol
	live   map[string]int // per directory
	peak   map[string]int // per directory that ever held a symbol
	total  int
	refs   map[[2]string]int // live symbols in [0] referring to [1]
	module *resolver

	// peakRefs is the most refs ever held between two directories at the
	// end of a revision; dirty holds the pairs the current revision moved.
	peakRefs map[[2]string]int
	dirty    [][2]string

	know *knowledge

	// names numbers directories for the frames until they are sorted.
	names []string
	num   map[string]int
}

type change struct {
	id lineage.ID
	e  *lineage.Event
}

func newReplay(h *lineage.History, tests bool) *replay {
	r := &replay{
		tests: tests, byRev: make([][]change, h.Revisions()), syms: map[lineage.ID]*ast.Symbol{},
		live: map[string]int{}, peak: map[string]int{}, refs: map[[2]string]int{}, num: map[string]int{},
		peakRefs: map[[2]string]int{}, know: newKnowledge(h),
	}
	var all []*ast.Symbol
	for sym := range h.All() {
		for i := range sym.Events {
			e := &sym.Events[i]
			if pos, ok := h.Position(e.SHA); ok {
				r.byRev[pos] = append(r.byRev[pos], change{sym.ID, e})
			}
			for _, s := range []*ast.Symbol{e.Old, e.New} {
				if s != nil && r.counts(s) {
					all = append(all, s)
				}
			}
		}
	}
	r.module = newResolver(all)
	return r
}

func (r *replay) counts(s *ast.Symbol) bool {
	return r.tests || !strings.HasSuffix(s.Path, "_test.go")
}

func (r *replay) apply(rev int) {
	for _, c := range r.byRev[rev] {
		old := r.syms[c.id]
		if old != nil {
			r.add(old, -1)
			delete(r.syms, c.id)
		}
		cur := false
		if s := c.e.New; s != nil && r.counts(s) {
			r.syms[c.id] = s
			r.add(s, 1)
			cur = true
		}
		r.know.apply(c.id, c.e, old, cur)
	}
	for _, k := range r.dirty {
		r.peakRefs[k] = max(r.peakRefs[k], r.refs[k])
	}
	r.dirty = r.dirty[:0]
}

// add counts s in or, with delta -1, out.
func (r *replay) add(s *ast.Symbol, delta int) {
	dir := path.Dir(s.Path)
	r.live[dir] += delta
	r.total += delta
	if r.live[dir] > r.peak[dir] {
		r.peak[dir] = r.live[dir]
	}
	for _, to := range r.module.targets(s) {
		if to == dir {
			continue
		}
		k := [2]string{dir, to}
		r.dirty = append(r.dirty, k)
		r.refs[k] += delta
		if r.refs[k] == 0 {
			delete(r.refs, k)
		}
	}
}

// coupling weighs every pair of directories that ever referred to each
// other: one for each direction that imports the other, plus the log of the
// most references, both ways, that ever held between them at once. It is
// taken per revision, so it does not depend on the step.
func (r *replay) coupling(idx map[string]int) []spring {
	type pair struct{ dirs, refs int }
	pairs := map[[2]int]pair{}
	for k, n := range r.peakRefs {
		a, okA := idx[k[0]]
		b, okB := idx[k[1]]
		if !okA || !okB || n == 0 {
			continue
		}
		key := [2]int{min(a, b), max(a, b)}
		p := pairs[key]
		p.dirs++
		p.refs += n
		pairs[key] = p
	}
	var out []spring
	for k, p := range pairs {
		out = append(out, spring{k[0], k[1], float64(p.dirs) + math.Log1p(float64(p.refs))})
	}
	// Map order is random; the layout must not depend on it.
	slices.SortFunc(out, func(p, q spring) int { return cmp.Or(cmp.Compare(p.a, q.a), cmp.Compare(p.b, q.b)) })
	return out
}

func (r *replay) id(dir string) int {
	n, ok := r.num[dir]
	if !ok {
		n = len(r.names)
		r.names = append(r.names, dir)
		r.num[dir] = n
	}
	return n
}

// snapshot returns the directories with live symbols or edits, numbered by
// r.id, with who knows them at now.
func (r *replay) snapshot(edits map[string]codemap.Kinds, now time.Time) []PackageState {
	var out []PackageState
	for dir, n := range r.live {
		if n > 0 || edits[dir].Total() > 0 {
			p := PackageState{Package: r.id(dir), Live: n, Edits: edits[dir]}
			if n > 0 {
				p.Knowers, p.Orphaned, p.BusFactor = r.know.state(dir, now)
			}
			out = append(out, p)
		}
	}
	for dir, k := range edits {
		if _, ok := r.live[dir]; !ok {
			// Edited but never live, e.g. a symbol added and deleted
			// within one commit: it still needs a place on the stage.
			out = append(out, PackageState{Package: r.id(dir), Edits: k})
			r.peak[dir] = max(r.peak[dir], 0)
		}
	}
	return out
}

func (r *replay) deps() []Dep {
	var out []Dep
	for k, n := range r.refs {
		if r.live[k[1]] > 0 {
			out = append(out, Dep{From: r.id(k[0]), To: r.id(k[1]), Refs: n})
		}
	}
	return out
}

// resolver maps the import paths in symbol references to repository
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
type resolver struct {
	modules []string // longest first
	dirs    map[string]bool
	cache   map[string]string
}

func newResolver(syms []*ast.Symbol) *resolver {
	r := &resolver{dirs: map[string]bool{}, cache: map[string]string{}}
	seen := map[string]bool{}
	var imports []string
	for _, s := range syms {
		r.dirs[path.Dir(s.Path)] = true
		for _, ref := range s.Refs {
			if ref.Kind == ast.RefImport && !seen[ref.Pkg] {
				seen[ref.Pkg] = true
				imports = append(imports, ref.Pkg)
			}
		}
	}
	slices.Sort(imports)
	first := 0
	for len(imports) > 0 {
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
		mod, best := "", 0
		for m, n := range matches {
			if n > best || n == best && m < mod {
				mod, best = m, n
			}
		}
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

// under returns the directory of the package imported as imp if it is in
// module mod, or "".
func (r *resolver) under(mod, imp string) string {
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

// dir returns the directory of the package imported as imp, or "".
func (r *resolver) dir(imp string) string {
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

// targets returns the directories s refers to, each once, sorted.
func (r *resolver) targets(s *ast.Symbol) []string {
	var out []string
	for _, ref := range s.Refs {
		if ref.Kind != ast.RefImport {
			continue
		}
		if d := r.dir(ref.Pkg); d != "" && !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	slices.Sort(out)
	return out
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
