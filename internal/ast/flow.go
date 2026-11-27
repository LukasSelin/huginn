package ast

import (
	"fmt"
	goast "go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"unicode/utf8"
)

// StepKind says what a step of a function's flow does.
type StepKind uint8

const (
	// StepDecision is an if, else if, else, switch, case or default, or a
	// select and its cases.
	StepDecision StepKind = iota + 1

	// StepLoop is a for or for range.
	StepLoop

	// StepExit leaves the function: a return, a panic, os.Exit or
	// log.Fatal.
	StepExit

	// StepJump is a break, continue, goto or fallthrough.
	StepJump

	// StepEffect crosses a boundary in straight-line code: a call into an
	// I/O package, a write to a field of the receiver, go or defer with a
	// call, a channel send, an error taken from a call, or a result dropped
	// with _.
	StepEffect

	// StepClosure is a function literal, with its own flow nested under it.
	// A go or defer of a function literal is a closure too.
	StepClosure
)

func (k StepKind) String() string {
	switch k {
	case StepDecision:
		return "decision"
	case StepLoop:
		return "loop"
	case StepExit:
		return "exit"
	case StepJump:
		return "jump"
	case StepEffect:
		return "effect"
	case StepClosure:
		return "closure"
	}
	return "unknown"
}

// MarshalText encodes a kind by its name.
func (k StepKind) MarshalText() ([]byte, error) { return []byte(k.String()), nil }

// Step is one step of a function's flow: a decision, loop, way out, jump,
// boundary crossing or closure, with the straight-line code between steps
// left out.
type Step struct {
	Kind  StepKind
	Depth int // how many steps enclose it; 0 at the top of the body
	Line  int

	// Text is the step as written, shortened: "if err != nil",
	// "for _, f := range files", "return nil, err", "writes lr.pending".
	Text string

	// Cond is the condition a decision tests, as written: "err != nil".
	// For a case of a switch on a tag it is the comparison the case makes,
	// "t.kind == a || t.kind == b", and for a case of a type switch,
	// "x is *ast.Ident". It is empty for else, default and other kinds.
	Cond string

	// Then is the way out or jump a decision ends in at once, which the
	// decision's step holds instead of a step of its own: "return err" for
	// if err != nil { return err }.
	Then string

	// Key is what two revisions of the function are compared by: the step
	// unshortened, with the receiver written ʳ whatever its name, and a case
	// of a switch on a tag written as the comparison it makes, so that
	// switch t.kind { case a: } and switch { case t.kind == a: } agree.
	Key string
}

// String writes the step as the reports show it: "if err != nil → return err".
func (s Step) String() string {
	if s.Then != "" {
		return s.Text + " → " + s.Then
	}
	return s.Text
}

// The receiver is written as recvMark while a function is walked, then as
// its name in Text and as recvKey in Key.
const (
	recvMark = ""
	recvKey  = "ʳ"
)

// maxText is how many characters of a step's Text are kept.
const maxText = 160

// ioPackages are the standard library packages whose calls cross the
// process boundary: files, programs, the network, databases and the
// system.
var ioPackages = map[string]bool{
	"os": true, "os/exec": true, "io/fs": true, "io/ioutil": true,
	"net": true, "net/http": true, "database/sql": true, "syscall": true,
}

// Flows returns the flow of each function and method declared in src, by
// the Key the symbol has in ParseSource's File. A file with syntax errors
// gives the flows of the functions that could be parsed.
func Flows(path string, src []byte) map[string][]Step {
	fset := token.NewFileSet()
	f, _ := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if f == nil {
		return nil
	}
	imports := importNames(f)
	out := map[string][]Step{}
	seen := map[string]int{}
	for _, decl := range f.Decls {
		d, ok := decl.(*goast.FuncDecl)
		if !ok {
			continue
		}
		key, recv := funcKey(d, seen)
		if d.Body == nil {
			continue
		}
		if recv != "" {
			markReceiver(d.Body, recv)
		}
		w := &flowWalker{fset: fset, imports: imports, recv: recv}
		w.block(0, d.Body.List)
		out[key] = w.out
	}
	return out
}

// importNames maps the local names of a file's imports to their paths,
// leaving out blank and dot imports.
func importNames(f *goast.File) map[string]string {
	out := map[string]string{}
	for _, im := range fileImports(f) {
		if name := im.LocalName(); name != "_" && name != "." {
			out[name] = im.Path
		}
	}
	return out
}

// funcKey returns the key ParseSource gives a function, counting in seen
// the keys given so far, and the name of its receiver, if it has one. Only
// functions can share a key with a function, so counting functions alone
// agrees with ParseSource.
func funcKey(d *goast.FuncDecl, seen map[string]int) (key, recv string) {
	key = d.Name.Name
	if d.Recv != nil && len(d.Recv.List) > 0 {
		base, _ := recvBase(d.Recv.List[0].Type)
		key = base + "." + key
		if names := d.Recv.List[0].Names; len(names) > 0 && names[0].Name != "_" {
			recv = names[0].Name
		}
	}
	seen[key]++
	if n := seen[key]; n > 1 {
		key = fmt.Sprintf("%s#%d", key, n)
	}
	return key, recv
}

// markReceiver writes recvMark for each use of the receiver recv in body.
func markReceiver(body *goast.BlockStmt, recv string) {
	goast.Inspect(body, func(n goast.Node) bool {
		if id, ok := n.(*goast.Ident); ok && id.Name == recv {
			id.Name = recvMark
		}
		return true
	})
}

// flowWalker collects the steps of one function body.
type flowWalker struct {
	fset    *token.FileSet
	imports map[string]string // local name to import path
	recv    string            // the receiver's name, or empty
	out     []Step
}

// str writes an expression or simple statement for a step's text and key.
func (w *flowWalker) str(n goast.Node) (text, key string) {
	s := short(nodeString(n))
	return strings.ReplaceAll(s, recvMark, w.recv), strings.ReplaceAll(s, recvMark, recvKey)
}

// emit adds a step whose key is its text with the receiver normalized.
func (w *flowWalker) emit(n goast.Node, depth int, kind StepKind, text, key, cond, then, thenKey string) {
	if then != "" {
		key += " → " + thenKey
	}
	w.out = append(w.out, Step{
		Kind: kind, Depth: depth, Line: w.fset.Position(n.Pos()).Line,
		Text: clip(text), Cond: clip(cond), Then: clip(then), Key: key,
	})
}

func (w *flowWalker) block(depth int, list []goast.Stmt) {
	for _, s := range list {
		w.stmt(depth, s)
	}
}

// terminal reports whether s leaves the function or jumps, so that a
// decision whose body is only s can hold it as its Then.
func (w *flowWalker) terminal(s goast.Stmt) (StepKind, bool) {
	switch s := s.(type) {
	case *goast.ReturnStmt:
		return StepExit, !hasFuncLit(s)
	case *goast.BranchStmt:
		return StepJump, true
	case *goast.ExprStmt:
		if w.exits(s.X) {
			return StepExit, true
		}
	}
	return 0, false
}

// exits reports whether e is a call that ends the function: panic, os.Exit
// or one of log's Fatal and Panic functions.
func (w *flowWalker) exits(e goast.Expr) bool {
	c, ok := goast.Unparen(e).(*goast.CallExpr)
	if !ok {
		return false
	}
	switch fn := goast.Unparen(c.Fun).(type) {
	case *goast.Ident:
		return fn.Name == "panic"
	case *goast.SelectorExpr:
		pkg, ok := fn.X.(*goast.Ident)
		if !ok {
			return false
		}
		switch w.imports[pkg.Name] {
		case "os":
			return fn.Sel.Name == "Exit"
		case "log":
			return strings.HasPrefix(fn.Sel.Name, "Fatal") || strings.HasPrefix(fn.Sel.Name, "Panic")
		}
	}
	return false
}

// decision adds a decision step, holding its body as Then when the body is
// a single way out or jump, and walking the body under it otherwise.
func (w *flowWalker) decision(n goast.Node, depth int, text, key, cond string, body []goast.Stmt) {
	if len(body) == 1 {
		if _, ok := w.terminal(body[0]); ok {
			then, thenKey := w.str(body[0])
			w.emit(n, depth, StepDecision, text, key, cond, then, thenKey)
			return
		}
	}
	w.emit(n, depth, StepDecision, text, key, cond, "", "")
	w.block(depth+1, body)
}

func (w *flowWalker) ifStmt(depth int, s *goast.IfStmt, word string) {
	cond, condKey := w.str(s.Cond)
	if s.Init != nil {
		init, initKey := w.str(s.Init)
		cond, condKey = init+"; "+cond, initKey+"; "+condKey
	}
	w.closuresIn(depth, s.Init)
	w.decision(s, depth, word+cond, word+condKey, cond, s.Body.List)
	switch e := s.Else.(type) {
	case *goast.IfStmt:
		w.ifStmt(depth, e, "else if ")
	case *goast.BlockStmt:
		w.decision(e, depth, "else", "else", "", e.List)
	}
}

func (w *flowWalker) stmt(depth int, s goast.Stmt) {
	switch s := s.(type) {
	case *goast.IfStmt:
		w.ifStmt(depth, s, "if ")

	case *goast.ForStmt:
		w.forStmt(depth, s)

	case *goast.RangeStmt:
		w.rangeStmt(depth, s)

	case *goast.SwitchStmt:
		w.switchStmt(depth, s)

	case *goast.TypeSwitchStmt:
		w.typeSwitchStmt(depth, s)

	case *goast.SelectStmt:
		w.selectStmt(depth, s)

	case *goast.ReturnStmt:
		text, key := w.str(s)
		w.emit(s, depth, StepExit, text, key, "", "", "")
		w.closuresIn(depth+1, s)

	case *goast.BranchStmt:
		text, key := w.str(s)
		w.emit(s, depth, StepJump, text, key, "", "", "")

	case *goast.GoStmt:
		w.deferred(depth, s, "go", s.Call)

	case *goast.DeferStmt:
		w.deferred(depth, s, "defer", s.Call)

	case *goast.SendStmt:
		ch, chKey := w.str(s.Chan)
		w.emit(s, depth, StepEffect, "send on "+ch, "send on "+chKey, "", "", "")
		w.closuresIn(depth, s)

	case *goast.BlockStmt:
		w.block(depth, s.List)

	case *goast.LabeledStmt:
		w.stmt(depth, s.Stmt)

	case *goast.ExprStmt:
		w.exprStmt(depth, s)

	default:
		w.effects(depth, s)
	}
}

// exprStmt adds an expression statement: a way out when it calls panic,
// os.Exit, or log's Fatal or Panic functions, and its effects otherwise.
func (w *flowWalker) exprStmt(depth int, s *goast.ExprStmt) {
	if w.exits(s.X) {
		text, key := w.str(s)
		w.emit(s, depth, StepExit, text, key, "", "", "")
		return
	}
	w.effects(depth, s)
}

// forStmt adds a for loop, written as its header, and walks its body.
func (w *flowWalker) forStmt(depth int, s *goast.ForStmt) {
	var parts, keys []string
	for _, n := range []goast.Node{s.Init, s.Cond, s.Post} {
		t, k := "", ""
		if n != nil {
			t, k = w.str(n)
		}
		parts, keys = append(parts, t), append(keys, k)
	}
	text, key := "for", "for"
	switch {
	case parts[0] != "" || parts[2] != "":
		text, key = "for "+strings.Join(parts, "; "), "for "+strings.Join(keys, "; ")
	case parts[1] != "":
		text, key = "for "+parts[1], "for "+keys[1]
	}
	w.emit(s, depth, StepLoop, text, key, "", "", "")
	w.block(depth+1, s.Body.List)
}

// rangeStmt adds a for range loop and walks its body.
func (w *flowWalker) rangeStmt(depth int, s *goast.RangeStmt) {
	x, xk := w.str(s.X)
	text, key := "for range "+x, "for range "+xk
	if s.Key != nil {
		vars, varsKey := w.str(s.Key)
		if s.Value != nil {
			v, vk := w.str(s.Value)
			vars, varsKey = vars+", "+v, varsKey+", "+vk
		}
		text = "for " + vars + " " + s.Tok.String() + " range " + x
		key = "for " + varsKey + " " + s.Tok.String() + " range " + xk
	}
	w.emit(s, depth, StepLoop, text, key, "", "", "")
	w.block(depth+1, s.Body.List)
}

// switchStmt adds a switch and its cases. A case of a switch on a tag is
// keyed, and conditioned, as the comparison it makes, so that it agrees
// with the same switch written without a tag.
func (w *flowWalker) switchStmt(depth int, s *goast.SwitchStmt) {
	w.closuresIn(depth, s.Init)
	text, key := "switch", "switch"
	var tag, tagKey string
	if s.Init != nil {
		init, initKey := w.str(s.Init)
		text, key = text+" "+init+";", key+" "+initKey+";"
	}
	if s.Tag != nil {
		tag, tagKey = w.str(s.Tag)
		text += " " + tag // the key leaves the tag to the cases
	}
	w.emit(s, depth, StepDecision, text, key, "", "", "")
	for _, cc := range caseClauses(s.Body) {
		if cc.List == nil {
			w.decision(cc, depth+1, "default", "default", "", cc.Body)
			continue
		}
		var shown, conds, condKeys []string
		for _, e := range cc.List {
			t, k := w.str(e)
			shown = append(shown, t)
			if s.Tag != nil {
				t, k = tag+" == "+t, tagKey+" == "+k
			}
			conds, condKeys = append(conds, t), append(condKeys, k)
		}
		cond, condKey := strings.Join(conds, " || "), strings.Join(condKeys, " || ")
		w.decision(cc, depth+1, "case "+strings.Join(shown, ", "), "case "+condKey, cond, cc.Body)
	}
}

// typeSwitchStmt adds a type switch and its cases, each conditioned as
// "x is T1 or T2".
func (w *flowWalker) typeSwitchStmt(depth int, s *goast.TypeSwitchStmt) {
	w.closuresIn(depth, s.Init)
	assign, assignKey := w.str(s.Assign)
	x := assign
	if subject := typeSwitchSubject(s.Assign); subject != nil {
		x, _ = w.str(subject)
	}
	w.emit(s, depth, StepDecision, "switch "+assign, "switch "+assignKey, "", "", "")
	for _, cc := range caseClauses(s.Body) {
		if cc.List == nil {
			w.decision(cc, depth+1, "default", "default", "", cc.Body)
			continue
		}
		var ts, tks []string
		for _, e := range cc.List {
			t, k := w.str(e)
			ts, tks = append(ts, t), append(tks, k)
		}
		list, listKey := strings.Join(ts, ", "), strings.Join(tks, ", ")
		cond := x + " is " + strings.Join(ts, " or ")
		w.decision(cc, depth+1, "case "+list, "case "+listKey, cond, cc.Body)
	}
}

// typeSwitchSubject returns the x of a type switch's x.(type), or nil.
func typeSwitchSubject(assign goast.Stmt) goast.Expr {
	var e goast.Expr
	switch a := assign.(type) {
	case *goast.AssignStmt:
		if len(a.Rhs) == 1 {
			e = a.Rhs[0]
		}
	case *goast.ExprStmt:
		e = a.X
	}
	if ta, ok := e.(*goast.TypeAssertExpr); ok {
		return ta.X
	}
	return nil
}

// caseClauses returns the case clauses of a switch's body.
func caseClauses(body *goast.BlockStmt) []*goast.CaseClause {
	var out []*goast.CaseClause
	for _, c := range body.List {
		if cc, ok := c.(*goast.CaseClause); ok {
			out = append(out, cc)
		}
	}
	return out
}

// selectStmt adds a select and its cases.
func (w *flowWalker) selectStmt(depth int, s *goast.SelectStmt) {
	w.emit(s, depth, StepDecision, "select", "select", "", "", "")
	for _, c := range s.Body.List {
		cc, ok := c.(*goast.CommClause)
		if !ok {
			continue
		}
		if cc.Comm == nil {
			w.decision(cc, depth+1, "default", "default", "", cc.Body)
			continue
		}
		comm, commKey := w.str(cc.Comm)
		w.decision(cc, depth+1, "case "+comm, "case "+commKey, comm, cc.Body)
	}
}

// deferred adds a go or defer statement: an effect naming the call, or for
// a function literal, a closure with the literal's flow under it.
func (w *flowWalker) deferred(depth int, s goast.Stmt, word string, call *goast.CallExpr) {
	if lit, ok := goast.Unparen(call.Fun).(*goast.FuncLit); ok {
		w.emit(s, depth, StepClosure, word+" func literal", word+" func literal", "", "", "")
		w.block(depth+1, lit.Body.List)
		return
	}
	fn, fnKey := w.str(call.Fun)
	w.emit(s, depth, StepEffect, word+" "+fn+"(…)", word+" "+fnKey+"(…)", "", "", "")
	w.closuresIn(depth, call)
}

// effects adds the boundary crossings of a straight-line statement: writes
// to the receiver's fields, errors taken from calls, results dropped, and
// calls into I/O packages; then the closures it holds.
func (w *flowWalker) effects(depth int, s goast.Stmt) {
	switch s := s.(type) {
	case *goast.IncDecStmt:
		w.write(depth, s, s.X)
	case *goast.AssignStmt:
		w.assignment(depth, s)
	}
	w.ioCalls(depth, s)
	w.closuresIn(depth, s)
}

// write adds a write to the receiver when target belongs to it.
func (w *flowWalker) write(depth int, s goast.Stmt, target goast.Expr) {
	if w.recv != "" && rootIdent(target) == recvMark {
		t, k := w.str(target)
		w.emit(s, depth, StepEffect, "writes "+t, "writes "+k, "", "", "")
	}
}

// assignment adds the effects of an assignment: writes to the receiver, an
// error taken from a call, and a call's last result dropped with _.
func (w *flowWalker) assignment(depth int, a *goast.AssignStmt) {
	var call *goast.CallExpr
	if len(a.Rhs) == 1 {
		call, _ = goast.Unparen(a.Rhs[0]).(*goast.CallExpr)
	}
	for i, l := range a.Lhs {
		w.write(depth, a, l)
		id, ok := l.(*goast.Ident)
		if !ok || call == nil {
			continue
		}
		fn, fnKey := w.str(call.Fun)
		switch {
		case id.Name == "err":
			w.emit(a, depth, StepEffect, "err ← "+fn+"(…)", "err ← "+fnKey+"(…)", "", "", "")
		case id.Name == "_" && i == len(a.Lhs)-1:
			w.emit(a, depth, StepEffect, "drops a result of "+fn+"(…)", "drops a result of "+fnKey+"(…)", "", "", "")
		}
	}
}

// ioCalls adds the calls into I/O packages in s, outside function literals.
func (w *flowWalker) ioCalls(depth int, s goast.Stmt) {
	goast.Inspect(s, func(n goast.Node) bool {
		switch n := n.(type) {
		case *goast.FuncLit:
			return false
		case *goast.CallExpr:
			sel, ok := goast.Unparen(n.Fun).(*goast.SelectorExpr)
			if !ok {
				return true
			}
			if pkg, ok := sel.X.(*goast.Ident); ok && ioPackages[w.imports[pkg.Name]] {
				fn, fnKey := w.str(sel)
				w.emit(n, depth, StepEffect, "I/O "+fn+"(…)", "I/O "+fnKey+"(…)", "", "", "")
			}
		}
		return true
	})
}

// closureName is a function literal's name by where it goes: its text,
// and its key with the receiver written ʳ.
type closureName struct{ text, key string }

// closuresIn adds the function literals directly in n, outside other
// literals, as closures named by where they go, each with its flow.
func (w *flowWalker) closuresIn(depth int, n goast.Node) {
	if n == nil {
		return
	}
	names := w.closureNames(n)
	goast.Inspect(n, func(x goast.Node) bool {
		lit, ok := x.(*goast.FuncLit)
		if !ok {
			return true
		}
		nm, ok := names[lit]
		if !ok {
			nm = closureName{"func literal", "func literal"}
		}
		w.out = append(w.out, Step{Kind: StepClosure, Depth: depth, Line: w.fset.Position(lit.Pos()).Line, Text: clip(nm.text), Key: nm.key})
		w.block(depth+1, lit.Body.List)
		return false
	})
}

// closureNames names the function literals directly in n that are assigned
// ("func literal → name") or passed to a call ("func literal in f").
func (w *flowWalker) closureNames(n goast.Node) map[*goast.FuncLit]closureName {
	names := map[*goast.FuncLit]closureName{}
	name := func(e goast.Expr, prefix string, to goast.Node) {
		if lit, ok := goast.Unparen(e).(*goast.FuncLit); ok {
			t, k := w.str(to)
			names[lit] = closureName{prefix + t, prefix + k}
		}
	}
	goast.Inspect(n, func(x goast.Node) bool {
		switch x := x.(type) {
		case *goast.AssignStmt:
			for i, r := range x.Rhs {
				if len(x.Lhs) == len(x.Rhs) {
					name(r, "func literal → ", x.Lhs[i])
				}
			}
		case *goast.CallExpr:
			for _, a := range x.Args {
				name(a, "func literal in ", x.Fun)
			}
		case *goast.FuncLit:
			return false
		}
		return true
	})
	return names
}

// hasFuncLit reports whether n holds a function literal.
func hasFuncLit(n goast.Node) bool {
	found := false
	goast.Inspect(n, func(x goast.Node) bool {
		if _, ok := x.(*goast.FuncLit); ok {
			found = true
		}
		return !found
	})
	return found
}

// rootIdent returns the name of the variable an assignment target belongs
// to: "s" for s.a.b[i], or "" when there is none.
func rootIdent(e goast.Expr) string {
	for {
		switch x := e.(type) {
		case *goast.Ident:
			return x.Name
		case *goast.SelectorExpr:
			e = x.X
		case *goast.IndexExpr:
			e = x.X
		case *goast.StarExpr:
			e = x.X
		case *goast.ParenExpr:
			e = x.X
		default:
			return ""
		}
	}
}

// nodeString writes an expression or a simple statement on one line, with
// function literals as "func literal" and composite literals' elements
// left out.
func nodeString(n goast.Node) string {
	switch n := n.(type) {
	case *goast.TypeAssertExpr:
		if n.Type == nil {
			return nodeString(n.X) + ".(type)"
		}
		return funcLits(types.ExprString(n))
	case goast.Expr:
		return funcLits(types.ExprString(n))
	case *goast.ExprStmt:
		return nodeString(n.X)
	case *goast.AssignStmt:
		return exprList(n.Lhs) + " " + n.Tok.String() + " " + exprList(n.Rhs)
	case *goast.IncDecStmt:
		return nodeString(n.X) + n.Tok.String()
	case *goast.SendStmt:
		return nodeString(n.Chan) + " <- " + nodeString(n.Value)
	case *goast.ReturnStmt:
		if len(n.Results) == 0 {
			return "return"
		}
		return "return " + exprList(n.Results)
	case *goast.BranchStmt:
		if n.Label != nil {
			return n.Tok.String() + " " + n.Label.Name
		}
		return n.Tok.String()
	case *goast.DeclStmt:
		return "var …"
	}
	return "…"
}

func exprList(es []goast.Expr) string {
	parts := make([]string, len(es))
	for i, e := range es {
		parts[i] = nodeString(e)
	}
	return strings.Join(parts, ", ")
}

// funcLits rewrites the "(func(…) … literal)" types.ExprString writes for a
// function literal as "func literal".
func funcLits(s string) string {
	const open, tail = "(func", " literal)"
	for {
		i := strings.Index(s, open)
		if i < 0 {
			return s
		}
		depth, end := 0, -1
		for j := i; j < len(s); j++ {
			switch s[j] {
			case '(':
				depth++
			case ')':
				depth--
			}
			if depth == 0 {
				end = j
				break
			}
		}
		if end < 0 || !strings.HasSuffix(s[:end+1], tail) {
			return s
		}
		s = s[:i] + "func literal" + s[end+1:]
	}
}

// short collapses white space.
func short(s string) string { return strings.Join(strings.Fields(s), " ") }

// clip shortens s to maxText characters.
func clip(s string) string {
	if utf8.RuneCountInString(s) <= maxText {
		return s
	}
	r := []rune(s)
	return string(r[:maxText-1]) + "…"
}
