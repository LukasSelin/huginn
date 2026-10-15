package story

import (
	"cmp"
	"fmt"
	"math"
	"slices"

	"huginn/internal/journey"
	"huginn/internal/lineage"
)

// Contributor is someone who committed in a chapter, with what they did.
type Contributor struct {
	Name    string
	Commits int
	Edits   int // semantic edits
}

// maxContributors is how many contributors a chapter lists.
const maxContributors = 3

// people gathers who committed in the revisions [from, to): how many
// distinct authors, those with the most semantic edits, and those whose
// first commit it holds, by that commit. first maps each author to the
// revision of their first commit.
func people(h *lineage.History, j *journey.Journey, first map[string]int, from, to int) (authors int, top []Contributor, newcomers []string) {
	by := map[string]*Contributor{}
	for i := from; i < to; i++ {
		a := h.AuthorAt(i)
		if a == "" {
			continue
		}
		c := by[a]
		if c == nil {
			c = &Contributor{Name: a}
			by[a] = c
			if first[a] == i {
				newcomers = append(newcomers, a)
			}
		}
		c.Commits++
		c.Edits += editsIn(j.Frames[i])
	}
	for _, c := range by {
		if c.Edits > 0 {
			top = append(top, *c)
		}
	}
	slices.SortFunc(top, func(a, b Contributor) int {
		return cmp.Or(cmp.Compare(b.Edits, a.Edits), cmp.Compare(b.Commits, a.Commits), cmp.Compare(a.Name, b.Name))
	})
	return len(by), top[:min(len(top), maxContributors)], newcomers
}

// firstCommits maps each author to the revision of their first commit.
func firstCommits(h *lineage.History) map[string]int {
	out := map[string]int{}
	for i := range h.Revisions() {
		if a := h.AuthorAt(i); a != "" {
			if _, ok := out[a]; !ok {
				out[a] = i
			}
		}
	}
	return out
}

// newcomerMoments marks each author's first commit, except the very first
// commit's: someone has to start.
func newcomerMoments(j *journey.Journey, first map[string]int) []Moment {
	var out []Moment
	for a, rev := range first {
		if rev == 0 {
			continue
		}
		f := j.Frames[rev]
		n := editsIn(f)
		text := "First commit by " + a
		if n > 0 {
			text += ", with " + plural(n, "semantic edit")
		}
		out = append(out, Moment{Kind: Newcomer, Time: f.Start, Rev: rev, SHA: f.SHA, Count: n,
			Text: text, Weight: 1 + 0.5*math.Log1p(float64(n))})
	}
	return out
}

func editsIn(f journey.Frame) int {
	n := 0
	for _, p := range f.Packages {
		n += p.Edits.Total()
	}
	return n
}

const (
	// handoffShare is the share of a package its new top knower must hold
	// for a change of top knower to be a handoff.
	handoffShare = 0.4
	// orphanedShare is the share departed authors must hold of a package
	// for it to count as orphaned.
	orphanedShare = 0.5
)

// knowledgeMoments finds where a package changes hands, its top knower
// changing to one who holds at least handoffShare of it, and where a
// package is first orphaned, departed authors holding more than
// orphanedShare of it.
func knowledgeMoments(j *journey.Journey, pkgs []string) []Moment {
	var out []Moment
	top := make([]int, len(pkgs)) // top knower + 1, 0 for none yet
	orphaned := make([]bool, len(pkgs))
	for rev, f := range j.Frames {
		for _, p := range f.Packages {
			if len(p.Knowers) == 0 {
				continue
			}
			k := p.Knowers[0]
			if prev := top[p.Package] - 1; prev >= 0 && prev != k.Author && k.Share >= handoffShare {
				out = append(out, Moment{Kind: Handoff, Time: f.Start, Rev: rev, SHA: f.SHA, Package: pkgs[p.Package],
					Count:  pct(k.Share),
					Text:   fmt.Sprintf("%s now knows %s best, holding %d%%; %s did before", j.Authors[k.Author], pkgName(pkgs[p.Package]), pct(k.Share), j.Authors[prev]),
					Weight: 1.5 + k.Share})
			}
			top[p.Package] = k.Author + 1
			if p.Orphaned > orphanedShare && !orphaned[p.Package] {
				orphaned[p.Package] = true
				out = append(out, Moment{Kind: Orphaned, Time: f.Start, Rev: rev, SHA: f.SHA, Package: pkgs[p.Package],
					Count:  pct(p.Orphaned),
					Text:   fmt.Sprintf("%d%% of what is known of %s is held by people who have not committed in six months", pct(p.Orphaned), pkgName(pkgs[p.Package])),
					Weight: 2 + p.Orphaned})
			}
		}
	}
	return out
}

func pct(share float64) int { return int(math.Round(100 * share)) }
