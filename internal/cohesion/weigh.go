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
	in := map[string]string{} // member → its block
	for _, b := range r.Blocks {
		for _, n := range append(slices.Clone(b.Fields), b.Methods...) {
			in[n] = b.Label
		}
	}
	split := 0
	for i := range r.Blocks {
		b := &r.Blocks[i]
		if b.Label == "core" {
			continue
		}
		split++
		b.OwnsWrites = true
		for _, m := range r.Methods {
			if m.Block == b.Label {
				continue
			}
			for _, u := range m.Uses {
				if u.Write && r.Fields[u.Field].Block == b.Label {
					b.OwnsWrites = false
				}
			}
		}
		if changes != nil {
			seen := map[string]bool{}
			for _, m := range b.Methods {
				for _, sha := range changes[m] {
					seen[sha] = true
				}
			}
			b.Changes = len(seen)
		}
		b.Word = blockWord(append(slices.Clone(b.Fields), b.Methods...))
		if b.Word != "" {
			b.Evidence = append(b.Evidence, Evidence{"names", "agrees", fmt.Sprintf("Most of its members are named with %q: the split matches the vocabulary.", b.Word)})
		}
		for _, w := range r.Words {
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
					b.Evidence = append(b.Evidence, Evidence{"names", "disagrees", fmt.Sprintf("%q names members of blocks %s and %s: the code is ahead of the names, so rename first.", w.Word, b.Label, l)})
				}
			}
		}
		for _, ro := range r.Roles {
			switch {
			case len(ro.Blocks) == 1 && ro.Blocks[0] == b.Label:
				b.Evidence = append(b.Evidence, Evidence{"clients", "agrees", fmt.Sprintf("%s %s only this block: the callers split the same way.", list(ro.Clients), verb(ro.Clients, "uses", "use"))})
			case slices.Contains(ro.Blocks, b.Label):
				b.Evidence = append(b.Evidence, Evidence{"clients", "note", fmt.Sprintf("%s %s it with blocks %s: a use case, which a facade or coordinator serves better than splitting along its lines.", list(ro.Clients), verb(ro.Clients, "uses", "use"), strings.Join(others(ro.Blocks, b.Label), ", "))})
			}
		}
		for _, c := range r.Coupled {
			if !c.Across || in[c.A] != b.Label && in[c.B] != b.Label {
				continue
			}
			other := in[c.B]
			if other == b.Label {
				other = in[c.A]
			}
			b.Evidence = append(b.Evidence, Evidence{"history", "disagrees", fmt.Sprintf("%s and %s changed together in %d commits, across blocks %s and %s: a concept missing between them, a boundary in the wrong place, or one rule written twice.", c.A, c.B, c.Together, b.Label, other)})
		}
		b.Verdict = "candidate"
		switch {
		case slices.ContainsFunc(b.Evidence, func(e Evidence) bool { return e.Stance == "disagrees" }):
			b.Verdict = "check"
		case slices.ContainsFunc(b.Evidence, func(e Evidence) bool { return e.Signal == "clients" && e.Stance == "agrees" }):
			b.Verdict = "extract"
		}
	}

	// The block to start with: it owns its writes, and of those it changes
	// most often. Entity state stays with the core entity, so its blocks
	// come into question only when there is nothing else to move out.
	var start *Block
	movable := func(b *Block) int {
		if b.Kind == "entity" {
			return 0
		}
		return 1
	}
	for i := range r.Blocks {
		b := &r.Blocks[i]
		if b.Label == "core" || !b.OwnsWrites {
			continue
		}
		if start == nil || cmp.Or(cmp.Compare(movable(b), movable(start)), cmp.Compare(b.Changes, start.Changes), cmp.Compare(len(b.Methods), len(start.Methods))) > 0 {
			start = b
		}
	}
	if start != nil && len(r.Blocks) > 1 {
		reason := "it owns its writes exclusively"
		if start.Changes > 0 {
			reason += fmt.Sprintf(" and changes most often (%s)", plural(start.Changes, "commit"))
		}
		if start.Verdict == "extract" {
			reason += ", and its callers split the same way"
		}
		r.Start = &Start{Block: start.Label, Reason: reason + "."}
	}

	if r.LCOM4 == 1 && r.LCOM4Split > 1 {
		r.Advice = append(r.Advice, fmt.Sprintf("LCOM4 is 1 only through hubs and orchestration: without them the type falls into %d groups.", r.LCOM4Split))
	}
	if split <= 1 && len(r.Roles) > 1 {
		r.Advice = append(r.Advice, fmt.Sprintf("The fields hang together, but callers use it in %d separate roles: segregate interfaces on the callers' side and keep the implementation together.", len(r.Roles)))
	}
	if ties := len(r.Ties); ties > 0 {
		r.Advice = append(r.Advice, fmt.Sprintf("Split the %s spanning blocks with Extract Method first, then redraw the matrix: long methods are often what ties the blocks together.", plural(ties, "row")))
	}
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
