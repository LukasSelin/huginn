package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"huginn/internal/git"
	"huginn/internal/lineage"
)

// testRepo is a throwaway repository whose commits are a day apart, as in
// the git package's tests.
type testRepo struct {
	t    *testing.T
	dir  string
	days int
}

func newTestRepo(t *testing.T) *testRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	global := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(global, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	r := &testRepo{t: t, dir: t.TempDir()}
	r.git("init", "-q", "-b", "master")
	r.git("config", "user.name", "Ada")
	r.git("config", "user.email", "ada@example.com")
	return r
}

func (r *testRepo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	date := time.Date(2026, 1, r.days, 12, 0, 0, 0, time.UTC).Format(time.RFC3339)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// commit writes files, deleting those whose content is empty, commits
// everything and returns the commit hash.
func (r *testRepo) commit(msg string, files map[string]string) string {
	r.t.Helper()
	r.days++
	for name, src := range files {
		p := filepath.Join(r.dir, filepath.FromSlash(name))
		if src == "" {
			if err := os.Remove(p); err != nil {
				r.t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			r.t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			r.t.Fatal(err)
		}
	}
	r.git("add", "-A")
	r.git("commit", "-q", "--allow-empty", "-m", msg)
	return r.git("rev-parse", "HEAD")
}

const storeV1 = `package store

// Open opens the database at path.
func Open(path string) error {
	return nil
}
`

const storeV2 = `package store

// Open opens the database at path, read-only if asked to.
func Open(path string, readOnly bool) error {
	if readOnly {
		return nil
	}
	return nil
}
`

const appV1 = `package app

import "example.com/shop/store"

func Run() {
	store.Open("a")
	store.Open("b")
}
`

const appV2 = `package app

import "example.com/shop/store"

func Run() {
	store.Open("a", false)
	store.Open("b", true)
}
`

const util = `package util

// Max returns the larger of a and b.
func Max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
`

// history is three commits; the second changes the signature of an exported
// function, updates its callers and touches a file only in a comment.
type history struct {
	repo             *testRepo
	first, sig, last string
}

func newHistory(t *testing.T) *history {
	r := newTestRepo(t)
	hs := &history{repo: r}
	hs.first = r.commit("Add store and app", map[string]string{
		"go.mod":         "module example.com/shop\n\ngo 1.24\n",
		"store/store.go": storeV1,
		"app/app.go":     appV1,
		"util/util.go":   util,
		"README.md":      "shop\n",
	})
	hs.sig = r.commit("Make Open optionally read-only\n\nCallers say whether they write.", map[string]string{
		"store/store.go": storeV2,
		"app/app.go":     appV2,
		"util/util.go":   strings.Replace(util, "the larger", "the greater", 1),
		"README.md":      "shop, a store\n",
	})
	r.git("checkout", "-q", "-b", "feature/min")
	hs.last = r.commit("Add Min", map[string]string{
		"util/util.go": util + "\n// Min returns the smaller of a and b.\nfunc Min(a, b int) int {\n\tif a < b {\n\t\treturn a\n\t}\n\treturn b\n}\n",
	})
	return hs
}

// server returns a test server over the history, indexed if index is set.
func (hs *history) server(t *testing.T, index bool) (*Server, *httptest.Server) {
	t.Helper()
	repo, err := git.Open(hs.repo.dir)
	if err != nil {
		t.Fatal(err)
	}
	s := New(repo, "shop", lineage.BuildOptions{})
	if index {
		if err := s.Index(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts
}

func get(t *testing.T, ts *httptest.Server, path string, v any) int {
	t.Helper()
	resp, err := http.Get(ts.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || v == nil {
		return resp.StatusCode
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("GET %s: Content-Type %q", path, ct)
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp.StatusCode
}

func TestCommitReport(t *testing.T) {
	hs := newHistory(t)
	_, ts := hs.server(t, true)

	var c commitJSON
	if code := get(t, ts, "/api/commit/"+hs.sig, &c); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}

	if c.SHA != hs.sig || c.Author != "Ada" || c.Email != "ada@example.com" {
		t.Errorf("header: sha %s author %s <%s>", c.SHA, c.Author, c.Email)
	}
	if c.Subject != "Make Open optionally read-only" || !strings.Contains(c.Message, "Callers say whether they write.") {
		t.Errorf("message: subject %q, message %q", c.Subject, c.Message)
	}
	if !slices.Equal(c.Parents, []string{hs.first}) {
		t.Errorf("parents %v, want [%s]", c.Parents, hs.first)
	}
	if want := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC).Unix(); c.T != want {
		t.Errorf("time %d, want %d", c.T, want)
	}

	// Go lines only on the headline; the README counts in the totals.
	if c.Added == 0 || c.TotalAdded != c.Added+1 || c.TotalRemoved != c.Removed+1 {
		t.Errorf("lines: +%d −%d of +%d −%d", c.Added, c.Removed, c.TotalAdded, c.TotalRemoved)
	}
	// Open changed signature and body, Run its body.
	if c.Symbols != 2 || c.Semantic != 3 {
		t.Errorf("symbols %d, semantic changes %d; want 2, 3", c.Symbols, c.Semantic)
	}
	if want := map[string]int{"signature": 1, "body": 2}; !mapsEqual(c.Counts, want) {
		t.Errorf("counts %v, want %v", c.Counts, want)
	}
	if !strings.HasPrefix(c.Headline, "+") || !strings.Contains(c.Headline, "3 semantic changes across 2 symbols") {
		t.Errorf("headline %q", c.Headline)
	}

	var files []string
	for _, f := range c.Files {
		files = append(files, f.Path+" "+strings.Join(f.Kinds, ","))
	}
	if want := []string{"app/app.go body", "store/store.go signature,body"}; !slices.Equal(files, want) {
		t.Errorf("files %q, want %q", files, want)
	}

	if len(c.Changes) != 2 {
		t.Fatalf("changes %+v", c.Changes)
	}
	var open changeJSON
	for _, ch := range c.Changes {
		if ch.Name == "store.Open" {
			open = ch
		}
	}
	if open.Kind != "signature" || !slices.Equal(open.Kinds, []string{"signature", "body"}) ||
		!slices.Equal(open.Flags, []string{"signature", "body"}) {
		t.Errorf("Open kinds: %s %v %v", open.Kind, open.Kinds, open.Flags)
	}
	if open.Package != "example.com/shop/store" || open.Symbol != "func" || open.Path != "store/store.go" || open.Line != 4 {
		t.Errorf("Open location: %s %s %s:%d", open.Package, open.Symbol, open.Path, open.Line)
	}
	if open.OldSignature != "func Open(path string) error" || open.NewSignature != "func Open(path string, readOnly bool) error" {
		t.Errorf("Open signatures: %q → %q", open.OldSignature, open.NewSignature)
	}
	if open.StmtsBefore != 1 || open.StmtsAfter != 3 || open.StmtsDelta != 2 || open.ComplexityDelta != 1 {
		t.Errorf("Open size: stmts %d → %d (%+d), complexity %+d", open.StmtsBefore, open.StmtsAfter, open.StmtsDelta, open.ComplexityDelta)
	}
	if !open.Exported || !open.APIChange || open.CallSites != 2 || open.Summary == "" || open.Impact != "2 call sites · exported" {
		t.Errorf("Open impact: exported %v api %v sites %d summary %q impact %q",
			open.Exported, open.APIChange, open.CallSites, open.Summary, open.Impact)
	}
	wantCaller := callerJSON{Package: "example.com/shop/app", Name: "app.Run", Path: "app/app.go", Line: 5, Calls: 2}
	if !slices.Equal(open.Callers, []callerJSON{wantCaller}) {
		t.Errorf("Open callers %+v, want %+v", open.Callers, wantCaller)
	}

	b := c.Blast
	if b.CallSites != 2 || !slices.Equal(b.Packages, []string{"example.com/shop/app"}) || len(b.Callers) != 1 {
		t.Errorf("blast: %d sites, packages %v, callers %+v", b.CallSites, b.Packages, b.Callers)
	}
	if !b.ExportedAPIChanged || !slices.Equal(b.APIChanges, []string{"store.Open"}) {
		t.Errorf("blast API: %v %v", b.ExportedAPIChanged, b.APIChanges)
	}

	if len(c.Noise) != 1 || c.Noise[0].Path != "util/util.go" || c.Noise[0].Reason != "formatting or comments" ||
		c.Noise[0].Added != 1 || c.Noise[0].Removed != 1 {
		t.Errorf("noise %+v", c.Noise)
	}

	if c.Nav == nil || !c.Nav.Indexed || c.Nav.Prev != hs.first || c.Nav.Next != hs.last {
		t.Errorf("nav %+v, want prev %s next %s", c.Nav, hs.first, hs.last)
	}
}

func mapsEqual(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestCommitRevisions(t *testing.T) {
	hs := newHistory(t)
	_, ts := hs.server(t, true)

	// Any revision git understands names the commit, branch names with
	// slashes included.
	for rev, want := range map[string]string{
		hs.sig[:7]:    hs.sig,
		"master":      hs.sig,
		"feature/min": hs.last,
		"HEAD~2":      hs.first,
	} {
		var c commitJSON
		if code := get(t, ts, "/api/commit/"+rev, &c); code != http.StatusOK || c.SHA != want {
			t.Errorf("%s: status %d, sha %s; want %s", rev, code, c.SHA, want)
		}
	}

	// The ends of the history have no neighbor there.
	var c commitJSON
	get(t, ts, "/api/commit/"+hs.first, &c)
	if c.Nav.Prev != "" || c.Nav.Next != hs.sig || len(c.Parents) != 0 {
		t.Errorf("first commit: nav %+v, parents %v", c.Nav, c.Parents)
	}
	if c.Symbols == 0 || c.Counts["added"] != c.Symbols {
		t.Errorf("first commit: %d symbols, counts %v", c.Symbols, c.Counts)
	}
	c = commitJSON{}
	get(t, ts, "/api/commit/"+hs.last, &c)
	if c.Nav.Prev != hs.sig || c.Nav.Next != "" {
		t.Errorf("last commit: nav %+v", c.Nav)
	}

	for _, rev := range []string{"nope", "0000000000000000000000000000000000000000", hs.first + "^", "--all"} {
		if code := get(t, ts, "/api/commit/"+rev, nil); code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", rev, code)
		}
	}
}

func TestCommitBeforeIndexing(t *testing.T) {
	hs := newHistory(t)
	_, ts := hs.server(t, false)

	if code := get(t, ts, "/api/health", nil); code != http.StatusServiceUnavailable {
		t.Errorf("/api/health: status %d, want 503", code)
	}
	// Reports need no index; the parent stands in for the previous commit.
	var c commitJSON
	if code := get(t, ts, "/api/commit/"+hs.sig, &c); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if c.Nav.Indexed || c.Nav.Prev != hs.first || c.Nav.Next != "" {
		t.Errorf("nav %+v", c.Nav)
	}
}

func TestCommitReportIsCached(t *testing.T) {
	hs := newHistory(t)
	s, ts := hs.server(t, true)

	var builds atomic.Int32
	s.reports = newReportCache(2)
	s.build = func(ctx context.Context, sha string) (*commitJSON, error) {
		builds.Add(1)
		return s.buildReport(ctx, sha)
	}

	var c commitJSON
	for _, rev := range []string{hs.sig, hs.sig[:8], "master"} {
		get(t, ts, "/api/commit/"+rev, &c)
	}
	if n := builds.Load(); n != 1 {
		t.Errorf("one commit under three names built %d times, want once", n)
	}

	// Two more commits evict the least recently used.
	get(t, ts, "/api/commit/"+hs.first, &c)
	get(t, ts, "/api/commit/"+hs.last, &c)
	get(t, ts, "/api/commit/"+hs.sig, &c)
	if n := builds.Load(); n != 4 {
		t.Errorf("after eviction: %d builds, want 4", n)
	}
}

func TestReportCacheSharesBuilds(t *testing.T) {
	c := newReportCache(4)
	var builds atomic.Int32
	release := make(chan struct{})
	build := func(ctx context.Context, sha string) (*commitJSON, error) {
		builds.Add(1)
		<-release
		if sha == "bad" {
			return nil, errors.New("boom")
		}
		return &commitJSON{SHA: sha}, nil
	}

	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if rep, err := c.get(context.Background(), "abc", build); err != nil || rep.SHA != "abc" {
				t.Errorf("get: %v, %v", rep, err)
			}
		}()
	}
	// Let every goroutine reach the cache before the build finishes.
	for {
		c.mu.Lock()
		n := c.lru.Len()
		c.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	if n := builds.Load(); n != 1 {
		t.Errorf("concurrent gets built %d times, want once", n)
	}

	// Failures are not cached.
	for range 2 {
		if _, err := c.get(context.Background(), "bad", build); err == nil {
			t.Error("get bad: no error")
		}
	}
	if n := builds.Load(); n != 3 {
		t.Errorf("failed build retried: %d builds, want 3", n)
	}
}

func TestSymbolEventsCarryCommits(t *testing.T) {
	hs := newHistory(t)
	_, ts := hs.server(t, true)

	var hits []symHitJSON
	get(t, ts, "/api/journey/symbols?q=Open", &hits)
	hits = slices.DeleteFunc(hits, func(h symHitJSON) bool { return h.Dir != "store" })
	if len(hits) != 1 {
		t.Fatalf("store hits %+v", hits)
	}
	var sym symbolJSON
	get(t, ts, "/api/symbol/"+itoa(int64(hits[0].ID)), &sym)
	var shas []string
	for _, e := range sym.Events {
		shas = append(shas, e.SHA)
	}
	if want := []string{hs.first, hs.sig}; !slices.Equal(shas, want) {
		t.Fatalf("event shas %v, want %v", shas, want)
	}
	// Each event's commit has a report naming the symbol.
	for _, sha := range shas {
		var c commitJSON
		get(t, ts, "/api/commit/"+sha, &c)
		if !slices.ContainsFunc(c.Changes, func(ch changeJSON) bool { return ch.Name == "store.Open" }) {
			t.Errorf("%s: no change to store.Open in %+v", sha, c.Changes)
		}
	}
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
