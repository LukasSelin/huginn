package semdiff

import (
	"strings"

	"huginn/internal/ast"
)

// MemberChange is one changed field, method, embedded type or type term of a
// struct or interface type.
type MemberChange struct {
	Flags  ast.MemberFlags
	Member string // kind of member: "field", "method", "embedded field", ...

	Name    string // after the change, or before it for a removal; empty for type terms
	OldName string // the name before a rename; empty otherwise

	// OldDecl and NewDecl render the member as declared on each side,
	// "Name string `json:\"name\"`"; empty on the side it is absent.
	OldDecl, NewDecl string

	Exported bool // the member's name is exported on either side

	// Breaking reports that the change can break code in other packages:
	// Breaks says whose, "callers" or "implementers" or both. Only members
	// of exported types in importable packages can break.
	Breaking bool
	Breaks   string

	// Uses counts the field accesses and struct literal keys in the tree that
	// name the old field, for a field that was removed, retyped or renamed.
	// Approximate is set when some were matched by field name only.
	Uses        int
	Approximate bool

	// Implementers are the named types in the tree that implement the
	// interface as it was before the commit, for a change that breaks
	// implementers: the types it can break. The search is approximate, see
	// index.implementers.
	Implementers []Caller

	// Unkeyed counts the unkeyed literals of the struct in the tree,
	// T{1, "x"}, for a field that was added or removed. They list every
	// field by position, so the change breaks them even when the field is
	// unexported. The count may include literals of a named slice or array
	// type of the same name.
	Unkeyed int
}

// memberChanges converts a type's member changes and counts the uses of the
// fields that changed in ways their users notice.
func memberChanges(c ast.Change, pkgOf func(path, pkg string) string, idx *index) []MemberChange {
	if len(c.Members) == 0 {
		return nil
	}
	pkg := pkgOf(c.Old.Path, c.Old.Package)
	api := isAPI(c.Old)
	out := make([]MemberChange, 0, len(c.Members))
	var impls []Caller
	searched := false
	for _, mc := range c.Members {
		cur := mc.Cur()
		rc := MemberChange{Flags: mc.Flags, Member: cur.Kind.String(), Name: cur.Name}
		if o := mc.Old; o != nil {
			rc.OldDecl = o.String()
			rc.Exported = o.Exported
			if mc.Flags.Has(ast.MemberRenamed) {
				rc.OldName = o.Name
			}
		}
		if n := mc.New; n != nil {
			rc.NewDecl = n.String()
			rc.Exported = rc.Exported || n.Exported
		}
		if api {
			rc.Breaks = breaks(mc)
			rc.Breaking = rc.Breaks != ""
		}
		if strings.Contains(rc.Breaks, "implementers") {
			if !searched {
				impls, searched = idx.implementers(c.Old, c.New, pkg), true
			}
			rc.Implementers = impls
		}
		if o := mc.Old; o != nil && (o.Kind == ast.MemberField || o.Kind == ast.MemberEmbedded) &&
			mc.Flags&(ast.MemberRemoved|ast.MemberRetyped|ast.MemberRenamed) != 0 {
			rc.Uses, rc.Approximate = idx.fieldUses(c.Old.Name, o.Name, pkg)
		}
		if m := mc.Cur(); (m.Kind == ast.MemberField || m.Kind == ast.MemberEmbedded) &&
			mc.Flags&(ast.MemberAdded|ast.MemberRemoved) != 0 {
			rc.Unkeyed = idx.unkeyedLiterals(c.Old.Name, pkg)
		}
		out = append(out, rc)
	}
	return out
}

// breaks says whose code a member change of an exported type breaks, or
// returns "" when it breaks no one outside the package.
//
// Removing, retyping or renaming an exported field, or an embedded one whose
// promoted fields and methods may be in use, breaks callers; adding a field
// does not. Adding a method or embedded interface to an interface breaks
// its implementers, removing one breaks its callers, and changing a method
// breaks both. Removing a type term breaks instantiations with that type;
// adding one can break generic code relying on the old type set.
func breaks(mc ast.MemberChange) string {
	m := mc.Cur()
	f := mc.Flags
	gone := f&(ast.MemberRemoved|ast.MemberRetyped|ast.MemberRenamed) != 0
	switch m.Kind {
	case ast.MemberField:
		if gone && mc.Old.Exported {
			return "callers"
		}
	case ast.MemberEmbedded:
		if gone {
			return "callers"
		}
	case ast.MemberMethod:
		switch {
		case f.Has(ast.MemberAdded):
			return "implementers"
		case gone && mc.Old.Exported && f.Has(ast.MemberRemoved):
			return "callers"
		case gone && mc.Old.Exported:
			return "callers and implementers"
		}
	case ast.MemberInterface:
		switch {
		case f.Has(ast.MemberAdded):
			return "implementers"
		case f.Has(ast.MemberRemoved):
			return "callers"
		}
	case ast.MemberTerm:
		switch {
		case f.Has(ast.MemberAdded):
			return "generic code"
		case f.Has(ast.MemberRemoved):
			return "callers"
		}
	}
	return ""
}

// memberSummary describes a type's member changes in one line:
// "+1 field · −1 method · 1 retyped".
func memberSummary(ms []ast.MemberChange) string {
	added, removed := map[string]int{}, map[string]int{}
	var addOrder, removeOrder []string
	var retyped, tags, renamed int
	for _, mc := range ms {
		noun := mc.Cur().Kind.String()
		switch {
		case mc.Flags.Has(ast.MemberAdded):
			if added[noun] == 0 {
				addOrder = append(addOrder, noun)
			}
			added[noun]++
		case mc.Flags.Has(ast.MemberRemoved):
			if removed[noun] == 0 {
				removeOrder = append(removeOrder, noun)
			}
			removed[noun]++
		}
		if mc.Flags.Has(ast.MemberRetyped) {
			retyped++
		}
		if mc.Flags.Has(ast.MemberTagChanged) {
			tags++
		}
		if mc.Flags.Has(ast.MemberRenamed) {
			renamed++
		}
	}
	var parts []string
	for _, noun := range addOrder {
		parts = append(parts, "+"+plural(added[noun], noun))
	}
	for _, noun := range removeOrder {
		parts = append(parts, "−"+plural(removed[noun], noun))
	}
	if retyped > 0 {
		parts = append(parts, plural(retyped, "member")+" retyped")
	}
	if renamed > 0 {
		parts = append(parts, plural(renamed, "member")+" renamed")
	}
	if tags > 0 {
		parts = append(parts, plural(tags, "tag")+" changed")
	}
	return strings.Join(parts, " · ")
}
