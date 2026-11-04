// Package web serves huginn's browser UI and the JSON API behind it.
//
// The history is indexed in memory once, in the background; until it is
// ready the API answers /api/status, which reports indexing progress, and
// /api/commit, whose reports are computed on demand. Once it is indexed, the
// responses the pages ask for are built before the index is published, so
// the pages draw from memory; see prebuilt.go.
package web

import (
	"cmp"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"huginn/internal/ast"
	"huginn/internal/codemap"
	"huginn/internal/git"
	"huginn/internal/health"
	"huginn/internal/lineage"
	"huginn/internal/silos"
)

//go:embed static
var static embed.FS

// Server indexes one repository and serves it.
type Server struct {
	repo  *git.Repo
	opts  lineage.BuildOptions
	name  string
	index atomic.Pointer[index]
	done  atomic.Int64 // commits applied so far while indexing
	err   atomic.Pointer[error]

	settled   chan struct{} // closed once the index is published or failed
	settle    sync.Once
	ui        fs.FS
	uiEncoded map[string]*encoded // the static files, compressed

	reports *reportCache                                       // commit reports by hash
	build   func(context.Context, string) (*commitJSON, error) // buildReport, or a test's stand-in
}

// index is the read-only view the API serves once indexing is done.
type index struct {
	h      *lineage.History
	cmap   *codemap.Map
	health *health.Report
	silos  *silos.Report
	took   time.Duration

	journeys journeys // built with the prebuilt responses, or on first request
	pre      sync.Map // request key → *encoded, see prebuilt.go
}

// New returns a server for repo. name labels it in the UI.
func New(repo *git.Repo, name string, opts lineage.BuildOptions) *Server {
	s := &Server{repo: repo, name: name, opts: opts, reports: newReportCache(reportCacheSize), settled: make(chan struct{})}
	s.build = s.buildReport
	s.ui, _ = fs.Sub(static, "static")
	s.uiEncoded = staticFiles(s.ui)
	return s
}

// Index builds the history and the responses the pages ask for, then
// publishes them and builds the per-directory ones. With a cache, the
// responses are saved there for the next Index of the same history. It
// blocks until all are done; run it in a goroutine to serve progress
// meanwhile.
func (s *Server) Index(ctx context.Context) error {
	defer s.settle.Do(func() { close(s.settled) })
	start := time.Now()
	opts := s.opts
	opts.Progress = func(n int) { s.done.Store(int64(n)) }
	h, err := lineage.Build(ctx, s.repo, opts)
	if err != nil {
		s.err.Store(&err)
		s.settle.Do(func() { close(s.settled) })
		return err
	}
	ix := &index{h: h, cmap: codemap.Build(h), health: health.Build(h, health.Options{}), silos: silos.Build(h, silos.Options{})}
	// The responses saved for this history are loaded rather than built
	// again. They were saved once all were built, the detail ones too.
	// Without a key they are neither loaded nor saved.
	key, _ := s.historyKey(ctx)
	saved := s.loadSaved(key)
	for k, e := range saved {
		ix.pre.Store(k, e)
	}
	s.prebuild(ctx, ix, s.pageURLs(ix))
	ix.took = time.Since(start)
	s.index.Store(ix)
	s.settle.Do(func() { close(s.settled) })
	if len(saved) > 0 {
		return nil
	}
	s.prebuild(ctx, ix, s.detailURLs(ix))
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := s.save(key, ix); err != nil {
		return fmt.Errorf("save responses: %w", err)
	}
	return nil
}

// Handler returns the UI and API routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /", s.file(""))
	mux.Handle("GET /{$}", s.file("journey.html"))
	mux.Handle("GET /journey", s.file("journey.html"))
	mux.Handle("GET /map", s.file("index.html"))
	mux.Handle("GET /health", s.file("health.html"))
	mux.HandleFunc("GET /api/status", s.status)
	s.indexRoutes(mux, s.index.Load)
	mux.HandleFunc("GET /api/commit/{rev...}", s.commit)
	return mux
}

// indexRoutes adds the routes served from the index that load returns.
func (s *Server) indexRoutes(mux *http.ServeMux, load func() *index) {
	ready := func(fn func(*index, http.ResponseWriter, *http.Request)) http.HandlerFunc {
		return ready(load, fn)
	}
	mux.HandleFunc("GET /api/symbol/{id}", ready(s.symbol))
	mux.HandleFunc("GET /api/map", ready(prebuilt(s.codeMap)))
	mux.HandleFunc("GET /api/map/{dir...}", ready(prebuilt(s.mapDir)))
	mux.HandleFunc("GET /api/health", ready(prebuilt(s.health)))
	mux.HandleFunc("GET /api/silos", ready(prebuilt(s.silosAPI)))
	mux.HandleFunc("GET /api/silos/leave", ready(s.silosLeave))
	mux.HandleFunc("GET /api/journey", ready(prebuilt(s.journeyAPI)))
	mux.HandleFunc("GET /api/journey/package/{dir...}", ready(prebuilt(s.journeyPackage)))
	mux.HandleFunc("GET /api/journey/links/{dir...}", ready(s.journeyLinks))
	mux.HandleFunc("GET /api/journey/symbols", ready(s.journeySymbols))
}

// file serves the static file name, or the one the request names when name
// is empty, from its compressed copy.
func (s *Server) file(name string) http.Handler {
	files := http.FileServerFS(s.ui)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := name
		if p == "" {
			p = strings.TrimPrefix(r.URL.Path, "/")
			if p == "" || strings.HasSuffix(p, "/") {
				p += "index.html"
			}
		}
		if e := s.uiEncoded[p]; e != nil {
			e.serve(w, r)
			return
		}
		files.ServeHTTP(w, r)
	})
}

func ready(load func() *index, fn func(*index, http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ix := load()
		if ix == nil {
			http.Error(w, "still indexing", http.StatusServiceUnavailable)
			return
		}
		fn(ix, w, r)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

type statusJSON struct {
	Repo    string `json:"repo"`
	Ready   bool   `json:"ready"`
	Commits int64  `json:"commits"`
	Error   string `json:"error,omitempty"`
	Live    int    `json:"live,omitempty"`
	Symbols int    `json:"symbols,omitempty"`
	From    int64  `json:"from,omitempty"` // unix seconds of the first commit
	To      int64  `json:"to,omitempty"`
	TookMS  int64  `json:"tookMs,omitempty"`
}

// statusWait is how long /api/status?wait=1 holds a request while
// indexing, so that pages learn the index is ready as soon as it is.
const statusWait = 2 * time.Second

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("wait") == "1" {
		select {
		case <-s.settled:
		case <-time.After(statusWait):
		case <-r.Context().Done():
			return
		}
	}
	st := statusJSON{Repo: s.name, Commits: s.done.Load()}
	if err := s.err.Load(); err != nil {
		st.Error = (*err).Error()
	}
	if ix := s.index.Load(); ix != nil {
		first, last := ix.h.Span()
		st.Ready, st.Live, st.From, st.To = true, ix.h.Live(), first.Unix(), last.Unix()
		st.Commits, st.TookMS = int64(ix.h.Revisions()), ix.took.Milliseconds()
		for range ix.h.All() {
			st.Symbols++
		}
	}
	writeJSON(w, st)
}

type symbolJSON struct {
	ID        lineage.ID  `json:"id"`
	Name      string      `json:"name"`
	Kind      string      `json:"kind"`
	Path      string      `json:"path"`
	Start     int         `json:"start"`
	End       int         `json:"end"`
	Signature string      `json:"signature"`
	Alive     bool        `json:"alive"`
	Authors   int         `json:"authors"`
	Events    []eventJSON `json:"events"` // oldest first
}

type eventJSON struct {
	SHA        string   `json:"sha"`
	T          int64    `json:"t"`
	Author     string   `json:"author"`
	Kind       string   `json:"kind"`
	Flags      []string `json:"flags"`
	Summary    string   `json:"summary"`
	Stmts      int      `json:"stmts"` // after the event; 0 once deleted
	Complexity int      `json:"complexity"`

	// Rev is the revision's index, oldest first. Path, Name and Signature
	// are the declaration after the event; before it for a deletion.
	// OldPath and OldName are the declaration before it, unless added.
	Rev       int    `json:"rev"`
	Path      string `json:"path"`
	Name      string `json:"name"`
	Signature string `json:"signature"`
	OldPath   string `json:"oldPath,omitempty"`
	OldName   string `json:"oldName,omitempty"`
}

func (s *Server) symbol(ix *index, w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	sym := ix.h.Symbol(lineage.ID(id))
	if sym == nil {
		http.Error(w, "no such symbol", http.StatusNotFound)
		return
	}
	cur := sym.Latest()
	out := symbolJSON{
		ID: sym.ID, Name: cur.QualifiedName(), Kind: cur.Kind.String(), Path: cur.Path,
		Start: cur.StartLine, End: cur.EndLine, Signature: cur.Signature, Alive: sym.Alive(),
	}
	authors := map[string]bool{}
	for _, e := range sym.Events {
		authors[e.Author] = true
		ej := eventJSON{SHA: e.SHA, T: e.Time.Unix(), Author: e.Author, Kind: e.Primary().String(), Summary: e.Summary()}
		for _, f := range []ast.ChangeFlags{ast.Added, ast.Deleted, ast.Renamed, ast.Moved, ast.SignatureChanged, ast.BodyChanged} {
			if e.Flags.Has(f) {
				ej.Flags = append(ej.Flags, f.String())
			}
		}
		if e.New != nil {
			ej.Stmts, ej.Complexity = e.New.Stmts, e.New.Complexity
		}
		ej.Rev, _ = ix.h.Position(e.SHA)
		decl := cmp.Or(e.New, e.Old)
		ej.Path, ej.Name, ej.Signature = decl.Path, decl.QualifiedName(), decl.Signature
		if e.Old != nil {
			ej.OldPath, ej.OldName = e.Old.Path, e.Old.QualifiedName()
		}
		out.Events = append(out.Events, ej)
	}
	out.Authors = len(authors)
	writeJSON(w, out)
}
