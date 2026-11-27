// Package limits holds the package-size budget: how many functions and
// methods a package may declare, exported or not. The budgets on single
// functions, gocognit and gocyclo, are in .golangci.yml.
//
// Each budget alone leaves a way around it: under gocyclo alone a long
// function is split into helpers in the same package, and under this budget
// alone helpers fold back into long functions. Held to both, code that keeps
// growing has to move into a package of its own.
package limits

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// maxFuncs is how many functions and methods a package may declare, test
// files aside.
const maxFuncs = 80

// frozen holds the packages that were over maxFuncs when the budget came
// in, at their size then. They may shrink but not grow. When one shrinks,
// TestFrozenBudgetsAreTight asks for its number to be lowered here, so the
// room freed is not filled again; once it is within maxFuncs it leaves the
// list.
var frozen = map[string]int{
	"cmd/huginn":        94,
	"internal/affinity": 195,
	"internal/ast":      113,
	"internal/cohesion": 101,
	"internal/journey":  81,
	"internal/review":   92,
	"internal/semdiff":  85,
	"internal/story":    92,
	"internal/web":      93,
}

// TestPackageSize fails when a package declares more functions and methods
// than its budget.
func TestPackageSize(t *testing.T) {
	counts := countFuncs(t)
	for _, pkg := range slices.Sorted(maps.Keys(counts)) {
		n, limit := counts[pkg], budget(pkg)
		if n > limit {
			t.Errorf("%s declares %d functions and methods, over its budget of %d.\n"+
				"It has outgrown one package. Split it into packages along a seam (for a big type, huginn affinity <type> "+
				"shows which methods belong together) instead of raising the budget, or remove functions it no longer needs.",
				pkg, n, limit)
		}
	}
}

// TestFrozenBudgetsAreTight fails when a frozen package has shrunk below its
// number, or is gone, so that its budget follows it down.
func TestFrozenBudgetsAreTight(t *testing.T) {
	counts := countFuncs(t)
	for _, pkg := range slices.Sorted(maps.Keys(frozen)) {
		n, ok := counts[pkg]
		switch limit := frozen[pkg]; {
		case !ok:
			t.Errorf("%s is frozen at %d but has no Go files: remove it from frozen", pkg, limit)
		case n <= maxFuncs:
			t.Errorf("%s is down to %d, within maxFuncs (%d): remove it from frozen", pkg, n, maxFuncs)
		case n < limit:
			t.Errorf("%s is down to %d from its frozen %d: lower it to %d", pkg, n, limit, n)
		}
	}
}

func budget(pkg string) int {
	if n, ok := frozen[pkg]; ok {
		return n
	}
	return maxFuncs
}

// countFuncs counts the functions and methods declared in each directory of
// the module, keyed by its slash-separated path from the module root. Test
// files, testdata and directories starting with . or _ are left out, as the
// go command leaves them out of ./...
func countFuncs(t *testing.T) map[string]int {
	t.Helper()
	root := moduleRoot(t)
	fset := token.NewFileSet()
	counts := map[string]int{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if path != root && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "testdata" || name == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}
		pkg := filepath.ToSlash(rel)
		for _, decl := range f.Decls {
			if _, ok := decl.(*ast.FuncDecl); ok {
				counts[pkg]++
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return counts
}

// moduleRoot returns the nearest directory above the test's own that holds
// a go.mod.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test directory")
		}
		dir = parent
	}
}
