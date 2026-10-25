package moves

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeModule writes files, keyed by slash-separated path, into a new
// module named m and returns its directory.
func writeModule(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	files["go.mod"] = "module m\n\ngo 1.22\n"
	for name, src := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func findMoves(t *testing.T, files map[string]string) (*Graph, map[string]Move) {
	t.Helper()
	g, err := Load(context.Background(), writeModule(t, files), "./...")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Move{}
	for _, m := range Find(g) {
		byName[m.Func.Pkg.Name+"."+m.Func.Name()] = m
	}
	return g, byName
}

func names(syms []*Sym) string {
	var s []string
	for _, x := range syms {
		s = append(s, x.Name())
	}
	return strings.Join(s, ",")
}

func TestFind(t *testing.T) {
	_, moves := findMoves(t, map[string]string{
		"a/a.go": `package a

// F is used only by b, and brings its private helpers along.
func F() int { return helper() + limit + (&counter{}).next() }

func helper() int { return twice(1) }
func twice(n int) int { return 2 * n }

const limit = 3

type counter struct{ n int }

func (c *counter) next() int { c.n++; return c.n }

// G is used only by b but shares shared with H, which stays.
func G() int { return shared() }
func H() int { return shared() }
func shared() int { return 1 }

// Used is referred to from inside a, so it stays.
func Used() int { return 1 }
func caller() int { return Used() }

var _ = caller

type T struct{}

// M is a method; methods cannot leave their type's package.
func (T) M() {}
`,
		"a/a_test.go": `package a

import "testing"

func TestTwice(t *testing.T) { _ = twice(2) }
`,
		"b/b.go": `package b

import "m/a"

func Use() int { return a.F() + a.G() + a.Used() }

func UseM() { a.T{}.M() }
`,
	})

	f, ok := moves["a.F"]
	if !ok {
		t.Fatalf("no move for a.F; got %v", moves)
	}
	if f.To.PkgPath != "m/b" || names(f.Users) != "Use" {
		t.Errorf("a.F moves to %s for %s, want m/b for Use", f.To.PkgPath, names(f.Users))
	}
	if got := names(f.With); got != "helper,limit,counter,twice" {
		t.Errorf("a.F moves with %s", got)
	}
	if len(f.Blocked) > 0 {
		t.Errorf("a.F blocked: %v", f.Blocked)
	}
	if len(f.TestFiles) != 1 || filepath.Base(f.TestFiles[0]) != "a_test.go" {
		t.Errorf("a.F named in %v, want a_test.go, which uses twice", f.TestFiles)
	}

	g := moves["a.G"]
	if len(g.Blocked) != 1 || !strings.Contains(g.Blocked[0], "unexported shared") {
		t.Errorf("a.G blocked: %v, want shared helper", g.Blocked)
	}
	if g.With != nil {
		t.Errorf("a.G moves with %s, but shared stays", names(g.With))
	}
	for _, name := range []string{"a.Used", "a.H", "a.(T).M", "b.Use"} {
		if _, ok := moves[name]; ok {
			t.Errorf("unexpected move for %s", name)
		}
	}
}

// A function moving into its only user's package cannot close a cycle: that
// package already imports the function's, which imports all it needs. The
// check still guards moves to a package that does not import the function's
// yet, as here, where b does not use K.
func TestBlockersImportCycle(t *testing.T) {
	g, _ := findMoves(t, map[string]string{
		"a/a.go": `package a

import "m/d"

func K() int { return d.D() }
`,
		"b/b.go": `package b

var Name = "b"
`,
		"d/d.go": `package d

import "m/b"

var _ = b.Name

func D() int { return 1 }
`,
	})
	var k *Sym
	for _, s := range g.Syms {
		if s.Name() == "K" {
			k = s
		}
	}
	m := Move{Func: k, To: g.Pkgs["m/b"]}
	got := g.blockers(m, closure(k))
	if len(got) != 1 || got[0] != "import cycle: m/b -> m/d -> m/b" {
		t.Errorf("blockers = %q", got)
	}
}

func TestFindInternal(t *testing.T) {
	_, moves := findMoves(t, map[string]string{
		// P needs x/internal/y, which outside/ may not import.
		"x/p/p.go": `package p

import "m/x/internal/y"

func P() int { return y.Y }
`,
		"x/internal/y/y.go": `package y

const Y = 1
`,
		"outside/o.go": `package outside

import "m/x/p"

var O = p.P()
`,
	})
	p := moves["p.P"]
	if len(p.Blocked) != 1 || !strings.Contains(p.Blocked[0], "internal package m/x/internal/y") {
		t.Errorf("p.P blocked: %q", p.Blocked)
	}
}

func TestDispatch(t *testing.T) {
	g, _ := findMoves(t, map[string]string{
		"a/a.go": `package a

type Shape interface{ Area() int }

type square struct{ n int }

func (s square) Area() int { return s.n * s.n }

func New() Shape { return square{2} }

func Total(s Shape) int { return s.Area() }
`,
	})
	var total, area *Sym
	for _, s := range g.Syms {
		switch s.Name() {
		case "Total":
			total = s
		case "(square).Area":
			area = s
		}
	}
	dynamic := false
	for _, u := range total.Out {
		if u.To == area && u.Dynamic {
			dynamic = true
		}
	}
	if !dynamic {
		t.Errorf("Total has no dynamic call to (square).Area: %v", total.Out)
	}
}
