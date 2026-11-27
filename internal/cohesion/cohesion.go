// Package cohesion builds the affinity matrix of one struct type, as the
// technique for splitting a God object does: a row per method, a column
// per field, each cell marking whether the method reads (R) or writes (W)
// the field, and a column of the methods each calls.
//
// It classifies the columns (entity state, policy or config, collaborator,
// infrastructure, temporary), sets aside the hubs that most rows use,
// reorders rows and columns into blocks along the diagonal and reads the
// five patterns: blocks are candidate types; bridge rows only call other
// methods and belong in a coordinator; rows spanning blocks are split with
// Extract Method first; a column written from two blocks is the hardest
// coupling, since ownership follows writes; and nearly empty rows and
// columns are free functions and fields only read through getters.
//
// Reads from another block are cheap and do not join blocks; writes and
// the use of policy, collaborators and temporaries do. The blocks are then
// checked against the other signals, who calls the methods, which change
// together and how the members are named, and the block to extract first
// is picked: one that owns its writes and changes most often.
//
// Everything is syntactic, as ast's references are: fields are matched on
// the receiver by name, and a method's calls on the receiver by name.
package cohesion

import (
	"cmp"
	goast "go/ast"
	"slices"
	"strings"

	"huginn/internal/ast"
)

// Input is one struct type at one revision.
type Input struct {
	Type    *ast.Symbol   // the type's declaration; its fields are its Members
	Methods []*ast.Symbol // its methods, from files other than tests

	// Clients, if not nil, returns the declarations outside the type that
	// use a method of it.
	Clients func(m *ast.Symbol) []Client

	// Interfaces holds the names of the methods interfaces declare. Such a
	// method may be called through an interface, where Clients cannot see
	// it, so it is not reported unused.
	Interfaces map[string]bool

	// Changes, if not nil, holds the commits that changed each method, by
	// method name: for change coupling, and for picking the block that
	// changes most often.
	Changes map[string][]string
}

// Client is a declaration outside the type that uses one of its methods.
type Client struct {
	Package     string // import path
	Name        string
	Calls       int
	Test        bool
	Approximate bool // matched by method name only
}

// Report is the affinity matrix of one type and what it shows.
type Report struct {
	Type string `json:"type"` // its name

	// Fields and Methods are the matrix's columns and rows, reordered block
	// by block so that each block's marks sit on the diagonal; hubs and
	// infrastructure come last among the columns.
	Fields  []Field  `json:"fields"`
	Methods []Method `json:"methods"`
	Blocks  []Block  `json:"blocks"`

	// LCOM4 counts the groups of methods connected by a shared field or a
	// call, on the raw type. Hubs and orchestration usually make it 1;
	// LCOM4Split counts them once hubs, infrastructure and bridge rows are
	// removed, which is when it becomes telling.
	LCOM4      int `json:"lcom4"`
	LCOM4Split int `json:"lcom4Split"`

	Ties         []Tie         `json:"ties"`         // rows spanning blocks
	SharedWrites []SharedWrite `json:"sharedWrites"` // columns written from two blocks
	Envy         []Envy        `json:"envy"`         // columns only read through getters

	Clients []ClientRow `json:"clients"` // the callers × public methods matrix
	Roles   []Role      `json:"roles"`   // its blocks of callers
	Unused  []string    `json:"unused"`  // public methods no caller uses

	Words []Word   `json:"words"` // the naming pass
	Flags []string `json:"flags"` // mode flags and states

	Coupled []Coupling `json:"coupled"` // methods changed together

	// Start is the block to extract first, if one owns its writes.
	Start *Start `json:"start"`

	// Advice is what the signals say together about the whole type.
	Advice []string `json:"advice"`
}

// Field is a column of the matrix.
type Field struct {
	Name string `json:"name"`
	Type string `json:"type"`

	// Role is the column's type, which says where it ends up:
	// "entity" (state written by methods: stays with the core entity),
	// "policy" (only read, set when the value is made: a stateless policy
	// or service), "collaborator" (an I/O handle, only read: the adapter
	// that uses it), "infrastructure" (logging, metrics: each new type gets
	// its own), "temporary" (written and read only within one operation: a
	// method object for it) or "unused".
	Role string `json:"role"`

	// Operation is the method whose operation a temporary field serves.
	Operation string `json:"operation,omitempty"`

	// Hub is set for a column most rows use; hubs connect everything and
	// hide the structure, so they are set aside.
	Hub bool `json:"hub"`

	Block   string `json:"block"` // the block's label; empty when in none
	Readers int    `json:"readers"`
	Writers int    `json:"writers"`
}

// Method is a row of the matrix.
type Method struct {
	Name     string `json:"name"`
	Exported bool   `json:"exported"`
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Stmts    int    `json:"stmts"`

	Uses  []Use    `json:"uses"`  // the fields it reads or writes, by index into Fields
	Calls []string `json:"calls"` // the methods of the type it calls

	// Kind says where the row goes: "" in Block; "core" uses only hubs;
	// "bridge" marks no field and calls other methods: orchestration;
	// "free" marks no field and calls nothing: a free function; "tie"
	// marks the fields of several blocks, listed in Spans.
	Kind  string   `json:"kind"`
	Block string   `json:"block"`
	Spans []string `json:"spans,omitempty"`
}

// Use is one cell of the matrix.
type Use struct {
	Field int  `json:"field"`
	Read  bool `json:"read"`
	Write bool `json:"write"`
}

// Block is a set of columns and the rows using them: a candidate type,
// whose fields the columns become and whose methods the rows.
type Block struct {
	Label   string   `json:"label"` // "A", "B", ..., or "core" for the hubs and the rows using only them
	Kind    string   `json:"kind"`  // "entity", "policy", "adapter", "method object" or "core", from its columns
	Fields  []string `json:"fields"`
	Methods []string `json:"methods"`

	// OwnsWrites says no row outside the block writes its columns, so it
	// can move out without anything else changing its state.
	OwnsWrites bool `json:"ownsWrites"`

	Changes int    `json:"changes"` // commits that changed its methods, given the history
	Word    string `json:"word"`    // a word most of its members are named with

	// Evidence is what the other signals say about splitting it off, and
	// Verdict sums it up: "extract" when they agree, "check" when one
	// disagrees, and "candidate" when none tells.
	Evidence []Evidence `json:"evidence"`
	Verdict  string     `json:"verdict"`
}

// Evidence is what one signal says about a block.
type Evidence struct {
	Signal string `json:"signal"` // "clients", "history" or "names"
	Stance string `json:"stance"` // "agrees" or "disagrees" with splitting it off, or "note"
	Text   string `json:"text"`
}

// Tie is a row spanning blocks: split it with Extract Method until each
// piece falls in one block, before splitting the type.
type Tie struct {
	Method string   `json:"method"`
	Stmts  int      `json:"stmts"`
	Blocks []string `json:"blocks"`
}

// SharedWrite is a column with W marks in more than one block: no block
// can keep its rules, so one should own the field and the others pass the
// value in or ask the owner to change it.
type SharedWrite struct {
	Field   string   `json:"field"`
	Writers []string `json:"writers"`
	Blocks  []string `json:"blocks"`
}

// Envy is a column only read, and only through getters: code outside the
// type does the work with it, and probably belongs in its block.
type Envy struct {
	Field   string   `json:"field"`
	Getters []string `json:"getters"`
	Clients []string `json:"clients"` // packages calling the getters
}

// ClientRow is a row of the callers × public methods matrix: one package.
type ClientRow struct {
	Package string   `json:"package"`
	Methods []string `json:"methods"`
	Calls   int      `json:"calls"`
}

// Role is a block of that matrix: callers using the same methods, one role
// the type plays for them, which a consumer-side interface can name.
type Role struct {
	Clients []string `json:"clients"`
	Methods []string `json:"methods"`
	Blocks  []string `json:"blocks"` // the field blocks of those methods; several make it a use case
}

// Word is a word several member names share, as billing in billingName and
// billingStreet.
type Word struct {
	Word    string   `json:"word"`
	Members []string `json:"members"`
}

// Coupling is a pair of methods changed in the same commits.
type Coupling struct {
	A        string `json:"a"`
	B        string `json:"b"`
	Together int    `json:"together"` // commits changing both
	AChanges int    `json:"aChanges"`
	BChanges int    `json:"bChanges"`
	Across   bool   `json:"across"` // the methods are in different blocks
}

// Start is the block to extract first.
type Start struct {
	Block  string `json:"block"`
	Reason string `json:"reason"`
}

// Analyze builds the affinity matrix of the type in and reads it.
func Analyze(in Input) *Report {
	a := newAnalysis(in)
	a.classify()
	r := &Report{Type: in.Type.Name, LCOM4: a.lcom4(false)}
	a.kinds()
	r.LCOM4Split = a.lcom4(true)
	a.block()
	a.report(r)
	r.Ties, r.SharedWrites, r.Envy = a.ties(), a.sharedWrites(), a.envy(in)
	if in.Clients != nil {
		r.Clients, r.Roles, r.Unused = a.clients(in)
	}
	r.Words, r.Flags = naming(a.fields, a.methods)
	if in.Changes != nil {
		r.Coupled = a.coupling(in.Changes)
	}
	a.weigh(r, in.Changes)
	return r
}

// analysis holds the matrix while it is worked out.
type analysis struct {
	typ     string
	fields  []Field
	methods []Method
	syms    []*ast.Symbol // by method index
	field   map[string]int
	method  map[string]int

	aside  []bool  // fields set aside from blocking: hubs, infrastructure, unused, shared writes
	edges  [][]int // per method, the fields that place it
	strong []bool  // per method: placed by a write or a non-entity column, not by reads alone

	blocks []struct{ fields, methods []int } // A, B, ... in order
}

func newAnalysis(in Input) *analysis {
	a := &analysis{typ: in.Type.Name, field: map[string]int{}, method: map[string]int{}}
	for _, m := range in.Type.Members {
		if (m.Kind != ast.MemberField && m.Kind != ast.MemberEmbedded) || m.Name == "" || m.Name == "_" {
			continue
		}
		a.field[m.Name] = len(a.fields)
		a.fields = append(a.fields, Field{Name: m.Name, Type: m.Type})
	}
	ms := slices.Clone(in.Methods)
	slices.SortFunc(ms, func(x, y *ast.Symbol) int {
		return cmp.Or(cmp.Compare(x.Path, y.Path), cmp.Compare(x.StartLine, y.StartLine))
	})
	for _, s := range ms {
		if _, dup := a.method[s.Name]; dup {
			continue
		}
		a.method[s.Name] = len(a.methods)
		a.methods = append(a.methods, Method{Name: s.Name, Exported: goast.IsExported(s.Name), Path: s.Path, Line: s.StartLine, Stmts: s.Stmts})
		a.syms = append(a.syms, s)
	}
	for i, s := range a.syms {
		m := &a.methods[i]
		for _, fr := range s.FieldRefs {
			if fr.Type != a.typ || fr.Pkg != "" {
				continue
			}
			if f, ok := a.field[fr.Name]; ok {
				m.Uses = append(m.Uses, Use{Field: f, Read: fr.Count > fr.Writes, Write: fr.Writes > 0})
				continue
			}
			// A method value, x.M without a call.
			if j, ok := a.method[fr.Name]; ok && j != i && !slices.Contains(m.Calls, fr.Name) {
				m.Calls = append(m.Calls, fr.Name)
			}
		}
		for _, ref := range s.Refs {
			if ref.Kind == ast.RefMethod && ref.Recv == a.typ && ref.Name != s.Name {
				if _, ok := a.method[ref.Name]; ok && !slices.Contains(m.Calls, ref.Name) {
					m.Calls = append(m.Calls, ref.Name)
				}
			}
		}
		slices.SortFunc(m.Uses, func(x, y Use) int { return cmp.Compare(x.Field, y.Field) })
		slices.Sort(m.Calls)
		for _, u := range m.Uses {
			if u.Read {
				a.fields[u.Field].Readers++
			}
			if u.Write {
				a.fields[u.Field].Writers++
			}
		}
	}
	return a
}

// Hubs are columns used by more than half the rows that mark any column,
// once at least minHubMethods rows do: on smaller types every column looks
// shared.
const minHubMethods = 5

// classify gives each column its type and finds the hubs.
func (a *analysis) classify() {
	for i := range a.fields {
		f := &a.fields[i]
		switch {
		case isInfrastructure(f.Name, f.Type):
			f.Role = "infrastructure"
		case f.Readers+f.Writers == 0:
			f.Role = "unused"
		case f.Writers > 0:
			f.Role = "entity"
			if op := a.operation(i); op != "" {
				f.Role, f.Operation = "temporary", op
			}
		case isCollaborator(f.Name, f.Type):
			f.Role = "collaborator"
		default:
			f.Role = "policy"
		}
	}
	users := make([]int, len(a.fields))
	marking := 0
	for _, m := range a.methods {
		counted := false
		for _, u := range m.Uses {
			if a.fields[u.Field].Role != "infrastructure" {
				users[u.Field]++
				counted = true
			}
		}
		if counted {
			marking++
		}
	}
	a.aside = make([]bool, len(a.fields))
	for i, f := range a.fields {
		if marking >= minHubMethods && f.Role != "infrastructure" && users[i] >= 3 && 2*users[i] > marking {
			a.fields[i].Hub = true
		}
		a.aside[i] = a.fields[i].Hub || f.Role == "infrastructure" || f.Role == "unused"
	}
}

// operation returns the method a field is temporary to: one that writes
// it, from which every row using it is reached by calls; or "".
func (a *analysis) operation(f int) string {
	var users []int
	for i, m := range a.methods {
		if slices.ContainsFunc(m.Uses, func(u Use) bool { return u.Field == f }) {
			users = append(users, i)
		}
	}
	for _, w := range users {
		if !slices.ContainsFunc(a.methods[w].Uses, func(u Use) bool { return u.Field == f && u.Write }) {
			continue
		}
		reach := a.reach(w)
		if !slices.ContainsFunc(users, func(i int) bool { return !reach[i] }) {
			return a.methods[w].Name
		}
	}
	return ""
}

// reach returns the methods reached from method i by calls, i included.
func (a *analysis) reach(i int) []bool {
	seen := make([]bool, len(a.methods))
	stack := []int{i}
	seen[i] = true
	for len(stack) > 0 {
		j := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, c := range a.methods[j].Calls {
			if k := a.method[c]; !seen[k] {
				seen[k] = true
				stack = append(stack, k)
			}
		}
	}
	return seen
}

// lcom4 counts the groups of methods connected by a shared column or a
// call. With split, hubs, infrastructure and bridge rows are removed
// first.
func (a *analysis) lcom4(split bool) int {
	keep := func(i int) bool { return !split || a.methods[i].Kind != "bridge" }
	uf := newUnion(len(a.methods))
	byField := make([]int, len(a.fields))
	for i := range byField {
		byField[i] = -1
	}
	n := 0
	for i, m := range a.methods {
		if !keep(i) {
			continue
		}
		n++
		for _, u := range m.Uses {
			if split && (a.fields[u.Field].Hub || a.fields[u.Field].Role == "infrastructure") {
				continue
			}
			if j := byField[u.Field]; j >= 0 {
				uf.join(i, j)
			} else {
				byField[u.Field] = i
			}
		}
		for _, c := range m.Calls {
			if j := a.method[c]; keep(j) {
				uf.join(i, j)
			}
		}
	}
	roots := map[int]bool{}
	for i := range a.methods {
		if keep(i) {
			roots[uf.find(i)] = true
		}
	}
	return len(roots)
}

// kinds finds, for each row, the columns that place it in a block. A write
// places a row with the field, and so does any use of a policy, a
// collaborator or a temporary: they end up with their users. A read of
// entity state is cheap and places a row only when nothing else does.
// Rows placed by nothing use only hubs (core), only call other methods
// (bridge) or neither (free).
func (a *analysis) kinds() {
	a.edges = make([][]int, len(a.methods))
	a.strong = make([]bool, len(a.methods))
	for i := range a.methods {
		m := &a.methods[i]
		var strong, reads []int
		hubs := false
		for _, u := range m.Uses {
			f := a.fields[u.Field]
			switch {
			case a.aside[u.Field]:
				// Hubs and shared writes, not infrastructure or unused.
				hubs = hubs || f.Role != "infrastructure" && f.Role != "unused"
			case u.Write || f.Role != "entity":
				strong = append(strong, u.Field)
			default:
				reads = append(reads, u.Field)
			}
		}
		a.edges[i], a.strong[i] = strong, len(strong) > 0
		if len(strong) == 0 {
			a.edges[i] = reads
		}
		switch {
		case len(a.edges[i]) > 0:
			m.Kind = ""
		case hubs:
			m.Kind = "core"
		case len(m.Calls) > 0:
			m.Kind = "bridge"
		default:
			m.Kind = "free"
		}
	}
}

// Rows spanning blocks and shared writes are only looked for in parts of
// at least minTieGroup rows: two blocks and what joins them.
const minTieGroup = 3

// block finds the blocks. Rows are connected through the columns that
// place them, and through calls between rows placed by writes or
// non-entity columns. In each connected part, a column whose writers fall
// apart once it is removed, each with columns of its own, is written from
// two blocks; it is set aside like a hub. Then a row marking columns that
// fall apart once the row is removed spans blocks, and is set aside too.
// What stays connected is a block.
func (a *analysis) block() {
	placed := func() []int {
		var out []int
		for i, m := range a.methods {
			if m.Kind == "" {
				out = append(out, i)
			}
		}
		return out
	}
	shared := false
	for _, part := range a.parts(placed(), nil, nil) {
		if len(part) < minTieGroup {
			continue
		}
		for _, f := range a.columns(part) {
			if a.fields[f].Writers < 2 {
				continue
			}
			writes := func(j int) bool {
				return slices.ContainsFunc(a.methods[j].Uses, func(u Use) bool { return u.Field == f && u.Write })
			}
			n := 0
			for _, side := range a.parts(part, nil, map[int]bool{f: true}) {
				own := slices.ContainsFunc(a.columns(side), func(c int) bool { return c != f })
				if own && slices.ContainsFunc(side, writes) {
					n++
				}
			}
			if n > 1 {
				a.aside[f], shared = true, true
			}
		}
	}
	if shared {
		a.kinds() // rows the shared columns placed are placed by what else they use
	}

	tie := map[int]bool{}
	for _, part := range a.parts(placed(), nil, nil) {
		if len(part) < minTieGroup {
			continue
		}
		for _, i := range part {
			if len(a.edges[i]) < 2 {
				continue // a single column is one block
			}
			sides := a.parts(part, map[int]bool{i: true}, nil)
			if a.apart(sides, a.edges[i], func(j, f int) bool { return slices.Contains(a.edges[j], f) }) {
				tie[i] = true
			}
		}
	}
	for i := range tie {
		a.methods[i].Kind = "tie"
	}

	parts := a.parts(placed(), nil, nil)
	slices.SortStableFunc(parts, func(x, y []int) int { return cmp.Or(cmp.Compare(len(y), len(x)), cmp.Compare(x[0], y[0])) })
	for k, part := range parts {
		label := blockLabel(k)
		cols := a.columns(part)
		for _, i := range part {
			a.methods[i].Block = label
		}
		for _, f := range cols {
			a.fields[f].Block = label
		}
		a.blocks = append(a.blocks, struct{ fields, methods []int }{cols, part})
	}
	for i, f := range a.fields {
		if f.Hub {
			a.fields[i].Block = "core"
		}
	}
	for i, m := range a.methods {
		switch m.Kind {
		case "core":
			a.methods[i].Block = "core"
		case "tie":
			for _, f := range a.edges[i] {
				if b := a.fields[f].Block; b != "" && !slices.Contains(a.methods[i].Spans, b) {
					a.methods[i].Spans = append(a.methods[i].Spans, b)
				}
			}
			slices.Sort(a.methods[i].Spans)
		}
	}
}

// apart reports whether the rows that touch the columns cols, as touches
// tells, lie in more than one of sides.
func (a *analysis) apart(sides [][]int, cols []int, touches func(row, col int) bool) bool {
	at := -1
	for k, side := range sides {
		for _, j := range side {
			if slices.ContainsFunc(cols, func(f int) bool { return touches(j, f) }) {
				if at >= 0 && at != k {
					return true
				}
				at = k
				break
			}
		}
	}
	return false
}

// parts splits rows into the parts their columns and calls connect,
// leaving out the rows in skipRows and the columns in skipCols.
func (a *analysis) parts(rows []int, skipRows, skipCols map[int]bool) [][]int {
	uf := newUnion(len(a.methods) + len(a.fields))
	in := map[int]bool{}
	for _, i := range rows {
		if !skipRows[i] {
			in[i] = true
		}
	}
	for i := range in {
		for _, f := range a.edges[i] {
			if !skipCols[f] {
				uf.join(i, len(a.methods)+f)
			}
		}
		if !a.strong[i] {
			continue
		}
		for _, c := range a.methods[i].Calls {
			if j := a.method[c]; in[j] && a.strong[j] {
				uf.join(i, j)
			}
		}
	}
	at := map[int]int{}
	var out [][]int
	for _, i := range rows {
		if !in[i] {
			continue
		}
		root := uf.find(i)
		k, ok := at[root]
		if !ok {
			k = len(out)
			at[root] = k
			out = append(out, nil)
		}
		out[k] = append(out[k], i)
	}
	return out
}

// columns returns the columns that place the rows, sorted.
func (a *analysis) columns(rows []int) []int {
	var out []int
	for _, i := range rows {
		for _, f := range a.edges[i] {
			if !slices.Contains(out, f) {
				out = append(out, f)
			}
		}
	}
	slices.Sort(out)
	return out
}

func blockLabel(k int) string {
	if k < 26 {
		return string(rune('A' + k))
	}
	return "Z" + strings.Repeat("'", k/26) + string(rune('A'+k%26))
}

// blockKind says where a block's columns end up: with the core entity if it
// holds entity state, else in the adapter of its collaborators, a method
// object for its temporaries, or a stateless policy.
func blockKind(roles []string) string {
	switch {
	case slices.Contains(roles, "entity"):
		return "entity"
	case slices.Contains(roles, "collaborator"):
		return "adapter"
	case slices.Contains(roles, "temporary"):
		return "method object"
	}
	return "policy"
}

// report fills in the matrix, rows and columns block by block: block
// columns, then hubs, shared writes and the rest, infrastructure last;
// block rows, then core rows, rows spanning blocks, bridges and free rows.
func (a *analysis) report(r *Report) {
	var forder, morder []int
	for _, b := range a.blocks {
		forder = append(forder, b.fields...)
		morder = append(morder, b.methods...)
	}
	rest := func(order []int, n int, take func(int) bool) []int {
		for i := range n {
			if !slices.Contains(order, i) && take(i) {
				order = append(order, i)
			}
		}
		return order
	}
	forder = rest(forder, len(a.fields), func(i int) bool { return a.fields[i].Hub })
	forder = rest(forder, len(a.fields), func(i int) bool { return a.fields[i].Role != "infrastructure" && a.fields[i].Role != "unused" })
	forder = rest(forder, len(a.fields), func(int) bool { return true })
	for _, kind := range []string{"core", "tie", "bridge", "free"} {
		morder = rest(morder, len(a.methods), func(i int) bool { return a.methods[i].Kind == kind })
	}

	newField := make([]int, len(a.fields))
	for k, i := range forder {
		newField[i] = k
		r.Fields = append(r.Fields, a.fields[i])
	}
	for _, i := range morder {
		m := a.methods[i]
		uses := make([]Use, len(m.Uses))
		for k, u := range m.Uses {
			uses[k] = Use{Field: newField[u.Field], Read: u.Read, Write: u.Write}
		}
		slices.SortFunc(uses, func(x, y Use) int { return cmp.Compare(x.Field, y.Field) })
		m.Uses = uses
		r.Methods = append(r.Methods, m)
	}
	for k, b := range a.blocks {
		blk := Block{Label: blockLabel(k)}
		var roles []string
		for _, f := range b.fields {
			blk.Fields = append(blk.Fields, a.fields[f].Name)
			roles = append(roles, a.fields[f].Role)
		}
		for _, i := range b.methods {
			blk.Methods = append(blk.Methods, a.methods[i].Name)
		}
		blk.Kind = blockKind(roles)
		r.Blocks = append(r.Blocks, blk)
	}
	core := Block{Label: "core", Kind: "core"}
	for _, f := range a.fields {
		if f.Hub {
			core.Fields = append(core.Fields, f.Name)
		}
	}
	for _, m := range r.Methods {
		if m.Kind == "core" {
			core.Methods = append(core.Methods, m.Name)
		}
	}
	if len(core.Fields)+len(core.Methods) > 0 {
		r.Blocks = append(r.Blocks, core)
	}
}

func (a *analysis) ties() []Tie {
	var out []Tie
	for _, m := range a.methods {
		if m.Kind == "tie" {
			out = append(out, Tie{Method: m.Name, Stmts: m.Stmts, Blocks: m.Spans})
		}
	}
	return out
}

// sharedWrites finds the columns with W marks in more than one block, the
// core counted as one.
func (a *analysis) sharedWrites() []SharedWrite {
	var out []SharedWrite
	for f, fld := range a.fields {
		if fld.Role == "infrastructure" {
			continue
		}
		sw := SharedWrite{Field: fld.Name}
		for _, m := range a.methods {
			if m.Block == "" {
				continue // spanning, bridge or free rows are in no block
			}
			for _, u := range m.Uses {
				if u.Field == f && u.Write {
					sw.Writers = append(sw.Writers, m.Name)
					if !slices.Contains(sw.Blocks, m.Block) {
						sw.Blocks = append(sw.Blocks, m.Block)
					}
				}
			}
		}
		if len(sw.Blocks) > 1 {
			slices.Sort(sw.Blocks)
			out = append(out, sw)
		}
	}
	return out
}

// envy finds columns read only by getters: exported methods of at most one
// statement that read that field alone and call nothing.
func (a *analysis) envy(in Input) []Envy {
	var out []Envy
	for f, fld := range a.fields {
		if fld.Writers > 0 || fld.Readers == 0 || fld.Role == "infrastructure" {
			continue
		}
		e := Envy{Field: fld.Name}
		only := true
		for i, m := range a.methods {
			uses := slices.ContainsFunc(m.Uses, func(u Use) bool { return u.Field == f })
			if !uses {
				continue
			}
			if !m.Exported || m.Stmts > 1 || len(m.Uses) != 1 || len(m.Calls) > 0 {
				only = false
				break
			}
			e.Getters = append(e.Getters, m.Name)
			if in.Clients != nil {
				for _, c := range in.Clients(a.syms[i]) {
					if !c.Test && !slices.Contains(e.Clients, c.Package) {
						e.Clients = append(e.Clients, c.Package)
					}
				}
			}
		}
		if only && len(e.Getters) > 0 {
			slices.Sort(e.Clients)
			out = append(out, e)
		}
	}
	return out
}

// union is a union-find over n elements.
type union struct{ parent []int }

func newUnion(n int) *union {
	u := &union{parent: make([]int, n)}
	for i := range u.parent {
		u.parent[i] = i
	}
	return u
}

func (u *union) find(i int) int {
	for u.parent[i] != i {
		u.parent[i] = u.parent[u.parent[i]]
		i = u.parent[i]
	}
	return i
}

func (u *union) join(i, j int) { u.parent[u.find(i)] = u.find(j) }
