package web

import (
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
)

func TestReviewAPI(t *testing.T) {
	hs := newHistory(t)
	_, ts := hs.server(t, false) // reviews do not wait for the index

	// The branch checked out, feature/min, into master by default.
	var rv reviewJSON
	if code := get(t, ts, "/api/review?path=util", &rv); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if rv.Head != "HEAD" || rv.HeadHash != hs.last || rv.Target != "master" || rv.MergeBase != hs.sig || rv.Commits != 1 {
		t.Errorf("review of %s (%s) into %s from %s, %d commits", rv.Head, rv.HeadHash, rv.Target, rv.MergeBase, rv.Commits)
	}
	if !slices.Equal(rv.Scope, []string{"util"}) || rv.Module != "example.com/shop" {
		t.Errorf("scope %q, module %q", rv.Scope, rv.Module)
	}
	if len(rv.Changes) != 1 || rv.Changes[0].Name != "util.Min" || rv.Changes[0].Kind != "added" {
		t.Errorf("changes = %+v", rv.Changes)
	}

	// The signature change of store.Open, reviewed as a merge request into
	// the first commit and limited to store: its caller in app is outside.
	rv = reviewJSON{}
	get(t, ts, "/api/review?head="+hs.sig+"&base="+hs.first+"&path=store", &rv)
	if rv.HeadName != hs.sig[:7] {
		t.Errorf("HeadName = %q", rv.HeadName)
	}
	b := rv.Blast
	if len(b.Callers) != 1 || b.Callers[0].Name != "app.Run" || !b.Callers[0].Outside || b.CallSites != 2 {
		t.Errorf("blast = %+v", b)
	}
	if !slices.Equal(b.APIChanges, []string{"store.Open"}) {
		t.Errorf("API changes = %q", b.APIChanges)
	}
	rel := rv.Relations
	if rel.AddedDeps == nil || rel.AddedRefs == nil || rel.NewPackages == nil || len(rel.AddedDeps)+len(rel.AddedRefs) > 0 {
		t.Errorf("relations = %+v, want empty lists", rel)
	}

	if code := get(t, ts, "/api/review?head=nope", nil); code != http.StatusBadRequest {
		t.Errorf("unknown head: status %d, want 400", code)
	}
}

func TestReviewOptions(t *testing.T) {
	hs := newHistory(t)
	_, ts := hs.server(t, false)
	var opts reviewOptionsJSON
	get(t, ts, "/api/review/options", &opts)
	if opts.Head != "feature/min" || opts.Target != "master" {
		t.Errorf("head %q, target %q", opts.Head, opts.Target)
	}
	if want := []string{"feature/min", "master"}; !slices.Equal(opts.Branches, want) {
		t.Errorf("branches = %q, want %q", opts.Branches, want)
	}
	if want := []string{"app", "store", "util"}; !slices.Equal(opts.Dirs, want) {
		t.Errorf("dirs = %q, want %q", opts.Dirs, want)
	}

	opts = reviewOptionsJSON{}
	get(t, ts, "/api/review/options?head="+hs.first, &opts)
	if want := []string{"app", "store", "util"}; !slices.Equal(opts.Dirs, want) {
		t.Errorf("dirs at the first commit = %q, want %q", opts.Dirs, want)
	}
}

func TestReviewPage(t *testing.T) {
	hs := newHistory(t)
	_, ts := hs.server(t, false)
	resp, err := http.Get(ts.URL + "/review")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `<script src="review.js">`) {
		t.Errorf("GET /review: %d\n%.300s", resp.StatusCode, body)
	}
}
