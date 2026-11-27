package review

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"huginn/internal/ast"
	"huginn/internal/capslock"
	"huginn/internal/git"
	"huginn/internal/semdiff"
)

// Capability is a way a function reaches outside the process, as capslock
// finds it: the network, files, other programs or the operating system.
// Tests rarely run such a path for real, so a reviewer has to read it.
type Capability struct {
	Name string // capslock's: "NETWORK", "FILES", "EXEC", "SYSTEM_CALLS", "MODIFY_SYSTEM_STATE/ENV", ...

	// New says the function did not reach it at the merge base: the change
	// makes it, or adds the function.
	New bool

	// Path is one way there: the function, the calls it makes within the
	// repository, and the call that leaves the repository's code. When
	// capslock finds the capability further on, Path ends with the
	// function it finds it in, Elided calls after the one that leaves.
	Path   []Step
	Elided int
}

// Step is one call on a path out of the process.
type Step struct {
	Package string // import path
	Name    string // qualified with the package name: "git.(*Repo).run", "exec.(*Cmd).Output"

	// Path and Line locate the call in the repository. Both are empty for
	// the function itself and for calls made outside the repository's
	// code.
	Path string
	Line int
}

// Label names the capability as the reports print it: "network", "files",
// "exec", "system calls", or for a kind of system state, "signals" or
// "env".
func (c Capability) Label() string {
	name, kind, _ := strings.Cut(c.Name, "/")
	return strings.ToLower(strings.ReplaceAll(cmp.Or(kind, name), "_", " "))
}

// Leaves returns the index in Path of the call that leaves the
// repository's code, the last one made from it, or -1 when the function
// itself has the capability.
func (c Capability) Leaves() int {
	for i := len(c.Path) - 1; i > 0; i-- {
		if c.Path[i].Path != "" {
			return i
		}
	}
	return -1
}

// outside lists, in the order the reports give them, capslock's
// capabilities that reach outside the process. Reflection, unsafe
// pointers, reading the environment and what capslock could not analyse
// are left out: they stay in the process, or a test controls them.
var outside = []string{"EXEC", "NETWORK", "FILES", "SYSTEM_CALLS", "OPERATING_SYSTEM", "MODIFY_SYSTEM_STATE", "CGO", "ARBITRARY_EXECUTION"}

// outsideRank returns the capability's place in outside, or -1 when it
// stays in the process.
func outsideRank(name string) int {
	kind, _, _ := strings.Cut(name, "/")
	return slices.Index(outside, kind)
}

// Tracer finds with capslock which functions reach outside the process. It
// checks out each revision it traces to a temporary directory, and keeps
// what it finds by revision, so a merge base many reviews compare against
// is traced once.
type Tracer struct {
	// run runs capslock on the packages patterns match in the module at
	// dir, in a checkout at root: capslock.Tool.Run, or a test's stand-in.
	run func(ctx context.Context, dir, root string, patterns []string) (capslock.Result, error)

	mu    sync.Mutex
	found map[string]*traced // by revision and packages
	keys  []string           // the keys of found, oldest first
}

// traced is a revision's trace, done or under way.
type traced struct {
	done chan struct{}
	res  map[string]capslock.Result // by module directory
	err  error
}

// maxTraced bounds the traces a Tracer keeps.
const maxTraced = 32

// NewTracer returns a tracer running tool.
func NewTracer(tool *capslock.Tool) *Tracer {
	return &Tracer{run: tool.Run}
}

// revision traces the packages at commit sha, given by module directory as
// patterns relative to it: "./internal/git". A trace already done or
// under way for the same packages is shared; one that failed is kept too,
// unless it was cancelled.
func (t *Tracer) revision(ctx context.Context, repo *git.Repo, sha string, pkgs map[string][]string) (map[string]capslock.Result, error) {
	key := sha
	for _, mod := range slices.Sorted(maps.Keys(pkgs)) {
		key += "\x00" + mod + "\x00" + strings.Join(slices.Sorted(slices.Values(pkgs[mod])), ",")
	}
	for {
		tr, mine := t.claim(key)
		if mine {
			tr.res, tr.err = t.checkout(ctx, repo, sha, pkgs)
			if errors.Is(tr.err, context.Canceled) || errors.Is(tr.err, context.DeadlineExceeded) {
				t.forget(key, tr)
			}
			close(tr.done)
			return tr.res, tr.err
		}
		select {
		case <-tr.done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if !errors.Is(tr.err, context.Canceled) && !errors.Is(tr.err, context.DeadlineExceeded) {
			return tr.res, tr.err
		}
		// The request tracing it went away: trace it for this one.
	}
}

// claim returns the trace kept under key, and whether it is new, for the
// caller to do.
func (t *Tracer) claim(key string) (*traced, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if tr, ok := t.found[key]; ok {
		return tr, false
	}
	if t.found == nil {
		t.found = map[string]*traced{}
	}
	tr := &traced{done: make(chan struct{})}
	t.found[key] = tr
	t.keys = append(t.keys, key)
	if len(t.keys) > maxTraced {
		delete(t.found, t.keys[0])
		t.keys = t.keys[1:]
	}
	return tr, true
}

// forget drops the trace kept under key, if it is still tr.
func (t *Tracer) forget(key string, tr *traced) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.found[key] == tr {
		delete(t.found, key)
		t.keys = slices.DeleteFunc(t.keys, func(k string) bool { return k == key })
	}
}

// checkout checks out commit sha and runs capslock in each module.
func (t *Tracer) checkout(ctx context.Context, repo *git.Repo, sha string, pkgs map[string][]string) (map[string]capslock.Result, error) {
	dir, remove, err := repo.Checkout(ctx, sha)
	if err != nil {
		return nil, err
	}
	defer remove()
	out := map[string]capslock.Result{}
	for _, mod := range slices.Sorted(maps.Keys(pkgs)) {
		res, err := t.run(ctx, filepath.Join(dir, filepath.FromSlash(mod)), dir, pkgs[mod])
		if err != nil {
			return nil, err
		}
		out[mod] = res
	}
	return out, nil
}

// target is a function or method the change adds or edits, to trace.
type target struct {
	change     int
	head, base capslock.Func // base is unset for added code
	headModule string        // the directories of the modules declaring it
	baseModule string
}

// trace finds which of the report's changes reach outside the process, by
// change index: the production functions and methods the change adds or
// edits, traced at the head and, to tell what is new, at the merge base.
// It returns warnings about what it could not trace; an error means it
// could not trace the head, so nothing is known.
func (t *Tracer) trace(ctx context.Context, repo *git.Repo, rv *Review, in semdiff.Input) (map[int][]Capability, []string, error) {
	pkgOf := semdiff.PackagePaths(in.GoMod)
	mods := moduleDirs(in.GoMod)
	headPkgs, basePkgs := map[string][]string{}, map[string][]string{}
	var targets []target
	for i, c := range rv.Report.Changes {
		if !traceable(c) {
			continue
		}
		tg := target{change: i, head: capslock.Func{Package: c.Package, Name: local(c.Name)}}
		var ok bool
		if tg.headModule, ok = mods.add(headPkgs, path.Dir(c.Path)); !ok {
			continue // outside any module, capslock cannot load it
		}
		if !c.Flags.Has(ast.Added) {
			oldName, oldPath := cmp.Or(c.OldName, c.Name), cmp.Or(c.OldPath, c.Path)
			pkgName, _, _ := strings.Cut(oldName, ".")
			tg.base = capslock.Func{Package: pkgOf(oldPath, pkgName), Name: local(oldName)}
			tg.baseModule, _ = mods.add(basePkgs, path.Dir(oldPath))
		}
		targets = append(targets, tg)
	}
	if len(targets) == 0 {
		return nil, nil, nil
	}

	var (
		wg               sync.WaitGroup
		head, base       map[string]capslock.Result
		headErr, baseErr error
	)
	wg.Go(func() { head, headErr = t.revision(ctx, repo, rv.HeadHash, headPkgs) })
	if len(basePkgs) > 0 {
		wg.Go(func() { base, baseErr = t.revision(ctx, repo, rv.MergeBase, basePkgs) })
	}
	wg.Wait()
	if headErr != nil {
		return nil, nil, headErr
	}
	var warnings []string
	if baseErr != nil {
		warnings = append(warnings, "capslock could not trace the merge base, so nothing is marked new: "+baseErr.Error())
	}

	name := packageNames(in.Tree, pkgOf)
	out := map[int][]Capability{}
	for _, tg := range targets {
		var had map[string]bool // at the merge base; nil when unknown
		if tg.base.Name != "" && baseErr == nil {
			had = map[string]bool{}
			for _, c := range base[tg.baseModule][tg.base] {
				had[c.Name] = true
			}
		}
		var caps []Capability
		for _, c := range head[tg.headModule][tg.head] {
			if outsideRank(c.Name) < 0 {
				continue
			}
			cp := shorten(c, mods.contains, name)
			cp.New = tg.base.Name == "" || (had != nil && !had[c.Name])
			caps = append(caps, cp)
		}
		slices.SortStableFunc(caps, func(a, b Capability) int {
			if a.New != b.New {
				if a.New {
					return -1
				}
				return 1
			}
			return cmp.Compare(outsideRank(a.Name), outsideRank(b.Name))
		})
		if len(caps) > 0 {
			out[tg.change] = caps
		}
	}
	return out, warnings, nil
}

// traceable reports whether c adds or edits production code capslock can
// trace: a function or method outside test files, added, or with its body
// or signature changed.
func traceable(c semdiff.Change) bool {
	if (c.Symbol != "func" && c.Symbol != "method") || strings.HasSuffix(c.Path, "_test.go") || c.Flags.Has(ast.Deleted) {
		return false
	}
	return c.Flags.Has(ast.Added) || c.Flags.Has(ast.BodyChanged) || c.Flags.Has(ast.SignatureChanged)
}

// local drops the package name from a qualified name: "(*Repo).Tree" for
// "git.(*Repo).Tree".
func local(name string) string {
	_, n, _ := strings.Cut(name, ".")
	return n
}

// shorten keeps what a reader needs of a capslock path: the calls within
// the repository, the call that leaves it, and the function capslock finds
// the capability in when that is further on.
func shorten(c capslock.Capability, inRepo func(string) bool, name func(string) string) Capability {
	out := Capability{Name: c.Name}
	leave := len(c.Path) - 1
	for i, s := range c.Path {
		if !inRepo(s.Package) {
			leave = i
			break
		}
	}
	keep := slices.Clone(c.Path[:leave+1])
	if last := len(c.Path) - 1; last > leave {
		out.Elided = last - leave - 1
		keep = append(keep, c.Path[last])
	}
	for _, s := range keep {
		out.Path = append(out.Path, Step{Package: s.Package, Name: name(s.Package) + "." + s.Name, Path: s.Path, Line: s.Line})
	}
	return out
}

// modules maps the repository directories holding a go.mod, "." for the
// root, to the module path each declares.
type modules map[string]string

func moduleDirs(gomod map[string][]byte) modules {
	m := modules{}
	for p, src := range gomod {
		if mod := semdiff.ModulePath(src); mod != "" {
			m[path.Dir(p)] = mod
		}
	}
	return m
}

// add adds the package in the repository directory dir to pkgs, under the
// directory of its module, as a pattern relative to it, and returns that
// directory. It returns false when no module holds dir.
func (m modules) add(pkgs map[string][]string, dir string) (string, bool) {
	mod := dir
	for {
		if _, ok := m[mod]; ok {
			break
		}
		if mod == "." || mod == "/" {
			return "", false
		}
		mod = path.Dir(mod)
	}
	p := "."
	switch {
	case dir == mod:
	case mod == ".":
		p = "./" + dir
	default:
		p = "./" + strings.TrimPrefix(dir, mod+"/")
	}
	if !slices.Contains(pkgs[mod], p) {
		pkgs[mod] = append(pkgs[mod], p)
	}
	return mod, true
}

// contains reports whether the package with import path pkg is one of the
// repository's.
func (m modules) contains(pkg string) bool {
	for _, mod := range m {
		if pkg == mod || strings.HasPrefix(pkg, mod+"/") {
			return true
		}
	}
	return false
}

// packageNames returns the function naming a package by its import path:
// as its files at the head declare it, for the repository's packages, and
// as guessName guesses for the others.
func packageNames(tree []*ast.File, pkgOf func(path, pkg string) string) func(string) string {
	names := map[string]string{}
	for _, f := range tree {
		if !strings.HasSuffix(f.Path, "_test.go") {
			names[pkgOf(f.Path, f.Package)] = f.Package
		}
	}
	return func(pkg string) string {
		if n, ok := names[pkg]; ok {
			return n
		}
		return guessName(pkg)
	}
}

// guessName guesses a package's name from its import path: the last
// element, without a major version, as "yaml" for gopkg.in/yaml.v3 and
// "pgx" for github.com/jackc/pgx/v5.
func guessName(pkg string) string {
	elems := strings.Split(pkg, "/")
	last := elems[len(elems)-1]
	if len(elems) > 1 && major(last) {
		last = elems[len(elems)-2]
	}
	if i := strings.LastIndex(last, "."); i > 0 && major(last[i+1:]) {
		last = last[:i]
	}
	return last
}

// major reports whether s is a major version suffix: "v2", "v10".
func major(s string) bool {
	return len(s) > 1 && s[0] == 'v' && strings.Trim(s[1:], "0123456789") == ""
}
