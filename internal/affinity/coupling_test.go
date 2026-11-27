package affinity

import (
	"cmp"
	"maps"
	"slices"
	"testing"

	"huginn/internal/cochange"
)

func TestBlockCohesionOrder(t *testing.T) {
	// Rows 0 to 4 are block 0, rows 5 and 6 block 1. Added up in different
	// orders, the weights inside block 0 come to four different floats, so
	// a sum in map order would change from run to run.
	rowBlock := []int{0, 0, 0, 0, 0, 1, 1}
	inside := map[[2]int]float64{
		{0, 1}: 1, {0, 2}: 1.0 / 3, {1, 2}: 1.0 / 3, {1, 3}: 1.0 / 5,
		{2, 3}: 1.0 / 7, {2, 4}: 1.0 / 2, {3, 4}: 1.0 / 6,
	}
	across := map[[2]int]float64{{0, 5}: 1.0 / 3, {4, 6}: 1.0 / 7, {5, 6}: 1}
	pairs := map[[2]int]cochange.Pair{}
	for k, w := range inside {
		pairs[k] = cochange.Pair{N: 1, W: w}
	}
	for k, w := range across {
		pairs[k] = cochange.Pair{N: 1, W: w}
	}
	keys := slices.SortedFunc(maps.Keys(pairs), func(x, y [2]int) int {
		return cmp.Or(cmp.Compare(x[0], y[0]), cmp.Compare(x[1], y[1]))
	})

	// The weights added in key order. {5, 6} lies wholly in block 1, so it
	// counts for neither side of block 0.
	var in, out float64
	for _, k := range keys {
		if w, ok := inside[k]; ok {
			in += w
		} else if k != [2]int{5, 6} {
			out += across[k]
		}
	}
	want := in / (in + out)

	for range 200 {
		got, ok := blockCohesion(rowBlock, 0, keys, pairs)
		if !ok || got != want {
			t.Fatalf("blockCohesion = %v, %v; want %v, true", got, ok, want)
		}
	}
	if _, ok := blockCohesion(rowBlock, 2, keys, pairs); ok {
		t.Error("a block without rows has a cohesion")
	}
}
