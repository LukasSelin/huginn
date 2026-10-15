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
	counts := func(s *ast.Symbol) bool { return s != nil && (opts.Tests || !strings.HasSuffix(s.Path, "_test.go")) }
	in := func(s *ast.Symbol) bool { return counts(s) && path.Dir(s.Path) == dir }

	type cand struct {
		sym   *lineage.Symbol
		first *ast.Symbol // first declaration in dir
		rev   int         // of that declaration
		peak  int
	}
	var cands []cand
	for sym := range h.All() {
		c := cand{sym: sym, rev: -1}
		for _, e := range sym.Events {
			if !in(e.New) {
				continue
			}
			if c.first == nil {
				c.first = e.New
				c.rev, _ = h.Position(e.SHA)
			}
			c.peak = max(c.peak, e.New.Stmts)
		}
		if c.first != nil {
			cands = append(cands, c)
		}
	}
	v := &SymbolView{Dir: dir, Total: len(cands), Symbols: []SymbolPlace{}}
	if opts.Limit > 0 && len(cands) > opts.Limit {
		slices.SortStableFunc(cands, func(a, b cand) int {
			ka, kb := a.sym.ID == opts.Keep, b.sym.ID == opts.Keep
			if ka != kb {
				if ka {
					return -1
				}
				return 1
			}
			return cmp.Or(cmp.Compare(b.peak, a.peak), cmp.Compare(a.sym.ID, b.sym.ID))
		})
		cands = cands[:opts.Limit]
		v.Truncated = true
	}
	// Place in file order, so a file's symbols sit together.
	slices.SortFunc(cands, func(a, b cand) int {
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

	// Events of the shown symbols, by revision.
	type change struct {
		sym int
		e   *lineage.Event
	}
	byRev := map[int][]change{}
	for _, c := range cands {
		for i := range c.sym.Events {
			e := &c.sym.Events[i]
			if !in(e.Old) && !in(e.New) {
				continue
			}
			if rev, ok := h.Position(e.SHA); ok {
				byRev[rev] = append(byRev[rev], change{idx[c.sym.ID], e})
			}
		}
	}

	live := map[int]int{} // symbol → statements
	v.Frames = make([]SymbolFrame, len(j.Frames))
	for fi, f := range j.Frames {
		start := make(map[int]int, len(live))
		for k, n := range live {
			start[k] = n
		}
		var evs []SymbolEvent
		for rev := f.Total - f.Commits; rev < f.Total; rev++ {
			cs := byRev[rev]
			slices.SortFunc(cs, func(a, b change) int { return cmp.Compare(a.sym, b.sym) })
			for _, c := range cs {
				_, was := live[c.sym]
				now := in(c.e.New)
				ev := SymbolEvent{Symbol: c.sym, Rev: rev}
				switch {
				case !was && now:
					ev.Kind, ev.Name = Born, c.e.New.QualifiedName()
					if counts(c.e.Old) && !in(c.e.Old) {
						ev.Kind, ev.From = MovedIn, path.Dir(c.e.Old.Path)
					}
					evs = append(evs, ev)
				case was && !now:
					ev.Kind, ev.Name = Died, c.e.Old.QualifiedName()
					if counts(c.e.New) {
						ev.Kind, ev.Name, ev.To = MovedOut, c.e.New.QualifiedName(), path.Dir(c.e.New.Path)
					}
					evs = append(evs, ev)
				case was && now:
					ev.Name = c.e.New.QualifiedName()
					if c.e.Flags.Has(ast.Renamed) && c.e.Old != nil {
						r := ev
						r.Kind, r.From = Renamed, c.e.Old.QualifiedName()
						evs = append(evs, r)
					}
					if c.e.Flags.Has(ast.SignatureChanged) && c.e.Old != nil {
						s := ev
						s.Kind, s.Sig, s.OldSig = Signature, c.e.New.Signature, c.e.Old.Signature
						evs = append(evs, s)
					}
				}
				if now {
					live[c.sym] = c.e.New.Stmts
				} else {
					delete(live, c.sym)
				}
			}
		}
		sf := SymbolFrame{Events: evs}
		for k, n := range live {
			if m, ok := start[k]; !ok || m != n {
				sf.Set = append(sf.Set, SymbolSize{k, n})
			}
		}
		for k := range start {
			if _, ok := live[k]; !ok {
				sf.Gone = append(sf.Gone, k)
			}
		}
		slices.SortFunc(sf.Set, func(a, b SymbolSize) int { return cmp.Compare(a.Symbol, b.Symbol) })
		slices.Sort(sf.Gone)
		v.Frames[fi] = sf
	}
	return v
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
