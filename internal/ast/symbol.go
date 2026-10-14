// Package ast extracts declaration-level symbols from Go source and compares
// them across revisions, so history can be tracked per function, method or
// type instead of per line.
package ast

// Kind is the kind of declaration a Symbol was extracted from.
type Kind uint8

const (
	KindFunc Kind = iota + 1
	KindMethod
	KindType
	KindVar
	KindConst
)

func (k Kind) String() string {
	switch k {
	case KindFunc:
		return "func"
	case KindMethod:
		return "method"
	case KindType:
		return "type"
	case KindVar:
		return "var"
	case KindConst:
		return "const"
	}
	return "unknown"
}

// Symbol is one top-level declaration in a file.
type Symbol struct {
	Kind    Kind
	Name    string // bare identifier, e.g. "Visit"
	Recv    string // receiver base type for methods, e.g. "Indexer"; empty otherwise
	PtrRecv bool

	// Key identifies the symbol within its file: "Visit" or "Indexer.Visit".
	// Repeated names (init, _) get a "#n" suffix in declaration order.
	Key string

	Path     string
	Package  string
	Exported bool

	// Signature is the declaration's header in canonical form, for display:
	// "func (*Indexer) Visit(n ast.Node, fn VisitFunc) error".
	Signature string

	StartLine, EndLine int
	Stmts              int // statements in the body, excluding blocks
	Complexity         int // cyclomatic complexity; 0 for non-functions

	// BodyHash identifies the structure of the declaration's body (function
	// body, type definition, or initializer), ignoring formatting and comments.
	BodyHash uint64

	// Refs are the symbols a function or method body refers to, sorted by
	// kind, package, receiver and name. Nil for other kinds of declarations.
	Refs []Ref

	// FieldRefs are the struct fields a function or method body uses by
	// name, sorted by package, type and name. Nil for other kinds of
	// declarations.
	FieldRefs []FieldRef

	// Members are the fields of a struct type or the methods, embedded
	// interfaces and type terms of an interface type, in declaration order.
	// Nil for other declarations. They are not part of BodyHash's input
	// beyond the type definition itself.
	Members []Member

	sigKey    string              // signature with parameter names dropped; used for comparison
	methodKey string              // a method's parameter and result types; see MethodKey
	shingles  map[uint64]struct{} // token n-grams of the body; used for fuzzy matching
}

// MethodKey returns a method's parameter and result types without names or
// receiver, "(int, string)(error)", in the form Member.MethodKey gives an
// interface method, so the two can be compared. It is empty for anything
// but a method.
func (s *Symbol) MethodKey() string { return s.methodKey }

// QualifiedName returns the name as Go writes it, e.g. "(*Indexer).Visit".
func (s *Symbol) QualifiedName() string {
	if s.Recv == "" {
		return s.Name
	}
	if s.PtrRecv {
		return "(*" + s.Recv + ")." + s.Name
	}
	return s.Recv + "." + s.Name
}

// File is the set of symbols declared in one source file.
type File struct {
	Path      string
	Package   string
	Generated bool // carries a "Code generated ... DO NOT EDIT." header
	Imports   []Import
	Symbols   []*Symbol
}
