package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"huginn/internal/git"
	"huginn/internal/gittest"
	"huginn/internal/lineage"
)

func TestSavedResponses(t *testing.T) {
	r := gittest.New(t)
	r.Commit("root", map[string]string{"geo/point.go": "package geo\n\nfunc Move() {}\n"})
	repo, err := git.Open(r.Dir)
	if err != nil {
		t.Fatal(err)
	}
	cache := lineage.NewCache(t.TempDir())
	get := func(srv *Server, url, encoding string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, url, nil)
		req.Header.Set("Accept-Encoding", encoding)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}
	serve := func() (*Server, string) {
		t.Helper()
		srv := New(repo, "demo", lineage.BuildOptions{Cache: cache})
		if err := srv.Index(context.Background()); err != nil {
			t.Fatal(err)
		}
		return srv, get(srv, "/api/dirs", "").Body.String()
	}

	// Mark the saved /api/dirs, so that serving it shows it was loaded.
	const marked = `["saved"]`
	mark := func(srv *Server) {
		t.Helper()
		key, err := srv.historyKey(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		saved := srv.loadSaved(key)
		if len(saved) == 0 {
			t.Fatal("nothing saved")
		}
		ix := &index{}
		for k, e := range saved {
			ix.pre.Store(k, e)
		}
		ix.pre.Store("/api/dirs", encode("application/json", []byte(marked)))
		if err := srv.save(key, ix); err != nil {
			t.Fatal(err)
		}
	}

	srv, fresh := serve()
	if fresh == marked {
		t.Fatal("first serve answered from a saved file")
	}
	const journey = "/api/journey?step=commit"
	want := get(srv, journey, "").Body.String()
	mark(srv)
	srv, got := serve()
	if got != marked {
		t.Errorf("same history: /api/dirs = %s, want the saved %s", got, marked)
	}
	// Only the gzipped copy is saved; it is inflated for clients without gzip.
	if rec := get(srv, journey, "gzip"); rec.Header().Get("Content-Encoding") != "gzip" {
		t.Errorf("saved %s not served gzipped", journey)
	}
	if got := get(srv, journey, "").Body.String(); got != want {
		t.Errorf("saved %s inflated to %.60q, want %.60q", journey, got, want)
	}

	// A new commit is a new history.
	r.Commit("add Stop", map[string]string{"geo/point.go": "package geo\n\nfunc Move() {}\n\nfunc Stop() {}\n"})
	srv, got = serve()
	if got == marked {
		t.Error("new commit: served the saved response of the old history")
	}
	mark(srv)

	// So is a new tag, which the story reads.
	r.Git("tag", "v1.0.0")
	if _, got := serve(); got == marked {
		t.Error("new tag: served the saved response of the old history")
	}

	// Without a cache nothing is saved or loaded.
	if f := New(repo, "demo", lineage.BuildOptions{}).savedFile(); f != "" {
		t.Errorf("savedFile without a cache = %q", f)
	}
}
