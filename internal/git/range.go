package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"

	"huginn/internal/ast"
)

// Range is what changed between two commits, as a merge request shows it:
// the head's tree compared with the base's, whatever lies between them.
type Range struct {
	Base, Head string // full commit hashes

	// Files are the Go files that differ, with both sides parsed, as Diff
	// gives a commit's. Changed is every file that differs, Go or not.
	Files   []FileDiff
	Changed []FileChange

	Added, Removed int // text lines over all files

	// ParseErrors holds syntax errors from either side.
	ParseErrors []error
}

// DiffRange compares the trees of the commits base and head, finding renames
// as Walk does. Unlike Diff, it compares any two commits, not a commit and
// its parent; for a merge request, base is the merge base of its head and
// its target branch (see MergeBase).
func (r *Repo) DiffRange(ctx context.Context, base, head string) (_ *Range, err error) {
	rg := &Range{}
	if rg.Base, err = r.resolve(ctx, base, "commit"); err != nil {
		return nil, err
	}
	if rg.Head, err = r.resolve(ctx, head, "commit"); err != nil {
		return nil, err
	}

	cmd := r.command(ctx, "diff", "--raw", "--numstat", "-z", "--no-abbrev", "-M",
		"--no-color", "--no-relative", "--no-ext-diff", "--no-textconv",
		"--end-of-options", rg.Base, rg.Head, "--")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, gitError(cmd, err, &stderr)
	}
	lr := newLogReader(bytes.NewReader(out))
	var c Commit
	if err = lr.files(&c); err != nil {
		return nil, fmt.Errorf("git diff: %w", err)
	}
	if lr.pending != nil {
		return nil, fmt.Errorf("git diff: unexpected entry %q", *lr.pending)
	}
	rg.Changed, rg.Added, rg.Removed = c.Files, c.Added, c.Removed

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cf, err := r.startCatFile(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			cancel()
			_ = cf.close() // err already says what went wrong
			return
		}
		err = cf.close()
	}()

	// Read every side first, then parse them on all cores.
	var blobs []*blob
	side := func(path, mode, hash string) *blob {
		if !isGoFile(path, mode) {
			return nil
		}
		b := &blob{hash: hash, path: path, done: make(chan struct{})}
		blobs = append(blobs, b)
		return b
	}
	type sides struct{ old, new *blob }
	var files []sides
	var changes []*FileChange
	for i := range rg.Changed {
		fc := &rg.Changed[i]
		var s sides
		// A copy leaves its source in place, so only the new side counts.
		if fc.Status != StatusCopied {
			s.old = side(fc.OldPath, fc.oldMode, fc.oldHash)
		}
		s.new = side(fc.NewPath, fc.newMode, fc.newHash)
		if s.old == nil && s.new == nil {
			continue
		}
		files = append(files, s)
		changes = append(changes, fc)
	}
	src := map[*blob][]byte{} // parsing lets go of a blob's contents
	for _, b := range blobs {
		if b.src, err = cf.blob(b.hash); err != nil {
			return nil, err
		}
		src[b] = b.src
	}
	parseAll(blobs)

	for i, s := range files {
		fd := FileDiff{FileChange: *changes[i]}
		for _, x := range []struct {
			b   *blob
			f   **ast.File
			src *[]byte
		}{{s.old, &fd.Old, &fd.OldSource}, {s.new, &fd.New, &fd.NewSource}} {
			p, _ := x.b.result() // the blobs were read above
			*x.f = p.f
			if x.b != nil {
				*x.src = src[x.b]
			}
			if p.err != nil {
				rg.ParseErrors = append(rg.ParseErrors, p.err)
			}
		}
		rg.Files = append(rg.Files, fd)
	}
	return rg, nil
}

// parseAll parses blobs whose contents have been read, on every core.
func parseAll(blobs []*blob) {
	work := make(chan *blob)
	var wg sync.WaitGroup
	for range min(runtime.GOMAXPROCS(0), len(blobs)) {
		wg.Go(func() {
			for b := range work {
				b.parse()
			}
		})
	}
	for _, b := range blobs {
		work <- b
	}
	close(work)
	wg.Wait()
}

// MergeBase returns the best common ancestor of the commits a and b, where a
// branch started from the other: what a merge request from one into the
// other is compared against.
func (r *Repo) MergeBase(ctx context.Context, a, b string) (string, error) {
	cmd := r.command(ctx, "merge-base", "--end-of-options", a, b)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if stderr.Len() == 0 {
			// git merge-base exits 1 without a word when there is none.
			return "", fmt.Errorf("%s and %s have no common ancestor; in a shallow clone, fetch more history", a, b)
		}
		return "", gitError(cmd, err, &stderr)
	}
	return strings.TrimSpace(string(out)), nil
}

// DefaultBranch returns the branch merge requests usually target: the
// remote's default branch as origin/HEAD names it, or else the first of
// origin/main, origin/master, main and master that exists.
func (r *Repo) DefaultBranch(ctx context.Context) (string, error) {
	cmd := r.command(ctx, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD")
	if out, err := cmd.Output(); err == nil {
		if ref := strings.TrimSpace(string(out)); ref != "" {
			return ref, nil
		}
	}
	for _, ref := range []string{"origin/main", "origin/master", "main", "master"} {
		if _, err := r.resolve(ctx, ref, "commit"); err == nil {
			return ref, nil
		}
	}
	return "", errors.New("no default branch: none of origin/HEAD, origin/main, origin/master, main and master exists")
}

// CurrentBranch returns the name of the branch checked out, or "" when the
// head is detached.
func (r *Repo) CurrentBranch(ctx context.Context) (string, error) {
	cmd := r.command(ctx, "symbolic-ref", "--quiet", "--short", "HEAD")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if stderr.Len() == 0 {
			return "", nil // exits 1 without a word when detached
		}
		return "", gitError(cmd, err, &stderr)
	}
	return strings.TrimSpace(string(out)), nil
}

// Branches returns the names of the local and remote-tracking branches,
// those committed to most recently first, without the remotes' HEADs.
func (r *Repo) Branches(ctx context.Context) ([]string, error) {
	cmd := r.command(ctx, "for-each-ref", "--sort=-committerdate", "--format=%(refname)", "refs/heads", "refs/remotes")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, gitError(cmd, err, &stderr)
	}
	var names []string
	for _, ref := range strings.Fields(string(out)) {
		if strings.HasSuffix(ref, "/HEAD") {
			continue
		}
		name, ok := strings.CutPrefix(ref, "refs/heads/")
		if !ok {
			name = strings.TrimPrefix(ref, "refs/remotes/")
		}
		names = append(names, name)
	}
	return names, nil
}

// GoDirs returns the slash-separated directories that hold regular .go
// files at rev, sorted, with "." for the root.
func (r *Repo) GoDirs(ctx context.Context, rev string) ([]string, error) {
	tree, err := r.resolve(ctx, rev, "tree")
	if err != nil {
		return nil, err
	}
	cmd := r.command(ctx, "ls-tree", "-r", "-z", "--full-tree", tree)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, gitError(cmd, err, &stderr)
	}
	seen := map[string]bool{}
	var dirs []string
	for entry := range strings.SplitSeq(strings.TrimSuffix(string(out), "\x00"), "\x00") {
		// "<mode> <type> <object>\t<path>"
		meta, p, ok := strings.Cut(entry, "\t")
		if f := strings.Fields(meta); !ok || len(f) != 3 || !isGoFile(p, f[0]) {
			continue
		}
		if d := path.Dir(p); !seen[d] {
			seen[d] = true
			dirs = append(dirs, d)
		}
	}
	slices.Sort(dirs)
	return dirs, nil
}

// Toplevel returns the absolute path of the root of the working tree.
func (r *Repo) Toplevel(ctx context.Context) (string, error) {
	cmd := r.command(ctx, "rev-parse", "--show-toplevel")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", gitError(cmd, err, &stderr)
	}
	return filepath.FromSlash(strings.TrimSpace(string(out))), nil
}
