package modpath

import (
	"testing"

	"huginn/internal/ast"
)

func fn(path, name string, imports ...string) *ast.Symbol {
	s := &ast.Symbol{Kind: ast.KindFunc, Name: name, Key: name, Path: path}
	for _, imp := range imports {
		s.Refs = append(s.Refs, ast.Ref{Kind: ast.RefImport, Pkg: imp, Name: "X", Count: 1})
	}
	return s
}

// TestResolverRenamedModule checks that a module imported under its old
// and its new path resolves under both, and that an import that merely
// ends like a directory of the repository does not.
func TestResolverRenamedModule(t *testing.T) {
	r := Infer([]*ast.Symbol{
		fn("main.go", "Main"),
		fn("api/api.go", "Serve", "example.com/old/core", "example.com/old/store"),
		fn("api/v2.go", "Serve2", "example.com/new/core", "example.com/new/store", "example.com/new"),
		fn("core/core.go", "Run", "example.com/old/store"),
		fn("store/store.go", "Get", "github.com/other/core"),
	})
	for imp, want := range map[string]string{
		"example.com/old/core": "core", "example.com/new/store": "store", "example.com/new": ".",
		"github.com/other/core": "", "example.com/new/missing": "",
	} {
		if got := r.Dir(imp); got != want {
			t.Errorf("Dir(%q) = %q, want %q", imp, got, want)
		}
	}
	for dir, want := range map[string]string{"core": "example.com/new/core", "store": "example.com/new/store", ".": "example.com/new", "api": "example.com/new/api"} {
		if got := r.ImportPath(dir); got != want {
			t.Errorf("ImportPath(%q) = %q, want %q", dir, got, want)
		}
	}
}
