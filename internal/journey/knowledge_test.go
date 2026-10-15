package journey

import (
	"math"
	"testing"
	"time"

	"huginn/internal/ast"
	"huginn/internal/codemap"
	"huginn/internal/lineage"
)

func stmts(s *ast.Symbol, n int) *ast.Symbol {
	s.Stmts = n
	return s
}

func buildHist(h *lineage.History, step Step) *Journey {
	return Build(h, codemap.Build(h), step, Options{})
}

// knowers flattens a package's state in a frame to author → share.
func knowers(j *Journey, f Frame, pkg string) (map[string]float64, PackageState) {
	for _, p := range f.Packages {
		if j.Packages[p.Package].Path == pkg {
			out := map[string]float64{}
			for _, k := range p.Knowers {
				out[j.Authors[k.Author]] = k.Share
			}
			return out, p
		}
	}
	return nil, PackageState{}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestKnowledgeWeighting(t *testing.T) {
	f := stmts(fn("a/a.go", "F"), 3)
	f2 := stmts(fn("a/a.go", "F"), 4)
	f3 := stmts(fn("a/a.go", "F"), 4)
	f3.StartLine = 7
	g := fn("a/a.go", "G")
	g2 := fn("a/b.go", "G")
	at := day("2026-03-02")

	h := lineage.New()
	h.Apply(lineage.Revision{SHA: "a", Time: at, Author: "alice", Changes: []ast.Change{{Flags: ast.Added, New: f}}})
	// One statement of four changes: bob takes a quarter of F.
	h.Apply(lineage.Revision{SHA: "b", Time: at, Author: "bob", Changes: []ast.Change{{Flags: ast.BodyChanged, Old: f, New: f2}}})
	// A body edit that keeps the count still changes at least one.
	h.Apply(lineage.Revision{SHA: "c", Time: at, Author: "carol", Changes: []ast.Change{{Flags: ast.BodyChanged, Old: f2, New: f3}}})
	// A move adds a little; G's own weight is bob's.
	h.Apply(lineage.Revision{SHA: "d", Time: at, Author: "bob", Changes: []ast.Change{{Flags: ast.Added, New: g}}})
	h.Apply(lineage.Revision{SHA: "e", Time: at, Author: "alice", Changes: []ast.Change{{Flags: ast.Moved, Old: g, New: g2}}})

	j := buildHist(h, Commit)
	if len(j.Authors) != 3 {
		t.Fatalf("authors %v", j.Authors)
	}

	got, p := knowers(j, j.Frames[0], "a")
	if !near(got["alice"], 1) || p.BusFactor != 1 || p.Orphaned != 0 {
		t.Errorf("after add: %v bus %d orphaned %v", got, p.BusFactor, p.Orphaned)
	}

	// alice 4 × ¾ = 3, bob ¼ × 5 = 1.25.
	got, _ = knowers(j, j.Frames[1], "a")
	if !near(got["alice"], 3/4.25) || !near(got["bob"], 1.25/4.25) {
		t.Errorf("after bob's edit: %v", got)
	}

	// carol takes a quarter: alice 2.25, bob 0.9375, carol 1.25.
	got, p = knowers(j, j.Frames[2], "a")
	total := 2.25 + 0.9375 + 1.25
	if !near(got["alice"], 2.25/total) || !near(got["bob"], 0.9375/total) || !near(got["carol"], 1.25/total) {
		t.Errorf("after carol's edit: %v", got)
	}
	if p.BusFactor != 1 {
		t.Errorf("bus factor %d, want 1: alice holds just over half", p.BusFactor)
	}
	if a := p.Knowers[0]; j.Authors[a.Author] != "alice" {
		t.Errorf("top knower %s, want alice", j.Authors[a.Author])
	}

	// G weighs 1 for bob, plus 0.5 for alice's move.
	got, p = knowers(j, j.Frames[4], "a")
	total += 1 + touch
	if !near(got["alice"], (2.25+touch)/total) || !near(got["bob"], (0.9375+1)/total) {
		t.Errorf("after the move: %v", got)
	}
	if p.BusFactor != 2 {
		t.Errorf("bus factor %d, want 2", p.BusFactor)
	}
}

func TestKnowledgeRewriteTakesOver(t *testing.T) {
	f := stmts(fn("a/a.go", "F"), 2)
	f2 := stmts(fn("a/a.go", "F"), 6)
	at := day("2026-03-02")
	h := lineage.New()
	h.Apply(lineage.Revision{SHA: "a", Time: at, Author: "alice", Changes: []ast.Change{{Flags: ast.Added, New: f}}})
	h.Apply(lineage.Revision{SHA: "b", Time: at, Author: "bob", Changes: []ast.Change{{Flags: ast.BodyChanged, Old: f, New: f2}}})
	j := buildHist(h, Commit)
	// Four of six statements changed: alice keeps a third of her 3.
	got, _ := knowers(j, j.Frames[1], "a")
	alice, bob := 3.0/3, 7*4.0/6
	if !near(got["alice"], alice/(alice+bob)) || !near(got["bob"], bob/(alice+bob)) {
		t.Errorf("%v", got)
	}
}

func TestKnowledgeDecayAndDeparture(t *testing.T) {
	x, y, z := fn("a/a.go", "X"), fn("a/a.go", "Y"), fn("b/b.go", "Z")
	t0 := day("2026-01-05")
	h := lineage.New()
	h.Apply(lineage.Revision{SHA: "a", Time: t0, Author: "alice", Changes: []ast.Change{{Flags: ast.Added, New: x}}})
	// One half-life later, bob's knowledge counts twice alice's.
	h.Apply(lineage.Revision{SHA: "b", Time: t0.Add(halfLife), Author: "bob", Changes: []ast.Change{{Flags: ast.Added, New: y}}})
	// A week later alice has been away more than six months.
	h.Apply(lineage.Revision{SHA: "c", Time: t0.Add(halfLife + 7*24*time.Hour), Author: "bob", Changes: []ast.Change{{Flags: ast.Added, New: z}}})
	// She comes back, in another package.
	h.Apply(lineage.Revision{SHA: "d", Time: t0.Add(halfLife + 14*24*time.Hour), Author: "alice", Changes: []ast.Change{{Flags: ast.Deleted, Old: z}}})
	j := buildHist(h, Commit)

	got, p := knowers(j, j.Frames[1], "a")
	if !near(got["alice"], 1.0/3) || !near(got["bob"], 2.0/3) {
		t.Errorf("decay: %v", got)
	}
	if p.Orphaned != 0 {
		t.Errorf("alice departed exactly at six months: orphaned %v", p.Orphaned)
	}
	_, p = knowers(j, j.Frames[2], "a")
	if !near(p.Orphaned, 1.0/3) || !p.Knowers[1].Departed || p.Knowers[0].Departed {
		t.Errorf("departure: orphaned %v knowers %+v", p.Orphaned, p.Knowers)
	}
	_, p = knowers(j, j.Frames[3], "a")
	if p.Orphaned != 0 {
		t.Errorf("alice is back: orphaned %v", p.Orphaned)
	}
}

func TestKnowledgeDeterministic(t *testing.T) {
	a, b := build(Week), build(Week)
	for i := range a.Frames {
		for k := range a.Frames[i].Packages {
			pa, pb := a.Frames[i].Packages[k], b.Frames[i].Packages[k]
			if len(pa.Knowers) != len(pb.Knowers) || pa.Orphaned != pb.Orphaned || pa.BusFactor != pb.BusFactor {
				t.Fatalf("frame %d package %d differs", i, k)
			}
			for n := range pa.Knowers {
				if pa.Knowers[n] != pb.Knowers[n] {
					t.Fatalf("frame %d package %d knower %d differs", i, k, n)
				}
			}
		}
	}
}
