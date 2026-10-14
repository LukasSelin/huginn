package git

import (
	"bytes"
	"context"
	"fmt"
	"path"
	"strings"

	"huginn/internal/ast"
)

// Tree is the Go code of a repository at one revision.
type Tree struct {
	// Files are the regular .go files, test and generated files included,
	// sorted by path. Generated ones are marked by File.Generated. A file
	// that did not parse at all is left out.
	Files []*ast.File

	// GoMod holds the contents of the go.mod files, keyed by their
	// slash-separated repository path ("go.mod", "tools/go.mod").
	GoMod map[string][]byte

	// ParseErrors holds syntax errors. Files with errors keep the symbols
	// that could be recovered.
	ParseErrors []error
}

// Tree reads and parses every Go file and go.mod at rev.
func (r *Repo) Tree(ctx context.Context, rev string) (_ *Tree, err error) {
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

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cf, err := r.startCatFile(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			cancel()
			cf.close()
			return
		}
		err = cf.close()
	}()

	t := &Tree{GoMod: map[string][]byte{}}
	for entry := range strings.SplitSeq(strings.TrimSuffix(string(out), "\x00"), "\x00") {
		if entry == "" {
			continue
		}
		// "<mode> <type> <object>\t<path>", sorted by path.
		meta, p, ok := strings.Cut(entry, "\t")
		f := strings.Fields(meta)
		if !ok || len(f) != 3 {
			return nil, fmt.Errorf("git ls-tree: bad entry %q", entry)
		}
		mode, hash := f[0], f[2]
		isMod := path.Base(p) == "go.mod" && (mode == "100644" || mode == "100755")
		if !isMod && !isGoFile(p, mode) {
			continue
		}
		src, err := cf.blob(hash)
		if err != nil {
			return nil, err
		}
		if isMod {
			t.GoMod[p] = src
			continue
		}
		file, perr := ast.ParseSource(p, src)
		if perr != nil {
			t.ParseErrors = append(t.ParseErrors, perr)
		}
		if file != nil {
			t.Files = append(t.Files, file)
		}
	}
	return t, nil
}

// resolve returns the name of the object of type typ ("commit", "tree") that
// rev refers to.
func (r *Repo) resolve(ctx context.Context, rev, typ string) (string, error) {
	cmd := r.command(ctx, "rev-parse", "--verify", "--quiet", "--end-of-options", rev+"^{"+typ+"}")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if stderr.Len() == 0 {
			return "", fmt.Errorf("%s is not a %s", rev, typ)
		}
		return "", gitError(cmd, err, &stderr)
	}
	return strings.TrimSpace(string(out)), nil
}
