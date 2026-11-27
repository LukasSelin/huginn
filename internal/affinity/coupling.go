package affinity

import (
	"cmp"
	"fmt"
	"maps"
	"slices"

	"huginn/internal/ast"
	"huginn/internal/cochange"
	"huginn/internal/lineage"
)

const (
	maxOutsideCommit = 20 // commits changing more live symbols say nothing about the ones outside the type
	outsideTop       = 5
)

// coupling reads how the type's methods changed together in the window.
//
// A commit counts for a method when it changed the method itself, not only
// moved it. The commit that added the type is left out, or every method
// born with it would look coupled, and so is a commit that changed more
// than half the methods (at least five), as a receiver rename or a sweep
// does. The rest weigh 1/(n-1) for the n methods they changed.
func (b *build) coupling() {
	r := b.r
	from, to, in := b.a.window(b.opts.Months)
	r.From, r.To = from, to
	n := len(r.Rows)
	limit := max(5, n/2)
	r.Coupling = Coupling{Limit: limit, Min: b.opts.MinCoChanges, Pairs: []Pair{}}

	birth := b.typeBirth()
	byRev := b.changedRows(func(e lineage.Event) bool { return counted(e) && e.SHA != birth && in(e.Time) })
	revs, positions := b.keptRevisions(byRev, limit)
	r.Coupling.Commits = len(revs)

	changes, pairs := cochange.Count(revs, n)
	b.couplingPairs(changes, pairs)
	for nb := range r.Blocks {
		b.blockCoupling(nb, revs, pairs)
	}
	b.outside(revs, positions)
}

// typeBirth returns the commit that added the type, or "" when its history
// does not start with one.
func (b *build) typeBirth() string {
	if ev := b.a.h.Symbol(b.r.Type.ID).Events; len(ev) > 0 && ev[0].Flags.Has(ast.Added) {
		return ev[0].SHA
	}
	return ""
}

// changedRows maps each revision to the rows whose methods it changed, in
// the events keep accepts.
func (b *build) changedRows(keep func(lineage.Event) bool) map[int][]int {
	h := b.a.h
	byRev := map[int][]int{} // revision → rows it changed
	for i, row := range b.r.Rows {
		for _, e := range h.Symbol(row.ID).Events {
			if !keep(e) {
				continue
			}
			if p, ok := h.Position(e.SHA); ok && !slices.Contains(byRev[p], i) {
				byRev[p] = append(byRev[p], i)
			}
		}
	}
	return byRev
}

// keptRevisions returns, in history order, the rows each revision changed
// and its position, leaving out as mass edits those changing more than
// limit rows. It counts the edits of every row in the ones it keeps.
func (b *build) keptRevisions(byRev map[int][]int, limit int) (revs [][]int, positions []int) {
	r := b.r
	for _, p := range slices.Sorted(maps.Keys(byRev)) {
		rows := byRev[p]
		if len(rows) > limit {
			r.Coupling.MassEdits++
			continue
		}
		revs = append(revs, rows)
		positions = append(positions, p)
		for _, i := range rows {
			r.Rows[i].Edits++
		}
	}
	return revs, positions
}

// couplingPairs lists the pairs of rows that changed together, the most
// coupled first, and says why the strong ones across blocks did.
func (b *build) couplingPairs(changes map[int]int, pairs map[[2]int]cochange.Pair) {
	r := b.r
	reach := b.closures()
	for _, k := range slices.SortedFunc(maps.Keys(pairs), func(x, y [2]int) int {
		return cmp.Or(cmp.Compare(x[0], y[0]), cmp.Compare(x[1], y[1]))
	}) {
		x, y, p := k[0], k[1], pairs[k]
		pr := Pair{A: x, B: y, N: p.N, Logical: cochange.Logical(p, changes[x], changes[y], 1), Strong: p.N >= b.opts.MinCoChanges}
		bx, by := b.rowBlock[x], b.rowBlock[y]
		pr.Cross = bx >= 0 && by >= 0 && bx != by
		if pr.Strong && pr.Cross {
			pr.Kind, pr.Text = b.crossKind(x, y, reach)
			pr.Text = fmt.Sprintf("%s (%s) and %s (%s) changed together in %d commits (%.0f%%): %s",
				r.Rows[x].Method, letter(bx), r.Rows[y].Method, letter(by), p.N, 100*pr.Logical, pr.Text)
		}
		r.Coupling.Pairs = append(r.Coupling.Pairs, pr)
	}
	slices.SortStableFunc(r.Coupling.Pairs, func(x, y Pair) int {
		return cmp.Or(cmp.Compare(y.Logical, x.Logical), cmp.Compare(y.N, x.N))
	})
}

// blockCoupling counts block nb's edits and the commits changing it and,
// for two methods or more, how much of its co-change weight stays inside
// it rather than going to other blocks.
func (b *build) blockCoupling(nb int, revs [][]int, pairs map[[2]int]cochange.Pair) {
	r := b.r
	bl := &r.Blocks[nb]
	inBlock := func(i int) bool { return b.rowBlock[i] == nb }
	for _, i := range bl.Rows {
		bl.Edits += r.Rows[i].Edits
	}
	for _, rows := range revs {
		if slices.ContainsFunc(rows, inBlock) {
			bl.Commits++
		}
	}
	var inside, across float64
	for k, p := range pairs {
		x, y := inBlock(k[0]), inBlock(k[1])
		switch {
		case x && y:
			inside += p.W
		case x && b.rowBlock[k[1]] >= 0, y && b.rowBlock[k[0]] >= 0:
			across += p.W
		}
	}
	if inside+across > 0 && len(bl.Rows) >= 2 {
		c := inside / (inside + across)
		bl.Cohesion = &c
	}
}

// crossKind tells why two methods of different blocks change together.
func (b *build) crossKind(x, y int, reach [][]bool) (kind, text string) {
	r := b.r
	switch {
	case reach[x][y] || reach[y][x]:
		from, to := x, y
		if !reach[x][y] {
			from, to = y, x
		}
		return "boundary", fmt.Sprintf("%s reaches %s through its calls; the boundary may be in the wrong place, or the result could pass as a value", r.Rows[from].Method, r.Rows[to].Method)
	}
	for j := range r.Columns {
		if b.cell[x][j].r && b.cell[y][j].r {
			return "duplicated", fmt.Sprintf("both read %s; the same rule may live in both places", r.Columns[j].Name)
		}
	}
	return "concept", "something passes between them that has no type yet; make it a value both use"
}

// outside lists, per block, the symbols elsewhere that changed with it most
// often, from the commits that changed few symbols in all.
func (b *build) outside(revs [][]int, positions []int) {
	r, a := b.r, b.a
	own := map[lineage.ID]bool{r.Type.ID: true}
	for _, row := range r.Rows {
		own[row.ID] = true
	}
	for nb := range r.Blocks {
		count := map[lineage.ID]int{}
		for k, rows := range revs {
			changed := a.revs[positions[k]]
			if len(changed) > maxOutsideCommit || !slices.ContainsFunc(rows, func(i int) bool { return b.rowBlock[i] == nb }) {
				continue
			}
			for _, id := range changed {
				if !own[id] {
					count[id]++
				}
			}
		}
		bl := &r.Blocks[nb]
		for id, n := range count {
			if n < b.opts.MinCoChanges {
				continue
			}
			bl.Outside = append(bl.Outside, Outside{Sym: a.sym(a.h.Symbol(id).Latest()), N: n, Share: float64(n) / float64(max(1, bl.Commits))})
		}
		slices.SortFunc(bl.Outside, func(x, y Outside) int {
			return cmp.Or(cmp.Compare(y.N, x.N), cmp.Compare(y.Share, x.Share), cmp.Compare(x.Name, y.Name), cmp.Compare(x.ID, y.ID))
		})
		bl.Outside = bl.Outside[:min(len(bl.Outside), outsideTop)]
	}
}
