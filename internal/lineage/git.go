package lineage

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"huginn/internal/ast"
	"huginn/internal/git"
)

// FromCommit converts a walked commit into a revision.
func FromCommit(c *git.Commit) Revision {
	subject, detail := Subjects(c.Message)
	return Revision{SHA: c.Hash, Time: c.AuthorTime, Author: c.Author, Subject: subject, Detail: detail, Changes: c.Changes}
}

// maxSubject caps the length of the lines Subjects keeps, in bytes.
const maxSubject = 200

// Subjects returns the first line of a commit message and the next
// non-empty one, each trimmed and capped.
func Subjects(msg string) (subject, detail string) {
	lines := strings.Split(msg, "\n")
	subject = clip(lines[0])
	for _, l := range lines[1:] {
		if l = clip(l); l != "" {
			return subject, l
		}
	}
	return subject, ""
}

func clip(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxSubject {
		return s
	}
	s = s[:maxSubject]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
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
	missing := uncached(hashes, revs)
	c.Hits, c.Walked = len(hashes)-len(missing), len(missing)

	// The walk delivers the missing commits in order; the cached ones
	// between them are applied as it reaches them.
	p := &cacheReplay{hashes: hashes, revs: revs, apply: apply}
	if len(missing) > 0 {
		walk.Commits = missing
		if err := repo.Walk(ctx, walk, p.applyWalked); err != nil {
			return err
		}
	}
	if err := p.applyCached(""); err != nil {
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

// uncached returns the hashes that revs has no revision for, in order.
func uncached(hashes []string, revs map[string]Revision) []string {
	var missing []string
	for _, sha := range hashes {
		if _, ok := revs[sha]; !ok {
			missing = append(missing, sha)
		}
	}
	return missing
}

// cacheReplay applies the revisions of hashes in order, each from the cache
// or, for those it lacks, from the walk.
type cacheReplay struct {
	hashes []string
	revs   map[string]Revision // gains the revisions walked
	next   int                 // index into hashes of the next to apply
	apply  func(Revision)
}

// applyCached applies the cached revisions from next up to, not including,
// until; "" applies all that are left.
func (p *cacheReplay) applyCached(until string) error {
	for ; p.next < len(p.hashes) && p.hashes[p.next] != until; p.next++ {
		r, ok := p.revs[p.hashes[p.next]]
		if !ok {
			return fmt.Errorf("walk skipped commit %s", p.hashes[p.next])
		}
		p.apply(r)
	}
	return nil
}

// applyWalked applies the cached revisions before cm, then cm's, which it
// keeps for the cache.
func (p *cacheReplay) applyWalked(cm *git.Commit) error {
	if err := p.applyCached(cm.Hash); err != nil {
		return err
	}
	if p.next == len(p.hashes) {
		return fmt.Errorf("walk returned unexpected commit %s", cm.Hash)
	}
	r := FromCommit(cm)
	p.revs[cm.Hash] = r
	p.apply(r)
	p.next++
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
