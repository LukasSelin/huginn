package web

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPrebuilt(t *testing.T) {
	srv, _ := newServer(t)
	if err := srv.Index(context.Background()); err != nil {
		t.Fatal(err)
	}
	ix := srv.index.Load()
	h := srv.Handler()

	urls := append(srv.pageURLs(ix), srv.detailURLs(ix)...)
	for _, u := range urls {
		r := httptest.NewRequest(http.MethodGet, u, nil)
		e, ok := ix.pre.Load(requestKey(r))
		if !ok {
			t.Errorf("%s was not prebuilt", u)
			continue
		}
		// The prebuilt body is what the handler computes.
		live := httptest.NewRecorder()
		mux := http.NewServeMux()
		fresh := &index{h: ix.h, cmap: ix.cmap, health: ix.health}
		srv.indexRoutes(mux, func() *index { return fresh })
		mux.ServeHTTP(live, r)
		if !bytes.Equal(e.(*encoded).body, live.Body.Bytes()) {
			t.Errorf("%s: prebuilt body differs from a fresh one", u)
		}
	}

	// Parameter order does not matter.
	q := ix.mapWindow(0, false)
	q.Set("series", "1")
	if _, ok := ix.pre.Load(requestKey(httptest.NewRequest(http.MethodGet, "/api/map?series=1", nil))); !ok {
		t.Errorf("/api/map?series=1 not found; prebuilt as %s", withQuery("/api/map", q))
	}

	t.Run("gzip and revalidation", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/api/journey?step=commit", nil)
		r.Header.Set("Accept-Encoding", "gzip")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Encoding") != "gzip" {
			t.Fatalf("status %d, encoding %q; want gzip", rec.Code, rec.Header().Get("Content-Encoding"))
		}
		zr, err := gzip.NewReader(rec.Body)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(zr)
		if !strings.HasPrefix(string(body), `{"step":"commit"`) {
			t.Errorf("body starts %.40q", body)
		}

		etag := rec.Header().Get("ETag")
		r = httptest.NewRequest(http.MethodGet, "/api/journey?step=commit", nil)
		r.Header.Set("If-None-Match", etag)
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if etag == "" || rec.Code != http.StatusNotModified {
			t.Errorf("revalidating with %q: status %d, want 304", etag, rec.Code)
		}
	})

	t.Run("static", func(t *testing.T) {
		for _, p := range []string{"/", "/journey", "/common.js", "/vendor/d3.min.js"} {
			r := httptest.NewRequest(http.MethodGet, p, nil)
			r.Header.Set("Accept-Encoding", "gzip")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			if rec.Code != http.StatusOK || rec.Header().Get("Content-Encoding") != "gzip" || rec.Header().Get("ETag") == "" {
				t.Errorf("GET %s: status %d, encoding %q, etag %q", p, rec.Code, rec.Header().Get("Content-Encoding"), rec.Header().Get("ETag"))
			}
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/no-such-file.js", nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("missing file: status %d", rec.Code)
		}
	})
}

func TestStatusWait(t *testing.T) {
	srv, _ := newServer(t)
	h := srv.Handler()
	wait := func() (statusJSON, time.Duration) {
		start := time.Now()
		var st statusJSON
		getJSON(t, h, "/api/status?wait=1", http.StatusOK, &st)
		return st, time.Since(start)
	}

	// While indexing, a poll is held until indexing is done.
	got := make(chan statusJSON)
	go func() { st, _ := wait(); got <- st }()
	time.Sleep(50 * time.Millisecond)
	if err := srv.Index(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := <-got; !st.Ready {
		t.Errorf("held poll returned %+v, want ready", st)
	}

	// Once ready, it answers at once.
	if st, took := wait(); !st.Ready || took > statusWait/2 {
		t.Errorf("poll after indexing: ready %v after %v", st.Ready, took)
	}
}
