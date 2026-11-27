package main

import (
	"strings"
	"unicode/utf8"

	"huginn/internal/review"
)

// stepWidth is how many characters of a step the reports print.
const stepWidth = 100

// clipText shortens s to stepWidth characters.
func clipText(s string) string {
	if utf8.RuneCountInString(s) <= stepWidth {
		return s
	}
	return string([]rune(s)[:stepWidth-1]) + "…"
}

// stepMark marks a step of a compared flow by how it changed.
func stepMark(m review.StepMark) string {
	switch m {
	case review.StepSame:
		return " "
	case review.StepRenested:
		return "~"
	case review.StepEdited:
		return "*"
	case review.StepAdded:
		return "+"
	case review.StepRemoved:
		return "-"
	case review.StepMoved, review.StepMovedEdited:
		return ">"
	}
	return "?"
}

// stepNote says what a mark leaves out: "(re-nested)", "(was: …)",
// "(in files, was: …)".
func stepNote(s review.PathStep) string {
	switch s.Mark {
	case review.StepRenested:
		return "  (re-nested)"
	case review.StepEdited:
		return "  (was: " + clipText(s.Old) + ")"
	case review.StepMoved:
		return "  (in " + s.To + ")"
	case review.StepMovedEdited:
		return "  (in " + s.To + ", was: " + clipText(s.Old) + ")"
	case review.StepSame, review.StepAdded, review.StepRemoved:
	}
	return ""
}

// exitMark marks a way out by how it changed.
func exitMark(m review.ExitMark) string {
	switch m {
	case review.ExitSame:
		return " "
	case review.ExitNew:
		return "+"
	case review.ExitGone:
		return "-"
	case review.ExitChanged:
		return "~"
	}
	return "?"
}

// exitWhen writes the decisions a way out is reached under, "  ⇐ a ∧ b",
// or nothing for one at the top of the function.
func exitWhen(e review.Exit) string {
	if len(e.When) == 0 {
		return ""
	}
	return "  ⇐ " + clipText(strings.Join(e.When, " ∧ "))
}

// flowLines writes what to show of a compared flow, one line a step with
// its mark, indented by depth, after a line for the steps a hunk skips and
// the decisions enclosing it.
func flowLines(ps *review.Paths) []string {
	var out []string
	for _, h := range ps.Hunks() {
		if h.Skipped > 0 {
			out = append(out, "  … "+plural(h.Skipped, "unchanged step"))
		}
		if len(h.Breadcrumb) > 0 {
			out = append(out, "  in "+clipText(strings.Join(h.Breadcrumb, " › ")))
		}
		for _, s := range h.Steps {
			out = append(out, stepMark(s.Mark)+" "+strings.Repeat("  ", s.Depth)+clipText(s.String())+stepNote(s))
		}
	}
	return out
}

// flow prints what a change does to its function's paths: the verdict, and
// for an edit the changed steps and the ways out. Only a review has paths.
func (p *printer) flow(i int) {
	if p.paths == nil {
		return
	}
	ps := p.paths(i)
	if ps == nil {
		return
	}
	p.printf("    paths: %s\n", ps.Verdict)
	if ps.Kind == review.PathsNew {
		return
	}
	for _, l := range flowLines(ps) {
		p.printf("      %s\n", l)
	}
	if len(ps.Exits) > 0 {
		p.printf("    ways out\n")
		for _, e := range ps.Exits {
			p.printf("      %s %s%s\n", exitMark(e.Mark), clipText(e.What), exitWhen(e))
		}
	}
}

// flow writes what a change does to its function's paths, as the line
// under the change and, for an edit, a diff of the changed steps.
func (m *markdown) flow(rv *review.Review, i int) {
	ps := rv.PathsOf(i)
	if ps == nil {
		return
	}
	m.printf("  \n  Paths: %s\n", mdText(ps.Verdict))
	if ps.Kind == review.PathsNew {
		return
	}
	lines := flowLines(ps)
	if len(lines) == 0 {
		return
	}
	m.printf("  ```diff\n")
	for _, l := range lines {
		// Edited and moved steps read as changed lines in a diff.
		if l[0] == '*' || l[0] == '>' || l[0] == '~' {
			l = "!" + l[1:]
		}
		m.printf("  %s\n", strings.ReplaceAll(l, "```", "'''"))
	}
	m.printf("  ```\n")
}

// mdText escapes the characters Markdown would read as formatting.
func mdText(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune("\\`*_<>[]|", r) {
			b.WriteRune('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
