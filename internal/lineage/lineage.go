// Package lineage follows symbols through a repository's history. It gives
// each declaration a stable ID when it first appears and carries that ID over
// renames, moves and edits, so a symbol's whole life can be read as one lane.
//
// The package does not read git itself: callers feed it revisions oldest
// first along one line of history, each with the semantic changes the commit
// made against its first parent.
package lineage

import (
	"fmt"
	"iter"
	"slices"
	"strings"
	"time"

	"huginn/internal/ast"
)

// ID identifies one symbol across its whole history. IDs are handed out from
// 1 in order of first appearance.
type ID int64

// Revision is one commit's semantic effect on the source.
type Revision struct {
	SHA     string
	Time    time.Time
	Author  string
	Changes []ast.Change
}

// Event is one semantic change to a symbol in one revision. Old is nil when
// the symbol was added, New when it was deleted. The symbols are shared with
// the caller and must not be modified.
type Event struct {
	SHA    string
	Time   time.Time
	Author string
	Flags  ast.ChangeFlags
	Old    *ast.Symbol
	New    *ast.Symbol
}

// Primary returns the one flag that best names the event when only one can
// be shown, e.g. as a marker on a timeline.
func (e Event) Primary() ast.ChangeFlags {
	for _, f := range []ast.ChangeFlags{ast.Deleted, ast.Added, ast.SignatureChanged, ast.Renamed, ast.Moved, ast.BodyChanged} {
		if e.Flags.Has(f) {
			return f
		}
	}
	return 0
}

// Summary describes what the event changed in one line, e.g.
// "Walk → Visit · walk.go → indexer.go".
func (e Event) Summary() string {
	switch {
	case e.Old == nil:
		return e.New.Signature
	case e.New == nil:
		return "deleted from " + e.Old.Path
	}
	var parts []string
	if e.Flags.Has(ast.Renamed) {
		parts = append(parts, e.Old.QualifiedName()+" → "+e.New.QualifiedName())
	}
	if e.Flags.Has(ast.Moved) {
		parts = append(parts, e.Old.Path+" → "+e.New.Path)
	}
	if e.Flags.Has(ast.SignatureChanged) {
		parts = append(parts, e.New.Signature)
	}
	if e.Flags.Has(ast.BodyChanged) {
		parts = append(parts, fmt.Sprintf("stmts %d → %d, cyclomatic %d → %d",
			e.Old.Stmts, e.New.Stmts, e.Old.Complexity, e.New.Complexity))
	}
	return strings.Join(parts, " · ")
}

// Symbol is the recorded life of one declaration.
type Symbol struct {
	ID     ID
	Events []Event // oldest first
}

// Latest returns the symbol as of its last event: its current declaration
// while alive, its last declaration once deleted.
func (s *Symbol) Latest() *ast.Symbol {
	e := s.Events[len(s.Events)-1]
	if e.New != nil {
		return e.New
	}
	return e.Old
}

// Alive reports whether the symbol still exists after the last revision.
func (s *Symbol) Alive() bool { return s.Events[len(s.Events)-1].New != nil }

// History is the in-memory record of every symbol seen in the revisions
// applied so far.
type History struct {
	live      map[locator]ID
	symbols   []*Symbol // by ID-1
	revisions int
	shas      []string       // applied revisions, oldest first
	pos       map[string]int // index into shas
	first     time.Time
	last      time.Time
}

// locator names a symbol's place in one revision of the tree.
type locator struct{ path, key string }

func at(s *ast.Symbol) locator { return locator{s.Path, s.Key} }

func New() *History { return &History{live: map[locator]ID{}, pos: map[string]int{}} }

// Apply records one revision. Revisions must be applied oldest first.
//
// A symbol whose old declaration is unknown gets a new ID. That happens for
// symbols added before the first applied revision, and for repeated names
// (init, _) whose "#n" key shifted in an earlier revision without a change.
func (h *History) Apply(r Revision) {
	h.revisions++
	h.pos[r.SHA] = len(h.shas)
	h.shas = append(h.shas, r.SHA)
	if h.first.IsZero() {
		h.first = r.Time
	}
	h.last = r.Time

	// Resolve every ID before unlinking anything, so that symbols trading
	// places within one revision keep their own identities.
	ids := make([]ID, len(r.Changes))
	for i, c := range r.Changes {
		if c.Old != nil {
			if id, ok := h.live[at(c.Old)]; ok {
				ids[i] = id
				continue
			}
		}
		h.symbols = append(h.symbols, &Symbol{ID: ID(len(h.symbols) + 1)})
		ids[i] = ID(len(h.symbols))
	}
	for _, c := range r.Changes {
		if c.Old != nil {
			delete(h.live, at(c.Old))
		}
	}
	for i, c := range r.Changes {
		if c.New != nil {
			h.live[at(c.New)] = ids[i]
		}
		s := h.symbols[ids[i]-1]
		s.Events = append(s.Events, Event{
			SHA: r.SHA, Time: r.Time, Author: r.Author,
			Flags: c.Flags, Old: c.Old, New: c.New,
		})
	}
}

// Symbol returns the symbol with the given ID, or nil.
func (h *History) Symbol(id ID) *Symbol {
	if id < 1 || int(id) > len(h.symbols) {
		return nil
	}
	return h.symbols[id-1]
}

// All yields every symbol ever seen, live or deleted, in ID order.
func (h *History) All() iter.Seq[*Symbol] { return slices.Values(h.symbols) }

// Lookup returns the live symbol with the given key in the file at path.
func (h *History) Lookup(path, key string) *Symbol {
	id, ok := h.live[locator{path, key}]
	if !ok {
		return nil
	}
	return h.Symbol(id)
}

// Find returns the symbols whose latest qualified name is name, e.g.
// "(*Indexer).Visit", live ones first, then by ID.
func (h *History) Find(name string) []*Symbol {
	var out []*Symbol
	for _, s := range h.symbols {
		if s.Latest().QualifiedName() == name {
			out = append(out, s)
		}
	}
	slices.SortStableFunc(out, func(a, b *Symbol) int {
		switch {
		case a.Alive() == b.Alive():
			return 0
		case a.Alive():
			return -1
		}
		return 1
	})
	return out
}

// Live returns how many symbols exist after the last revision.
func (h *History) Live() int { return len(h.live) }

// Revisions returns how many revisions were applied.
func (h *History) Revisions() int { return h.revisions }

// Neighbors returns the revisions applied just before and just after sha,
// "" at either end. ok is false when sha was never applied.
func (h *History) Neighbors(sha string) (prev, next string, ok bool) {
	i, ok := h.pos[sha]
	if !ok {
		return "", "", false
	}
	if i > 0 {
		prev = h.shas[i-1]
	}
	if i+1 < len(h.shas) {
		next = h.shas[i+1]
	}
	return prev, next, true
}

// Span returns the times of the first and last applied revisions.
func (h *History) Span() (first, last time.Time) { return h.first, h.last }
