// Package review reports what a merge request changes: its semantic diff
// from the merge base with the branch it goes into, optionally limited to a
// scope of folders, with the code that depends on what it changed and the
// relations between packages it starts and stops.
package review

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"

	"huginn/internal/git"
	"huginn/internal/lineage"
	"huginn/internal/semdiff"
)

// Options says what to review.
type Options struct {
	// Base is the branch or commit the merge request goes into. When it is
	// empty, Target chooses one.
	Base string

	// Head is the merge request's head, a branch or commit; empty for HEAD.
	Head string

	// Scope limits the report to these slash-separated repository
	// directories or files, as semdiff.Input.Scope does.
	Scope []string

	IncludeGenerated bool

	// Getenv reads the environment, where a CI may name the target branch.
	// When nil the environment is not consulted.
	Getenv func(string) string

	// History, if not nil, is the indexed history, from which the review
	// tells which methods of a type change together.
	History *lineage.History

	// Tracer, if not nil, traces with capslock which of the functions the
	// change adds or edits reach outside the process, to look at first. It
	// checks out the head and the merge base and analyses their packages,
	// which takes a while.
	Tracer *Tracer
}

// Review is a merge request's report, with what it was compared against.
type Review struct {
	Head      string   // as given, or "HEAD"
	HeadHash  string   // full commit hash of the head
	Target    string   // the branch or commit it goes into
	MergeBase string   // full hash of the commit it is compared from
	Commits   []string // between the merge base and the head, oldest first
	Scope     []string // as given; empty for the whole repository
	Module    string   // the module path in the root go.mod at the head, if any
	Report    *semdiff.Report

	// Types are the seams of the struct types the change touches, before
	// and after it, the largest first.
	Types []TypeReview

	// Focus is what to look at first, most important first: what the
	// change does that its diff does not show.
	Focus []Focus

	// Paths are what the change does to the paths through each production
	// function it adds or whose body it edits, in the order of the
	// report's changes.
	Paths []Paths

	// Parts split the functions and methods the change touches into groups
	// to read one at a time, in reading order.
	Parts []Part

	// Layers places the packages the change touches on the layers of the
	// whole codebase on either side of it; nil when it touches none.
	Layers *Layers

	// Traced says capslock traced the functions the change adds or edits,
	// so those missing from the focus's FocusExternal items reach nothing
	// outside the process that capslock can see.
	Traced bool

	// Warnings are what may hide changes, callers or capabilities: syntax
	// errors, or a trace that failed.
	Warnings []string
}

// HeadName names the head as given, with its short hash unless it is one.
func (rv *Review) HeadName() string {
	if strings.HasPrefix(rv.HeadHash, rv.Head) {
		return fmt.Sprintf("%.7s", rv.HeadHash)
	}
	return fmt.Sprintf("%s (%.7s)", rv.Head, rv.HeadHash)
}

// Build reviews the merge request opts describes.
func Build(ctx context.Context, repo *git.Repo, opts Options) (*Review, error) {
	head := cmp.Or(opts.Head, "HEAD")
	target, err := Target(ctx, repo, opts.Base, opts.Getenv)
	if err != nil {
		return nil, err
	}
	mergeBase, err := repo.MergeBase(ctx, target, head)
	if err != nil {
		return nil, err
	}
	rg, err := repo.DiffRange(ctx, mergeBase, head)
	if err != nil {
		return nil, err
	}
	tree, err := repo.Tree(ctx, rg.Head)
	if err != nil {
		return nil, err
	}
	commits, err := repo.Commits(ctx, git.Options{Branch: rg.Base + ".." + rg.Head})
	if err != nil {
		return nil, err
	}

	in := semdiff.Input{Tree: tree.Files, GoMod: tree.GoMod, IncludeGenerated: opts.IncludeGenerated, Scope: opts.Scope}
	for _, fd := range rg.Files {
		in.Files = append(in.Files, semdiff.FileDiff{Old: fd.Old, New: fd.New, Added: fd.Added, Removed: fd.Removed})
	}
	rv := &Review{
		Head:      head,
		HeadHash:  rg.Head,
		Target:    target,
		MergeBase: rg.Base,
		Commits:   commits,
		Scope:     opts.Scope,
		Module:    semdiff.ModulePath(tree.GoMod["go.mod"]),
		Report:    semdiff.Build(in),
	}
	rv.Types = types(rv.Report, in, opts.History)
	var caps map[int][]Capability
	if opts.Tracer != nil {
		var warnings []string
		caps, warnings, err = opts.Tracer.trace(ctx, repo, rv, in)
		switch {
		case ctx.Err() != nil:
			return nil, ctx.Err()
		case err != nil:
			rv.Warnings = append(rv.Warnings, "capslock could not trace the changes: "+err.Error())
		default:
			rv.Traced = true
		}
		rv.Warnings = append(rv.Warnings, warnings...)
	}
	rv.Focus = focus(rv.Report, rv.Types, caps)
	rv.Paths = paths(rv.Report, rg.Files)
	rv.Parts = parts(rv.Report)
	rv.Layers = layersOf(rv.Report, semdiff.TreeBefore(in.Tree, in.Files), in.Tree, semdiff.PackagePaths(in.GoMod))
	for _, err := range rg.ParseErrors {
		rv.Warnings = append(rv.Warnings, err.Error())
	}
	if n := len(tree.ParseErrors); n > 0 {
		rv.Warnings = append(rv.Warnings, fmt.Sprintf("%d files in the tree have syntax errors; callers may be missing", n))
	}
	return rv, nil
}

// Target returns what a merge request goes into: base if given, else the
// target that GitLab's merge request pipelines or GitHub's pull request
// workflows name in the environment getenv reads, else the remote's default
// branch.
func Target(ctx context.Context, repo *git.Repo, base string, getenv func(string) string) (string, error) {
	if base != "" {
		return base, nil
	}
	if getenv != nil {
		if sha := getenv("CI_MERGE_REQUEST_DIFF_BASE_SHA"); sha != "" {
			return sha, nil
		}
		for _, v := range []string{"CI_MERGE_REQUEST_TARGET_BRANCH_NAME", "GITHUB_BASE_REF"} {
			if b := getenv(v); b != "" {
				return "origin/" + b, nil
			}
		}
	}
	b, err := repo.DefaultBranch(ctx)
	if err != nil {
		return "", errors.Join(err, errors.New("name the branch the merge request goes into"))
	}
	return b, nil
}
