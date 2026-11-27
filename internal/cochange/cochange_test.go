package cochange

import "testing"

func TestCount(t *testing.T) {
	changes, pairs := Count([][]int{{0, 1}, {1, 0, 2}, {0, 1, 2, 3, 4}, {}}, 3)
	if len(changes) != 3 || changes[0] != 2 || changes[1] != 2 || changes[2] != 1 {
		t.Errorf("changes = %v", changes)
	}
	if p := pairs[Key(1, 0)]; p.N != 2 || p.W != 1.5 {
		t.Errorf("pair 0,1 = %+v, want N 2, W 1.5", p)
	}
	if p := pairs[Key(2, 1)]; p.N != 1 || p.W != 0.5 {
		t.Errorf("pair 1,2 = %+v, want N 1, W 0.5", p)
	}
	if _, ok := pairs[Key(3, 4)]; ok {
		t.Error("a revision over the limit was counted")
	}
	if got := Logical(pairs[Key(0, 1)], 2, 2, 2); got != 0.75 {
		t.Errorf("Logical = %v, want 0.75", got)
	}
	if got := Logical(pairs[Key(1, 2)], 2, 1, 2); got != 0 {
		t.Errorf("Logical below the minimum = %v, want 0", got)
	}
	if got := Logical(Pair{N: 3, W: 3}, 3, 5, 0); got != 1 {
		t.Errorf("Logical = %v, want at most 1", got)
	}
}
