package story

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// Templates narrates chapters by filling sentence templates with their
// facts. Every sentence it writes restates one or more facts and nothing
// else.
type Templates struct{}

// Narrate implements Narrator.
func (Templates) Narrate(_ context.Context, f Facts) (Narration, error) {
	return Narration{Title: title(f), Text: paragraph(f)}, nil
}

// title names the chapter after its strongest signal, in this order: new
// packages, more removed than added, renames and moves, signature changes,
// the last release, and otherwise the hottest package.
func title(f Facts) string {
	switch {
	case f.Edits == 0:
		return "Changes outside the code"
	case len(f.Births) > 0 && f.Number == 1:
		return "Beginnings: " + pkgName(biggestBirth(f)) + more(len(f.Births)-1)
	case len(f.Births) > 0:
		return "Enter " + pkgName(biggestBirth(f)) + more(len(f.Births)-1)
	case f.Removed > f.Added:
		return "Pruning " + pkgName(f.Hottest)
	case 10*(f.Renamed+f.Moved) >= 3*f.Edits:
		return "Reshaping " + pkgName(f.Hottest)
	case 5*f.Signature >= f.Edits:
		return "Reworking the API of " + pkgName(f.Hottest)
	case len(f.Releases) > 0:
		return "Toward " + f.Releases[len(f.Releases)-1]
	}
	return "Deepening " + pkgName(f.Hottest)
}

// biggestBirth returns the package born with the most symbols, the first by
// path among equals.
func biggestBirth(f Facts) string {
	best, n := f.Births[0], -1
	for _, m := range f.Moments {
		if m.Kind == Birth && (m.Count > n || m.Count == n && m.Package < best) {
			best, n = m.Package, m.Count
		}
	}
	return best
}

func paragraph(f Facts) string {
	var s []string
	from, to := f.From.Format("Jan 2, 2006"), f.To.Format("Jan 2, 2006")
	switch {
	case from == to:
		s = append(s, fmt.Sprintf("On %s, %s landed.", from, plural(f.Commits, "commit")))
	default:
		s = append(s, fmt.Sprintf("Between %s and %s, %s landed.", from, to, plural(f.Commits, "commit")))
	}
	if t := themeSentence(f); t != "" {
		s = append(s, t)
	}

	if f.Edits > 0 {
		share := percent(f.HottestEdits, f.Edits)
		lead := fmt.Sprintf("The busiest package was %s, with %d%% of the %s", pkgName(f.Hottest), share, plural(f.Edits, "semantic edit"))
		if 2*f.HottestEdits > f.Edits {
			lead = fmt.Sprintf("Most of the %s (%d%%) were in %s", plural(f.Edits, "semantic edit"), share, pkgName(f.Hottest))
		}
		if f.Runner != "" {
			lead += fmt.Sprintf(", followed by %s (%d%%)", pkgName(f.Runner), percent(f.RunnerEdits, f.Edits))
		}
		s = append(s, lead+".")
		s = append(s, fmt.Sprintf("%s %s added and %d removed, leaving %s in %s.",
			plural(f.Added, "symbol"), pick(f.Added, "was", "were"), f.Removed, plural(f.Live, "live symbol"), plural(f.LivePackages, "package")))
	} else {
		s = append(s, "No Go symbols changed.")
	}

	if len(f.Births) > 0 {
		s = append(s, pick(len(f.Births), "New package: ", "New packages: ")+names(f.Births)+".")
	}
	if len(f.Deaths) > 0 {
		s = append(s, pick(len(f.Deaths), "Removed package: ", "Removed packages: ")+names(f.Deaths)+".")
	}
	if len(f.Releases) > 0 {
		s = append(s, pick(len(f.Releases), "Release: ", "Releases: ")+list(f.Releases)+".")
	}
	if p := peopleSentence(f); p != "" {
		s = append(s, p)
	}
	// The weightiest moment that has not been said already.
	var best *Moment
	for i, m := range f.Moments {
		said := m.Kind == Birth || m.Kind == Death || m.Kind == Tagged && len(f.Releases) > 1 || m.Kind == Newcomer
		if !said && (best == nil || m.Weight > best.Weight) {
			best = &f.Moments[i]
		}
	}
	if best != nil {
		s = append(s, best.Text+".")
	}
	return strings.Join(s, " ")
}

// typeNouns name conventional-commit types as counted things.
var typeNouns = map[string][2]string{
	"feat":     {"feature", "features"},
	"fix":      {"fix", "fixes"},
	"docs":     {"documentation change", "documentation changes"},
	"style":    {"style change", "style changes"},
	"refactor": {"refactoring", "refactorings"},
	"perf":     {"performance change", "performance changes"},
	"test":     {"test change", "test changes"},
	"build":    {"build change", "build changes"},
	"ci":       {"CI change", "CI changes"},
	"chore":    {"chore", "chores"},
	"revert":   {"revert", "reverts"},
	"deps":     {"dependency update", "dependency updates"},
}

func typeCount(t Theme) string {
	n := typeNouns[t.Name]
	return count(t.Count, n[0], n[1])
}

// themeSentence says what the commit messages were about: their types when
// most commits have one, else the areas they name; then the words that
// recur, and how many pull requests were merged.
func themeSentence(f Facts) string {
	th := f.Themes
	var s []string
	switch {
	case len(th.Types) > 0 && 2*th.Types[0].Count > f.Commits:
		lead := th.Types[0]
		t := fmt.Sprintf("Most commits were %s (%d of %d)", typeNouns[lead.Name][1], lead.Count, f.Commits)
		if th.LeadScopeCount >= 2 && 5*th.LeadScopeCount >= 2*lead.Count {
			t += fmt.Sprintf(", %d of them scoped to %s", th.LeadScopeCount, th.LeadScope)
		}
		s = append(s, t+".")
	case th.Typed >= 3 && 3*th.Typed >= f.Commits:
		parts := make([]string, len(th.Types))
		for i, t := range th.Types {
			parts[i] = typeCount(t)
		}
		s = append(s, "Commit types: "+list(parts)+".")
	case len(th.Scopes) > 0:
		parts := make([]string, len(th.Scopes))
		for i, t := range th.Scopes {
			parts[i] = fmt.Sprintf("%s (%d)", t.Name, t.Count)
		}
		s = append(s, "Commit messages most often named "+list(parts)+".")
	}
	if len(th.Terms) > 0 {
		parts := make([]string, len(th.Terms))
		for i, t := range th.Terms {
			parts[i] = fmt.Sprintf("%q (%d)", t.Name, t.Count)
		}
		s = append(s, "Recurring words in commit messages: "+list(parts)+".")
	}
	if th.PullRequests > 0 {
		s = append(s, fmt.Sprintf("%s %s merged.", plural(th.PullRequests, "pull request"), pick(th.PullRequests, "was", "were")))
	}
	return strings.Join(s, " ")
}

// peopleSentence says how many people committed and how many semantic edits
// the busiest made, when more than one did; and who committed for the
// first time, after the first chapter.
func peopleSentence(f Facts) string {
	var s []string
	if f.Authors > 1 {
		s = append(s, count(f.Authors, "person", "people")+" committed.")
		if len(f.Contributors) > 0 && f.Edits > 0 {
			parts := make([]string, len(f.Contributors))
			for i, c := range f.Contributors {
				parts[i] = fmt.Sprintf("%s %d", c.Name, c.Edits)
			}
			parts[0] = fmt.Sprintf("%s made %d", f.Contributors[0].Name, f.Contributors[0].Edits)
			s = append(s, fmt.Sprintf("Of the %s, %s.", plural(f.Edits, "semantic edit"), list(parts)))
		}
	}
	if n := len(f.Newcomers); n > 0 && f.Number > 1 {
		t := pick(n, "First commit by ", "First commits by ") + list(f.Newcomers) + "."
		if n > 4 {
			t = fmt.Sprintf("%d people committed for the first time.", n)
		}
		s = append(s, t)
	}
	return strings.Join(s, " ")
}

func names(pkgs []string) string {
	out := make([]string, len(pkgs))
	for i, p := range pkgs {
		out[i] = pkgName(p)
	}
	return list(out)
}

// list joins items in prose: "a", "a and b", "a, b and c", and past four
// "a, b, c and 2 others".
func list(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	case 2:
		return items[0] + " and " + items[1]
	}
	if len(items) > 4 {
		return strings.Join(items[:3], ", ") + fmt.Sprintf(" and %d others", len(items)-3)
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

func percent(n, of int) int {
	if of == 0 {
		return 0
	}
	return (100*n + of/2) / of
}

// pick returns one when n is 1, else many.
func pick(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// count says n of something: "1 person", "3 people".
func count(n int, one, many string) string { return strconv.Itoa(n) + " " + pick(n, one, many) }

// plural says n of a regular noun: "1 commit", "3 commits".
func plural(n int, noun string) string { return count(n, noun, noun+"s") }

// more says how many more there are than the one named, if any.
func more(n int) string {
	if n == 0 {
		return ""
	}
	return " and " + strconv.Itoa(n) + " more"
}
