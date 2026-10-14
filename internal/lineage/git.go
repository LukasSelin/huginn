package lineage

import (
	"context"
	"strings"

	"huginn/internal/ast"
	"huginn/internal/git"
)

// FromCommit converts a walked commit into a revision.
func FromCommit(c *git.Commit) Revision {
	return Revision{SHA: c.Hash, Time: c.AuthorTime, Author: c.Author, Changes: c.Changes}
}

// BuildOptions tunes Build.
type BuildOptions struct {
	git.Options

	// Tests keeps symbols declared in _test.go files.
	Tests bool

	// Progress, if set, is called after each commit is applied with the
	// number applied so far.
	Progress func(commits int)
}

// Build walks a repository's history and records every symbol in it. It
// always follows first parents only, since Apply needs one line of history.
func Build(ctx context.Context, repo *git.Repo, opts BuildOptions) (*History, error) {
	h := New()
	walk := opts.Options
	walk.FirstParent = true
	err := repo.Walk(ctx, walk, func(c *git.Commit) error {
		r := FromCommit(c)
		if !opts.Tests {
			r.Changes = withoutTests(r.Changes)
		}
		h.Apply(r)
		if opts.Progress != nil {
			opts.Progress(h.Revisions())
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return h, nil
}

// withoutTests drops changes to symbols that are declared in test files on
// both sides. A symbol moved into or out of a test file is kept.
func withoutTests(changes []ast.Change) []ast.Change {
	out := changes[:0:0]
	for _, c := range changes {
		if isTest(c.Old) && isTest(c.New) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// isTest reports whether s is declared in a test file. A missing side counts
// as a test file, so that additions and deletions in tests are dropped too.
func isTest(s *ast.Symbol) bool { return s == nil || strings.HasSuffix(s.Path, "_test.go") }
