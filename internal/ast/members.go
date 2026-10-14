package ast

import (
	"cmp"
	"fmt"
	goast "go/ast"
	"go/token"
	"go/types"
	"slices"
	"strconv"
	"strings"
)

// MemberKind is the kind of element a struct or interface type declares.
type MemberKind uint8

const (
	MemberField     MemberKind = iota + 1 // named struct field
	MemberEmbedded                        // embedded struct field
	MemberMethod                          // interface method
	MemberInterface                       // embedded interface, or a single non-union type element
	MemberTerm                            // type-set term of a constraint: ~int | ~string
)

func (k MemberKind) String() string {
	switch k {
	case MemberField:
		return "field"
	case MemberEmbedded:
		return "embedded field"
	case MemberMethod:
		return "method"
	case MemberInterface:
		return "embedded interface"
	case MemberTerm:
		return "type term"
	}
	return "unknown"
}

// Member is one element of a struct or interface type: a field, a method, an
// embedded type or a type-set term.
type Member struct {
	Kind MemberKind

	// Name is the field or method name. An embedded type is named as Go
	// names the field: by its base type, "Reader" for *io.Reader. A type
	// term has no name.
	Name string

	// Type is the field's type, the embedded type, the term, or a method's
	// signature without "func": "(p []byte) (n int, err error)".
	Type string

	Tag      string // unquoted struct tag; empty when absent
	Exported bool   // Name is exported; false for type terms
	Index    int    // position among the type's members
	Line     int

	typeKey string // Type with parameter names dropped; used for comparison
}

// Embedded reports that the member is an embedded struct field or interface.
func (m *Member) Embedded() bool { return m.Kind == MemberEmbedded || m.Kind == MemberInterface }

// String renders the member as it would be declared: "Name string `json:"name"`".
func (m *Member) String() string {
	var s string
	switch m.Kind {
	case MemberField:
		s = m.Name + " " + m.Type
	case MemberMethod:
		s = m.Name + m.Type
	default:
		s = m.Type
	}
	if m.Tag != "" {
		s += " `" + m.Tag + "`"
	}
	return s
}

// typeMembers returns the members of a struct or interface type
// declaration, or nil for any other type.
func typeMembers(fset *token.FileSet, sp *goast.TypeSpec) []Member {
	var out []Member
	add := func(m Member, n goast.Node) {
		m.Index = len(out)
		m.Line = fset.Position(n.Pos()).Line
		if m.typeKey == "" {
			m.typeKey = m.Type
		}
		out = append(out, m)
	}

	switch t := sp.Type.(type) {
	case *goast.StructType:
		for _, f := range t.Fields.List {
			typ := types.ExprString(f.Type)
			tag := ""
			if f.Tag != nil {
				if s, err := strconv.Unquote(f.Tag.Value); err == nil {
					tag = s
				} else {
					tag = f.Tag.Value
				}
			}
			if len(f.Names) == 0 {
				name := embeddedName(f.Type)
				add(Member{Kind: MemberEmbedded, Name: name, Type: typ, Tag: tag, Exported: goast.IsExported(name)}, f)
				continue
			}
			for _, id := range f.Names {
				add(Member{Kind: MemberField, Name: id.Name, Type: typ, Tag: tag, Exported: goast.IsExported(id.Name)}, id)
			}
		}
	case *goast.InterfaceType:
		for _, f := range t.Methods.List {
			if ft, ok := f.Type.(*goast.FuncType); ok && len(f.Names) > 0 {
				sig := strings.TrimPrefix(types.ExprString(ft), "func")
				key := fieldList(ft.Params, "(", ")", false) + fieldList(ft.Results, "(", ")", false)
				for _, id := range f.Names {
					add(Member{Kind: MemberMethod, Name: id.Name, Type: sig, Exported: goast.IsExported(id.Name), typeKey: key}, id)
				}
				continue
			}
			typ := types.ExprString(f.Type)
			switch f.Type.(type) {
			case *goast.BinaryExpr, *goast.UnaryExpr:
				add(Member{Kind: MemberTerm, Type: typ}, f)
			default:
				name := embeddedName(f.Type)
				add(Member{Kind: MemberInterface, Name: name, Type: typ, Exported: goast.IsExported(name)}, f)
			}
		}
	}
	return out
}

// embeddedName is the field name Go gives an embedded type: its base type
// name without pointer, package qualifier or type arguments.
func embeddedName(e goast.Expr) string {
	for {
		switch x := e.(type) {
		case *goast.StarExpr:
			e = x.X
		case *goast.ParenExpr:
			e = x.X
		case *goast.IndexExpr:
			e = x.X
		case *goast.IndexListExpr:
			e = x.X
		case *goast.SelectorExpr:
			return x.Sel.Name
		case *goast.Ident:
			return x.Name
		default:
			return types.ExprString(e)
		}
	}
}

// MemberFlags describes how a member changed. Retyped, TagChanged and
// Renamed can combine; Added and Removed stand alone.
type MemberFlags uint8

const (
	MemberAdded MemberFlags = 1 << iota
	MemberRemoved
	MemberRetyped
	MemberTagChanged
	MemberRenamed
)

var memberFlagNames = []string{"added", "removed", "retyped", "tag", "renamed"}

func (f MemberFlags) Has(x MemberFlags) bool { return f&x == x }

func (f MemberFlags) String() string {
	var parts []string
	for i, name := range memberFlagNames {
		if f&(1<<i) != 0 {
			parts = append(parts, name)
		}
	}
	return strings.Join(parts, "|")
}

// MemberChange is one member-level difference between two revisions of a
// type. Old is nil for an added member, New for a removed one.
type MemberChange struct {
	Flags    MemberFlags
	Old, New *Member
}

// Cur returns the member after the change, or before it for a removal.
func (c *MemberChange) Cur() *Member {
	if c.New != nil {
		return c.New
	}
	return c.Old
}

// DiffMembers compares the members of two revisions of a type. Members are
// matched by kind and name (embedded interfaces and type terms by their
// type). A removed and an added field or method are reported as one rename
// only when they have the same type at the same position: a rename that
// also moves or retypes the member shows as a removal and an addition.
func DiffMembers(o, n *Symbol) []MemberChange {
	if o == nil || n == nil || o.Kind != KindType || n.Kind != KindType {
		return nil
	}
	var out []MemberChange

	byKey := map[string]int{}
	oldKeys := memberKeys(o.Members)
	for i, k := range oldKeys {
		byKey[k] = i
	}
	matchedOld := make([]bool, len(o.Members))
	var added []int
	for j, k := range memberKeys(n.Members) {
		i, ok := byKey[k]
		if !ok {
			added = append(added, j)
			continue
		}
		matchedOld[i] = true
		om, nm := &o.Members[i], &n.Members[j]
		var f MemberFlags
		if om.typeKey != nm.typeKey {
			f |= MemberRetyped
		}
		if om.Tag != nm.Tag {
			f |= MemberTagChanged
		}
		if f != 0 {
			out = append(out, MemberChange{Flags: f, Old: om, New: nm})
		}
	}

	for i := range o.Members {
		if matchedOld[i] {
			continue
		}
		om := &o.Members[i]
		rename := -1
		if om.Kind == MemberField || om.Kind == MemberMethod {
			for x, j := range added {
				nm := &n.Members[j]
				if nm.Kind == om.Kind && nm.Index == om.Index && nm.typeKey == om.typeKey {
					rename = x
					break
				}
			}
		}
		if rename < 0 {
			out = append(out, MemberChange{Flags: MemberRemoved, Old: om})
			continue
		}
		nm := &n.Members[added[rename]]
		added = slices.Delete(added, rename, rename+1)
		f := MemberRenamed
		if om.Tag != nm.Tag {
			f |= MemberTagChanged
		}
		out = append(out, MemberChange{Flags: f, Old: om, New: nm})
	}
	for _, j := range added {
		out = append(out, MemberChange{Flags: MemberAdded, New: &n.Members[j]})
	}

	slices.SortStableFunc(out, func(a, b MemberChange) int {
		return cmp.Or(cmp.Compare(a.Cur().Index, b.Cur().Index), cmp.Compare(removedFirst(a), removedFirst(b)))
	})
	return out
}

func removedFirst(c MemberChange) int {
	if c.Flags.Has(MemberRemoved) {
		return 0
	}
	return 1
}

// memberKeys identifies each member within its type. Repeated keys, such as
// blank fields, get a "#n" suffix in declaration order.
func memberKeys(ms []Member) []string {
	out := make([]string, len(ms))
	seen := map[string]int{}
	for i, m := range ms {
		k := m.Kind.String() + " " + m.Name
		if m.Kind == MemberInterface || m.Kind == MemberTerm {
			k = m.Kind.String() + " " + m.typeKey
		}
		seen[k]++
		if n := seen[k]; n > 1 {
			k = fmt.Sprintf("%s#%d", k, n)
		}
		out[i] = k
	}
	return out
}
