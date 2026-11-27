package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckout(t *testing.T) {
	r := newTestRepo(t)
	first := r.Commit("first", map[string]string{"go.mod": "module example.com/m\n", "a/a.go": "package a\n\nfunc A() int { return 1 }\n"})
	r.Commit("second", map[string]string{"a/a.go": "package a\n\nfunc A() int { return 2 }\n", "b/b.go": "package b\n"})
	repo := r.open()
	ctx := context.Background()

	dir, remove, err := repo.Checkout(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "a", "a.go"))
	if err != nil || string(got) != "package a\n\nfunc A() int { return 1 }\n" {
		t.Errorf("a/a.go at the first commit = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "b")); !os.IsNotExist(err) {
		t.Errorf("b exists at the first commit: %v", err)
	}
	remove()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the checkout is still there after remove: %v", err)
	}

	// The repository keeps its branch and its working tree.
	if b := r.Git("rev-parse", "--abbrev-ref", "HEAD"); b != "master" {
		t.Errorf("HEAD is %s, want master", b)
	}
	if s := r.Git("status", "--porcelain"); s != "" {
		t.Errorf("status = %q, want clean", s)
	}

	if _, _, err := repo.Checkout(ctx, "nope"); err == nil {
		t.Error("checkout of an unknown revision: want an error")
	}
}
