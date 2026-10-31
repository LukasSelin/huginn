package journey

import (
	"cmp"
	"path"
	"slices"
	"strings"

	"huginn/internal/ast"
	"huginn/internal/lineage"
	"huginn/internal/modpath"
)

// Link is a live symbol referring to identifiers of another package of the
// repository.
type Link struct {
	ID       lineage.ID
	Name     string // qualified name of the referring symbol
	Kind     ast.Kind
	From, To string   // directories
	Names    []string // what it refers to in To, sorted
	Refs     int      // occurrences of those names
}

// Links are the references into and out of one package directory at a
// revision.
type Links struct {
	Dir string
	// Symbols are the live symbols of Dir at the revision, by qualified
	// name, so that the names others refer to can be told apart from the
	// ones that are gone or are not symbols of their own.
	Symbols []LinkSymbol
	Out     []Link // Dir's symbols referring elsewhere, by To then Name
	In      []Link // other symbols referring to Dir, by From then Name
}

// LinkSymbol is a live symbol of the package a Links is about.
type LinkSymbol struct {
	ID   lineage.ID
	Name string // qualified
	Kind ast.Kind
}

// LinksAt returns the references into and out of package directory dir
// among the symbols live after revision rev, an index into the applied
// revisions; with rev < 0 nothing is live.
func LinksAt(h *lineage.History, dir string, rev int, tests bool) *Links {
	counts := func(s *ast.Symbol) bool { return s != nil && (tests || !strings.HasSuffix(s.Path, "_test.go")) }
	type live struct {
		id  lineage.ID
		sym *ast.Symbol
	}
	var all []*ast.Symbol
	var now []live
	for sym := range h.All() {
		var cur *ast.Symbol
		for _, e := range sym.Events {
			for _, s := range []*ast.Symbol{e.Old, e.New} {
				if counts(s) {
					all = append(all, s)
				}
			}
			if pos, ok := h.Position(e.SHA); ok && pos <= rev {
				cur = e.New
			}
		}
		if counts(cur) {
			now = append(now, live{sym.ID, cur})
		}
	}
	mods := modpath.Infer(all)

	out := &Links{Dir: dir, Symbols: []LinkSymbol{}, Out: []Link{}, In: []Link{}}
	for _, l := range now {
		from := path.Dir(l.sym.Path)
		if from == dir {
			out.Symbols = append(out.Symbols, LinkSymbol{ID: l.id, Name: l.sym.QualifiedName(), Kind: l.sym.Kind})
		}
		byDir := map[string]*Link{}
		for _, ref := range l.sym.Refs {
			if ref.Kind != ast.RefImport {
				continue
			}
			to := mods.Dir(ref.Pkg)
			if to == "" || to == from || (from != dir && to != dir) {
				continue
			}
			k := byDir[to]
			if k == nil {
				k = &Link{ID: l.id, Name: l.sym.QualifiedName(), Kind: l.sym.Kind, From: from, To: to}
				byDir[to] = k
			}
			if !slices.Contains(k.Names, ref.Name) {
				k.Names = append(k.Names, ref.Name)
			}
			k.Refs += max(1, ref.Count)
		}
		for _, k := range byDir {
			slices.Sort(k.Names)
			if from == dir {
				out.Out = append(out.Out, *k)
			} else {
				out.In = append(out.In, *k)
			}
		}
	}
	slices.SortFunc(out.Symbols, func(a, b LinkSymbol) int { return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID)) })
	slices.SortFunc(out.Out, func(a, b Link) int {
		return cmp.Or(cmp.Compare(a.To, b.To), cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID))
	})
	slices.SortFunc(out.In, func(a, b Link) int {
		return cmp.Or(cmp.Compare(a.From, b.From), cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID))
	})
	return out
}
