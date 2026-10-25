package web

import (
	"net/http"
	"path"

	"huginn/internal/lineage"
)

type healthJSON struct {
	Tests     bool          `json:"tests"` // whether test lines were counted
	Buckets   []bucketJSON  `json:"buckets"`
	Hotspots  []hotspotJSON `json:"hotspots"`
	Owners    []ownerJSON   `json:"owners"`
	Authors   int           `json:"authors"`
	BusFactor int           `json:"busFactor"`
}

type bucketJSON struct {
	First      string         `json:"first"`
	Last       string         `json:"last"`
	From       int64          `json:"from"` // unix seconds
	To         int64          `json:"to"`
	Revisions  int            `json:"revisions"`
	Symbols    int            `json:"symbols"`
	Exported   int            `json:"exported"`
	Lines      int            `json:"lines"`
	Complexity int            `json:"complexity"`
	Funcs      int            `json:"funcs"`
	TestLines  int            `json:"testLines"`
	Edits      map[string]int `json:"edits"`
	Reshaped   int            `json:"reshaped"` // renames, moves and signature changes
	Added      int            `json:"added"`
	Surviving  int            `json:"surviving"`
	Fixed      int            `json:"fixed"` // added symbols a later fix edited
	Commits    int            `json:"commits"`
	Features   int            `json:"features"`
	Fixes      int            `json:"fixes"` // including reverts
	Reverts    int            `json:"reverts"`
	FixEdits   int            `json:"fixEdits"`
}

type hotspotJSON struct {
	Path          string     `json:"path"`
	Dir           string     `json:"dir"`
	Edits         int        `json:"edits"`
	Fixes         int        `json:"fixes"`
	Authors       int        `json:"authors"`
	Complexity    int        `json:"complexity"`
	Lines         int        `json:"lines"`
	Score         int        `json:"score"`
	Top           lineage.ID `json:"top"`
	TopName       string     `json:"topName"`
	TopComplexity int        `json:"topComplexity"`
}

type ownerJSON struct {
	Name    string `json:"name"`
	Symbols int    `json:"symbols"`
}

// health serves trends over the whole history, the hotspot files and who
// owns the live code.
func (s *Server) health(ix *index, w http.ResponseWriter, r *http.Request) {
	rep := ix.health
	out := healthJSON{
		Tests: s.opts.Tests, Buckets: []bucketJSON{}, Hotspots: []hotspotJSON{}, Owners: []ownerJSON{},
		Authors: rep.Authors, BusFactor: rep.BusFactor,
	}
	for _, b := range rep.Buckets {
		out.Buckets = append(out.Buckets, bucketJSON{
			First: b.First, Last: b.Last, From: b.From.Unix(), To: b.To.Unix(), Revisions: b.Revisions,
			Symbols: b.Symbols, Exported: b.Exported, Lines: b.Lines, Complexity: b.Complexity, Funcs: b.Funcs,
			TestLines: b.TestLines, Edits: kindsJSON(b.Edits), Reshaped: b.Restructured(), Added: b.Added, Surviving: b.Surviving,
			Fixed: b.Fixed, Commits: b.Commits, Features: b.Features, Fixes: b.Fixes, Reverts: b.Reverts, FixEdits: b.FixEdits,
		})
	}
	for _, h := range rep.Hotspots {
		out.Hotspots = append(out.Hotspots, hotspotJSON{
			Path: h.Path, Dir: path.Dir(h.Path), Edits: h.Edits, Fixes: h.Fixes, Authors: h.Authors, Complexity: h.Complexity, Lines: h.Lines,
			Score: h.Score, Top: h.Top, TopName: h.TopName, TopComplexity: h.TopComplexity,
		})
	}
	for _, o := range rep.Owners {
		out.Owners = append(out.Owners, ownerJSON{Name: o.Name, Symbols: o.Symbols})
	}
	writeJSON(w, out)
}
