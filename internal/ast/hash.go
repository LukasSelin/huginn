package ast

import (
	goast "go/ast"
	"go/token"
	"hash/fnv"
	"reflect"
)

// shingleSize is the length of the token n-grams used for fuzzy matching.
const shingleSize = 4

// hasher walks a syntax tree and reduces it to a token stream that depends
// only on structure, identifiers, literals and operators, never on positions,
// whitespace or comments. A gofmt-only commit therefore hashes identically.
type hasher struct {
	toks       []uint64
	stmts      int
	complexity int
}

func newHasher() *hasher { return &hasher{complexity: 1} }

func (h *hasher) emit(s string) {
	f := fnv.New64a()
	f.Write([]byte(s))
	h.toks = append(h.toks, f.Sum64())
}

func (h *hasher) walk(root goast.Node) {
	goast.Inspect(root, func(n goast.Node) bool {
		if n == nil {
			h.emit(")")
			return false
		}
		switch n.(type) {
		case *goast.CommentGroup, *goast.Comment:
			return false
		}

		h.emit(reflect.TypeOf(n).Elem().Name())
		if _, ok := n.(goast.Stmt); ok {
			if _, block := n.(*goast.BlockStmt); !block {
				h.stmts++
			}
		}

		switch x := n.(type) {
		case *goast.Ident:
			h.emit(x.Name)
		case *goast.BasicLit:
			h.emit(x.Value)
		case *goast.BinaryExpr:
			h.emit(x.Op.String())
			if x.Op == token.LAND || x.Op == token.LOR {
				h.complexity++
			}
		case *goast.UnaryExpr:
			h.emit(x.Op.String())
		case *goast.AssignStmt:
			h.emit(x.Tok.String())
		case *goast.IncDecStmt:
			h.emit(x.Tok.String())
		case *goast.BranchStmt:
			h.emit(x.Tok.String())
		case *goast.GenDecl:
			h.emit(x.Tok.String())
		case *goast.ChanType:
			h.emit(string(rune('0' + x.Dir)))
		case *goast.RangeStmt:
			h.emit(x.Tok.String())
			h.complexity++
		case *goast.IfStmt, *goast.ForStmt:
			h.complexity++
		case *goast.CaseClause:
			if x.List != nil {
				h.complexity++
			}
		case *goast.CommClause:
			if x.Comm != nil {
				h.complexity++
			}
		}
		return true
	})
}

// finish stores the hash, shingles and metrics on s.
func (h *hasher) finish(s *Symbol) {
	f := fnv.New64a()
	var b [8]byte
	for _, t := range h.toks {
		for i := range b {
			b[i] = byte(t >> (8 * i))
		}
		f.Write(b[:])
	}
	s.BodyHash = f.Sum64()
	s.Stmts = h.stmts
	if s.Kind == KindFunc || s.Kind == KindMethod {
		s.Complexity = h.complexity
	}

	s.shingles = make(map[uint64]struct{}, len(h.toks))
	if len(h.toks) < shingleSize {
		s.shingles[s.BodyHash] = struct{}{}
		return
	}
	for i := 0; i+shingleSize <= len(h.toks); i++ {
		var x uint64 = 14695981039346656037
		for _, t := range h.toks[i : i+shingleSize] {
			x = (x ^ t) * 1099511628211
		}
		s.shingles[x] = struct{}{}
	}
}

// similarity is the Jaccard index of two symbols' body shingles, in [0, 1].
func similarity(a, b *Symbol) float64 {
	if len(a.shingles) == 0 || len(b.shingles) == 0 {
		return 0
	}
	small, large := a.shingles, b.shingles
	if len(small) > len(large) {
		small, large = large, small
	}
	inter := 0
	for k := range small {
		if _, ok := large[k]; ok {
			inter++
		}
	}
	return float64(inter) / float64(len(a.shingles)+len(b.shingles)-inter)
}
