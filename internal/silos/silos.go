// Package silos finds knowledge silos in the live code of a history: code
// few people know that much depends on, what would be left without anyone
// who knows it if some authors left, and code that is coupled across the
// line between who knows what.
//
// Who knows what comes from the knowledge model (internal/knowledge) as of
// the last revision, which is also when departed authors are counted. What
// depends on a declaration comes from the references between the live
// declarations, resolved syntactically as semdiff resolves callers. Test code
// (_test.go files) is left out.
package silos

import (
	"cmp"
	"fmt"
	"math"
	"path"
	"slices"
	"strings"
	"time"

	"huginn/internal/ast"
	"huginn/internal/knowledge"
	"huginn/internal/lineage"
	"huginn/internal/modpath"
	"huginn/internal/semdiff"
)

// Options tunes Build.
type Options struct {
	// Top is how many entries each ranked list keeps; default 20.
	Top int

	// MinKnown is the share of a symbol or package the authors still
	// around must know for it not to count as orphaned; default 1/3.
	MinKnown float64
}

const (
	maxKnowers   = 5
	crossWeight  = 3   // a caller in another package counts this many times
	approxWeight = 0.5 // a caller matched by method name only counts this much

	// minConcentration is the least share a silo loses with its holder.
	minConcentration = 0.5
)

// Unit is a live symbol or a package directory.
type Unit struct {
	ID   lineage.ID // the symbol; 0 for a package
	Name string     // qualified name, "(*Repo).Walk", or the directory
	Path string     // the symbol's file, or the directory
	Dir  string     // the package directory
}

// Knower is an author's share of what is known of a unit.
type Knower struct {
	Name     string
	Share    float64 // 0 to 1
	Departed bool    // not around: no commits for six months, or leaving
}

// Spread is how what is known of a unit is held.
type Spread struct {
	Knowers []Knower // most first, at most five
	Known   float64  // the share held by authors still around

	// Holder is the author still around who knows the most of it, and
	// Held their share; Holder is empty when nobody around knows it.
	Holder string
	Held   float64

	// BusFactor is the fewest authors still around who together hold more
	// than half of what those authors know; 0 when nobody around knows it.
	BusFactor int
}

// Concentration is the share of what is known of the unit that is lost if
// its holder leaves: what departed authors hold, plus the holder's share.
func (s Spread) Concentration() float64 { return min(1, 1-s.Known+s.Held) }

// Dependence is how much depends on a unit and how hard it is to change.
type Dependence struct {
	// Callers counts the declarations referring to the unit (for a
	// package, from other packages), Packages the other packages they are
	// in, and Approximate those matched by method name only. CallSites
	// counts their references.
	Callers, Packages, Approximate, CallSites int

	// FanIn is Callers weighted: three times from another package, half
	// when matched approximately.
	FanIn float64

	Complexity int // cyclomatic, of its functions
	Edits      int // semantic edits over its whole history
}

// Criticality grows with fan-in, complexity and edits, each on a log scale
// so that none of them swamps the others; it is 1 for a constant nobody
// refers to and nobody edited since adding it.
func (d Dependence) Criticality() float64 {
	return math.Log2(2+d.FanIn) * math.Log2(2+float64(d.Complexity)) * math.Log2(2+float64(d.Edits))
}

// Risk is a silo: a unit whose holder alone knows most of what is known
// of it, scored by how much depends on it.
type Risk struct {
	Unit
	Spread
	Dependence
	Score   float64 // Concentration × Criticality
	Reasons []string
}

// Report is the knowledge silos of one history.
type Report struct {
	Now      time.Time // the last revision
	Authors  []string  // commit authors, sorted
	Departed []string  // of them, those not around at Now

	// Knowers is everyone who knows some of the live code, by their share
	// of all that is known of it, most first.
	Knowers []Knower

	// The silos, highest score first, at most Options.Top of each, and how
	// many there are in all.
	Symbols, Packages         []Risk
	SymbolSilos, PackageSilos int

	// Units coupled across knowledge boundaries, highest score first, at
	// most Options.Top of each.
	SymbolPairs, PackagePairs []Pair

	a *analysis
}

// analysis is the state the report is computed from, kept for Leave.
type analysis struct {
	opts  Options
	model *knowledge.Model
	now   time.Time
	syms  []*unit // live source symbols, in ID order
	pkgs  []*unit // their directories, sorted

	symLinks, pkgLinks map[[2]int]*link // by unit indexes, lower first
}

type unit struct {
	Unit
	sym *ast.Symbol        // nil for a package
	ws  []knowledge.Weight // by author
	dep Dependence
	pkg int // index into analysis.pkgs, for a symbol
}

// Build finds the silos of h. h must not change afterwards.
func Build(h *lineage.History, opts Options) *Report {
	if opts.Top <= 0 {
		opts.Top = 20
	}
	if opts.MinKnown <= 0 {
		opts.MinKnown = 1.0 / 3
	}
	a := &analysis{opts: opts, symLinks: map[[2]int]*link{}, pkgLinks: map[[2]int]*link{}}
	a.model = knowledge.Replay(h, func(s *ast.Symbol) bool { return !isTest(s.Path) })
	_, a.now = h.Span()
	a.collect(h)
	a.depend()

	r := &Report{Now: a.now, Authors: a.model.Authors(), Departed: []string{}, a: a}
	for i, name := range r.Authors {
		if a.departed(i) {
			r.Departed = append(r.Departed, name)
		}
	}
	r.Knowers = a.knowers()
	r.Symbols, r.SymbolSilos = a.risks(a.syms)
	r.Packages, r.PackageSilos = a.risks(a.pkgs)
	r.SymbolPairs, r.PackagePairs = a.couplings(h)
	return r
}

// collect gathers the live source symbols and their directories.
func (a *analysis) collect(h *lineage.History) {
	byDir := map[string][]*unit{}
	for sym := range h.All() {
		if !sym.Alive() || isTest(sym.Latest().Path) {
			continue
		}
		cur := sym.Latest()
		u := &unit{Unit: Unit{ID: sym.ID, Name: cur.QualifiedName(), Path: cur.Path, Dir: path.Dir(cur.Path)},
			sym: cur, ws: a.model.Weights(sym.ID)}
		u.dep.Complexity = cur.Complexity
		for _, e := range sym.Events {
			if e.Primary() != 0 && !isTest(latestPath(&e)) {
				u.dep.Edits++
			}
		}
		a.syms = append(a.syms, u)
		byDir[u.Dir] = append(byDir[u.Dir], u)
	}
	dirs := make([]string, 0, len(byDir))
	for d := range byDir {
		dirs = append(dirs, d)
	}
	slices.Sort(dirs)
	for i, d := range dirs {
		p := &unit{Unit: Unit{Name: d, Path: d, Dir: d}}
		by := map[int]float64{}
		for _, u := range byDir[d] {
			u.pkg = i
			knowledge.Sum(by, u.ws)
			p.dep.Complexity += u.dep.Complexity
			p.dep.Edits += u.dep.Edits
		}
		for a, w := range by {
			p.ws = append(p.ws, knowledge.Weight{Author: a, W: w})
		}
		slices.SortFunc(p.ws, func(x, y knowledge.Weight) int { return cmp.Compare(x.Author, y.Author) })
		a.pkgs = append(a.pkgs, p)
	}
}

// depend resolves who refers to each live symbol, and through them to each
// package from the others.
func (a *analysis) depend() {
	all := make([]*ast.Symbol, len(a.syms))
	at := map[*ast.Symbol]int{}
	files := map[string]*ast.File{}
	var tree []*ast.File
	for i, u := range a.syms {
		all[i] = u.sym
		at[u.sym] = i
		f := files[u.Path]
		if f == nil {
			f = &ast.File{Path: u.Path, Package: u.sym.Package}
			files[u.Path] = f
			tree = append(tree, f)
		}
		f.Symbols = append(f.Symbols, u.sym)
	}
	// The history keeps no imports, but every import a file uses shows in
	// the references of its symbols.
	for _, f := range tree {
		for _, s := range f.Symbols {
			for _, ref := range s.Refs {
				if ref.Kind == ast.RefImport && !slices.ContainsFunc(f.Imports, func(im ast.Import) bool { return im.Path == ref.Pkg }) {
					f.Imports = append(f.Imports, ast.Import{Path: ref.Pkg})
				}
			}
		}
	}
	mods := modpath.Infer(all)
	pkgOf := func(p, _ string) string { return mods.ImportPath(path.Dir(p)) }
	idx := semdiff.NewIndex(tree, pkgOf)

	type from struct {
		pkg, caller int
	}
	pkgCallers := map[from]bool{} // whether every reference was approximate
	pkgDirs := map[[2]int]bool{}
	for i, u := range a.syms {
		dirs := map[int]bool{}
		for _, use := range idx.Uses(u.sym, pkgOf(u.Path, "")) {
			c, ok := at[use.Caller]
			if !ok {
				continue
			}
			cu := a.syms[c]
			cross := cu.pkg != u.pkg
			u.dep.add(use.Calls, use.Approximate, cross)
			addLink(a.symLinks, i, c, use)
			if !cross {
				continue
			}
			dirs[cu.pkg] = true
			f := from{u.pkg, c}
			approx, seen := pkgCallers[f]
			pkgCallers[f] = use.Approximate && (approx || !seen)
			pkgDirs[[2]int{u.pkg, cu.pkg}] = true
			a.pkgs[u.pkg].dep.CallSites += use.Calls
			addLink(a.pkgLinks, u.pkg, cu.pkg, use)
		}
		u.dep.Packages = len(dirs)
	}
	for f, approx := range pkgCallers {
		a.pkgs[f.pkg].dep.add(0, approx, true)
	}
	for k := range pkgDirs {
		a.pkgs[k[0]].dep.Packages++
	}
}

// knowers ranks everyone by their share of all that is known of the live
// code.
func (a *analysis) knowers() []Knower {
	by := map[int]float64{}
	for _, u := range a.syms {
		knowledge.Sum(by, u.ws)
	}
	ranked := knowledge.Rank(by)
	total := knowledge.Total(ranked)
	out := []Knower{}
	for _, w := range ranked {
		out = append(out, Knower{Name: a.model.Authors()[w.Author], Share: w.W / total, Departed: a.departed(w.Author)})
	}
	return out
}

// add counts one caller with calls references.
func (d *Dependence) add(calls int, approx, cross bool) {
	d.Callers++
	d.CallSites += calls
	w := 1.0
	if cross {
		w = crossWeight
	}
	if approx {
		w *= approxWeight
		d.Approximate++
	}
	d.FanIn += w
}

// departed reports whether author i is not around at the last revision.
func (a *analysis) departed(i int) bool { return a.model.Departed(i, a.now) }

// spread works out how ws is held when gone tells who is not around.
func (a *analysis) spread(ws []knowledge.Weight, gone func(int) bool) Spread {
	ranked := knowledge.Ranked(slices.Clone(ws))
	total := knowledge.Total(ranked)
	if total <= 0 {
		return Spread{}
	}
	var s Spread
	var present []knowledge.Weight
	names := a.model.Authors()
	for _, w := range ranked {
		share := w.W / total
		out := gone(w.Author)
		if !out {
			present = append(present, w)
			s.Known += share
		}
		if len(s.Knowers) < maxKnowers {
			s.Knowers = append(s.Knowers, Knower{Name: names[w.Author], Share: share, Departed: out})
		}
	}
	s.Known = min(1, s.Known)
	s.BusFactor = knowledge.BusFactor(present)
	if len(present) > 0 {
		s.Holder, s.Held = names[present[0].Author], present[0].W/total
	}
	return s
}

// risks ranks the silos among us, returning the top ones and how many
// there are.
func (a *analysis) risks(us []*unit) ([]Risk, int) {
	out := []Risk{}
	for _, u := range us {
		sp := a.spread(u.ws, a.departed)
		c := sp.Concentration()
		if len(sp.Knowers) == 0 || c < minConcentration {
			continue
		}
		out = append(out, Risk{Unit: u.Unit, Spread: sp, Dependence: u.dep, Score: c * u.dep.Criticality(),
			Reasons: append(a.spreadReasons(sp), u.dep.reasons()...)})
	}
	sortBy(out, func(r Risk) (float64, string) { return r.Score, r.key() })
	return out[:min(len(out), a.opts.Top)], len(out)
}

// sortBy sorts xs by score, highest first, then by tie key.
func sortBy[T any](xs []T, key func(T) (float64, string)) {
	slices.SortFunc(xs, func(x, y T) int {
		sx, kx := key(x)
		sy, ky := key(y)
		return cmp.Or(cmp.Compare(sy, sx), strings.Compare(kx, ky))
	})
}

// key orders units with equal scores.
func (u Unit) key() string { return u.Path + "\x00" + u.Name }

func (a *analysis) spreadReasons(s Spread) []string {
	var out []string
	switch {
	case s.Holder == "":
		return []string{"nobody still around knows it"}
	case s.Known < a.opts.MinKnown:
		out = append(out, "orphaned: departed authors hold "+pct(1-s.Known))
	case s.Known <= 0.95:
		out = append(out, "departed authors hold "+pct(1-s.Known))
	}
	out = append(out, fmt.Sprintf("%s holds %s", s.Holder, pct(s.Held)))
	return append(out, fmt.Sprintf("bus factor %d", s.BusFactor))
}

func (d Dependence) reasons() []string {
	var out []string
	if d.Callers > 0 {
		r := fmt.Sprintf("%s from %s", plural(d.CallSites, "call site"), plural(d.Callers, "caller"))
		if d.Packages > 0 {
			r += " in " + plural(d.Packages, "other package")
		}
		if d.Approximate > 0 {
			r += fmt.Sprintf(" (%d approx.)", d.Approximate)
		}
		out = append(out, r)
	}
	if d.Complexity > 0 {
		out = append(out, fmt.Sprintf("cyclomatic %d", d.Complexity))
	}
	if d.Edits > 0 {
		out = append(out, plural(d.Edits, "edit"))
	}
	return out
}

func pct(x float64) string { return fmt.Sprintf("%.0f%%", 100*x) }

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
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
