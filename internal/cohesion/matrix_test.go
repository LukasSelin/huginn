package cohesion

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"huginn/internal/ast"
)

func fieldsOf(r *Report) []string {
	var out []string
	for _, f := range r.Fields {
		s := f.Name + ":" + f.Role
		if f.Hub {
			s += ":hub"
		}
		if f.Block != "" {
			s += ":" + f.Block
		}
		out = append(out, s)
	}
	return out
}

func rowsOf(r *Report) string {
	var rows []string
	for _, m := range r.Methods {
		var cells []string
		for _, u := range m.Uses {
			c := ""
			if u.Read {
				c += "R"
			}
			if u.Write {
				c += "W"
			}
			cells = append(cells, r.Fields[u.Field].Name+"="+c)
		}
		rows = append(rows, fmt.Sprintf("%s %s/%s %v %v", m.Name, m.Kind, m.Block, cells, m.Calls))
	}
	return strings.Join(rows, "\n")
}

func blocksOf(r *Report) []string {
	var out []string
	for _, b := range r.Blocks {
		out = append(out, fmt.Sprintf("%s %s %v %v", b.Label, b.Kind, b.Fields, b.Methods))
	}
	return out
}

func notesOf(ns []Note) string {
	var out []string
	for _, n := range ns {
		w := "info"
		if n.Warn {
			w = "warn"
		}
		out = append(out, w+": "+n.Text)
	}
	return strings.Join(out, "\n")
}

// TestMatrix reads the textbook example as the technique does: items, used
// by most rows, is a hub and log is infrastructure; Save's reads of the
// coupons do not tie the repository to the order; RenderReceipt and
// SendReceipt share a block through the call between them; Checkout, which
// only calls, is a bridge.
func TestMatrix(t *testing.T) {
	r := Analyze(input(t, order, "Order"))
	check(t, "columns", fieldsOf(r), []string{
		"coupons:entity:A", "tmpl:collaborator:B", "smtp:collaborator:B", "taxRate:policy:C", "db:collaborator:D",
		"items:entity:hub:core", "log:infrastructure",
	})
	check(t, "rows", rowsOf(r), strings.Join([]string{
		"ApplyCoupon /A [coupons=RW items=R log=R] []",
		"Discount /A [coupons=R items=R] []",
		"RenderReceipt /B [tmpl=R items=R] []",
		"SendReceipt /B [smtp=R] [RenderReceipt]",
		"Total /C [taxRate=R] [Discount Subtotal]",
		"Save /D [coupons=R db=R items=R] []",
		"AddItem core/core [items=RW log=R] []",
		"Subtotal core/core [items=R] []",
		"Checkout bridge/ [log=R] [Save SendReceipt Total]",
	}, "\n"))
	// The core order, a receipt mailer, a pricer and a repository.
	check(t, "blocks", blocksOf(r), []string{
		"A entity [coupons] [ApplyCoupon Discount]",
		"B adapter [tmpl smtp] [RenderReceipt SendReceipt]",
		"C policy [taxRate] [Total]",
		"D adapter [db] [Save]",
		"core core [items] [AddItem Subtotal]",
	})
	if r.LCOM4 != 1 || r.LCOM4Split != 3 {
		t.Errorf("LCOM4 = %d raw, %d split; want 1 and 3", r.LCOM4, r.LCOM4Split)
	}
	if len(r.Ties)+len(r.SharedWrites) > 0 {
		t.Errorf("ties %v, shared writes %v", r.Ties, r.SharedWrites)
	}
	check(t, "start", r.Start, &Start{Block: "B", Reason: "it owns its writes exclusively."})
	check(t, "word of B", r.Blocks[1].Word, "receipt")
	check(t, "advice", r.Advice, []string{"LCOM4 is 1 only through hubs and orchestration: without them the type falls into 3 groups."})
}

func TestSpanningRows(t *testing.T) {
	before := Analyze(input(t, order, "Order"))
	// PriceTag uses the pricer's tax rate and the mailer's template, and
	// MailCoupon writes the coupons and mails: each spans two blocks.
	after := Analyze(input(t, order+`
func (o *Order) PriceTag() string { return fmt.Sprint(o.taxRate, o.tmpl) }

func (o *Order) MailCoupon(to string) {
	o.coupons = nil
	o.smtp.Send(to, "coupon")
}
`, "Order"))
	check(t, "ties", after.Ties, []Tie{{Method: "PriceTag", Stmts: 1, Blocks: []string{"B", "C"}}, {Method: "MailCoupon", Stmts: 2, Blocks: []string{"A", "B"}}})
	check(t, "notes", notesOf(Compare(before, after)), strings.Join([]string{
		"warn: PriceTag now spans blocks B {tmpl, smtp} and C {taxRate}: split it with Extract Method until each piece falls in one block, before splitting the type.",
		"warn: MailCoupon now spans blocks A {coupons} and B {tmpl, smtp}: split it with Extract Method until each piece falls in one block, before splitting the type.",
	}, "\n"))
	check(t, "new type", notesOf(Compare(nil, after)), strings.Join([]string{
		"info: New type: 7 fields, 11 methods, 4 blocks.",
		"warn: PriceTag spans blocks B {tmpl, smtp} and C {taxRate}: split it before the type grows.",
		"warn: MailCoupon spans blocks A {coupons} and B {tmpl, smtp}: split it before the type grows.",
	}, "\n"))
	check(t, "removed", notesOf(Compare(before, nil)), "info: The type is removed.")
}

func TestJoinedBlocks(t *testing.T) {
	before := Analyze(input(t, order, "Order"))
	// Two methods using the tax rate with the template join the pricer and
	// the mailer: neither alone holds them together.
	after := Analyze(input(t, order+`
func (o *Order) PriceTag() string { return fmt.Sprint(o.taxRate, o.tmpl) }

func (o *Order) PriceLine() string { return fmt.Sprint(o.tmpl, o.taxRate) }
`, "Order"))
	check(t, "notes", notesOf(Compare(before, after)), strings.Join([]string{
		"warn: Joins blocks that were apart: {taxRate} and {tmpl, smtp} are now one block, A, through PriceTag, PriceLine.",
		"info: Blocks: 3, was 4. LCOM4 without hubs and bridges: 2, was 3.",
		"info: New method PriceTag goes to block A {taxRate, tmpl, smtp}.",
		"info: New method PriceLine goes to block A {taxRate, tmpl, smtp}.",
	}, "\n"))
}

func TestSharedWrites(t *testing.T) {
	// The technique's own example: status is set both by ApplyCoupon and by
	// SendReceipt, so two blocks change it. That is no reason to join them.
	src := strings.Replace(order, "\to.coupons = append(o.coupons, c)\n", "\to.coupons = append(o.coupons, c)\n\to.status = 1\n", 1)
	src = strings.Replace(src, "\tlog     *slog.Logger\n", "\tlog     *slog.Logger\n\tstatus  int\n", 1)
	src = strings.Replace(src, "func (o *Order) SendReceipt(to string) error { return o.smtp.Send(to, o.RenderReceipt()) }",
		"func (o *Order) SendReceipt(to string) error {\n\to.status = 2\n\treturn o.smtp.Send(to, o.RenderReceipt())\n}", 1)
	r := Analyze(input(t, src, "Order"))
	check(t, "shared writes", r.SharedWrites, []SharedWrite{{Field: "status", Writers: []string{"ApplyCoupon", "SendReceipt"}, Blocks: []string{"A", "B"}}})
	check(t, "blocks", blocksOf(r)[:2], []string{
		"A entity [coupons] [ApplyCoupon Discount]",
		"B adapter [tmpl smtp] [RenderReceipt SendReceipt]",
	})
	if len(r.Ties) > 0 {
		t.Errorf("ties = %v; the shared column joins the blocks, not a row", r.Ties)
	}

	// A hub written from two blocks.
	r = Analyze(input(t, `package bank

type Account struct {
	status  int
	balance int
	history []string
	email   string
}

func (a *Account) Deposit(n int)  { _ = a.status; a.balance += n }
func (a *Account) Withdraw(n int) { _ = a.status; a.balance -= n }
func (a *Account) Freeze()        { a.status = 2; _ = a.balance }
func (a *Account) Record(s string) { _ = a.status; a.history = append(a.history, s) }
func (a *Account) History() []string { _ = a.status; return a.history }
func (a *Account) Notify()        { a.status = 3; send(a.email) }
`, "Account"))
	check(t, "hub written from two blocks", r.SharedWrites, []SharedWrite{{Field: "status", Writers: []string{"Freeze", "Notify"}, Blocks: []string{"A", "C"}}})
}

func TestTemporary(t *testing.T) {
	r := Analyze(input(t, `package parse

type Parser struct {
	src string
	pos int
	out []string
}

func (p *Parser) Parse() []string {
	p.pos, p.out = 0, nil
	for p.pos < len(p.src) {
		p.step()
	}
	return p.out
}

func (p *Parser) step() {
	p.out = append(p.out, p.src[p.pos:p.pos+1])
	p.pos++
}

func (p *Parser) Source() string { return p.src }
`, "Parser"))
	var got []string
	for _, f := range r.Fields {
		got = append(got, f.Name+":"+f.Role+":"+f.Operation)
	}
	slices.Sort(got)
	// pos and out live only during one Parse: a method object for it.
	check(t, "columns", got, []string{"out:temporary:Parse", "pos:temporary:Parse", "src:policy:"})
}

func TestClients(t *testing.T) {
	in := input(t, order, "Order")
	uses := map[string][]Client{
		"AddItem":       {{Package: "api/cart", Calls: 2}},
		"ApplyCoupon":   {{Package: "api/cart", Calls: 1}},
		"Total":         {{Package: "api/cart", Calls: 1}, {Package: "api/checkout", Calls: 1}, {Package: "admin/reports", Calls: 3}},
		"Save":          {{Package: "api/checkout", Calls: 1}, {Package: "shop", Test: true, Calls: 4}},
		"RenderReceipt": {{Package: "jobs/receipts", Calls: 1}},
	}
	in.Clients = func(m *ast.Symbol) []Client { return uses[m.Name] }
	r := Analyze(in)

	check(t, "clients", r.Clients, []ClientRow{
		{Package: "admin/reports", Methods: []string{"Total"}, Calls: 3},
		{Package: "api/cart", Methods: []string{"AddItem", "ApplyCoupon", "Total"}, Calls: 4},
		{Package: "api/checkout", Methods: []string{"Total", "Save"}, Calls: 2},
		{Package: "jobs/receipts", Methods: []string{"RenderReceipt"}, Calls: 1},
	})
	// Total, which most clients use, does not tie them into one role.
	if len(r.Roles) != 4 {
		t.Errorf("roles = %+v, want one per client", r.Roles)
	}
	cart := r.Roles[slices.IndexFunc(r.Roles, func(x Role) bool { return x.Clients[0] == "api/cart" })]
	check(t, "cart's blocks", cart.Blocks, []string{"A", "C", "core"})
	check(t, "unused", r.Unused, []string{"Subtotal", "Discount", "SendReceipt", "Checkout"})

	// The receipts job uses the mailer alone: the callers split as the
	// columns do.
	mailer := r.Blocks[1]
	i := slices.IndexFunc(mailer.Evidence, func(e Evidence) bool { return e.Signal == "clients" })
	if mailer.Verdict != "extract" || i < 0 || mailer.Evidence[i].Text != "jobs/receipts uses only this block: the callers split the same way." {
		t.Errorf("mailer: %s %+v", mailer.Verdict, mailer.Evidence)
	}
	check(t, "start", r.Start, &Start{Block: "B", Reason: "it owns its writes exclusively, and its callers split the same way."})
}

func TestCoupling(t *testing.T) {
	in := input(t, order, "Order")
	in.Changes = map[string][]string{
		"Discount":      {"c1", "c2", "c3", "c4"},
		"RenderReceipt": {"c1", "c2", "c3", "c5"},
		"ApplyCoupon":   {"c1", "c6", "c7"},
		"Total":         {"c8", "c9", "c10"},
	}
	r := Analyze(in)
	check(t, "coupled", r.Coupled, []Coupling{
		{A: "Discount", B: "RenderReceipt", Together: 3, AChanges: 4, BChanges: 4, Across: true},
	})
	// The discount and the receipt change together: a discount breakdown
	// passed between them may be missing, so check before splitting.
	mailer := r.Blocks[1]
	if mailer.Verdict != "check" || mailer.Changes != 4 {
		t.Errorf("mailer: verdict %s, %d changes", mailer.Verdict, mailer.Changes)
	}
	check(t, "start", r.Start, &Start{Block: "B", Reason: "it owns its writes exclusively and changes most often (4 commits)."})
}

func TestNaming(t *testing.T) {
	check(t, "words", [][]string{words("HTTPServer"), words("billing_name"), words("taxRate2Pct"), words("ID")},
		[][]string{{"http", "server"}, {"billing", "name"}, {"tax", "rate2", "pct"}, {"id"}})

	r := Analyze(input(t, `package shop

type Customer struct {
	billingName, billingStreet, billingCity string
	shippingName string
	status       int
	isGuest      bool
	id           int
}

func (c *Customer) ValidateBilling() bool { return c.billingName != "" && c.billingStreet != "" }
func (c *Customer) Ship() string { return c.shippingName + c.billingCity }
func (c *Customer) ID() int { return c.id }
`, "Customer"))
	check(t, "words", r.Words, []Word{{Word: "billing", Members: []string{"billingName", "billingStreet", "billingCity", "ValidateBilling"}}})
	check(t, "flags", r.Flags, []string{"status", "isGuest"})
}
