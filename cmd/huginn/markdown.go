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
	if r.Blast.ExportedAPIChanged() {
		m.printf("\n**Exported API changed:** %s\n", code(r.Blast.APIChanges...))
	}

	m.impact(r.Blast, len(rv.Scope) > 0)
	m.relations(r.Relations)
	m.changes(r)
}

func (m *markdown) impact(b semdiff.BlastRadius, scoped bool) {
	m.printf("\n#### Impact\n\n")
	if b.CallSites == 0 {
		m.printf("No callers outside the change.\n")
		return
	}
	sites := plural(b.CallSites, "call site")
	if b.Approximate {
		sites += " (approx.)"
	}
	m.printf("%s in %s, %s.", sites, plural(len(b.Callers), "declaration"), plural(len(b.Packages), "package"))
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
	for i, cl := range b.Callers {
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

// changes lists the changed symbols and the files, folded away.
func (m *markdown) changes(r *semdiff.Report) {
	if len(r.Changes) > 0 {
		m.printf("\n<details><summary>Changes (%s)</summary>\n\n", plural(r.Symbols, "symbol"))
		for _, ch := range r.Changes {
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
