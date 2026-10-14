package lineage

import (
	"context"
	"fmt"
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

	// Cache, if set, supplies the revisions of commits walked before and
	// keeps those of the commits walked now.
	Cache *Cache
}

// Build walks a repository's history and records every symbol in it. It
// always follows first parents only, since Apply needs one line of history.
func Build(ctx context.Context, repo *git.Repo, opts BuildOptions) (*History, error) {
	h := New()
	apply := func(r Revision) {
		if !opts.Tests {
			r.Changes = withoutTests(r.Changes)
		}
		h.Apply(r)
		if opts.Progress != nil {
			opts.Progress(h.Revisions())
		}
	}
	walk := opts.Options
	walk.FirstParent = true
	if opts.Cache != nil {
		if err := opts.Cache.build(ctx, repo, walk, apply); err != nil {
			return nil, err
		}
		return h, nil
	}
	err := repo.Walk(ctx, walk, func(c *git.Commit) error {
		apply(FromCommit(c))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return h, nil
}

// build applies the revisions of the commits walk selects, oldest first,
// reading those it has from the cache and walking only the rest.
func (c *Cache) build(ctx context.Context, repo *git.Repo, walk git.Options, apply func(Revision)) error {
	hashes, err := repo.Commits(ctx, walk)
	if err != nil {
		return err
	}
	revs := c.load(walk.IncludeGenerated)
	var missing []string
	for _, sha := range hashes {
		if _, ok := revs[sha]; !ok {
			missing = append(missing, sha)
		}
	}
	c.Hits, c.Walked = len(hashes)-len(missing), len(missing)

	// The walk delivers the missing commits in order; the cached ones
	// between them are applied as it reaches them.
	next := 0
	applyCached := func(until string) error {
		for ; next < len(hashes) && hashes[next] != until; next++ {
			r, ok := revs[hashes[next]]
			if !ok {
				return fmt.Errorf("walk skipped commit %s", hashes[next])
			}
			apply(r)
		}
		return nil
	}
	if len(missing) > 0 {
		walk.Commits = missing
		err := repo.Walk(ctx, walk, func(cm *git.Commit) error {
			if err := applyCached(cm.Hash); err != nil {
				return err
			}
			if next == len(hashes) {
				return fmt.Errorf("walk returned unexpected commit %s", cm.Hash)
			}
			r := FromCommit(cm)
			revs[cm.Hash] = r
			apply(r)
			next++
			return nil
		})
		if err != nil {
			return err
		}
	}
	if err := applyCached(""); err != nil {
		return err
	}
	if len(missing) == 0 {
		return nil
	}
	if err := c.save(walk.IncludeGenerated, revs); err != nil {
		return fmt.Errorf("save cache: %w", err)
	}
	return nil
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
