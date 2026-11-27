package web

import (
	"errors"
	"net/http"
	"strconv"
	"sync"

	"huginn/internal/affinity"
	"huginn/internal/lineage"
)

// candidateTop caps the types the affinity page lists to pick from.
const candidateTop = 500

// affinityState is an index's affinity analyzer, made on first use, and the
// reports it made, kept per type and window. Its zero value is ready.
type affinityState struct {
	once sync.Once
	an   *affinity.Analyzer

	mu      sync.Mutex
	types   map[int]*memo[[]affinity.Candidate]
	reports map[affinityKey]*memo[*affinity.Report]
}

type affinityKey struct {
	id     lineage.ID
	months int
}

// memo is a value computed once.
type memo[T any] struct {
	once sync.Once
	v    T
	err  error
}

func (m *memo[T]) get(f func() (T, error)) (T, error) {
	m.once.Do(func() { m.v, m.err = f() })
	return m.v, m.err
}

// entry returns the memo for k in *ms, adding it if needed.
func entry[K comparable, T any](mu *sync.Mutex, ms *map[K]*memo[T], k K) *memo[T] {
	mu.Lock()
	defer mu.Unlock()
	if *ms == nil {
		*ms = map[K]*memo[T]{}
	}
	m := (*ms)[k]
	if m == nil {
		m = &memo[T]{}
		(*ms)[k] = m
	}
	return m
}

func (s *Server) analyzer(ix *index) *affinity.Analyzer {
	st := &ix.affinity
	st.once.Do(func() { st.an = affinity.New(ix.h, s.opts.Tests) })
	return st.an
}

// affinityTypes serves the struct types that look like they do too much,
// for the window of history the months parameter gives.
func (s *Server) affinityTypes(ix *index, w http.ResponseWriter, r *http.Request) {
	months, ok := monthsParam(w, r)
	if !ok {
		return
	}
	an := s.analyzer(ix)
	cs, _ := entry(&ix.affinity.mu, &ix.affinity.types, months).get(func() ([]affinity.Candidate, error) {
		return an.Candidates(affinity.Options{Months: months, Top: candidateTop}), nil
	})
	writeJSON(w, cs)
}

// affinityAPI serves the affinity report of the type with the ID the type
// parameter gives, or of the one the name parameter names.
func (s *Server) affinityAPI(ix *index, w http.ResponseWriter, r *http.Request) {
	months, ok := monthsParam(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	idParam, name := q.Get("type"), q.Get("name")
	if (idParam == "") == (name == "") {
		http.Error(w, "want either type or name", http.StatusBadRequest)
		return
	}
	an := s.analyzer(ix)
	var id lineage.ID
	if idParam != "" {
		n, err := strconv.ParseInt(idParam, 10, 64)
		if err != nil {
			http.Error(w, "bad type ID", http.StatusBadRequest)
			return
		}
		id = lineage.ID(n)
	} else {
		var err error
		if id, err = an.Lookup(name); err != nil {
			affinityError(w, err)
			return
		}
	}
	rep, err := entry(&ix.affinity.mu, &ix.affinity.reports, affinityKey{id, months}).get(func() (*affinity.Report, error) {
		return an.Report(id, affinity.Options{Months: months})
	})
	if err != nil {
		affinityError(w, err)
		return
	}
	writeJSON(w, rep)
}

func affinityError(w http.ResponseWriter, err error) {
	var ambiguous *affinity.AmbiguousError
	switch {
	case errors.Is(err, affinity.ErrNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.As(err, &ambiguous):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// monthsParam reads the window of history in months, 12 by default and 0
// for all of it, answering a bad request for anything outside 0 to 120.
func monthsParam(w http.ResponseWriter, r *http.Request) (int, bool) {
	p := r.URL.Query().Get("months")
	if p == "" {
		return 12, true
	}
	n, err := strconv.Atoi(p)
	if err != nil || n < 0 || n > 120 {
		http.Error(w, "months must be 0 to 120", http.StatusBadRequest)
		return 0, false
	}
	return n, true
}
