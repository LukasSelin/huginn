package review

import (
	"context"
	"testing"

	"huginn/internal/git"
	"huginn/internal/gittest"
)

func TestFocus(t *testing.T) {
	r := gittest.New(t)
	r.Commit("root", map[string]string{
		"go.mod":         "module example.com/shop\n",
		"store/store.go": "package store\n\ntype Store interface {\n\tGet() int\n}\n",
		"mem/mem.go": "package mem\n\nimport \"example.com/shop/store\"\n\nvar _ store.Store = (*Mem)(nil)\n\n" +
			"type Mem struct{ n int }\n\nfunc (m *Mem) Get() int { return m.n }\n",
		"billing/billing.go":      "package billing\n\nfunc Charge(n int) int { return n }\n\nfunc Small() int { return 1 }\n\nfunc helper() int { return 42 }\n",
		"billing/billing_test.go": "package billing\n\nimport \"testing\"\n\nfunc TestCharge(t *testing.T) { Charge(1) }\n",
		"web/web.go":              "package web\n\nimport \"example.com/shop/billing\"\n\nfunc Serve() { billing.Charge(1); billing.Charge(2) }\n",
	})
	r.Git("checkout", "-q", "-b", "feature")
	r.Commit("pay", map[string]string{
		"store/store.go":       "package store\n\ntype Store interface {\n\tGet() int\n\tPut(int)\n}\n",
		"payments/payments.go": "package payments\n\nfunc Authorize(n int) bool { return n > 0 }\n",
		"billing/billing.go": "package billing\n\nimport \"example.com/shop/payments\"\n\n" +
			"func Charge(n int) int {\n\tif !payments.Authorize(n) {\n\t\treturn 0\n\t}\n\treturn n\n}\n\n" +
			"func Small() int { return 2 }\n\nfunc assist() int { return 42 }\n\nfunc Added() int { return 3 }\n",
		"billing/billing_test.go": "package billing\n\nimport \"testing\"\n\nfunc TestCharge(t *testing.T) { Charge(2) }\n",
	})
	repo, err := git.Open(context.Background(), r.Dir)
	if err != nil {
		t.Fatal(err)
	}
	rv, err := Build(context.Background(), repo, Options{})
	if err != nil {
		t.Fatal(err)
	}

	// The interface breaks the implementer that lacks the new method; then
	// come the edits, the one reaching a caller first; then the new import.
	// The rename, the added code and the test are left out.
	want := []struct {
		kind FocusKind
		name string
		what string
	}{
		{FocusBreaking, "store.Store", "+1 method: Put"},
		{FocusBehavior, "billing.Charge", "+2 stmts · cyclomatic 1 → 2"},
		{FocusBehavior, "billing.Small", "body edited, same size"},
		{FocusDependency, "example.com/shop/billing → example.com/shop/payments", "new import · 1 reference"},
	}
	if len(rv.Focus) != len(want) {
		t.Fatalf("focus = %+v, want %d items", rv.Focus, len(want))
	}
	for i, w := range want {
		f := rv.Focus[i]
		if f.Kind != w.kind || f.Name != w.name || f.What != w.what {
			t.Errorf("focus[%d] = %s %q %q, want %s %q %q", i, f.Kind, f.Name, f.What, w.kind, w.name, w.what)
		}
	}

	brk := rv.Focus[0]
	if brk.Why != "breaks implementers" || !brk.Implementers || len(brk.Reaches) != 1 || brk.Reaches[0].Name != "mem.Mem" {
		t.Errorf("breaking: why %q, implementers %v, reaches %+v", brk.Why, brk.Implementers, brk.Reaches)
	}
	if c := rv.Report.Changes[brk.Change]; c.Name != "store.Store" {
		t.Errorf("breaking points at change %s", c.Name)
	}

	charge := rv.Focus[1]
	if len(charge.Reaches) != 1 || charge.Reaches[0].Name != "web.Serve" || charge.Reaches[0].Calls != 2 || charge.Tests != 1 {
		t.Errorf("Charge reaches %+v and %d tests, want web.Serve ×2 and 1 test", charge.Reaches, charge.Tests)
	}

	dep := rv.Focus[3]
	if dep.Change != -1 || dep.Path != "billing/billing.go" || dep.Line == 0 {
		t.Errorf("dependency at change %d, %s:%d", dep.Change, dep.Path, dep.Line)
	}
}
