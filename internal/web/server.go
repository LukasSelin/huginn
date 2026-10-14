// Package web serves huginn's browser UI and the JSON API behind it.
//
// The history is indexed in memory once, in the background; until it is
// ready the API answers /api/status, which reports indexing progress, and
// /api/commit, whose reports are computed on demand.
package web

import (
	"cmp"
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"path"
	"slices"
	"strconv"
	"sync/atomic"
	"time"

	"huginn/internal/ast"
	"huginn/internal/git"
	"huginn/internal/lineage"
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

	reports *reportCache                                       // commit reports by hash
	build   func(context.Context, string) (*commitJSON, error) // buildReport, or a test's stand-in
}

// index is the read-only view the API serves once indexing is done.
type index struct {
	h     *lineage.History
	dirs  map[string][]*lineage.Symbol // by the directory of the latest declaration
	order []string                     // directories, sorted
	took  time.Duration
}

// New returns a server for repo. name labels it in the UI.
func New(repo *git.Repo, name string, opts lineage.BuildOptions) *Server {
	s := &Server{repo: repo, name: name, opts: opts, reports: newReportCache(reportCacheSize)}
	s.build = s.buildReport
	return s
}

// Index builds the history. It blocks until done; run it in a goroutine to
// serve progress meanwhile.
func (s *Server) Index(ctx context.Context) error {
	start := time.Now()
	opts := s.opts
	opts.Progress = func(n int) { s.done.Store(int64(n)) }
	h, err := lineage.Build(ctx, s.repo, opts)
	if err != nil {
		s.err.Store(&err)
		return err
	}
	ix := &index{h: h, dirs: map[string][]*lineage.Symbol{}, took: time.Since(start)}
	for sym := range h.All() {
		d := path.Dir(sym.Latest().Path)
		ix.dirs[d] = append(ix.dirs[d], sym)
	}
	for d := range ix.dirs {
		ix.order = append(ix.order, d)
	}
	slices.Sort(ix.order)
	s.index.Store(ix)
	return nil
}

// Handler returns the UI and API routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	ui, _ := fs.Sub(static, "static")
	mux.Handle("GET /", http.FileServerFS(ui))
	mux.HandleFunc("GET /api/status", s.status)
	mux.HandleFunc("GET /api/dirs", s.ready(s.dirList))
	mux.HandleFunc("GET /api/dir", s.ready(s.dir))
	mux.HandleFunc("GET /api/symbol/{id}", s.ready(s.symbol))
	mux.HandleFunc("GET /api/commit/{rev...}", s.commit)
	return mux
}

func (s *Server) ready(fn func(*index, http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ix := s.index.Load()
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

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
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

type dirJSON struct {
	Dir     string `json:"dir"`
	Live    int    `json:"live"`
	Changes int    `json:"changes"`
}

func (s *Server) dirList(ix *index, w http.ResponseWriter, r *http.Request) {
	out := []dirJSON{}
	for _, d := range ix.order {
		dj := dirJSON{Dir: d}
		for _, sym := range ix.dirs[d] {
			if sym.Alive() {
				dj.Live++
			}
			dj.Changes += len(sym.Events)
		}
		out = append(out, dj)
	}
	writeJSON(w, out)
}

// laneJSON is one symbol in a directory listing, with just enough of its
// history to draw its lane.
type laneJSON struct {
	ID      lineage.ID `json:"id"`
	Name    string     `json:"name"` // qualified, e.g. "(*Indexer).Visit"
	Kind    string     `json:"kind"`
	Recv    string     `json:"recv,omitempty"`
	File    string     `json:"file"`
	Line    int        `json:"line"`
	Alive   bool       `json:"alive"`
	Changes int        `json:"changes"`
	Marks   []markJSON `json:"marks"`
}

type markJSON struct {
	T    int64  `json:"t"`
	Kind string `json:"k"` // the event's primary flag
}

func (s *Server) dir(ix *index, w http.ResponseWriter, r *http.Request) {
	d := r.URL.Query().Get("d")
	syms, ok := ix.dirs[d]
	if !ok {
		http.Error(w, "no such directory", http.StatusNotFound)
		return
	}
	out := make([]laneJSON, 0, len(syms))
	for _, sym := range syms {
		cur := sym.Latest()
		l := laneJSON{
			ID: sym.ID, Name: cur.QualifiedName(), Kind: cur.Kind.String(), Recv: cur.Recv,
			File: path.Base(cur.Path), Line: cur.StartLine, Alive: sym.Alive(), Changes: len(sym.Events),
		}
		for _, e := range sym.Events {
			l.Marks = append(l.Marks, markJSON{T: e.Time.Unix(), Kind: e.Primary().String()})
		}
		out = append(out, l)
	}
	// Live before deleted, then by file and position.
	slices.SortFunc(out, func(a, b laneJSON) int {
		if a.Alive != b.Alive {
			if a.Alive {
				return -1
			}
			return 1
		}
		return cmp.Or(cmp.Compare(a.File, b.File), cmp.Compare(a.Line, b.Line), cmp.Compare(a.ID, b.ID))
	})
	writeJSON(w, map[string]any{"dir": d, "symbols": out})
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
		out.Events = append(out.Events, ej)
	}
	out.Authors = len(authors)
	writeJSON(w, out)
}
