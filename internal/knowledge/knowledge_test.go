package knowledge

import (
	"testing"
	"time"

	"huginn/internal/ast"
	"huginn/internal/lineage"
)

func TestBusFactor(t *testing.T) {
	for _, c := range []struct {
		ws   []Weight
		want int
	}{
		{nil, 0},
		{[]Weight{{0, 3}}, 1},
		{[]Weight{{0, 6}, {1, 4}}, 1}, // 60% is more than half
		{[]Weight{{0, 5}, {1, 5}}, 2}, // exactly half is not
		{[]Weight{{0, 4}, {1, 3}, {2, 3}}, 2},
	} {
		if got := BusFactor(c.ws); got != c.want {
			t.Errorf("BusFactor(%v) = %d, want %d", c.ws, got, c.want)
		}
	}
}

// TestReplay checks that replaying a history symbol by symbol counts only
// what counts, and forgets deleted symbols.
func TestReplay(t *testing.T) {
	f := &ast.Symbol{Kind: ast.KindFunc, Name: "F", Key: "F", Path: "a/a.go", Stmts: 3}
	f2 := &ast.Symbol{Kind: ast.KindFunc, Name: "F", Key: "F", Path: "a/a.go", Stmts: 4}
	tf := &ast.Symbol{Kind: ast.KindFunc, Name: "TestF", Key: "TestF", Path: "a/a_test.go", Stmts: 2}
	g := &ast.Symbol{Kind: ast.KindFunc, Name: "G", Key: "G", Path: "a/a.go"}
	at := time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC)
	h := lineage.New()
	h.Apply(lineage.Revision{SHA: "a", Time: at, Author: "ann", Changes: []ast.Change{
		{Flags: ast.Added, New: f}, {Flags: ast.Added, New: tf}, {Flags: ast.Added, New: g},
	}})
	h.Apply(lineage.Revision{SHA: "b", Time: at, Author: "bob", Changes: []ast.Change{
		{Flags: ast.BodyChanged, Old: f, New: f2}, {Flags: ast.Deleted, Old: g},
	}})
	m := Replay(h, func(s *ast.Symbol) bool { return s.Path != "a/a_test.go" })
	ann, _ := m.Author("ann")
	bob, _ := m.Author("bob")

	// bob changed one statement of four: ann keeps 4 × ¾, bob gets 5 × ¼.
	got := m.Weights(1)
	if len(got) != 2 || got[0] != (Weight{ann, 3}) || got[1] != (Weight{bob, 1.25}) {
		t.Errorf("F = %v", got)
	}
	if m.Weights(2) != nil || m.Weights(3) != nil {
		t.Errorf("test or deleted symbol kept: %v %v", m.Weights(2), m.Weights(3))
	}
	if m.Departed(ann, at.Add(AwayAfter)) || !m.Departed(ann, at.Add(AwayAfter+time.Hour)) {
		t.Errorf("ann departs once six months pass without a commit")
	}
}
