package lineage

import (
	"bufio"
	"encoding/gob"
	"os"
	"path/filepath"
	"time"

	"huginn/internal/ast"
)

// cacheVersion identifies the cache format. Bump it whenever the encoding
// changes, or the ast or git packages start producing different changes for
// the same commit, so that stale caches are discarded rather than trusted.
const cacheVersion = 2

// Cache keeps walked revisions on disk, keyed by commit hash, so that a later
// Build only walks the commits it has not seen. A revision depends only on
// its commit, so entries stay valid as branches move; they are never evicted.
//
// Symbols read back from the cache lack the private data ast.Diff matches
// with, so they must not be diffed again.
type Cache struct {
	dir string

	// Hits and Walked count the commits the last Build read from the cache
	// and had to walk.
	Hits, Walked int
}

// NewCache returns a cache that stores its files in dir, typically
// .git/huginn. The directory is created on first write.
func NewCache(dir string) *Cache { return &Cache{dir: dir} }

// cacheHeader is decoded before anything else, so that a cache from another
// version is discarded without decoding the rest.
type cacheHeader struct {
	Version          int
	IncludeGenerated bool
}

// cacheBody stores each distinct symbol once: along a linear history the new
// side of one commit is often the old side of a later one.
type cacheBody struct {
	Symbols   []ast.Symbol
	Revisions []cachedRevision
}

type cachedRevision struct {
	SHA     string
	Time    time.Time
	Author  string
	Changes []cachedChange
}

type cachedChange struct {
	Flags      ast.ChangeFlags
	Old, New   int // 1-based index into Symbols; 0 for none
	Similarity float64
}

// file is where revisions walked with or without generated code are kept.
// The two never mix, since they hold different changes for the same commit.
func (c *Cache) file(includeGenerated bool) string {
	if includeGenerated {
		return filepath.Join(c.dir, "revisions-generated.gob")
	}
	return filepath.Join(c.dir, "revisions.gob")
}

// load returns the cached revisions by commit hash. A missing, unreadable or
// outdated cache is empty.
func (c *Cache) load(includeGenerated bool) map[string]Revision {
	revs := map[string]Revision{}
	f, err := os.Open(c.file(includeGenerated))
	if err != nil {
		return revs
	}
	defer f.Close()
	dec := gob.NewDecoder(bufio.NewReader(f))
	var h cacheHeader
	var b cacheBody
	if dec.Decode(&h) != nil || h != (cacheHeader{cacheVersion, includeGenerated}) || dec.Decode(&b) != nil {
		return revs
	}
	sym := func(i int) (*ast.Symbol, bool) {
		if i == 0 {
			return nil, true
		}
		if i < 0 || i > len(b.Symbols) {
			return nil, false
		}
		return &b.Symbols[i-1], true
	}
	for _, cr := range b.Revisions {
		r := Revision{SHA: cr.SHA, Time: cr.Time, Author: cr.Author}
		for _, cc := range cr.Changes {
			old, ok1 := sym(cc.Old)
			new, ok2 := sym(cc.New)
			if !ok1 || !ok2 {
				return map[string]Revision{} // corrupt: start over
			}
			r.Changes = append(r.Changes, ast.Change{Flags: cc.Flags, Old: old, New: new, Similarity: cc.Similarity})
		}
		revs[r.SHA] = r
	}
	return revs
}

// save replaces the cache with revs. The file is written aside and renamed
// into place, so a concurrent reader sees the old cache or the new one.
func (c *Cache) save(includeGenerated bool, revs map[string]Revision) (err error) {
	var b cacheBody
	index := map[*ast.Symbol]int{}
	ref := func(s *ast.Symbol) int {
		if s == nil {
			return 0
		}
		if i, ok := index[s]; ok {
			return i
		}
		b.Symbols = append(b.Symbols, *s)
		index[s] = len(b.Symbols)
		return len(b.Symbols)
	}
	for _, r := range revs {
		cr := cachedRevision{SHA: r.SHA, Time: r.Time, Author: r.Author}
		for _, ch := range r.Changes {
			cr.Changes = append(cr.Changes, cachedChange{Flags: ch.Flags, Old: ref(ch.Old), New: ref(ch.New), Similarity: ch.Similarity})
		}
		b.Revisions = append(b.Revisions, cr)
	}

	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(c.dir, "revisions-*.tmp")
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
	if err := enc.Encode(cacheHeader{cacheVersion, includeGenerated}); err != nil {
		return err
	}
	if err := enc.Encode(b); err != nil {
		return err
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), c.file(includeGenerated))
}
