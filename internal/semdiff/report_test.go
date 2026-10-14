package semdiff

import (
	"fmt"
	"strings"
	"testing"

	"huginn/internal/ast"
)

var goMod = map[string][]byte{"go.mod": []byte("module example.com/app // the app\n\ngo 1.27.1\n")}

func parse(t *testing.T, path, src string) *ast.File {
	t.Helper()
	f, err := ast.ParseSource(path, []byte(src))
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return f
}

func change(t *testing.T, r *Report, name string) Change {
	t.Helper()
	for _, c := range r.Changes {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no change %q in %+v", name, r.Changes)
	return Change{}
}

func callers(cs []Caller) string {
	var out []string
	for _, c := range cs {
		s := fmt.Sprintf("%s %s %d", c.Package, c.Name, c.Calls)
		if c.Approximate {
			s += " approx"
		}
		out = append(out, s)
	}
	return fmt.Sprint(out)
}

func TestExportedSignatureChange(t *testing.T) {
	oldStore := parse(t, "store/store.go", `package store

import "os"

type DB struct{ f *os.File }

// Open opens the database at path.
func Open(path string) (*DB, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return &DB{f: f}, nil
}

func reopen(db *DB) (*DB, error) { return Open(db.f.Name()) }
`)
	newStore := parse(t, "store/store.go", `package store

import (
	"context"
	"os"
)

type DB struct{ f *os.File }

// Open opens the database at path.
func Open(ctx context.Context, path string) (*DB, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return &DB{f: f}, nil
}

func reopen(db *DB) (*DB, error) { return Open(context.Background(), db.f.Name()) }
`)
	tree := []*ast.File{
		newStore,
		parse(t, "api/api.go", `package api

import (
	"context"

	"example.com/app/store"
)

func Load(ctx context.Context) error {
	db, err := store.Open(ctx, "a")
	if err != nil {
		return err
	}
	_, err = store.Open(ctx, "b")
	return err
}
`),
		parse(t, "cmd/tool/main.go", `package main

import st "example.com/app/store"

func main() { st.Open(nil, "x") }
`),
		// Same name, different package: not a caller.
		parse(t, "other/other.go", "package other\n\nfunc Open() {}\n\nfunc use() { Open() }\n"),
	}

	r := Build(Input{
		Files: []FileDiff{{Old: oldStore, New: newStore, Added: 9, Removed: 2}},
		Tree:  tree,
		GoMod: goMod,
	})

	open := change(t, r, "store.Open")
	if open.Kind != KindSignature || fmt.Sprint(open.Kinds) != "[signature body edit]" {
		t.Errorf("Open kinds = %v %v", open.Kind, open.Kinds)
	}
	if open.OldSignature != "func Open(path string) (*DB, error)" ||
		open.NewSignature != "func Open(ctx context.Context, path string) (*DB, error)" {
		t.Errorf("signatures = %q -> %q", open.OldSignature, open.NewSignature)
	}
	if open.Package != "example.com/app/store" || open.Path != "store/store.go" || open.Line != 11 {
		t.Errorf("location = %s %s:%d", open.Package, open.Path, open.Line)
	}
	if open.Summary != "+3 stmts · cyclomatic 2 → 3" {
		t.Errorf("Summary = %q", open.Summary)
	}
	if !open.Exported || !open.APIChange {
		t.Errorf("Exported=%v APIChange=%v", open.Exported, open.APIChange)
	}
	want := "[example.com/app/api api.Load 2 example.com/app/cmd/tool main.main 1 example.com/app/store store.reopen 1]"
	if got := callers(open.Callers); got != want {
		t.Errorf("callers:\n got %s\nwant %s", got, want)
	}
	if open.CallSites != 4 || open.Approximate || open.Impact != "4 call sites · exported" {
		t.Errorf("CallSites=%d Approximate=%v Impact=%q", open.CallSites, open.Approximate, open.Impact)
	}

	reopen := change(t, r, "store.reopen")
	if reopen.Kind != KindBody || reopen.APIChange || reopen.Impact != "no callers" {
		t.Errorf("reopen = %v api=%v %q", reopen.Kind, reopen.APIChange, reopen.Impact)
	}

	b := r.Blast
	if b.CallSites != 4 || fmt.Sprint(b.Packages) != "[example.com/app/api example.com/app/cmd/tool example.com/app/store]" {
		t.Errorf("blast = %d %v", b.CallSites, b.Packages)
	}
	if !b.ExportedAPIChanged() || fmt.Sprint(b.APIChanges) != "[store.Open]" {
		t.Errorf("APIChanges = %v", b.APIChanges)
	}
	if got := callers(b.Callers); got != want {
		t.Errorf("blast callers:\n got %s\nwant %s", got, want)
	}

	if len(r.Files) != 1 || fmt.Sprint(r.Files[0].Kinds) != "[signature body edit]" || r.Files[0].Changes != 2 {
		t.Errorf("Files = %+v", r.Files)
	}
	if got := r.Headline(); got != "+9 −2 lines → 3 semantic changes across 2 symbols" {
		t.Errorf("Headline = %q", got)
	}
}

func TestRenamedMethodThroughSelector(t *testing.T) {
	const indexer = `package index

type Node interface{ Kids() []Node }

type Indexer struct{ seen map[Node]bool }

func (ix *Indexer) Walk(n Node) int {
	if n == nil || ix.seen[n] {
		return 0
	}
	ix.seen[n] = true
	total := 1
	for _, k := range n.Kids() {
		total += ix.Walk(k)
	}
	return total
}

func (ix *Indexer) All(roots []Node) (sum int) {
	for _, r := range roots {
		sum += ix.Walk(r)
	}
	return sum
}
`
	renamed := strings.ReplaceAll(indexer, "Walk", "Visit")
	newIndex := parse(t, "index/index.go", renamed)
	tree := []*ast.File{
		newIndex,
		parse(t, "render/render.go", `package render

import "example.com/app/index"

func Render(roots []index.Node) int {
	ix := &index.Indexer{}
	n := 0
	for _, r := range roots {
		n += ix.Visit(r)
	}
	return n
}
`),
		// Calls a Visit method too, but cannot reach index's: not a caller.
		parse(t, "other/visitor.go", "package other\n\ntype V struct{}\n\nfunc (V) Visit() {}\n\nfunc run(v V) { v.Visit() }\n"),
	}

	r := Build(Input{
		Files: []FileDiff{{Old: parse(t, "index/index.go", indexer), New: newIndex, Added: 3, Removed: 3}},
		Tree:  tree,
		GoMod: goMod,
	})

	visit := change(t, r, "index.(*Indexer).Visit")
	if visit.Kind != KindRenamed || visit.OldName != "index.(*Indexer).Walk" {
		t.Errorf("Visit = %v from %q", visit.Kind, visit.OldName)
	}
	if !visit.APIChange || visit.Summary != "renamed from (*Indexer).Walk · body edited, same size" {
		t.Errorf("APIChange=%v Summary=%q", visit.APIChange, visit.Summary)
	}
	// All calls through the receiver, so it resolves exactly; Render only by name.
	want := "[example.com/app/index index.(*Indexer).All 1 example.com/app/render render.Render 1 approx]"
	if got := callers(visit.Callers); got != want {
		t.Errorf("callers:\n got %s\nwant %s", got, want)
	}
	if !visit.Approximate || visit.Impact != "2 call sites (approx.) · exported" {
		t.Errorf("Approximate=%v Impact=%q", visit.Approximate, visit.Impact)
	}
	if !r.Blast.Approximate || fmt.Sprint(r.Blast.APIChanges) != "[index.(*Indexer).Visit]" {
		t.Errorf("blast = %+v", r.Blast)
	}
}

func TestFormattingOnlyFileIsNoise(t *testing.T) {
	const util = `package util

import "strings"

// Trim trims.
func Trim(s string) string { return strings.TrimSpace(s) }
`
	const reformatted = `package util

import "strings"

// Trim trims surrounding white space.
func Trim(s string) string {
	return strings.TrimSpace(s) // see strings
}
`
	const gen = "// Code generated by stringer. DO NOT EDIT.\n\npackage util\n\nfunc (c Color) String() string { return %q }\n"
	r := Build(Input{
		Files: []FileDiff{
			{Old: parse(t, "util/util.go", util), New: parse(t, "util/util.go", reformatted), Added: 4, Removed: 2},
			{Old: parse(t, "util/color_string.go", fmt.Sprintf(gen, "red")), New: parse(t, "util/color_string.go", fmt.Sprintf(gen, "blue")), Added: 1, Removed: 1},
			// An import edit changes no symbol but is not formatting either.
			{Old: parse(t, "util/conf.go", "package util\n\nimport _ \"embed\"\n"), New: parse(t, "util/conf.go", "package util\n"), Removed: 2},
		},
		GoMod: goMod,
	})

	if len(r.Changes) != 0 {
		t.Errorf("Changes = %+v", r.Changes)
	}
	want := "[{util/color_string.go generated 1 1} {util/util.go formatting or comments 4 2}]"
	if got := fmt.Sprint(r.Noise); got != want {
		t.Errorf("Noise:\n got %s\nwant %s", got, want)
	}
	if len(r.Files) != 1 || r.Files[0].Path != "util/conf.go" || len(r.Files[0].Kinds) != 0 {
		t.Errorf("Files = %+v", r.Files)
	}
	if got := r.Headline(); got != "+5 −5 lines → 0 semantic changes across 0 symbols" {
		t.Errorf("Headline = %q", got)
	}
}

func TestAddedAndDeleted(t *testing.T) {
	newA := parse(t, "a/a.go", "package a\n\nfunc Fresh(s string) bool { return s != \"\" }\n")
	r := Build(Input{
		Files: []FileDiff{{Old: parse(t, "a/a.go", "package a\n\nfunc Legacy() int { return 1 }\n"), New: newA, Added: 1, Removed: 1}},
		Tree: []*ast.File{
			newA,
			parse(t, "b/b.go", "package b\n\nimport \"example.com/app/a\"\n\nfunc Use() bool { return a.Fresh(\"x\") }\n"),
		},
		GoMod: goMod,
	})

	if r.Symbols != 2 || r.Counts[KindAdded] != 1 || r.Counts[KindDeleted] != 1 || r.SemanticChanges != 2 {
		t.Fatalf("Symbols=%d Counts=%v", r.Symbols, r.Counts)
	}
	fresh, legacy := change(t, r, "a.Fresh"), change(t, r, "a.Legacy")
	if fresh.Kind != KindAdded || fresh.OldSignature != "" || fresh.NewSignature != "func Fresh(s string) bool" ||
		fresh.Summary != "1 stmt · cyclomatic 1" || fresh.APIChange {
		t.Errorf("Fresh = %+v", fresh)
	}
	if fresh.CallSites != 1 || fresh.Impact != "1 call site · exported" {
		t.Errorf("Fresh callers = %d %q", fresh.CallSites, fresh.Impact)
	}
	if legacy.Kind != KindDeleted || legacy.NewSignature != "" || legacy.Summary != "−1 stmt" || !legacy.APIChange {
		t.Errorf("Legacy = %+v", legacy)
	}
	// Callers of an added symbol are new code, not blast radius.
	if r.Blast.CallSites != 0 || len(r.Blast.Callers) != 0 || fmt.Sprint(r.Blast.APIChanges) != "[a.Legacy]" {
		t.Errorf("Blast = %+v", r.Blast)
	}
	if fmt.Sprint(r.Files[0].Kinds) != "[added deleted]" {
		t.Errorf("file kinds = %v", r.Files[0].Kinds)
	}
}

func TestImportPath(t *testing.T) {
	mods := newModules(map[string][]byte{
		"go.mod":       []byte("module example.com/app\n"),
		"tools/go.mod": []byte("// tools\nmodule \"example.com/tools\"\n"),
	})
	for dir, want := range map[string]string{
		".":            "example.com/app",
		"internal/ast": "example.com/app/internal/ast",
		"tools":        "example.com/tools",
		"tools/gen/x":  "example.com/tools/gen/x",
		"toolsy/thing": "example.com/app/toolsy/thing",
	} {
		if got := mods.importPath(dir); got != want {
			t.Errorf("importPath(%q) = %q, want %q", dir, got, want)
		}
	}
	if got := (modules{}).importPath("a/b"); got != "a/b" {
		t.Errorf("without go.mod: %q", got)
	}
}
