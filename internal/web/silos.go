package web

import (
	"net/http"

	"huginn/internal/lineage"
	"huginn/internal/silos"
)

type silosJSON struct {
	Now          int64        `json:"now"` // unix seconds of the last commit
	Authors      []string     `json:"authors"`
	Departed     []string     `json:"departed"`
	Knowers      []knowerJSON `json:"knowers"` // by share of all that is known of the live code
	Symbols      []riskJSON   `json:"symbols"`
	Packages     []riskJSON   `json:"packages"`
	SymbolSilos  int          `json:"symbolSilos"`
	PackageSilos int          `json:"packageSilos"`
	SymbolPairs  []pairJSON   `json:"symbolPairs"`
	PackagePairs []pairJSON   `json:"packagePairs"`
}

type unitJSON struct {
	ID   lineage.ID `json:"id,omitempty"` // 0 for a package
	Name string     `json:"name"`
	Path string     `json:"path"`
	Dir  string     `json:"dir"`
}

type knowerJSON struct {
	Name     string  `json:"name"`
	Share    float64 `json:"share"`
	Departed bool    `json:"departed,omitempty"`
}

type spreadJSON struct {
	Knowers   []knowerJSON `json:"knowers"`
	Known     float64      `json:"known"` // share held by authors still around
	Holder    string       `json:"holder"`
	Held      float64      `json:"held"`
	BusFactor int          `json:"busFactor"`
}

type dependenceJSON struct {
	Callers     int     `json:"callers"`
	Packages    int     `json:"packages"` // other packages the callers are in
	Approximate int     `json:"approximate"`
	CallSites   int     `json:"callSites"`
	FanIn       float64 `json:"fanIn"`
	Complexity  int     `json:"complexity"`
	Edits       int     `json:"edits"`
}

type riskJSON struct {
	unitJSON
	spreadJSON
	dependenceJSON
	Concentration float64  `json:"concentration"`
	Score         float64  `json:"score"`
	Reasons       []string `json:"reasons"`
}

type pairJSON struct {
	A           unitJSON     `json:"a"`
	B           unitJSON     `json:"b"`
	AKnowers    []knowerJSON `json:"aKnowers"`
	BKnowers    []knowerJSON `json:"bKnowers"`
	Refs        int          `json:"refs"`
	Approximate bool         `json:"approximate"`
	CoChanges   int          `json:"coChanges"`
	Logical     float64      `json:"logical"`
	Overlap     float64      `json:"overlap"`
	Score       float64      `json:"score"`
	Reasons     []string     `json:"reasons"`
}

type leaveJSON struct {
	Leaving          []string   `json:"leaving"`
	Unknown          []string   `json:"unknown"`
	Share            float64    `json:"share"`
	Symbols          []lossJSON `json:"symbols"`
	Packages         []lossJSON `json:"packages"`
	OrphanedSymbols  int        `json:"orphanedSymbols"`
	SingleSymbols    int        `json:"singleSymbols"`
	OrphanedPackages int        `json:"orphanedPackages"`
	SinglePackages   int        `json:"singlePackages"`
}

type lossJSON struct {
	unitJSON
	dependenceJSON
	Before   spreadJSON `json:"before"`
	After    spreadJSON `json:"after"`
	Lost     float64    `json:"lost"`
	Orphaned bool       `json:"orphaned"`
	Score    float64    `json:"score"`
	Reasons  []string   `json:"reasons"`
}

// silosAPI serves the knowledge silos of the live code and the pairs coupled
// across knowledge boundaries.
func (s *Server) silosAPI(ix *index, w http.ResponseWriter, r *http.Request) {
	rep := ix.silos
	out := silosJSON{
		Now: rep.Now.Unix(), Authors: rep.Authors, Departed: rep.Departed, Knowers: knowersJSON(rep.Knowers),
		Symbols: risksJSON(rep.Symbols), Packages: risksJSON(rep.Packages),
		SymbolSilos: rep.SymbolSilos, PackageSilos: rep.PackageSilos,
		SymbolPairs: pairsJSON(rep.SymbolPairs), PackagePairs: pairsJSON(rep.PackagePairs),
	}
	writeJSON(w, out)
}

// silosLeave serves what would be left if the authors named by the author
// parameters left.
func (s *Server) silosLeave(ix *index, w http.ResponseWriter, r *http.Request) {
	names := r.URL.Query()["author"]
	if len(names) == 0 {
		http.Error(w, "want at least one author", http.StatusBadRequest)
		return
	}
	d := ix.silos.Leave(names...)
	writeJSON(w, leaveJSON{
		Leaving: d.Leaving, Unknown: d.Unknown, Share: d.Share,
		Symbols: lossesJSON(d.Symbols), Packages: lossesJSON(d.Packages),
		OrphanedSymbols: d.OrphanedSymbols, SingleSymbols: d.SingleSymbols,
		OrphanedPackages: d.OrphanedPackages, SinglePackages: d.SinglePackages,
	})
}

func toUnitJSON(u silos.Unit) unitJSON {
	return unitJSON{ID: u.ID, Name: u.Name, Path: u.Path, Dir: u.Dir}
}

func knowersJSON(ks []silos.Knower) []knowerJSON {
	out := []knowerJSON{}
	for _, k := range ks {
		out = append(out, knowerJSON{Name: k.Name, Share: k.Share, Departed: k.Departed})
	}
	return out
}

func toSpreadJSON(s silos.Spread) spreadJSON {
	return spreadJSON{Knowers: knowersJSON(s.Knowers), Known: s.Known, Holder: s.Holder, Held: s.Held, BusFactor: s.BusFactor}
}

func toDependenceJSON(d silos.Dependence) dependenceJSON {
	return dependenceJSON{Callers: d.Callers, Packages: d.Packages, Approximate: d.Approximate, CallSites: d.CallSites,
		FanIn: d.FanIn, Complexity: d.Complexity, Edits: d.Edits}
}

func risksJSON(rs []silos.Risk) []riskJSON {
	out := []riskJSON{}
	for _, r := range rs {
		out = append(out, riskJSON{unitJSON: toUnitJSON(r.Unit), spreadJSON: toSpreadJSON(r.Spread), dependenceJSON: toDependenceJSON(r.Dependence),
			Concentration: r.Concentration(), Score: r.Score, Reasons: r.Reasons})
	}
	return out
}

func pairsJSON(ps []silos.Pair) []pairJSON {
	out := []pairJSON{}
	for _, p := range ps {
		out = append(out, pairJSON{A: toUnitJSON(p.A), B: toUnitJSON(p.B), AKnowers: knowersJSON(p.AKnowers), BKnowers: knowersJSON(p.BKnowers),
			Refs: p.Refs, Approximate: p.Approximate, CoChanges: p.CoChanges, Logical: p.Logical, Overlap: p.Overlap, Score: p.Score, Reasons: p.Reasons})
	}
	return out
}

func lossesJSON(ls []silos.Loss) []lossJSON {
	out := []lossJSON{}
	for _, l := range ls {
		out = append(out, lossJSON{unitJSON: toUnitJSON(l.Unit), dependenceJSON: toDependenceJSON(l.Dependence),
			Before: toSpreadJSON(l.Before), After: toSpreadJSON(l.After), Lost: l.Lost, Orphaned: l.Orphaned, Score: l.Score, Reasons: l.Reasons})
	}
	return out
}
