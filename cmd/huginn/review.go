package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"huginn/internal/git"
	"huginn/internal/semdiff"
)

// review prints what a merge request changes, the code that depends on it
// and the relations it starts and stops between packages, limited to the
// folders given.
func review(ctx context.Context, stdout, stderr io.Writer, args []string) error {
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
	target, err := targetBranch(ctx, repo, *base, os.Getenv)
	if err != nil {
		return err
	}
	mergeBase, err := repo.MergeBase(ctx, target, *head)
	if err != nil {
		return err
	}
	rg, err := repo.DiffRange(ctx, mergeBase, *head)
	if err != nil {
		return err
	}
	tree, err := repo.Tree(ctx, rg.Head)
	if err != nil {
		return err
	}
	commits, err := repo.Commits(ctx, git.Options{Branch: rg.Base + ".." + rg.Head})
	if err != nil {
		return err
	}

	in := semdiff.Input{Tree: tree.Files, GoMod: tree.GoMod, IncludeGenerated: *includeGenerated, Scope: scope}
	for _, fd := range rg.Files {
		in.Files = append(in.Files, semdiff.FileDiff{Old: fd.Old, New: fd.New, Added: fd.Added, Removed: fd.Removed})
	}
	for _, err := range rg.ParseErrors {
		fmt.Fprintln(stderr, "warning:", err)
	}
	if n := len(tree.ParseErrors); n > 0 {
		fmt.Fprintf(stderr, "warning: %d files in the tree have syntax errors; callers may be missing\n", n)
	}

	rv := &reviewed{
		Head:      *head,
		HeadHash:  rg.Head,
		Target:    target,
		MergeBase: rg.Base,
		Commits:   commits,
		Scope:     scope,
		Report:    semdiff.Build(in),
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

// reviewed is a merge request's report, with what it was compared against.
type reviewed struct {
	Head      string // as given: "HEAD", a branch or a commit
	HeadHash  string
	Target    string   // the branch or commit it goes into
	MergeBase string   // where it was compared from
	Commits   []string // between the merge base and the head, oldest first
	Scope     []string // slash-separated repository paths; empty for the whole repository
	Report    *semdiff.Report
}

// headName names the head as given, with its short hash unless it is one.
func (rv *reviewed) headName() string {
	if strings.HasPrefix(rv.HeadHash, rv.Head) {
		return fmt.Sprintf("%.7s", rv.HeadHash)
	}
	return fmt.Sprintf("%s (%.7s)", rv.Head, rv.HeadHash)
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
	repo, err := git.Open(at)
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
			return nil, nil, fmt.Errorf("%s is not in the repository at %s", folders[i], top)
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

// targetBranch returns what a merge request goes into: base if given, else
// the target that GitLab's merge request pipelines or GitHub's pull request
// workflows name in the environment, else the remote's default branch.
func targetBranch(ctx context.Context, repo *git.Repo, base string, getenv func(string) string) (string, error) {
	if base != "" {
		return base, nil
	}
	if sha := getenv("CI_MERGE_REQUEST_DIFF_BASE_SHA"); sha != "" {
		return sha, nil
	}
	for _, v := range []string{"CI_MERGE_REQUEST_TARGET_BRANCH_NAME", "GITHUB_BASE_REF"} {
		if b := getenv(v); b != "" {
			return "origin/" + b, nil
		}
	}
	b, err := repo.DefaultBranch(ctx)
	if err != nil {
		return "", errors.Join(err, errors.New("name the branch the merge request goes into with -base"))
	}
	return b, nil
}

// review prints a merge request's report: what it reaches and the relations
// it changes first, then the files and changes they come from.
func (p *printer) review(rv *reviewed) {
	p.printf("review %s into %s\n", rv.headName(), rv.Target)
	p.printf("from merge base %.7s · %s\n", rv.MergeBase, plural(len(rv.Commits), "commit"))
	if len(rv.Scope) > 0 {
		p.printf("scope: %s\n", strings.Join(rv.Scope, ", "))
	}
	p.printf("\n")
	r := rv.Report
	p.summary(r)
	p.blast("Impact", r)
	p.relations(r.Relations)
	p.files(r)
	p.changes(r)
	p.noise(r)
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
