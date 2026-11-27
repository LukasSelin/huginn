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

	// Warnings are syntax errors that may hide changes or callers.
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
