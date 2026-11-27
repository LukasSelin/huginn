package semdiff

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"huginn/internal/ast"
)

// mergeRequest builds the input of a merge request that, in example.com/app:
//
//   - renames store.Open to store.Connect, updating its callers;
//   - deletes package legacy, which billing.Charge used;
//   - adds package payments, which billing.Charge now uses;
//   - makes web.Serve call billing.Charge;
//   - adds a test in billing that uses api, which is no relation.
func mergeRequest(t *testing.T) Input {
	t.Helper()
	const openBody = `{
	if path == "" {
		return nil, errors.New("no path")
	}
	db := &DB{path: path}
	db.retries = 3
	return db, nil
}
`
	oldStore := parse(t, "store/store.go", `package store

import "errors"

type DB struct{ path string; retries int }

func Open(path string) (*DB, error) `+openBody)
	newStore := parse(t, "store/store.go", `package store

import "errors"

type DB struct{ path string; retries int }

func Connect(path string) (*DB, error) `+openBody)

	oldLegacy := parse(t, "legacy/legacy.go", "package legacy\n\nfunc Bill() {}\n")
	payments := parse(t, "payments/payments.go", `package payments

type Gateway struct{}

func New() *Gateway { return &Gateway{} }

func (g *Gateway) Authorize() error { return g.check() }

func (g *Gateway) check() error { return nil }
`)
	oldBilling := parse(t, "billing/billing.go", `package billing

import (
	"example.com/app/legacy"
	"example.com/app/store"
)

func Charge() {
	legacy.Bill()
	store.Open("x")
}
`)
	newBilling := parse(t, "billing/billing.go", `package billing

import (
	"example.com/app/payments"
	"example.com/app/store"
)

func Charge() {
	g := payments.New()
	g.Authorize()
	g.check()
	store.Connect("x")
}
`)
	billingTest := parse(t, "billing/billing_test.go", `package billing

import (
	"testing"

	"example.com/app/api"
)

func TestCharge(t *testing.T) { api.Load(); Charge() }
`)
	oldAPI := parse(t, "api/api.go", "package api\n\nimport \"example.com/app/store\"\n\nfunc Load() { store.Open(\"a\") }\n")
	newAPI := parse(t, "api/api.go", "package api\n\nimport \"example.com/app/store\"\n\nfunc Load() { store.Connect(\"a\") }\n")
	oldWeb := parse(t, "web/web.go", "package web\n\nfunc Serve() {}\n")
	newWeb := parse(t, "web/web.go", "package web\n\nimport \"example.com/app/billing\"\n\nfunc Serve() { billing.Charge() }\n")

	return Input{
		Files: []FileDiff{
			{Old: oldAPI, New: newAPI, Added: 1, Removed: 1},
			{Old: oldBilling, New: newBilling, Added: 4, Removed: 3},
			{New: billingTest, Added: 9},
			{Old: oldLegacy, Removed: 3},
			{New: payments, Added: 9},
			{Old: oldStore, New: newStore, Added: 1, Removed: 1},
			{Old: oldWeb, New: newWeb, Added: 3, Removed: 1},
		},
		Tree:  []*ast.File{newAPI, newBilling, billingTest, payments, newStore, newWeb},
		GoMod: goMod,
	}
}

func deps(ds []Dependency) []string {
	out := []string{}
	for _, d := range ds {
		s := fmt.Sprintf("%s -> %s %v", strings.TrimPrefix(d.From, "example.com/app/"), strings.TrimPrefix(d.To, "example.com/app/"), d.Files)
		if d.Crossing != Within {
			s += " " + d.Crossing.String()
		}
		out = append(out, s)
	}
	return out
}

func refs(rs []Reference) []string {
	out := []string{}
	for _, r := range rs {
		s := fmt.Sprintf("%s -> %s %s:%d ×%d", r.From, r.To, r.Path, r.Line, r.Uses)
		if r.Approximate {
			s += " approx"
		}
		if r.Crossing != Within {
			s += " " + r.Crossing.String()
		}
		out = append(out, s)
	}
	return out
}

func check(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s:\n got %q\nwant %q", what, got, want)
	}
}

func TestRelations(t *testing.T) {
	rel := Build(mergeRequest(t)).Relations

	check(t, "new packages", rel.NewPackages, []string{"example.com/app/payments"})
	check(t, "gone packages", rel.GonePackages, []string{"example.com/app/legacy"})
	// The test file's import of api is no dependency: nothing imports tests.
	check(t, "added deps", deps(rel.AddedDeps), []string{
		"billing -> payments [billing/billing.go]",
		"web -> billing [web/web.go]",
	})
	check(t, "removed deps", deps(rel.RemovedDeps), []string{"billing -> legacy [billing/billing.go]"})

	// g.Authorize() is matched by name with the methods of the packages
	// billing imports; g.check() is not, since only payments can call it.
	// Callers that follow store.Open's rename to Connect start and stop
	// nothing.
	check(t, "added refs", refs(rel.AddedRefs), []string{
		"billing.Charge -> payments.New billing/billing.go:9 ×1",
		"billing.Charge -> payments.(*Gateway).Authorize billing/billing.go:10 ×1 approx",
		"web.Serve -> billing.Charge web/web.go:5 ×1",
	})
	check(t, "removed refs", refs(rel.RemovedRefs), []string{"billing.Charge -> legacy.Bill billing/billing.go:9 ×1"})
}

func TestScope(t *testing.T) {
	in := mergeRequest(t)
	in.Scope = []string{"./billing/"}
	r := Build(in)

	var names []string
	for _, c := range r.Changes {
		names = append(names, c.Name)
	}
	check(t, "changes", names, []string{"billing.Charge", "billing.TestCharge"})
	if r.Added != 13 || r.Removed != 3 || len(r.Files) != 2 {
		t.Errorf("lines +%d −%d in %d files, want +13 −3 in 2", r.Added, r.Removed, len(r.Files))
	}

	// web.Serve calls billing.Charge from outside the scope, the new test
	// from inside.
	charge := change(t, r, "billing.Charge")
	if got := callers(charge.Callers); got != "[example.com/app/billing billing.TestCharge 1 example.com/app/web web.Serve 1]" {
		t.Errorf("Charge callers = %s", got)
	}
	var outside []string
	for _, c := range r.Blast.Callers {
		if c.Outside {
			outside = append(outside, c.Name)
		}
	}
	check(t, "callers outside the scope", outside, []string{"web.Serve"})

	rel := r.Relations
	if len(rel.NewPackages)+len(rel.GonePackages) > 0 {
		t.Errorf("packages outside the scope reported: new %v, gone %v", rel.NewPackages, rel.GonePackages)
	}
	check(t, "added deps", deps(rel.AddedDeps), []string{
		"billing -> payments [billing/billing.go] outward",
		"web -> billing [web/web.go] inward",
	})
	check(t, "removed deps", deps(rel.RemovedDeps), []string{"billing -> legacy [billing/billing.go] outward"})
	check(t, "added refs", refs(rel.AddedRefs), []string{
		"billing.Charge -> payments.New billing/billing.go:9 ×1 outward",
		"billing.Charge -> payments.(*Gateway).Authorize billing/billing.go:10 ×1 approx outward",
		"web.Serve -> billing.Charge web/web.go:5 ×1 inward",
	})
}

func TestScopeOfFile(t *testing.T) {
	in := mergeRequest(t)
	in.Scope = []string{"web/web.go"}
	r := Build(in)
	if len(r.Changes) != 1 || r.Changes[0].Name != "web.Serve" {
		t.Errorf("changes = %+v, want web.Serve only", r.Changes)
	}
	// The file is in the scope, its package's directory is not: the import
	// starts in the scope, and leads out of it.
	check(t, "added deps", deps(r.Relations.AddedDeps), []string{"web -> billing [web/web.go] outward"})
}

func TestRootScopeIsEverything(t *testing.T) {
	for _, scope := range [][]string{nil, {"."}, {"billing", "/"}} {
		in := mergeRequest(t)
		in.Scope = scope
		r := Build(in)
		if r.Symbols != 10 {
			t.Errorf("scope %q: %d symbols, want 10", scope, r.Symbols)
		}
		for _, c := range r.Blast.Callers {
			if c.Outside {
				t.Errorf("scope %q: caller %s outside", scope, c.Name)
			}
		}
	}
}
