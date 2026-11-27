package review

import (
	"context"
	"strings"
	"testing"

	"huginn/internal/git"
	"huginn/internal/gittest"
)

// shop makes a repository whose branch feature, checked out, makes billing
// use a new package payments.
func shop(t *testing.T) (*gittest.Repo, *git.Repo) {
	t.Helper()
	r := gittest.New(t)
	r.Commit("root", map[string]string{
		"go.mod":             "module example.com/shop\n",
		"billing/billing.go": "package billing\n\nfunc Charge(n int) int { return n }\n",
	})
	r.Git("checkout", "-q", "-b", "feature")
	r.Commit("pay", map[string]string{
		"payments/payments.go": "package payments\n\nfunc Authorize(n int) bool { return n > 0 }\n",
		"billing/billing.go":   "package billing\n\nimport \"example.com/shop/payments\"\n\nfunc Charge(n int) int {\n\tif !payments.Authorize(n) {\n\t\treturn 0\n\t}\n\treturn n\n}\n",
	})
	repo, err := git.Open(context.Background(), r.Dir)
	if err != nil {
		t.Fatal(err)
	}
	return r, repo
}

func TestBuild(t *testing.T) {
	r, repo := shop(t)
	rv, err := Build(context.Background(), repo, Options{Scope: []string{"billing"}})
	if err != nil {
		t.Fatal(err)
	}
	if rv.Head != "HEAD" || rv.HeadHash != r.Git("rev-parse", "feature") || rv.Target != "master" ||
		rv.MergeBase != r.Git("rev-parse", "master") || len(rv.Commits) != 1 {
		t.Errorf("review of %s (%s) into %s from %s, %d commits", rv.Head, rv.HeadHash, rv.Target, rv.MergeBase, len(rv.Commits))
	}
	if rv.Module != "example.com/shop" {
		t.Errorf("Module = %q", rv.Module)
	}
	if rv.Report.Symbols != 1 || len(rv.Report.Relations.AddedDeps) != 1 {
		t.Errorf("report: %d symbols, deps %+v", rv.Report.Symbols, rv.Report.Relations.AddedDeps)
	}
	if got := rv.HeadName(); got != "HEAD ("+rv.HeadHash[:7]+")" {
		t.Errorf("HeadName = %q", got)
	}
	rv.Head = rv.HeadHash[:9]
	if got := rv.HeadName(); got != rv.HeadHash[:7] {
		t.Errorf("HeadName of a hash = %q", got)
	}

	if _, err := Build(context.Background(), repo, Options{Head: "nope"}); err == nil {
		t.Error("no error for an unknown head")
	}
}

func TestTypes(t *testing.T) {
	r := gittest.New(t)
	const order = `package shop

type Order struct {
	items   []int
	coupons []int
	taxRate float64
	tmpl    *template.Template
	smtp    *smtp.Client
}

func (o *Order) Add(n int)          { o.items = append(o.items, n) }
func (o *Order) Coupon(c int)       { o.coupons = append(o.coupons, c) }
func (o *Order) Discount() int      { return len(o.coupons) }
func (o *Order) Total() float64     { return float64(len(o.items)) * o.taxRate }
func (o *Order) Render() string     { return o.tmpl.Name() }
func (o *Order) Send(to string)     { o.smtp.Send(to, o.Render()) }
`
	r.Commit("order", map[string]string{
		"go.mod":         "module example.com/shop\n",
		"shop/order.go":  order,
		"api/api.go":     "package api\n\nimport \"example.com/shop/shop\"\n\nfunc Mail(o *shop.Order) { o.Send(\"x\") }\n",
		"shop/helper.go": "package shop\n\nfunc helper() {}\n",
	})
	r.Git("checkout", "-q", "-b", "feature")
	r.Commit("mail coupons", map[string]string{
		"shop/order.go": order + "\nfunc (o *Order) MailCoupon(to string) {\n\to.coupons = nil\n\to.smtp.Send(to, \"coupon\")\n}\n",
	})
	repo, err := git.Open(context.Background(), r.Dir)
	if err != nil {
		t.Fatal(err)
	}
	rv, err := Build(context.Background(), repo, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rv.Types) != 1 {
		t.Fatalf("types = %+v, want shop.Order", rv.Types)
	}
	ty := rv.Types[0]
	if ty.Name != "shop.Order" || ty.Package != "example.com/shop/shop" || ty.Path != "shop/order.go" || ty.Before == nil || ty.After == nil {
		t.Errorf("type %s %s %s, before %v, after %v", ty.Name, ty.Package, ty.Path, ty.Before != nil, ty.After != nil)
	}
	if len(ty.Changed) != 1 || ty.Changed[0] != "MailCoupon" {
		t.Errorf("changed = %q", ty.Changed)
	}
	if len(ty.Notes) == 0 || !ty.Notes[0].Warn || !strings.HasPrefix(ty.Notes[0].Text, "MailCoupon now spans blocks") {
		t.Errorf("notes = %+v", ty.Notes)
	}
	// The mailer's callers are found in other packages.
	if c := ty.After.Clients; len(c) != 1 || c[0].Package != "example.com/shop/api" {
		t.Errorf("clients = %+v", c)
	}
}

func TestTarget(t *testing.T) {
	_, repo := shop(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name, base string
		env        map[string]string
		want       string
	}{
		{"given", "release", map[string]string{"GITHUB_BASE_REF": "main"}, "release"},
		{"GitLab merge request", "", map[string]string{"CI_MERGE_REQUEST_DIFF_BASE_SHA": "abc123", "CI_MERGE_REQUEST_TARGET_BRANCH_NAME": "main"}, "abc123"},
		{"GitLab without the base", "", map[string]string{"CI_MERGE_REQUEST_TARGET_BRANCH_NAME": "main"}, "origin/main"},
		{"GitHub pull request", "", map[string]string{"GITHUB_BASE_REF": "develop"}, "origin/develop"},
		{"default branch", "", nil, "master"},
	} {
		got, err := Target(ctx, repo, tc.base, func(k string) string { return tc.env[k] })
		if err != nil || got != tc.want {
			t.Errorf("%s: %q, %v; want %q", tc.name, got, err, tc.want)
		}
	}
	// Without getenv the environment is not read.
	t.Setenv("GITHUB_BASE_REF", "develop")
	if got, err := Target(ctx, repo, "", nil); err != nil || got != "master" {
		t.Errorf("without getenv: %q, %v; want master", got, err)
	}
}
