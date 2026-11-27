package cohesion

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
)

// weigh checks each block against the other signals, as combining them
// does: callers that split the same way make a split certain; methods that
// change together across blocks point at a missing concept or a misplaced
// boundary; a word that spans blocks says the names disagree with the
// split. It then picks the block to extract first, one that owns its
// writes exclusively and changes most often, and advises on the type.
func (a *analysis) weigh(r *Report, changes map[string][]string) {
	in := memberBlocks(r.Blocks) // member → its block
	split := 0
	for i := range r.Blocks {
		b := &r.Blocks[i]
		if b.Label == "core" {
			continue
		}
		split++
		weighBlock(r, b, in, changes)
	}
	if start := extractFirst(r.Blocks); start != nil && len(r.Blocks) > 1 {
		r.Start = &Start{Block: start.Label, Reason: startReason(start)}
	}
	r.Advice = append(r.Advice, typeAdvice(r, split)...)
}

// memberBlocks maps each member of the blocks, field or method, to the
// label of its block.
func memberBlocks(blocks []Block) map[string]string {
	in := map[string]string{}
	for _, b := range blocks {
		for _, n := range append(slices.Clone(b.Fields), b.Methods...) {
			in[n] = b.Label
		}
	}
	return in
}

// weighBlock sets what block b owns and how often it changes, collects
// what the names, the callers and the history say about splitting it off,
// and sums that up in its verdict.
func weighBlock(r *Report, b *Block, in map[string]string, changes map[string][]string) {
	b.OwnsWrites = blockOwnsWrites(r, b.Label)
	if changes != nil {
		b.Changes = blockChanges(b.Methods, changes)
	}
	b.Word = blockWord(append(slices.Clone(b.Fields), b.Methods...))
	b.Evidence = append(b.Evidence, namesEvidence(b, r.Words, in)...)
	b.Evidence = append(b.Evidence, clientsEvidence(b.Label, r.Roles)...)
	b.Evidence = append(b.Evidence, historyEvidence(b.Label, r.Coupled, in)...)
	b.Verdict = blockVerdict(b.Evidence)
}

// blockOwnsWrites reports whether no row outside the block labelled label
// writes one of its columns.
func blockOwnsWrites(r *Report, label string) bool {
	for _, m := range r.Methods {
		if m.Block == label {
			continue
		}
		for _, u := range m.Uses {
			if u.Write && r.Fields[u.Field].Block == label {
				return false
			}
		}
	}
	return true
}

// blockChanges counts the commits that changed any of methods.
func blockChanges(methods []string, changes map[string][]string) int {
	seen := map[string]bool{}
	for _, m := range methods {
		for _, sha := range changes[m] {
			seen[sha] = true
		}
	}
	return len(seen)
}

// namesEvidence is what the names say about block b: they agree when most
// of its members share a word, and disagree for each other block that a
// word names two or more members of, as it does of b.
func namesEvidence(b *Block, ws []Word, in map[string]string) []Evidence {
	var out []Evidence
	if b.Word != "" {
		out = append(out, Evidence{"names", "agrees", fmt.Sprintf("Most of its members are named with %q: the split matches the vocabulary.", b.Word)})
	}
	for _, w := range ws {
		by := map[string]int{}
		for _, m := range w.Members {
			if l := in[m]; l != "" && l != "core" {
				by[l]++
			}
		}
		if by[b.Label] < 2 {
			continue
		}
		for l, n := range by {
			if l != b.Label && n >= 2 {
				out = append(out, Evidence{"names", "disagrees", fmt.Sprintf("%q names members of blocks %s and %s: the code is ahead of the names, so rename first.", w.Word, b.Label, l)})
			}
		}
	}
	return out
}

// clientsEvidence is what the callers say about the block labelled label:
// a role using only it agrees, and one using it with other blocks is a use
// case, noted.
func clientsEvidence(label string, roles []Role) []Evidence {
	var out []Evidence
	for _, ro := range roles {
		switch {
		case len(ro.Blocks) == 1 && ro.Blocks[0] == label:
			out = append(out, Evidence{"clients", "agrees", fmt.Sprintf("%s %s only this block: the callers split the same way.", list(ro.Clients), verb(ro.Clients, "uses", "use"))})
		case slices.Contains(ro.Blocks, label):
			out = append(out, Evidence{"clients", "note", fmt.Sprintf("%s %s it with blocks %s: a use case, which a facade or coordinator serves better than splitting along its lines.", list(ro.Clients), verb(ro.Clients, "uses", "use"), strings.Join(others(ro.Blocks, label), ", "))})
		}
	}
	return out
}

// historyEvidence is what the history says about the block labelled
// label: each pair of methods changed together across it and another block
// disagrees.
func historyEvidence(label string, coupled []Coupling, in map[string]string) []Evidence {
	var out []Evidence
	for _, c := range coupled {
		if !c.Across || in[c.A] != label && in[c.B] != label {
			continue
		}
		other := in[c.B]
		if other == label {
			other = in[c.A]
		}
		out = append(out, Evidence{"history", "disagrees", fmt.Sprintf("%s and %s changed together in %d commits, across blocks %s and %s: a concept missing between them, a boundary in the wrong place, or one rule written twice.", c.A, c.B, c.Together, label, other)})
	}
	return out
}

// blockVerdict sums up a block's evidence: "check" when a signal
// disagrees, "extract" when the callers agree, and "candidate" otherwise.
func blockVerdict(evidence []Evidence) string {
	switch {
	case slices.ContainsFunc(evidence, func(e Evidence) bool { return e.Stance == "disagrees" }):
		return "check"
	case slices.ContainsFunc(evidence, func(e Evidence) bool { return e.Signal == "clients" && e.Stance == "agrees" }):
		return "extract"
	}
	return "candidate"
}

// extractFirst picks the block to start with: it owns its writes, and of
// those it changes most often. Entity state stays with the core entity, so
// its blocks come into question only when there is nothing else to move
// out.
func extractFirst(blocks []Block) *Block {
	var start *Block
	for i := range blocks {
		b := &blocks[i]
		if b.Label == "core" || !b.OwnsWrites {
			continue
		}
		if start == nil || cmp.Or(cmp.Compare(movableBlock(b), movableBlock(start)), cmp.Compare(b.Changes, start.Changes), cmp.Compare(len(b.Methods), len(start.Methods))) > 0 {
			start = b
		}
	}
	return start
}

// movableBlock ranks the blocks of entity state below the others.
func movableBlock(b *Block) int {
	if b.Kind == "entity" {
		return 0
	}
	return 1
}

// startReason says why start is the block to extract first.
func startReason(start *Block) string {
	reason := "it owns its writes exclusively"
	if start.Changes > 0 {
		reason += fmt.Sprintf(" and changes most often (%s)", plural(start.Changes, "commit"))
	}
	if start.Verdict == "extract" {
		reason += ", and its callers split the same way"
	}
	return reason + "."
}

// typeAdvice is what the signals say together about the whole type, which
// falls into split blocks besides the core.
func typeAdvice(r *Report, split int) []string {
	var out []string
	if r.LCOM4 == 1 && r.LCOM4Split > 1 {
		out = append(out, fmt.Sprintf("LCOM4 is 1 only through hubs and orchestration: without them the type falls into %d groups.", r.LCOM4Split))
	}
	if split <= 1 && len(r.Roles) > 1 {
		out = append(out, fmt.Sprintf("The fields hang together, but callers use it in %d separate roles: segregate interfaces on the callers' side and keep the implementation together.", len(r.Roles)))
	}
	if ties := len(r.Ties); ties > 0 {
		out = append(out, fmt.Sprintf("Split the %s spanning blocks with Extract Method first, then redraw the matrix: long methods are often what ties the blocks together.", plural(ties, "row")))
	}
	return out
}

// blockWord returns the word most of a block's members share, if any.
func blockWord(members []string) string {
	count := map[string]int{}
	for _, n := range members {
		var seen []string
		for _, w := range words(n) {
			if len(w) >= 3 && !stopWords[w] && !slices.Contains(seen, w) {
				seen = append(seen, w)
				count[w]++
			}
		}
	}
	best, n := "", 0
	need := max(2, (len(members)+1)/2)
	for w, c := range count {
		if c >= need && (c > n || c == n && w < best) {
			best, n = w, c
		}
	}
	return best
}

func others(xs []string, not string) []string {
	return slices.DeleteFunc(slices.Clone(xs), func(x string) bool { return x == not })
}

func list(xs []string) string {
	if len(xs) > 3 {
		return strings.Join(xs[:3], ", ") + fmt.Sprintf(" and %d more", len(xs)-3)
	}
	return strings.Join(xs, ", ")
}
