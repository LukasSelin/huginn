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
func funcRefs(fset *token.FileSet, imports map[string]string, d *goast.FuncDecl) []Ref {
	if d.Body == nil {
		return nil
	}
	r := refCollector{
		fset:     fset,
		imports:  imports,
		declared: map[string]bool{},
		skip:     map[*goast.Ident]bool{},
		called:   map[goast.Expr]bool{},
		refs:     map[refKey]*Ref{},
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
	return out
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
}

// declare records the names a node declares and the identifiers that name
// fields or labels rather than refer to symbols.
func (r *refCollector) declare(n goast.Node) bool {
	switch x := n.(type) {
	case *goast.FuncType:
		r.declareFields(x.TypeParams)
		r.declareFields(x.Params)
		r.declareFields(x.Results)
	case *goast.AssignStmt:
		if x.Tok == token.DEFINE {
			for _, e := range x.Lhs {
				if id, ok := e.(*goast.Ident); ok {
					r.declared[id.Name] = true
				}
			}
		}
	case *goast.RangeStmt:
		if x.Tok == token.DEFINE {
			for _, e := range []goast.Expr{x.Key, x.Value} {
				if id, ok := e.(*goast.Ident); ok {
					r.declared[id.Name] = true
				}
			}
		}
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
		// Keys of struct literals are field names. Map keys are expressions,
		// but the literal's type is only recognizable as a map when spelled out.
		if _, isMap := x.Type.(*goast.MapType); !isMap {
			for _, e := range x.Elts {
				if kv, ok := e.(*goast.KeyValueExpr); ok {
					if id, ok := kv.Key.(*goast.Ident); ok {
						r.skip[id] = true
					}
				}
			}
		}
	case *goast.CallExpr:
		r.called[callee(x.Fun)] = true
	}
	return true
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
		r.skip[x.Sel] = true
		if id, ok := x.X.(*goast.Ident); ok && !r.declared[id.Name] {
			if path, ok := r.imports[id.Name]; ok {
				r.skip[id] = true
				r.add(refKey{kind: RefImport, pkg: path, name: x.Sel.Name}, x)
				return true
			}
		}
		if r.called[x] {
			key := refKey{kind: RefMethod, name: x.Sel.Name}
			if id, ok := x.X.(*goast.Ident); ok && id.Name == r.recvName && r.recvName != "" && r.recvName != "_" {
				key.recv = r.recvType
			}
			r.add(key, x)
		}
	case *goast.Ident:
		if r.skip[x] || r.declared[x.Name] || x.Name == "_" {
			return true
		}
		if _, isImport := r.imports[x.Name]; isImport {
			return true
		}
		r.add(refKey{kind: RefLocal, name: x.Name}, x)
	}
	return true
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
