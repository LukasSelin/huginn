package health

import (
	"testing"
	"time"

	"huginn/internal/ast"
	"huginn/internal/lineage"
)

func TestReadIntent(t *testing.T) {
	for _, c := range []struct {
		subject, detail string
		want            Intent
	}{
		{"fix(parser): handle empty input", "", Fix},
		{"feat: add the health page", "", Feature},
		{"refactor: split walk", "", Other},
		{"docs: fix a typo", "", Other},
		{"Fix the cache key on Windows", "", Fix},
		{"Stop a cancelled walk from hanging on Windows", "", Fix},
		{"parser: fix crash on generics", "", Fix},
		{"Add the journey view", "", Feature},
		{"Size the code map by the color metric", "", Other},
		{"Revert \"Add the journey view\"", "", Revert},
		{"Merge branch 'master' into topic", "", Merge},
		{"Merge pull request #7 from a/b", "Prevent a panic on empty repos", Fix},
		{"Merge pull request #8 from a/b", "Tell the journey in chapters", Other},
		{"Don't crash on binary files", "", Fix},
	} {
		if got := ReadIntent(c.subject, c.detail); got != c.want {
			t.Errorf("ReadIntent(%q, %q) = %d, want %d", c.subject, c.detail, got, c.want)
		}
	}
}

// TestIntents counts each bucket's commits by intent, the edits fixes made,
// and the added symbols a later fix edited.
func TestIntents(t *testing.T) {
	f1, f2, f3 := fn("p/a.go", "F", 1, 5, 1), fn("p/a.go", "F", 1, 6, 2), fn("p/a.go", "F", 1, 7, 2)
	g := fn("p/a.go", "G", 8, 9, 1)
	h := lineage.New()
	h.Apply(lineage.Revision{SHA: "a", Time: t0, Author: "ann", Subject: "feat: add F and G",
		Changes: []ast.Change{{Flags: ast.Added, New: f1}, {Flags: ast.Added, New: g}}})
	h.Apply(lineage.Revision{SHA: "b", Time: t0.Add(time.Hour), Author: "ann", Subject: "Merge branch 'x'"})
	h.Apply(lineage.Revision{SHA: "c", Time: t0.Add(2 * time.Hour), Author: "bob", Subject: "Fix F on empty input",
		Changes: []ast.Change{{Flags: ast.BodyChanged, Old: f1, New: f2}}})
	h.Apply(lineage.Revision{SHA: "d", Time: t0.Add(3 * time.Hour), Author: "bob", Subject: "Revert \"Fix F\"",
		Changes: []ast.Change{{Flags: ast.BodyChanged, Old: f2, New: f3}}})

	r := Build(h, Options{Buckets: 2})
	b0, b1 := r.Buckets[0], r.Buckets[1]
	if b0.Commits != 1 || b0.Features != 1 || b0.Fixes != 0 || b0.Added != 2 || b0.Fixed != 1 {
		t.Errorf("bucket 0 = %+v, want 1 commit, 1 feature, 0 fixes, 2 added, 1 fixed", b0)
	}
	if b1.Commits != 2 || b1.Fixes != 2 || b1.Reverts != 1 || b1.FixEdits != 2 {
		t.Errorf("bucket 1 = %+v, want 2 commits, 2 fixes, 1 revert, 2 fix edits", b1)
	}
	if len(r.Hotspots) != 1 || r.Hotspots[0].Edits != 4 || r.Hotspots[0].Fixes != 2 {
		t.Errorf("hotspots = %+v, want p/a.go with 4 edits, 2 by fixes", r.Hotspots)
	}
}
