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

	// Group numbers the runs of members not separated by a blank line, from
	// 0: a blank line before a member, or before its doc comment, starts the
	// next group. Comments alone do not.
	Group int

	typeKey string // Type with parameter names dropped; used for comparison
}

// MethodKey returns an interface method's parameter and result types without
// names, "(int, string)(error)", comparable with Symbol.MethodKey. It is
// empty for other members.
func (m *Member) MethodKey() string {
	if m.Kind != MemberMethod {
		return ""
	}
	return m.typeKey
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
	l := &memberList{fset: fset}
	switch t := sp.Type.(type) {
	case *goast.StructType:
		l.structMembers(t)
	case *goast.InterfaceType:
		l.interfaceMembers(t)
	}
	return l.out
}

// memberList gathers the members of a type in declaration order.
type memberList struct {
	fset    *token.FileSet
	out     []Member
	group   int // of the field being read
	prevEnd int // the line the field before it ends on; 0 for none
}

// next moves to the group of field f: the next one when a blank line
// separates f, with its doc comment, from the field before it.
func (l *memberList) next(f *goast.Field) {
	start, end := f.Pos(), f.End()
	if f.Doc != nil {
		start = f.Doc.Pos()
	}
	if f.Comment != nil {
		end = f.Comment.End()
	}
	if line := l.fset.Position(start).Line; l.prevEnd > 0 && line > l.prevEnd+1 {
		l.group++
	}
	l.prevEnd = l.fset.Position(end).Line
}

// add appends m, declared at n, to the current group.
func (l *memberList) add(m Member, n goast.Node) {
	m.Index = len(l.out)
	m.Line = l.fset.Position(n.Pos()).Line
	m.Group = l.group
	if m.typeKey == "" {
		m.typeKey = m.Type
	}
	l.out = append(l.out, m)
}

// structMembers adds each field of a struct type: one member per declared
// name, or one for an embedded field.
func (l *memberList) structMembers(t *goast.StructType) {
	for _, f := range t.Fields.List {
		l.next(f)
		typ := types.ExprString(f.Type)
		tag := fieldTag(f.Tag)
		if len(f.Names) == 0 {
			name := embeddedName(f.Type)
			l.add(Member{Kind: MemberEmbedded, Name: name, Type: typ, Tag: tag, Exported: goast.IsExported(name)}, f)
			continue
		}
		for _, id := range f.Names {
			l.add(Member{Kind: MemberField, Name: id.Name, Type: typ, Tag: tag, Exported: goast.IsExported(id.Name)}, id)
		}
	}
}

// fieldTag returns a struct tag unquoted, or as written when it does not
// unquote. It is empty when the field has no tag.
func fieldTag(lit *goast.BasicLit) string {
	if lit == nil {
		return ""
	}
	if s, err := strconv.Unquote(lit.Value); err == nil {
		return s
	}
	return lit.Value
}

// interfaceMembers adds each element of an interface type: its methods,
// embedded interfaces and type terms.
func (l *memberList) interfaceMembers(t *goast.InterfaceType) {
	for _, f := range t.Methods.List {
		l.next(f)
		if ft, ok := f.Type.(*goast.FuncType); ok && len(f.Names) > 0 {
			sig := strings.TrimPrefix(types.ExprString(ft), "func")
			key := fieldList(ft.Params, "(", ")", false) + fieldList(ft.Results, "(", ")", false)
			for _, id := range f.Names {
				l.add(Member{Kind: MemberMethod, Name: id.Name, Type: sig, Exported: goast.IsExported(id.Name), typeKey: key}, id)
			}
			continue
		}
		typ := types.ExprString(f.Type)
		switch f.Type.(type) {
		case *goast.BinaryExpr, *goast.UnaryExpr:
			l.add(Member{Kind: MemberTerm, Type: typ}, f)
		default:
			name := embeddedName(f.Type)
			l.add(Member{Kind: MemberInterface, Name: name, Type: typ, Exported: goast.IsExported(name)}, f)
		}
	}
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
	out, matchedOld, added := matchMembers(o.Members, n.Members)

	for i := range o.Members {
		if matchedOld[i] {
			continue
		}
		om := &o.Members[i]
		rename := renameOf(om, n.Members, added)
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

// matchMembers pairs the old and new members of a type by key and reports
// the pairs whose type or tag changed. It also returns which old members it
// paired and the indexes of the new members it could not pair, in order.
func matchMembers(oldMembers, newMembers []Member) (out []MemberChange, matchedOld []bool, added []int) {
	byKey := map[string]int{}
	oldKeys := memberKeys(oldMembers)
	for i, k := range oldKeys {
		byKey[k] = i
	}
	matchedOld = make([]bool, len(oldMembers))
	for j, k := range memberKeys(newMembers) {
		i, ok := byKey[k]
		if !ok {
			added = append(added, j)
			continue
		}
		matchedOld[i] = true
		om, nm := &oldMembers[i], &newMembers[j]
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
	return out, matchedOld, added
}

// renameOf returns the position in added of the new member that om was
// renamed to, or -1. Only a field or method can be renamed, and only to one
// of the same kind with the same type at the same position.
func renameOf(om *Member, newMembers []Member, added []int) int {
	if om.Kind != MemberField && om.Kind != MemberMethod {
		return -1
	}
	for x, j := range added {
		nm := &newMembers[j]
		if nm.Kind == om.Kind && nm.Index == om.Index && nm.typeKey == om.typeKey {
			return x
		}
	}
	return -1
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
