package web

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"strings"

	"huginn/internal/review"
	"huginn/internal/semdiff"
)

// reviewJSON is a merge request's report, as the review page shows it.
type reviewJSON struct {
	Head      string   `json:"head"`     // as asked for, or "HEAD"
	HeadName  string   `json:"headName"` // with its short hash
	HeadHash  string   `json:"headHash"`
	Target    string   `json:"target"`
	MergeBase string   `json:"mergeBase"`
	Commits   int      `json:"commits"`
	Scope     []string `json:"scope"`
	Module    string   `json:"module"` // to shorten import paths with

	reportJSON
	Relations relationsJSON `json:"relations"`
}

type relationsJSON struct {
	NewPackages  []string  `json:"newPackages"`
	GonePackages []string  `json:"gonePackages"`
	AddedDeps    []depJSON `json:"addedDeps"`
	RemovedDeps  []depJSON `json:"removedDeps"`
	AddedRefs    []refJSON `json:"addedRefs"`
	RemovedRefs  []refJSON `json:"removedRefs"`
}

// depJSON is one package importing another; crossing is "within",
// "outward" or "inward", as semdiff.Crossing names it.
type depJSON struct {
	From     string   `json:"from"`
	To       string   `json:"to"`
	Files    []string `json:"files"`
	Crossing string   `json:"crossing"`
}

type refJSON struct {
	From        string `json:"from"`
	To          string `json:"to"`
	FromPackage string `json:"fromPackage"`
	ToPackage   string `json:"toPackage"`
	Path        string `json:"path"`
	Line        int    `json:"line"`
	Uses        int    `json:"uses"`
	Approximate bool   `json:"approximate"`
	Crossing    string `json:"crossing"`
}

func newReviewJSON(rv *review.Review) *reviewJSON {
	rel := rv.Report.Relations
	return &reviewJSON{
		Head: rv.Head, HeadName: rv.HeadName(), HeadHash: rv.HeadHash, Target: rv.Target,
		MergeBase: rv.MergeBase, Commits: len(rv.Commits), Scope: orEmpty(rv.Scope), Module: rv.Module,
		reportJSON: newReportJSON(rv.Report, rv.Warnings),
		Relations: relationsJSON{
			NewPackages:  orEmpty(rel.NewPackages),
			GonePackages: orEmpty(rel.GonePackages),
			AddedDeps:    depsJSON(rel.AddedDeps),
			RemovedDeps:  depsJSON(rel.RemovedDeps),
			AddedRefs:    refsJSON(rel.AddedRefs),
			RemovedRefs:  refsJSON(rel.RemovedRefs),
		},
	}
}

// orEmpty returns ss, or an empty slice for nil, which encodes as [].
func orEmpty(ss []string) []string {
	if ss == nil {
		return []string{}
	}
	return ss
}

func depsJSON(ds []semdiff.Dependency) []depJSON {
	out := make([]depJSON, len(ds))
	for i, d := range ds {
		out[i] = depJSON{From: d.From, To: d.To, Files: d.Files, Crossing: d.Crossing.String()}
	}
	return out
}

func refsJSON(rs []semdiff.Reference) []refJSON {
	out := make([]refJSON, len(rs))
	for i, r := range rs {
		out[i] = refJSON{
			From: r.From, To: r.To, FromPackage: r.FromPackage, ToPackage: r.ToPackage,
			Path: r.Path, Line: r.Line, Uses: r.Uses, Approximate: r.Approximate, Crossing: r.Crossing.String(),
		}
	}
	return out
}

// reviewAPI serves the review of the merge request from ?head= (default
// HEAD) into ?base= (default the remote's default branch), limited to the
// repository paths in ?path=, repeated. It does not need the index.
func (s *Server) reviewAPI(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var scope []string
	for _, p := range q["path"] {
		if p = strings.TrimSpace(p); p != "" {
			scope = append(scope, p)
		}
	}
	rv, err := review.Build(r.Context(), s.repo, review.Options{
		Base:             strings.TrimSpace(q.Get("base")),
		Head:             strings.TrimSpace(q.Get("head")),
		Scope:            scope,
		IncludeGenerated: s.opts.IncludeGenerated,
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, newReviewJSON(rv))
}

// reviewOptionsJSON is what the review page offers to pick from.
type reviewOptionsJSON struct {
	Head     string   `json:"head"`   // the branch checked out, or "HEAD" when detached
	Target   string   `json:"target"` // what a review goes into unless told otherwise; empty if unknown
	Branches []string `json:"branches"`
	Dirs     []string `json:"dirs"` // holding Go files at ?head=, or at the head
}

func (s *Server) reviewOptions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	out := reviewOptionsJSON{Head: "HEAD", Branches: []string{}, Dirs: []string{}}
	if b, err := s.repo.CurrentBranch(ctx); err == nil && b != "" {
		out.Head = b
	}
	out.Target, _ = review.Target(ctx, s.repo, "", nil)
	if bs, err := s.repo.Branches(ctx); err == nil {
		out.Branches = bs
	}
	dirs, err := s.repo.GoDirs(ctx, cmp.Or(strings.TrimSpace(r.URL.Query().Get("head")), "HEAD"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	out.Dirs = orEmpty(dirs)
	writeJSON(w, out)
}
