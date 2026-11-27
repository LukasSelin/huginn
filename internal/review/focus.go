package review

import (
	"cmp"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"

	"huginn/internal/ast"
	"huginn/internal/semdiff"
)

// FocusKind says why a reviewer should look at something first.
type FocusKind uint8

const (
	// FocusBreaking is exported API changed in a way that breaks the code
	// depending on it: its callers, or the implementers of an interface.
	FocusBreaking FocusKind = iota + 1

	// FocusBehavior is existing production code the change edits, so that
	// the code using it now runs something else.
	FocusBehavior

	// FocusDependency is an import between packages the change starts.
	FocusDependency

	// FocusStructure is a warning about the blocks of a type it touches.
	FocusStructure

	// FocusExternal is production code the change adds or edits whose
	// calls reach outside the process: the network, files, other programs
	// or the operating system, as capslock finds. Tests rarely run those
	// paths for real, so they come first.
	FocusExternal
)

func (k FocusKind) String() string {
	switch k {
	case FocusExternal:
		return "external"
	case FocusBreaking:
		return "breaking"
	case FocusBehavior:
		return "behavior"
	case FocusDependency:
		return "dependency"
	case FocusStructure:
		return "structure"
	}
	return "unknown"
}

// MarshalText encodes a kind by its name.
func (k FocusKind) MarshalText() ([]byte, error) { return []byte(k.String()), nil }

// Focus is one thing to look at first in a merge request: what its diff does
// not show, such as where its calls lead outside the process, the existing
// code an edit reaches or the import between packages it starts. New code
// is left out, since the diff shows all of it, unless it reaches outside
// the process.
type Focus struct {
	Kind FocusKind
	Name string // the symbol or type, or "from → to" import paths
	Path string // where it is declared, or the file that imports
	Line int

	What string // what changed: "+33 stmts · cyclomatic 13 → 18"
	Why  string // why it matters, when Reaches does not say it: "breaks implementers"

	// Change indexes Report.Changes for a symbol's change, and is -1
	// otherwise.
	Change int

	// Reaches is the production code the change reaches, most uses first:
	// the declarations using what changed or, when Implementers is set, the
	// types implementing an interface the change breaks. Tests counts those
	// declared in test files, which Reaches leaves out.
	Reaches      []semdiff.Caller
	Tests        int
	Implementers bool

	// Capabilities are the ways a FocusExternal item reaches outside the
	// process, those the change makes first.
	Capabilities []Capability
}

// Exit returns the call by which a FocusExternal item's first capability
// leaves the code the change adds or edits (see Capability.Exit). Several
// functions the change adds or edits often reach outside through the same
// changed call; the focus lists the items sharing an exit together (see
// SameExit), so that call is read once.
func (f Focus) Exit() (Step, bool) {
	if len(f.Capabilities) == 0 {
		return Step{}, false
	}
	return f.Capabilities[0].Exit()
}

// SameExit reports whether f and g leave the repository's code by the same
// call: the same function, called on the same line.
func (f Focus) SameExit(g Focus) bool {
	x, okF := f.Exit()
	y, okG := g.Exit()
	return okF && okG && x == y
}

// MarshalJSON encodes an item's fields and, under "Exit", the call Exit
// returns, or null for an item without one.
func (f Focus) MarshalJSON() ([]byte, error) {
	type fields Focus // without this method
	var exit *Step
	if s, ok := f.Exit(); ok {
		exit = &s
	}
	return json.Marshal(struct {
		fields
		Exit *Step
	}{fields(f), exit})
}

// focus lists what to look at first in r, most important first: the code
// the change adds or edits whose calls reach outside the process, by caps
// (indexed as the changes), then exported API changes that break code,
// then edits of existing production code, the largest reaching the most
// callers first, then the imports between packages it starts, the most
// used first, then warnings about the types it touches.
//
// Code reaching outside the process comes first: what it does there
// depends on the world, which tests rarely bring along. Of it, existing
// code the change makes reach somewhere new comes first, then new code,
// then the rest, each the largest edit reaching the most callers first;
// and the items leaving the changed code by the same call come
// together, where the first of them is.
func focus(r *semdiff.Report, types []TypeReview, caps map[int][]Capability) []Focus {
	var external, breaking, behavior []Focus
	weights := map[int]float64{}
	for i, c := range r.Changes {
		var f Focus
		switch {
		case c.APIChange:
			f = breakingFocus(i, c)
		case editsBehavior(c) || len(caps[i]) > 0:
			f = Focus{Kind: FocusBehavior, Name: c.Name, Path: c.Path, Line: c.Line, What: c.Summary, Change: i}
			if c.Flags.Has(ast.Added) {
				f.What = "new " + c.Symbol + " · " + c.Summary
			}
			f.Reaches, f.Tests = production(c.Callers)
		default:
			continue
		}
		weights[i] = weight(c, f.Reaches)
		switch {
		case len(caps[i]) > 0:
			f.Kind, f.Capabilities = FocusExternal, caps[i]
			external = append(external, f)
		case f.Kind == FocusBreaking:
			breaking = append(breaking, f)
		default:
			behavior = append(behavior, f)
		}
	}
	byWeight := func(a, b Focus) int { return cmp.Compare(weights[b.Change], weights[a.Change]) }
	slices.SortStableFunc(behavior, byWeight)
	slices.SortStableFunc(external, func(a, b Focus) int {
		return cmp.Or(cmp.Compare(novelty(r, b), novelty(r, a)), byWeight(a, b))
	})

	out := append(byExit(external), breaking...)
	out = append(out, behavior...)
	out = append(out, dependencies(r.Relations)...)
	for _, t := range types {
		for _, n := range t.Notes {
			if n.Warn {
				out = append(out, Focus{Kind: FocusStructure, Name: t.Name, Path: t.Path, Line: t.Line, What: n.Text, Change: -1})
			}
		}
	}
	return out
}

// byExit moves up next to each item the later ones sharing its exit,
// keeping the order otherwise: the items before a group's first stay
// before it, and its members keep their order.
func byExit(fs []Focus) []Focus {
	lead := make([]int, len(fs)) // the first item sharing its exit, by index
	for i, f := range fs {
		if lead[i] = slices.IndexFunc(fs[:i], f.SameExit); lead[i] < 0 {
			lead[i] = i
		}
	}
	order := make([]int, len(fs))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int { return cmp.Compare(lead[a], lead[b]) })
	out := make([]Focus, len(fs))
	for k, i := range order {
		out[k] = fs[i]
	}
	return out
}

// novelty ranks an item reaching outside the process: 2 for existing code
// the change makes reach somewhere new, 1 for new code, 0 for the rest.
func novelty(r *semdiff.Report, f Focus) int {
	if r.Changes[f.Change].Flags.Has(ast.Added) {
		return 1
	}
	if slices.ContainsFunc(f.Capabilities, func(c Capability) bool { return c.New }) {
		return 2
	}
	return 0
}

// editsBehavior reports whether c edits existing production code in a way
// its users can notice: its body, signature or value changed, or it was
// deleted. Pure renames and moves are left out, as are test files.
func editsBehavior(c semdiff.Change) bool {
	if strings.HasSuffix(c.Path, "_test.go") || strings.HasSuffix(c.OldPath, "_test.go") {
		return false
	}
	return c.Flags.Has(ast.BodyChanged) || c.Flags.Has(ast.SignatureChanged) || c.Flags.Has(ast.Deleted)
}

// breakingFocus describes a change to exported API: what it breaks, and
// whose code.
func breakingFocus(i int, c semdiff.Change) Focus {
	f := Focus{Kind: FocusBreaking, Name: c.Name, Path: c.Path, Line: c.Line, Change: i}
	var whose []string
	var names []string
	for _, m := range c.Members {
		if !m.Breaking {
			continue
		}
		names = append(names, cmp.Or(m.Name, m.OldName))
		for _, w := range strings.Split(m.Breaks, " and ") {
			if !slices.Contains(whose, w) {
				whose = append(whose, w)
			}
		}
		if strings.Contains(m.Breaks, "implementers") {
			f.Implementers = true
			f.Reaches, f.Tests = production(m.Implementers) // every member shares the one list
		}
	}
	switch {
	case len(names) > 0:
		f.What = c.Summary + ": " + strings.Join(names, ", ")
	case c.Kind == semdiff.KindDeleted:
		f.What = "deleted"
	case c.Kind == semdiff.KindRenamed:
		f.What = "renamed from " + c.OldName
	case c.Kind == semdiff.KindSignature:
		f.What = "signature changed"
	default:
		f.What = c.Summary
	}
	if len(whose) == 0 {
		whose = []string{"callers"}
	}
	f.Why = "breaks " + strings.Join(whose, " and ")
	if !f.Implementers {
		f.Reaches, f.Tests = production(c.Callers)
	}
	return f
}

// weight ranks an edit of existing code: how much of it changed, times how
// far it reaches. Complexity counts three times a statement, and reach
// grows with the logarithm of the call sites.
func weight(c semdiff.Change, reaches []semdiff.Caller) float64 {
	abs := func(n int) int { return max(n, -n) }
	size := 1 + abs(c.StmtsAfter-c.StmtsBefore) + 3*abs(c.ComplexityAfter-c.ComplexityBefore) + len(c.Members)
	sites := 0
	for _, cl := range reaches {
		sites += cl.Calls
	}
	return float64(size) * (1 + math.Log2(1+float64(sites)))
}

// production splits callers into those in production code, in order, and
// the number declared in test files.
func production(cs []semdiff.Caller) (prod []semdiff.Caller, tests int) {
	for _, c := range cs {
		if c.Test {
			tests++
		} else {
			prod = append(prod, c)
		}
	}
	return prod, tests
}

// dependencies lists the imports between packages the change starts, those
// with the most references along them first.
func dependencies(rel semdiff.Relations) []Focus {
	refs := map[[2]string]int{}
	first := map[[2]string]semdiff.Reference{}
	for _, r := range rel.AddedRefs {
		k := [2]string{r.FromPackage, r.ToPackage}
		if refs[k] == 0 {
			first[k] = r
		}
		refs[k]++
	}
	deps := slices.Clone(rel.AddedDeps)
	slices.SortStableFunc(deps, func(a, b semdiff.Dependency) int {
		return cmp.Compare(refs[[2]string{b.From, b.To}], refs[[2]string{a.From, a.To}])
	})
	out := make([]Focus, 0, len(deps))
	for _, d := range deps {
		k := [2]string{d.From, d.To}
		f := Focus{Kind: FocusDependency, Name: d.From + " → " + d.To, What: "new import", Change: -1}
		if n := refs[k]; n > 0 {
			f.What += " · " + plural(n, "reference")
			f.Path, f.Line = first[k].Path, first[k].Line
		} else if len(d.Files) > 0 {
			f.Path = d.Files[0]
		}
		switch d.Crossing {
		case semdiff.Outward:
			f.Why = "out of the scope"
		case semdiff.Inward:
			f.Why = "into the scope"
		case semdiff.Within:
		}
		out = append(out, f)
	}
	return out
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
