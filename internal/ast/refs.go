package ast

import (
	"cmp"
	goast "go/ast"
	"go/token"
	"slices"
	"strconv"
	"strings"
)

// RefKind says how a reference was resolved.
type RefKind uint8

const (
	// RefLocal is an unqualified identifier that is not declared inside the
	// function: a package-level symbol of the same package, or a predeclared
	// name such as len.
	RefLocal RefKind = iota + 1
	// RefImport is a pkg.Name selector whose pkg is one of the file's imports.
	RefImport
	// RefMethod is a call x.Name(...) on something other than an import.
	// Without type information only the method name is known.
	RefMethod
)

func (k RefKind) String() string {
	switch k {
	case RefLocal:
		return "local"
	case RefImport:
		return "import"
	case RefMethod:
		return "method"
	}
	return "unknown"
}

// Ref is one symbol that a function or method body refers to, with all its
// occurrences in that body folded together.
//
// Refs are syntactic. They are extracted without type checking so that they
// work on revisions that do not compile, and so they can name things that are
// not package-level symbols, such as builtins or struct fields of func type.
// Consumers resolve them against the symbols they know about.
type Ref struct {
	Kind RefKind
	Pkg  string // import path, for RefImport
	Name string // identifier, or method name for RefMethod

	// Recv is set for a RefMethod called on the enclosing method's receiver
	// variable, e.g. ix.walk() inside (*Indexer).Walk: it is the receiver's
	// base type, "Indexer". The method may still be promoted from an embedded
	// field.
	Recv string

	Count int // occurrences
	Calls int // occurrences as the callee of a call expression
	Line  int // first occurrence
}

// Approximate reports that the reference was resolved by method name only
// and may stand for any method of that name.
func (r Ref) Approximate() bool { return r.Kind == RefMethod && r.Recv == "" }

// FieldRef is one struct field that a function or method body uses by name,
// as a selector x.Name or as a key of a struct literal, with all its
// occurrences in that body folded together.
//
// Like Refs, FieldRefs are syntactic. The struct type is known for keyed
// literals that spell their type (T{Name: v}, pkg.T{Name: v}) and for
// selectors on the enclosing method's receiver; otherwise only the field
// name is, and a method value x.M can be mistaken for a field.
//
// An unkeyed literal of a named type, T{1, "x"}, is recorded with an empty
// Name: it uses every field of T by position, so adding or removing any
// field breaks it. T is not known to be a struct; it may be a named slice
// or array type.
type FieldRef struct {
	Pkg  string // import path, for a literal of an imported type
	Type string // the struct type's name when known: "Config"
	Name string // field name; empty for an unkeyed literal

	Count int // occurrences
	Line  int // first occurrence

	// Writes counts the occurrences that may change the field: as the
	// target of an assignment or of ++ or --, with its address taken, as the
	// channel of a send, as a range key or value assigned with =, or passed
	// to delete, clear or close, directly or through an index or a field of
	// it (x.items[i] = v writes items). Calls of its methods count as
	// reads; they may change it too.
	Writes int

	// Methods are the methods called on the field, x.f.M(), sorted; nil
	// when none. Whether they change it depends on its type, which is not
	// known here.
	Methods []string
}

// Approximate reports that the struct type is unknown and the reference may
// stand for any field of that name.
func (r FieldRef) Approximate() bool { return r.Type == "" }

// Unkeyed reports that the reference is an unkeyed literal of Type.
func (r FieldRef) Unkeyed() bool { return r.Name == "" }

// Import is one import declaration of a file.
type Import struct {
	Name string // explicit name ("_", ".", or an alias); empty when absent
	Path string
}

// LocalName is the identifier the import is referred to by in the file. For
// an import without an explicit name it is guessed from the path, since the
// package clause of the imported package is not available.
func (im Import) LocalName() string {
	if im.Name != "" {
		return im.Name
	}
	return ImportName(im.Path)
}

// ImportName guesses the package name of an import path by Go convention:
// the last path element, skipping a major version suffix ("/v2"), with a
// ".v3"-style suffix and a "go-" prefix dropped.
func ImportName(path string) string {
	elems := strings.Split(path, "/")
	name := elems[len(elems)-1]
	if len(elems) > 1 && isMajorVersion(name) {
		name = elems[len(elems)-2]
	}
	if i := strings.Index(name, ".v"); i > 0 && isMajorVersion(name[i+1:]) {
		name = name[:i]
	}
	name = strings.TrimPrefix(name, "go-")
	return strings.NewReplacer("-", "_", ".", "_").Replace(name)
}

func isMajorVersion(s string) bool {
	if len(s) < 2 || s[0] != 'v' {
		return false
	}
	_, err := strconv.Atoi(s[1:])
	return err == nil
}

func fileImports(f *goast.File) []Import {
	out := make([]Import, 0, len(f.Imports))
	for _, spec := range f.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		im := Import{Path: path}
		if spec.Name != nil {
			im.Name = spec.Name.Name
		}
		out = append(out, im)
	}
	return out
}

// funcRefs extracts the references made by a function or method declaration's
// body. imports maps the file's import names to paths.
//
// Scoping is flow-insensitive: a name declared anywhere in the function
// (parameter, result, :=, var, const, type, range variable, closure
// parameter) hides the package-level symbol of that name everywhere in it.
// That can drop a reference that is made before a shadowing declaration, but
// never invents one.
func funcRefs(fset *token.FileSet, imports map[string]string, d *goast.FuncDecl) ([]Ref, []FieldRef) {
	if d.Body == nil {
		return nil, nil
	}
	r := refCollector{
		fset:     fset,
		imports:  imports,
		declared: map[string]bool{},
		skip:     map[*goast.Ident]bool{},
		called:   map[goast.Expr]bool{},
		refs:     map[refKey]*Ref{},
		fields:   map[refKey]*FieldRef{},
		written:  map[*goast.SelectorExpr]bool{},
		methods:  map[*goast.SelectorExpr][]string{},
	}
	if d.Recv != nil && len(d.Recv.List) > 0 {
		r.recvType, _ = recvBase(d.Recv.List[0].Type)
		if names := d.Recv.List[0].Names; len(names) > 0 {
			r.recvName = names[0].Name
		}
		r.declareFields(d.Recv)
	}
	goast.Inspect(d.Type, r.declare)
	goast.Inspect(d.Body, r.declare)
	goast.Inspect(d.Body, r.visit)

	out := make([]Ref, 0, len(r.refs))
	for _, ref := range r.refs {
		out = append(out, *ref)
	}
	slices.SortFunc(out, func(a, b Ref) int {
		return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Pkg, b.Pkg), cmp.Compare(a.Recv, b.Recv), cmp.Compare(a.Name, b.Name))
	})

	var fields []FieldRef
	for _, f := range r.fields {
		slices.Sort(f.Methods)
		f.Methods = slices.Compact(f.Methods)
		fields = append(fields, *f)
	}
	slices.SortFunc(fields, func(a, b FieldRef) int {
		return cmp.Or(cmp.Compare(a.Pkg, b.Pkg), cmp.Compare(a.Type, b.Type), cmp.Compare(a.Name, b.Name))
	})
	return out, fields
}

type refKey struct {
	kind            RefKind
	pkg, recv, name string
}

type refCollector struct {
	fset     *token.FileSet
	imports  map[string]string
	recvName string // receiver variable, e.g. "ix"
	recvType string // receiver base type, e.g. "Indexer"

	declared map[string]bool       // names declared inside the function
	skip     map[*goast.Ident]bool // identifiers that are not references
	called   map[goast.Expr]bool   // callees of call expressions
	refs     map[refKey]*Ref
	fields   map[refKey]*FieldRef             // kind is unused; recv holds the struct type
	written  map[*goast.SelectorExpr]bool     // fields written, see write
	methods  map[*goast.SelectorExpr][]string // fields with methods called on them, x.f.M()
}

// write records that e is written, which writes the field selected at its
// root: x.f in x.f = v, x.f[i] = v, x.f.g = v or *x.f = v.
func (r *refCollector) write(e goast.Expr) {
	for {
		switch x := e.(type) {
		case *goast.ParenExpr:
			e = x.X
		case *goast.IndexExpr:
			e = x.X
		case *goast.IndexListExpr:
			e = x.X
		case *goast.StarExpr:
			e = x.X
		case *goast.SelectorExpr:
			if _, ok := x.X.(*goast.Ident); ok {
				r.written[x] = true
				return
			}
			e = x.X
		default:
			return
		}
	}
}

// declare records the names a node declares and the identifiers that name
// fields or labels rather than refer to symbols.
func (r *refCollector) declare(n goast.Node) bool {
	switch x := n.(type) {
	case *goast.FuncType:
		r.declareFields(x.TypeParams)
		r.declareFields(x.Params)
		r.declareFields(x.Results)
	case *goast.AssignStmt, *goast.IncDecStmt, *goast.UnaryExpr, *goast.SendStmt, *goast.RangeStmt:
		r.assign(x)
	case *goast.ValueSpec:
		for _, id := range x.Names {
			r.declared[id.Name] = true
		}
	case *goast.TypeSpec:
		r.declared[x.Name.Name] = true
	case *goast.StructType:
		r.skipFieldNames(x.Fields)
	case *goast.InterfaceType:
		r.skipFieldNames(x.Methods)
	case *goast.LabeledStmt:
		r.skip[x.Label] = true
	case *goast.BranchStmt:
		if x.Label != nil {
			r.skip[x.Label] = true
		}
	case *goast.CompositeLit:
		r.declareLiteral(x)
	case *goast.CallExpr:
		r.declareCall(x)
	}
	return true
}

// assign records what a statement that changes a value writes (an
// assignment, ++ or --, a send, a range clause or taking an address), and
// the names := declares.
func (r *refCollector) assign(n goast.Node) {
	switch x := n.(type) {
	case *goast.AssignStmt:
		if x.Tok == token.DEFINE {
			r.declareIdents(x.Lhs...)
		}
		for _, e := range x.Lhs {
			r.write(e)
		}
	case *goast.IncDecStmt:
		r.write(x.X)
	case *goast.UnaryExpr:
		if x.Op == token.AND {
			r.write(x.X)
		}
	case *goast.SendStmt:
		r.write(x.Chan)
	case *goast.RangeStmt:
		if x.Tok == token.ASSIGN {
			r.write(x.Key)
			r.write(x.Value)
		}
		if x.Tok == token.DEFINE {
			r.declareIdents(x.Key, x.Value)
		}
	}
}

// declareCall records the callee of a call, the field that delete, clear or
// close changes, and the method called on a field, x.f.M().
func (r *refCollector) declareCall(x *goast.CallExpr) {
	fn := callee(x.Fun)
	r.called[fn] = true
	switch fn := fn.(type) {
	case *goast.Ident:
		if (fn.Name == "delete" || fn.Name == "clear" || fn.Name == "close") && len(x.Args) > 0 {
			r.write(x.Args[0])
		}
	case *goast.SelectorExpr:
		if on, ok := unparen(fn.X).(*goast.SelectorExpr); ok {
			r.methods[on] = append(r.methods[on], fn.Sel.Name)
		}
	}
}

func unparen(e goast.Expr) goast.Expr {
	for {
		p, ok := e.(*goast.ParenExpr)
		if !ok {
			return e
		}
		e = p.X
	}
}

// declareIdents records each identifier among es as declared. Anything
// else, such as an absent range key, declares nothing.
func (r *refCollector) declareIdents(es ...goast.Expr) {
	for _, e := range es {
		if id, ok := e.(*goast.Ident); ok {
			r.declared[id.Name] = true
		}
	}
}

// declareLiteral records the fields a composite literal sets. Keys of struct
// literals are field names. Map keys are expressions, but the literal's type
// is only recognizable as a map when spelled out.
func (r *refCollector) declareLiteral(x *goast.CompositeLit) {
	if _, isMap := x.Type.(*goast.MapType); isMap {
		return
	}
	pkg, typ := r.literalType(x.Type)
	if typ != "" && len(x.Elts) > 0 {
		if _, keyed := x.Elts[0].(*goast.KeyValueExpr); !keyed {
			// T{1, "x"}: every field, by position. T may also be
			// a named slice or array type; it is not known here.
			r.addField(refKey{pkg: pkg, recv: typ}, x)
		}
	}
	for _, e := range x.Elts {
		kv, ok := e.(*goast.KeyValueExpr)
		if !ok {
			continue
		}
		if id, ok := kv.Key.(*goast.Ident); ok {
			r.skip[id] = true
			r.addField(refKey{pkg: pkg, recv: typ, name: id.Name}, id)
		}
	}
}

func (r *refCollector) declareFields(fl *goast.FieldList) {
	if fl == nil {
		return
	}
	for _, f := range fl.List {
		for _, id := range f.Names {
			r.declared[id.Name] = true
		}
	}
}

func (r *refCollector) skipFieldNames(fl *goast.FieldList) {
	if fl == nil {
		return
	}
	for _, f := range fl.List {
		for _, id := range f.Names {
			r.skip[id] = true
		}
	}
}

func (r *refCollector) visit(n goast.Node) bool {
	switch x := n.(type) {
	case *goast.SelectorExpr:
		r.visitSelector(x)
	case *goast.Ident:
		r.visitIdent(x)
	}
	return true
}

// visitSelector records x.Sel as a reference into an import, a method call
// or a field use, and marks the identifiers that are not references.
func (r *refCollector) visitSelector(x *goast.SelectorExpr) {
	r.skip[x.Sel] = true
	if id, ok := x.X.(*goast.Ident); ok && !r.declared[id.Name] {
		if path, ok := r.imports[id.Name]; ok {
			r.skip[id] = true
			r.add(refKey{kind: RefImport, pkg: path, name: x.Sel.Name}, x)
			return
		}
	}
	if r.called[x] {
		r.add(refKey{kind: RefMethod, recv: r.recvTypeOf(x.X), name: x.Sel.Name}, x)
	} else {
		r.addField(refKey{recv: r.recvTypeOf(x.X), name: x.Sel.Name}, x)
	}
}

// recvTypeOf returns the receiver's base type when e is the enclosing
// method's receiver variable, and "" otherwise.
func (r *refCollector) recvTypeOf(e goast.Expr) string {
	if id, ok := e.(*goast.Ident); ok && id.Name == r.recvName && r.recvName != "" && r.recvName != "_" {
		return r.recvType
	}
	return ""
}

// visitIdent records an unqualified identifier as a local reference unless
// it is declared in the function, blank, an import name or not a reference.
func (r *refCollector) visitIdent(x *goast.Ident) {
	if r.skip[x] || r.declared[x.Name] || x.Name == "_" {
		return
	}
	if _, isImport := r.imports[x.Name]; isImport {
		return
	}
	r.add(refKey{kind: RefLocal, name: x.Name}, x)
}

func (r *refCollector) add(k refKey, n goast.Expr) {
	ref := r.refs[k]
	if ref == nil {
		ref = &Ref{Kind: k.kind, Pkg: k.pkg, Recv: k.recv, Name: k.name, Line: r.fset.Position(n.Pos()).Line}
		r.refs[k] = ref
	}
	ref.Count++
	if r.called[n] {
		ref.Calls++
	}
}

func (r *refCollector) addField(k refKey, n goast.Node) {
	f := r.fields[k]
	if f == nil {
		f = &FieldRef{Pkg: k.pkg, Type: k.recv, Name: k.name, Line: r.fset.Position(n.Pos()).Line}
		r.fields[k] = f
	}
	f.Count++
	if sel, ok := n.(*goast.SelectorExpr); ok {
		if r.written[sel] {
			f.Writes++
		}
		f.Methods = append(f.Methods, r.methods[sel]...)
	}
}

// literalType resolves the spelled-out type of a struct literal to a type
// name, and an import path for an imported type. Both are empty when the
// type is elided, local to the function or not a named type.
func (r *refCollector) literalType(e goast.Expr) (pkg, name string) {
	switch x := callee(e).(type) {
	case *goast.Ident:
		if !r.declared[x.Name] {
			return "", x.Name
		}
	case *goast.SelectorExpr:
		if id, ok := x.X.(*goast.Ident); ok && !r.declared[id.Name] {
			if path, ok := r.imports[id.Name]; ok {
				return path, x.Sel.Name
			}
		}
	}
	return "", ""
}

// callee strips parentheses and type arguments from a call's function
// expression: (pkg.F[int]) -> pkg.F.
func callee(e goast.Expr) goast.Expr {
	for {
		switch x := e.(type) {
		case *goast.ParenExpr:
			e = x.X
		case *goast.IndexExpr:
			e = x.X
		case *goast.IndexListExpr:
			e = x.X
		default:
			return e
		}
	}
}
