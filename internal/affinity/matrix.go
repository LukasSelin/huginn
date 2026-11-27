package affinity

import (
	"cmp"
	"fmt"
	"path"
	"slices"
	"strings"

	"huginn/internal/ast"
)

// build is one analysis in progress. Until order runs, rows and columns
// are in declaration order; after it, in display order.
type build struct {
	a    *Analyzer
	opts Options
	typ  *ast.Symbol
	r    *Report

	syms []*ast.Symbol // per row
	cell [][]use       // [row][col]
	call [][]int       // [row][row]: calls and uses as a value
	val  [][]bool      // [row][row]: used as a value

	// clustering, filled by cluster and remapped by order
	blocks   []*blk
	rowBlock []int // index into blocks, -1 when not in one
	why      []why
	glue     []int      // rows writing state that also use a service block
	shared   []sharedAt // shared state, one per column
	readsAt  [][]int    // per row, the state columns it reads from a service block
	free     []int      // display order, after order
	bridges  []int

	rowCallers [][]*ast.Symbol // per row, its callers outside the type
}

// use is how one method uses one field.
type use struct {
	n, w  int    // occurrences, of which writes
	r, wr bool   // read, written
	via   string // the call that writes it, or "promoted"
}

func (u use) any() bool { return u.r || u.wr }

// blk is a block being formed.
type blk struct {
	kind   BlockKind
	cols   []int
	rows   []int
	hubs   []int   // the hub columns a shared block needs
	merged []merge // helper blocks merged into it
	dead   bool    // merged into another
}

type merge struct{ rows, callers []int }

// why is the reason a row was placed, rendered once the blocks have letters.
type why struct {
	rule    string // writes, uses, reads, delegate, bridge, free, freeCalls, shared, cycle
	cols    []int
	blocks  []int
	callees []int
	infra   []int // infrastructure fields a free row uses
}

type sharedAt struct {
	col   int
	parts [][]int // the state columns on each side, the column's own side first
	both  bool    // written from two sides, not only read from the other
}

var lockMethods = map[string]bool{"Lock": true, "Unlock": true, "RLock": true, "RUnlock": true, "TryLock": true, "TryRLock": true}

// newBuild builds the matrix of typ: its fields, its methods, the cells and
// calls, and the class of every field.
func (a *Analyzer) newBuild(typ *ast.Symbol, opts Options) *build {
	dir := path.Dir(typ.Path)
	b := &build{a: a, opts: opts, typ: typ, syms: a.methods[recvKey{dir, typ.Package, typ.Name}]}
	r := &Report{Type: a.sym(typ), Dir: dir, Package: a.pkgOf(typ), Struct: isStruct(typ), Months: opts.Months,
		Columns: []Column{}, Rows: []Row{}, Blocks: []Block{}, Findings: []Finding{}, Steps: []Step{}, Notes: []Note{}}
	b.r = r

	colOf := map[string]int{}
	var embedded []int
	for _, m := range typ.Members {
		if m.Kind != ast.MemberField && m.Kind != ast.MemberEmbedded || m.Name == "_" {
			continue
		}
		j := len(r.Columns)
		colOf[m.Name] = j
		if m.Kind == ast.MemberEmbedded {
			embedded = append(embedded, j)
		}
		r.Columns = append(r.Columns, Column{Name: m.Name, Type: m.Type, Embedded: m.Kind == ast.MemberEmbedded,
			Decl: j, Group: m.Group, Line: m.Line, Block: -1})
	}
	rowOf := map[string]int{}
	for i, s := range b.syms {
		rowOf[s.Name] = i
		r.Rows = append(r.Rows, Row{Sym: a.sym(s), Method: s.Name, Signature: s.Signature, Exported: s.Exported,
			Decl: i, Stmts: s.Stmts, Cells: []Cell{}, Calls: []Call{}, Block: -1, Notes: []string{}})
	}
	nr, nc := len(r.Rows), len(r.Columns)
	b.cell = make([][]use, nr)
	b.call = make([][]int, nr)
	b.val = make([][]bool, nr)
	for i := range nr {
		b.cell[i] = make([]use, nc)
		b.call[i] = make([]int, nr)
		b.val[i] = make([]bool, nr)
	}
	mutex := -1
	for _, j := range embedded {
		if t := strings.TrimPrefix(r.Columns[j].Type, "*"); t == "sync.Mutex" || t == "sync.RWMutex" {
			mutex = j
		}
	}

	for i, s := range b.syms {
		for _, fr := range s.FieldRefs {
			if fr.Pkg != "" || fr.Type != typ.Name || fr.Name == "" {
				continue
			}
			if j, ok := colOf[fr.Name]; ok {
				b.mark(i, j, fr.Count, fr.Writes, fr.Methods, "")
				continue
			}
			if k, ok := rowOf[fr.Name]; ok {
				b.call[i][k] += fr.Count
				b.val[i][k] = true
				continue
			}
			if len(embedded) == 1 {
				b.mark(i, embedded[0], fr.Count, fr.Writes, nil, "promoted")
				continue
			}
			r.Unattributed += fr.Count
		}
		for _, ref := range s.Refs {
			if ref.Kind != ast.RefMethod || ref.Recv != typ.Name {
				continue
			}
			k, isMethod := rowOf[ref.Name]
			j, isField := colOf[ref.Name]
			switch {
			case isMethod:
				b.call[i][k] += ref.Count
			case isField:
				b.mark(i, j, ref.Count, 0, nil, "") // calls a func-typed field
			case lockMethods[ref.Name] && mutex >= 0:
				b.mark(i, mutex, ref.Count, 0, nil, ref.Name)
			case len(embedded) == 1:
				b.mark(i, embedded[0], ref.Count, 0, nil, "promoted")
			default:
				r.Unattributed += ref.Count
			}
		}
	}
	ctor := a.ctor[typeKey{r.Package, typ.Name}]
	for j := range r.Columns {
		r.Columns[j].Ctor = ctor[r.Columns[j].Name]
	}
	b.classify()
	return b
}

// mark records that row i uses column j: n times, w of them as writes, and
// calling methods on it.
func (b *build) mark(i, j, n, w int, methods []string, via string) {
	u := &b.cell[i][j]
	u.n += n
	u.w += w
	if w > 0 {
		u.wr = true
	}
	writers := mutators(b.r.Columns[j].Type)
	var others int
	for _, m := range methods {
		if writers[m] {
			u.wr = true
			u.w++
			if u.via == "" {
				u.via = m
			}
		} else {
			others++
		}
	}
	switch {
	case writers != nil:
		// The calls tell: Store writes, Load reads.
		if others > 0 || len(methods) == 0 && n > w {
			u.r = true
		}
	case n > w:
		u.r = true
	}
	if via != "" && u.via == "" {
		u.via = via
	}
}

// mutators returns the methods that change a value of a field's type, for
// the types whose changes go through methods; nil for others.
func mutators(typ string) map[string]bool {
	t := strings.TrimPrefix(typ, "*")
	if i := strings.IndexByte(t, '['); i >= 0 {
		t = t[:i]
	}
	switch {
	case strings.HasPrefix(t, "atomic."):
		return set("Store", "Swap", "CompareAndSwap", "Add", "And", "Or")
	case t == "sync.Map":
		return set("Store", "Swap", "CompareAndSwap", "Delete", "LoadOrStore", "LoadAndDelete", "CompareAndDelete", "Clear")
	case t == "bytes.Buffer" || t == "strings.Builder":
		return set("Write", "WriteString", "WriteByte", "WriteRune", "ReadFrom", "Reset", "Grow", "Truncate")
	}
	return nil
}

func set(xs ...string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

var infraTypes = set("sync.Mutex", "sync.RWMutex", "sync.Once", "sync.WaitGroup", "sync.Cond",
	"log.Logger", "slog.Logger", "zap.Logger", "zap.SugaredLogger", "logr.Logger", "zerolog.Logger", "logrus.Logger", "logrus.Entry")

var infraNames = set("mu", "mtx", "mutex", "lock", "wg", "once", "log", "logger", "metrics", "tracer")

var stdInterfaces = set("context.Context", "error", "io.Reader", "io.Writer", "io.Closer", "io.ReadCloser", "io.WriteCloser",
	"io.ReadWriter", "io.ReadWriteCloser", "fs.FS", "http.Handler", "http.RoundTripper", "net.Conn", "net.Listener", "slog.Handler")

func isInfra(c *Column) bool {
	return infraTypes[strings.TrimPrefix(c.Type, "*")] || infraNames[strings.ToLower(c.Name)] ||
		strings.HasSuffix(c.Name, "Mu") || strings.HasSuffix(c.Name, "Mutex") || strings.HasSuffix(c.Name, "Lock")
}

// isCollaborator reports whether a field of type typ, declared in dir, is
// a handle on something else rather than a value.
func (b *build) isCollaborator(typ string) bool {
	for _, p := range []string{"func(", "func ", "interface{", "*", "chan ", "<-chan ", "chan<- "} {
		if strings.HasPrefix(typ, p) {
			return true
		}
	}
	if stdInterfaces[typ] {
		return true
	}
	pkg, name, qualified := strings.Cut(typ, ".")
	if !qualified {
		pkg, name = "", typ
	}
	for _, s := range b.a.ifaces[name] {
		if qualified && s.Package == pkg || !qualified && path.Dir(s.Path) == b.r.Dir {
			return true
		}
	}
	return false
}

// classify gives every column its class and marks the hubs.
func (b *build) classify() {
	r := b.r
	reach := b.closures()
	for j := range r.Columns {
		c := &r.Columns[j]
		var touch, write []int
		for i := range r.Rows {
			u := b.cell[i][j]
			if u.any() {
				touch = append(touch, i)
			}
			if u.wr {
				write = append(write, i)
			}
			if u.r {
				c.Readers++
			}
		}
		c.Writers = len(write)
		temp := -1
		if len(touch) >= 2 {
			for _, w := range write {
				if !slices.ContainsFunc(touch, func(i int) bool { return !reach[w][i] }) {
					temp = w
					break
				}
			}
		}
		switch {
		case isInfra(c):
			c.Class = Infra
			c.Reason = "a lock, logger or the like: infrastructure, which each new type gets its own of"
		case len(touch) == 0:
			c.Class = Unused
			c.Reason = "no method uses it"
		case temp >= 0:
			c.Class = Temporary
			c.Reason = fmt.Sprintf("written by %s and used only by what it calls: the state of one operation", b.syms[temp].Name)
		case len(write) > 0:
			c.Class = Entity
			c.Reason = "written by " + b.rowNames(write, 3)
		case b.isCollaborator(c.Type):
			c.Class = Collaborator
			c.Reason = "never written by a method, and a handle on something else: a collaborator"
		default:
			c.Class = Config
			c.Reason = "never written by a method: configuration"
		}
		if c.Ctor && (c.Class == Config || c.Class == Collaborator) {
			c.Reason += ", set where the type is constructed"
		}
	}

	var fieldRows int
	for i := range r.Rows {
		if b.touchesNonInfra(i) {
			fieldRows++
		}
	}
	if fieldRows < 4 {
		return
	}
	for j := range r.Columns {
		c := &r.Columns[j]
		if c.Class != Config && c.Class != Collaborator {
			continue
		}
		n := 0
		for i := range r.Rows {
			if b.cell[i][j].any() {
				n++
			}
		}
		if float64(n) > b.opts.HubShare*float64(fieldRows) {
			c.Hub = true
			c.Reason = fmt.Sprintf("used by %d of the %d methods that use fields: a hub, left out of clustering", n, fieldRows)
		}
	}
}

func (b *build) touchesNonInfra(i int) bool {
	for j, u := range b.cell[i] {
		if u.any() && b.r.Columns[j].Class != Infra {
			return true
		}
	}
	return false
}

// closures returns, per row, the rows it reaches through calls and method
// values, itself included.
func (b *build) closures() [][]bool {
	n := len(b.syms)
	out := make([][]bool, n)
	for i := range n {
		seen := make([]bool, n)
		seen[i] = true
		stack := []int{i}
		for len(stack) > 0 {
			x := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			for k, c := range b.call[x] {
				if c > 0 && !seen[k] {
					seen[k] = true
					stack = append(stack, k)
				}
			}
		}
		out[i] = seen
	}
	return out
}

func (b *build) isEntity(j int) bool { return b.r.Columns[j].Class == Entity }

func (b *build) isService(j int) bool {
	c := b.r.Columns[j]
	return !c.Hub && (c.Class == Config || c.Class == Collaborator || c.Class == Temporary)
}

// entitySide reports whether row i belongs with state rather than with a
// service: it writes state, or reads state and uses no service.
func (b *build) entitySide(i int) bool {
	var e, n bool
	for j, u := range b.cell[i] {
		switch {
		case !u.any():
		case b.isEntity(j) && u.wr:
			return true
		case b.isEntity(j):
			e = true
		case b.isService(j):
			n = true
		}
	}
	return e && !n
}

// cluster forms the blocks and places every row.
func (b *build) cluster() {
	r := b.r
	nr, nc := len(r.Rows), len(r.Columns)
	b.rowBlock = fill(nr, -1)
	b.why = make([]why, nr)
	b.readsAt = make([][]int, nr)
	colBlock := fill(nc, -1)

	// State: the fields written together, linked by the rows on their side.
	var entityCols []int
	for j := range nc {
		if b.isEntity(j) {
			entityCols = append(entityCols, j)
		}
	}
	work := b.components(entityCols, nil)
	for len(work) > 0 {
		comp := work[0]
		work = work[1:]
		if parts, at, ok := b.splitShared(comp); ok {
			work = append(parts, work...)
			if at.both {
				b.shared = append(b.shared, at)
				r.Columns[at.col].Shared = true
			}
			continue
		}
		bl := &blk{kind: State, cols: comp}
		for _, j := range comp {
			colBlock[j] = len(b.blocks)
		}
		b.blocks = append(b.blocks, bl)
	}

	// Services: configuration, collaborators and temporaries, linked by any
	// row using two of them.
	var serviceCols []int
	for j := range nc {
		if b.isService(j) {
			serviceCols = append(serviceCols, j)
		}
	}
	for _, comp := range b.components(serviceCols, func(int) bool { return true }) {
		for _, j := range comp {
			colBlock[j] = len(b.blocks)
		}
		b.blocks = append(b.blocks, &blk{kind: Service, cols: comp})
	}

	// Place the rows that use fields of a block.
	for i := range nr {
		var reads, writes, services, sharedOnly []int
		for j, u := range b.cell[i] {
			switch {
			case !u.any():
			case b.isEntity(j) && r.Columns[j].Shared:
				sharedOnly = append(sharedOnly, j)
			case b.isEntity(j) && u.wr:
				writes = append(writes, j)
				reads = append(reads, j)
			case b.isEntity(j):
				reads = append(reads, j)
			case b.isService(j):
				services = append(services, j)
			}
		}
		if len(reads) == 0 && len(sharedOnly) > 0 {
			for _, j := range sharedOnly {
				reads = append(reads, j)
				if b.cell[i][j].wr {
					writes = append(writes, j)
				}
			}
		}
		switch {
		case len(writes) > 0:
			bl := b.most(i, writes, func(u use) int { return u.w }, colBlock)
			b.place(i, bl, why{rule: "writes", cols: inBlock(writes, colBlock, bl)})
			if len(services) > 0 {
				b.glue = append(b.glue, i)
			}
		case len(services) > 0:
			bl := colBlock[services[0]]
			b.place(i, bl, why{rule: "uses", cols: services})
			b.readsAt[i] = reads
		case len(reads) > 0:
			bl := b.most(i, reads, func(u use) int { return u.n }, colBlock)
			b.place(i, bl, why{rule: "reads", cols: inBlock(reads, colBlock, bl)})
		}
	}

	b.mergeHelpers()

	// Rows needing only hubs, and rows that only call others.
	sharedBy := map[string]int{}
	joinShared := func(i int, hubs []int) {
		key := fmt.Sprint(hubs)
		bl, ok := sharedBy[key]
		if !ok {
			bl = len(b.blocks)
			sharedBy[key] = bl
			b.blocks = append(b.blocks, &blk{kind: Shared, hubs: hubs})
		}
		b.place(i, bl, why{rule: "shared", cols: hubs})
	}
	hubsOf := func(i int) (hubs, infra []int) {
		for j, u := range b.cell[i] {
			if u.any() && r.Columns[j].Hub {
				hubs = append(hubs, j)
			} else if u.any() && r.Columns[j].Class == Infra {
				infra = append(infra, j)
			}
		}
		return hubs, infra
	}
	kind := make([]RowKind, nr)
	for i := range nr {
		if b.rowBlock[i] >= 0 {
			kind[i] = Member
			continue
		}
		hubs, infra := hubsOf(i)
		if len(b.callees(i)) > 0 {
			continue
		}
		if len(hubs) > 0 {
			joinShared(i, hubs)
			kind[i] = Member
			continue
		}
		kind[i] = Free
		b.why[i] = why{rule: "free", infra: infra}
	}
	for changed := true; changed; {
		changed = false
		for i := range nr {
			if kind[i] != "" {
				continue
			}
			callees := b.callees(i)
			var groups []int
			bridge, ready := false, true
			for _, k := range callees {
				switch kind[k] {
				case "":
					ready = false
				case Bridge:
					bridge = true
				case Member:
					if !slices.Contains(groups, b.rowBlock[k]) {
						groups = append(groups, b.rowBlock[k])
					}
				case Free:
					// A free callee belongs to no block.
				}
			}
			if !ready {
				continue
			}
			changed = true
			slices.Sort(groups)
			switch hubs, infra := hubsOf(i); {
			case bridge || len(groups) >= 2:
				kind[i] = Bridge
				b.why[i] = why{rule: "bridge", blocks: groups, callees: callees}
			case len(groups) == 1:
				kind[i] = Member
				b.place(i, groups[0], why{rule: "delegate", blocks: groups, callees: callees})
			case len(hubs) > 0:
				kind[i] = Member
				joinShared(i, hubs)
			default:
				kind[i] = Free
				b.why[i] = why{rule: "freeCalls", callees: callees, infra: infra}
			}
		}
	}
	for i := range nr {
		if kind[i] == "" {
			kind[i] = Bridge
			b.why[i] = why{rule: "cycle", callees: b.callees(i)}
		}
		r.Rows[i].Kind = kind[i]
	}
	for x, bl := range b.blocks {
		if !bl.dead {
			for _, j := range bl.cols {
				r.Columns[j].Block = x
			}
		}
	}
}

// components groups cols into the sets linked by the rows that use two of
// them; only rows accepted by side count, entitySide when side is nil.
// The sets and their columns are in declaration order.
func (b *build) components(cols []int, side func(int) bool) [][]int {
	if side == nil {
		side = b.entitySide
	}
	in := map[int]bool{}
	for _, j := range cols {
		in[j] = true
	}
	u := newUnion(len(b.r.Columns))
	for i := range b.r.Rows {
		if !side(i) {
			continue
		}
		first := -1
		for j, c := range b.cell[i] {
			if !c.any() || !in[j] {
				continue
			}
			if first < 0 {
				first = j
			} else {
				u.union(first, j)
			}
		}
	}
	return u.groups(cols)
}

// splitShared finds a state column whose removal splits comp into parts
// that each have rows of their own: state written from two sides. The
// column goes to the side of its top writer.
func (b *build) splitShared(comp []int) (parts [][]int, at sharedAt, ok bool) {
	if len(comp) < 3 {
		return nil, at, false
	}
	for _, c := range comp {
		rest := slices.DeleteFunc(slices.Clone(comp), func(j int) bool { return j == c })
		parts = b.components(rest, nil)
		if len(parts) < 2 || slices.ContainsFunc(parts, func(p []int) bool { return !b.hasSideRow(p) }) {
			continue
		}
		top, best := -1, use{}
		for i := range b.r.Rows {
			u := b.cell[i][c]
			if u.wr && (top < 0 || u.w > best.w || u.w == best.w && u.n > best.n) {
				top, best = i, u
			}
		}
		own := 0
		for p, cols := range parts {
			if top >= 0 && slices.ContainsFunc(cols, func(j int) bool { return b.cell[top][j].any() }) {
				own = p
				break
			}
		}
		sides := map[int]bool{}
		for i := range b.r.Rows {
			if !b.cell[i][c].wr {
				continue
			}
			side := own
			for p, cols := range parts {
				if slices.ContainsFunc(cols, func(j int) bool { return b.cell[i][j].any() }) {
					side = p
					break
				}
			}
			sides[side] = true
		}
		parts[own] = append(parts[own], c)
		slices.Sort(parts[own])
		parts[0], parts[own] = parts[own], parts[0]
		at = sharedAt{col: c, both: len(sides) >= 2}
		for _, p := range parts {
			at.parts = append(at.parts, slices.Clone(p))
		}
		return parts, at, true
	}
	return nil, at, false
}

func (b *build) hasSideRow(cols []int) bool {
	for i := range b.r.Rows {
		if b.entitySide(i) && slices.ContainsFunc(cols, func(j int) bool { return b.cell[i][j].any() }) {
			return true
		}
	}
	return false
}

// most returns the block of cols that row i uses most by weight, the first
// on a tie.
func (b *build) most(i int, cols []int, weight func(use) int, colBlock []int) int {
	by := map[int]int{}
	for _, j := range cols {
		by[colBlock[j]] += weight(b.cell[i][j])
	}
	best := -1
	for bl, w := range by {
		if best < 0 || w > by[best] || w == by[best] && bl < best {
			best = bl
		}
	}
	return best
}

func inBlock(cols, colBlock []int, bl int) []int {
	var out []int
	for _, j := range cols {
		if colBlock[j] == bl {
			out = append(out, j)
		}
	}
	return out
}

func (b *build) place(i, bl int, w why) {
	b.rowBlock[i] = bl
	b.why[i] = w
	b.blocks[bl].rows = append(b.blocks[bl].rows, i)
}

// callees returns the other rows row i calls or uses as a value.
func (b *build) callees(i int) []int {
	var out []int
	for k, c := range b.call[i] {
		if c > 0 && k != i {
			out = append(out, k)
		}
	}
	return out
}

// callers returns the other rows calling row k or using it as a value.
func (b *build) callers(k int) []int {
	var out []int
	for i := range b.call {
		if b.call[i][k] > 0 && i != k {
			out = append(out, i)
		}
	}
	return out
}

// mergeHelpers merges a service block into another when every caller of
// its rows from outside is in that other service block: it is that block's
// helper. Callers not placed yet keep it apart, as does a caller in a block
// of state, which holds it rather than absorbs it.
func (b *build) mergeHelpers() {
	for changed := true; changed; {
		changed = false
		for x, bl := range b.blocks {
			if bl.dead || bl.kind != Service || len(bl.rows) == 0 {
				continue
			}
			var callers []int
			for _, k := range bl.rows {
				for _, i := range b.callers(k) {
					if b.rowBlock[i] != x && !slices.Contains(callers, i) {
						callers = append(callers, i)
					}
				}
			}
			target := -1
			for _, i := range callers {
				y := b.rowBlock[i]
				if y < 0 || b.blocks[y].kind != Service || target >= 0 && y != target {
					target = -2
					break
				}
				target = y
			}
			if len(callers) == 0 || target < 0 {
				continue
			}
			into := b.blocks[target]
			into.cols = append(into.cols, bl.cols...)
			into.rows = append(into.rows, bl.rows...)
			slices.Sort(callers)
			into.merged = append(into.merged, merge{rows: slices.Clone(bl.rows), callers: callers})
			for _, i := range bl.rows {
				b.rowBlock[i] = target
			}
			bl.dead = true
			changed = true
		}
	}
}

// order picks the core, letters the blocks and puts rows and columns in
// display order: block by block, then hubs, infrastructure and unused
// fields, then bridges and free methods.
func (b *build) order() {
	r := b.r
	live := b.liveBlocks()
	b.pickCore(live)
	slices.SortStableFunc(live, b.compareBlocks)
	newBlock := fill(len(b.blocks), -1)
	for n, x := range live {
		newBlock[x] = n
	}

	// Display order of the columns and rows, as old indexes.
	cols, rows := b.displayCols(live), b.displayRows(live)
	colAt, rowAt := invert(cols), invert(rows)

	// Permute everything.
	b.permuteCols(cols, newBlock)
	b.permuteRows(rows, newBlock, colAt, rowAt)
	b.permuteCells(cols, rows)
	b.glue = remap(b.glue, rowAt)
	b.remapShared(colAt)
	b.keepLive(live, colAt, rowAt)

	// Cells, calls and the report's blocks.
	b.fillRows()
	b.reportBlocks()
	b.rowNotes()
	for n := range r.Blocks {
		b.label(n)
	}
	r.LCOM4 = b.lcom4()
}

// liveBlocks returns the blocks not merged into another, with their columns
// and rows sorted.
func (b *build) liveBlocks() []int {
	var live []int
	for x, bl := range b.blocks {
		if bl.dead {
			continue
		}
		slices.Sort(bl.cols)
		slices.Sort(bl.rows)
		live = append(live, x)
	}
	return live
}

// pickCore makes the largest state block of live the core, the first of
// them on a tie.
func (b *build) pickCore(live []int) {
	core := -1
	for _, x := range live {
		if b.blocks[x].kind == State && (core < 0 || b.stateSize(x) > b.stateSize(core)) {
			core = x
		}
	}
	if core >= 0 {
		b.blocks[core].kind = Core
	}
}

// stateSize counts the columns and rows of block x, which rank the state
// blocks.
func (b *build) stateSize(x int) int { return len(b.blocks[x].cols) + len(b.blocks[x].rows) }

// blockKindRank puts the core first, then state, services and shared blocks.
var blockKindRank = map[BlockKind]int{Core: 0, State: 1, Service: 2, Shared: 3}

// compareBlocks orders blocks x and y for display: by kind, then the larger
// state block first, services by their first column and the rest by their
// first row.
func (b *build) compareBlocks(x, y int) int {
	bx, by := b.blocks[x], b.blocks[y]
	if c := cmp.Compare(blockKindRank[bx.kind], blockKindRank[by.kind]); c != 0 {
		return c
	}
	switch bx.kind {
	case State:
		return cmp.Or(cmp.Compare(b.stateSize(y), b.stateSize(x)), cmp.Compare(firstOrEnd(bx.cols), firstOrEnd(by.cols)))
	case Service:
		return cmp.Compare(firstOrEnd(bx.cols), firstOrEnd(by.cols))
	case Core, Shared:
	}
	return cmp.Compare(firstOrEnd(bx.rows), firstOrEnd(by.rows))
}

// firstOrEnd returns the first of xs, or a number past any index when xs is
// empty, so that an empty block sorts last.
func firstOrEnd(xs []int) int {
	if len(xs) == 0 {
		return 1 << 30
	}
	return xs[0]
}

// displayCols lists the columns in display order, as old indexes: block by
// block, then the hubs, infrastructure and unused fields outside blocks.
func (b *build) displayCols(live []int) []int {
	var cols []int
	for _, x := range live {
		cols = append(cols, b.blocks[x].cols...)
	}
	for _, class := range []func(Column) bool{
		func(c Column) bool { return c.Hub },
		func(c Column) bool { return c.Class == Infra },
		func(c Column) bool { return c.Class == Unused },
	} {
		for j, c := range b.r.Columns {
			if c.Block < 0 && class(c) && !slices.Contains(cols, j) {
				cols = append(cols, j)
			}
		}
	}
	return cols
}

// displayRows lists the rows in display order, as old indexes: block by
// block, then the bridges and the free methods.
func (b *build) displayRows(live []int) []int {
	var rows []int
	for _, x := range live {
		rows = append(rows, b.blocks[x].rows...)
	}
	for _, k := range []RowKind{Bridge, Free} {
		for i, row := range b.r.Rows {
			if row.Kind == k {
				rows = append(rows, i)
			}
		}
	}
	return rows
}

// permuteCols puts the report's columns in the display order cols, with
// their blocks renumbered by newBlock.
func (b *build) permuteCols(cols, newBlock []int) {
	r := b.r
	oldCols := r.Columns
	r.Columns = make([]Column, len(cols))
	for n, j := range cols {
		r.Columns[n] = oldCols[j]
		if c := &r.Columns[n]; c.Block >= 0 {
			c.Block = newBlock[c.Block]
		}
	}
}

// permuteRows puts the report's rows, and what is kept per row, in the
// display order rows, with the indexes they hold mapped through newBlock,
// colAt and rowAt.
func (b *build) permuteRows(rows, newBlock, colAt, rowAt []int) {
	r := b.r
	oldRows, oldSyms, oldRowBlock, oldWhy, oldReads := r.Rows, b.syms, b.rowBlock, b.why, b.readsAt
	r.Rows = make([]Row, len(rows))
	b.syms, b.rowBlock, b.why, b.readsAt = make([]*ast.Symbol, len(rows)), make([]int, len(rows)), make([]why, len(rows)), make([][]int, len(rows))
	for n, i := range rows {
		r.Rows[n] = oldRows[i]
		b.syms[n] = oldSyms[i]
		b.rowBlock[n] = -1
		if x := oldRowBlock[i]; x >= 0 {
			b.rowBlock[n] = newBlock[x]
		}
		r.Rows[n].Block = b.rowBlock[n]
		w := oldWhy[i]
		w.cols, w.blocks, w.callees, w.infra = remap(w.cols, colAt), remap(w.blocks, newBlock), remap(w.callees, rowAt), remap(w.infra, colAt)
		b.why[n] = w
		b.readsAt[n] = remap(oldReads[i], colAt)
	}
}

// permuteCells puts the cells, calls and value uses in the display order
// cols and rows.
func (b *build) permuteCells(cols, rows []int) {
	oldCell, oldCall, oldVal := b.cell, b.call, b.val
	b.cell, b.call, b.val = make([][]use, len(rows)), make([][]int, len(rows)), make([][]bool, len(rows))
	for n, i := range rows {
		b.cell[n] = make([]use, len(cols))
		b.call[n] = make([]int, len(rows))
		b.val[n] = make([]bool, len(rows))
		for m, j := range cols {
			b.cell[n][m] = oldCell[i][j]
		}
		for m, k := range rows {
			b.call[n][m] = oldCall[i][k]
			b.val[n][m] = oldVal[i][k]
		}
	}
}

// remapShared maps the columns of the shared state through colAt.
func (b *build) remapShared(colAt []int) {
	for s := range b.shared {
		b.shared[s].col = colAt[b.shared[s].col]
		for p := range b.shared[s].parts {
			b.shared[s].parts[p] = remap(b.shared[s].parts[p], colAt)
		}
	}
}

// keepLive keeps only the live blocks, in their display order, with their
// columns and rows mapped through colAt and rowAt.
func (b *build) keepLive(live, colAt, rowAt []int) {
	blocks := make([]*blk, len(live))
	for n, x := range live {
		bl := b.blocks[x]
		bl.cols, bl.rows, bl.hubs = remap(bl.cols, colAt), remap(bl.rows, rowAt), remap(bl.hubs, colAt)
		for m := range bl.merged {
			bl.merged[m].rows, bl.merged[m].callers = remap(bl.merged[m].rows, rowAt), remap(bl.merged[m].callers, rowAt)
		}
		blocks[n] = bl
	}
	b.blocks = blocks
}

// fillRows gives each report row its cells and calls, and lists the
// bridges and free methods.
func (b *build) fillRows() {
	r := b.r
	for i := range r.Rows {
		row := &r.Rows[i]
		for j, u := range b.cell[i] {
			if u.any() {
				row.Cells = append(row.Cells, Cell{Col: j, Read: u.r, Write: u.wr, Count: u.n, Via: u.via})
			}
		}
		for k, c := range b.call[i] {
			if c > 0 {
				row.Calls = append(row.Calls, Call{Row: k, Value: b.val[i][k], Count: c})
			}
		}
		switch row.Kind {
		case Bridge:
			b.bridges = append(b.bridges, i)
		case Free:
			b.free = append(b.free, i)
		case Member:
		}
	}
}

// reportBlocks adds the blocks to the report and counts those that are not
// shared.
func (b *build) reportBlocks() {
	r := b.r
	for n, bl := range b.blocks {
		r.Blocks = append(r.Blocks, Block{Letter: letter(n), Kind: bl.kind, Columns: orEmpty(bl.cols), Rows: orEmpty(bl.rows),
			Crossings: []Crossing{}, Outside: []Outside{}})
		if bl.kind != Shared {
			r.Split++
		}
	}
}

// rowNotes gives each row its reason, and notes on what it reads from a
// service block and, for glue, which services it also uses.
func (b *build) rowNotes() {
	r := b.r
	for i := range r.Rows {
		r.Rows[i].Reason = b.reason(i)
		if reads := b.readsAt[i]; len(reads) > 0 {
			r.Rows[i].Notes = append(r.Rows[i].Notes, fmt.Sprintf("reads %s: take them as parameters", b.colsIn(reads)))
		}
	}
	for _, i := range b.glue {
		r.Rows[i].Notes = append(r.Rows[i].Notes, fmt.Sprintf("also uses %s: extract that part first", b.colsIn(b.serviceUses(i))))
	}
}

// serviceUses returns the service columns row i uses.
func (b *build) serviceUses(i int) []int {
	var uses []int
	for j, u := range b.cell[i] {
		if u.any() && b.isService(j) {
			uses = append(uses, j)
		}
	}
	return uses
}

// reason renders why row i was placed.
func (b *build) reason(i int) string {
	w := b.why[i]
	switch w.rule {
	case "writes":
		return "writes " + b.colNames(w.cols)
	case "uses":
		return "uses " + b.colNames(w.cols)
	case "reads":
		return "reads " + b.colNames(w.cols)
	case "shared":
		return "needs only " + b.colNames(w.cols)
	case "delegate":
		return fmt.Sprintf("calls only %s (%s)", b.rowNames(w.callees, 3), letter(w.blocks[0]))
	case "bridge":
		if len(w.blocks) == 0 {
			return "calls only coordinators: " + b.rowNames(w.callees, 3)
		}
		return "coordinates " + b.letters(w.blocks)
	case "freeCalls":
		return "calls only methods that touch no field: " + b.rowNames(w.callees, 3)
	case "cycle":
		return "calls methods that call it back: " + b.rowNames(w.callees, 3)
	}
	s := "touches no field and calls no method"
	if len(w.infra) > 0 {
		s += " (but " + b.colNames(w.infra) + ")"
	}
	return s
}

// label names block n and says why it holds together.
func (b *build) label(n int) {
	r, bl := b.r, b.blocks[n]
	out := &r.Blocks[n]
	switch bl.kind {
	case Core:
		out.Label = "core"
		out.Reason = "the largest block of state: " + b.colNames(bl.cols) + ", written by " + b.writersOf(bl.cols)
	case State:
		out.Reason = "state of its own: " + b.colNames(bl.cols) + ", written by " + b.writersOf(bl.cols)
	case Service:
		out.Reason = b.serviceReason(bl)
	case Shared:
		out.Label = "needs only " + b.colNames(bl.hubs)
		out.Reason = "methods that use only the hub " + b.colNames(bl.hubs)
	}
	if out.Label == "" {
		if len(bl.cols) > 0 {
			names := []string{r.Columns[bl.cols[0]].Name}
			if len(bl.cols) > 1 {
				names = append(names, r.Columns[bl.cols[1]].Name)
			}
			out.Label = strings.Join(names, "+")
		} else if len(bl.rows) > 0 {
			out.Label = r.Rows[bl.rows[0]].Method
		}
	}
}

// serviceReason says what service block bl holds: its configuration,
// collaborators and state of one operation, and the helpers merged into it.
func (b *build) serviceReason(bl *blk) string {
	var kinds []string
	for _, c := range []Class{Config, Collaborator, Temporary} {
		var of []int
		for _, j := range bl.cols {
			if b.r.Columns[j].Class == c {
				of = append(of, j)
			}
		}
		if len(of) > 0 {
			kinds = append(kinds, map[Class]string{Config: "configuration", Collaborator: "collaborators", Temporary: "the state of one operation"}[c]+" "+b.colNames(of))
		}
	}
	reason := strings.Join(kinds, ", ") + " and the methods using them"
	for _, m := range bl.merged {
		reason += fmt.Sprintf("; %s only called from %s", b.rowNames(m.rows, 3), b.rowNames(m.callers, 3))
	}
	return reason
}

func (b *build) writersOf(cols []int) string {
	var rows []int
	for i := range b.r.Rows {
		if slices.ContainsFunc(cols, func(j int) bool { return b.cell[i][j].wr }) {
			rows = append(rows, i)
		}
	}
	return b.rowNames(rows, 3)
}

// lcom4 counts the groups of methods linked by any shared field or a call.
func (b *build) lcom4() int {
	n := len(b.r.Rows)
	u := newUnion(n)
	for j := range b.r.Columns {
		first := -1
		for i := range n {
			if !b.cell[i][j].any() {
				continue
			}
			if first < 0 {
				first = i
			} else {
				u.union(first, i)
			}
		}
	}
	for i := range n {
		for k, c := range b.call[i] {
			if c > 0 {
				u.union(i, k)
			}
		}
	}
	roots := map[int]bool{}
	for i := range n {
		roots[u.find(i)] = true
	}
	return len(roots)
}

// findings reports the patterns of the matrix.
func (b *build) findings() {
	r := b.r
	b.glueFindings()
	b.sharedStateFindings()
	b.articulationFindings()
	if len(b.bridges) > 0 {
		b.addFinding(Finding{Kind: "bridge", Rows: b.bridges,
			Text:   b.rowNames(b.bridges, 0) + " only call methods of several blocks",
			Advice: "Orchestration: keep them in a thin coordinator, an application service that holds the extracted parts"})
	}
	b.freeFinding()
	b.hubFindings()
	for n, bl := range b.blocks {
		if bl.kind == Shared {
			b.addFinding(Finding{Kind: "hubOnly", Rows: bl.rows, Cols: bl.hubs, Blocks: []int{n},
				Text:   fmt.Sprintf("%s need only %s (%s)", b.rowNames(bl.rows, 0), b.colNames(bl.hubs), letter(n)),
				Advice: "They depend on nothing but configuration: pass them only what they read of it"})
		}
	}
	for j, c := range r.Columns {
		if c.Class == Temporary {
			b.addFinding(Finding{Kind: "temporary", Cols: []int{j}, Blocks: []int{c.Block}, Text: c.Name + " is " + c.Reason,
				Advice: "Extract that operation into an object of its own that holds it: a method object"})
		}
	}
	for _, cl := range b.clumps() {
		b.addFinding(Finding{Kind: "clump", Rows: cl.Rows, Cols: cl.Cols,
			Text:   fmt.Sprintf("%s are used by exactly the same methods (%s)", b.colNames(cl.Cols), b.rowNames(cl.Rows, 4)),
			Advice: "Fields that always travel together may be one value type"})
	}
	b.unusedFinding()
	b.crossings()
}

// addFinding adds f to the report, with empty lists rather than nil ones.
func (b *build) addFinding(f Finding) {
	f.Rows, f.Cols, f.Blocks = orEmpty(f.Rows), orEmpty(f.Cols), orEmpty(f.Blocks)
	b.r.Findings = append(b.r.Findings, f)
}

// glueFindings reports each row that writes the state of its block and also
// uses service blocks.
func (b *build) glueFindings() {
	r := b.r
	for _, i := range b.glue {
		uses := b.serviceUses(i)
		var blocks []int
		for _, j := range uses {
			if !slices.Contains(blocks, r.Columns[j].Block) {
				blocks = append(blocks, r.Columns[j].Block)
			}
		}
		own := b.rowBlock[i]
		b.addFinding(Finding{Kind: "glue", Rows: []int{i}, Cols: uses, Blocks: append([]int{own}, blocks...),
			Text:   fmt.Sprintf("%s writes %s (%s) and uses %s", r.Rows[i].Method, b.colNames(b.why[i].cols), letter(own), b.colsIn(uses)),
			Advice: "Split it with Extract Method until each piece falls inside one block, then look again: long methods are often what ties the blocks together"})
	}
}

// sharedStateFindings reports each column that is state of two blocks, with
// the methods of each block that write it.
func (b *build) sharedStateFindings() {
	r := b.r
	for _, s := range b.shared {
		var parts []string
		var blocks []int
		for _, p := range s.parts {
			bl := b.partBlock(s.col, p)
			if bl < 0 {
				continue
			}
			blocks = append(blocks, bl)
			parts = append(parts, fmt.Sprintf("%s (%s)", or(b.rowNames(b.blockWriters(s.col, bl), 3), "no method"), letter(bl)))
		}
		b.addFinding(Finding{Kind: "sharedState", Cols: []int{s.col}, Blocks: blocks,
			Text:   fmt.Sprintf("%s is state of two blocks, written by %s", r.Columns[s.col].Name, strings.Join(parts, " and ")),
			Advice: "Ownership follows writes: choose one block to own it; the other gets the value passed in or asks the owner to change it"})
	}
}

// partBlock returns the block of the first column in part other than col,
// or -1 when none is in a block.
func (b *build) partBlock(col int, part []int) int {
	for _, j := range part {
		if j != col && b.r.Columns[j].Block >= 0 {
			return b.r.Columns[j].Block
		}
	}
	return -1
}

// blockWriters returns the rows of block bl that write column col.
func (b *build) blockWriters(col, bl int) []int {
	var writers []int
	for i := range b.r.Rows {
		if b.cell[i][col].wr && b.rowBlock[i] == bl {
			writers = append(writers, i)
		}
	}
	return writers
}

// articulationFindings reports each method that alone ties the columns of
// its block together: without it, the block's columns fall into two or more
// parts.
func (b *build) articulationFindings() {
	r := b.r
	for n, bl := range b.blocks {
		if bl.kind == Shared || len(bl.cols) < 2 || len(bl.rows) < 2 {
			continue
		}
		for _, m := range bl.rows {
			rest := slices.DeleteFunc(slices.Clone(bl.rows), func(i int) bool { return i == m })
			parts := b.componentsBy(bl.cols, rest)
			if len(parts) < 2 {
				continue
			}
			var sides []string
			for _, p := range parts {
				sides = append(sides, b.colNames(p))
			}
			b.addFinding(Finding{Kind: "articulation", Rows: []int{m}, Cols: bl.cols, Blocks: []int{n},
				Text:   fmt.Sprintf("only %s ties %s together in %s", r.Rows[m].Method, strings.Join(sides, " and "), letter(n)),
				Advice: "Extract Method on it until each piece uses one side, then look again: the block may be two"})
		}
	}
}

// freeFinding reports the methods that touch no field and call no method,
// and the parameter type they all take, if any.
func (b *build) freeFinding() {
	if len(b.free) == 0 {
		return
	}
	f := Finding{Kind: "free", Rows: b.free,
		Text:   b.rowNames(b.free, 0) + " touch no field and call no method",
		Advice: "Move them out first: functions are the safest extraction"}
	if p := b.sharedParam(b.free); p != "" && len(b.free) > 1 {
		f.Text += "; all take " + p
		f.Advice += "; they may belong with " + strings.TrimLeft(p, "*[]")
	}
	b.addFinding(f)
}

// hubFindings reports each hub column with the rows that use it.
func (b *build) hubFindings() {
	r := b.r
	for j, c := range r.Columns {
		if !c.Hub {
			continue
		}
		var rows []int
		for i := range r.Rows {
			if b.cell[i][j].any() {
				rows = append(rows, i)
			}
		}
		b.addFinding(Finding{Kind: "hub", Rows: rows, Cols: []int{j}, Text: c.Name + " is " + c.Reason[:strings.Index(c.Reason, ":")],
			Advice: "Give each extracted part what it needs of it rather than splitting along it"})
	}
}

// unusedFinding reports the columns no method uses.
func (b *build) unusedFinding() {
	var unused []int
	for j, c := range b.r.Columns {
		if c.Class == Unused {
			unused = append(unused, j)
		}
	}
	if len(unused) > 0 {
		b.addFinding(Finding{Kind: "unused", Cols: unused, Text: b.colNames(unused) + " used by no method",
			Advice: "Delete what nothing uses, or find the code outside the type that does"})
	}
}

// componentsBy groups cols into the sets linked by the given rows.
func (b *build) componentsBy(cols, rows []int) [][]int {
	u := newUnion(len(b.r.Columns))
	for _, i := range rows {
		first := -1
		for _, j := range cols {
			if !b.cell[i][j].any() {
				continue
			}
			if first < 0 {
				first = j
			} else {
				u.union(first, j)
			}
		}
	}
	groups := u.groups(cols)
	// Only parts that some remaining row uses count.
	return slices.DeleteFunc(groups, func(p []int) bool {
		return !slices.ContainsFunc(rows, func(i int) bool {
			return slices.ContainsFunc(p, func(j int) bool { return b.cell[i][j].any() })
		})
	})
}

// clumps returns the sets of two or more fields used by exactly the same
// two or more methods; infrastructure and unused fields are left out.
func (b *build) clumps() []Clump {
	by := map[string]*Clump{}
	var order []string
	for j, c := range b.r.Columns {
		if c.Class == Infra || c.Class == Unused {
			continue
		}
		var rows []int
		for i := range b.r.Rows {
			if b.cell[i][j].any() {
				rows = append(rows, i)
			}
		}
		if len(rows) < 2 {
			continue
		}
		k := fmt.Sprint(rows)
		if by[k] == nil {
			by[k] = &Clump{Rows: rows}
			order = append(order, k)
		}
		by[k].Cols = append(by[k].Cols, j)
	}
	out := []Clump{}
	for _, k := range order {
		if len(by[k].Cols) >= 2 {
			out = append(out, *by[k])
		}
	}
	return out
}

// crossings lists every block's dependencies on the others and whether it
// owns its writes.
func (b *build) crossings() {
	r := b.r
	for n := range r.Blocks {
		bl := &r.Blocks[n]
		if bl.Kind == Shared {
			continue
		}
		bl.Exclusive = true
		for i := range r.Rows {
			b.fieldCrossings(bl, n, i)
			if b.rowBlock[i] == n {
				b.callCrossings(bl, n, i)
			}
		}
	}
}

// fieldCrossings adds to bl, block n, the fields row i uses across its
// edge: other blocks' fields from inside, its own from outside. A write
// across makes the block not exclusive.
func (b *build) fieldCrossings(bl *Block, n, i int) {
	in := b.rowBlock[i] == n
	for j, u := range b.cell[i] {
		cb := b.r.Columns[j].Block
		if !u.any() || cb < 0 {
			continue
		}
		switch {
		case in && cb != n:
			bl.Crossings = append(bl.Crossings, Crossing{Row: i, Col: j, Callee: -1, Out: true, Write: u.wr})
			if u.wr {
				bl.Exclusive = false
			}
		case !in && cb == n:
			bl.Crossings = append(bl.Crossings, Crossing{Row: i, Col: j, Callee: -1, Write: u.wr})
			if u.wr {
				bl.Exclusive = false
			}
		}
	}
}

// callCrossings adds to bl, block n, the methods of other blocks that row
// i, one of its own, calls.
func (b *build) callCrossings(bl *Block, n, i int) {
	for _, k := range b.callees(i) {
		if kb := b.rowBlock[k]; kb >= 0 && kb != n {
			bl.Crossings = append(bl.Crossings, Crossing{Row: i, Col: -1, Callee: k, Out: true})
		}
	}
}

// sharedParam returns the parameter type every one of rows takes, or "".
func (b *build) sharedParam(rows []int) string {
	var common []string
	for n, i := range rows {
		ps := paramTypes(b.syms[i])
		if n == 0 {
			common = ps
			continue
		}
		common = slices.DeleteFunc(common, func(t string) bool { return !slices.Contains(ps, t) })
	}
	for _, t := range common {
		if t != "context.Context" && t != "error" && t != "string" && t != "int" && t != "bool" {
			return t
		}
	}
	return ""
}

// colNames joins the names of columns: "a, b and c".
func (b *build) colNames(cols []int) string {
	names := make([]string, len(cols))
	for n, j := range cols {
		names[n] = b.r.Columns[j].Name
	}
	return join(names, 0)
}

// colsIn names columns with their blocks: "items, coupons (A)".
func (b *build) colsIn(cols []int) string {
	var parts []string
	var group []string
	last := -2
	flush := func() {
		if len(group) > 0 {
			parts = append(parts, fmt.Sprintf("%s (%s)", join(group, 0), letterOr(last)))
		}
		group = nil
	}
	for _, j := range cols {
		bl := b.r.Columns[j].Block
		if bl != last {
			flush()
			last = bl
		}
		group = append(group, b.r.Columns[j].Name)
	}
	flush()
	return strings.Join(parts, ", ")
}

// rowNames joins the method names of rows, at most max of them (all for 0).
func (b *build) rowNames(rows []int, max int) string {
	names := make([]string, len(rows))
	for n, i := range rows {
		names[n] = b.syms[i].Name
	}
	return join(names, max)
}

func (b *build) letters(blocks []int) string {
	ls := make([]string, len(blocks))
	for n, x := range blocks {
		ls[n] = letter(x)
	}
	return join(ls, 0)
}

// join joins words as prose, "a, b and c", keeping the first max and
// counting the rest; all of them for max 0.
func join(words []string, max int) string {
	if max > 0 && len(words) > max {
		return strings.Join(words[:max], ", ") + fmt.Sprintf(" and %d more", len(words)-max)
	}
	switch len(words) {
	case 0:
		return ""
	case 1:
		return words[0]
	}
	return strings.Join(words[:len(words)-1], ", ") + " and " + words[len(words)-1]
}

func or(s, alt string) string {
	if s == "" {
		return alt
	}
	return s
}

// letter names block n: A to Z, then AA, AB, ...
func letter(n int) string {
	if n < 26 {
		return string(rune('A' + n))
	}
	return letter(n/26-1) + string(rune('A'+n%26))
}

func letterOr(n int) string {
	if n < 0 {
		return "hub"
	}
	return letter(n)
}

func fill(n, v int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func orEmpty(xs []int) []int {
	if xs == nil {
		return []int{}
	}
	return xs
}

// invert returns, for a permutation listing old indexes in new order, the
// new index of each old one.
func invert(order []int) []int {
	out := fill(len(order), -1)
	for n, old := range order {
		out[old] = n
	}
	return out
}

// remap maps indexes through at and sorts them.
func remap(xs, at []int) []int {
	if xs == nil {
		return nil
	}
	out := make([]int, len(xs))
	for n, x := range xs {
		out[n] = at[x]
	}
	slices.Sort(out)
	return out
}

// union is a union-find over 0..n-1.
type union []int

func newUnion(n int) union {
	u := make(union, n)
	for i := range u {
		u[i] = i
	}
	return u
}

func (u union) find(x int) int {
	for u[x] != x {
		u[x] = u[u[x]]
		x = u[x]
	}
	return x
}

func (u union) union(x, y int) {
	x, y = u.find(x), u.find(y)
	if x != y {
		u[max(x, y)] = min(x, y)
	}
}

// groups splits xs by set, ordered by their first member.
func (u union) groups(xs []int) [][]int {
	by := map[int][]int{}
	var roots []int
	for _, x := range xs {
		r := u.find(x)
		if by[r] == nil {
			roots = append(roots, r)
		}
		by[r] = append(by[r], x)
	}
	out := make([][]int, len(roots))
	for n, r := range roots {
		out[n] = by[r]
		slices.Sort(out[n])
	}
	slices.SortFunc(out, func(a, b []int) int { return cmp.Compare(a[0], b[0]) })
	return out
}
