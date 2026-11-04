package journey

import (
	"path"
	"slices"
	"time"

	"huginn/internal/ast"
	"huginn/internal/knowledge"
	"huginn/internal/lineage"
)

// Who knows each package is rolled up from the knowledge model (see
// internal/knowledge) per directory, at the end of every frame.

// maxKnowers is how many knowers a package state lists.
const maxKnowers = 5

// Knower is an author's share of what is known of a package.
type Knower struct {
	Author   int     // index into Journey.Authors
	Share    float64 // of the package's knowledge, 0 to 1
	Departed bool    // no commits for six months at the end of the frame
}

// dirKnowledge sums the knowledge model per directory during a replay.
type dirKnowledge struct {
	*knowledge.Model
	inDir map[string]map[lineage.ID]bool
	dirty map[string]bool
	sums  map[string][]knowledge.Weight // per directory, most first, as of the last snapshot
}

func newDirKnowledge(h *lineage.History) *dirKnowledge {
	return &dirKnowledge{Model: knowledge.New(h), inDir: map[string]map[lineage.ID]bool{},
		dirty: map[string]bool{}, sums: map[string][]knowledge.Weight{}}
}

// apply updates the knowledge of symbol id for event e, and which directory
// holds it. old is the symbol's counted declaration before e, nil if it was
// not live; e.New counts only when cur is true.
func (k *dirKnowledge) apply(id lineage.ID, e *lineage.Event, old *ast.Symbol, cur bool) {
	if old != nil {
		k.leave(id, path.Dir(old.Path))
	}
	if cur {
		dir := path.Dir(e.New.Path)
		if k.inDir[dir] == nil {
			k.inDir[dir] = map[lineage.ID]bool{}
		}
		k.inDir[dir][id] = true
		k.dirty[dir] = true
	}
	k.Apply(id, e, old, cur)
}

func (k *dirKnowledge) leave(id lineage.ID, dir string) {
	delete(k.inDir[dir], id)
	k.dirty[dir] = true
}

// state returns a directory's knowers, orphaned share and bus factor at t.
// Directories whose symbols changed since the last call are summed again,
// symbol by symbol in ID order so the result does not depend on map order.
func (k *dirKnowledge) state(dir string, t time.Time) (knowers []Knower, orphaned float64, bus int) {
	if k.dirty[dir] {
		delete(k.dirty, dir)
		ids := make([]lineage.ID, 0, len(k.inDir[dir]))
		for id := range k.inDir[dir] {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		by := map[int]float64{}
		for _, id := range ids {
			knowledge.Sum(by, k.Weights(id))
		}
		k.sums[dir] = knowledge.Rank(by)
	}
	sum := k.sums[dir]
	total := knowledge.Total(sum)
	if total <= 0 {
		return nil, 0, 0
	}
	for i, w := range sum {
		share := w.W / total
		gone := k.Departed(w.Author, t)
		if gone {
			orphaned += share
		}
		if i < maxKnowers {
			knowers = append(knowers, Knower{Author: w.Author, Share: share, Departed: gone})
		}
	}
	return knowers, min(1, orphaned), knowledge.BusFactor(sum)
}
