package web

import (
	"cmp"
	"net/http"

	"huginn/internal/journey"
)

// layersJSON is the history frame by frame as the layers page stacks it:
// each live package on a layer above everything it refers to.
type layersJSON struct {
	Step     string           `json:"step"`
	Packages []packageJSON    `json:"packages"` // as /api/journey's, with the same layout
	Frames   []layerFrameJSON `json:"frames"`
}

type layerFrameJSON struct {
	T       int64    `json:"t"`             // unix seconds of the frame's start, or of its commit
	End     int64    `json:"end,omitempty"` // of the frame's end, for weeks and months
	SHA     string   `json:"sha,omitempty"` // for commit frames
	Commits int      `json:"commits"`
	Total   int      `json:"total"`
	Live    int      `json:"live"`
	Pkgs    [][3]int `json:"pkgs"` // [package, live symbols, layer] for every package with live symbols
	Deps    [][4]int `json:"deps"` // [from, to, refs, 1 if it runs within a cycle else 0]
}

// layersAPI serves the packages of every frame of ?step=commit|week|month
// (week by default) in layers, by journey.Layers.
func (ix *index) layersAPI(w http.ResponseWriter, r *http.Request) {
	step, ok := journey.ParseStep(cmp.Or(r.URL.Query().Get("step"), "week"))
	if !ok {
		http.Error(w, "step must be commit, week or month", http.StatusBadRequest)
		return
	}
	j := ix.journey(step)
	out := layersJSON{Step: step.String(), Packages: packagesJSON(j.Packages), Frames: make([]layerFrameJSON, 0, len(j.Frames))}
	for _, f := range j.Frames {
		layer, cyclic := journey.Layers(len(j.Packages), f.Deps)
		fj := layerFrameJSON{T: f.Start.Unix(), SHA: f.SHA, Commits: f.Commits, Total: f.Total, Live: f.Live, Pkgs: [][3]int{}, Deps: [][4]int{}}
		if step != journey.Commit {
			fj.End = f.End.Unix()
		}
		for _, p := range f.Packages {
			if p.Live > 0 {
				fj.Pkgs = append(fj.Pkgs, [3]int{p.Package, p.Live, layer[p.Package]})
			}
		}
		for i, d := range f.Deps {
			c := 0
			if cyclic[i] {
				c = 1
			}
			fj.Deps = append(fj.Deps, [4]int{d.From, d.To, d.Refs, c})
		}
		out.Frames = append(out.Frames, fj)
	}
	writeJSON(w, out)
}
