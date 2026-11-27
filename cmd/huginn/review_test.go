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

func runReview(t *testing.T, args ...string) string {
	t.Helper()
	var out, errOut bytes.Buffer
	if err := reviewMergeRequest(context.Background(), &out, &errOut, args); err != nil {
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
		Report  struct {
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
	if rv.Report.Counts["body edit"] != 1 {
		t.Errorf("json counts = %v", rv.Report.Counts)
	}
	if d := rv.Report.Relations.AddedDeps; len(d) != 2 || d[0].Crossing != "outward" || d[1].Crossing != "inward" {
		t.Errorf("json deps = %+v", d)
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
