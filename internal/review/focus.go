package review

import (
	"cmp"
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
)

func (k FocusKind) String() string {
	switch k {
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
// not show, such as the existing code an edit reaches or the import between
// packages it starts. New code is left out, since the diff shows all of it.
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
}

// focus lists what to look at first in r, most important first: exported
// API changes that break code, then edits of existing production code, the
// largest reaching the most callers first, then the imports between
// packages it starts, the most used first, then warnings about the types
// it touches.
func focus(r *semdiff.Report, types []TypeReview) []Focus {
	var breaking, behavior []Focus
	weights := map[int]float64{}
	for i, c := range r.Changes {
		switch {
		case c.APIChange:
			breaking = append(breaking, breakingFocus(i, c))
		case editsBehavior(c):
			f := Focus{Kind: FocusBehavior, Name: c.Name, Path: c.Path, Line: c.Line, What: c.Summary, Change: i}
			f.Reaches, f.Tests = production(c.Callers)
			behavior = append(behavior, f)
			weights[i] = weight(c, f.Reaches)
		}
	}
	slices.SortStableFunc(behavior, func(a, b Focus) int { return cmp.Compare(weights[b.Change], weights[a.Change]) })

	out := append(breaking, behavior...)
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
