package web

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"huginn/internal/journey"
)

// Responses that depend only on the index are encoded and gzipped once, as
// soon as indexing is done, so that the pages' requests are answered from
// memory. Requests outside that set, like a custom map window or a search,
// are computed as before. The static files are compressed once at startup.

// encoded is a response body ready to send. In saved responses only the
// smaller of body and gz is kept; the other is nil.
type encoded struct {
	body, gz []byte
	ctype    string
	etag     string
}

func encode(ctype string, body []byte) *encoded {
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	zw.Write(body)
	zw.Close()
	sum := sha256.Sum256(body)
	return &encoded{body: body, gz: buf.Bytes(), ctype: ctype, etag: `"` + hex.EncodeToString(sum[:12]) + `"`}
}

// serve writes e, gzipped when the client accepts it, or answers 304 when
// the client already has it. The client revalidates every time, since a
// restarted server may have indexed new commits.
func (e *encoded) serve(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Type", e.ctype)
	h.Set("Cache-Control", "no-cache")
	h.Set("ETag", e.etag)
	h.Add("Vary", "Accept-Encoding")
	if strings.Contains(r.Header.Get("If-None-Match"), e.etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	body := e.body
	switch {
	case e.gz != nil && (body == nil || len(e.gz) < len(body)) && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip"):
		h.Set("Content-Encoding", "gzip")
		body = e.gz
	case body == nil:
		body = e.inflate()
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	if r.Method != http.MethodHead {
		w.Write(body)
	}
}

// inflate decompresses e's gzipped copy, for when only that was kept.
func (e *encoded) inflate() []byte {
	zr, err := gzip.NewReader(bytes.NewReader(e.gz))
	if err != nil {
		return nil
	}
	body, _ := io.ReadAll(zr)
	return body
}

// requestKey names a request by its path and its sorted query, so that
// parameter order does not matter.
func requestKey(r *http.Request) string {
	q := r.URL.Query()
	if len(q) == 0 {
		return r.URL.Path
	}
	return r.URL.Path + "?" + q.Encode()
}

// prebuilt serves fn's response from the index's prebuilt ones when it has
// it, and computes it otherwise.
func prebuilt(fn func(*index, http.ResponseWriter, *http.Request)) func(*index, http.ResponseWriter, *http.Request) {
	return func(ix *index, w http.ResponseWriter, r *http.Request) {
		if e, ok := ix.pre.Load(requestKey(r)); ok {
			e.(*encoded).serve(w, r)
			return
		}
		fn(ix, w, r)
	}
}

// prebuild runs each of urls against ix and keeps the successful responses.
// It stops early when ctx is done.
func (s *Server) prebuild(ctx context.Context, ix *index, urls []string) {
	mux := http.NewServeMux()
	s.indexRoutes(mux, func() *index { return ix })
	for _, u := range urls {
		if ctx.Err() != nil {
			return
		}
		req := httptest.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if _, ok := ix.pre.Load(requestKey(req)); ok {
			continue
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code == http.StatusOK {
			ix.pre.Store(requestKey(req), encode(rec.Header().Get("Content-Type"), rec.Body.Bytes()))
		}
	}
}

// mapWindows are the map page's preset windows, as it asks for them: open
// at both ends, or counting back from just after the last commit.
var mapWindows = []time.Duration{0, 30 * 24 * time.Hour, 90 * 24 * time.Hour, 365 * 24 * time.Hour}

// mapDefaultWindow is the window the map page opens with.
const mapDefaultWindow = 90 * 24 * time.Hour

func (ix *index) mapWindow(d time.Duration, tests bool) url.Values {
	q := url.Values{}
	if d > 0 {
		_, last := ix.h.Span()
		q.Set("from", strconv.FormatInt(last.Unix()+1-int64(d/time.Second), 10))
	}
	if tests {
		q.Set("tests", "1")
	}
	return q
}

func withQuery(p string, q url.Values) string {
	if len(q) == 0 {
		return p
	}
	return p + "?" + q.Encode()
}

// escDir is a directory as the pages put it in a path: the root is empty.
func escDir(d string) string {
	if d == "." {
		return ""
	}
	parts := strings.Split(d, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

var steps = []journey.Step{journey.Commit, journey.Week, journey.Month}

// pageURLs are what the pages ask for when they open. They are built before
// the index is published, so no page waits on them.
func (s *Server) pageURLs(ix *index) []string {
	urls := []string{"/api/health"}
	for _, st := range steps {
		urls = append(urls, "/api/journey?step="+st.String())
	}
	tests := []bool{false}
	if s.opts.Tests {
		tests = append(tests, true)
	}
	for _, t := range tests {
		q := ix.mapWindow(0, t)
		q.Set("series", "1")
		urls = append(urls, withQuery("/api/map", q))
		for _, d := range mapWindows {
			urls = append(urls, withQuery("/api/map", ix.mapWindow(d, t)))
		}
	}
	return urls
}

// detailURLs are what the pages ask for when drilling into a directory or a
// package. There is one per directory and step, so they are built after the
// index is published, while the first page draws.
func (s *Server) detailURLs(ix *index) []string {
	var urls []string
	for _, st := range steps {
		for _, p := range s.journey(ix, st).Packages {
			urls = append(urls, "/api/journey/package/"+escDir(p.Path)+"?step="+st.String())
		}
	}
	for _, d := range ix.cmap.Dirs() {
		urls = append(urls, withQuery("/api/map/"+escDir(d.Path), ix.mapWindow(mapDefaultWindow, false)))
	}
	return urls
}

// staticFiles encodes every embedded UI file, by its path under static.
func staticFiles(ui fs.FS) map[string]*encoded {
	out := map[string]*encoded{}
	fs.WalkDir(ui, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, err := fs.ReadFile(ui, p)
		if err != nil {
			return err
		}
		out[p] = encode(contentType(p), body)
		return nil
	})
	return out
}

// contentType fixes the types the UI uses, which the system's MIME table
// may get wrong (Windows has served .js as text/plain).
func contentType(p string) string {
	switch path.Ext(p) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".js":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	}
	if t := mime.TypeByExtension(path.Ext(p)); t != "" {
		return t
	}
	return "application/octet-stream"
}
