// Package story tells a repository's history as a handful of chapters.
//
// It cuts the history where the work shifts: where the edits move to other
// packages, where packages are born, where the commit rate changes sharply,
// and after releases. Each chapter gets its facts (dates, commits, symbols
// added and removed, the hottest package, releases, what commit messages
// talk about, who committed, and its key moments), and a Narrator turns
// those facts into a title and a paragraph. Everything is deterministic: the
// same history always tells the same story.
package story

import (
	"cmp"
	"context"
	"math"
	"path"
	"slices"
	"time"

	"huginn/internal/ast"
	"huginn/internal/codemap"
	"huginn/internal/journey"
	"huginn/internal/lineage"
)

const (
	minChapters = 3
	maxChapters = 7

	// Histories spanning at least this many weeks are segmented by week;
	// shorter ones commit by commit.
	weeklyFrom = 16
)

// MomentKind says what a key moment is about.
type MomentKind uint8

const (
	Birth      MomentKind = iota + 1 // a package got its first symbols
	Death                            // a package lost its last symbols
	Rename                           // symbols were renamed in one commit
	Move                             // symbols moved between packages or files in one commit
	Signature                        // exported signatures changed in one commit
	Quiet                            // work resumed after a long pause
	Busiest                          // the week with the most commits
	Tagged                           // a release was tagged
	Newcomer                         // someone committed for the first time
	DepAdded                         // a package started depending on another
	DepRemoved                       // a package stopped depending on another
	CycleIn                          // the first dependency cycle appeared
	CycleOut                         // the last dependency cycle went
)

func (k MomentKind) String() string {
	switch k {
	case Birth:
		return "birth"
	case Death:
		return "death"
	case Rename:
		return "rename"
	case Move:
		return "move"
	case Signature:
		return "signature"
	case Quiet:
		return "quiet"
	case Busiest:
		return "busiest"
	case Tagged:
		return "release"
	case Newcomer:
		return "newcomer"
	case DepAdded:
		return "dep-added"
	case DepRemoved:
		return "dep-removed"
	case CycleIn:
		return "cycle"
	case CycleOut:
		return "cycle-gone"
	}
	return "unknown"
}

// Moment is one key moment in the history.
type Moment struct {
	Kind    MomentKind
	Time    time.Time
	Rev     int    // index of the revision it happened in, oldest first
	SHA     string // of that revision
	Package string // the package it is about, if one
	Tag     string // the release's tag, for a release
	Count   int    // symbols, commits, days or references, depending on the kind
	Text    string // one line, stating only facts
	Weight  float64
}

// Facts are what a chapter is made of, before it is narrated.
type Facts struct {
	Number, Of int // 1-based chapter number, and how many there are

	From, To           time.Time // first and last commit
	FirstRev, LastRev  int       // revision indexes, inclusive
	Commits            int
	Added, Removed     int // symbols
	Renamed, Moved     int // symbol edits of these kinds
	Signature, Edits   int // signature edits, and semantic edits of any kind
	Hottest            string
	HottestEdits       int
	Runner             string // the second hottest package, if any
	RunnerEdits        int
	Births, Deaths     []string // packages, by path
	Live, LivePackages int      // at the end of the chapter
	Releases           []string // tags released in the chapter, oldest first
	Themes             Themes   // what the commit messages are about

	Authors      int           // distinct commit authors
	Contributors []Contributor // those with the most semantic edits, most first
	Newcomers    []string      // authors whose first commit is in the chapter, by that commit

	Moments []Moment // by time
}

// Narration is a narrated chapter.
type Narration struct {
	Title string
	Text  string
}

// A Narrator turns a chapter's facts into prose. It must not state anything
// the facts do not contain.
//
// Templates is the built-in one. A narrator backed by a language model can
// wrap it: rewrite the templated narration into nicer prose and fall back
// to the template when the model is unavailable.
type Narrator interface {
	Narrate(ctx context.Context, f Facts) (Narration, error)
}

// Chapter is a narrated chapter.
type Chapter struct {
	Facts
	Narration
}

// Story is a history told in chapters.
type Story struct {
	Chapters []Chapter
	Moments  []Moment // every key moment, by time
	Releases []Moment // every release, by time, whether a key moment or not
}

// Build tells the story of h. j must be h replayed commit by commit:
// journey.Build(h, m, journey.Commit, ...) with m = codemap.Build(h). rels
// are the repository's tags, placed on h's line of history; tags whose
// commits h does not hold are ignored.
func Build(ctx context.Context, h *lineage.History, j *journey.Journey, rels []Release, n Narrator) (*Story, error) {
	st := &Story{Moments: []Moment{}, Releases: []Moment{}}
	if len(j.Frames) == 0 {
		return st, nil
	}
	pkgs := make([]string, len(j.Packages))
	for i, p := range j.Packages {
		pkgs[i] = p.Path
	}
	life := lives(j)
	rs := placeReleases(h, rels)
	first := firstCommits(h)
	all, sigs := moments(h, j, pkgs, life)
	st.Releases = releaseMoments(j, rs, sigs)
	all = append(all, st.Releases...)
	all = append(all, newcomerMoments(j, first)...)
	all = append(all, depMoments(j, pkgs, life)...)
	slices.SortStableFunc(all, byRev)

	us := units(j, life)
	cuts := segment(us, releaseBonus(us, rs))
	spans := make([][2]int, len(cuts))
	for c := range cuts {
		b := len(us)
		if c+1 < len(cuts) {
			b = cuts[c+1]
		}
		spans[c] = [2]int{us[cuts[c]].from, us[b-1].to}
	}
	msgs := messages(h)
	df := termChapters(msgs, spans)
	for c, sp := range spans {
		f := facts(j, pkgs, life, sp[0], sp[1])
		f.Number, f.Of = c+1, len(cuts)
		f.Releases = releasesIn(rs, f.FirstRev, f.LastRev)
		f.Themes = themes(msgs, sp[0], sp[1], df, len(spans))
		f.Authors, f.Contributors, f.Newcomers = people(h, j, first, sp[0], sp[1])
		f.Moments = keyMoments(all, f.FirstRev, f.LastRev)
		st.Moments = append(st.Moments, f.Moments...)
		nar, err := n.Narrate(ctx, f)
		if err != nil {
			return nil, err
		}
		st.Chapters = append(st.Chapters, Chapter{f, nar})
	}
	return st, nil
}

// limits is how many moments of a kind a chapter keeps, the weightiest
// ones. Kinds not listed are all kept.
var limits = map[MomentKind]int{
	Rename: 3, Move: 3, Signature: 3, Quiet: 3,
	Tagged: 4, Newcomer: 3, DepAdded: 3, DepRemoved: 3,
}

// keyMoments picks a chapter's key moments from all moments, by time: every
// birth, death, busiest week and dependency cycle, and the weightiest
// of the other kinds.
func keyMoments(all []Moment, first, last int) []Moment {
	var out, capped []Moment
	for _, m := range all {
		switch {
		case m.Rev < first || m.Rev > last:
		case limits[m.Kind] > 0:
			capped = append(capped, m)
		default:
			out = append(out, m)
		}
	}
	slices.SortStableFunc(capped, func(a, b Moment) int {
		return cmp.Or(cmp.Compare(b.Weight, a.Weight), cmp.Compare(b.Count, a.Count))
	})
	seen := map[MomentKind]int{}
	for _, m := range capped {
		if seen[m.Kind] < limits[m.Kind] {
			seen[m.Kind]++
			out = append(out, m)
		}
	}
	slices.SortStableFunc(out, byRev)
	return out
}

func byRev(a, b Moment) int {
	return cmp.Or(cmp.Compare(a.Rev, b.Rev), cmp.Compare(a.Kind, b.Kind), cmp.Compare(b.Weight, a.Weight), cmp.Compare(a.Text, b.Text))
}

// life records when packages were born and died, by revision.
type life struct {
	born, died [][]int // packages per revision
}

func lives(j *journey.Journey) life {
	l := life{born: make([][]int, len(j.Frames)), died: make([][]int, len(j.Frames))}
	alive := make([]bool, len(j.Packages))
	for i, f := range j.Frames {
		now := make([]bool, len(j.Packages))
		for _, p := range f.Packages {
			now[p.Package] = p.Live > 0
		}
		for p := range now {
			switch {
			case now[p] && !alive[p]:
				l.born[i] = append(l.born[i], p)
			case !now[p] && alive[p]:
				l.died[i] = append(l.died[i], p)
			}
		}
		alive = now
	}
	return l
}

// unit is a stretch of history segmentation works on: one commit, or one
// week for long histories.
type unit struct {
	start, end time.Time
	from, to   int // revisions [from, to)
	edits      map[int]int
	births     int
}

func units(j *journey.Journey, l life) []unit {
	frames := j.Frames
	first, last := frames[0].Start, frames[len(frames)-1].Start
	var us []unit
	if last.Sub(first) < weeklyFrom*7*24*time.Hour {
		for i, f := range frames {
			us = append(us, unit{start: f.Start, end: f.Start, from: i, to: i + 1, edits: editsOf(f), births: len(l.born[i])})
		}
		return us
	}
	// Weeks, empty ones included, so that rates see the pauses. A commit
	// dated before its predecessor's week counts in that week.
	u := unit{start: codemap.WeekOf(first), edits: map[int]int{}}
	u.end = u.start.AddDate(0, 0, 7)
	for i, f := range frames {
		for !f.Start.Before(u.end) {
			u.to = i
			us = append(us, u)
			u = unit{start: u.end, end: u.end.AddDate(0, 0, 7), from: i, edits: map[int]int{}}
		}
		for p, n := range editsOf(f) {
			u.edits[p] += n
		}
		u.births += len(l.born[i])
	}
	u.to = len(frames)
	return append(us, u)
}

func editsOf(f journey.Frame) map[int]int {
	out := map[int]int{}
	for _, p := range f.Packages {
		if n := p.Edits.Total(); n > 0 {
			out[p.Package] = n
		}
	}
	return out
}

// segment returns the indexes of the units that start chapters, the first
// always 0. It scores every unit with commits as a possible start: how
// different the distribution of edits over packages is in the units just
// before and just after it, how many packages it gives birth to, how much
// the commit rate changes, how long the pause before it was, and bonus,
// which is how strongly a release just before it ends a chapter. A start
// must score best among its neighbors, so one shift opens one chapter. The
// best starts win: at least minChapters chapters when the history allows,
// and more only for strong shifts.
func segment(us []unit, bonus []float64) []int {
	var active []int
	for i, u := range us {
		if u.to > u.from {
			active = append(active, i)
		}
	}
	if len(active) <= minChapters {
		return active
	}
	n := len(us)
	w := min(max(len(active)/8, 2), 12)
	edge := max(1, w/2)
	pause := pauseScores(us)

	type cand struct {
		i     int
		score float64
	}
	var cs []cand
	for _, i := range active {
		if i < edge || n-i < edge {
			continue
		}
		before, after := us[max(0, i-w):i], us[i:min(n, i+w)]
		s := shift(before, after) + min(1, 0.5*float64(us[i].births)) + 0.5*rateChange(before, after) + pause[i] + bonus[i]
		cs = append(cs, cand{i, s})
	}
	// Keep the local maxima first, then the rest, each best first.
	var peaks, rest []cand
	for k, c := range cs {
		peak := true
		for _, o := range cs {
			d := o.i - c.i
			if d != 0 && d > -w && d < w && (o.score > c.score || o.score == c.score && d < 0) {
				peak = false
				break
			}
		}
		if peak {
			peaks = append(peaks, cs[k])
		} else {
			rest = append(rest, cs[k])
		}
	}
	// Pick starts greedily, best first, weighing each by the chapter it
	// splits: a start that leaves a sliver of a chapter on either side
	// counts for less, and one inside a long chapter counts for more, by
	// the square root of its length against an even share of the commits,
	// so that one quiet stretch does not swallow years.
	total := us[n-1].to - us[0].from
	sliver := float64(total) / (4 * maxChapters)
	even := float64(total) / maxChapters
	cuts := []int{0}
	worth := func(c cand) float64 {
		k, _ := slices.BinarySearch(cuts, c.i)
		lo, hi := us[cuts[k-1]].from, us[n-1].to
		if k < len(cuts) {
			hi = us[cuts[k]].from
		}
		at := us[c.i].from
		small := float64(min(at-lo, hi-at))
		return c.score * min(1, small/sliver) * math.Sqrt(float64(hi-lo)/even)
	}
	pick := func(pool []cand, enough func(w float64) bool) {
		for len(cuts) < maxChapters {
			best, bw := -1, 0.0
			for k, c := range pool {
				if slices.Contains(cuts, c.i) {
					continue
				}
				if w := worth(c); best < 0 || w > bw {
					best, bw = k, w
				}
			}
			if best < 0 || !enough(bw) {
				return
			}
			cuts = append(cuts, pool[best].i)
			slices.Sort(cuts)
		}
	}
	pick(peaks, func(w float64) bool { return len(cuts) < minChapters || w >= strong })
	// Too few shifts stand out: split where the scores are best anyway.
	pick(rest, func(float64) bool { return len(cuts) < minChapters })
	for _, i := range active {
		if len(cuts) >= minChapters {
			break
		}
		if !slices.Contains(cuts, i) {
			cuts = append(cuts, i)
		}
	}
	slices.Sort(cuts)
	return cuts
}

// strong is the score a shift needs to open a chapter beyond minChapters.
const strong = 0.6

// pauseScores scores, per unit, the pause before it, from 0 to 1. For commits it
// is the gap since the previous commit against the median gap: twice the
// median scores 0, 32 times scores 1. For weeks it is the run of empty
// weeks before it: one scores 0, four score 1.
func pauseScores(us []unit) []float64 {
	out := make([]float64, len(us))
	if us[0].start.Equal(us[0].end) {
		gaps := make([]time.Duration, len(us))
		latest := us[0].start
		for i := 1; i < len(us); i++ {
			gaps[i] = max(0, us[i].start.Sub(latest))
			latest = later(latest, us[i].start)
		}
		med := max(median(gaps[1:]), time.Minute)
		for i := 1; i < len(us); i++ {
			ratio := float64(gaps[i]) / float64(med)
			out[i] = min(1, max(0, math.Log2(ratio)-1)/4)
		}
		return out
	}
	empty := 0
	for i, u := range us {
		if u.to == u.from {
			empty++
			continue
		}
		out[i] = min(1, float64(max(0, empty-1))/3)
		empty = 0
	}
	return out
}

func median(ds []time.Duration) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	s := slices.Clone(ds)
	slices.Sort(s)
	return s[len(s)/2]
}

// shift is the total variation distance between where edits went in two
// stretches: 0 for the same packages in the same proportions, 1 for
// entirely different packages.
func shift(a, b []unit) float64 {
	pa, ta := distribution(a)
	pb, tb := distribution(b)
	if ta == 0 || tb == 0 {
		return 0
	}
	d := 0.0
	for p, n := range pa {
		d += math.Abs(float64(n)/ta - float64(pb[p])/tb)
	}
	for p, n := range pb {
		if _, ok := pa[p]; !ok {
			d += float64(n) / tb
		}
	}
	return d / 2
}

func distribution(us []unit) (map[int]int, float64) {
	out, total := map[int]int{}, 0
	for _, u := range us {
		for p, n := range u.edits {
			out[p] += n
			total += n
		}
	}
	return out, float64(total)
}

// rateChange is how sharply the commit rate changes between two stretches,
// from 0 to 1 for a factor of 8 or more.
func rateChange(a, b []unit) float64 {
	ra, rb := rate(a), rate(b)
	if ra == 0 || rb == 0 {
		return 0
	}
	return min(1, math.Abs(math.Log2(rb/ra))/3)
}

// rate is how fast commits come in a stretch, 0 if it cannot tell. For
// commits it is the inverse of the median gap between them, so that one
// pause, which pauses scores on its own, does not dominate. For weeks it is
// commits per week.
func rate(us []unit) float64 {
	if us[0].start.Equal(us[0].end) {
		if len(us) < 2 {
			return 0
		}
		gaps := make([]time.Duration, 0, len(us)-1)
		for i := 1; i < len(us); i++ {
			gaps = append(gaps, us[i].start.Sub(us[i-1].start))
		}
		return 1 / max(median(gaps), time.Minute).Hours()
	}
	commits := 0
	for _, u := range us {
		commits += u.to - u.from
	}
	return (float64(commits) + 0.5) / float64(len(us))
}

// facts gathers the facts of the revisions [from, to).
func facts(j *journey.Journey, pkgs []string, l life, from, to int) Facts {
	f := Facts{FirstRev: from, LastRev: to - 1, From: j.Frames[from].Start, To: j.Frames[to-1].Start, Commits: to - from}
	edits := map[int]int{}
	for i := from; i < to; i++ {
		for _, p := range j.Frames[i].Packages {
			e := p.Edits
			f.Added += e.Of(ast.Added)
			f.Removed += e.Of(ast.Deleted)
			f.Renamed += e.Of(ast.Renamed)
			f.Moved += e.Of(ast.Moved)
			f.Signature += e.Of(ast.SignatureChanged)
			f.Edits += e.Total()
			if e.Total() > 0 {
				edits[p.Package] += e.Total()
			}
		}
		for _, p := range l.born[i] {
			f.Births = append(f.Births, pkgs[p])
		}
		for _, p := range l.died[i] {
			f.Deaths = append(f.Deaths, pkgs[p])
		}
	}
	ranked := slices.Collect(func(yield func(int) bool) {
		for p := range edits {
			if !yield(p) {
				return
			}
		}
	})
	slices.SortFunc(ranked, func(a, b int) int { return cmp.Or(cmp.Compare(edits[b], edits[a]), cmp.Compare(pkgs[a], pkgs[b])) })
	if len(ranked) > 0 {
		f.Hottest, f.HottestEdits = pkgs[ranked[0]], edits[ranked[0]]
	}
	if len(ranked) > 1 {
		f.Runner, f.RunnerEdits = pkgs[ranked[1]], edits[ranked[1]]
	}
	end := j.Frames[to-1]
	f.Live = end.Live
	for _, p := range end.Packages {
		if p.Live > 0 {
			f.LivePackages++
		}
	}
	slices.Sort(f.Births)
	slices.Sort(f.Deaths)
	return f
}

// moments finds the key moments of the whole history that edits and
// commit times make, and counts the exported signature changes per
// revision.
func moments(h *lineage.History, j *journey.Journey, pkgs []string, l life) ([]Moment, []int) {
	sigs := make([]int, len(j.Frames))
	var out []Moment
	at := func(rev int) (time.Time, string) { return j.Frames[rev].Start, j.Frames[rev].SHA }

	for rev := range j.Frames {
		t, sha := at(rev)
		for _, p := range l.born[rev] {
			n := 0
			for _, s := range j.Frames[rev].Packages {
				if s.Package == p {
					n = s.Live
				}
			}
			out = append(out, Moment{Kind: Birth, Time: t, Rev: rev, SHA: sha, Package: pkgs[p], Count: n,
				Text: pkgName(pkgs[p]) + " appears with " + plural(n, "symbol"), Weight: 3 + math.Log1p(float64(n))})
		}
		for _, p := range l.died[rev] {
			peak := j.Packages[p].Peak
			out = append(out, Moment{Kind: Death, Time: t, Rev: rev, SHA: sha, Package: pkgs[p], Count: peak,
				Text: pkgName(pkgs[p]) + " is gone; it once held " + plural(peak, "symbol"), Weight: 3 + math.Log1p(float64(peak))})
		}
	}

	// Renames, moves and exported signature changes, per commit.
	type group struct {
		rev   int
		pkg   string // the package most of them are in
		names []string
		dirs  map[[2]string]int
	}
	groups := map[MomentKind]map[int]*group{Rename: {}, Move: {}, Signature: {}}
	add := func(k MomentKind, rev int, e *lineage.Event, name string) {
		g := groups[k][rev]
		if g == nil {
			g = &group{rev: rev, dirs: map[[2]string]int{}}
			groups[k][rev] = g
		}
		g.names = append(g.names, name)
		g.dirs[[2]string{path.Dir(e.Old.Path), path.Dir(e.New.Path)}]++
	}
	known := map[string]bool{}
	for _, p := range pkgs {
		known[p] = true
	}
	for sym := range h.All() {
		for i := range sym.Events {
			e := &sym.Events[i]
			if e.Old == nil || e.New == nil || !known[path.Dir(e.New.Path)] {
				continue // added, deleted, or filtered out (tests)
			}
			rev, ok := h.Position(e.SHA)
			if !ok {
				continue
			}
			if e.Flags.Has(ast.Renamed) {
				add(Rename, rev, e, e.Old.QualifiedName()+" → "+e.New.QualifiedName())
			}
			if e.Flags.Has(ast.Moved) {
				add(Move, rev, e, e.New.QualifiedName())
			}
			if e.Flags.Has(ast.SignatureChanged) && (e.Old.Exported || e.New.Exported) {
				add(Signature, rev, e, e.New.QualifiedName())
				sigs[rev]++
			}
		}
	}
	for _, k := range []MomentKind{Rename, Move, Signature} {
		for _, g := range groups[k] {
			slices.Sort(g.names)
			t, sha := at(g.rev)
			n := len(g.names)
			// The pair of directories most of the group is in.
			var top [2]string
			for d, c := range g.dirs {
				if c > g.dirs[top] || c == g.dirs[top] && (d[0] < top[0] || d[0] == top[0] && d[1] < top[1]) {
					top = d
				}
			}
			m := Moment{Kind: k, Time: t, Rev: g.rev, SHA: sha, Package: top[1], Count: n, Weight: 1 + math.Log1p(float64(n))}
			switch k {
			case Rename:
				m.Text = "Renamed " + g.names[0]
				if n > 1 {
					m.Text = plural(n, "symbol") + " renamed, including " + g.names[0]
				}
			case Move:
				if top[0] != top[1] && g.dirs[top] == n {
					m.Text = plural(n, "symbol") + " moved from " + pkgName(top[0]) + " to " + pkgName(top[1])
				} else {
					m.Text = plural(n, "symbol") + " moved between files, including " + g.names[0]
				}
				if top[0] != top[1] {
					m.Weight += 1
				}
			case Signature:
				m.Text = "Exported signature of " + g.names[0] + " changed"
				if n > 1 {
					m.Text = plural(n, "exported signature") + " changed, including " + g.names[0]
				}
			}
			out = append(out, m)
		}
	}

	out = append(out, pauses(j)...)
	out = append(out, busiest(j)...)
	slices.SortStableFunc(out, byRev)
	return out, sigs
}

// pauses finds the gaps between commits that stand out: at least three days
// and eight times the median gap. A commit dated before one applied ahead
// of it, as merged commits often are, ends no pause: gaps run from the
// latest commit so far.
func pauses(j *journey.Journey) []Moment {
	if len(j.Frames) < 3 {
		return nil
	}
	gaps := make([]time.Duration, 0, len(j.Frames)-1)
	latest := j.Frames[0].Start
	for i := 1; i < len(j.Frames); i++ {
		gaps = append(gaps, max(0, j.Frames[i].Start.Sub(latest)))
		latest = later(latest, j.Frames[i].Start)
	}
	sorted := slices.Clone(gaps)
	slices.Sort(sorted)
	limit := max(3*24*time.Hour, 8*sorted[len(sorted)/2])
	var out []Moment
	for i, g := range gaps {
		if g < limit {
			continue
		}
		days := int(g.Hours() / 24)
		f := j.Frames[i+1]
		out = append(out, Moment{Kind: Quiet, Time: f.Start, Rev: i + 1, SHA: f.SHA, Count: days,
			Text: "Work resumes after " + plural(days, "quiet day"), Weight: 1.5 + math.Log1p(float64(days)/7)})
	}
	return out
}

// busiest finds the week with the most commits, when there is more than one
// week. The moment sits at the week's first commit.
func busiest(j *journey.Journey) []Moment {
	type week struct{ first, commits int }
	weeks := map[time.Time]*week{}
	var order []time.Time
	for i, f := range j.Frames {
		w := codemap.WeekOf(f.Start)
		if weeks[w] == nil {
			weeks[w] = &week{first: i}
			order = append(order, w)
		}
		weeks[w].commits++
	}
	if len(order) < 2 {
		return nil
	}
	best := order[0]
	for _, w := range order[1:] {
		if weeks[w].commits > weeks[best].commits {
			best = w
		}
	}
	b := weeks[best]
	f := j.Frames[b.first]
	return []Moment{{Kind: Busiest, Time: f.Start, Rev: b.first, SHA: f.SHA, Count: b.commits,
		Text: "The busiest week: " + plural(b.commits, "commit") + " in the week of " + best.Format("Jan 2, 2006"), Weight: 2.5}}
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// pkgName names a package directory in prose.
func pkgName(dir string) string {
	if dir == "." {
		return "the root package"
	}
	return dir
}
