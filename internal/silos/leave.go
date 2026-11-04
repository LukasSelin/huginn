package silos

import (
	"fmt"
	"strings"

	"huginn/internal/knowledge"
)

// Departure is what would be left if some authors left at the last
// revision: the symbols and packages nobody around would know enough of,
// and those that would rest on one person. Ranked by what is lost and how
// much depends on it, it is the list of what to hand over first.
type Departure struct {
	Leaving []string // the authors named, as they commit
	Unknown []string // the names that match no author

	// Share is how much of what is known of the live code they hold.
	Share float64

	// Highest score first, at most Options.Top of each.
	Symbols, Packages []Loss

	// How many symbols and packages would be orphaned, and how many would
	// newly rest on one person, before the lists are cut.
	OrphanedSymbols, SingleSymbols, OrphanedPackages, SinglePackages int
}

// Loss is one unit the departure leaves orphaned or resting on one person.
type Loss struct {
	Unit
	Before, After Spread
	Dependence
	Lost float64 // the share of it the leavers hold

	// Orphaned is set when the authors left would know less than
	// Options.MinKnown of it; otherwise its bus factor drops to 1, or the
	// one person it rests on changes.
	Orphaned bool

	Score   float64 // Lost × Criticality
	Reasons []string
}

// Leave reports what would be left if the authors called names left. A
// name matches an author exactly or, failing that, ignoring case.
func (r *Report) Leave(names ...string) *Departure {
	a := r.a
	d := &Departure{Leaving: []string{}, Unknown: []string{}, Symbols: []Loss{}, Packages: []Loss{}}
	leaving := map[int]bool{}
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		i, ok := a.author(n)
		if !ok {
			d.Unknown = append(d.Unknown, n)
			continue
		}
		if !leaving[i] {
			leaving[i] = true
			d.Leaving = append(d.Leaving, a.model.Authors()[i])
		}
	}
	after := func(i int) bool { return leaving[i] || a.departed(i) }

	total, held := 0.0, 0.0
	for _, u := range a.syms {
		total += knowledge.Total(u.ws)
		for _, w := range u.ws {
			if leaving[w.Author] && !a.departed(w.Author) {
				held += w.W
			}
		}
	}
	if total > 0 {
		d.Share = held / total
	}
	who := strings.Join(d.Leaving, ", ")
	d.Symbols, d.OrphanedSymbols, d.SingleSymbols = a.losses(a.syms, after, who)
	d.Packages, d.OrphanedPackages, d.SinglePackages = a.losses(a.pkgs, after, who)
	return d
}

// author returns the index of the author called name.
func (a *analysis) author(name string) (int, bool) {
	if i, ok := a.model.Author(name); ok {
		return i, true
	}
	for i, n := range a.model.Authors() {
		if strings.EqualFold(n, name) {
			return i, true
		}
	}
	return 0, false
}

// losses ranks what of us the departure leaves orphaned or resting on one
// person, returning the top ones and how many of each kind there are.
func (a *analysis) losses(us []*unit, after func(int) bool, who string) (out []Loss, orphaned, single int) {
	out = []Loss{}
	for _, u := range us {
		b, af := a.spread(u.ws, a.departed), a.spread(u.ws, after)
		lost := b.Known - af.Known
		if lost <= 1e-9 {
			continue
		}
		l := Loss{Unit: u.Unit, Before: b, After: af, Dependence: u.dep, Lost: lost, Score: lost * u.dep.Criticality()}
		switch {
		case af.Known < a.opts.MinKnown && b.Known >= a.opts.MinKnown:
			l.Orphaned = true
			orphaned++
		case af.Known >= a.opts.MinKnown && af.BusFactor == 1 && (b.BusFactor > 1 || b.Holder != af.Holder):
			single++
		default:
			continue
		}
		l.Reasons = append(a.lossReasons(l, who), u.dep.reasons()...)
		out = append(out, l)
	}
	sortBy(out, func(l Loss) (float64, string) { return l.Score, l.key() })
	return out[:min(len(out), a.opts.Top)], orphaned, single
}

func (a *analysis) lossReasons(l Loss, who string) []string {
	verb := "holds"
	if strings.Contains(who, ", ") {
		verb = "hold"
	}
	out := []string{fmt.Sprintf("%s %s %s", who, verb, pct(l.Lost))}
	switch {
	case l.After.Holder == "":
		out = append(out, "nobody left knows it")
	case l.Orphaned:
		out = append(out, fmt.Sprintf("only %s stays known; %s knows the most (%s)", pct(l.After.Known), l.After.Holder, pct(l.After.Held)))
	case l.Before.BusFactor > 1:
		out = append(out, fmt.Sprintf("bus factor %d → 1: hand over to %s (%s)", l.Before.BusFactor, l.After.Holder, pct(l.After.Held)))
	default:
		out = append(out, fmt.Sprintf("would rest on %s alone (%s)", l.After.Holder, pct(l.After.Held)))
	}
	return out
}
