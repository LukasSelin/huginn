package review

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"huginn/internal/capslock"
	"huginn/internal/git"
	"huginn/internal/gittest"
)

// shopRepo makes a merge request in which billing.Charge starts paying
// through payments, which calls a payment provider over HTTP, billing.Refund
// is new and does too, store.Load keeps reading its file, and billing.Total
// only changes its arithmetic.
func shopRepo(t *testing.T) *git.Repo {
	r := gittest.New(t)
	r.Commit("root", map[string]string{
		"go.mod":               "module example.com/shop\n",
		"billing/billing.go":   "package billing\n\nfunc Charge(n int) int { return n }\n\nfunc Total(xs []int) int { return len(xs) }\n",
		"payments/payments.go": "package payments\n\nfunc Authorize(n int) bool { return n > 0 }\n",
		"store/store.go":       "package store\n\nimport \"os\"\n\nfunc Load() ([]byte, error) { return os.ReadFile(\"store.db\") }\n",
	})
	r.Git("checkout", "-q", "-b", "feature")
	r.Commit("pay", map[string]string{
		"billing/billing.go": "package billing\n\nimport \"example.com/shop/payments\"\n\n" +
			"func Charge(n int) int {\n\tif !payments.Authorize(n) {\n\t\treturn 0\n\t}\n\treturn n\n}\n\n" +
			"func Refund(n int) bool { return payments.Authorize(-n) }\n\n" +
			"func Total(xs []int) int {\n\tt := 0\n\tfor _, x := range xs {\n\t\tt += x\n\t}\n\treturn t\n}\n",
		"payments/payments.go": "package payments\n\nimport \"net/http\"\n\nfunc Authorize(n int) bool {\n\t_, err := http.Get(\"https://pay.example.com\")\n\treturn err == nil && n > 0\n}\n",
		"store/store.go":       "package store\n\nimport \"os\"\n\nfunc Load() ([]byte, error) { return os.ReadFile(\"shop.db\") }\n",
	})
	repo, err := git.Open(context.Background(), r.Dir)
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

// fakeCapslock stands in for capslock: at the head, which it tells by
// billing.Refund, Charge and Refund reach the network through payments and
// Load reads a file; at the merge base only Load does. Total reflects,
// which stays in the process.
type fakeCapslock struct {
	mu       sync.Mutex
	runs     []string // "head ./billing,./store" for each run
	failBase bool
}

func (f *fakeCapslock) run(_ context.Context, dir, root string, patterns []string) (capslock.Result, error) {
	src, err := os.ReadFile(filepath.Join(root, "billing", "billing.go"))
	if err != nil {
		return nil, err
	}
	head := strings.Contains(string(src), "Refund")
	f.mu.Lock()
	rev := map[bool]string{true: "head", false: "base"}[head]
	f.runs = append(f.runs, rev+" "+strings.Join(slices.Sorted(slices.Values(patterns)), ","))
	f.mu.Unlock()
	if dir != root {
		return nil, errors.New("not run in the module")
	}
	if !head && f.failBase {
		return nil, errors.New("the merge base does not build")
	}
	files := capslock.Capability{Name: "FILES", Path: []capslock.Call{
		{Package: "example.com/shop/store", Name: "Load"},
		{Package: "os", Name: "ReadFile", Path: "store/store.go", Line: 5},
	}}
	res := capslock.Result{
		{Package: "example.com/shop/store", Name: "Load"}:    {files},
		{Package: "example.com/shop/billing", Name: "Total"}: {{Name: "REFLECT", Path: []capslock.Call{{Package: "example.com/shop/billing", Name: "Total"}, {Package: "reflect", Name: "TypeOf"}}}},
	}
	if head {
		authorize := []capslock.Call{
			{Package: "example.com/shop/payments", Name: "Authorize"},
			{Package: "net/http", Name: "Get", Path: "payments/payments.go", Line: 6},
			{Package: "net/http", Name: "(*Client).Do"},
			{Package: "net", Name: "(*Dialer).DialContext"},
		}
		network := func(fn string, line int) []capslock.Capability {
			path := append([]capslock.Call{{Package: "example.com/shop/billing", Name: fn}}, authorize...)
			path[1].Path, path[1].Line = "billing/billing.go", line
			return []capslock.Capability{{Name: "NETWORK", Path: path}}
		}
		res[capslock.Func{Package: "example.com/shop/billing", Name: "Charge"}] = network("Charge", 6)
		res[capslock.Func{Package: "example.com/shop/billing", Name: "Refund"}] = network("Refund", 12)
		res[capslock.Func{Package: "example.com/shop/payments", Name: "Authorize"}] = []capslock.Capability{{Name: "NETWORK", Path: authorize}}
	}
	return res, nil
}

func TestTrace(t *testing.T) {
	repo := shopRepo(t)
	fake := &fakeCapslock{}
	tracer := &Tracer{run: fake.run}
	rv, err := Build(context.Background(), repo, Options{Base: "master", Tracer: tracer})
	if err != nil {
		t.Fatal(err)
	}
	if !rv.Traced || len(rv.Warnings) > 0 {
		t.Fatalf("traced %v, warnings %q", rv.Traced, rv.Warnings)
	}

	// Authorize and Charge now reach the network, Authorize, the larger
	// edit, called by both, first; Refund is new and does too; Load still
	// reads its file. Total only changes its arithmetic.
	type item struct {
		kind FocusKind
		name string
	}
	var got []item
	for _, f := range rv.Focus {
		got = append(got, item{f.Kind, f.Name})
	}
	want := []item{
		{FocusExternal, "payments.Authorize"},
		{FocusExternal, "billing.Charge"},
		{FocusExternal, "billing.Refund"},
		{FocusExternal, "store.Load"},
		{FocusBehavior, "billing.Total"},
		{FocusDependency, "example.com/shop/billing → example.com/shop/payments"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("focus =\n%v\nwant\n%v", got, want)
	}

	charge := rv.Focus[1]
	if len(charge.Capabilities) != 1 {
		t.Fatalf("Charge's capabilities = %+v", charge.Capabilities)
	}
	c := charge.Capabilities[0]
	var names []string
	for _, s := range c.Path {
		names = append(names, s.Name)
	}
	if c.Name != "NETWORK" || c.Label() != "network" || !c.New || c.Elided != 1 ||
		strings.Join(names, " → ") != "billing.Charge → payments.Authorize → http.Get → net.(*Dialer).DialContext" {
		t.Errorf("Charge reaches %s (new %v) by %s, %d elided", c.Name, c.New, strings.Join(names, " → "), c.Elided)
	}
	if i := c.Leaves(); i != 2 || c.Path[i].Path != "payments/payments.go" || c.Path[i].Line != 6 {
		t.Errorf("leaves at step %d of %+v, want http.Get at payments/payments.go:6", i, c.Path)
	}
	if refund := rv.Focus[2]; refund.What != "new func · 1 stmt · cyclomatic 1" || !refund.Capabilities[0].New {
		t.Errorf("Refund: what %q, capabilities %+v", refund.What, refund.Capabilities)
	}
	if load := rv.Focus[3].Capabilities; len(load) != 1 || load[0].New || load[0].Label() != "files" {
		t.Errorf("Load reaches %+v, want files, not new", load)
	}

	// capslock ran on the packages with the changed functions, once at
	// each revision; a second review of the same merge request reuses it.
	wantRuns := []string{"base ./billing,./payments,./store", "head ./billing,./payments,./store"}
	if runs := slices.Sorted(slices.Values(fake.runs)); !slices.Equal(runs, wantRuns) {
		t.Errorf("runs = %q, want %q", runs, wantRuns)
	}
	if _, err := Build(context.Background(), repo, Options{Base: "master", Tracer: tracer}); err != nil {
		t.Fatal(err)
	}
	if len(fake.runs) != 2 {
		t.Errorf("runs after a second review = %q, want the first two only", fake.runs)
	}
}

func TestTraceFailures(t *testing.T) {
	repo := shopRepo(t)

	// Without the merge base, nothing is new but what the change adds.
	rv, err := Build(context.Background(), repo, Options{Base: "master", Tracer: &Tracer{run: (&fakeCapslock{failBase: true}).run}})
	if err != nil {
		t.Fatal(err)
	}
	if !rv.Traced || len(rv.Warnings) != 1 || !strings.Contains(rv.Warnings[0], "merge base does not build") {
		t.Errorf("traced %v, warnings %q", rv.Traced, rv.Warnings)
	}
	for _, f := range rv.Focus {
		for _, c := range f.Capabilities {
			if c.New != (f.Name == "billing.Refund") {
				t.Errorf("%s reaches %s, new %v", f.Name, c.Name, c.New)
			}
		}
	}

	// Without the head, nothing is known.
	fail := func(context.Context, string, string, []string) (capslock.Result, error) {
		return nil, errors.New("capslock exploded")
	}
	rv, err = Build(context.Background(), repo, Options{Base: "master", Tracer: &Tracer{run: fail}})
	if err != nil {
		t.Fatal(err)
	}
	if rv.Traced || len(rv.Warnings) != 1 || !strings.Contains(rv.Warnings[0], "capslock exploded") {
		t.Errorf("traced %v, warnings %q", rv.Traced, rv.Warnings)
	}
	if rv.Focus[0].Kind != FocusBehavior {
		t.Errorf("focus starts with %s %s", rv.Focus[0].Kind, rv.Focus[0].Name)
	}
}

// TestTraceCapslock reviews a merge request with capslock itself: Load
// starts reading its file, Size keeps reading its file's size, and Exists
// is new and checks for it.
func TestTraceCapslock(t *testing.T) {
	if testing.Short() {
		t.Skip("capslock loads the standard library, twice")
	}
	tool, err := capslock.Find()
	if err != nil {
		t.Skip(err)
	}
	r := gittest.New(t)
	r.Commit("root", map[string]string{
		"go.mod": "module example.com/conf\n\ngo 1.22\n",
		"conf/conf.go": "package conf\n\nimport \"os\"\n\nfunc Load(name string) string { return name }\n\n" +
			"func Size(name string) int64 {\n\tfi, err := os.Stat(name)\n\tif err != nil {\n\t\treturn 0\n\t}\n\treturn fi.Size()\n}\n",
	})
	r.Git("checkout", "-q", "-b", "feature")
	r.Commit("read", map[string]string{
		"conf/conf.go": "package conf\n\nimport \"os\"\n\nfunc Load(name string) string {\n\tb, _ := os.ReadFile(name)\n\treturn string(b)\n}\n\n" +
			"func Size(name string) int64 {\n\tfi, err := os.Stat(name)\n\tif err != nil {\n\t\treturn -1\n\t}\n\treturn fi.Size()\n}\n\n" +
			"func Exists(name string) bool {\n\t_, err := os.Stat(name)\n\treturn err == nil\n}\n",
	})
	repo, err := git.Open(context.Background(), r.Dir)
	if err != nil {
		t.Fatal(err)
	}
	rv, err := Build(context.Background(), repo, Options{Base: "master", Tracer: NewTracer(tool)})
	if err != nil {
		t.Fatal(err)
	}
	if !rv.Traced || len(rv.Warnings) > 0 {
		t.Fatalf("traced %v, warnings %q", rv.Traced, rv.Warnings)
	}
	var got []string
	for _, f := range rv.Focus {
		if f.Kind != FocusExternal {
			continue
		}
		var caps []string
		for _, c := range f.Capabilities {
			caps = append(caps, c.Label()+map[bool]string{true: " (new)"}[c.New])
		}
		got = append(got, f.Name+": "+strings.Join(caps, ", "))
	}
	want := []string{"conf.Load: files (new)", "conf.Exists: files (new)", "conf.Size: files"}
	if !slices.Equal(got, want) {
		t.Errorf("reaching outside:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if c := rv.Focus[0].Capabilities[0]; c.Path[c.Leaves()].Path != "conf/conf.go" || c.Path[c.Leaves()].Line != 6 {
		t.Errorf("Load leaves at %+v, want conf/conf.go:6", c.Path)
	}
}
