package lineage

import (
	"bufio"
	"encoding/gob"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"huginn/internal/ast"
)

// cacheVersion identifies the cache format. Bump it whenever the encoding
// changes, or the ast or git packages start producing different changes or
// symbols for the same commit, so that stale caches are discarded rather than
// trusted.
//
// Worktrees share the cache, and two branches may bump the version to the
// same number for different changes, so the header also carries the shape of
// the encoded types: a cache from a build whose ast types differ is discarded
// even when its version matches. Version 4 was used that way by two
// branches; this one skips it.
const cacheVersion = 5

// cacheShape fingerprints the encoded types, field by field.
var cacheShape = func() string {
	h := fnv.New64a()
	h.Write([]byte(typeShape(reflect.TypeOf(cacheBody{}), map[reflect.Type]bool{})))
	return fmt.Sprintf("%016x", h.Sum64())
}()

// typeShape describes a type by the names and shapes of its exported
// fields, as gob encodes them.
func typeShape(t reflect.Type, seen map[reflect.Type]bool) string {
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array:
		return t.Kind().String() + "(" + typeShape(t.Elem(), seen) + ")"
	case reflect.Map:
		return "map(" + typeShape(t.Key(), seen) + "," + typeShape(t.Elem(), seen) + ")"
	case reflect.Struct:
		if seen[t] {
			return t.String()
		}
		seen[t] = true
		var fields []string
		for i := range t.NumField() {
			if f := t.Field(i); f.IsExported() {
				fields = append(fields, f.Name+" "+typeShape(f.Type, seen))
			}
		}
		return t.String() + "{" + strings.Join(fields, "; ") + "}"
	default:
		return t.String()
	}
}

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

// Dir returns the directory the cache keeps its files in, where callers may
// keep files of their own that derive from the same history.
func (c *Cache) Dir() string { return c.dir }

// cacheHeader is decoded before anything else, so that a cache from another
// version is discarded without decoding the rest.
type cacheHeader struct {
	Version          int
	IncludeGenerated bool
	Shape            string // cacheShape
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
	Subject string
	Detail  string
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
	defer func() { _ = f.Close() }() // read only: nothing to lose
	dec := gob.NewDecoder(bufio.NewReader(f))
	var h cacheHeader
	var b cacheBody
	if dec.Decode(&h) != nil || h != (cacheHeader{cacheVersion, includeGenerated, cacheShape}) || dec.Decode(&b) != nil {
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
		r := Revision{SHA: cr.SHA, Time: cr.Time, Author: cr.Author, Subject: cr.Subject, Detail: cr.Detail}
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
		cr := cachedRevision{SHA: r.SHA, Time: r.Time, Author: r.Author, Subject: r.Subject, Detail: r.Detail}
		for _, ch := range r.Changes {
			cr.Changes = append(cr.Changes, cachedChange{Flags: ch.Flags, Old: ref(ch.Old), New: ref(ch.New), Similarity: ch.Similarity})
		}
		b.Revisions = append(b.Revisions, cr)
	}

	if err = os.MkdirAll(c.dir, 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(c.dir, "revisions-*.tmp")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			// Best effort: err already says what went wrong.
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	w := bufio.NewWriter(tmp)
	enc := gob.NewEncoder(w)
	if err := enc.Encode(cacheHeader{cacheVersion, includeGenerated, cacheShape}); err != nil {
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
