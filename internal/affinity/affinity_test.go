package affinity

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"
	"testing"
	"time"

	"huginn/internal/ast"
	"huginn/internal/lineage"
)

// fixture replays revisions given as path → source maps of the touched
// files, a day apart unless told otherwise; an empty source deletes the
// file.
type fixture struct {
	t    *testing.T
	h    *lineage.History
	tree map[string]*ast.File
	n    int
	at   time.Time
}

func newFixture(t *testing.T) *fixture {
	return &fixture{t: t, h: lineage.New(), tree: map[string]*ast.File{}, at: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
}

// commit applies one revision, diffing the touched files directory by
// directory as the git walker does.
func (f *fixture) commit(files map[string]string) {
	f.t.Helper()
	f.n++
	f.at = f.at.AddDate(0, 0, 1)
	old, cur := map[string][]*ast.File{}, map[string][]*ast.File{}
	for _, p := range slices.Sorted(maps.Keys(files)) {
		if !strings.HasSuffix(p, ".go") {
			continue // the history holds Go files only
		}
		dir := path.Dir(p)
		if x := f.tree[p]; x != nil {
			old[dir] = append(old[dir], x)
		}
		if files[p] == "" {
			delete(f.tree, p)
			continue
		}
		x, err := ast.ParseSource(p, []byte(files[p]))
		if err != nil {
			f.t.Fatalf("parse %s: %v", p, err)
		}
		f.tree[p] = x
		cur[dir] = append(cur[dir], x)
	}
	var changes []ast.Change
	dirs := slices.Sorted(maps.Keys(files))
	seen := map[string]bool{}
	for _, p := range dirs {
		dir := path.Dir(p)
		if seen[dir] {
			continue
		}
		seen[dir] = true
		changes = append(changes, ast.Diff(old[dir], cur[dir])...)
	}
	f.h.Apply(lineage.Revision{SHA: fmt.Sprintf("c%03d", f.n), Time: f.at, Author: "Ada", Changes: changes})
}

// skip moves the clock on, so the next commit comes that much later.
func (f *fixture) skip(d time.Duration) { f.at = f.at.Add(d) }

func (f *fixture) report(name string, opts Options) *Report {
	f.t.Helper()
	a := New(f.h, false)
	id, err := a.Lookup(name)
	if err != nil {
		f.t.Fatalf("Lookup(%q): %v", name, err)
	}
	r, err := a.Report(id, opts)
	if err != nil {
		f.t.Fatalf("Report(%q): %v", name, err)
	}
	return r
}

const orderSrc = `package order

import (
	"database/sql"
	"log"
	"strings"
	"text/template"
)

type Item struct {
	Name  string
	Price int
}

type Order struct {
	items   []Item
	coupons []string
	taxRate float64
	tmpl    *template.Template
	smtp    Mailer
	db      *sql.DB
	log     *log.Logger
}

func (o *Order) AddItem(it Item) {
	o.items = append(o.items, it)
	o.log.Printf("added %s", it.Name)
}

func (o *Order) ApplyCoupon(code string) {
	if len(o.items) == 0 {
		return
	}
	o.coupons = append(o.coupons, code)
	o.log.Printf("coupon %s", code)
}

func (o *Order) Subtotal() int {
	n := 0
	for _, it := range o.items {
		n += it.Price
	}
	return n
}

func (o *Order) Discount() int {
	return len(o.coupons) * len(o.items)
}

func (o *Order) Total() float64 {
	return float64(o.Subtotal()-o.Discount()) * (1 + o.taxRate)
}

func (o *Order) RenderReceipt() string {
	var b strings.Builder
	o.tmpl.Execute(&b, o.items)
	return b.String() + fmtTotal(o.Total())
}

func (o *Order) SendReceipt(to string) error {
	return o.smtp.Send(to, o.RenderReceipt())
}

func (o *Order) Save() error {
	_, err := o.db.Exec("insert", o.items, o.coupons)
	return err
}

func (o *Order) Checkout(to string) error {
	o.log.Printf("checkout %v", o.Total())
	if err := o.Save(); err != nil {
		return err
	}
	return o.SendReceipt(to)
}
`

const mailSrc = `package order

import "fmt"

type Mailer interface {
	Send(to, body string) error
}

func fmtTotal(t float64) string { return fmt.Sprint(t) }
`

// layout renders a report's blocks and placements compactly.
func layout(r *Report) string {
	var s strings.Builder
	for _, bl := range r.Blocks {
		var cols, rows []string
		for _, j := range bl.Columns {
			cols = append(cols, r.Columns[j].Name)
		}
		for _, i := range bl.Rows {
			rows = append(rows, r.Rows[i].Method)
		}
		fmt.Fprintf(&s, "%s %s %s {%s; %s}\n", bl.Letter, bl.Kind, bl.Label, strings.Join(cols, ", "), strings.Join(rows, ", "))
	}
	for _, row := range r.Rows {
		if row.Kind != Member {
			fmt.Fprintf(&s, "%s %s\n", row.Kind, row.Method)
		}
	}
	for _, c := range r.Columns {
		if c.Block < 0 {
			fmt.Fprintf(&s, "%s %s hub=%v\n", c.Class, c.Name, c.Hub)
		}
	}
	return s.String()
}

func row(t *testing.T, r *Report, method string) Row {
	t.Helper()
	for _, x := range r.Rows {
		if x.Method == method {
			return x
		}
	}
	t.Fatalf("no row %s", method)
	return Row{}
}

func col(t *testing.T, r *Report, name string) (int, Column) {
	t.Helper()
	for j, c := range r.Columns {
		if c.Name == name {
			return j, c
		}
	}
	t.Fatalf("no column %s", name)
	return 0, Column{}
}

func cell(t *testing.T, r *Report, method, field string) string {
	t.Helper()
	j, _ := col(t, r, field)
	for _, c := range row(t, r, method).Cells {
		if c.Col == j {
			s := ""
			if c.Read {
				s += "R"
			}
			if c.Write {
				s += "W"
			}
			return s
		}
	}
	return ""
}

func findings(r *Report, kind string) []Finding {
	var out []Finding
	for _, f := range r.Findings {
		if f.Kind == kind {
			out = append(out, f)
		}
	}
	return out
}

func TestOrderMatrix(t *testing.T) {
	f := newFixture(t)
	f.commit(map[string]string{"go.mod": "", "order/order.go": orderSrc, "order/mail.go": mailSrc})
	r := f.report("Order", Options{})

	want := `A core core {items, coupons; AddItem, ApplyCoupon, Subtotal, Discount}
B service taxRate {taxRate; Total}
C service receipt {tmpl, smtp; RenderReceipt, SendReceipt}
D service db {db; Save}
bridge Checkout
infra log hub=false
`
	if got := layout(r); got != want {
		t.Errorf("layout:\n%s\nwant:\n%s", got, want)
	}
	for name, class := range map[string]Class{"items": Entity, "coupons": Entity, "taxRate": Config, "tmpl": Collaborator, "smtp": Collaborator, "db": Collaborator, "log": Infra} {
		if _, c := col(t, r, name); c.Class != class {
			t.Errorf("%s is %s (%s), want %s", name, c.Class, c.Reason, class)
		}
	}
	for _, tc := range []struct{ method, field, want string }{
		{"AddItem", "items", "RW"}, {"ApplyCoupon", "coupons", "RW"}, {"ApplyCoupon", "items", "R"},
		{"Subtotal", "items", "R"}, {"SendReceipt", "smtp", "R"}, {"Checkout", "log", "R"}, {"Total", "items", ""},
	} {
		if got := cell(t, r, tc.method, tc.field); got != tc.want {
			t.Errorf("%s × %s = %q, want %q", tc.method, tc.field, got, tc.want)
		}
	}
	for method, want := range map[string]string{
		"AddItem": "writes items", "Subtotal": "reads items", "Discount": "reads items and coupons",
		"Total": "uses taxRate", "RenderReceipt": "uses tmpl", "Checkout": "coordinates B, C and D",
	} {
		if got := row(t, r, method).Reason; got != want {
			t.Errorf("%s reason %q, want %q", method, got, want)
		}
	}
	if notes := row(t, r, "Save").Notes; !slices.Equal(notes, []string{"reads items and coupons (A): take them as parameters"}) {
		t.Errorf("Save notes %q", notes)
	}
	if notes := row(t, r, "RenderReceipt").Notes; !slices.Equal(notes, []string{"reads items (A): take them as parameters"}) {
		t.Errorf("RenderReceipt notes %q", notes)
	}
	if got := r.Blocks[2].Reason; !strings.Contains(got, "RenderReceipt only called from SendReceipt") {
		t.Errorf("C reason %q, want the helper merge", got)
	}
	var calls []string
	for _, c := range row(t, r, "Total").Calls {
		calls = append(calls, r.Rows[c.Row].Method)
	}
	if !slices.Equal(calls, []string{"Subtotal", "Discount"}) {
		t.Errorf("Total calls %v", calls)
	}
	if r.LCOM4 != 1 || r.Split != 4 {
		t.Errorf("LCOM4 %d → Split %d, want 1 → 4", r.LCOM4, r.Split)
	}
	if len(findings(r, "bridge")) != 1 || len(findings(r, "glue")) != 0 {
		t.Errorf("findings %+v", r.Findings)
	}

	// Building twice gives the same document.
	again := f.report("Order", Options{})
	x, _ := json.Marshal(r)
	y, _ := json.Marshal(again)
	if string(x) != string(y) {
		t.Error("two builds differ")
	}
	if strings.Contains(string(x), ":null") {
		t.Errorf("null in JSON: %s", x)
	}
}

func TestGlue(t *testing.T) {
	f := newFixture(t)
	src := orderSrc + `
func (o *Order) Load(id int) error {
	rows, err := o.db.Query("select", id)
	o.items = scan(rows)
	return err
}
`
	f.commit(map[string]string{"order/order.go": src, "order/mail.go": mailSrc + "\nfunc scan(any) []Item { return nil }\n"})
	r := f.report("Order", Options{})
	load := row(t, r, "Load")
	if load.Block != 0 || load.Reason != "writes items" {
		t.Errorf("Load in %d: %s", load.Block, load.Reason)
	}
	g := findings(r, "glue")
	if len(g) != 1 || g[0].Text != "Load writes items (A) and uses db (D)" {
		t.Errorf("glue %+v", g)
	}
}

func TestHubsAndFree(t *testing.T) {
	f := newFixture(t)
	f.commit(map[string]string{"srv/srv.go": `package srv

type Options struct{ Root string }
type index struct{ n int }

type Server struct {
	opts  Options
	count int
	cache map[string]string
}

func (s *Server) A() string { return s.opts.Root }
func (s *Server) B() string { return s.opts.Root + "b" }
func (s *Server) C() string { return s.opts.Root + s.cache["c"] }
func (s *Server) D() { s.count++; _ = s.opts }
func (s *Server) E() string { return s.A() }
func (s *Server) F(ix *index) int { return ix.n }
func (s *Server) G(ix *index, x int) int { return ix.n + x }
func (s *Server) H() string { return s.opts.Root + "h" }
`})
	r := f.report("Server", Options{})
	if _, c := col(t, r, "opts"); !c.Hub {
		t.Errorf("opts is no hub: %+v", c)
	}
	want := `A core core {count; D}
B service cache {cache; C}
C shared needs only opts {; A, B, E, H}
free F
free G
hub=true
`
	got := layout(r)
	got = strings.Replace(got, "config opts hub=true", "hub=true", 1)
	if got != want {
		t.Errorf("layout:\n%s\nwant:\n%s", got, want)
	}
	if e := row(t, r, "E"); e.Reason != "calls only A (C)" {
		t.Errorf("E reason %q", e.Reason)
	}
	free := findings(r, "free")
	if len(free) != 1 || !strings.Contains(free[0].Text, "all take *index") {
		t.Errorf("free %+v", free)
	}
}

func TestMutators(t *testing.T) {
	f := newFixture(t)
	f.commit(map[string]string{"q/q.go": `package q

import "sync"
import "sync/atomic"

type Queue struct {
	sync.Mutex
	n     atomic.Int64
	done  chan struct{}
	items []int
}

func (q *Queue) Push(x int) {
	q.Lock()
	defer q.Unlock()
	q.items = append(q.items, x)
	q.n.Add(1)
}

func (q *Queue) Len() int64 { return q.n.Load() }

func (q *Queue) Close() { close(q.done) }

func (q *Queue) Wait() { <-q.done }
`})
	r := f.report("Queue", Options{})
	for _, tc := range []struct{ method, field, want string }{
		{"Push", "n", "W"}, {"Len", "n", "R"}, {"Close", "done", "W"}, {"Wait", "done", "R"}, {"Push", "Mutex", "R"},
	} {
		if got := cell(t, r, tc.method, tc.field); got != tc.want {
			t.Errorf("%s × %s = %q, want %q", tc.method, tc.field, got, tc.want)
		}
	}
	for name, class := range map[string]Class{"Mutex": Infra, "n": Entity, "done": Entity, "items": Entity} {
		if _, c := col(t, r, name); c.Class != class {
			t.Errorf("%s is %s, want %s", name, c.Class, class)
		}
	}
	if r.Unattributed != 0 {
		t.Errorf("unattributed %d", r.Unattributed)
	}
}

func TestSharedState(t *testing.T) {
	f := newFixture(t)
	f.commit(map[string]string{"c/c.go": `package c

type Client struct {
	state int
	conns []int
	queue []string
}

func (c *Client) Dial()        { c.conns = append(c.conns, 1); c.state = 1 }
func (c *Client) Hangup()      { c.conns = nil }
func (c *Client) Enqueue(s string) { c.queue = append(c.queue, s); c.state = 2 }
func (c *Client) Drain()       { c.queue = nil }
`})
	r := f.report("Client", Options{})
	want := `A core core {state, conns; Dial, Hangup}
B state queue {queue; Enqueue, Drain}
`
	if got := layout(r); got != want {
		t.Errorf("layout:\n%s\nwant:\n%s", got, want)
	}
	s := findings(r, "sharedState")
	if len(s) != 1 || s[0].Text != "state is state of two blocks, written by Dial (A) and Enqueue (B)" {
		t.Errorf("shared state %+v", s)
	}
	if r.Blocks[1].Exclusive {
		t.Error("B owns its writes, though Enqueue writes state")
	}
}

func TestTemporary(t *testing.T) {
	f := newFixture(t)
	f.commit(map[string]string{"p/p.go": `package p

type Parser struct {
	src []byte
	pos int
	out []string
}

func (p *Parser) Parse() []string {
	p.pos = 0
	for p.more() {
		p.out = append(p.out, p.next())
	}
	return p.out
}

func (p *Parser) more() bool   { return p.pos < len(p.src) }
func (p *Parser) next() string { p.pos++; return string(p.src[p.pos-1]) }
func (p *Parser) Reset()       { p.out = nil }
`})
	r := f.report("Parser", Options{})
	if _, c := col(t, r, "pos"); c.Class != Temporary {
		t.Errorf("pos is %s (%s)", c.Class, c.Reason)
	}
	if len(findings(r, "temporary")) != 1 {
		t.Errorf("findings %+v", r.Findings)
	}
}

func TestCoupling(t *testing.T) {
	f := newFixture(t)
	edit := func(src, method, body string) string {
		i := strings.Index(src, "func (o *Order) "+method)
		j := i + strings.Index(src[i:], "\n}\n")
		return src[:j] + "\n\t" + body + src[j:]
	}
	cartSrc := func(n int) string {
		return fmt.Sprintf("package cart\n\nimport \"example.com/m/order\"\n\nfunc Add(o *order.Order) { o.AddItem(order.Item{Price: %d}) }\n", n)
	}
	cart := cartSrc(-1)
	src := orderSrc
	f.commit(map[string]string{"go.mod": "module example.com/m\n", "order/order.go": src, "order/mail.go": mailSrc, "cart/cart.go": cart})
	f.skip(400 * 24 * time.Hour)
	for n := range 3 {
		src = edit(src, "Discount", fmt.Sprintf("_ = %d", n))
		src = edit(src, "RenderReceipt", fmt.Sprintf("_ = %d", n))
		f.commit(map[string]string{"order/order.go": src})
	}
	for n := range 2 {
		src = edit(src, "AddItem", fmt.Sprintf("_ = %d", n))
		cart = cartSrc(n)
		f.commit(map[string]string{"order/order.go": src, "cart/cart.go": cart})
	}
	// A receiver rename changes every method that uses the receiver.
	f.skip(60 * 24 * time.Hour)
	f.commit(map[string]string{"order/order.go": strings.ReplaceAll(strings.ReplaceAll(src, "(o *Order)", "(x *Order)"), "o.", "x.")})

	r := f.report("Order", Options{Months: 12})
	if r.Coupling.MassEdits != 1 || r.Coupling.Commits != 5 {
		t.Errorf("coupling %d commits, %d mass edits; want 5, 1", r.Coupling.Commits, r.Coupling.MassEdits)
	}
	var cross *Pair
	for k, p := range r.Coupling.Pairs {
		if p.Strong && p.Cross {
			if cross != nil {
				t.Errorf("second strong cross pair %+v", p)
			}
			cross = &r.Coupling.Pairs[k]
		}
	}
	if cross == nil || cross.N != 3 || cross.Kind != "boundary" || r.Rows[cross.A].Method+r.Rows[cross.B].Method != "DiscountRenderReceipt" {
		t.Fatalf("cross pair %+v", cross)
	}
	if !strings.Contains(cross.Text, "RenderReceipt reaches Discount through its calls") {
		t.Errorf("text %q", cross.Text)
	}
	if e := row(t, r, "Discount").Edits; e != 3 {
		t.Errorf("Discount edits %d, want 3 (birth and mass edit left out)", e)
	}
	core := r.Blocks[0]
	if len(core.Outside) != 1 || core.Outside[0].Name != "Add" || core.Outside[0].N != 2 {
		t.Errorf("core outside %+v", core.Outside)
	}
	notes := slices.IndexFunc(r.Notes, func(n Note) bool { return n.Kind == "gitTogether" })
	if notes < 0 {
		t.Errorf("no gitTogether note: %+v", r.Notes)
	}

	// A window of a month keeps only the rename.
	if r := f.report("Order", Options{Months: 1}); r.Coupling.Commits != 0 || r.Coupling.MassEdits != 1 {
		t.Errorf("a month: %d commits, %d mass edits", r.Coupling.Commits, r.Coupling.MassEdits)
	}
}

func TestClients(t *testing.T) {
	f := newFixture(t)
	f.commit(map[string]string{
		"go.mod":         "module example.com/m\n",
		"order/order.go": orderSrc + "\nfunc (o *Order) String() string { return \"order\" }\n\nfunc (o *Order) Items() []Item { return o.items }\n",
		"order/mail.go":  mailSrc,
		"order/report.go": `package order

func report(o *Order) int { return o.Subtotal() - o.Discount() }
`,
		"api/cart/cart.go": `package cart

import "example.com/m/order"

func Add(o *order.Order, it order.Item) int {
	o.AddItem(it)
	return o.Subtotal()
}
`,
		"api/checkout/checkout.go": `package checkout

import "example.com/m/order"

func Pay(o *order.Order) (float64, error) {
	return o.Total(), o.Checkout("me")
}

func Count(o *order.Order) int { return len(o.Items()) }
`,
		"jobs/receipts/receipts.go": `package receipts

import "example.com/m/order"

func Run(o *order.Order) func(string) error {
	var _ order.Item
	return o.SendReceipt
}
`,
	})
	r := f.report("Order", Options{})
	got := map[string]string{}
	for _, c := range r.Clients.List {
		var uses []string
		for _, u := range c.Uses {
			uses = append(uses, r.Rows[u.Row].Method)
		}
		got[c.Name] = strings.Join(uses, ",") + " | " + c.Text
	}
	want := map[string]string{
		"example.com/m/api/cart":      "AddItem,Subtotal | uses only A (core)",
		"example.com/m/api/checkout":  "Items,Total,Checkout | spans A and B: a use case; give it a facade rather than splitting along its lines",
		"example.com/m/jobs/receipts": "SendReceipt | uses only C (receipt)",
		"order/report.go":             "Subtotal,Discount | uses only A (core)",
	}
	if !maps.Equal(got, want) {
		t.Errorf("clients:\n got %q\nwant %q", got, want)
	}
	var iface string
	for _, c := range r.Clients.List {
		if c.Name == "example.com/m/api/cart" {
			iface = c.Interface
		}
	}
	if want := "type orderAPI interface {\n\tAddItem(it order.Item)\n\tSubtotal() int\n}"; iface != want {
		t.Errorf("interface:\n%s\nwant:\n%s", iface, want)
	}
	if len(r.Clients.Roles) != 3 {
		t.Errorf("roles %+v", r.Clients.Roles)
	}
	var uncalled []string
	for _, u := range r.Clients.Uncalled {
		uncalled = append(uncalled, r.Rows[u.Row].Method+": "+u.Why)
	}
	if !slices.Equal(uncalled, []string{"ApplyCoupon: exported: may be used outside the repository", "String: may implement fmt.Stringer"}) {
		t.Errorf("uncalled %q", uncalled)
	}
	g := findings(r, "getter")
	if len(g) != 1 || len(g[0].Callers) != 1 || g[0].Callers[0].Name != "Count" {
		t.Errorf("getter %+v", g)
	}
}

func TestNames(t *testing.T) {
	f := newFixture(t)
	f.commit(map[string]string{"b/b.go": `package b

type Billing struct {
	billingName   string
	billingStreet string

	mode    string
	verbose bool
}

func (b *Billing) BillingValidate() bool { return b.billingName != "" && b.billingStreet != "" && b.verbose }
func (b *Billing) Describe() string { return b.mode + b.billingName + b.billingStreet }
`})
	r := f.report("Billing", Options{})
	if len(r.Names.Words) == 0 || r.Names.Words[0].Word != "billing" || len(r.Names.Words[0].Cols) != 2 || len(r.Names.Words[0].Rows) != 1 {
		t.Errorf("words %+v", r.Names.Words)
	}
	if len(r.Names.Clumps) != 1 || len(r.Names.Clumps[0].Cols) != 2 {
		t.Errorf("clumps %+v", r.Names.Clumps)
	}
	var flags []string
	for _, fl := range r.Names.Flags {
		flags = append(flags, r.Columns[fl.Col].Name)
	}
	slices.Sort(flags)
	if !slices.Equal(flags, []string{"mode", "verbose"}) {
		t.Errorf("flags %v", flags)
	}
	if len(r.Names.Groups) != 2 {
		t.Errorf("groups %+v", r.Names.Groups)
	}
	for _, tc := range []struct{ in, want string }{
		{"detailURLs", "detail url"}, {"HTTPServer", "http server"}, {"getItems", "item"}, {"status", "status"}, {"uiEncoded", "ui encoded"},
	} {
		if got := strings.Join(words(tc.in), " "); got != tc.want {
			t.Errorf("words(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestLookup(t *testing.T) {
	f := newFixture(t)
	f.commit(map[string]string{
		"go.mod": "module x\n",
		"a/a.go": "package a\n\ntype Server struct{ n int }\n\nfunc (s *Server) Run() { s.n++ }\n",
		"b/b.go": "package b\n\ntype Server struct{}\n",
		"c/c.go": "package c\n\nimport (\n\t\"x/a\"\n\t\"x/b\"\n)\n\nfunc use() { _, _ = a.Server{}, b.Server{} }\n",
		"a/x.go": "package a\n\ntype Point int\n",
	})
	a := New(f.h, false)
	var ambiguous *AmbiguousError
	if _, err := a.Lookup("Server"); !errors.As(err, &ambiguous) || !slices.Equal(ambiguous.Matches, []string{"a.Server", "b.Server"}) {
		t.Errorf("Lookup(Server) error %v", err)
	}
	want, err := a.Lookup("a.Server")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"x/a.Server", "(*Server).Run", "Server.Run"} {
		if id, err := a.Lookup(name); err != nil || id != want {
			t.Errorf("Lookup(%q) = %d, %v; want %d", name, id, err, want)
		}
	}
	if _, err := a.Lookup("Nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Lookup(Nope) error %v", err)
	}
	if r, err := a.Report(want, Options{}); err != nil || len(r.Rows) != 1 {
		t.Errorf("Report: %v", err)
	}
	if _, err := a.Report(f.h.Lookup("a/a.go", "Server.Run").ID, Options{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("Report of a method: %v", err)
	}
	if id, err := a.Lookup("Point"); err != nil {
		t.Errorf("Lookup(Point): %v", err)
	} else if r, err := a.Report(id, Options{}); err != nil || r.Struct || len(r.Columns) != 0 {
		t.Errorf("Report(Point) %+v, %v", r, err)
	}
}

func TestCandidates(t *testing.T) {
	f := newFixture(t)
	f.commit(map[string]string{
		"order/order.go": orderSrc, "order/mail.go": mailSrc,
		"geo/geo.go": "package geo\n\ntype Point struct{ X, Y int }\n\nfunc (p *Point) Move() { p.X++; p.Y++ }\n\ntype Empty struct{ n int }\n",
	})
	cs := New(f.h, false).Candidates(Options{})
	var names []string
	for _, c := range cs {
		names = append(names, fmt.Sprintf("%s %d/%d split %d", c.Name, c.Methods, c.Fields, c.Split))
	}
	if !slices.Equal(names, []string{"Order 9/7 split 4", "Point 1/2 split 1"}) {
		t.Errorf("candidates %q", names)
	}
}

func TestSteps(t *testing.T) {
	f := newFixture(t)
	src := orderSrc + "\nfunc (o *Order) Format(x int) string { return fmtTotal(float64(x)) }\n"
	f.commit(map[string]string{"order/order.go": src, "order/mail.go": mailSrc})
	src = strings.Replace(src, "n := 0", "n := 1", 1)
	f.commit(map[string]string{"order/order.go": strings.Replace(src, "(1 + o.taxRate)", "(2 + o.taxRate)", 1)})
	r := f.report("Order", Options{})
	var kinds []string
	for _, s := range r.Steps {
		kinds = append(kinds, s.Kind+" "+letterOr(s.Block))
	}
	// B changed, so it goes first among the exclusive blocks.
	if want := []string{"free hub", "extract B", "extract C", "extract D", "remains A"}; !slices.Equal(kinds, want) {
		t.Errorf("steps %q, want %q", kinds, want)
	}
}
