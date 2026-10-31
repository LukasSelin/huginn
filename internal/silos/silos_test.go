package silos

import (
	"context"
	"math"
	"slices"
	"strings"
	"testing"

	"huginn/internal/git"
	"huginn/internal/gittest"
	"huginn/internal/lineage"
)

// commitAs commits files as author who.
func commitAs(r *gittest.Repo, who, msg string, files map[string]string) string {
	r.T.Helper()
	r.Git("config", "user.name", who)
	return r.Commit(msg, files)
}

// build indexes r, tests included, and finds its silos.
func build(t *testing.T, r *gittest.Repo) *Report {
	t.Helper()
	repo, err := git.Open(r.Dir)
	if err != nil {
		t.Fatal(err)
	}
	h, err := lineage.Build(context.Background(), repo, lineage.BuildOptions{Tests: true})
	if err != nil {
		t.Fatal(err)
	}
	return Build(h, Options{})
}

func near(a, b float64) bool { return math.Abs(a-b) < 0.02 }

func find[T any](xs []T, unit func(T) Unit, name string) (T, bool) {
	for _, x := range xs {
		if unit(x).Name == name {
			return x, true
		}
	}
	var zero T
	return zero, false
}

func riskUnit(r Risk) Unit { return r.Unit }
func lossUnit(l Loss) Unit { return l.Unit }

// storeRepo is a small module of three packages:
//
//	Ada:  store.Get (called from api and store), store.Put, util.Join
//	Bob:  api.Handle, api.List, a rewrite of most of store.Put, util.Split
//	Cara: store.Cached, util.Trim
//
// and a test calling Get that must not count.
func storeRepo(t *testing.T) *gittest.Repo {
	r := gittest.New(t)
	commitAs(r, "Ada", "store", map[string]string{
		"go.mod": "module example.com/app\n",
		"store/store.go": `package store

func Get(k string) string {
	if k == "" {
		return ""
	}
	return k
}

func Put(k string) {
	a := k
	b := a
	_ = b
}
`,
		"util/join.go": "package util\n\nfunc Join(a, b string) string {\n\tx := a\n\ty := b\n\tx += y\n\ty += x\n\treturn x + y\n}\n",
	})
	commitAs(r, "Bob", "api", map[string]string{
		"api/api.go": `package api

import "example.com/app/store"

func Handle(k string) string {
	store.Put(k)
	return store.Get(k) + store.Get(k)
}

func List() string { return store.Get("x") }
`,
		"util/split.go": "package util\n\nfunc Split(a string) string {\n\tx := a\n\ty := x\n\treturn y\n}\n",
	})
	commitAs(r, "Bob", "rewrite Put", map[string]string{
		"store/store.go": `package store

func Get(k string) string {
	if k == "" {
		return ""
	}
	return k
}

func Put(k string) {
	a := k
	b := a
	c := b
	d := c
	e := d
	_ = e
}
`,
	})
	commitAs(r, "Cara", "cache", map[string]string{
		"store/cache.go":      "package store\n\nfunc Cached(k string) string { return Get(k) }\n",
		"store/store_test.go": "package store\n\nimport \"testing\"\n\nfunc TestGet(t *testing.T) { Get(\"a\"); Get(\"b\") }\n",
		"util/trim.go":        "package util\n\nfunc Trim(a string) string {\n\tx := a\n\ty := x\n\treturn y\n}\n",
	})
	return r
}

func TestRiskRanking(t *testing.T) {
	rep := build(t, storeRepo(t))
	if !slices.Equal(rep.Authors, []string{"Ada", "Bob", "Cara"}) || len(rep.Departed) != 0 {
		t.Fatalf("authors %v departed %v", rep.Authors, rep.Departed)
	}
	// Bob's 12.5 against Ada's 12 and Cara's 6.
	if k := rep.Knowers; len(k) != 3 || k[0].Name != "Bob" || k[1].Name != "Ada" || !near(k[0].Share+k[1].Share+k[2].Share, 1) {
		t.Errorf("knowers %+v", k)
	}
	for _, r := range append(slices.Clone(rep.Symbols), rep.Packages...) {
		if strings.HasSuffix(r.Path, "_test.go") {
			t.Errorf("test code ranked: %s", r.Path)
		}
	}

	get, ok := find(rep.Symbols, riskUnit, "Get")
	if !ok {
		t.Fatalf("Get is no silo: %+v", rep.Symbols)
	}
	if rep.Symbols[0].Name != "Get" {
		t.Errorf("top silo %s, want Get", rep.Symbols[0].Name)
	}
	// Handle twice, List and Cached; the test is left out.
	d := get.Dependence
	if d.Callers != 3 || d.CallSites != 4 || d.Packages != 1 || d.Approximate != 0 || d.FanIn != 2*crossWeight+1 {
		t.Errorf("Get depends: %+v", d)
	}
	if get.Holder != "Ada" || get.Held != 1 || get.BusFactor != 1 || get.Concentration() != 1 {
		t.Errorf("Get spread: %+v", get.Spread)
	}
	want := []string{"Ada holds 100%", "bus factor 1", "4 call sites from 3 callers in 1 other package", "cyclomatic 2", "1 edit"}
	if !slices.Equal(get.Reasons, want) {
		t.Errorf("Get reasons:\n got %q\nwant %q", get.Reasons, want)
	}

	// Bob rewrote half of Put: 0.5 × 7 against Ada's 4 × 0.5.
	put, ok := find(rep.Symbols, riskUnit, "Put")
	if !ok || put.Holder != "Bob" || !near(put.Held, 3.5/5.5) || put.Edits != 2 {
		t.Errorf("Put: %+v", put)
	}
	if put.Score >= get.Score {
		t.Errorf("Put scores %v, Get %v: Get is known by one and called more", put.Score, get.Score)
	}

	// util is shared three ways, so no one holds half of it.
	if _, ok := find(rep.Packages, riskUnit, "util"); ok {
		t.Errorf("util ranked as a silo")
	}
	store, ok := find(rep.Packages, riskUnit, "store")
	if !ok || rep.Packages[0].Name != "store" {
		t.Fatalf("packages %+v, want store first", rep.Packages)
	}
	// From api only: Handle (Put, Get twice) and List.
	if d := store.Dependence; d.Callers != 2 || d.CallSites != 4 || d.Packages != 1 || d.Complexity != 4 {
		t.Errorf("store depends: %+v", d)
	}
	if store.Holder != "Ada" || store.BusFactor != 1 {
		t.Errorf("store spread: %+v", store.Spread)
	}
}

func TestLeave(t *testing.T) {
	rep := build(t, storeRepo(t))
	d := rep.Leave("ada", "nobody", "Ada")
	if !slices.Equal(d.Leaving, []string{"Ada"}) || !slices.Equal(d.Unknown, []string{"nobody"}) {
		t.Fatalf("leaving %v unknown %v", d.Leaving, d.Unknown)
	}
	// Get and Join are Ada's alone; Put stays Bob's.
	if d.OrphanedSymbols != 2 || d.SingleSymbols != 0 {
		t.Errorf("orphaned %d single %d: %+v", d.OrphanedSymbols, d.SingleSymbols, d.Symbols)
	}
	if d.Symbols[0].Name != "Get" || !d.Symbols[0].Orphaned || d.Symbols[0].Lost != 1 {
		t.Errorf("first to hand over: %+v", d.Symbols[0])
	}
	if want := []string{"Ada holds 100%", "nobody left knows it"}; !slices.Equal(d.Symbols[0].Reasons[:2], want) {
		t.Errorf("Get reasons %q", d.Symbols[0].Reasons)
	}
	if _, ok := find(d.Symbols, lossUnit, "Put"); ok {
		t.Errorf("Put listed, but Bob holds most of it either way")
	}
	// store went from Ada to Bob: it rests on one person still, but
	// another one.
	store, ok := find(d.Packages, lossUnit, "store")
	if !ok || store.Orphaned || store.After.Holder != "Bob" || store.Before.Holder != "Ada" {
		t.Errorf("store: %+v", store)
	}
	if d.Share <= 0.3 || d.Share >= 0.6 {
		t.Errorf("Ada holds %v of everything", d.Share)
	}

	// util is about 43/28/29: without Bob, Ada holds most of what is left.
	d = rep.Leave("Bob")
	util, ok := find(d.Packages, lossUnit, "util")
	if !ok || util.Orphaned || util.Before.BusFactor != 2 || util.After.BusFactor != 1 || util.After.Holder != "Ada" {
		t.Fatalf("util: %+v", util)
	}
	if !slices.Contains(util.Reasons, "bus factor 2 → 1: hand over to Ada (43%)") {
		t.Errorf("util reasons %q", util.Reasons)
	}
	if d.SinglePackages < 1 {
		t.Errorf("single packages %d", d.SinglePackages)
	}
}

func TestDepartedAuthors(t *testing.T) {
	r := gittest.New(t)
	commitAs(r, "Ada", "add", map[string]string{
		"a/a.go": "package a\n\nfunc A() int {\n\treturn 1\n}\n",
	})
	r.Days += 200
	commitAs(r, "Bob", "add", map[string]string{
		"b/b.go": "package b\n\nfunc B() int {\n\treturn 2\n}\n",
	})
	rep := build(t, r)
	if !slices.Equal(rep.Departed, []string{"Ada"}) {
		t.Fatalf("departed %v", rep.Departed)
	}
	a, ok := find(rep.Symbols, riskUnit, "A")
	if !ok || a.Holder != "" || a.Known != 0 || a.BusFactor != 0 || a.Concentration() != 1 {
		t.Fatalf("A: %+v", a)
	}
	if a.Reasons[0] != "nobody still around knows it" {
		t.Errorf("A reasons %q", a.Reasons)
	}
	// Ada is gone already: her leaving loses nothing more.
	if d := rep.Leave("Ada"); len(d.Symbols) != 0 || d.Share != 0 {
		t.Errorf("leaving again: %+v", d)
	}
}

// big is a function body of n statements, the first of which calls call.
func big(name, call string, n int) string {
	var b strings.Builder
	b.WriteString("func " + name + "() {\n")
	if call != "" {
		b.WriteString("\t" + call + "\n\t" + call + "\n")
	}
	for i := range n {
		b.WriteString("\t_ = " + strings.Repeat("1+", i) + "1\n")
	}
	b.WriteString("}\n")
	return b.String()
}

func TestCoupling(t *testing.T) {
	r := gittest.New(t)
	commitAs(r, "Ada", "a", map[string]string{
		"go.mod": "module example.com/app\n",
		"a/a.go": "package a\n\n" + big("A", "", 10),
	})
	commitAs(r, "Bob", "b", map[string]string{
		"b/b.go": "package b\n\nimport \"example.com/app/a\"\n\n" + big("B", "a.A()", 10),
	})
	commitAs(r, "Ada", "c", map[string]string{
		"c/c.go": "package c\n\nimport \"example.com/app/a\"\n\n" + big("C", "a.A()", 2),
	})
	// Cara changes A and B together twice, a statement each.
	for _, n := range []int{11, 12} {
		commitAs(r, "Cara", "both", map[string]string{
			"a/a.go": "package a\n\n" + big("A", "", n),
			"b/b.go": "package b\n\nimport \"example.com/app/a\"\n\n" + big("B", "a.A()", n),
		})
	}
	rep := build(t, r)

	ab, ok := find(rep.SymbolPairs, func(p Pair) Unit { return p.A }, "A")
	if !ok || ab.B.Name != "B" {
		t.Fatalf("pairs %+v, want A–B", rep.SymbolPairs)
	}
	if ab.Refs != 2 || ab.Approximate || ab.CoChanges != 2 || !near(ab.Logical, 2.0/3) || ab.Overlap > 0.1 {
		t.Errorf("A–B: %+v", ab)
	}
	if ab.AKnowers[0].Name != "Ada" || ab.BKnowers[0].Name != "Bob" {
		t.Errorf("knowers %+v · %+v", ab.AKnowers, ab.BKnowers)
	}
	if want := "changed together in 2 commits (67%)"; !slices.Contains(ab.Reasons, want) {
		t.Errorf("reasons %q, want %q", ab.Reasons, want)
	}
	for _, p := range rep.SymbolPairs {
		if p.A.Name == "C" || p.B.Name == "C" {
			t.Errorf("A–C are both Ada's: %+v", p)
		}
	}
	if len(rep.PackagePairs) != 1 || rep.PackagePairs[0].A.Name != "a" || rep.PackagePairs[0].B.Name != "b" {
		t.Errorf("package pairs %+v, want a–b", rep.PackagePairs)
	}
}

func TestCochangeNormalizes(t *testing.T) {
	links := map[[2]int]*link{}
	changes := cochange([][]int{{0, 1}, {0, 1, 2}, {0, 1, 2, 3}, {3}}, 3, links)
	// The four-unit commit is too big to count.
	if changes[0] != 2 || changes[3] != 1 {
		t.Errorf("changes %v", changes)
	}
	if l := links[key(0, 1)]; l.co != 2 || l.w != 1.5 {
		t.Errorf("0–1: %+v", l)
	}
	if l := links[key(1, 2)]; l.co != 1 || l.w != 0.5 {
		t.Errorf("1–2: %+v", l)
	}
	if links[key(0, 3)] != nil {
		t.Errorf("0–3 linked by the big commit")
	}
}
