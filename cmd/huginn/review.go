package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"huginn/internal/capslock"
	"huginn/internal/cohesion"
	"huginn/internal/git"
	"huginn/internal/review"
	"huginn/internal/semdiff"
)

// reviewMergeRequest prints what a merge request changes, the code that depends on it
// and the relations it starts and stops between packages, limited to the
// folders given.
func reviewMergeRequest(ctx context.Context, stdout, stderr io.Writer, args []string) error {
	fs := flag.NewFlagSet("review", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("C", "", "repository directory (default: the first folder's repository, or the current directory)")
	base := fs.String("base", "", "branch or commit the merge request goes into (default: the CI's target branch, or the remote's default branch)")
	head := fs.String("head", "HEAD", "the merge request's head: a branch or commit")
	format := fs.String("format", "text", "output `format`: text, markdown or json")
	includeGenerated := fs.Bool("include-generated", false, "diff generated files like hand-written ones")
	maxCallers := fs.Int("callers", 5, "list at most `n` callers per change and 3n in the impact; 0 for all")
	caps := fs.Bool("caps", true, "trace with capslock which changed functions reach the network, files or other programs, and list them first")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *format != "text" && *format != "markdown" && *format != "json" {
		return fmt.Errorf("unknown format %q: want text, markdown or json", *format)
	}

	repo, scope, err := openScoped(ctx, *dir, fs.Args())
	if err != nil {
		return err
	}
	opts := review.Options{Base: *base, Head: *head, Scope: scope, IncludeGenerated: *includeGenerated, Getenv: os.Getenv}
	if *caps {
		tool, findErr := capslock.Find()
		if findErr != nil {
			fmt.Fprintf(stderr, "note: %v, to see which changes reach outside the process; -caps=false skips this\n", findErr)
		} else {
			opts.Tracer = review.NewTracer(tool)
		}
	}
	rv, err := review.Build(ctx, repo, opts)
	if err != nil {
		return err
	}
	for _, w := range rv.Warnings {
		fmt.Fprintln(stderr, "warning:", w)
	}
	switch *format {
	case "json":
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rv)
	case "markdown":
		m := &markdown{w: stdout, maxCallers: *maxCallers}
		m.review(rv)
		return m.err
	}
	p := &printer{w: stdout, maxCallers: *maxCallers, scoped: len(scope) > 0}
	p.review(rv)
	return p.err
}

// openScoped opens the repository and turns the folders, relative to dir
// (or to the current directory), into slash-separated repository paths.
// Without dir, the repository is the first folder's, so a folder alone
// says where to look. A folder need not exist in the working tree: the
// merge request may add or delete it.
func openScoped(ctx context.Context, dir string, folders []string) (*git.Repo, []string, error) {
	abs := make([]string, len(folders))
	for i, f := range folders {
		if !filepath.IsAbs(f) {
			f = filepath.Join(dir, f)
		}
		var err error
		if abs[i], err = filepath.Abs(f); err != nil {
			return nil, nil, err
		}
	}
	at := dir
	if at == "" {
		at = "."
		if len(abs) > 0 {
			at = existing(abs[0])
		}
	}
	repo, err := git.Open(ctx, at)
	if err != nil {
		return nil, nil, err
	}
	top, err := repo.Toplevel(ctx)
	if err != nil {
		return nil, nil, err
	}
	var scope []string
	for i, a := range abs {
		rel, err := filepath.Rel(top, a)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return nil, nil, fmt.Errorf("%s is not in the repository at %s", folders[i], top) //nolint:gosec // G602: abs has one entry per folder
		}
		if rel != "." {
			scope = append(scope, filepath.ToSlash(rel))
		}
	}
	if len(scope) < len(abs) {
		scope = nil // one of the folders is the whole repository
	}
	return repo, scope, nil
}

// existing returns the nearest directory at or above p that exists.
func existing(p string) string {
	for {
		if fi, err := os.Stat(p); err == nil {
			if fi.IsDir() {
				return p
			}
			return filepath.Dir(p)
		}
		up := filepath.Dir(p)
		if up == p {
			return p
		}
		p = up
	}
}

// review prints a merge request's report: what it reaches and the relations
// it changes first, then the files and changes they come from.
func (p *printer) review(rv *review.Review) {
	p.printf("review %s into %s\n", rv.HeadName(), rv.Target)
	p.printf("from merge base %.7s · %s\n", rv.MergeBase, plural(len(rv.Commits), "commit"))
	if len(rv.Scope) > 0 {
		p.printf("scope: %s\n", strings.Join(rv.Scope, ", "))
	}
	p.printf("\n")
	r := rv.Report
	p.paths = rv.PathsOf
	p.summary(r)
	p.focus(rv)
	p.blast("Impact", r)
	p.relations(r.Relations)
	p.types(rv.Types)
	p.files(r)
	p.changes(r)
	p.noise(r)
}

// The focus lists up to focusEdits edits of existing code, and as many
// changes reaching outside the process, and counts the rest; each item
// names up to reachNames of the declarations it reaches, and each path out
// of the process up to pathNames calls.
const (
	focusEdits = 6
	reachNames = 3
	pathNames  = 6
)

// noFocus is what the focus says when there is nothing in it.
const noFocus = "nothing beyond new code: no existing code is edited and no import between packages starts"

// noneOutside is what the focus says when capslock traced the change and
// found nothing reaching outside the process.
const noneOutside = "traced with capslock: nothing the change adds or edits reaches the network, files or other programs"

// focusRun is a focus item alone, or the FocusExternal items leaving the
// changed code by the same call, which the focus lists together:
// items are those shown, of total.
type focusRun struct {
	items []review.Focus
	total int
}

// grouped reports whether the run lists items under the call they leave
// the changed code by.
func (r focusRun) grouped() bool { return r.total > 1 }

// focusRuns cuts the focus into runs: each FocusExternal item with those
// right after it sharing its exit, and every other item alone.
func focusRuns(fs []review.Focus) []focusRun {
	var out []focusRun
	for _, f := range fs {
		if n := len(out); n > 0 && out[n-1].items[0].SameExit(f) {
			out[n-1].items = append(out[n-1].items, f)
			out[n-1].total++
			continue
		}
		out = append(out, focusRun{items: []review.Focus{f}, total: 1})
	}
	return out
}

// shownFocus returns the focus runs to list and how many changes reaching
// outside the process and edits of existing code it leaves out. A run of
// changes reaching outside keeps its total when only some of its items fit.
func shownFocus(fs []review.Focus) (shown []focusRun, moreOutside, moreEdits int) {
	outside, edits := 0, 0
	for _, r := range focusRuns(fs) {
		switch r.items[0].Kind {
		case review.FocusExternal:
			n := min(len(r.items), max(0, focusEdits-outside))
			outside += n
			moreOutside += len(r.items) - n
			if n == 0 {
				continue
			}
			r.items = r.items[:n]
		case review.FocusBehavior:
			if edits++; edits > focusEdits {
				moreEdits++
				continue
			}
		default:
		}
		shown = append(shown, r)
	}
	return shown, moreOutside, moreEdits
}

// focusMore says what shownFocus left out: "3 more changes reaching
// outside the process and 2 more edits of existing code", or "".
func focusMore(moreOutside, moreEdits int) string {
	var parts []string
	if moreOutside > 0 {
		parts = append(parts, plural(moreOutside, "more change")+" reaching outside the process")
	}
	if moreEdits > 0 {
		parts = append(parts, plural(moreEdits, "more edit")+" of existing code")
	}
	return strings.Join(parts, " and ")
}

// tracedNothing reports whether capslock traced the change and found
// nothing in it reaching outside the process.
func tracedNothing(rv *review.Review) bool {
	return rv.Traced && !slices.ContainsFunc(rv.Focus, func(f review.Focus) bool { return f.Kind == review.FocusExternal })
}

// capabilityText says how a focus item reaches outside the process,
// "network (new): payments.Authorize → http.Get → … → net.(*Dialer).Dial",
// names formatted by name, and where the call leaving the repository's code
// is made: "payments/payments.go:6", or "". Listed under the call it leaves
// the changed code by (grouped), a path leaving by that call stops there,
// and the rest is left to the group to say.
func capabilityText(f review.Focus, c review.Capability, name func(string) string, grouped bool) (text, where string) {
	text = c.Label()
	if c.New {
		text += " (new)"
	}
	steps, elided, at := c.Path, c.Elided, c.Leaves()
	exit, ok := c.Exit()
	if shared, sharedOK := f.Exit(); grouped && ok && sharedOK && exit == shared {
		steps, elided, at = c.Path[:c.LeavesChange()+1], 0, -1
	}
	if len(steps) > 0 && steps[0].Name == f.Name {
		steps = steps[1:] // the item itself, unless the path starts in a function literal of it
	}
	if len(steps) > 0 {
		text += ": " + pathText(steps, elided, name)
	}
	if at >= 0 {
		where = fmt.Sprintf("%s:%d", c.Path[at].Path, c.Path[at].Line)
	}
	return text, where
}

// exitText says how the items of a run leave the changed code: by the call
// they share and what it leads to, "client.Do → http.Get → … →
// net.(*Dialer).DialContext", names formatted by name, made at
// "payments/payments.go:6".
func exitText(r focusRun, name func(string) string) (text, where string) {
	c := r.items[0].Capabilities[0]
	i := c.LeavesChange()
	return pathText(c.Path[i:], c.Elided, name), fmt.Sprintf("%s:%d", c.Path[i].Path, c.Path[i].Line)
}

// exitDetail says how many changes reach outside through a run's call and,
// when the path leaves the repository's code further on, where, formatted
// by code: "2 changes reach outside through this call, leaving the
// repository's code at client/client.go:9".
func exitDetail(r focusRun, code func(string) string) string {
	text := plural(r.total, "change") + " reach outside through this call"
	c := r.items[0].Capabilities[0]
	if i := c.Leaves(); i != c.LeavesChange() {
		text += ", leaving the repository's code at " + code(fmt.Sprintf("%s:%d", c.Path[i].Path, c.Path[i].Line))
	}
	return text
}

// pathText joins the names of a path's steps with arrows, names formatted
// by name, "…" standing for the elided calls before the last and for the
// middle of a path with more than pathNames.
func pathText(steps []review.Step, elided int, name func(string) string) string {
	var names []string
	for i, s := range steps {
		if elided > 0 && i == len(steps)-1 {
			names = append(names, "…")
		}
		names = append(names, name(s.Name))
	}
	if len(names) > pathNames {
		names = slices.Concat(names[:2], []string{"…"}, names[len(names)-3:])
	}
	return strings.Join(names, " → ")
}

// focusLabel names a focus item's kind as the reports print it.
func focusLabel(k review.FocusKind) string {
	switch k {
	case review.FocusExternal:
		return "external"
	case review.FocusBreaking:
		return "breaking"
	case review.FocusBehavior:
		return "edit"
	case review.FocusDependency:
		return "import"
	case review.FocusStructure:
		return "blocks"
	}
	return k.String()
}

// focusDetail says what a focus item changed, why it matters and what it
// reaches, names formatted by name: "+3 stmts · called by a, b and 2 more ·
// 12 in tests".
func focusDetail(f review.Focus, r *semdiff.Report, name func(string) string) string {
	parts := []string{f.What}
	if f.Why != "" {
		parts = append(parts, f.Why)
	}
	if reach := focusReach(f, r, name); reach != "" {
		parts = append(parts, reach)
	}
	if f.Tests > 0 {
		parts = append(parts, fmt.Sprintf("%d in tests", f.Tests))
	}
	return strings.Join(parts, " · ")
}

// focusReach says what reaches a focus item from outside the change:
// "called by a, b, 2 more", or that nothing does; "" when neither applies.
func focusReach(f review.Focus, r *semdiff.Report, name func(string) string) string {
	switch {
	case len(f.Reaches) > 0:
		var names []string
		for i, c := range f.Reaches {
			if i == reachNames {
				names = append(names, fmt.Sprintf("%d more", len(f.Reaches)-reachNames))
				break
			}
			names = append(names, name(c.Name))
		}
		return reachVerb(f, r) + " " + strings.Join(names, ", ")
	case f.Implementers:
		return "no implementer found outside the change"
	case (f.Kind == review.FocusBehavior || f.Kind == review.FocusExternal) && f.Tests == 0:
		return "nothing calls it"
	case f.Kind == review.FocusBehavior || f.Kind == review.FocusExternal:
		return "only tests use it"
	}
	return ""
}

// reachVerb is how a focus item's reach is introduced: implemented by,
// called by for a function or method, or used by.
func reachVerb(f review.Focus, r *semdiff.Report) string {
	switch {
	case f.Implementers:
		return "implemented by"
	case f.Change >= 0 && (r.Changes[f.Change].Symbol == "func" || r.Changes[f.Change].Symbol == "method"):
		return "called by"
	}
	return "used by"
}

// at locates a focus item: "path:line", the path alone, or nothing.
func at(f review.Focus) string {
	if f.Line > 0 {
		return fmt.Sprintf("%s:%d", f.Path, f.Line)
	}
	return f.Path
}

// focus prints what to look at first: what the change does that its diff
// does not show.
func (p *printer) focus(rv *review.Review) {
	p.printf("\nLook here first\n")
	shown, moreOutside, moreEdits := shownFocus(rv.Focus)
	if len(shown) == 0 {
		p.printf("  %s\n", noFocus)
	}
	for _, r := range shown {
		if !r.grouped() {
			p.focusItem(rv, r.items[0], "  ", false)
			continue
		}
		text, where := exitText(r, plainName)
		p.printf("  [%s] %s  %s\n", focusLabel(review.FocusExternal), text, where)
		p.printf("      %s\n", exitDetail(r, plainName))
		for _, f := range r.items {
			p.focusItem(rv, f, "      ", true)
		}
	}
	if more := focusMore(moreOutside, moreEdits); more != "" {
		p.printf("  … and %s under Changes\n", more)
	}
	if tracedNothing(rv) {
		p.printf("  %s\n", noneOutside)
	}
}

// focusItem prints a focus item indented by in: its kind, unless it is
// grouped under the call it leaves the changed code by, then what it
// does to its paths, what changed and what it reaches, and how it reaches
// outside the process.
func (p *printer) focusItem(rv *review.Review, f review.Focus, in string, grouped bool) {
	if grouped {
		p.printf("%s%s  %s\n", in, f.Name, at(f))
	} else {
		p.printf("%s[%s] %s  %s\n", in, focusLabel(f.Kind), f.Name, at(f))
	}
	if ps := rv.PathsOf(f.Change); ps != nil {
		p.printf("%s    %s\n", in, clipText(ps.Verdict))
	}
	p.printf("%s    %s\n", in, focusDetail(f, rv.Report, plainName))
	for _, c := range f.Capabilities {
		text, where := capabilityText(f, c, plainName, grouped)
		if where != "" {
			text += "  " + where
		}
		p.printf("%s    %s\n", in, text)
	}
}

// plainName formats a name as it is, for text.
func plainName(s string) string { return s }

// types prints, for each struct type the change touches, what it does to
// the type's affinity matrix: its blocks before and after, warnings first,
// and the block to extract first.
func (p *printer) types(ts []review.TypeReview) {
	if len(ts) == 0 {
		return
	}
	p.printf("\nAffinity matrix of the types it touches\n")
	for _, t := range ts {
		p.printf("  %s  %s:%d  %s\n", t.Name, t.Path, t.Line, matrixSummary(t))
		for _, n := range t.Notes {
			mark := "-"
			if n.Warn {
				mark = "!"
			}
			p.printf("    %s %s\n", mark, n.Text)
		}
		if r := t.After; r != nil && r.Start != nil {
			p.printf("    start with block %s: %s\n", r.Start.Block, r.Start.Reason)
		}
	}
}

// matrixSummary sums up a type's matrix: "11 methods · blocks 4 → 3 ·
// LCOM4 1, 2 without hubs and bridges".
func matrixSummary(t review.TypeReview) string {
	r := t.After
	if r == nil {
		r = t.Before
	}
	blocks := plural(realBlocks(r), "block")
	if t.Before != nil && t.After != nil {
		blocks = fmt.Sprintf("blocks %d → %d", realBlocks(t.Before), realBlocks(t.After))
	}
	return fmt.Sprintf("%s · %s · LCOM4 %d, %d without hubs and bridges", plural(len(r.Methods), "method"), blocks, r.LCOM4, r.LCOM4Split)
}

func realBlocks(r *cohesion.Report) int {
	n := 0
	for _, b := range r.Blocks {
		if b.Label != "core" {
			n++
		}
	}
	return n
}

// relations prints the imports and cross-package references a change
// starts and stops: each import with the references along it, then the
// references along imports that stay.
func (p *printer) relations(rel semdiff.Relations) {
	p.printf("\nRelations\n")
	if rel.Empty() {
		p.printf("  no imports or references between packages started or stopped\n")
		return
	}
	for _, pkg := range rel.NewPackages {
		p.printf("  new package %s\n", pkg)
	}
	for _, pkg := range rel.GonePackages {
		p.printf("  removed package %s\n", pkg)
	}
	added, addedAlong := along(rel.AddedDeps, rel.AddedRefs)
	removed, removedAlong := along(rel.RemovedDeps, rel.RemovedRefs)
	for i, d := range rel.AddedDeps {
		p.dependency("+", d, added[i])
	}
	for i, d := range rel.RemovedDeps {
		p.dependency("−", d, removed[i])
	}
	if len(addedAlong)+len(removedAlong) > 0 {
		p.printf("  along imports that stay\n")
		limit := 3 * p.maxCallers
		p.references("    + ", addedAlong, limit)
		p.references("    − ", removedAlong, limit)
	}
}

// along splits refs into those along each of deps, in order, and the rest.
func along(deps []semdiff.Dependency, refs []semdiff.Reference) ([][]semdiff.Reference, []semdiff.Reference) {
	at := map[[2]string]int{}
	for i, d := range deps {
		at[[2]string{d.From, d.To}] = i
	}
	byDep := make([][]semdiff.Reference, len(deps))
	var rest []semdiff.Reference
	for _, r := range refs {
		if i, ok := at[[2]string{r.FromPackage, r.ToPackage}]; ok {
			byDep[i] = append(byDep[i], r)
		} else {
			rest = append(rest, r)
		}
	}
	return byDep, rest
}

func (p *printer) dependency(mark string, d semdiff.Dependency, refs []semdiff.Reference) {
	note := ""
	switch d.Crossing {
	case semdiff.Outward:
		note = "  (out of the scope)"
	case semdiff.Inward:
		note = "  (into the scope)"
	case semdiff.Within:
	}
	p.printf("  %s %s → %s%s\n", mark, d.From, d.To, note)
	if len(refs) == 0 {
		p.printf("      imported in %s\n", strings.Join(d.Files, ", "))
		return
	}
	p.references("      ", refs, p.maxCallers)
}

// references lists up to limit references, all of them if limit is 0.
func (p *printer) references(indent string, refs []semdiff.Reference, limit int) {
	for i, r := range refs {
		if limit > 0 && i == limit {
			p.printf("%s… and %d more\n", indent, len(refs)-limit)
			return
		}
		note := ""
		if r.Approximate {
			note = " (approx.)"
		}
		p.printf("%s%s → %s  %s:%d  ×%d%s\n", indent, r.From, r.To, r.Path, r.Line, r.Uses, note)
	}
}
