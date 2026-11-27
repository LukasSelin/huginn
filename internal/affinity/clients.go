package affinity

import (
	"cmp"
	"fmt"
	goast "go/ast"
	"go/parser"
	"go/types"
	"path"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"huginn/internal/ast"
)

// stdMethods are method names that satisfy well-known interfaces, which
// are called through the interface and so show no callers.
var stdMethods = map[string]string{
	"String": "fmt.Stringer", "GoString": "fmt.GoStringer", "Format": "fmt.Formatter", "Error": "error",
	"Unwrap": "errors", "Is": "errors", "As": "errors", "ServeHTTP": "http.Handler",
	"MarshalJSON": "json.Marshaler", "UnmarshalJSON": "json.Unmarshaler",
	"MarshalText": "encoding.TextMarshaler", "UnmarshalText": "encoding.TextUnmarshaler",
	"MarshalBinary": "encoding.BinaryMarshaler", "UnmarshalBinary": "encoding.BinaryUnmarshaler",
	"GobEncode": "gob.GobEncoder", "GobDecode": "gob.GobDecoder",
	"Read": "io.Reader", "Write": "io.Writer", "Close": "io.Closer", "Seek": "io.Seeker",
	"WriteTo": "io.WriterTo", "ReadFrom": "io.ReaderFrom", "Len": "sort.Interface", "Less": "sort.Interface",
	"Swap": "sort.Interface", "Scan": "sql.Scanner", "Value": "driver.Valuer",
}

// clients groups the code calling the type's methods into roles.
//
// A client is a package, or a file of the type's own package, calling
// methods of the type or using them as values. Calls from the type's own
// methods are counted per row as internal instead.
func (b *build) clients() {
	r := b.r
	list := []Client{}
	for _, x := range b.gatherClients() {
		list = append(list, b.finishClient(x))
	}
	slices.SortFunc(list, compareClients)
	for n := range list {
		b.describeClient(&list[n])
	}
	r.Clients = Clients{List: list, Roles: []Role{}, Hubs: []int{}, Uncalled: []Uncalled{}}
	b.roles()
	b.blockClients()
	b.uncalledRows()
	b.getters()
}

// clientAcc is a client being gathered, with its callers kept as symbols
// until they are sorted.
type clientAcc struct {
	c       *Client
	callers []*ast.Symbol
}

// gatherClients collects the clients of every row's method by name, and
// each row's callers outside the type.
func (b *build) gatherClients() map[string]*clientAcc {
	rowOf := map[*ast.Symbol]int{}
	for i, s := range b.syms {
		rowOf[s] = i
	}
	byName := map[string]*clientAcc{}
	b.rowCallers = make([][]*ast.Symbol, len(b.syms))
	for i, s := range b.syms {
		b.gatherRowClients(i, s, rowOf, byName)
	}
	return byName
}

// gatherRowClients adds the calls and value uses of row i's method s to
// the clients in byName, or to the row's internal calls when they come from
// one of the type's own methods in rowOf.
func (b *build) gatherRowClients(i int, s *ast.Symbol, rowOf map[*ast.Symbol]int, byName map[string]*clientAcc) {
	r, a := b.r, b.a
	idx := a.index()
	uses := idx.Uses(s, r.Package)
	values := idx.ValueUses(s, r.Package)
	for n, u := range append(uses, values...) {
		value := n >= len(uses)
		if value && u.Approximate && a.fieldsIn[path.Dir(u.Caller.Path)][s.Name] {
			continue // more likely a field of that name
		}
		if _, internal := rowOf[u.Caller]; internal {
			// The type's own calls on its receiver resolve exactly;
			// the others matched only the name, on another value.
			if !u.Approximate {
				r.Rows[i].Internal += u.Calls
			}
			continue
		}
		if !slices.Contains(b.rowCallers[i], u.Caller) {
			b.rowCallers[i] = append(b.rowCallers[i], u.Caller)
		}
		b.clientNamed(byName, u.Package, u.Caller).addUse(i, u.Caller, u.Calls, value, u.Approximate)
	}
}

// clientNamed returns the client a caller in package pkg belongs to,
// adding it to byName when new: the package, or the caller's file when it
// is in the type's own package.
func (b *build) clientNamed(byName map[string]*clientAcc, pkg string, caller *ast.Symbol) *clientAcc {
	name, isPkg := pkg, true
	if pkg == b.r.Package {
		name, isPkg = caller.Path, false
	}
	x := byName[name]
	if x == nil {
		x = &clientAcc{c: &Client{Name: name, Package: isPkg, Uses: []ClientUse{}, Callers: []Sym{}, Blocks: []int{}, Approximate: true}}
		byName[name] = x
	}
	return x
}

// addUse adds calls from caller to row i, as a value or not. The use and
// the client stay approximate only while all their calls matched by name
// only.
func (x *clientAcc) addUse(i int, caller *ast.Symbol, calls int, value, approx bool) {
	k := slices.IndexFunc(x.c.Uses, func(cu ClientUse) bool { return cu.Row == i })
	if k < 0 {
		k = len(x.c.Uses)
		x.c.Uses = append(x.c.Uses, ClientUse{Row: i, Approximate: true})
	}
	cu := &x.c.Uses[k]
	cu.Calls += calls
	cu.Value = cu.Value || value
	cu.Approximate = cu.Approximate && approx
	x.c.Approximate = x.c.Approximate && approx
	if !slices.Contains(x.callers, caller) {
		x.callers = append(x.callers, caller)
	}
}

// finishClient sorts a gathered client's uses and callers and lists the
// blocks of the methods it uses.
func (b *build) finishClient(x *clientAcc) Client {
	slices.SortFunc(x.c.Uses, func(p, q ClientUse) int { return cmp.Compare(p.Row, q.Row) })
	slices.SortFunc(x.callers, func(p, q *ast.Symbol) int {
		return cmp.Or(cmp.Compare(p.Path, q.Path), cmp.Compare(p.StartLine, q.StartLine))
	})
	for _, s := range x.callers {
		x.c.Callers = append(x.c.Callers, b.a.sym(s))
	}
	for _, cu := range x.c.Uses {
		x.c.Blocks = addBlock(x.c.Blocks, b.rowBlock[cu.Row])
	}
	slices.Sort(x.c.Blocks)
	return *x.c
}

// compareClients orders the packages before the files of the type's own
// package, then by name.
func compareClients(p, q Client) int {
	if p.Package != q.Package {
		if p.Package {
			return -1
		}
		return 1
	}
	return cmp.Compare(p.Name, q.Name)
}

// describeClient counts c on the rows it uses, says which blocks it uses
// and, for a package, writes the interface it could declare.
func (b *build) describeClient(c *Client) {
	r := b.r
	for _, cu := range c.Uses {
		r.Rows[cu.Row].Clients++
	}
	switch len(c.Blocks) {
	case 0:
		c.Text = "uses only methods outside the blocks"
	case 1:
		c.Text = fmt.Sprintf("uses only %s (%s)", letter(c.Blocks[0]), r.Blocks[c.Blocks[0]].Label)
	default:
		c.Text = fmt.Sprintf("spans %s: a use case; give it a facade rather than splitting along its lines", b.letters(c.Blocks))
	}
	if c.Package {
		c.Interface = b.iface(c)
	}
}

// blockClients counts each block's clients and the share of them that use
// no other block.
func (b *build) blockClients() {
	r := b.r
	for nb := range r.Blocks {
		var users, only int
		for _, c := range r.Clients.List {
			if !slices.Contains(c.Blocks, nb) {
				continue
			}
			users++
			if len(c.Blocks) == 1 {
				only++
			}
		}
		r.Blocks[nb].Clients = users
		if users > 0 {
			ag := float64(only) / float64(users)
			r.Blocks[nb].Agreement = &ag
		}
	}
}

// uncalledRows lists the methods nothing calls, from inside the type or
// out, with why that may be.
func (b *build) uncalledRows() {
	r := b.r
	ifaceMethods := b.a.ifaceMethods()
	for i, row := range r.Rows {
		if row.Clients > 0 || row.Internal > 0 || len(b.callers(i)) > 0 {
			continue
		}
		r.Clients.Uncalled = append(r.Clients.Uncalled, Uncalled{Row: i, Why: uncalledWhy(row.Method, row.Exported, ifaceMethods)})
	}
}

// uncalledWhy says why nothing may call a method: it may implement a
// well-known interface or one in ifaceMethods, be used outside the
// repository, or be dead.
func uncalledWhy(method string, exported bool, ifaceMethods map[string]string) string {
	switch {
	case stdMethods[method] != "":
		return "may implement " + stdMethods[method]
	case ifaceMethods[method] != "":
		return "may implement " + ifaceMethods[method]
	case exported:
		return "exported: may be used outside the repository"
	default:
		return "nothing calls it: delete it"
	}
}

// ifaceMethods maps the name of each method the live interfaces declare to
// an interface declaring it.
func (a *Analyzer) ifaceMethods() map[string]string {
	out := map[string]string{}
	for _, ss := range a.ifaces {
		for _, s := range ss {
			for _, m := range s.Members {
				if m.Kind != ast.MemberMethod {
					continue
				}
				if _, ok := out[m.Name]; !ok {
					out[m.Name] = path.Dir(s.Path) + "." + s.Name
				}
			}
		}
	}
	return out
}

// roles groups the clients linked by the methods they share, leaving out
// the methods most of them call.
func (b *build) roles() {
	r := b.r
	hub := b.roleHubs()
	u := b.linkClients(hub)
	all := make([]int, len(r.Clients.List))
	for n := range all {
		all[n] = n
	}
	for _, group := range u.groups(all) {
		r.Clients.Roles = append(r.Clients.Roles, b.roleOf(group, hub))
	}
}

// roleHubs marks the methods more than half the clients call, once there
// are four clients or more, and lists them as the hubs.
func (b *build) roleHubs() []bool {
	r := b.r
	cs := r.Clients.List
	users := make([]int, len(r.Rows))
	for _, c := range cs {
		for _, cu := range c.Uses {
			users[cu.Row]++
		}
	}
	hub := make([]bool, len(r.Rows))
	if len(cs) < 4 {
		return hub
	}
	for i, n := range users {
		if float64(n) > 0.5*float64(len(cs)) {
			hub[i] = true
			r.Clients.Hubs = append(r.Clients.Hubs, i)
		}
	}
	return hub
}

// linkClients joins the clients that use the same method, unless it is a
// hub.
func (b *build) linkClients(hub []bool) union {
	cs := b.r.Clients.List
	u := newUnion(len(cs))
	for i := range b.r.Rows {
		if hub[i] {
			continue
		}
		first := -1
		for n, c := range cs {
			if !slices.ContainsFunc(c.Uses, func(cu ClientUse) bool { return cu.Row == i }) {
				continue
			}
			if first < 0 {
				first = n
			} else {
				u.union(first, n)
			}
		}
	}
	return u
}

// roleOf builds the next role from a group of linked clients: the methods
// they use other than the hubs, and the blocks of those.
func (b *build) roleOf(group []int, hub []bool) Role {
	r := b.r
	cs := r.Clients.List
	role := Role{Clients: group, Rows: []int{}, Blocks: []int{}}
	var names []string
	for _, n := range group {
		cs[n].Role = len(r.Clients.Roles)
		names = append(names, cs[n].Name)
		for _, cu := range cs[n].Uses {
			if !hub[cu.Row] && !slices.Contains(role.Rows, cu.Row) {
				role.Rows = append(role.Rows, cu.Row)
			}
		}
	}
	slices.Sort(role.Rows)
	for _, i := range role.Rows {
		role.Blocks = addBlock(role.Blocks, b.rowBlock[i])
	}
	slices.Sort(role.Blocks)
	role.Text = join(names, 3) + " use " + or(b.rowNames(role.Rows, 4), "only the most called methods")
	if len(role.Blocks) > 0 {
		role.Text += " (" + b.letters(role.Blocks) + ")"
	}
	return role
}

// getters finds the one-statement methods that only read one field and are
// called from outside: the work on that field happens in their callers.
func (b *build) getters() {
	r := b.r
	for i, row := range r.Rows {
		if row.Stmts != 1 || len(row.Cells) != 1 || row.Cells[0].Write || len(row.Calls) > 0 || row.Clients == 0 {
			continue
		}
		var callers []Sym
		for _, s := range b.rowCallers[i] {
			callers = append(callers, b.a.sym(s))
		}
		slices.SortFunc(callers, func(x, y Sym) int { return cmp.Or(cmp.Compare(x.Path, y.Path), cmp.Compare(x.Line, y.Line)) })
		col := row.Cells[0].Col
		names := make([]string, len(callers))
		for n, s := range callers {
			names[n] = s.Name
		}
		r.Findings = append(r.Findings, Finding{Kind: "getter", Rows: []int{i}, Cols: []int{col}, Blocks: []int{},
			Callers: callers,
			Text:    fmt.Sprintf("%s is read from outside through %s, by %s", r.Columns[col].Name, row.Method, join(names, 4)),
			Advice:  "Feature envy: the logic working on it may live in those callers; it may belong with this block"})
	}
}

// iface writes the interface a client package could declare for what it
// uses, so it depends on those methods only.
func (b *build) iface(c *Client) string {
	typ := b.typ
	if strings.HasPrefix(strings.TrimPrefix(typ.Signature, "type "+typ.Name), "[") {
		return "" // a generic type
	}
	name := lowerFirst(typ.Name)
	if name == typ.Package {
		name += "API"
	}
	local := map[string]bool{}
	for _, t := range b.a.types {
		if path.Dir(t.Path) == b.r.Dir && t.Package == typ.Package {
			local[t.Name] = true
		}
	}
	var needs []string
	qualify := func(id string) goast.Expr {
		if !local[id] {
			return nil
		}
		if !goast.IsExported(id) {
			if !slices.Contains(needs, id) {
				needs = append(needs, id)
			}
			return nil
		}
		return &goast.SelectorExpr{X: goast.NewIdent(typ.Package), Sel: goast.NewIdent(id)}
	}
	rows := make([]int, len(c.Uses))
	for n, cu := range c.Uses {
		rows[n] = cu.Row
	}
	slices.SortFunc(rows, func(x, y int) int { return cmp.Compare(b.r.Rows[x].Decl, b.r.Rows[y].Decl) })
	var lines []string
	for _, i := range rows {
		ft := funcType(b.syms[i])
		if ft == nil {
			return ""
		}
		qualifyExpr(ft, qualify)
		lines = append(lines, "\t"+b.syms[i].Name+strings.TrimPrefix(types.ExprString(ft), "func"))
	}
	var s strings.Builder
	if len(needs) > 0 {
		fmt.Fprintf(&s, "// needs unexported %s: export it or keep the parameter concrete\n", join(needs, 0))
	}
	fmt.Fprintf(&s, "type %s interface {\n%s\n}", name, strings.Join(lines, "\n"))
	return s.String()
}

func lowerFirst(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	return string(unicode.ToLower(r)) + s[n:]
}

// funcType parses the parameters and results of a function's or method's
// signature into a func type; nil when it cannot.
func funcType(s *ast.Symbol) *goast.FuncType {
	sig := strings.TrimPrefix(s.Signature, "func ")
	sig = skipBracketed(sig, '(', ')') // the receiver
	sig = strings.TrimPrefix(strings.TrimSpace(sig), s.Name)
	sig = skipBracketed(sig, '[', ']') // type parameters
	e, err := parser.ParseExpr("func" + sig)
	if err != nil {
		return nil
	}
	ft, _ := e.(*goast.FuncType)
	return ft
}

// skipBracketed drops a leading bracketed group from s.
func skipBracketed(s string, open, close rune) string {
	if !strings.HasPrefix(s, string(open)) {
		return s
	}
	depth := 0
	for i, c := range s {
		switch c {
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return s[i+1:]
			}
		}
	}
	return s
}

// paramTypes lists the types of a function's parameters, one per name.
func paramTypes(s *ast.Symbol) []string {
	ft := funcType(s)
	if ft == nil || ft.Params == nil {
		return nil
	}
	var out []string
	for _, f := range ft.Params.List {
		t := types.ExprString(f.Type)
		for range max(1, len(f.Names)) {
			out = append(out, t)
		}
	}
	return out
}

// qualifyExpr replaces, in the types of e, the identifiers q has a
// replacement for.
func qualifyExpr(e goast.Expr, q func(string) goast.Expr) goast.Expr {
	switch x := e.(type) {
	case *goast.Ident:
		if r := q(x.Name); r != nil {
			return r
		}
	case *goast.StarExpr:
		x.X = qualifyExpr(x.X, q)
	case *goast.ArrayType:
		x.Elt = qualifyExpr(x.Elt, q)
	case *goast.MapType:
		x.Key, x.Value = qualifyExpr(x.Key, q), qualifyExpr(x.Value, q)
	case *goast.ChanType:
		x.Value = qualifyExpr(x.Value, q)
	case *goast.Ellipsis:
		x.Elt = qualifyExpr(x.Elt, q)
	case *goast.ParenExpr:
		x.X = qualifyExpr(x.X, q)
	case *goast.IndexExpr:
		x.X, x.Index = qualifyExpr(x.X, q), qualifyExpr(x.Index, q)
	case *goast.IndexListExpr:
		x.X = qualifyExpr(x.X, q)
		for n := range x.Indices {
			x.Indices[n] = qualifyExpr(x.Indices[n], q)
		}
	case *goast.FuncType:
		qualifyFields(x.Params, q)
		qualifyFields(x.Results, q)
	}
	return e
}

// qualifyFields qualifies the types of a func type's parameters or
// results, which may be missing.
func qualifyFields(fl *goast.FieldList, q func(string) goast.Expr) {
	if fl == nil {
		return
	}
	for _, f := range fl.List {
		f.Type = qualifyExpr(f.Type, q)
	}
}
