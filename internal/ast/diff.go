package ast

import (
	"cmp"
	"slices"
	"strings"
)

// ChangeFlags describes how a symbol changed between two revisions. A single
// change can carry several flags, e.g. Renamed|BodyChanged.
type ChangeFlags uint8

const (
	Added ChangeFlags = 1 << iota
	Deleted
	Renamed
	Moved // declared in a different file or package
	SignatureChanged
	BodyChanged
)

var flagNames = []string{"added", "deleted", "renamed", "moved", "signature", "body"}

func (f ChangeFlags) Has(x ChangeFlags) bool { return f&x == x }

func (f ChangeFlags) String() string {
	var parts []string
	for i, name := range flagNames {
		if f&(1<<i) != 0 {
			parts = append(parts, name)
		}
	}
	return strings.Join(parts, "|")
}

// Change is one symbol-level difference. Old is nil for Added, New is nil for
// Deleted.
type Change struct {
	Flags      ChangeFlags
	Old, New   *Symbol
	Similarity float64 // body similarity of a fuzzy match; 1 for matches by name

	// Members are the member-level changes of a struct or interface type
	// whose definition changed, as DiffMembers reports them.
	Members []MemberChange
}

// RenameThreshold is the minimum body similarity for two differently named
// symbols to be treated as one symbol that was renamed or moved.
const RenameThreshold = 0.7

// minShingles is the body size below which fuzzy matching is not trusted:
// small bodies such as `return x.n` look alike across unrelated symbols.
const minShingles = 6

// Diff compares the symbols of two revisions of a set of files, usually the
// files a commit touched, and returns the semantic changes between them.
// Edits that leave the syntax tree unchanged (formatting, comments) produce
// no changes.
func Diff(old, new []*File) []Change {
	oldSyms, newSyms := collect(old), collect(new)
	matchedOld := make([]bool, len(oldSyms))
	var changes []Change
	var unmatchedNew []*Symbol

	// Pass 1: same package and key. Prefer a candidate in the same file, so
	// that repeated names (init) pair up with their own file's declaration.
	byID := map[string][]int{}
	for i, s := range oldSyms {
		byID[symbolID(s)] = append(byID[symbolID(s)], i)
	}
	for _, n := range newSyms {
		best := -1
		for _, i := range byID[symbolID(n)] {
			if matchedOld[i] || oldSyms[i].Kind != n.Kind {
				continue
			}
			if best < 0 || oldSyms[i].Path == n.Path {
				best = i
			}
		}
		if best < 0 {
			unmatchedNew = append(unmatchedNew, n)
			continue
		}
		matchedOld[best] = true
		if f := compare(oldSyms[best], n); f != 0 {
			changes = append(changes, matched(f, oldSyms[best], n, 1))
		}
	}

	// Pass 2: pair leftovers of the same kind by body similarity, best first.
	type pair struct {
		o, n  int
		score float64
	}
	var pairs []pair
	for i, o := range oldSyms {
		if matchedOld[i] {
			continue
		}
		for j, n := range unmatchedNew {
			if o.Kind != n.Kind {
				continue
			}
			if score := matchScore(o, n); score >= RenameThreshold {
				pairs = append(pairs, pair{i, j, score})
			}
		}
	}
	slices.SortStableFunc(pairs, func(a, b pair) int { return cmp.Compare(b.score, a.score) })
	matchedNew := make([]bool, len(unmatchedNew))
	for _, p := range pairs {
		if matchedOld[p.o] || matchedNew[p.n] {
			continue
		}
		matchedOld[p.o], matchedNew[p.n] = true, true
		o, n := oldSyms[p.o], unmatchedNew[p.n]
		changes = append(changes, matched(compare(o, n), o, n, p.score))
	}

	// Pass 3: whatever is left was added or deleted.
	for j, n := range unmatchedNew {
		if !matchedNew[j] {
			changes = append(changes, Change{Flags: Added, New: n})
		}
	}
	for i, o := range oldSyms {
		if !matchedOld[i] {
			changes = append(changes, Change{Flags: Deleted, Old: o})
		}
	}

	slices.SortStableFunc(changes, func(a, b Change) int {
		sa, sb := a.New, b.New
		if sa == nil {
			sa = a.Old
		}
		if sb == nil {
			sb = b.Old
		}
		return cmp.Or(cmp.Compare(sa.Path, sb.Path), cmp.Compare(sa.StartLine, sb.StartLine), cmp.Compare(sa.Key, sb.Key))
	})
	return changes
}

// matched builds the change of a symbol found on both sides.
func matched(f ChangeFlags, o, n *Symbol, sim float64) Change {
	c := Change{Flags: f, Old: o, New: n, Similarity: sim}
	if f.Has(BodyChanged) {
		c.Members = DiffMembers(o, n)
	}
	return c
}

func collect(files []*File) []*Symbol {
	var out []*Symbol
	for _, f := range files {
		if f != nil {
			out = append(out, f.Symbols...)
		}
	}
	return out
}

// symbolID is the symbol's identity within a package, ignoring which file
// declares it.
func symbolID(s *Symbol) string {
	return s.Package + "." + strings.SplitN(s.Key, "#", 2)[0]
}

func compare(o, n *Symbol) ChangeFlags {
	var f ChangeFlags
	if o.Name != n.Name || o.Recv != n.Recv {
		f |= Renamed
	}
	if o.Path != n.Path || o.Package != n.Package {
		f |= Moved
	}
	if o.sigKey != n.sigKey || o.PtrRecv != n.PtrRecv {
		f |= SignatureChanged
	}
	if o.BodyHash != n.BodyHash {
		f |= BodyChanged
	}
	return f
}

// matchScore rates how likely two differently named symbols are the same
// symbol. Small bodies only match when body, signature and receiver are
// identical.
func matchScore(o, n *Symbol) float64 {
	if len(o.shingles) < minShingles || len(n.shingles) < minShingles {
		if o.BodyHash == n.BodyHash && o.sigKey == n.sigKey && o.Recv == n.Recv && len(o.shingles) > 0 {
			return 1
		}
		return 0
	}
	if o.BodyHash == n.BodyHash {
		return 1
	}
	return similarity(o, n)
}
