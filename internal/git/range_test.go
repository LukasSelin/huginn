package git

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// mergeRequestRepo makes a repository with a branch feature off master,
// and master moved on since, and returns the commit both started from.
func mergeRequestRepo(t *testing.T) (*testRepo, string) {
	r := newTestRepo(t)
	root := r.Commit("root", map[string]string{
		"go.mod":       "module example.com/m\n",
		"a/a.go":       "package a\n\nfunc A() int { return 1 }\n",
		"b/b.go":       "package b\n\n// B is long enough for git to see the rename.\nfunc B(x, y int) int {\n\tif x > y {\n\t\treturn x - y\n\t}\n\treturn y - x\n}\n",
		"other.go":     "package m\n\nfunc Other() {}\n",
		"docs/use.txt": "how\n",
	})
	r.Git("checkout", "-q", "-b", "feature")
	r.Commit("edit a", map[string]string{"a/a.go": "package a\n\nfunc A() int { return 2 }\n\nfunc A2() {}\n"})
	r.Git("mv", "b", "c")
	r.Commit("move b", map[string]string{"docs/use.txt": "how and why\n"})
	r.Git("checkout", "-q", "master")
	r.Commit("unrelated", map[string]string{"other.go": "package m\n\nfunc Other() { Other() }\n"})
	return r, root
}

func TestDiffRange(t *testing.T) {
	r, root := mergeRequestRepo(t)
	repo := r.open()
	ctx := context.Background()

	base, err := repo.MergeBase(ctx, "master", "feature")
	if err != nil {
		t.Fatal(err)
	}
	if base != root {
		t.Fatalf("merge base = %s, want the root %s", base, root)
	}
	rg, err := repo.DiffRange(ctx, base, "feature")
	if err != nil {
		t.Fatal(err)
	}
	if rg.Base != root || rg.Head != r.Git("rev-parse", "feature") {
		t.Errorf("range %s..%s", rg.Base, rg.Head)
	}

	// master's own commit since is not part of it, and the docs change is
	// no Go file.
	var got []string
	for _, fd := range rg.Files {
		got = append(got, fd.Status.String()+" "+side(fd.Old)+" -> "+side(fd.New))
	}
	want := []string{"M a/a.go -> a/a.go", "R b/b.go -> c/b.go"}
	if !slices.Equal(got, want) {
		t.Errorf("files =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if len(rg.Changed) != 3 {
		t.Errorf("%d files changed, want 3 with the docs", len(rg.Changed))
	}
	if rg.Added != 4 || rg.Removed != 2 {
		t.Errorf("lines +%d −%d, want +4 −2", rg.Added, rg.Removed)
	}
	if a := rg.Files[0].New; a == nil || len(a.Symbols) != 2 {
		t.Errorf("a/a.go after = %+v, want A and A2", a)
	}
	if len(rg.ParseErrors) > 0 {
		t.Errorf("parse errors: %v", rg.ParseErrors)
	}
}

func TestBranchesAndGoDirs(t *testing.T) {
	r, _ := mergeRequestRepo(t)
	repo := r.open()
	ctx := context.Background()
	clone := filepath.Join(t.TempDir(), "clone")
	r.Git("clone", "-q", r.Dir, clone)
	cr, err := Open(ctx, clone)
	if err != nil {
		t.Fatal(err)
	}
	// master was committed to last; origin/HEAD is left out.
	got, err := cr.Branches(ctx)
	if want := []string{"master", "origin/master", "origin/feature"}; err != nil || !slices.Equal(got, want) {
		t.Errorf("Branches = %q, %v; want %q", got, err, want)
	}

	got, err = repo.GoDirs(ctx, "feature")
	if want := []string{".", "a", "c"}; err != nil || !slices.Equal(got, want) {
		t.Errorf("GoDirs(feature) = %q, %v; want %q", got, err, want)
	}
	if _, err := repo.GoDirs(ctx, "nope"); err == nil {
		t.Error("GoDirs(nope): no error")
	}
}

func TestMergeBaseUnrelated(t *testing.T) {
	r, _ := mergeRequestRepo(t)
	r.Git("checkout", "-q", "--orphan", "lonely")
	r.Commit("alone", map[string]string{"x.go": "package x\n"})
	_, err := r.open().MergeBase(context.Background(), "master", "lonely")
	if err == nil || !strings.Contains(err.Error(), "no common ancestor") {
		t.Errorf("err = %v, want no common ancestor", err)
	}
}

func TestDefaultBranch(t *testing.T) {
	r, _ := mergeRequestRepo(t)
	repo := r.open()
	ctx := context.Background()
	if b, err := repo.DefaultBranch(ctx); err != nil || b != "master" {
		t.Errorf("without a remote: %q, %v; want master", b, err)
	}

	// A clone knows the remote's default branch, the branch checked out
	// there, through origin/HEAD, even with origin/master around.
	r.Git("checkout", "-q", "feature")
	clone := filepath.Join(t.TempDir(), "clone")
	r.Git("clone", "-q", r.Dir, clone)
	cr, err := Open(context.Background(), clone)
	if err != nil {
		t.Fatal(err)
	}
	if b, berr := cr.DefaultBranch(ctx); berr != nil || b != "origin/feature" {
		t.Errorf("in a clone: %q, %v; want origin/feature", b, berr)
	}

	top, err := cr.Toplevel(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if a, b := stat(t, top), stat(t, clone); !os.SameFile(a, b) {
		t.Errorf("Toplevel = %s, want %s", top, clone)
	}
}

func stat(t *testing.T, p string) os.FileInfo {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi
}
