package lineage

import (
	"context"
	"encoding/gob"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"huginn/internal/git"
	"huginn/internal/gittest"
)

// dump renders every event of h in ID order, enough to tell two histories
// apart.
func dump(h *History) []string {
	var out []string
	for s := range h.All() {
		for _, e := range s.Events {
			out = append(out, fmt.Sprintf("%d %.7s %s %s %s %s:%d %s",
				s.ID, e.SHA, e.Time.UTC().Format("01-02"), e.Author, e.Flags,
				s.Latest().Path, s.Latest().StartLine, e.Summary()))
		}
	}
	return out
}

func build(t *testing.T, r *gittest.Repo, opts BuildOptions) *History {
	t.Helper()
	repo, err := git.Open(r.Dir)
	if err != nil {
		t.Fatal(err)
	}
	h, err := Build(context.Background(), repo, opts)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestCache(t *testing.T) {
	r := gittest.New(t)
	r.Commit("root", map[string]string{
		"geo/point.go":    "package geo\n\ntype Point struct{ X, Y float64 }\n\nfunc (p Point) Add(q Point) Point { return Point{p.X + q.X, p.Y + q.Y} }\n",
		"geo/geo_test.go": "package geo\n\nfunc TestAdd() {}\n",
	})
	r.Commit("rename", map[string]string{
		"geo/point.go": "package geo\n\ntype Point struct{ X, Y float64 }\n\nfunc (p Point) Plus(q Point) Point { return Point{p.X + q.X, p.Y + q.Y} }\n",
	})
	r.Git("checkout", "-q", "-b", "side")
	r.Commit("side", map[string]string{"geo/side.go": "package geo\n\nfunc Side() {}\n"})
	r.Git("checkout", "-q", "master")
	r.Commit("scale", map[string]string{
		"geo/scale.go": "package geo\n\nfunc Scale(p Point, k float64) Point { return Point{p.X * k, p.Y * k} }\n",
	})
	r.Days++
	r.Git("merge", "-q", "--no-ff", "-m", "merge side", "side")

	dir := t.TempDir()
	cache := NewCache(dir)
	want := dump(build(t, r, BuildOptions{}))

	if got := dump(build(t, r, BuildOptions{Cache: cache})); !slices.Equal(got, want) {
		t.Fatalf("cold build = %q\nwant %q", got, want)
	}
	if cache.Hits != 0 || cache.Walked != 4 {
		t.Errorf("cold build: hits %d, walked %d, want 0, 4", cache.Hits, cache.Walked)
	}

	// Tests are filtered after the cache, so it serves either setting.
	withTests := dump(build(t, r, BuildOptions{Tests: true}))
	if got := dump(build(t, r, BuildOptions{Cache: cache, Tests: true})); !slices.Equal(got, withTests) {
		t.Fatalf("warm build with tests = %q\nwant %q", got, withTests)
	}
	if cache.Hits != 4 || cache.Walked != 0 {
		t.Errorf("warm build: hits %d, walked %d, want 4, 0", cache.Hits, cache.Walked)
	}

	// New commits are walked; the rest still come from the cache.
	r.Commit("delete scale", map[string]string{"geo/scale.go": ""})
	r.Commit("edit", map[string]string{
		"geo/point.go": "package geo\n\ntype Point struct{ X, Y float64 }\n\nfunc (p Point) Plus(q Point) Point {\n\tp.X += q.X\n\tp.Y += q.Y\n\treturn p\n}\n",
	})
	want = dump(build(t, r, BuildOptions{}))
	if got := dump(build(t, r, BuildOptions{Cache: cache})); !slices.Equal(got, want) {
		t.Fatalf("incremental build = %q\nwant %q", got, want)
	}
	if cache.Hits != 4 || cache.Walked != 2 {
		t.Errorf("incremental build: hits %d, walked %d, want 4, 2", cache.Hits, cache.Walked)
	}

	// A range inside the cached history needs no walk.
	ranged := BuildOptions{Options: git.Options{Since: r.Date(3)}}
	want = dump(build(t, r, ranged))
	ranged.Cache = cache
	if got := dump(build(t, r, ranged)); !slices.Equal(got, want) || cache.Walked != 0 {
		t.Fatalf("ranged build walked %d: %q\nwant %q", cache.Walked, got, want)
	}

	// Generated code is cached separately.
	build(t, r, BuildOptions{Options: git.Options{IncludeGenerated: true}, Cache: cache})
	if cache.Walked != 6 {
		t.Errorf("IncludeGenerated build walked %d, want 6", cache.Walked)
	}
}

func TestCacheDiscarded(t *testing.T) {
	r := gittest.New(t)
	r.Commit("root", map[string]string{"a.go": "package a\n\nfunc A() {}\n"})
	r.Commit("edit", map[string]string{"a.go": "package a\n\nfunc A() { println() }\n"})
	want := dump(build(t, r, BuildOptions{}))

	for name, corrupt := range map[string]func(path string) error{
		"version": func(path string) error {
			f, err := os.Create(path)
			if err != nil {
				return err
			}
			defer f.Close()
			return gob.NewEncoder(f).Encode(cacheHeader{Version: cacheVersion + 1})
		},
		"truncated": func(path string) error {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			return os.WriteFile(path, b[:len(b)/2], 0o644)
		},
		"garbage": func(path string) error { return os.WriteFile(path, []byte("not a cache"), 0o644) },
	} {
		t.Run(name, func(t *testing.T) {
			cache := NewCache(filepath.Join(t.TempDir(), "huginn"))
			build(t, r, BuildOptions{Cache: cache})
			if err := corrupt(cache.file(false)); err != nil {
				t.Fatal(err)
			}
			if got := dump(build(t, r, BuildOptions{Cache: cache})); !slices.Equal(got, want) {
				t.Fatalf("build = %q\nwant %q", got, want)
			}
			if cache.Hits != 0 || cache.Walked != 2 {
				t.Errorf("hits %d, walked %d, want 0, 2", cache.Hits, cache.Walked)
			}
			// The rewritten cache is good again.
			build(t, r, BuildOptions{Cache: cache})
			if cache.Hits != 2 {
				t.Errorf("after rewrite: hits %d, want 2", cache.Hits)
			}
		})
	}
}
