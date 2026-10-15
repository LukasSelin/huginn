package story

import (
	"cmp"
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
