package git

import (
	"bytes"
	"cmp"
	"context"
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
	// header. By default a file generated on either side is ignored.
	IncludeGenerated bool
}

// Walk calls fn for each commit on opts.Branch, parents before children.
// Each commit is diffed against its first parent, and a root commit against
// the empty tree. Walk stops at the first error from fn and returns it.
func (r *Repo) Walk(ctx context.Context, opts Options, fn func(*Commit) error) (err error) {
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
		if err := w.analyze(c); err != nil {
			return err
		}
		if err := fn(c); err != nil {
			return err
		}
	}
}

func logArgs(opts Options) []string {
	args := []string{
		"log", "--reverse", "--topo-order", "--root", "-z", logFormat,
		"--raw", "--numstat", "--no-abbrev", "-M", "--diff-merges=first-parent",
		// Neutralize user configuration that would change the output.
		"--no-color", "--no-relative", "--no-ext-diff", "--no-textconv",
		"--no-show-signature", "--encoding=UTF-8",
	}
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
	f   *ast.File
	err error
}

// pkgFiles are the two sides of the Go files a commit touched in one directory.
type pkgFiles struct{ old, new []*ast.File }

func (w *walker) analyze(c *Commit) error {
	w.prev, w.cur = w.cur, map[string]parsed{}

	pkgs := map[string]*pkgFiles{}
	group := func(p string) *pkgFiles {
		dir := path.Dir(p)
		if pkgs[dir] == nil {
			pkgs[dir] = &pkgFiles{}
		}
		return pkgs[dir]
	}

	goFiles := 0
	for i := range c.Files {
		fc := &c.Files[i]
		// A copy leaves its source in place, so only the new side counts.
		oldGo := fc.Status != StatusCopied && isGoFile(fc.OldPath, fc.oldMode)
		newGo := isGoFile(fc.NewPath, fc.newMode)
		if !oldGo && !newGo {
			continue
		}
		var old, new *ast.File
		if oldGo {
			p, err := w.parse(fc.OldPath, fc.oldHash)
			if err != nil {
				return err
			}
			old = p.f
			if p.err != nil {
				c.ParseErrors = append(c.ParseErrors, p.err)
			}
		}
		if newGo {
			p, err := w.parse(fc.NewPath, fc.newHash)
			if err != nil {
				return err
			}
			new = p.f
			if p.err != nil {
				c.ParseErrors = append(c.ParseErrors, p.err)
			}
		}
		if !w.opts.IncludeGenerated && (old != nil && old.Generated || new != nil && new.Generated) {
			continue
		}
		goFiles++
		if old != nil {
			g := group(fc.OldPath)
			g.old = append(g.old, old)
		}
		if new != nil {
			g := group(fc.NewPath)
			g.new = append(g.new, new)
		}
	}

	c.Changes = diffPackages(pkgs)
	c.FormattingOnly = goFiles > 0 && len(c.Changes) == 0 && len(c.ParseErrors) == 0
	return nil
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
