package web

import (
	"container/list"
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"huginn/internal/ast"
	"huginn/internal/git"
	"huginn/internal/semdiff"
)

// reportCacheSize is how many commit reports are kept, most recently used.
const reportCacheSize = 64

// commitJSON is the semantic diff report of one commit, as the commit screen
// draws it.
type commitJSON struct {
	SHA     string   `json:"sha"`
	Parents []string `json:"parents"`
	Author  string   `json:"author"`
	Email   string   `json:"email"`
	T       int64    `json:"t"` // author time, unix seconds
	Subject string   `json:"subject"`
	Message string   `json:"message"`

	// Added and Removed count text lines over the Go files touched, which is
	// what the semantic changes are weighed against. TotalAdded and
	// TotalRemoved count every file.
	Added        int    `json:"added"`
	Removed      int    `json:"removed"`
	TotalAdded   int    `json:"totalAdded"`
	TotalRemoved int    `json:"totalRemoved"`
	Symbols      int    `json:"symbols"`
	Semantic     int    `json:"semanticChanges"`
	Headline     string `json:"headline"`

	Counts   map[string]int    `json:"counts"` // by kind key; kinds without changes are left out
	Files    []commitFileJSON  `json:"files"`
	Changes  []changeJSON      `json:"changes"`
	Blast    blastJSON         `json:"blast"`
	Noise    []noiseJSON       `json:"noise"`
	Warnings []string          `json:"warnings,omitempty"`
	Nav      *commitNavigation `json:"nav"`
}

// commitNavigation links a commit to its neighbors on the indexed line of
// history. Before indexing is done, or for a commit off that line, Prev falls
// back to the first parent and Next is unknown.
type commitNavigation struct {
	Prev    string `json:"prev,omitempty"`
	Next    string `json:"next,omitempty"`
	Indexed bool   `json:"indexed"`
}

type commitFileJSON struct {
	Path    string   `json:"path"`
	OldPath string   `json:"oldPath,omitempty"`
	Kinds   []string `json:"kinds"`
	Changes int      `json:"changes"`
	Added   int      `json:"added"`
	Removed int      `json:"removed"`
}

type changeJSON struct {
	Kind    string   `json:"kind"`  // the card's label
	Kinds   []string `json:"kinds"` // every kind the change carries
	Flags   []string `json:"flags"`
	Package string   `json:"package"`
	Name    string   `json:"name"`
	OldName string   `json:"oldName,omitempty"`
	Symbol  string   `json:"symbol"` // "func", "method", "type", ...

	Path    string `json:"path"`
	Line    int    `json:"line"`
	OldPath string `json:"oldPath,omitempty"`
	OldLine int    `json:"oldLine,omitempty"`

	OldSignature string `json:"oldSignature,omitempty"`
	NewSignature string `json:"newSignature,omitempty"`

	StmtsBefore      int `json:"stmtsBefore"`
	StmtsAfter       int `json:"stmtsAfter"`
	StmtsDelta       int `json:"stmtsDelta"`
	ComplexityBefore int `json:"complexityBefore"`
	ComplexityAfter  int `json:"complexityAfter"`
	ComplexityDelta  int `json:"complexityDelta"`

	Summary     string       `json:"summary"`
	Impact      string       `json:"impact"`
	Exported    bool         `json:"exported"`
	APIChange   bool         `json:"apiChange"`
	CallSites   int          `json:"callSites"`
	Approximate bool         `json:"approximate"`
	Similarity  float64      `json:"similarity"`
	Callers     []callerJSON `json:"callers"`
	Members     []memberJSON `json:"members,omitempty"`
}

// memberJSON is one changed field, method, embedded type or type term of a
// struct or interface type.
type memberJSON struct {
	Flags       string `json:"flags"` // "added", "retyped|tag", ...
	Member      string `json:"member"`
	Name        string `json:"name,omitempty"`
	OldName     string `json:"oldName,omitempty"`
	OldDecl     string `json:"oldDecl,omitempty"`
	NewDecl     string `json:"newDecl,omitempty"`
	Exported    bool   `json:"exported"`
	Breaks      string `json:"breaks,omitempty"` // whose code the change breaks
	Uses        int    `json:"uses"`
	Approximate bool   `json:"approximate"`

	Implementers []callerJSON `json:"implementers,omitempty"` // approximate, see semdiff.MemberChange
	Unkeyed      int          `json:"unkeyed,omitempty"`      // unkeyed literals the change breaks
}

func membersJSON(ms []semdiff.MemberChange) []memberJSON {
	var out []memberJSON
	for _, m := range ms {
		out = append(out, memberJSON{
			Flags: m.Flags.String(), Member: m.Member, Name: m.Name, OldName: m.OldName,
			OldDecl: m.OldDecl, NewDecl: m.NewDecl, Exported: m.Exported, Breaks: m.Breaks,
			Uses: m.Uses, Approximate: m.Approximate, Implementers: callersJSON(m.Implementers),
			Unkeyed: m.Unkeyed,
		})
	}
	return out
}

type callerJSON struct {
	Package     string `json:"package"`
	Name        string `json:"name"`
	Path        string `json:"path"`
	Line        int    `json:"line"`
	Calls       int    `json:"calls"`
	Approximate bool   `json:"approximate"`
	Test        bool   `json:"test"`
}

type blastJSON struct {
	CallSites          int          `json:"callSites"`
	Packages           []string     `json:"packages"`
	Callers            []callerJSON `json:"callers"`
	APIChanges         []string     `json:"apiChanges"`
	ExportedAPIChanged bool         `json:"exportedApiChanged"`
	Approximate        bool         `json:"approximate"`
}

type noiseJSON struct {
	Path    string `json:"path"`
	Reason  string `json:"reason"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
}

// kindKey names a kind as the UI does in CSS classes and filter chips.
func kindKey(k semdiff.Kind) string {
	if k == semdiff.KindBody {
		return "body"
	}
	return k.String()
}

func kindKeys(ks []semdiff.Kind) []string {
	out := make([]string, len(ks))
	for i, k := range ks {
		out[i] = kindKey(k)
	}
	return out
}

// flagNames lists the change flags set in f, in the order symbol events use.
func flagNames(f ast.ChangeFlags) []string {
	out := []string{}
	for _, fl := range []ast.ChangeFlags{ast.Added, ast.Deleted, ast.Renamed, ast.Moved, ast.SignatureChanged, ast.BodyChanged} {
		if f.Has(fl) {
			out = append(out, fl.String())
		}
	}
	return out
}

func callersJSON(cs []semdiff.Caller) []callerJSON {
	out := make([]callerJSON, len(cs))
	for i, c := range cs {
		out[i] = callerJSON{
			Package: c.Package, Name: c.Name, Path: c.Path, Line: c.Line,
			Calls: c.Calls, Approximate: c.Approximate, Test: c.Test,
		}
	}
	return out
}

// newCommitJSON converts a report. Nav is left for the handler to fill in,
// since it depends on the index rather than the commit.
func newCommitJSON(c *git.Commit, r *semdiff.Report, warnings []string) *commitJSON {
	subject, _, _ := strings.Cut(strings.TrimSpace(c.Message), "\n")
	out := &commitJSON{
		SHA: c.Hash, Parents: c.Parents, Author: c.Author, Email: c.Email, T: c.AuthorTime.Unix(),
		Subject: subject, Message: strings.TrimSpace(c.Message),
		Added: r.Added, Removed: r.Removed, TotalAdded: c.Added, TotalRemoved: c.Removed,
		Symbols: r.Symbols, Semantic: r.SemanticChanges, Headline: r.Headline(),
		Counts: map[string]int{}, Files: []commitFileJSON{}, Changes: []changeJSON{}, Noise: []noiseJSON{},
		Warnings: warnings,
	}
	if out.Parents == nil {
		out.Parents = []string{}
	}
	for k, n := range r.Counts {
		if n > 0 {
			out.Counts[kindKey(k)] = n
		}
	}
	for _, f := range r.Files {
		out.Files = append(out.Files, commitFileJSON{
			Path: f.Path, OldPath: f.OldPath, Kinds: kindKeys(f.Kinds),
			Changes: f.Changes, Added: f.Added, Removed: f.Removed,
		})
	}
	for _, ch := range r.Changes {
		out.Changes = append(out.Changes, changeJSON{
			Kind: kindKey(ch.Kind), Kinds: kindKeys(ch.Kinds), Flags: flagNames(ch.Flags),
			Package: ch.Package, Name: ch.Name, OldName: ch.OldName, Symbol: ch.Symbol,
			Path: ch.Path, Line: ch.Line, OldPath: ch.OldPath, OldLine: ch.OldLine,
			OldSignature: ch.OldSignature, NewSignature: ch.NewSignature,
			StmtsBefore: ch.StmtsBefore, StmtsAfter: ch.StmtsAfter, StmtsDelta: ch.StmtsAfter - ch.StmtsBefore,
			ComplexityBefore: ch.ComplexityBefore, ComplexityAfter: ch.ComplexityAfter, ComplexityDelta: ch.ComplexityAfter - ch.ComplexityBefore,
			Summary: ch.Summary, Impact: ch.Impact, Exported: ch.Exported, APIChange: ch.APIChange,
			CallSites: ch.CallSites, Approximate: ch.Approximate, Similarity: ch.Similarity,
			Callers: callersJSON(ch.Callers), Members: membersJSON(ch.Members),
		})
	}
	b := r.Blast
	out.Blast = blastJSON{
		CallSites: b.CallSites, Packages: b.Packages, Callers: callersJSON(b.Callers),
		APIChanges: b.APIChanges, ExportedAPIChanged: b.ExportedAPIChanged(), Approximate: b.Approximate,
	}
	if out.Blast.Packages == nil {
		out.Blast.Packages = []string{}
	}
	if out.Blast.APIChanges == nil {
		out.Blast.APIChanges = []string{}
	}
	for _, n := range r.Noise {
		out.Noise = append(out.Noise, noiseJSON{Path: n.Path, Reason: n.Reason, Added: n.Added, Removed: n.Removed})
	}
	return out
}

// buildReport computes the report of the commit with the given full hash.
func (s *Server) buildReport(ctx context.Context, sha string) (*commitJSON, error) {
	gen := s.opts.IncludeGenerated
	c, files, err := s.repo.Diff(ctx, sha, git.Options{IncludeGenerated: gen})
	if err != nil {
		return nil, err
	}
	tree, err := s.repo.Tree(ctx, c.Hash)
	if err != nil {
		return nil, err
	}
	in := semdiff.Input{Tree: tree.Files, GoMod: tree.GoMod, IncludeGenerated: gen}
	for _, fd := range files {
		in.Files = append(in.Files, semdiff.FileDiff{Old: fd.Old, New: fd.New, Added: fd.Added, Removed: fd.Removed})
	}
	var warnings []string
	for _, err := range c.ParseErrors {
		warnings = append(warnings, err.Error())
	}
	if n := len(tree.ParseErrors); n > 0 {
		warnings = append(warnings, fmt.Sprintf("%d files in the tree have syntax errors; callers may be missing", n))
	}
	return newCommitJSON(c, semdiff.Build(in), warnings), nil
}

// reportCache keeps the most recently used reports by commit hash. Requests
// for a report being built wait for it instead of building it again.
type reportCache struct {
	mu      sync.Mutex
	size    int
	entries map[string]*list.Element // of *reportEntry
	lru     list.List                // most recent first
}

type reportEntry struct {
	sha   string
	ready chan struct{} // closed once report or err is set
	rep   *commitJSON
	err   error
}

func newReportCache(size int) *reportCache {
	return &reportCache{size: size, entries: map[string]*list.Element{}}
}

// get returns the report for sha, building it with build on a miss. Failed
// builds are not cached.
func (c *reportCache) get(ctx context.Context, sha string, build func(context.Context, string) (*commitJSON, error)) (*commitJSON, error) {
	c.mu.Lock()
	if el, ok := c.entries[sha]; ok {
		c.lru.MoveToFront(el)
		e := el.Value.(*reportEntry)
		c.mu.Unlock()
		select {
		case <-e.ready:
			return e.rep, e.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	e := &reportEntry{sha: sha, ready: make(chan struct{})}
	c.entries[sha] = c.lru.PushFront(e)
	for c.lru.Len() > c.size {
		old := c.lru.Back()
		c.lru.Remove(old)
		delete(c.entries, old.Value.(*reportEntry).sha)
	}
	c.mu.Unlock()

	// Build detached from the request, so that a client going away does not
	// fail the report for the others waiting on it.
	e.rep, e.err = build(context.WithoutCancel(ctx), sha)
	close(e.ready)
	if e.err != nil {
		c.mu.Lock()
		if el, ok := c.entries[sha]; ok && el.Value == e {
			c.lru.Remove(el)
			delete(c.entries, sha)
		}
		c.mu.Unlock()
	}
	return e.rep, e.err
}

// commit serves the semantic diff report of one commit. It does not need the
// index, so it works while indexing is still running.
func (s *Server) commit(w http.ResponseWriter, r *http.Request) {
	sha, err := s.repo.ResolveCommit(r.Context(), r.PathValue("rev"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	rep, err := s.reports.get(r.Context(), sha, s.build)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := *rep
	out.Nav = &commitNavigation{}
	if ix := s.index.Load(); ix != nil {
		out.Nav.Prev, out.Nav.Next, out.Nav.Indexed = ix.h.Neighbors(sha)
	}
	if !out.Nav.Indexed && len(rep.Parents) > 0 {
		out.Nav.Prev = rep.Parents[0]
	}
	writeJSON(w, out)
}
