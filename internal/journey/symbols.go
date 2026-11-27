package journey

import (
	"cmp"
	"math"
	"path"
	"slices"
	"strings"

	"huginn/internal/ast"
	"huginn/internal/lineage"
)

// EventKind says what happened to a symbol in a package view.
type EventKind uint8

const (
	Born      EventKind = iota + 1 // added to the package
	Died                           // deleted from the package
	Renamed                        // renamed within the package
	MovedIn                        // moved here from another package
	MovedOut                       // moved from here to another package
	Signature                      // signature changed within the package
)

func (k EventKind) String() string {
	switch k {
	case Born:
		return "born"
	case Died:
		return "died"
	case Renamed:
		return "renamed"
	case MovedIn:
		return "in"
	case MovedOut:
		return "out"
	case Signature:
		return "signature"
	}
	return "unknown"
}

// SymbolPlace is one symbol of a package view, placed once for the whole
// journey.
type SymbolPlace struct {
	ID        lineage.ID
	Name      string // qualified, as it first appeared in the package
	Kind      ast.Kind
	File      string // base name of the file it first appeared in
	PeakStmts int    // most statements it had while in the package

	// X and Y place it in the unit square, inside the circle of radius
	// 0.5 around the center. Symbols of one file share a wedge.
	X, Y float64
}

// SymbolSize is a live symbol and its statement count.
type SymbolSize struct {
	Symbol int // index into SymbolView.Symbols
	Stmts  int
}

// SymbolEvent is one change to a symbol of the view.
type SymbolEvent struct {
	Symbol int // index into SymbolView.Symbols
	Rev    int // revision index, oldest first
	Kind   EventKind
	Name   string // qualified name after the event; before it for Died
	From   string // the old name for Renamed, the old package for MovedIn
	To     string // the new package for MovedOut
	Sig    string // the new signature for Signature
	OldSig string // the old signature for Signature
}

// SymbolFrame is how a package's symbols changed over one journey frame.
// Replaying the frames in order from an empty package gives the live
// symbols at the end of each.
type SymbolFrame struct {
	Set    []SymbolSize  // symbols that became live or changed size, by Symbol
	Gone   []int         // symbols that were live at the start and are not at the end, sorted
	Events []SymbolEvent // by revision, then symbol
}

// SymbolView is one package's symbols over a journey's frames.
type SymbolView struct {
	Dir       string
	Symbols   []SymbolPlace
	Frames    []SymbolFrame // one per Journey.Frames
	Total     int           // symbols that were ever in the package
	Truncated bool          // only the biggest Limit of Total are shown
}

// SymbolOptions configure Journey.Symbols.
type SymbolOptions struct {
	Tests bool // as for Build

	// Limit bounds the symbols shown; the ones with the most statements
	// are kept. 0 means no limit.
	Limit int
	// Keep is shown even when it falls outside the limit, e.g. a symbol
	// the reader follows.
	Keep lineage.ID
}

// Symbols replays the symbols of package directory dir over j's frames. h
// must be the history j was built from.
func (j *Journey) Symbols(h *lineage.History, dir string, opts SymbolOptions) *SymbolView {
	sc := symbolScope{tests: opts.Tests, dir: dir}
	cands := symbolCands(h, sc)
	v := &SymbolView{Dir: dir, Total: len(cands), Symbols: []SymbolPlace{}}
	cands, v.Truncated = keepBiggest(cands, opts.Limit, opts.Keep)
	// Place in file order, so a file's symbols sit together.
	slices.SortFunc(cands, func(a, b symbolCand) int {
		return cmp.Or(cmp.Compare(a.first.Path, b.first.Path), cmp.Compare(a.first.StartLine, b.first.StartLine), cmp.Compare(a.sym.ID, b.sym.ID))
	})
	idx := map[lineage.ID]int{}
	for i, c := range cands {
		idx[c.sym.ID] = i
		v.Symbols = append(v.Symbols, SymbolPlace{
			ID: c.sym.ID, Name: c.first.QualifiedName(), Kind: c.first.Kind, File: path.Base(c.first.Path), PeakStmts: c.peak,
		})
	}
	placeSymbols(v.Symbols)

	r := symbolReplay{sc: sc, byRev: symbolChanges(h, sc, cands, idx), live: map[int]int{}}
	v.Frames = make([]SymbolFrame, len(j.Frames))
	for fi, f := range j.Frames {
		v.Frames[fi] = r.frame(f)
	}
	return v
}

// symbolScope says which declarations a package view counts.
type symbolScope struct {
	tests bool
	dir   string
}

// counts reports whether s is a declaration the view counts in any package.
func (sc symbolScope) counts(s *ast.Symbol) bool {
	return s != nil && (sc.tests || !strings.HasSuffix(s.Path, "_test.go"))
}

// in reports whether s is a counted declaration in the view's package.
func (sc symbolScope) in(s *ast.Symbol) bool { return sc.counts(s) && path.Dir(s.Path) == sc.dir }

// symbolCand is a symbol that was in the package at some point.
type symbolCand struct {
	sym   *lineage.Symbol
	first *ast.Symbol // first declaration in dir
	rev   int         // of that declaration
	peak  int
}

// symbolCands returns every symbol that was ever in the package, in history
// order.
func symbolCands(h *lineage.History, sc symbolScope) []symbolCand {
	var cands []symbolCand
	for sym := range h.All() {
		if c, ok := newSymbolCand(h, sc, sym); ok {
			cands = append(cands, c)
		}
	}
	return cands
}

// newSymbolCand finds sym's first declaration in the package and the most
// statements it had there; ok is false if it was never there.
func newSymbolCand(h *lineage.History, sc symbolScope, sym *lineage.Symbol) (c symbolCand, ok bool) {
	c = symbolCand{sym: sym, rev: -1}
	for _, e := range sym.Events {
		if !sc.in(e.New) {
			continue
		}
		if c.first == nil {
			c.first = e.New
			c.rev, _ = h.Position(e.SHA)
		}
		c.peak = max(c.peak, e.New.Stmts)
	}
	return c, c.first != nil
}

// keepBiggest cuts cands to limit, keeping keep and then the ones with the
// most statements; truncated says whether it cut any. A limit of 0 keeps
// them all.
func keepBiggest(cands []symbolCand, limit int, keep lineage.ID) (_ []symbolCand, truncated bool) {
	if limit <= 0 || len(cands) <= limit {
		return cands, false
	}
	slices.SortStableFunc(cands, func(a, b symbolCand) int {
		ka, kb := a.sym.ID == keep, b.sym.ID == keep
		if ka != kb {
			if ka {
				return -1
			}
			return 1
		}
		return cmp.Or(cmp.Compare(b.peak, a.peak), cmp.Compare(a.sym.ID, b.sym.ID))
	})
	return cands[:limit], true
}

// symChange is an event of a shown symbol, by the symbol's index in the
// view.
type symChange struct {
	sym int
	e   *lineage.Event
}

// symbolChanges groups the events of the shown symbols that touch the
// package by revision.
func symbolChanges(h *lineage.History, sc symbolScope, cands []symbolCand, idx map[lineage.ID]int) map[int][]symChange {
	byRev := map[int][]symChange{}
	for _, c := range cands {
		for i := range c.sym.Events {
			e := &c.sym.Events[i]
			if !sc.in(e.Old) && !sc.in(e.New) {
				continue
			}
			if rev, ok := h.Position(e.SHA); ok {
				byRev[rev] = append(byRev[rev], symChange{idx[c.sym.ID], e})
			}
		}
	}
	return byRev
}

// symbolReplay keeps the package's live symbols while its frames are
// replayed in order.
type symbolReplay struct {
	sc    symbolScope
	byRev map[int][]symChange
	live  map[int]int // symbol → statements
}

// frame replays the revisions of f and returns how they changed the
// package.
func (r *symbolReplay) frame(f Frame) SymbolFrame {
	start := make(map[int]int, len(r.live))
	for k, n := range r.live {
		start[k] = n
	}
	var evs []SymbolEvent
	for rev := f.Total - f.Commits; rev < f.Total; rev++ {
		evs = r.revision(evs, rev)
	}
	return symbolFrame(start, r.live, evs)
}

// revision applies the changes of revision rev, by symbol, and appends
// their events to evs.
func (r *symbolReplay) revision(evs []SymbolEvent, rev int) []SymbolEvent {
	cs := r.byRev[rev]
	slices.SortFunc(cs, func(a, b symChange) int { return cmp.Compare(a.sym, b.sym) })
	for _, c := range cs {
		_, was := r.live[c.sym]
		evs = r.sc.appendEvents(evs, c.sym, rev, c.e, was)
		if r.sc.in(c.e.New) {
			r.live[c.sym] = c.e.New.Stmts
		} else {
			delete(r.live, c.sym)
		}
	}
	return evs
}

// appendEvents appends what e did to the package at rev to evs, given
// whether symbol sym was live before it.
func (sc symbolScope) appendEvents(evs []SymbolEvent, sym, rev int, e *lineage.Event, was bool) []SymbolEvent {
	now := sc.in(e.New)
	ev := SymbolEvent{Symbol: sym, Rev: rev}
	switch {
	case !was && now:
		return append(evs, sc.arrived(ev, e))
	case was && !now:
		return append(evs, sc.left(ev, e))
	case was && now:
		return appendEdits(evs, ev, e)
	}
	return evs
}

// arrived fills in ev for a symbol that became live: born here, or moved in
// from another package.
func (sc symbolScope) arrived(ev SymbolEvent, e *lineage.Event) SymbolEvent {
	ev.Kind, ev.Name = Born, e.New.QualifiedName()
	if sc.counts(e.Old) && !sc.in(e.Old) {
		ev.Kind, ev.From = MovedIn, path.Dir(e.Old.Path)
	}
	return ev
}

// left fills in ev for a live symbol that went away: deleted, or moved out
// to another package.
func (sc symbolScope) left(ev SymbolEvent, e *lineage.Event) SymbolEvent {
	ev.Kind, ev.Name = Died, e.Old.QualifiedName()
	if sc.counts(e.New) {
		ev.Kind, ev.Name, ev.To = MovedOut, e.New.QualifiedName(), path.Dir(e.New.Path)
	}
	return ev
}

// appendEdits appends the rename and the signature change, if any, of a
// symbol that stayed live.
func appendEdits(evs []SymbolEvent, ev SymbolEvent, e *lineage.Event) []SymbolEvent {
	ev.Name = e.New.QualifiedName()
	if e.Old == nil {
		return evs
	}
	if e.Flags.Has(ast.Renamed) {
		r := ev
		r.Kind, r.From = Renamed, e.Old.QualifiedName()
		evs = append(evs, r)
	}
	if e.Flags.Has(ast.SignatureChanged) {
		s := ev
		s.Kind, s.Sig, s.OldSig = Signature, e.New.Signature, e.Old.Signature
		evs = append(evs, s)
	}
	return evs
}

// symbolFrame is the frame that takes the live symbols from start to end.
func symbolFrame(start, end map[int]int, evs []SymbolEvent) SymbolFrame {
	sf := SymbolFrame{Events: evs}
	for k, n := range end {
		if m, ok := start[k]; !ok || m != n {
			sf.Set = append(sf.Set, SymbolSize{k, n})
		}
	}
	for k := range start {
		if _, ok := end[k]; !ok {
			sf.Gone = append(sf.Gone, k)
		}
	}
	slices.SortFunc(sf.Set, func(a, b SymbolSize) int { return cmp.Compare(a.Symbol, b.Symbol) })
	slices.Sort(sf.Gone)
	return sf
}

// placeSymbols spreads the symbols over the disc on a sunflower spiral, then
// hands out its points by angle, so that symbols next to each other in the
// slice share a wedge.
func placeSymbols(syms []SymbolPlace) {
	n := len(syms)
	type pt struct{ x, y, a float64 }
	pts := make([]pt, n)
	const golden = 2.399963229728653 // radians
	for i := range n {
		r := 0.44 * math.Sqrt((float64(i)+0.5)/float64(n))
		a := float64(i) * golden
		x, y := r*math.Cos(a), r*math.Sin(a)
		pts[i] = pt{0.5 + x, 0.5 + y, math.Atan2(y, x)}
	}
	slices.SortStableFunc(pts, func(p, q pt) int { return cmp.Compare(p.a, q.a) })
	for i := range syms {
		syms[i].X, syms[i].Y = pts[i].x, pts[i].y
	}
}
