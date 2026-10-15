package web

import (
	"context"
	"net/http"
	"sync"

	"huginn/internal/git"
	"huginn/internal/journey"
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
	out := journeyJSON{Step: step.String(), Packages: []packageJSON{}, Frames: []frameJSON{}, Chapters: []chapterJSON{}, Moments: momentsJSON(st.Moments), Releases: momentsJSON(st.Releases)}
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
			Releases: nonNil(c.Releases), Themes: themesOf(c.Themes), Authors: c.Authors, People: []personJSON{}, Newcomers: nonNil(c.Newcomers),
		})
		cj := &out.Chapters[len(out.Chapters)-1]
		for _, p := range c.Contributors {
			cj.People = append(cj.People, personJSON{p.Name, p.Commits, p.Edits})
		}
	}
	writeJSON(w, out)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
