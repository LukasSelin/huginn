package review

import (
	"context"
	"encoding/json"
	"slices"
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

// TestByExit keeps together the items reaching outside the process
// through the same call, each group where its first item is: Charge and
// Refund, ranked after Load, move up to Authorize, which pays by the same
// call, while Sync, which leaves nowhere in the repository, Load and
// Receipt, which writes a file on Authorize's line, keep their places.
func TestByExit(t *testing.T) {
	call := func(name, callee, path string, line int) Focus {
		c := Capability{Name: "NETWORK", Path: []Step{{Name: name}}}
		if path != "" {
			c.Path = append(c.Path, Step{Name: callee, Path: path, Line: line})
		}
		return Focus{Kind: FocusExternal, Name: name, Capabilities: []Capability{c}}
	}
	item := func(name, path string, line int) Focus { return call(name, "http.Get", path, line) }
	fs := []Focus{
		item("payments.Authorize", "payments/payments.go", 6),
		item("sync.Sync", "", 0),
		item("store.Load", "store/store.go", 5),
		call("payments.Receipt", "os.WriteFile", "payments/payments.go", 6),
		item("billing.Charge", "payments/payments.go", 6),
		item("store.Save", "store/store.go", 9),
		item("billing.Refund", "payments/payments.go", 6),
	}
	var got []string
	for _, f := range byExit(fs) {
		got = append(got, f.Name)
	}
	want := []string{"payments.Authorize", "billing.Charge", "billing.Refund", "sync.Sync", "store.Load", "payments.Receipt", "store.Save"}
	if !slices.Equal(got, want) {
		t.Errorf("byExit = %q, want %q", got, want)
	}
	if !fs[0].SameExit(fs[4]) || fs[0].SameExit(fs[3]) || fs[1].SameExit(fs[1]) {
		t.Errorf("Authorize shares its exit with Charge %v, with Receipt %v; Sync with itself %v",
			fs[0].SameExit(fs[4]), fs[0].SameExit(fs[3]), fs[1].SameExit(fs[1]))
	}

	if exit, ok := fs[4].Exit(); !ok || exit.Name != "http.Get" || exit.Path != "payments/payments.go" || exit.Line != 6 {
		t.Errorf("Charge's exit = %+v, %v", exit, ok)
	}
	if exit, ok := fs[1].Exit(); ok {
		t.Errorf("Sync's exit = %+v, want none", exit)
	}
	if _, ok := (Focus{Kind: FocusBehavior}).Exit(); ok {
		t.Error("an edit has an exit")
	}

	// The JSON the review is printed as carries each item's exit.
	var decoded []struct {
		Kind string
		Name string
		Exit *Step
	}
	b, err := json.Marshal([]Focus{fs[4], fs[1], {Kind: FocusBehavior, Name: "billing.Total"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 3 || decoded[0].Kind != "external" || decoded[0].Name != "billing.Charge" || decoded[0].Exit == nil ||
		*decoded[0].Exit != (Step{Name: "http.Get", Path: "payments/payments.go", Line: 6}) || decoded[1].Exit != nil || decoded[2].Exit != nil {
		t.Errorf("json:\n%s", b)
	}
}
