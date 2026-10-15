package story

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
)

// Templates narrates chapters by filling sentence templates with their
// facts. Every sentence it writes restates one or more facts and nothing
// else.
type Templates struct{}

// Narrate implements Narrator.
func (Templates) Narrate(_ context.Context, f Facts) (Narration, error) {
	return Narration{Title: title(f), Text: paragraph(f)}, nil
}

// Retell narrates st's chapters again with n, up to workers at a time, and
// returns the retold story; st is left as it is. A chapter n fails on keeps
// its narration. The error is the context's, if it ended first.
func Retell(ctx context.Context, st *Story, n Narrator, workers int) (*Story, error) {
	out := &Story{Chapters: slices.Clone(st.Chapters), Moments: st.Moments}
	sem := make(chan struct{}, max(1, workers))
	var wg sync.WaitGroup
	for i := range out.Chapters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			if nar, err := n.Narrate(ctx, out.Chapters[i].Facts); err == nil {
				out.Chapters[i].Narration = nar
			}
		}()
	}
	wg.Wait()
	return out, ctx.Err()
}

// title names the chapter after its strongest signal, in this order: new
// packages, more removed than added, renames and moves, signature changes,
// and otherwise the hottest package.
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

func more(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf(" and %d more", n)
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
		s = append(s, fmt.Sprintf("%s added and %d removed, leaving %s in %s.",
			wereAdded(f.Added), f.Removed, plural(f.Live, "live symbol"), plural(f.LivePackages, "package")))
	} else {
		s = append(s, "No Go symbols changed.")
	}

	if len(f.Births) > 0 {
		s = append(s, label(len(f.Births), "New package: ", "New packages: ")+names(f.Births)+".")
	}
	if len(f.Deaths) > 0 {
		s = append(s, label(len(f.Deaths), "Removed package: ", "Removed packages: ")+names(f.Deaths)+".")
	}
	// The weightiest moment that is not a birth or death, said already.
	var best *Moment
	for i, m := range f.Moments {
		if m.Kind != Birth && m.Kind != Death && (best == nil || m.Weight > best.Weight) {
			best = &f.Moments[i]
		}
	}
	if best != nil {
		s = append(s, best.Text+".")
	}
	return strings.Join(s, " ")
}

func wereAdded(n int) string {
	if n == 1 {
		return "1 symbol was"
	}
	return fmt.Sprintf("%d symbols were", n)
}

func label(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func names(pkgs []string) string {
	out := make([]string, len(pkgs))
	for i, p := range pkgs {
		out[i] = pkgName(p)
	}
	switch len(out) {
	case 1:
		return out[0]
	case 2:
		return out[0] + " and " + out[1]
	}
	if len(out) > 4 {
		return strings.Join(out[:3], ", ") + fmt.Sprintf(" and %d others", len(out)-3)
	}
	return strings.Join(out[:len(out)-1], ", ") + " and " + out[len(out)-1]
}

func percent(n, of int) int {
	if of == 0 {
		return 0
	}
	return (100*n + of/2) / of
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
