package web

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"strings"

	"huginn/internal/cohesion"
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
	Types     []typeJSON    `json:"types"`
	Focus     []focusJSON   `json:"focus"`
	Paths     []pathsJSON   `json:"paths"`

	// Indexed says the history was indexed, so the types' change coupling
	// was looked for.
	Indexed bool `json:"indexed"`

	// Traceable says the server can trace with capslock what the change
	// reaches outside the process, as it does when asked with ?caps=1;
	// Traced says this review was traced.
	Traceable bool `json:"traceable"`
	Traced    bool `json:"traced"`
}

// typeJSON is the seams of one struct type the merge request touches.
type typeJSON struct {
	Package string           `json:"package"`
	Name    string           `json:"name"`
	Path    string           `json:"path"`
	Line    int              `json:"line"`
	Changed []string         `json:"changed"`
	Before  *cohesion.Report `json:"before"`
	After   *cohesion.Report `json:"after"`
	Notes   []cohesion.Note  `json:"notes"`
}

// focusJSON is one thing to look at first, as review.Focus has it: kind is
// "external", "breaking", "behavior", "dependency" or "structure", and
// change indexes the changes, -1 for none. Exit is the call an "external"
// item's first capability leaves the code the change adds or edits by; the
// items right after it sharing that exit (review.Focus.SameExit) are
// listed with it.
type focusJSON struct {
	Kind         string           `json:"kind"`
	Name         string           `json:"name"`
	Path         string           `json:"path"`
	Line         int              `json:"line"`
	What         string           `json:"what"`
	Why          string           `json:"why,omitempty"`
	Change       int              `json:"change"`
	Reaches      []callerJSON     `json:"reaches"`
	Tests        int              `json:"tests"`
	Implementers bool             `json:"implementers,omitempty"`
	Capabilities []capabilityJSON `json:"capabilities,omitempty"`
	Exit         *stepJSON        `json:"exit,omitempty"`
}

// capabilityJSON is a way out of the process, as review.Capability has it:
// leaves indexes the step leaving the repository's code, and leavesChange
// the one leaving the code the change adds or edits, -1 for none.
type capabilityJSON struct {
	Name         string     `json:"name"`
	Label        string     `json:"label"`
	New          bool       `json:"new"`
	Path         []stepJSON `json:"path"`
	Elided       int        `json:"elided"`
	Leaves       int        `json:"leaves"`
	LeavesChange int        `json:"leavesChange"`
}

type stepJSON struct {
	Package string `json:"package"`
	Name    string `json:"name"`
	Path    string `json:"path,omitempty"`
	Line    int    `json:"line,omitempty"`
}

func focusesJSON(fs []review.Focus) []focusJSON {
	out := make([]focusJSON, len(fs))
	for i, f := range fs {
		out[i] = focusJSON{
			Kind: f.Kind.String(), Name: f.Name, Path: f.Path, Line: f.Line, What: f.What, Why: f.Why,
			Change: f.Change, Reaches: callersJSON(f.Reaches), Tests: f.Tests, Implementers: f.Implementers,
		}
		for _, c := range f.Capabilities {
			cj := capabilityJSON{Name: c.Name, Label: c.Label(), New: c.New, Elided: c.Elided, Leaves: c.Leaves(), LeavesChange: c.LeavesChange()}
			for _, s := range c.Path {
				cj.Path = append(cj.Path, stepJSON{Package: s.Package, Name: s.Name, Path: s.Path, Line: s.Line})
			}
			out[i].Capabilities = append(out[i].Capabilities, cj)
		}
		if s, ok := f.Exit(); ok {
			out[i].Exit = &stepJSON{Package: s.Package, Name: s.Name, Path: s.Path, Line: s.Line}
		}
	}
	return out
}

// pathsJSON is what a change does to its function's paths, as review.Paths
// has it, with the steps to show already cut into hunks. Kind is "new",
// "unchanged", "extracted", "reshaped" or "changed".
type pathsJSON struct {
	Change    int        `json:"change"`
	Kind      string     `json:"kind"`
	Verdict   string     `json:"verdict"`
	Decisions int        `json:"decisions"`
	WaysOut   int        `json:"waysOut"`
	Hunks     []hunkJSON `json:"hunks"`
	Exits     []exitJSON `json:"exits"`
}

type hunkJSON struct {
	Skipped    int            `json:"skipped"`
	Breadcrumb []string       `json:"breadcrumb"`
	Steps      []flowStepJSON `json:"steps"`
}

// flowStepJSON is a step of a compared flow: mark is "same", "renested",
// "edited", "added", "removed", "moved" or "movedEdited", and kind is
// "decision", "loop", "exit", "jump", "effect" or "closure".
type flowStepJSON struct {
	Mark  string `json:"mark"`
	Kind  string `json:"kind"`
	Depth int    `json:"depth"`
	Text  string `json:"text"` // with the way out a decision ends in
	Old   string `json:"old,omitempty"`
	To    string `json:"to,omitempty"`
}

// exitJSON is a way out: mark is "same", "new", "gone" or "changed".
type exitJSON struct {
	What string   `json:"what"`
	When []string `json:"when"`
	Line int      `json:"line"`
	Mark string   `json:"mark"`
}

func pathsListJSON(ps []review.Paths) []pathsJSON {
	out := make([]pathsJSON, len(ps))
	for i := range ps {
		p := &ps[i]
		out[i] = pathsJSON{
			Change: p.Change, Kind: p.Kind.String(), Verdict: p.Verdict, Decisions: p.Decisions, WaysOut: p.WaysOut,
			Hunks: []hunkJSON{}, Exits: []exitJSON{},
		}
		for _, h := range p.Hunks() {
			hj := hunkJSON{Skipped: h.Skipped, Breadcrumb: orEmpty(h.Breadcrumb), Steps: []flowStepJSON{}}
			for _, s := range h.Steps {
				hj.Steps = append(hj.Steps, flowStepJSON{
					Mark: s.Mark.String(), Kind: s.Kind.String(), Depth: s.Depth, Text: s.String(), Old: s.Old, To: s.To,
				})
			}
			out[i].Hunks = append(out[i].Hunks, hj)
		}
		for _, e := range p.Exits {
			out[i].Exits = append(out[i].Exits, exitJSON{What: e.What, When: orEmpty(e.When), Line: e.Line, Mark: e.Mark.String()})
		}
	}
	return out
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
		Types: typesJSON(rv.Types),
		Focus: focusesJSON(rv.Focus),
		Paths: pathsListJSON(rv.Paths),
	}
}

func typesJSON(ts []review.TypeReview) []typeJSON {
	out := make([]typeJSON, len(ts))
	for i, t := range ts {
		out[i] = typeJSON{
			Package: t.Package, Name: t.Name, Path: t.Path, Line: t.Line, Changed: orEmpty(t.Changed),
			Before: t.Before, After: t.After, Notes: t.Notes,
		}
		if out[i].Notes == nil {
			out[i].Notes = []cohesion.Note{}
		}
	}
	return out
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
// repository paths in ?path=, repeated. It does not need the index; once
// load returns one, the types it touches get their change coupling. With
// ?caps=1, and capslock to trace with, it also traces what the change
// reaches outside the process, which takes a while: the page asks for it
// once it shows the review.
func (a *repoAPI) reviewAPI(load func() *index) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var scope []string
		for _, p := range q["path"] {
			if p = strings.TrimSpace(p); p != "" {
				scope = append(scope, p)
			}
		}
		opts := review.Options{
			Base:             strings.TrimSpace(q.Get("base")),
			Head:             strings.TrimSpace(q.Get("head")),
			Scope:            scope,
			IncludeGenerated: a.generated,
		}
		ix := load()
		if ix != nil {
			opts.History = ix.h
		}
		if q.Get("caps") == "1" {
			opts.Tracer = a.tracer
		}
		rv, err := review.Build(r.Context(), a.repo, opts)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		out := newReviewJSON(rv)
		out.Indexed = ix != nil
		out.Traceable, out.Traced = a.tracer != nil, rv.Traced
		writeJSON(w, out)
	}
}

// reviewOptionsJSON is what the review page offers to pick from.
type reviewOptionsJSON struct {
	Head     string   `json:"head"`   // the branch checked out, or "HEAD" when detached
	Target   string   `json:"target"` // what a review goes into unless told otherwise; empty if unknown
	Branches []string `json:"branches"`
	Dirs     []string `json:"dirs"` // holding Go files at ?head=, or at the head
}

func (a *repoAPI) reviewOptions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	out := reviewOptionsJSON{Head: "HEAD", Branches: []string{}, Dirs: []string{}}
	if b, err := a.repo.CurrentBranch(ctx); err == nil && b != "" {
		out.Head = b
	}
	out.Target, _ = review.Target(ctx, a.repo, "", nil)
	if bs, err := a.repo.Branches(ctx); err == nil {
		out.Branches = bs
	}
	dirs, err := a.repo.GoDirs(ctx, cmp.Or(strings.TrimSpace(r.URL.Query().Get("head")), "HEAD"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	out.Dirs = orEmpty(dirs)
	writeJSON(w, out)
}
