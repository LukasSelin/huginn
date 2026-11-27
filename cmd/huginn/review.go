package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

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
	rv, err := review.Build(ctx, repo, review.Options{
		Base: *base, Head: *head, Scope: scope, IncludeGenerated: *includeGenerated, Getenv: os.Getenv,
	})
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
	p.summary(r)
	p.blast("Impact", r)
	p.relations(r.Relations)
	p.types(rv.Types)
	p.files(r)
	p.changes(r)
	p.noise(r)
}

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
