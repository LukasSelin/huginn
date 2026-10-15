package web

import (
	"cmp"
	"context"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"

	"huginn/internal/journey"
	"huginn/internal/lineage"
	"huginn/internal/story"
)

// journeys builds each step's journey and the story once, on first use.
type journeys struct {
	once  [journey.Month + 1]sync.Once
	steps [journey.Month + 1]*journey.Journey

	storyOnce sync.Once
	story     *story.Story
	storyErr  error
}

func (s *Server) journey(ix *index, step journey.Step) *journey.Journey {
	ix.journeys.once[step].Do(func() {
		ix.journeys.steps[step] = journey.Build(ix.h, ix.cmap, step, journey.Options{Tests: s.opts.Tests})
	})
	return ix.journeys.steps[step]
}

func (s *Server) story(ix *index) (*story.Story, error) {
	ix.journeys.storyOnce.Do(func() {
		// Templated narration needs no context of its own; a narrator that
		// calls out would get one from the request instead.
		ix.journeys.story, ix.journeys.storyErr = story.Build(context.Background(), ix.h, s.journey(ix, journey.Commit), story.Templates{})
	})
	return ix.journeys.story, ix.journeys.storyErr
}

type journeyJSON struct {
	Step     string        `json:"step"`
	Packages []packageJSON `json:"packages"`
	Frames   []frameJSON   `json:"frames"`
	Chapters []chapterJSON `json:"chapters"`
	Moments  []momentJSON  `json:"moments"`  // every chapter's key moments, by time
	RevTimes []int64       `json:"revTimes"` // unix seconds of every commit, oldest first
}

type packageJSON struct {
	Path string  `json:"path"`
	Peak int     `json:"peak"`
	X    float64 `json:"x"` // center of the bubble at its peak, in the unit square
	Y    float64 `json:"y"`
	R    float64 `json:"r"`
}

type frameJSON struct {
	T       int64     `json:"t"`             // unix seconds of the frame's start, or of its commit
	End     int64     `json:"end,omitempty"` // of the frame's end, for weeks and months
	SHA     string    `json:"sha,omitempty"` // for commit frames
	Commits int       `json:"commits"`
	Total   int       `json:"total"` // commits up to the end of the frame
	Live    int       `json:"live"`
	Pkgs    []pkgJSON `json:"pkgs"` // packages with live symbols or edits
	Deps    [][3]int  `json:"deps"` // [from, to, refs], package indexes
}

type pkgJSON struct {
	P     int            `json:"p"` // index into packages
	Live  int            `json:"live"`
	Kinds map[string]int `json:"kinds,omitempty"` // semantic edits in the frame
}

type chapterJSON struct {
	Title     string       `json:"title"`
	Text      string       `json:"text"`
	From      int64        `json:"from"` // unix seconds of the first and last commit
	To        int64        `json:"to"`
	FirstRev  int          `json:"firstRev"` // revision indexes, inclusive
	LastRev   int          `json:"lastRev"`
	Commits   int          `json:"commits"`
	Added     int          `json:"added"`
	Removed   int          `json:"removed"`
	Hottest   string       `json:"hottest,omitempty"`
	HotEdits  int          `json:"hottestEdits"`
	Births    []string     `json:"births"`
	Deaths    []string     `json:"deaths"`
	Moments   []momentJSON `json:"moments"`
	Live      int          `json:"live"`
	LivePkgs  int          `json:"livePackages"`
	EditCount int          `json:"edits"`
}

type momentJSON struct {
	Kind    string  `json:"kind"`
	T       int64   `json:"t"`
	Rev     int     `json:"rev"` // revision index, oldest first
	SHA     string  `json:"sha"`
	Package string  `json:"pkg,omitempty"`
	Text    string  `json:"text"`
	Weight  float64 `json:"weight"`
}

func momentsJSON(ms []story.Moment) []momentJSON {
	out := make([]momentJSON, 0, len(ms))
	for _, m := range ms {
		out = append(out, momentJSON{Kind: m.Kind.String(), T: m.Time.Unix(), Rev: m.Rev, SHA: m.SHA, Package: m.Package, Text: m.Text, Weight: m.Weight})
	}
	return out
}

// journeyAPI serves the history as frames of ?step=commit|week|month (week
// by default), with the package layout and the story.
func (s *Server) journeyAPI(ix *index, w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("step")
	if q == "" {
		q = "week"
	}
	step, ok := journey.ParseStep(q)
	if !ok {
		http.Error(w, "step must be commit, week or month", http.StatusBadRequest)
		return
	}
	st, err := s.story(ix)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	j := s.journey(ix, step)
	out := journeyJSON{Step: step.String(), Packages: []packageJSON{}, Frames: []frameJSON{}, Chapters: []chapterJSON{}, Moments: momentsJSON(st.Moments)}
	out.RevTimes = make([]int64, ix.h.Revisions())
	for i := range out.RevTimes {
		_, t := ix.h.RevisionAt(i)
		out.RevTimes[i] = t.Unix()
	}
	for _, p := range j.Packages {
		out.Packages = append(out.Packages, packageJSON{Path: p.Path, Peak: p.Peak, X: p.X, Y: p.Y, R: p.R})
	}
	for _, f := range j.Frames {
		fj := frameJSON{T: f.Start.Unix(), SHA: f.SHA, Commits: f.Commits, Total: f.Total, Live: f.Live, Pkgs: []pkgJSON{}, Deps: [][3]int{}}
		if step != journey.Commit {
			fj.End = f.End.Unix()
		}
		for _, p := range f.Packages {
			pj := pkgJSON{P: p.Package, Live: p.Live}
			if p.Edits.Total() > 0 {
				pj.Kinds = kindsJSON(p.Edits)
			}
			fj.Pkgs = append(fj.Pkgs, pj)
		}
		for _, d := range f.Deps {
			fj.Deps = append(fj.Deps, [3]int{d.From, d.To, d.Refs})
		}
		out.Frames = append(out.Frames, fj)
	}
	for _, c := range st.Chapters {
		out.Chapters = append(out.Chapters, chapterJSON{
			Title: c.Title, Text: c.Text, From: c.From.Unix(), To: c.To.Unix(), FirstRev: c.FirstRev, LastRev: c.LastRev,
			Commits: c.Commits, Added: c.Added, Removed: c.Removed, Hottest: c.Hottest, HotEdits: c.HottestEdits,
			Births: nonNil(c.Births), Deaths: nonNil(c.Deaths), Moments: momentsJSON(c.Moments),
			Live: c.Live, LivePkgs: c.LivePackages, EditCount: c.Edits,
		})
	}
	writeJSON(w, out)
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// packageSymbolLimit bounds the symbols a package view sends; big packages
// show their biggest symbols.
const packageSymbolLimit = 600

type symViewJSON struct {
	Dir       string         `json:"dir"`
	Step      string         `json:"step"`
	Total     int            `json:"total"` // symbols ever in the package
	Truncated bool           `json:"truncated"`
	Symbols   []symPlaceJSON `json:"symbols"`
	Frames    []symFrameJSON `json:"frames"` // parallel to /api/journey's frames for the step
}

type symPlaceJSON struct {
	ID   lineage.ID `json:"id"`
	Name string     `json:"name"` // as first seen in the package
	Kind string     `json:"kind"`
	File string     `json:"file"`
	Peak int        `json:"peak"` // most statements
	X    float64    `json:"x"`    // in the unit square, inside the inscribed circle
	Y    float64    `json:"y"`
}

// symFrameJSON is a frame as changes to the previous one: replaying from
// the first frame gives the live symbols of each.
type symFrameJSON struct {
	Set  [][2]int       `json:"set"`  // [symbol, stmts] that became live or changed size
	Gone []int          `json:"gone"` // symbols that left
	Ev   []symEventJSON `json:"ev"`
}

type symEventJSON struct {
	S      int    `json:"s"` // index into symbols
	Rev    int    `json:"rev"`
	Kind   string `json:"k"` // born, died, renamed, in, out, signature
	Name   string `json:"name"`
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
	Sig    string `json:"sig,omitempty"`
	OldSig string `json:"oldSig,omitempty"`
}

// journeyPackage serves the symbols of one package directory frame by
// frame, for ?step= as /api/journey. An empty directory is the root. ?keep=
// names a symbol ID to include even in a truncated view.
func (s *Server) journeyPackage(ix *index, w http.ResponseWriter, r *http.Request) {
	dir := r.PathValue("dir")
	if dir == "" {
		dir = "."
	}
	q := r.URL.Query()
	step, ok := journey.ParseStep(cmp.Or(q.Get("step"), "week"))
	if !ok {
		http.Error(w, "step must be commit, week or month", http.StatusBadRequest)
		return
	}
	keep, _ := strconv.ParseInt(q.Get("keep"), 10, 64)
	v := s.journey(ix, step).Symbols(ix.h, dir, journey.SymbolOptions{Tests: s.opts.Tests, Limit: packageSymbolLimit, Keep: lineage.ID(keep)})
	if v.Total == 0 {
		http.Error(w, "no symbols in that directory", http.StatusNotFound)
		return
	}
	out := symViewJSON{Dir: dir, Step: step.String(), Total: v.Total, Truncated: v.Truncated, Symbols: []symPlaceJSON{}, Frames: make([]symFrameJSON, 0, len(v.Frames))}
	for _, p := range v.Symbols {
		out.Symbols = append(out.Symbols, symPlaceJSON{ID: p.ID, Name: p.Name, Kind: p.Kind.String(), File: p.File, Peak: p.PeakStmts, X: p.X, Y: p.Y})
	}
	for _, f := range v.Frames {
		fj := symFrameJSON{Set: [][2]int{}, Gone: nonNil(f.Gone), Ev: []symEventJSON{}}
		for _, z := range f.Set {
			fj.Set = append(fj.Set, [2]int{z.Symbol, z.Stmts})
		}
		for _, e := range f.Events {
			fj.Ev = append(fj.Ev, symEventJSON{S: e.Symbol, Rev: e.Rev, Kind: e.Kind.String(), Name: e.Name, From: e.From, To: e.To, Sig: e.Sig, OldSig: e.OldSig})
		}
		out.Frames = append(out.Frames, fj)
	}
	writeJSON(w, out)
}

// symbolSearchLimit bounds the symbols a search returns.
const symbolSearchLimit = 20

type symHitJSON struct {
	ID    lineage.ID `json:"id"`
	Name  string     `json:"name"` // latest qualified name
	Kind  string     `json:"kind"`
	Dir   string     `json:"dir"` // of the latest declaration
	Alive bool       `json:"alive"`
}

// journeySymbols finds symbols to follow by ?q=, a case-insensitive part of
// their latest qualified name: exact matches first, then prefixes, then the
// rest; live before deleted; then by name and ID.
func (s *Server) journeySymbols(ix *index, w http.ResponseWriter, r *http.Request) {
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	out := []symHitJSON{}
	if q == "" {
		writeJSON(w, out)
		return
	}
	type hit struct {
		symHitJSON
		rank int
	}
	var hits []hit
	for sym := range ix.h.All() {
		cur := sym.Latest()
		name := cur.QualifiedName()
		low, bare := strings.ToLower(name), strings.ToLower(cur.Name)
		rank := 2
		switch {
		case low == q || bare == q:
			rank = 0
		case strings.HasPrefix(low, q) || strings.HasPrefix(bare, q):
			rank = 1
		case !strings.Contains(low, q):
			continue
		}
		hits = append(hits, hit{symHitJSON{ID: sym.ID, Name: name, Kind: cur.Kind.String(), Dir: path.Dir(cur.Path), Alive: sym.Alive()}, rank})
	}
	slices.SortFunc(hits, func(a, b hit) int {
		if a.Alive != b.Alive {
			if a.Alive {
				return -1
			}
			return 1
		}
		return cmp.Or(cmp.Compare(a.rank, b.rank), cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID))
	})
	for _, h := range hits[:min(len(hits), symbolSearchLimit)] {
		out = append(out, h.symHitJSON)
	}
	writeJSON(w, out)
}
