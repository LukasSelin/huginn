package web

import (
	"cmp"
	"context"
	"math"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"

	"huginn/internal/git"
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
		ctx := context.Background()
		tags, err := s.repo.Tags(ctx, git.Options{Branch: s.opts.Branch})
		if err != nil {
			ix.journeys.storyErr = err
			return
		}
		ix.journeys.story, ix.journeys.storyErr = story.Build(ctx, ix.h, s.journey(ix, journey.Commit), story.FromTags(tags), story.Templates{})
	})
	return ix.journeys.story, ix.journeys.storyErr
}

type journeyJSON struct {
	Step     string        `json:"step"`
	Packages []packageJSON `json:"packages"`
	Authors  []string      `json:"authors"` // sorted; knowers index into it
	Frames   []frameJSON   `json:"frames"`
	Chapters []chapterJSON `json:"chapters"`
	Moments  []momentJSON  `json:"moments"`  // every chapter's key moments, by time
	Releases []momentJSON  `json:"releases"` // every release, by time
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

	// Who knows the package at the end of the frame: [author, share,
	// departed (1) or not (0)], most first; the share held by departed
	// authors; and the fewest authors who hold more than half.
	Knowers  [][3]float64 `json:"knowers,omitempty"`
	Orphaned float64      `json:"orphaned,omitempty"`
	Bus      int          `json:"bus,omitempty"`
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
	Releases  []string     `json:"releases"`
	Themes    themesJSON   `json:"themes"`
	Authors   int          `json:"authors"`
	People    []personJSON `json:"contributors"` // most semantic edits first
	Newcomers []string     `json:"newcomers"`
}

type themesJSON struct {
	Types        []themeJSON `json:"types"`
	Typed        int         `json:"typed"` // commits with a conventional type
	Scopes       []themeJSON `json:"scopes"`
	Terms        []themeJSON `json:"terms"`
	PullRequests int         `json:"pullRequests"`
}

type themeJSON struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type personJSON struct {
	Name    string `json:"name"`
	Commits int    `json:"commits"`
	Edits   int    `json:"edits"`
}

func themesOf(t story.Themes) themesJSON {
	list := func(ts []story.Theme) []themeJSON {
		out := make([]themeJSON, len(ts))
		for i, x := range ts {
			out[i] = themeJSON{x.Name, x.Count}
		}
		return out
	}
	return themesJSON{Types: list(t.Types), Typed: t.Typed, Scopes: list(t.Scopes), Terms: list(t.Terms), PullRequests: t.PullRequests}
}

type momentJSON struct {
	Kind    string  `json:"kind"`
	T       int64   `json:"t"`
	Rev     int     `json:"rev"` // revision index, oldest first
	SHA     string  `json:"sha"`
	Package string  `json:"pkg,omitempty"`
	Tag     string  `json:"tag,omitempty"` // for a release
	Text    string  `json:"text"`
	Weight  float64 `json:"weight"`
}

func momentsJSON(ms []story.Moment) []momentJSON {
	out := make([]momentJSON, 0, len(ms))
	for _, m := range ms {
		out = append(out, momentJSON{Kind: m.Kind.String(), T: m.Time.Unix(), Rev: m.Rev, SHA: m.SHA, Package: m.Package, Tag: m.Tag, Text: m.Text, Weight: m.Weight})
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
	out := journeyJSON{Step: step.String(), Packages: []packageJSON{}, Authors: nonNil(j.Authors), Frames: []frameJSON{}, Chapters: []chapterJSON{}, Moments: momentsJSON(st.Moments), Releases: momentsJSON(st.Releases)}
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
			for _, k := range p.Knowers {
				gone := 0.0
				if k.Departed {
					gone = 1
				}
				pj.Knowers = append(pj.Knowers, [3]float64{float64(k.Author), round3(k.Share), gone})
			}
			pj.Orphaned, pj.Bus = round3(p.Orphaned), p.BusFactor
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
			Releases: nonNil(c.Releases), Themes: themesOf(c.Themes), Authors: c.Authors, People: []personJSON{}, Newcomers: nonNil(c.Newcomers),
		})
		cj := &out.Chapters[len(out.Chapters)-1]
		for _, p := range c.Contributors {
			cj.People = append(cj.People, personJSON{p.Name, p.Commits, p.Edits})
		}
	}
	writeJSON(w, out)
}

// round3 rounds a share to three decimals, which is all a ring can show,
// to keep the frames small.
func round3(x float64) float64 { return math.Round(x*1000) / 1000 }

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

type linksJSON struct {
	Dir     string        `json:"dir"`
	Rev     int           `json:"rev"`
	Symbols []linkSymJSON `json:"symbols"` // live in dir, by name
	Out     []linkJSON    `json:"out"`     // dir's symbols referring elsewhere
	In      []linkJSON    `json:"in"`      // other symbols referring to dir
}

type linkSymJSON struct {
	ID   lineage.ID `json:"id"`
	Name string     `json:"name"`
	Kind string     `json:"kind"`
}

type linkJSON struct {
	ID    lineage.ID `json:"id"`
	Name  string     `json:"name"` // of the referring symbol
	Kind  string     `json:"kind"`
	From  string     `json:"from"`
	To    string     `json:"to"`
	Names []string   `json:"names"` // what it refers to in to
	Refs  int        `json:"refs"`
}

// journeyLinks serves the symbols referring into and out of one package
// directory after revision ?rev=, the latest by default. An empty directory
// is the root.
func (s *Server) journeyLinks(ix *index, w http.ResponseWriter, r *http.Request) {
	dir := r.PathValue("dir")
	if dir == "" {
		dir = "."
	}
	rev := ix.h.Revisions() - 1
	if q := r.URL.Query().Get("rev"); q != "" {
		n, err := strconv.Atoi(q)
		if err != nil || n < -1 || n > rev {
			http.Error(w, "rev must be a revision index", http.StatusBadRequest)
			return
		}
		rev = n
	}
	l := journey.LinksAt(ix.h, dir, rev, s.opts.Tests)
	out := linksJSON{Dir: dir, Rev: rev, Symbols: []linkSymJSON{}, Out: []linkJSON{}, In: []linkJSON{}}
	for _, x := range l.Symbols {
		out.Symbols = append(out.Symbols, linkSymJSON{ID: x.ID, Name: x.Name, Kind: x.Kind.String()})
	}
	conv := func(k journey.Link) linkJSON {
		return linkJSON{ID: k.ID, Name: k.Name, Kind: k.Kind.String(), From: k.From, To: k.To, Names: k.Names, Refs: k.Refs}
	}
	for _, k := range l.Out {
		out.Out = append(out.Out, conv(k))
	}
	for _, k := range l.In {
		out.In = append(out.In, conv(k))
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
