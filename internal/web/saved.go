package web

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// The prebuilt responses are saved next to the revision cache, keyed on the
// history they were built from, so that the next serve of the same history
// loads them instead of encoding and compressing them again. Only the last
// history's responses are kept.

// savedVersion identifies the saved format. Bump it whenever the encoding
// changes, or a prebuilt response would come out differently for the same
// history, so that a stale file is discarded rather than served.
const savedVersion = 2

// savedHeader is decoded before anything else, so that a file from another
// version or another history is discarded without decoding the rest.
type savedHeader struct {
	Version int
	Key     string
}

// savedResponse is one prebuilt response. Body is kept only when gzip did
// not make it smaller: the pages always accept gzip, and the rest of the
// file would be mostly uncompressed JSON.
type savedResponse struct {
	Key         string // the request key, see requestKey
	Body, Gz    []byte
	Ctype, ETag string
}

// savedFile is where the responses are saved, or "" when the server has no
// cache to keep them in.
func (s *Server) savedFile() string {
	if s.opts.Cache == nil {
		return ""
	}
	return filepath.Join(s.opts.Cache.Dir(), "responses.gob")
}

// historyKey names everything the prebuilt responses depend on: the commits
// indexed, the tags the story reads, and the options that filter them.
func (s *Server) historyKey(ctx context.Context) (string, error) {
	walk := s.opts.Options
	walk.FirstParent = true
	hashes, err := s.repo.Commits(ctx, walk)
	if err != nil {
		return "", err
	}
	tags, err := s.repo.Tags(ctx, walk)
	if err != nil {
		return "", err
	}
	sum := sha256.New()
	fmt.Fprintf(sum, "v%d tests=%t generated=%t\n", savedVersion, s.opts.Tests, walk.IncludeGenerated)
	for _, h := range hashes {
		fmt.Fprintln(sum, h)
	}
	for _, t := range tags {
		fmt.Fprintf(sum, "tag %s %s %s %d\n", t.Name, t.Commit, t.Mainline, t.Time.Unix())
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

// loadSaved returns the responses saved for the history named key. A
// missing, unreadable or outdated file holds none.
func (s *Server) loadSaved(key string) map[string]*encoded {
	name := s.savedFile()
	if name == "" || key == "" {
		return nil
	}
	f, err := os.Open(name)
	if err != nil {
		return nil
	}
	defer f.Close()
	dec := gob.NewDecoder(bufio.NewReader(f))
	var h savedHeader
	var rs []savedResponse
	if dec.Decode(&h) != nil || h != (savedHeader{savedVersion, key}) || dec.Decode(&rs) != nil {
		return nil
	}
	out := make(map[string]*encoded, len(rs))
	for _, r := range rs {
		if r.Gz == nil && r.Body == nil {
			r.Body = []byte{} // gob decodes an empty body as nil
		}
		out[r.Key] = &encoded{body: r.Body, gz: r.Gz, ctype: r.Ctype, etag: r.ETag}
	}
	return out
}

// save replaces the saved responses with those prebuilt in ix, for the
// history named key. The file is written aside and renamed into place, so a
// concurrent reader sees the old file or the new one.
func (s *Server) save(key string, ix *index) (err error) {
	name := s.savedFile()
	if name == "" || key == "" {
		return nil
	}
	var rs []savedResponse
	ix.pre.Range(func(k, v any) bool {
		e := v.(*encoded)
		sr := savedResponse{Key: k.(string), Gz: e.gz, Ctype: e.ctype, ETag: e.etag}
		if e.body != nil && len(e.gz) >= len(e.body) {
			sr.Body, sr.Gz = e.body, nil
		}
		rs = append(rs, sr)
		return true
	})

	dir := filepath.Dir(name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "responses-*.tmp")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	w := bufio.NewWriter(tmp)
	enc := gob.NewEncoder(w)
	if err := enc.Encode(savedHeader{savedVersion, key}); err != nil {
		return err
	}
	if err := enc.Encode(rs); err != nil {
		return err
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), name)
}
