package lineage

import (
	"strings"
	"testing"

	"huginn/internal/ast"
)

// history replays revisions given as path → source maps of the touched
// files; an empty source deletes the file.
type history struct {
	t    *testing.T
	h    *History
	tree map[string]*ast.File
	n    int
}

func newHistory(t *testing.T) *history {
	return &history{t: t, h: New(), tree: map[string]*ast.File{}}
}

// commit diffs the touched files the way the git walker does for files in
// one directory, applies the revision and returns the events it recorded.
func (h *history) commit(files map[string]string) []Event {
	h.t.Helper()
	h.n++
	var old, cur []*ast.File
	for path, src := range files {
		if f := h.tree[path]; f != nil {
			old = append(old, f)
		}
		if src == "" {
			delete(h.tree, path)
			continue
		}
		f, err := ast.ParseSource(path, []byte(src))
		if err != nil {
			h.t.Fatalf("parse %s: %v", path, err)
		}
		h.tree[path] = f
		cur = append(cur, f)
	}
	sha := string(rune('a' + h.n - 1))
	h.h.Apply(Revision{SHA: sha, Changes: ast.Diff(old, cur)})

	var events []Event
	for id := ID(1); h.h.Symbol(id) != nil; id++ {
		for _, e := range h.h.Symbol(id).Events {
			if e.SHA == sha {
				events = append(events, e)
			}
		}
	}
	return events
}

func (h *history) id(path, key string) ID {
	h.t.Helper()
	s := h.h.Lookup(path, key)
	if s == nil {
		h.t.Fatalf("no live symbol %s in %s", key, path)
	}
	return s.ID
}

func (h *history) gone(path, key string) {
	h.t.Helper()
	if h.h.Lookup(path, key) != nil {
		h.t.Fatalf("symbol %s in %s still live", key, path)
	}
}

const walkBody = `{
	if n == nil {
		return nil
	}
	for _, c := range n.Children() {
		if !fn(c) {
			continue
		}
		total += len(c.Children())
	}
	return nil
}
`

func TestRenameChainKeepsID(t *testing.T) {
	h := newHistory(t)
	h.commit(map[string]string{"walk.go": "package p\n\nfunc (ix *Indexer) Walk(n Node, fn func(Node) bool) error " + walkBody})
	born := h.id("walk.go", "Indexer.Walk")

	// Moved to another file.
	ev := h.commit(map[string]string{
		"walk.go":    "",
		"indexer.go": "package p\n\nfunc (ix *Indexer) Walk(n Node, fn func(Node) bool) error " + walkBody,
	})
	if len(ev) != 1 || !ev[0].Flags.Has(ast.Moved) || h.id("indexer.go", "Indexer.Walk") != born {
		t.Fatalf("move events = %+v", ev)
	}

	// Renamed.
	h.commit(map[string]string{"indexer.go": "package p\n\nfunc (ix *Indexer) Visit(n Node, fn func(Node) bool) error " + walkBody})
	// Signature changed.
	ev = h.commit(map[string]string{"indexer.go": "package p\n\nfunc (ix *Indexer) Visit(n Node, fn VisitFunc) error " + walkBody})

	if got := h.id("indexer.go", "Indexer.Visit"); got != born {
		t.Fatalf("Visit id = %d, want %d (born as Walk)", got, born)
	}
	if len(ev) != 1 || !ev[0].Flags.Has(ast.SignatureChanged) {
		t.Fatalf("signature events = %+v", ev)
	}
	h.gone("walk.go", "Indexer.Walk")
}

func TestUnchangedSymbolsInTouchedFileKeepIDs(t *testing.T) {
	h := newHistory(t)
	h.commit(map[string]string{"a.go": "package p\n\nfunc A() int { return 1 }\n\nfunc B() int { return 2 }\n"})
	a, b := h.id("a.go", "A"), h.id("a.go", "B")

	ev := h.commit(map[string]string{"a.go": "package p\n\nfunc A() int { return 10 }\n\n\n// B doc.\nfunc B() int { return 2 }\n"})
	if len(ev) != 1 || ev[0].New.Key != "A" || ev[0].Flags != ast.BodyChanged {
		t.Fatalf("events = %+v", ev)
	}
	if h.id("a.go", "A") != a || h.id("a.go", "B") != b {
		t.Fatal("ids changed")
	}
}

func TestDeleteThenReAddGetsNewID(t *testing.T) {
	h := newHistory(t)
	h.commit(map[string]string{"a.go": "package p\n\nfunc Legacy() {}\n\nfunc Keep() {}\n"})
	old := h.id("a.go", "Legacy")

	ev := h.commit(map[string]string{"a.go": "package p\n\nfunc Keep() {}\n"})
	if len(ev) != 1 || ev[0].Flags != ast.Deleted || h.h.Symbol(old).Alive() {
		t.Fatalf("delete events = %+v", ev)
	}
	h.gone("a.go", "Legacy")

	h.commit(map[string]string{"a.go": "package p\n\nfunc Legacy() {}\n\nfunc Keep() {}\n"})
	if h.id("a.go", "Legacy") == old {
		t.Fatal("re-added symbol reused the deleted symbol's id")
	}
}

func TestSplitFile(t *testing.T) {
	h := newHistory(t)
	h.commit(map[string]string{"big.go": "package p\n\nfunc One() int { return 1 }\n\nfunc Two() int { return 2 }\n"})
	one, two := h.id("big.go", "One"), h.id("big.go", "Two")

	h.commit(map[string]string{
		"big.go": "package p\n\nfunc One() int { return 1 }\n",
		"two.go": "package p\n\nfunc Two() int { return 2 }\n",
	})
	if h.id("big.go", "One") != one || h.id("two.go", "Two") != two {
		t.Fatal("split lost an id")
	}
	if h.h.Live() != 2 {
		t.Fatalf("live = %d, want 2", h.h.Live())
	}
}

func TestLifeAndFind(t *testing.T) {
	h := newHistory(t)
	h.commit(map[string]string{"a.go": "package p\n\nfunc (ix *Indexer) Walk(n Node, fn func(Node) bool) error " + walkBody})
	h.commit(map[string]string{"a.go": "package p\n\nfunc (ix *Indexer) Visit(n Node, fn func(Node) bool) error " + walkBody})
	h.commit(map[string]string{"a.go": "package p\n"})

	got := h.h.Find("(*Indexer).Visit")
	if len(got) != 1 {
		t.Fatalf("Find = %d symbols, want 1", len(got))
	}
	s := got[0]
	if s.Alive() || len(s.Events) != 3 {
		t.Fatalf("alive=%v events=%d, want deleted after 3 events", s.Alive(), len(s.Events))
	}
	want := []ast.ChangeFlags{ast.Added, ast.Renamed, ast.Deleted}
	for i, e := range s.Events {
		if e.Flags != want[i] || e.SHA != string(rune('a'+i)) {
			t.Errorf("event %d = %v@%s, want %v", i, e.Flags, e.SHA, want[i])
		}
	}
	if s.Latest().Name != "Visit" {
		t.Errorf("Latest = %s", s.Latest().Name)
	}
}

func TestUntouchedFilesAreLeftAlone(t *testing.T) {
	h := newHistory(t)
	h.commit(map[string]string{"a.go": "package p\n\nfunc A() {}\n", "b.go": "package p\n\nfunc B() {}\n"})
	b := h.id("b.go", "B")
	h.commit(map[string]string{"a.go": "package p\n\nfunc A() { x() }\n"})
	if h.id("b.go", "B") != b {
		t.Fatal("untouched symbol lost its id")
	}
}

func TestSubjects(t *testing.T) {
	for _, c := range []struct{ msg, subject, detail string }{
		{"Fix the parser", "Fix the parser", ""},
		{"Merge pull request #12 from a/b\n\nAdd the journey view\n\nLong body.", "Merge pull request #12 from a/b", "Add the journey view"},
		{"  padded  \n  next  ", "padded", "next"},
		{strings.Repeat("é", 150), strings.Repeat("é", 100), ""},
	} {
		s, d := Subjects(c.msg)
		if s != c.subject || d != c.detail {
			t.Errorf("Subjects(%q) = %q, %q; want %q, %q", c.msg, s, d, c.subject, c.detail)
		}
	}
}
