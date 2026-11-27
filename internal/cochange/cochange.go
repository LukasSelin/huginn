// Package cochange counts how often units of code change in the same
// commits: the logical coupling that history shows and references may not.
package cochange

// Pair is how often two units changed together.
type Pair struct {
	N int     // commits changing both
	W float64 // the same commits, each weighed 1/(n-1) for the n units it changed
}

// Key returns the key of the pair of units a and b in the pairs Count
// returns: the smaller first.
func Key(a, b int) [2]int { return [2]int{min(a, b), max(a, b)} }

// Count counts, over the revisions in revs that changed at least one and at
// most limit units, how often each unit changed and how often each pair of
// units changed together. Each revision lists the units it changed, each
// once. A commit changing many units says little about any two of them, so
// each co-change weighs 1/(n-1) for the n units its commit changed.
func Count(revs [][]int, limit int) (changes map[int]int, pairs map[[2]int]Pair) {
	changes, pairs = map[int]int{}, map[[2]int]Pair{}
	for _, us := range revs {
		n := len(us)
		if n == 0 || n > limit {
			continue
		}
		for _, u := range us {
			changes[u]++
		}
		for i := range us {
			for j := i + 1; j < n; j++ {
				k := Key(us[i], us[j])
				p := pairs[k]
				p.N++
				p.W += 1 / float64(n-1)
				pairs[k] = p
			}
		}
	}
	return changes, pairs
}

// Logical rates a pair's coupling from 0 to 1: its weighted co-changes
// against the changes of the less often changed of the two, a and b being
// how often each changed. It is 1 when every change to that one came with
// the other alone, and 0 below minN co-changes.
func Logical(p Pair, a, b, minN int) float64 {
	if p.N == 0 || p.N < minN {
		return 0
	}
	return min(1, p.W/float64(min(a, b)))
}
