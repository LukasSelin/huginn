package git

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"io"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"huginn/internal/ast"
)

// Status is git's code for how a file changed.
type Status byte

const (
	StatusAdded       Status = 'A'
	StatusDeleted     Status = 'D'
	StatusModified    Status = 'M'
	StatusRenamed     Status = 'R'
	StatusCopied      Status = 'C'
	StatusTypeChanged Status = 'T' // e.g. a regular file became a symlink
)

func (s Status) String() string { return string(s) }

// FileChange is one file a commit touched, compared with its first parent.
type FileChange struct {
	Status  Status
	OldPath string // empty for additions
	NewPath string // empty for deletions

	// Added and Removed count text lines. Both are zero for binary files.
	Added, Removed int
	Binary         bool

	oldMode, newMode string
	oldHash, newHash string
}

// path is the path the file has after the commit, or had before a deletion.
func (fc *FileChange) path() string {
	if fc.NewPath != "" {
		return fc.NewPath
	}
	return fc.OldPath
}

// Commit is one commit reduced to what it did to the code.
type Commit struct {
	Hash    string
	Parents []string // empty for a root commit; the first is the one diffed against

	Author, Email          string // after .mailmap
	AuthorTime, CommitTime time.Time
	Message                string

	Files          []FileChange // every file touched, Go or not
	Added, Removed int          // text lines over all files

	// Changes are the symbol-level changes to Go code, ordered by file and
	// line. Symbols may be shared with neighboring commits and must not be
	// modified.
	Changes []ast.Change

	// FormattingOnly reports that Go files changed and parsed but no symbol
	// did: gofmt runs, comment edits, and edits outside declarations such as
	// imports.
	FormattingOnly bool

	// ParseErrors holds syntax errors from either side of the commit. The
	// symbols that could be recovered are still diffed, so Changes may hold
	// spurious additions and deletions.
	ParseErrors []error
}

// Options limits and tunes a Walk. The zero value walks all of HEAD.
type Options struct {
	// Branch is the revision to walk; default HEAD.
	Branch string

	// Since and Until bound the committer date, inclusive. Zero is unbounded.
	Since, Until time.Time

	// MaxCount keeps only the newest MaxCount commits in range, which are
	// still delivered oldest first. Zero means no limit.
	MaxCount int

	// FirstParent follows only the first parent of merges, so that each
	// merged branch appears as its merge commit alone. Otherwise the merged
	// commits appear too, and the merge's diff against its first parent
	// repeats their changes.
	FirstParent bool

	// IncludeGenerated diffs files with a "Code generated ... DO NOT EDIT."
	// header. By default generated code is treated as absent, so a file that
	// gains or loses the header deletes or adds its symbols.
	IncludeGenerated bool

	// Commits, if not empty, walks exactly these commits in this order
	// instead of a range of history; Branch, Since, Until, MaxCount and
	// FirstParent are ignored. Each is still diffed against its first parent.
	Commits []string
}

// Walk calls fn for each commit on opts.Branch, parents before children.
// Each commit is diffed against its first parent, and a root commit against
// the empty tree. Walk stops at the first error from fn and returns it.
func (r *Repo) Walk(ctx context.Context, opts Options, fn func(*Commit) error) error {
	return r.walk(ctx, opts, func(c *Commit, _ []fileSides) error { return fn(c) })
}

// FileDiff is one Go file a commit touched, with both sides parsed.
//
// Unlike Commit.Changes, the sides are given whether or not they hold
// generated code; File.Generated marks those that do, and the consumer
// decides whether they count, as Options.IncludeGenerated does for the walk.
type FileDiff struct {
	FileChange

	// Old and New are the file before and after the commit. Either is nil
	// when that side is absent, is not a regular Go file, or did not parse at
	// all. Syntax errors are in the commit's ParseErrors.
	Old, New *ast.File
}

// ResolveCommit returns the full hash of the commit rev names.
func (r *Repo) ResolveCommit(ctx context.Context, rev string) (string, error) {
	return r.resolve(ctx, rev, "commit")
}

// Diff returns the single commit rev, analyzed as Walk would with opts, and
// the Go files it touched. Only opts.IncludeGenerated is used.
func (r *Repo) Diff(ctx context.Context, rev string, opts Options) (*Commit, []FileDiff, error) {
	hash, err := r.resolve(ctx, rev, "commit")
	if err != nil {
		return nil, nil, err
	}
	opts = Options{Commits: []string{hash}, IncludeGenerated: opts.IncludeGenerated}
	var commit *Commit
	var files []FileDiff
	err = r.walk(ctx, opts, func(c *Commit, sides []fileSides) error {
		commit = c
		for _, s := range sides {
			files = append(files, FileDiff{FileChange: *s.fc, Old: s.old.f, New: s.new.f})
		}
		return nil
	})
	if err == nil && commit == nil {
		err = fmt.Errorf("diff %s: no such commit", rev)
	}
	if err != nil {
		return nil, nil, err
	}
	return commit, files, nil
}

// walk is Walk, also handing fn the parsed sides of each commit's Go files.
func (r *Repo) walk(ctx context.Context, opts Options, fn func(*Commit, []fileSides) error) (err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	cf, err := r.startCatFile(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			cancel()
			cf.close()
			return
		}
		err = cf.close()
	}()

	cmd := r.command(ctx, logArgs(opts)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if len(opts.Commits) > 0 {
		cmd.Stdin = strings.NewReader(strings.Join(opts.Commits, "\n") + "\n")
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			cancel() // stop git log early; its exit status no longer matters
			cmd.Wait()
			return
		}
		if werr := cmd.Wait(); werr != nil {
			err = gitError(cmd, werr, &stderr)
		}
	}()

	w := &walker{cf: cf, opts: opts}
	lr := newLogReader(stdout)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		c, err := lr.next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		sides, err := w.analyze(c)
		if err != nil {
			return err
		}
		if err := fn(c, sides); err != nil {
			return err
		}
	}
}

func logArgs(opts Options) []string {
	args := []string{
		"log", "--root", "-z", logFormat,
		"--raw", "--numstat", "--no-abbrev", "-M", "--diff-merges=first-parent",
		// Neutralize user configuration that would change the output.
		"--no-color", "--no-relative", "--no-ext-diff", "--no-textconv",
		"--no-show-signature", "--encoding=UTF-8",
	}
	if len(opts.Commits) > 0 {
		// The hashes are fed on stdin, which keeps a long list off the
		// command line.
		return append(args, "--no-walk=unsorted", "--stdin", "--end-of-options", "--")
	}
	return append(args, rangeArgs(opts)...)
}

// rangeArgs selects the commits of a walk, oldest first, for git log or git
// rev-list.
func rangeArgs(opts Options) []string {
	args := []string{"--reverse", "--topo-order"}
	if opts.FirstParent {
		args = append(args, "--first-parent")
	}
	if opts.MaxCount > 0 {
		args = append(args, "--max-count="+strconv.Itoa(opts.MaxCount))
	}
	const gitDate = "2006-01-02 15:04:05 -0700"
	if !opts.Since.IsZero() {
		args = append(args, "--since="+opts.Since.Format(gitDate))
	}
	if !opts.Until.IsZero() {
		args = append(args, "--until="+opts.Until.Format(gitDate))
	}
	branch := cmp.Or(opts.Branch, "HEAD")
	return append(args, "--end-of-options", branch, "--")
}

// Commits returns the hashes of the commits Walk would visit with opts, in
// the same order, without reading or diffing them.
func (r *Repo) Commits(ctx context.Context, opts Options) ([]string, error) {
	if len(opts.Commits) > 0 {
		return slices.Clone(opts.Commits), nil
	}
	cmd := r.command(ctx, append([]string{"rev-list"}, rangeArgs(opts)...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, gitError(cmd, err, &stderr)
	}
	return strings.Fields(string(out)), nil
}

// walker turns commits from the log into symbol changes.
type walker struct {
	cf   *catFile
	opts Options

	// Parsed blobs from this commit and the previous one. Along a linear
	// history the old side of a file is the new side of the commit before,
	// so most files are read and parsed once.
	cur, prev map[string]parsed
}

type parsed struct {
	ok  bool // the side is a Go file to diff; f may still be nil if it did not parse
	f   *ast.File
	err error
}

// present returns a side as the diff sees it. Unless generated code is
// included, a generated side counts as absent: a file that stops being
// generated adds its symbols, and one that becomes generated deletes them.
func (w *walker) present(p parsed) parsed {
	if w.opts.IncludeGenerated || p.f == nil || !p.f.Generated {
		return p
	}
	return parsed{}
}

// fileSides are both sides of one Go file a commit touched, as parsed.
type fileSides struct {
	fc       *FileChange
	old, new parsed
}

// sides parses both sides of every Go file c touched, generated or not.
func (w *walker) sides(c *Commit) ([]fileSides, error) {
	var out []fileSides
	for i := range c.Files {
		fc := &c.Files[i]
		// A copy leaves its source in place, so only the new side counts.
		oldGo := fc.Status != StatusCopied && isGoFile(fc.OldPath, fc.oldMode)
		newGo := isGoFile(fc.NewPath, fc.newMode)
		if !oldGo && !newGo {
			continue
		}
		s := fileSides{fc: fc}
		var err error
		if oldGo {
			if s.old, err = w.parse(fc.OldPath, fc.oldHash); err != nil {
				return nil, err
			}
		}
		if newGo {
			if s.new, err = w.parse(fc.NewPath, fc.newHash); err != nil {
				return nil, err
			}
		}
		out = append(out, s)
	}
	return out, nil
}

// pkgFiles are the two sides of the Go files a commit touched in one directory.
type pkgFiles struct{ old, new []*ast.File }

// analyze fills in the symbol changes of c and returns the sides it parsed.
func (w *walker) analyze(c *Commit) ([]fileSides, error) {
	w.prev, w.cur = w.cur, map[string]parsed{}
	all, err := w.sides(c)
	if err != nil {
		return nil, err
	}

	pkgs := map[string]*pkgFiles{}
	group := func(p string) *pkgFiles {
		dir := path.Dir(p)
		if pkgs[dir] == nil {
			pkgs[dir] = &pkgFiles{}
		}
		return pkgs[dir]
	}

	goFiles := 0
	for _, s := range all {
		old, new := w.present(s.old), w.present(s.new)
		if !old.ok && !new.ok {
			continue
		}
		goFiles++
		for _, p := range []parsed{old, new} {
			if p.ok && p.err != nil {
				c.ParseErrors = append(c.ParseErrors, p.err)
			}
		}
		if old.f != nil {
			g := group(s.fc.OldPath)
			g.old = append(g.old, old.f)
		}
		if new.f != nil {
			g := group(s.fc.NewPath)
			g.new = append(g.new, new.f)
		}
	}

	c.Changes = diffPackages(pkgs)
	c.FormattingOnly = goFiles > 0 && len(c.Changes) == 0 && len(c.ParseErrors) == 0
	return all, nil
}

// parse reads and parses one blob. The error is for failing to read it; a
// syntax error is reported in parsed.err alongside the recovered symbols.
func (w *walker) parse(path, hash string) (parsed, error) {
	key := hash + "\x00" + path
	if p, ok := w.cur[key]; ok {
		return p, nil
	}
	p, ok := w.prev[key]
	if !ok {
		src, err := w.cf.blob(hash)
		if err != nil {
			return parsed{}, err
		}
		p.f, p.err = ast.ParseSource(path, src)
		p.ok = true
	}
	w.cur[key] = p
	return p, nil
}

func isGoFile(p, mode string) bool {
	// Regular files only: not symlinks (120000) or submodules (160000).
	return strings.HasSuffix(p, ".go") && (mode == "100644" || mode == "100755")
}

// diffPackages diffs each directory on its own, so that symbols sharing a
// package name in different directories (main, internal) are not confused,
// while moves between the files of one package are still found. Symbols left
// added or deleted are then matched across directories, which finds moves
// between packages.
func diffPackages(pkgs map[string]*pkgFiles) []ast.Change {
	var changes []ast.Change
	var added, deleted []*ast.Symbol
	for _, dir := range slices.Sorted(maps.Keys(pkgs)) {
		for _, ch := range ast.Diff(pkgs[dir].old, pkgs[dir].new) {
			switch ch.Flags {
			case ast.Added:
				added = append(added, ch.New)
			case ast.Deleted:
				deleted = append(deleted, ch.Old)
			default:
				changes = append(changes, ch)
			}
		}
	}
	// A symbol left over in one directory has no partner in that directory,
	// so any match this finds crosses directories.
	changes = append(changes, ast.Diff(
		[]*ast.File{{Symbols: deleted}},
		[]*ast.File{{Symbols: added}},
	)...)

	slices.SortStableFunc(changes, func(a, b ast.Change) int {
		sa, sb := cmp.Or(a.New, a.Old), cmp.Or(b.New, b.Old)
		return cmp.Or(cmp.Compare(sa.Path, sb.Path), cmp.Compare(sa.StartLine, sb.StartLine), cmp.Compare(sa.Key, sb.Key))
	})
	return changes
}
