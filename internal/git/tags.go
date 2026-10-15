package git

import (
	"bytes"
	"cmp"
	"context"
	"slices"
	"strings"
	"time"
)

// Tag is a tag that names a commit.
type Tag struct {
	Name   string
	Commit string    // the commit the tag points to, peeled
	Time   time.Time // the tagger date of an annotated tag, else the committer date

	// Mainline is the commit on the first-parent line of the walked branch
	// that brought Commit in: Commit itself when it is on that line, else
	// the merge whose second side first reached it. Empty when the branch
	// does not contain Commit.
	Mainline string
}

// Tags returns the tags that name commits, with the first-parent commit of
// opts.Branch that brought each in, sorted by the order those commits were
// made in, then by name. Only opts.Branch is used.
func (r *Repo) Tags(ctx context.Context, opts Options) ([]Tag, error) {
	cmd := r.command(ctx, "for-each-ref", "--format=%(refname:strip=2)%00%(objecttype)%00%(objectname)%00%(*objecttype)%00%(*objectname)%00%(creatordate:iso-strict)", "refs/tags")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, gitError(cmd, err, &stderr)
	}
	var tags []Tag
	for line := range strings.Lines(string(out)) {
		f := strings.Split(strings.TrimRight(line, "\r\n"), "\x00")
		if len(f) != 6 {
			continue
		}
		t := Tag{Name: f[0]}
		switch {
		case f[1] == "commit":
			t.Commit = f[2]
		case f[1] == "tag" && f[3] == "commit":
			t.Commit = f[4]
		default:
			continue // a tag of a tree or blob, or of another tag
		}
		t.Time, _ = time.Parse(time.RFC3339, f[5])
		tags = append(tags, t)
	}
	if len(tags) == 0 {
		return nil, nil
	}

	mainline, order, err := r.mainline(ctx, cmp.Or(opts.Branch, "HEAD"))
	if err != nil {
		return nil, err
	}
	for i := range tags {
		tags[i].Mainline = mainline[tags[i].Commit]
	}
	slices.SortStableFunc(tags, func(a, b Tag) int {
		oa, ok1 := order[a.Mainline]
		ob, ok2 := order[b.Mainline]
		switch {
		case ok1 != ok2 && ok1:
			return -1
		case ok1 != ok2:
			return 1
		}
		return cmp.Or(cmp.Compare(oa, ob), a.Time.Compare(b.Time), cmp.Compare(a.Name, b.Name))
	})
	return tags, nil
}

// mainline maps every commit of branch to the first-parent commit that
// brought it in, and numbers the first-parent commits oldest first.
func (r *Repo) mainline(ctx context.Context, branch string) (map[string]string, map[string]int, error) {
	cmd := r.command(ctx, "rev-list", "--parents", "--topo-order", "--reverse", "--end-of-options", branch, "--")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, nil, gitError(cmd, err, &stderr)
	}
	parents := map[string][]string{}
	var tip string
	for line := range strings.Lines(string(out)) {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		parents[f[0]] = f[1:]
		tip = f[0]
	}
	// The first-parent line, newest first, then flipped.
	var line []string
	for c := tip; c != ""; {
		line = append(line, c)
		ps := parents[c]
		c = ""
		if len(ps) > 0 {
			c = ps[0]
		}
	}
	slices.Reverse(line)

	by := map[string]string{}
	order := map[string]int{}
	for i, c := range line {
		by[c] = c
		order[c] = i
	}
	// Oldest first, each mainline commit claims what its other parents
	// reach that no earlier one did.
	for _, c := range line {
		stack := slices.Clone(parents[c][min(1, len(parents[c])):])
		for len(stack) > 0 {
			x := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if _, ok := by[x]; ok {
				continue
			}
			by[x] = c
			stack = append(stack, parents[x]...)
		}
	}
	return by, order, nil
}
