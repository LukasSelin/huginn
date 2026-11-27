package cohesion

import (
	"fmt"
	"testing"

	"huginn/internal/ast"
)

// order is the God object of the affinity matrix's textbook example: an
// order that also prices itself, renders and mails its receipt and saves
// itself.
const order = `package shop

type Order struct {
	items   []Item
	coupons []Coupon
	taxRate float64
	tmpl    *template.Template
	smtp    *smtp.Client
	db      *sql.DB
	log     *slog.Logger
}

func (o *Order) AddItem(it Item) {
	o.items = append(o.items, it)
	o.log.Info("add")
}

func (o *Order) ApplyCoupon(c Coupon) {
	if len(o.items) == 0 {
		return
	}
	o.coupons = append(o.coupons, c)
	o.log.Info("coupon")
}

func (o *Order) Subtotal() int {
	n := 0
	for _, it := range o.items {
		n += it.Price
	}
	return n
}

func (o *Order) Discount() int {
	n := 0
	for range o.items {
		for _, c := range o.coupons {
			n += c.Off
		}
	}
	return n
}

func (o *Order) Total() float64 { return float64(o.Subtotal()-o.Discount()) * (1 + o.taxRate) }

func (o *Order) RenderReceipt() string {
	var b strings.Builder
	o.tmpl.Execute(&b, o.items)
	return b.String()
}

func (o *Order) SendReceipt(to string) error { return o.smtp.Send(to, o.RenderReceipt()) }

func (o *Order) Save() error {
	_, err := o.db.Exec("insert", o.items, o.coupons)
	return err
}

func (o *Order) Checkout(to string) error {
	o.log.Info("checkout")
	o.Total()
	if err := o.Save(); err != nil {
		return err
	}
	return o.SendReceipt(to)
}
`

// input parses src and returns the input for the type named typ.
func input(t *testing.T, src, typ string) Input {
	t.Helper()
	f, err := ast.ParseSource("shop/order.go", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	var in Input
	for _, s := range f.Symbols {
		switch {
		case s.Kind == ast.KindType && s.Name == typ:
			in.Type = s
		case s.Kind == ast.KindMethod && s.Recv == typ:
			in.Methods = append(in.Methods, s)
		}
	}
	if in.Type == nil {
		t.Fatalf("no type %s", typ)
	}
	return in
}

func check(t *testing.T, what string, got, want any) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("%s:\n got %v\nwant %v", what, got, want)
	}
}
