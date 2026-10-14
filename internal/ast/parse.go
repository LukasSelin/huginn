package ast

import (
	"fmt"
	goast "go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
)

// ParseSource extracts the top-level symbols from one Go source file.
//
// Commits in history do not always compile, so on a syntax error ParseSource
// returns the symbols it could recover together with the error. The File is
// nil only when nothing could be parsed.
func ParseSource(path string, src []byte) (*File, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments|parser.SkipObjectResolution)
	if f == nil {
		return nil, err
	}

	out := &File{Path: path, Package: f.Name.Name, Generated: goast.IsGenerated(f), Imports: fileImports(f)}
	imports := map[string]string{}
	for _, im := range out.Imports {
		if name := im.LocalName(); name != "_" && name != "." {
			imports[name] = im.Path
		}
	}
	seen := map[string]int{}
	add := func(s *Symbol, n goast.Node) {
		s.Path = path
		s.Package = out.Package
		s.Exported = goast.IsExported(s.Name)
		s.StartLine = fset.Position(n.Pos()).Line
		s.EndLine = fset.Position(n.End()).Line

		key := s.Name
		if s.Recv != "" {
			key = s.Recv + "." + s.Name
		}
		seen[key]++
		if n := seen[key]; n > 1 {
			key = fmt.Sprintf("%s#%d", key, n)
		}
		s.Key = key
		out.Symbols = append(out.Symbols, s)
	}

	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *goast.FuncDecl:
			s := funcSymbol(d)
			s.Refs, s.FieldRefs = funcRefs(fset, imports, d)
			add(s, d)
		case *goast.GenDecl:
			for _, spec := range d.Specs {
				// An ungrouped declaration spans its keyword; a grouped one only its spec.
				var node goast.Node = spec
				if !d.Lparen.IsValid() {
					node = d
				}
				switch sp := spec.(type) {
				case *goast.TypeSpec:
					s := typeSymbol(sp)
					s.Members = typeMembers(fset, sp)
					add(s, node)
				case *goast.ValueSpec:
					for _, s := range valueSymbols(d.Tok, sp) {
						add(s, node)
					}
				}
			}
		}
	}
	return out, err
}

func funcSymbol(d *goast.FuncDecl) *Symbol {
	s := &Symbol{Kind: KindFunc, Name: d.Name.Name}

	var head strings.Builder
	head.WriteString("func ")
	if d.Recv != nil && len(d.Recv.List) > 0 {
		s.Kind = KindMethod
		s.Recv, s.PtrRecv = recvBase(d.Recv.List[0].Type)
		head.WriteString("(" + types.ExprString(d.Recv.List[0].Type) + ") ")
	}
	head.WriteString(d.Name.Name)
	head.WriteString(fieldList(d.Type.TypeParams, "[", "]", true))
	head.WriteString(strings.TrimPrefix(types.ExprString(d.Type), "func"))
	s.Signature = head.String()

	s.sigKey = "(" + fieldList(d.Recv, "", "", false) + ")" +
		fieldList(d.Type.TypeParams, "[", "]", false) +
		fieldList(d.Type.Params, "(", ")", false) +
		fieldList(d.Type.Results, "(", ")", false)
	if s.Kind == KindMethod {
		s.methodKey = fieldList(d.Type.Params, "(", ")", false) + fieldList(d.Type.Results, "(", ")", false)
	}

	if d.Body != nil {
		h := newHasher()
		h.walk(d.Body)
		h.finish(s)
	}
	return s
}

func typeSymbol(sp *goast.TypeSpec) *Symbol {
	s := &Symbol{Kind: KindType, Name: sp.Name.Name}
	assign := " "
	if sp.Assign.IsValid() {
		assign = " = "
	}
	s.Signature = "type " + sp.Name.Name + fieldList(sp.TypeParams, "[", "]", true) + assign + types.ExprString(sp.Type)
	// The header is the type's name, parameters and alias-ness; the definition is its body.
	s.sigKey = fieldList(sp.TypeParams, "[", "]", false) + assign

	h := newHasher()
	h.walk(sp.Type)
	h.finish(s)
	return s
}

func valueSymbols(tok token.Token, sp *goast.ValueSpec) []*Symbol {
	kind := KindVar
	if tok == token.CONST {
		kind = KindConst
	}
	typ := ""
	if sp.Type != nil {
		typ = types.ExprString(sp.Type)
	}

	out := make([]*Symbol, 0, len(sp.Names))
	for i, name := range sp.Names {
		s := &Symbol{Kind: kind, Name: name.Name, sigKey: typ}
		s.Signature = tok.String() + " " + name.Name
		if typ != "" {
			s.Signature += " " + typ
		}
		h := newHasher()
		if i < len(sp.Values) {
			h.walk(sp.Values[i])
		}
		h.finish(s)
		out = append(out, s)
	}
	return out
}

// recvBase returns the receiver's base type name, stripping pointers, parens
// and type arguments: *Set[T] -> "Set", true.
func recvBase(e goast.Expr) (name string, ptr bool) {
	for {
		switch x := e.(type) {
		case *goast.StarExpr:
			ptr = true
			e = x.X
		case *goast.ParenExpr:
			e = x.X
		case *goast.IndexExpr:
			e = x.X
		case *goast.IndexListExpr:
			e = x.X
		case *goast.Ident:
			return x.Name, ptr
		default:
			return types.ExprString(e), ptr
		}
	}
}

// fieldList renders a parameter, result or type-parameter list. With names
// false only the types are kept, one per declared name, so that renaming a
// parameter does not count as a signature change.
func fieldList(fl *goast.FieldList, open, close string, names bool) string {
	if fl == nil || len(fl.List) == 0 {
		if open == "[" {
			return ""
		}
		return open + close
	}
	parts := make([]string, 0, len(fl.List))
	for _, f := range fl.List {
		typ := types.ExprString(f.Type)
		switch {
		case names && len(f.Names) > 0:
			ns := make([]string, len(f.Names))
			for i, n := range f.Names {
				ns[i] = n.Name
			}
			parts = append(parts, strings.Join(ns, ", ")+" "+typ)
		case len(f.Names) > 1:
			for range f.Names {
				parts = append(parts, typ)
			}
		default:
			parts = append(parts, typ)
		}
	}
	return open + strings.Join(parts, ", ") + close
}
