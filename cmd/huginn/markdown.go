package main

import (
	"fmt"
	"io"
	"strings"

	"huginn/internal/review"
	"huginn/internal/semdiff"
)

// markdown writes a merge request's report as Markdown, to be posted as a
// comment on it, remembering the first write error. The changes and files
// are folded away, since the merge request shows the diff already.
type markdown struct {
	w          io.Writer
	maxCallers int
	err        error
}

func (m *markdown) printf(format string, args ...any) {
	if m.err == nil {
		_, m.err = fmt.Fprintf(m.w, format, args...)
	}
}

func (m *markdown) review(rv *review.Review) {
	r := rv.Report
	where := "the whole repository"
	if len(rv.Scope) > 0 {
		where = code(rv.Scope...)
	}
	m.printf("### huginn review of %s\n\n", where)
	m.printf("%s into %s, from merge base %.7s · %s\n\n", code(rv.HeadName()), code(rv.Target),
		rv.MergeBase, plural(len(rv.Commits), "commit"))
	m.printf("**%s**", r.Headline())
	var counts []string
	for _, k := range semdiff.Kinds {
		if n := r.Counts[k]; n > 0 {
			counts = append(counts, fmt.Sprintf("%s %d", k, n))
		}
	}
	if len(counts) > 0 {
		m.printf("  \n%s", strings.Join(counts, " · "))
	}
	m.printf("\n")

	m.focus(rv)
	m.impact(r.Blast, len(rv.Scope) > 0)
	m.relations(r.Relations)
	m.types(rv.Types)
	m.changes(rv)
}

// types lists what the change does to the affinity matrix of each struct
// type it touches, warnings first.
func (m *markdown) types(ts []review.TypeReview) {
	if len(ts) == 0 {
		return
	}
	m.printf("\n#### Affinity matrix of the types it touches\n\n")
	for _, t := range ts {
		m.printf("- %s at %s: %s\n", code(t.Name), code(fmt.Sprintf("%s:%d", t.Path, t.Line)), matrixSummary(t))
		for _, n := range t.Notes {
			if n.Warn {
				m.printf("  - **%s**\n", n.Text)
			} else {
				m.printf("  - %s\n", n.Text)
			}
		}
		if r := t.After; r != nil && r.Start != nil {
			m.printf("  - Start with block %s: %s\n", r.Start.Block, r.Start.Reason)
		}
	}
}

// focus lists what to look at first, exported API it breaks first.
func (m *markdown) focus(rv *review.Review) {
	m.printf("\n#### Look here first\n\n")
	shown, more := shownFocus(rv.Focus)
	if len(shown) == 0 {
		m.printf("Nothing beyond new code: no existing code is edited and no import between packages starts.\n")
		return
	}
	for _, f := range shown {
		name := code(f.Name)
		if f.Kind == review.FocusDependency {
			from, to, _ := strings.Cut(f.Name, " → ")
			name = code(from) + " → " + code(to)
		}
		where := ""
		if loc := at(f); loc != "" {
			where = " at " + code(loc)
		}
		verdict := ""
		if ps := rv.PathsOf(f.Change); ps != nil {
			verdict = mdText(ps.Verdict) + " — "
		}
		m.printf("- **%s** %s%s: %s%s\n", focusLabel(f.Kind), name, where, verdict, focusDetail(f, rv.Report, func(s string) string { return code(s) }))
	}
	if more > 0 {
		m.printf("- … and %s of existing code under Changes\n", plural(more, "more edit"))
	}
}

func (m *markdown) impact(b semdiff.BlastRadius, scoped bool) {
	m.printf("\n#### Impact\n\n")
	prod, tests := sumCallers(b.Callers, isProduction), sumCallers(b.Callers, isTest)
	if prod.decls == 0 {
		if tests.decls == 0 {
			m.printf("No callers outside the change.\n")
		} else {
			m.printf("No callers outside tests; %s in tests.\n", plural(tests.sites, "call site"))
		}
		return
	}
	sites := plural(prod.sites, "call site")
	if prod.approx {
		sites += " (approx.)"
	}
	m.printf("%s in %s, %s.", sites, plural(prod.decls, "declaration"), plural(len(prod.pkgs), "package"))
	if tests.decls > 0 {
		m.printf(" %d more in tests.", tests.sites)
	}
	if scoped {
		if out := outside(b.Callers); out.decls == 0 {
			m.printf(" None outside the scope.")
		} else {
			m.printf(" **Outside the scope: %s in %s, %s** (%s).", plural(out.sites, "call site"),
				plural(out.decls, "declaration"), plural(len(out.pkgs), "package"), code(out.pkgs...))
		}
	}
	m.printf("\n\n| Caller | Where | Uses |\n| --- | --- | --- |\n")
	limit := 3 * m.maxCallers
	for i, cl := range productionFirst(b.Callers) {
		if limit > 0 && i == limit {
			m.printf("| … and %d more | | |\n", len(b.Callers)-limit)
			break
		}
		var notes []string
		if cl.Approximate {
			notes = append(notes, "approx.")
		}
		if cl.Test {
			notes = append(notes, "test")
		}
		if cl.Outside {
			notes = append(notes, "**outside**")
		}
		note := ""
		if len(notes) > 0 {
			note = " " + strings.Join(notes, ", ")
		}
		m.printf("| %s | %s | ×%d%s |\n", code(cl.Name), code(fmt.Sprintf("%s:%d", cl.Path, cl.Line)), cl.Calls, note)
	}
}

func (m *markdown) relations(rel semdiff.Relations) {
	m.printf("\n#### Relations\n\n")
	if rel.Empty() {
		m.printf("No imports or references between packages started or stopped.\n")
		return
	}
	if len(rel.NewPackages) > 0 {
		m.printf("New packages: %s  \n", code(rel.NewPackages...))
	}
	if len(rel.GonePackages) > 0 {
		m.printf("Removed packages: %s  \n", code(rel.GonePackages...))
	}
	if len(rel.NewPackages)+len(rel.GonePackages) > 0 {
		m.printf("\n")
	}
	added, addedAlong := along(rel.AddedDeps, rel.AddedRefs)
	removed, removedAlong := along(rel.RemovedDeps, rel.RemovedRefs)
	for i, d := range rel.AddedDeps {
		m.dependency("+", "new import", d, added[i])
	}
	for i, d := range rel.RemovedDeps {
		m.dependency("−", "import removed", d, removed[i])
	}
	if len(addedAlong)+len(removedAlong) > 0 {
		m.printf("- Along imports that stay\n")
		limit := 3 * m.maxCallers
		m.references("  - + ", addedAlong, limit)
		m.references("  - − ", removedAlong, limit)
	}
}

func (m *markdown) dependency(mark, what string, d semdiff.Dependency, refs []semdiff.Reference) {
	note := ""
	switch d.Crossing {
	case semdiff.Outward:
		note = ", **out of the scope**"
	case semdiff.Inward:
		note = ", **into the scope**"
	case semdiff.Within:
	}
	m.printf("- **%s** %s → %s (%s%s)\n", mark, code(d.From), code(d.To), what, note)
	if len(refs) == 0 {
		m.printf("  - imported in %s\n", code(d.Files...))
		return
	}
	m.references("  - ", refs, m.maxCallers)
}

func (m *markdown) references(prefix string, refs []semdiff.Reference, limit int) {
	for i, r := range refs {
		if limit > 0 && i == limit {
			m.printf("%s… and %d more\n", prefix, len(refs)-limit)
			return
		}
		note := ""
		if r.Approximate {
			note = " (approx.)"
		}
		m.printf("%s%s → %s ×%d%s at %s\n", prefix, code(r.From), code(r.To), r.Uses, note, code(fmt.Sprintf("%s:%d", r.Path, r.Line)))
	}
}

// changes lists the changed symbols, with what each does to its
// function's paths, and the files, folded away.
func (m *markdown) changes(rv *review.Review) {
	r := rv.Report
	if len(r.Changes) > 0 {
		m.printf("\n<details><summary>Changes (%s)</summary>\n\n", plural(r.Symbols, "symbol"))
		for i, ch := range r.Changes {
			impact := ch.Impact
			if ch.APIChange {
				impact += " · **API change**"
			}
			m.printf("- **%s** %s %s at %s", kinds(ch.Kinds), ch.Symbol, code(ch.Name), code(fmt.Sprintf("%s:%d", ch.Path, ch.Line)))
			if ch.OldName != "" {
				m.printf(", was %s", code(ch.OldName))
			}
			if ch.Summary != "" {
				m.printf(" · %s", ch.Summary)
			}
			m.printf("  \n  %s\n", impact)
			if ch.OldSignature != "" && ch.NewSignature != "" && ch.OldSignature != ch.NewSignature {
				m.printf("  ```diff\n  - %s\n  + %s\n  ```\n", ch.OldSignature, ch.NewSignature)
			}
			m.flow(rv, i)
		}
		m.printf("\n</details>\n")
	}
	if len(r.Files) > 0 {
		m.printf("\n<details><summary>Files (%d)</summary>\n\n| File | Lines | Changes |\n| --- | --- | --- |\n", len(r.Files))
		for _, f := range r.Files {
			name := code(f.Path)
			if f.OldPath != "" {
				name = code(f.OldPath) + " → " + name
			}
			what := ""
			if f.Changes > 0 {
				what = plural(f.Changes, "symbol") + ": " + kinds(f.Kinds)
			}
			m.printf("| %s | +%d −%d | %s |\n", name, f.Added, f.Removed, what)
		}
		m.printf("\n</details>\n")
	}
}

// code formats each of ss as inline code, separated by commas.
func code(ss ...string) string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = "`" + strings.ReplaceAll(s, "`", "'") + "`"
	}
	return strings.Join(out, ", ")
}
