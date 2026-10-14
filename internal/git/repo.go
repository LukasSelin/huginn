// Package git reads commit history from a repository and reduces each commit
// to the symbol-level changes it made to Go code.
//
// It drives the git command-line tool rather than reimplementing the object
// format: one git log process streams commit metadata and changed paths, and
// one git cat-file --batch process serves blob contents. git 2.31 or later is
// required.
package git

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Repo is a git repository on disk.
type Repo struct {
	dir string
	git string // path to the git executable
}

// Open returns the repository containing dir.
func Open(dir string) (*Repo, error) {
	exe, err := exec.LookPath("git")
	if err != nil {
		return nil, err
	}
	r := &Repo{dir: dir, git: exe}
	cmd := r.command(context.Background(), "rev-parse", "--git-dir")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("open %s: %w", dir, gitError(cmd, err, &stderr))
	}
	return r, nil
}

// GitDir returns the absolute path of the repository's git directory. For a
// linked worktree it is the main repository's, which all worktrees share.
func (r *Repo) GitDir(ctx context.Context) (string, error) {
	cmd := r.command(ctx, "rev-parse", "--path-format=absolute", "--git-common-dir")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", gitError(cmd, err, &stderr)
	}
	return filepath.FromSlash(strings.TrimSpace(string(out))), nil
}

func (r *Repo) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, r.git, args...)
	cmd.Dir = r.dir
	return cmd
}

// gitError adds git's own diagnostics to the error of a failed command.
func gitError(cmd *exec.Cmd, err error, stderr *bytes.Buffer) error {
	msg := strings.TrimSpace(stderr.String())
	if msg == "" {
		return fmt.Errorf("git %s: %w", cmd.Args[1], err)
	}
	return fmt.Errorf("git %s: %w: %s", cmd.Args[1], err, msg)
}

// catFile reads objects through a long-running git cat-file --batch process.
// blob makes one request at a time, so the pipes cannot deadlock. To keep
// git busy instead, one goroutine can write many object names with request
// while another reads the replies, in the same order, with readBlob.
type catFile struct {
	cmd    *exec.Cmd
	in     io.WriteCloser
	out    *bufio.Reader
	stderr bytes.Buffer
}

func (r *Repo) startCatFile(ctx context.Context) (*catFile, error) {
	c := &catFile{cmd: r.command(ctx, "cat-file", "--batch")}
	c.cmd.Stderr = &c.stderr
	var err error
	if c.in, err = c.cmd.StdinPipe(); err != nil {
		return nil, err
	}
	out, err := c.cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	c.out = bufio.NewReaderSize(out, 64<<10)
	if err := c.cmd.Start(); err != nil {
		return nil, err
	}
	return c, nil
}

// blob returns the contents of the blob with the given object name.
func (c *catFile) blob(name string) ([]byte, error) {
	if err := c.request(name); err != nil {
		return nil, err
	}
	return c.readBlob()
}

// request asks for the given objects in one write.
func (c *catFile) request(names ...string) error {
	n := 0
	for _, name := range names {
		n += len(name) + 1
	}
	buf := make([]byte, 0, n)
	for _, name := range names {
		buf = append(append(buf, name...), '\n')
	}
	if _, err := c.in.Write(buf); err != nil {
		return fmt.Errorf("git cat-file: %w", err)
	}
	return nil
}

// readBlob reads the reply to the oldest request not yet read, which must be
// for a blob.
func (c *catFile) readBlob() ([]byte, error) {
	header, err := c.out.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("git cat-file: %w", err)
	}
	// "<name> <type> <size>", or "<name> missing".
	f := strings.Fields(header)
	if len(f) != 3 || f[1] != "blob" {
		return nil, fmt.Errorf("git cat-file: unexpected reply %q", strings.TrimSpace(header))
	}
	size, err := strconv.Atoi(f[2])
	if err != nil {
		return nil, fmt.Errorf("git cat-file: bad size in %q", strings.TrimSpace(header))
	}
	buf := make([]byte, size+1) // contents and a trailing newline
	if _, err := io.ReadFull(c.out, buf); err != nil {
		return nil, fmt.Errorf("git cat-file: %w", err)
	}
	return buf[:size], nil
}

func (c *catFile) close() error {
	c.in.Close()
	if err := c.cmd.Wait(); err != nil {
		return gitError(c.cmd, err, &c.stderr)
	}
	return nil
}
