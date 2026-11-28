package review

import (
	"fmt"
	"slices"
	"testing"

	"huginn/internal/ast"
	"huginn/internal/semdiff"
)

// parseTree parses the files of one revision of example.com/app, by path.
func parseTree(t *testing.T, srcs map[string]string) []*ast.File {
	t.Helper()
	var out []*ast.File
	for path, src := range srcs {
		f, err := ast.ParseSource(path, []byte(src))
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		out = append(out, f)
	}
	return out
}

func TestLayersOf(t *testing.T) {
	const m = "example.com/app/"
	pkgOf := semdiff.PackagePaths(map[string][]byte{"go.mod": []byte("module example.com/app\n")})
	// Before: e imports c, c imports b, b imports a; a and d import
	// nothing. The test file's import of e makes no layer.
	before := parseTree(t, map[string]string{
		"a/a.go":      "package a\n",
		"a/a_test.go": "package a\n\nimport _ \"example.com/app/e\"\n",
		"b/b.go":      "package b\n\nimport _ \"example.com/app/a\"\n",
		"c/c.go":      "package c\n\nimport _ \"example.com/app/b\"\n",
		"d/d.go":      "package d\n",
		"e/e.go":      "package e\n\nimport _ \"example.com/app/c\"\n",
	})
	// After: a now imports d, which raises a and everything above it, and
	// the new package f imports a.
	after := parseTree(t, map[string]string{
		"a/a.go":      "package a\n\nimport _ \"example.com/app/d\"\n",
		"a/a_test.go": "package a\n\nimport _ \"example.com/app/e\"\n",
		"b/b.go":      "package b\n\nimport _ \"example.com/app/a\"\n",
		"c/c.go":      "package c\n\nimport _ \"example.com/app/b\"\n",
		"d/d.go":      "package d\n",
		"e/e.go":      "package e\n\nimport _ \"example.com/app/c\"\n",
		"f/f.go":      "package f\n\nimport _ \"example.com/app/a\"\n",
	})
	r := &semdiff.Report{
		Changes: []semdiff.Change{
			{Package: m + "a", Name: "a.Run", Path: "a/a.go"},
			{Package: m + "a", Name: "a.TestRun", Path: "a/a_test.go"},
			{Package: m + "f", Name: "f.New", Path: "f/f.go"},
		},
		Relations: semdiff.Relations{
			NewPackages: []string{m + "f"},
			AddedDeps:   []semdiff.Dependency{{From: m + "a", To: m + "d"}, {From: m + "f", To: m + "a"}},
		},
	}

	got := layersOf(r, before, after, pkgOf)
	var pkgs, imports []string
	for _, p := range got.Packages {
		pkgs = append(pkgs, fmt.Sprintf("%s %d→%d ×%d", p.Package[len(m):], p.Before, p.After, p.Changes))
	}
	for _, im := range got.Imports {
		imports = append(imports, fmt.Sprintf("%s→%s %v %v", im.From[len(m):], im.To[len(m):], im.Before, im.After))
	}
	// The packages it changes, the one it starts importing, and those it
	// lifts a layer though it leaves them alone; the test change counts not.
	check(t, "packages", pkgs, []string{"e 3→4 ×0", "c 2→3 ×0", "b 1→2 ×0", "f -1→2 ×1", "a 0→1 ×1", "d 0→0 ×0"})
	check(t, "imports", imports, []string{
		"a→d false true", "b→a true true", "c→b true true", "e→c true true", "f→a false true",
	})
	if want := []int{1, 1, 2, 1, 1}; !slices.Equal(got.Sizes, want) {
		t.Errorf("sizes = %v, want %v", got.Sizes, want)
	}
}

// A change that touches no package of the repository has no layers.
func TestLayersOfNothing(t *testing.T) {
	pkgOf := semdiff.PackagePaths(map[string][]byte{"go.mod": []byte("module example.com/app\n")})
	files := parseTree(t, map[string]string{"a/a.go": "package a\n"})
	if got := layersOf(&semdiff.Report{}, files, files, pkgOf); got != nil {
		t.Errorf("layers = %+v, want nil", got)
	}
}

func check(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s:\n got %q\nwant %q", what, got, want)
	}
}
