package story

import (
	"cmp"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"huginn/internal/ast"
	"huginn/internal/git"
	"huginn/internal/journey"
	"huginn/internal/lineage"
)

// Release is a tag on the history. SHA is the commit on the walked line of
// history that brought the tagged commit in; tags off that line are
// ignored.
type Release struct {
	Name string
	SHA  string
}

// releaseKind ranks tags by how much they mark the end of a stretch of work.
type releaseKind uint8

const (
	preRelease   releaseKind = iota + 1 // v1.2.0-rc.1
	patchRelease                        // v1.2.3
	otherTag                            // anything not a version
	minorRelease                        // v1.2.0, v0.3.0
	majorRelease                        // v2.0.0
)

// version is a parsed version tag: v1.2.3, 1.2, go1.21.0, v1.0.0-rc.1, and
// the same behind a path prefix (tools/v0.3.0) for a nested module.
var version = regexp.MustCompile(`^(?:(.+)/)?(?:v|go)?(\d+)\.(\d+)(?:\.(\d+))?(-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)

type tag struct {
	name   string
	kind   releaseKind
	nested bool   // behind a path prefix: a nested module's release
	v      [3]int // major, minor, patch
	pre    string
}

func parseTag(name string) tag {
	t := tag{name: name, kind: otherTag}
	m := version.FindStringSubmatch(name)
	if m == nil {
		return t
	}
	for i := range 3 {
		t.v[i], _ = strconv.Atoi(m[2+i])
	}
	t.nested, t.pre = m[1] != "", m[5]
	switch {
	case t.pre != "":
		t.kind = preRelease
	case t.v[2] != 0:
		t.kind = patchRelease
	case t.v[1] == 0 && t.v[0] > 0:
		t.kind = majorRelease
	default:
		t.kind = minorRelease
	}
	return t
}

// better orders the tags of one commit, the one to name it by first: the
// main module's before nested ones, final versions before others, then the
// highest version, then "v1.2.0" before "1.2.0".
func better(a, b tag) int {
	rank := func(t tag) int {
		switch {
		case t.kind == otherTag:
			return 1
		case t.kind == preRelease:
			return 2
		}
		return 3
	}
	return cmp.Or(
		cmp.Compare(b2i(a.nested), b2i(b.nested)),
		cmp.Compare(rank(b), rank(a)),
		cmp.Compare(b.v[0], a.v[0]), cmp.Compare(b.v[1], a.v[1]), cmp.Compare(b.v[2], a.v[2]),
		cmp.Compare(b2i(!strings.HasPrefix(a.name, "v")), b2i(!strings.HasPrefix(b.name, "v"))),
		cmp.Compare(a.name, b.name))
}

// boundary is how strongly a release suggests a chapter ends with it.
func (t tag) boundary() float64 {
	if t.nested {
		return 0.1
	}
	switch t.kind {
	case majorRelease:
		return 1
	case minorRelease:
		return 0.6
	case otherTag:
		return 0.3
	case patchRelease:
		return 0.2
	}
	return 0.1
}

func (t tag) weight() float64 {
	if t.nested {
		return 1.5
	}
	switch t.kind {
	case majorRelease:
		return 4
	case minorRelease:
		return 3.5
	case otherTag:
		return 2.5
	case patchRelease:
		return 2
	}
	return 1.5
}

// released is the release made at one revision, named by its best tag.
type released struct {
	rev int
	tag tag
}

// placeReleases puts each release on the revision that brought it in,
// keeping one per revision, oldest first.
func placeReleases(h *lineage.History, rels []Release) []released {
	byRev := map[int]tag{}
	for _, r := range rels {
		rev, ok := h.Position(r.SHA)
		if !ok {
			continue
		}
		t := parseTag(r.Name)
		if old, ok := byRev[rev]; !ok || better(t, old) < 0 {
			byRev[rev] = t
		}
	}
	var out []released
	for rev, t := range byRev {
		out = append(out, released{rev, t})
	}
	slices.SortFunc(out, func(a, b released) int { return cmp.Compare(a.rev, b.rev) })
	return out
}

// releaseMoments tells each release with what changed since the previous
// one: the previous final release for a final one, else the previous of
// any kind. sigs counts exported signature changes per revision.
func releaseMoments(j *journey.Journey, rs []released, sigs []int) []Moment {
	var out []Moment
	for i, r := range rs {
		prev := -1
		for k := i - 1; k >= 0; k-- {
			if r.tag.kind == preRelease || rs[k].tag.kind != preRelease {
				prev = k
				break
			}
		}
		from, since := 0, "the first commit"
		if prev >= 0 {
			from, since = rs[prev].rev+1, rs[prev].tag.name
		}
		added, changed := 0, 0
		for rev := from; rev <= r.rev; rev++ {
			added += addedIn(j.Frames[rev])
			changed += sigs[rev]
		}
		text := r.tag.name + " released: "
		if added == 0 {
			text += "no symbols added since " + since
		} else {
			text += plural(added, "symbol") + " added since " + since
		}
		if changed > 0 {
			text += ", " + plural(changed, "exported signature") + " changed"
		}
		f := j.Frames[r.rev]
		out = append(out, Moment{Kind: Tagged, Time: f.Start, Rev: r.rev, SHA: f.SHA, Count: added,
			Text: text, Weight: r.tag.weight(), Tag: r.tag.name})
	}
	return out
}

// releaseBonus scores, per unit, a chapter starting there because a release
// ended the unit before: the chapter that holds a release ends with it, or
// with its week.
func releaseBonus(us []unit, rs []released) []float64 {
	out := make([]float64, len(us))
	for _, r := range rs {
		k, _ := slices.BinarySearchFunc(us, r.rev, func(u unit, rev int) int {
			switch {
			case u.to <= rev:
				return -1
			case u.from > rev:
				return 1
			}
			return 0
		})
		// The next unit with commits starts the next chapter.
		for k++; k < len(us) && us[k].to == us[k].from; k++ {
		}
		if k < len(us) {
			out[k] = max(out[k], r.tag.boundary())
		}
	}
	return out
}

// releasesIn names the releases among revisions [from, to], oldest first.
func releasesIn(rs []released, from, to int) []string {
	var out []string
	for _, r := range rs {
		if r.rev >= from && r.rev <= to {
			out = append(out, r.tag.name)
		}
	}
	return out
}

func addedIn(f journey.Frame) int {
	n := 0
	for _, p := range f.Packages {
		n += p.Edits.Of(ast.Added)
	}
	return n
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// FromTags returns the releases of tags, placed where they joined the
// walked line of history. Tags off that line are dropped.
func FromTags(tags []git.Tag) []Release {
	var out []Release
	for _, t := range tags {
		if t.Mainline != "" {
			out = append(out, Release{Name: t.Name, SHA: t.Mainline})
		}
	}
	return out
}
