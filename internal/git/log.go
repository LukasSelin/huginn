package git

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// logFormat is the per-commit header. With -z every field is NUL-terminated,
// so messages may contain anything but NUL. The fixed "commit " prefix keeps
// a header from being mistaken for a raw (":...") or numstat ("12\t3\t...")
// entry of the previous commit.
const logFormat = "--format=commit %H%x00%P%x00%aN%x00%aE%x00%aI%x00%cI%x00%B"

// logReader parses the output of git log --raw --numstat -z with logFormat.
type logReader struct {
	r       *bufio.Reader
	pending *string // first token of the next commit, read ahead
}

func newLogReader(r io.Reader) *logReader {
	return &logReader{r: bufio.NewReaderSize(r, 64<<10)}
}

// token returns the next NUL-terminated field. io.EOF means the stream ended
// cleanly between fields.
func (lr *logReader) token() (string, error) {
	if lr.pending != nil {
		tok := *lr.pending
		lr.pending = nil
		return tok, nil
	}
	tok, err := lr.r.ReadString(0)
	switch {
	case err == io.EOF && tok == "":
		return "", io.EOF
	case err == io.EOF:
		return "", io.ErrUnexpectedEOF
	case err != nil:
		return "", err
	}
	return tok[:len(tok)-1], nil
}

// mustToken is token for fields that are required to follow.
func (lr *logReader) mustToken() (string, error) {
	tok, err := lr.token()
	if err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	return tok, err
}

// next returns the next commit, or io.EOF after the last one.
func (lr *logReader) next() (*Commit, error) {
	head, err := lr.token()
	if err != nil {
		return nil, err
	}
	hash, ok := strings.CutPrefix(head, "commit ")
	if !ok {
		return nil, fmt.Errorf("git log: expected commit header, got %q", head)
	}
	// Parents, author name, author email, author date, committer date, message.
	var f [6]string
	for i := range f {
		if f[i], err = lr.mustToken(); err != nil {
			return nil, fmt.Errorf("git log: commit %s: %w", hash, err)
		}
	}
	c := &Commit{
		Hash:    hash,
		Parents: strings.Fields(f[0]),
		Author:  f[1],
		Email:   f[2],
		Message: strings.TrimRight(f[5], "\n"),
	}
	if c.AuthorTime, err = time.Parse(time.RFC3339, f[3]); err != nil {
		return nil, fmt.Errorf("git log: commit %s: %w", hash, err)
	}
	if c.CommitTime, err = time.Parse(time.RFC3339, f[4]); err != nil {
		return nil, fmt.Errorf("git log: commit %s: %w", hash, err)
	}

	// Raw entries come first, then one numstat entry per raw entry in the
	// same order. Anything else starts the next commit.
	stats := 0
	for {
		tok, err := lr.token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		tok = strings.TrimPrefix(tok, "\n") // git separates the header from the diff with a newline
		switch {
		case strings.HasPrefix(tok, ":"):
			fc, err := lr.raw(tok)
			if err != nil {
				return nil, fmt.Errorf("git log: commit %s: %w", hash, err)
			}
			c.Files = append(c.Files, fc)
		case tok != "" && (tok[0] == '-' || '0' <= tok[0] && tok[0] <= '9'):
			if stats >= len(c.Files) {
				return nil, fmt.Errorf("git log: commit %s: numstat entry without raw entry", hash)
			}
			if err := lr.numstat(tok, &c.Files[stats]); err != nil {
				return nil, fmt.Errorf("git log: commit %s: %w", hash, err)
			}
			c.Added += c.Files[stats].Added
			c.Removed += c.Files[stats].Removed
			stats++
		default:
			lr.pending = &tok
			return c, nil
		}
	}
	return c, nil
}

// raw parses ":oldmode newmode oldhash newhash status" and its paths.
func (lr *logReader) raw(tok string) (FileChange, error) {
	f := strings.Fields(tok[1:])
	if len(f) != 5 || f[4] == "" {
		return FileChange{}, fmt.Errorf("bad raw entry %q", tok)
	}
	fc := FileChange{
		Status:  Status(f[4][0]),
		oldMode: f[0], newMode: f[1],
		oldHash: f[2], newHash: f[3],
	}
	path, err := lr.mustToken()
	if err != nil {
		return fc, err
	}
	switch fc.Status {
	case StatusRenamed, StatusCopied:
		fc.OldPath = path
		if fc.NewPath, err = lr.mustToken(); err != nil {
			return fc, err
		}
	case StatusAdded:
		fc.NewPath = path
	case StatusDeleted:
		fc.OldPath = path
	default:
		fc.OldPath, fc.NewPath = path, path
	}
	return fc, nil
}

// numstat parses "added\tremoved\tpath" into fc. Renames and copies leave the
// path empty and follow with the old and new paths as separate fields.
func (lr *logReader) numstat(tok string, fc *FileChange) error {
	f := strings.SplitN(tok, "\t", 3)
	if len(f) != 3 {
		return fmt.Errorf("bad numstat entry %q", tok)
	}
	path := f[2]
	if path == "" {
		if _, err := lr.mustToken(); err != nil {
			return err
		}
		var err error
		if path, err = lr.mustToken(); err != nil {
			return err
		}
	}
	if path != fc.path() {
		return fmt.Errorf("numstat entry for %q does not match raw entry for %q", path, fc.path())
	}
	if f[0] == "-" && f[1] == "-" {
		fc.Binary = true
		return nil
	}
	var err1, err2 error
	fc.Added, err1 = strconv.Atoi(f[0])
	fc.Removed, err2 = strconv.Atoi(f[1])
	if err := errors.Join(err1, err2); err != nil {
		return fmt.Errorf("bad numstat entry %q: %w", tok, err)
	}
	return nil
}
