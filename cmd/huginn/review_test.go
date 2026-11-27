package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"huginn/internal/gittest"
	"huginn/internal/review"
	"huginn/internal/semdiff"
)

// shop makes a repository whose branch feature, checked out, makes billing
// use a new package payments and web call billing.
func shop(t *testing.T) *gittest.Repo {
	t.Helper()
	// Run as on a laptop, whatever CI runs the tests.
	for _, v := range []string{"CI_MERGE_REQUEST_DIFF_BASE_SHA", "CI_MERGE_REQUEST_TARGET_BRANCH_NAME", "GITHUB_BASE_REF"} {
		t.Setenv(v, "")
	}
	r := gittest.New(t)
	r.Commit("root", map[string]string{
		"go.mod":             "module example.com/shop\n",
		"billing/billing.go": "package billing\n\nfunc Charge(n int) int { return n }\n",
		"web/web.go":         "package web\n\nfunc Serve() {}\n",
	})
	r.Git("checkout", "-q", "-b", "feature")
	r.Commit("pay", map[string]string{
		"payments/payments.go": "package payments\n\nfunc Authorize(n int) bool { return n > 0 }\n",
		"billing/billing.go":   "package billing\n\nimport \"example.com/shop/payments\"\n\nfunc Charge(n int) int {\n\tif !payments.Authorize(n) {\n\t\treturn 0\n\t}\n\treturn n\n}\n",
		"web/web.go":           "package web\n\nimport \"example.com/shop/billing\"\n\nfunc Serve() { billing.Charge(1) }\n",
	})
	return r
}

// runReview reviews as huginn review does with args, without capslock: the
// shop's code reaches nothing outside the process, and tracing it takes
// seconds. TestReviewCapabilities prints what capslock finds.
func runReview(t *testing.T, args ...string) string {
	t.Helper()
	var out, errOut bytes.Buffer
	if err := reviewMergeRequest(context.Background(), &out, &errOut, append([]string{"-caps=false"}, args...)); err != nil {
		t.Fatalf("review %q: %v\n%s", args, err, errOut.String())
	}
	return out.String()
}

func TestReview(t *testing.T) {
	r := shop(t)
	out := runReview(t, "-C", r.Dir, "billing")
	for _, want := range []string{
		"review HEAD (",
		") into master\n",
		"· 1 commit\n",
		"scope: billing\n",
		"→ 1 semantic change across 1 symbol\n",
		"Look here first\n" +
			"  [edit] billing.Charge  billing/billing.go:5\n" +
			"      1 new path: if !payments.Authorize(n) → return 0 · 1 way out changed\n" +
			"      +2 stmts · cyclomatic 1 → 2 · called by web.Serve\n",
		"    +2 stmts · cyclomatic 1 → 2\n" +
			"    paths: 1 new path: if !payments.Authorize(n) → return 0 · 1 way out changed\n" +
			"      + if !payments.Authorize(n) → return 0\n" +
			"        return n\n" +
			"    ways out\n" +
			"      + return 0  ⇐ !payments.Authorize(n)\n" +
			"        return n\n",
		"  [import] example.com/shop/billing → example.com/shop/payments  billing/billing.go:6\n" +
			"      new import · 1 reference · out of the scope\n",
		"Impact\n  1 call site in 1 declaration, 1 package\n  outside the scope: 1 call site in 1 declaration, 1 package\n    example.com/shop/web\n",
		"  web.Serve  web/web.go:5  ×1 (outside)\n",
		"  + example.com/shop/billing → example.com/shop/payments  (out of the scope)\n" +
			"      billing.Charge → payments.Authorize  billing/billing.go:6  ×1\n",
		"  + example.com/shop/web → example.com/shop/billing  (into the scope)\n" +
			"      web.Serve → billing.Charge  web/web.go:5  ×1\n",
		"[body edit] func billing.Charge",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	// payments is outside the scope; the reader is told only how billing
	// reaches it.
	if strings.Contains(out, "new package") || strings.Contains(out, "payments.go") {
		t.Errorf("output reports outside the scope:\n%s", out)
	}
}

func TestReviewFormats(t *testing.T) {
	r := shop(t)
	md := runReview(t, "-C", r.Dir, "-format", "markdown", "billing")
	for _, want := range []string{
		"### huginn review of `billing`\n",
		"#### Look here first\n\n- **edit** `billing.Charge` at `billing/billing.go:5`: 1 new path: if !payments.Authorize(n) → return 0 · 1 way out changed — +2 stmts · cyclomatic 1 → 2 · called by `web.Serve`\n",
		"  \n  Paths: 1 new path: if !payments.Authorize(n) → return 0 · 1 way out changed\n" +
			"  ```diff\n  + if !payments.Authorize(n) → return 0\n    return n\n  ```\n",
		"**Outside the scope: 1 call site in 1 declaration, 1 package** (`example.com/shop/web`).",
		"| `web.Serve` | `web/web.go:5` | ×1 **outside** |",
		"- **+** `example.com/shop/billing` → `example.com/shop/payments` (new import, **out of the scope**)\n",
		"<details><summary>Changes (1 symbol)</summary>",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}

	var rv struct {
		Target  string
		Commits []string
		Scope   []string
		Paths   []struct {
			Change int
			Kind   string
			Steps  []struct{ Mark, Kind, Text, Then string }
		}
		Report struct {
			Counts    map[string]int
			Relations struct {
				AddedDeps []struct{ From, To, Crossing string }
			}
		}
	}
	if err := json.Unmarshal([]byte(runReview(t, "-C", r.Dir, "-format", "json", "billing")), &rv); err != nil {
		t.Fatal(err)
	}
	if rv.Target != "master" || len(rv.Commits) != 1 || !slices.Equal(rv.Scope, []string{"billing"}) {
		t.Errorf("json: target %q, %d commits, scope %q", rv.Target, len(rv.Commits), rv.Scope)
	}
	if p := rv.Paths; len(p) != 1 || p[0].Kind != "changed" || len(p[0].Steps) != 2 ||
		p[0].Steps[0].Mark != "added" || p[0].Steps[0].Kind != "decision" || p[0].Steps[0].Then != "return 0" {
		t.Errorf("json paths = %+v", p)
	}
	if rv.Report.Counts["body edit"] != 1 {
		t.Errorf("json counts = %v", rv.Report.Counts)
	}
	if d := rv.Report.Relations.AddedDeps; len(d) != 2 || d[0].Crossing != "outward" || d[1].Crossing != "inward" {
		t.Errorf("json deps = %+v", d)
	}
}

// TestReviewCapabilities prints a change reaching the network, as capslock
// traced it, and a traced change reaching nothing outside the process.
func TestReviewCapabilities(t *testing.T) {
	rv := &review.Review{
		Report: &semdiff.Report{Changes: []semdiff.Change{{Name: "payments.Authorize", Symbol: "func"}}},
		Traced: true,
		Focus: []review.Focus{{
			Kind: review.FocusExternal, Name: "payments.Authorize", Path: "payments/payments.go", Line: 5, What: "+1 stmt", Change: 0,
			Reaches: []semdiff.Caller{{Name: "billing.Charge", Calls: 1}},
			Capabilities: []review.Capability{
				{Name: "NETWORK", New: true, Elided: 1, Path: []review.Step{
					{Name: "payments.Authorize"},
					{Name: "http.Get", Path: "payments/payments.go", Line: 6},
					{Name: "net.(*Dialer).DialContext"},
				}},
				{Name: "MODIFY_SYSTEM_STATE/ENV", Path: []review.Step{
					{Name: "payments.Authorize.func1"},
					{Name: "payments.setProxy", Path: "payments/payments.go", Line: 9},
					{Name: "os.Setenv", Path: "payments/proxy.go", Line: 4},
				}},
			},
		}},
	}
	var text, md bytes.Buffer
	(&printer{w: &text}).focus(rv)
	want := "  [external] payments.Authorize  payments/payments.go:5\n" +
		"      +1 stmt · called by billing.Charge\n" +
		"      network (new): http.Get → … → net.(*Dialer).DialContext  payments/payments.go:6\n" +
		"      env: payments.Authorize.func1 → payments.setProxy → os.Setenv  payments/proxy.go:4\n"
	if !strings.Contains(text.String(), want) || strings.Contains(text.String(), "traced with capslock") {
		t.Errorf("text:\n%s\nwant\n%s", text.String(), want)
	}
	(&markdown{w: &md}).focus(rv)
	wantMD := "- **external** `payments.Authorize` at `payments/payments.go:5`: +1 stmt · called by `billing.Charge`\n" +
		"  - network (new): `http.Get` → … → `net.(*Dialer).DialContext` at `payments/payments.go:6`\n"
	if !strings.Contains(md.String(), wantMD) {
		t.Errorf("markdown:\n%s\nwant\n%s", md.String(), wantMD)
	}

	rv.Focus = nil
	text.Reset()
	(&printer{w: &text}).focus(rv)
	if !strings.Contains(text.String(), "  "+noneOutside+"\n") {
		t.Errorf("text without anything outside:\n%s", text.String())
	}
}

func TestReviewWholeRepository(t *testing.T) {
	r := shop(t)
	out := runReview(t, "-C", r.Dir)
	for _, want := range []string{"new package example.com/shop/payments\n", "→ 3 semantic changes across 3 symbols\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "scope") {
		t.Errorf("unscoped review mentions a scope:\n%s", out)
	}
}

// The impact counts production code first and the tests apart, and lists
// the production callers before the tests, though the test calls more.
func TestReviewImpactCountsTestsApart(t *testing.T) {
	r := shop(t)
	r.Commit("test", map[string]string{
		"billing/billing_test.go": "package billing\n\nimport \"testing\"\n\nfunc TestCharge(t *testing.T) { Charge(1); Charge(2); Charge(3) }\n",
	})
	out := runReview(t, "-C", r.Dir, "billing")
	for _, want := range []string{
		"Impact\n  1 call site in 1 declaration, 1 package · 3 more in tests\n",
		"  web.Serve  web/web.go:5  ×1 (outside)\n  billing.TestCharge  billing/billing_test.go:5  ×3 (test)\n",
		"      +2 stmts · cyclomatic 1 → 2 · called by web.Serve · 1 in tests\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	md := runReview(t, "-C", r.Dir, "-format", "markdown", "billing")
	if want := "1 call site in 1 declaration, 1 package. 3 more in tests."; !strings.Contains(md, want) {
		t.Errorf("markdown lacks %q:\n%s", want, md)
	}
}

func TestOpenScoped(t *testing.T) {
	r := shop(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name, dir string
		folders   []string
		want      []string
	}{
		{"relative to -C", r.Dir, []string{"billing", "web/web.go"}, []string{"billing", "web/web.go"}},
		{"a folder alone finds its repository", "", []string{filepath.Join(r.Dir, "billing")}, []string{"billing"}},
		{"a folder the merge request removes", "", []string{filepath.Join(r.Dir, "gone", "deep")}, []string{"gone/deep"}},
		{"the root is everything", r.Dir, []string{"billing", "."}, nil},
		{"no folders", r.Dir, nil, nil},
	} {
		_, scope, err := openScoped(ctx, tc.dir, tc.folders)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if !slices.Equal(scope, tc.want) {
			t.Errorf("%s: scope %q, want %q", tc.name, scope, tc.want)
		}
	}
	if _, _, err := openScoped(ctx, r.Dir, []string{".."}); err == nil || !strings.Contains(err.Error(), "not in the repository") {
		t.Errorf("folder above the repository: err = %v", err)
	}
}
