// Package gittest builds throwaway git repositories for tests.
package gittest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Repo is a throwaway repository whose commits are a day apart, starting on
// 2026-01-01.
type Repo struct {
	T    testing.TB
	Dir  string
	Days int // days since 2025-12-31 of the latest commit; bump it to space commits further
}

// New creates an empty repository on branch master, committing as Ada. It
// skips the test if git is not installed.
func New(t testing.TB) *Repo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	// Keep the user's and the system's configuration (autocrlf, hooks,
	// signing) out of the test.
	global := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(global, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	r := &Repo{T: t, Dir: t.TempDir()}
	r.Git("init", "-q", "-b", "master")
	r.Git("config", "user.name", "Ada")
	r.Git("config", "user.email", "ada@example.com")
	return r
}

// Date returns the author and committer date of the commit made on day.
func (r *Repo) Date(day int) time.Time {
	return time.Date(2026, 1, day, 12, 0, 0, 0, time.UTC)
}

// Git runs git in the repository, dated as of r.Days, and returns its
// trimmed output. It fails the test if git fails.
func (r *Repo) Git(args ...string) string {
	r.T.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.Dir
	date := r.Date(r.Days).Format(time.RFC3339)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.T.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// Commit writes files, deleting those whose content is empty, commits
// everything a day after the previous commit and returns the commit hash.
func (r *Repo) Commit(msg string, files map[string]string) string {
	r.T.Helper()
	r.Days++
	for name, src := range files {
		p := filepath.Join(r.Dir, filepath.FromSlash(name))
		if src == "" {
			if err := os.Remove(p); err != nil {
				r.T.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			r.T.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			r.T.Fatal(err)
		}
	}
	r.Git("add", "-A")
	r.Git("commit", "-q", "--allow-empty", "-m", msg)
	return r.Git("rev-parse", "HEAD")
}
