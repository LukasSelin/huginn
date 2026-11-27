package review

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"huginn/internal/semdiff"
)

// changeSet builds a report's changed functions and the calls between them.
type changeSet struct {
	r  semdiff.Report
	at map[string]int // "pkg.Name" → index into r.Changes
}

func newChangeSet() *changeSet { return &changeSet{at: map[string]int{}} }

// fn adds a changed function of package pkg.
func (s *changeSet) fn(pkg, name string) {
	s.at[pkg+"."+name] = len(s.r.Changes)
	s.r.Changes = append(s.r.Changes, semdiff.Change{
		Package: "example.com/app/" + pkg, Name: pkg + "." + name, Symbol: "func", Path: pkg + "/" + pkg + ".go",
	})
}

// calls records that from starts calling to, both "pkg.Name", as its callee.
func (s *changeSet) calls(from, to string) {
	pkg, _, _ := strings.Cut(to, ".")
	c := &s.r.Changes[s.at[from]]
	c.Callees = append(c.Callees, semdiff.Callee{Package: "example.com/app/" + pkg, Name: to, Started: true})
}

// calledBy records that caller, "pkg.Name", calls to, as to's caller.
func (s *changeSet) calledBy(to, caller string, test bool) {
	pkg, _, _ := strings.Cut(caller, ".")
	c := &s.r.Changes[s.at[to]]
	c.Callers = append(c.Callers, semdiff.Caller{Package: "example.com/app/" + pkg, Name: caller, Calls: 1, Test: test})
}

// describe writes parts as "hub: name·depth ...", loose ones marked.
func describe(r *semdiff.Report, ps []Part) []string {
	var out []string
	for _, p := range ps {
		var fs []string
		for _, f := range p.Functions {
			fs = append(fs, fmt.Sprintf("%s·%d", r.Changes[f.Change].Name, f.Depth))
		}
		s := r.Changes[p.Hub].Name + ": " + strings.Join(fs, " ")
		if p.Loose {
			s = "loose " + s
		}
		out = append(out, s)
	}
	return out
}

func TestParts(t *testing.T) {
	s := newChangeSet()
	// review: Build runs the tracer.
	s.fn("review", "Build")
	s.fn("review", "trace")
	s.fn("review", "revision")
	s.fn("review", "checkout")
	s.fn("review", "traceable")
	// cmd: the report shows the focus.
	s.fn("cmd", "focusA")
	s.fn("cmd", "focusB")
	s.fn("cmd", "focusC")
	// The wiring, one or two functions of a package each.
	s.fn("cmd", "serve")
	s.fn("cmd", "reviewMR")
	s.fn("capslock", "Find")
	s.fn("review", "NewTracer")
	s.fn("review", "Label")
	s.fn("web", "json")
	// Linked to one part, to two, and to none.
	s.fn("git", "Checkout")
	s.fn("util", "Fmt")
	s.fn("review", "contains")

	s.calls("review.Build", "review.trace")
	s.calls("review.trace", "review.revision")
	s.calls("review.trace", "review.traceable")
	s.calls("review.revision", "review.checkout")
	s.calls("review.checkout", "git.Checkout")
	s.calls("util.Fmt", "review.revision")
	s.calls("cmd.focusA", "cmd.focusB")
	s.calls("cmd.focusB", "cmd.focusC")
	s.calls("cmd.focusA", "util.Fmt")
	s.calls("cmd.focusC", "util.Fmt")
	s.calls("cmd.focusB", "review.Label")
	s.calls("cmd.serve", "capslock.Find")
	s.calls("cmd.serve", "review.NewTracer")
	s.calls("cmd.serve", "review.Label")
	s.calls("cmd.reviewMR", "review.NewTracer")
	s.calls("web.json", "review.Label")
	s.calledBy("review.Build", "cmd.reviewMR", false) // recorded on the callee's side only
	s.calledBy("cmd.focusA", "cmd.report", false)     // code the change leaves alone
	s.calledBy("cmd.serve", "cmd.main", false)
	s.calledBy("review.contains", "review.TestContains", true)

	// The focus is entered from unchanged code and calls the wiring, which
	// calls the tracer: they come in that order, the loose function last.
	// Checkout joins the tracer, its only link; Fmt, linked to both, joins
	// the focus, which calls it twice.
	got := describe(&s.r, parts(&s.r))
	want := []string{
		"cmd.focusA: cmd.focusA·0 cmd.focusB·1 cmd.focusC·2 util.Fmt·3",
		"cmd.serve: cmd.serve·0 capslock.Find·1 review.NewTracer·1 review.Label·1 cmd.reviewMR·0 web.json·0",
		"review.trace: review.Build·0 review.trace·1 review.revision·2 review.checkout·3 git.Checkout·4 review.traceable·2",
		"loose review.contains: review.contains·0",
	}
	if !slices.Equal(got, want) {
		t.Errorf("parts:\n got %q\nwant %q", got, want)
	}
}

// A loop with no way in is read from its first function.
func TestPartsLoop(t *testing.T) {
	s := newChangeSet()
	s.fn("a", "one")
	s.fn("a", "two")
	s.fn("a", "three")
	s.calls("a.one", "a.two")
	s.calls("a.two", "a.three")
	s.calls("a.three", "a.one")
	got := describe(&s.r, parts(&s.r))
	if want := []string{"a.one: a.one·0 a.two·1 a.three·2"}; !slices.Equal(got, want) {
		t.Errorf("parts = %q, want %q", got, want)
	}
}

// Types and test files make no part.
func TestPartsLeaveOut(t *testing.T) {
	s := newChangeSet()
	s.fn("a", "TestOne")
	s.r.Changes[0].Path = "a/a_test.go"
	s.r.Changes = append(s.r.Changes, semdiff.Change{Package: "example.com/app/a", Name: "a.T", Symbol: "type", Path: "a/a.go"})
	if ps := parts(&s.r); ps != nil {
		t.Errorf("parts = %q, want none", describe(&s.r, ps))
	}
}
