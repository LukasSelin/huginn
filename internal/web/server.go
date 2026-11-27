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
	"huginn/internal/review"
	"huginn/internal/silos"
)

//go:embed static
var static embed.FS

// Server indexes one repository and serves it.
type Server struct {
	repo     *git.Repo
	opts     lineage.BuildOptions
	name     string
	index    atomic.Pointer[index]
	progress *progress
	ui       assets // the pages and their scripts

	api   *repoAPI     // commit reports and reviews, which need no index
	saved responseFile // where the prebuilt responses are kept between runs
}

// progress is how far indexing has got, as /api/status reports it.
type progress struct {
	commits atomic.Int64 // applied so far
	err     atomic.Pointer[error]
	settled chan struct{} // closed once the index is published or indexing failed
	once    sync.Once
}

func newProgress() *progress { return &progress{settled: make(chan struct{})} }

// settle marks indexing as over, failed with err unless it is nil. Only
// the first call counts.
func (p *progress) settle(err error) {
	p.once.Do(func() {
		if err != nil {
			p.err.Store(&err)
		}
		close(p.settled)
	})
}

// index is the read-only view the API serves once indexing is done, with
// the routes that serve it; see indexRoutes.
type index struct {
	h      *lineage.History
	cmap   *codemap.Map
	health *health.Report
	silos  *silos.Report
	took   time.Duration
	tests  bool // the history keeps the symbols of test files

	// tags reads the tags the story marks its releases with.
	tags func(context.Context) ([]git.Tag, error)

	journeys journeys      // built with the prebuilt responses, or on first request
	affinity affinityState // reports made on request
	pre      responses     // see prebuilt.go
}

// New returns a server for repo. name labels it in the UI.
func New(repo *git.Repo, name string, opts lineage.BuildOptions) *Server {
	return &Server{repo: repo, name: name, opts: opts, progress: newProgress(), ui: newAssets(),
		api: newRepoAPI(repo, opts.IncludeGenerated), saved: savedFile(opts.Cache)}
}

// SetTracer has the review page trace with t what each merge request
// reaches outside the process. Call it before serving.
func (s *Server) SetTracer(t *review.Tracer) { s.api.tracer = t }

// Index builds the history and the responses the pages ask for, then
// publishes them and builds the per-directory ones. With a cache, the
// responses are saved there for the next Index of the same history. It
// blocks until all are done; run it in a goroutine to serve progress
// meanwhile.
func (s *Server) Index(ctx context.Context) error {
	defer s.progress.settle(nil)
	start := time.Now()
	opts := s.opts
	opts.Progress = func(n int) { s.progress.commits.Store(int64(n)) }
	h, err := lineage.Build(ctx, s.repo, opts)
	if err != nil {
		s.progress.settle(err)
		return err
	}
	ix := &index{h: h, cmap: codemap.Build(h), health: health.Build(h, health.Options{}), silos: silos.Build(h, silos.Options{}),
		tests: s.opts.Tests, tags: func(ctx context.Context) ([]git.Tag, error) {
			return s.repo.Tags(ctx, git.Options{Branch: s.opts.Branch})
		}}
	// The responses saved for this history are loaded rather than built
	// again. They were saved once all were built, the detail ones too.
	// Without a key they are neither loaded nor saved.
	key, _ := historyKey(ctx, s.repo, s.opts)
	saved := s.saved.load(key)
	for k, e := range saved {
		ix.pre.Store(k, e)
	}
	ix.prebuild(ctx, ix.pageURLs())
	ix.took = time.Since(start)
	s.index.Store(ix)
	s.progress.settle(nil)
	if len(saved) > 0 {
		return nil
	}
	ix.prebuild(ctx, ix.detailURLs())
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := s.saved.save(key, ix); err != nil {
		return fmt.Errorf("save responses: %w", err)
	}
	return nil
}

// Handler returns the UI and API routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /", s.ui.file(""))
	mux.Handle("GET /{$}", s.ui.file("journey.html"))
	mux.Handle("GET /journey", s.ui.file("journey.html"))
	mux.Handle("GET /map", s.ui.file("index.html"))
	mux.Handle("GET /health", s.ui.file("health.html"))
	mux.Handle("GET /layers", s.ui.file("layers.html"))
	mux.Handle("GET /review", s.ui.file("review.html"))
	mux.Handle("GET /affinity", s.ui.file("affinity.html"))
	mux.HandleFunc("GET /api/status", s.status)
	indexRoutes(mux, s.index.Load)
	mux.HandleFunc("GET /api/commit/{rev...}", s.api.commit(s.index.Load))
	mux.HandleFunc("GET /api/review", s.api.reviewAPI(s.index.Load))
	mux.HandleFunc("GET /api/review/options", s.api.reviewOptions)
	return mux
}

// indexRoutes adds the routes served from the index that load returns.
func indexRoutes(mux *http.ServeMux, load func() *index) {
	ready := func(fn func(*index, http.ResponseWriter, *http.Request)) http.HandlerFunc {
		return ready(load, fn)
	}
	mux.HandleFunc("GET /api/symbol/{id}", ready((*index).symbol))
	mux.HandleFunc("GET /api/map", ready(prebuilt((*index).codeMap)))
	mux.HandleFunc("GET /api/map/{dir...}", ready(prebuilt((*index).mapDir)))
	mux.HandleFunc("GET /api/health", ready(prebuilt((*index).healthAPI)))
	mux.HandleFunc("GET /api/silos", ready(prebuilt((*index).silosAPI)))
	mux.HandleFunc("GET /api/silos/leave", ready((*index).silosLeave))
	mux.HandleFunc("GET /api/journey", ready(prebuilt((*index).journeyAPI)))
	mux.HandleFunc("GET /api/journey/package/{dir...}", ready(prebuilt((*index).journeyPackage)))
	mux.HandleFunc("GET /api/journey/links/{dir...}", ready((*index).journeyLinks))
	mux.HandleFunc("GET /api/journey/symbols", ready((*index).journeySymbols))
	mux.HandleFunc("GET /api/layers", ready(prebuilt((*index).layersAPI)))
	mux.HandleFunc("GET /api/affinity/types", ready((*index).affinityTypes))
	mux.HandleFunc("GET /api/affinity", ready((*index).affinityAPI))
}

// assets are the UI's static files, embedded, with the compressed copies
// they are served from.
type assets struct {
	files   fs.FS
	encoded map[string]*encoded // by path under static
}

func newAssets() assets {
	files, _ := fs.Sub(static, "static")
	return assets{files: files, encoded: staticFiles(files)}
}

// file serves the static file name, or the one the request names when name
// is empty, from its compressed copy.
func (a assets) file(name string) http.Handler {
	files := http.FileServerFS(a.files)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := name
		if p == "" {
			p = strings.TrimPrefix(r.URL.Path, "/")
			if p == "" || strings.HasSuffix(p, "/") {
				p += "index.html"
			}
		}
		if e := a.encoded[p]; e != nil {
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

// writeJSON sends v as JSON. It is encoded before anything is written, so a
// value that can't be encoded gets a 500 rather than an empty 200.
func writeJSON(w http.ResponseWriter, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(append(body, '\n')) // a client that went away needs no answer
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
		case <-s.progress.settled:
		case <-time.After(statusWait):
		case <-r.Context().Done():
			return
		}
	}
	st := statusJSON{Repo: s.name, Commits: s.progress.commits.Load()}
	if err := s.progress.err.Load(); err != nil {
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

func (ix *index) symbol(w http.ResponseWriter, r *http.Request) {
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
