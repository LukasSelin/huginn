package git

import (
	"bytes"
	"context"
	"os"
)

// Checkout writes the files of commit rev to a new temporary directory, for
// tools that need a working tree, and returns it with a function removing
// it. The directory is a clone that borrows the repository's objects, so
// nothing is copied but the files, and the repository itself is left as it
// is. rev is best a full commit hash: the clone has none of the
// repository's local branches.
func (r *Repo) Checkout(ctx context.Context, rev string) (dir string, remove func(), err error) {
	gitDir, err := r.GitDir(ctx)
	if err != nil {
		return "", nil, err
	}
	dir, err = os.MkdirTemp("", "huginn-checkout-")
	if err != nil {
		return "", nil, err
	}
	remove = func() { _ = os.RemoveAll(dir) }
	run := func(args ...string) error {
		cmd := r.command(ctx, args...)
		// Large files kept in Git LFS stay pointers rather than being
		// downloaded: the tools reading the checkout want the code.
		cmd.Env = append(os.Environ(), "GIT_LFS_SKIP_SMUDGE=1")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return gitError(cmd, err, &stderr)
		}
		return nil
	}
	if err := run("clone", "--quiet", "--shared", "--no-checkout", "--", gitDir, dir); err != nil {
		remove()
		return "", nil, err
	}
	if err := run("-C", dir, "-c", "advice.detachedHead=false", "checkout", "--quiet", "--detach", rev, "--"); err != nil {
		remove()
		return "", nil, err
	}
	return dir, remove, nil
}
