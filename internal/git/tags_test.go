package git

import (
	"context"
	"fmt"
	"testing"
)

func TestTags(t *testing.T) {
	r := newTestRepo(t)
	root := r.Commit("root", map[string]string{"a.go": "package a\n"})
	r.Git("tag", "v0.1.0") // lightweight
	r.Git("checkout", "-q", "-b", "feature")
	feature := r.Commit("feature", map[string]string{"b.go": "package a\n"})
	r.Git("tag", "-a", "-m", "release candidate", "v0.2.0-rc.1") // annotated, off the line
	r.Git("checkout", "-q", "-b", "unmerged")
	unmerged := r.Commit("unmerged", map[string]string{"c.go": "package a\n"})
	r.Git("tag", "never")
	r.Git("checkout", "-q", "master")
	r.Commit("mainline", map[string]string{"a.go": "package a // edited\n"})
	r.Days++
	r.Git("merge", "-q", "--no-ff", "-m", "merge feature", "feature")
	merge := r.Git("rev-parse", "HEAD")
	r.Git("tag", "-a", "-m", "v0.2.0", "v0.2.0")
	r.Git("tag", "tree", "HEAD^{tree}") // not a commit: skipped

	repo, err := Open(r.Dir)
	if err != nil {
		t.Fatal(err)
	}
	tags, err := repo.Tags(context.Background(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, tg := range tags {
		got = append(got, fmt.Sprintf("%s %.7s %.7s", tg.Name, tg.Commit, tg.Mainline))
	}
	want := []string{
		fmt.Sprintf("v0.1.0 %.7s %.7s", root, root),
		fmt.Sprintf("v0.2.0-rc.1 %.7s %.7s", feature, merge), // tagged before v0.2.0
		fmt.Sprintf("v0.2.0 %.7s %.7s", merge, merge),
		fmt.Sprintf("never %.7s ", unmerged),
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("tags:\n got %q\nwant %q", got, want)
	}
	if !tags[0].Time.Equal(r.Date(1)) || !tags[2].Time.Equal(r.Date(r.Days)) {
		t.Errorf("tag times %v, %v", tags[0].Time, tags[2].Time)
	}
}
