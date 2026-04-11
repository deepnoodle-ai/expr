// Env shapes: one expression read against a struct, a map, and a
// pointer-to-struct wrapped view. The surface the expression sees is
// driven entirely by the env you pass to Run.
package main

import (
	"context"
	"fmt"

	"github.com/deepnoodle-ai/expr"
)

type LineItem struct {
	SKU   string
	Price float64
}

type Order struct {
	ID    string
	Items []LineItem
	Meta  map[string]any
}

func (o *Order) Subtotal() float64 {
	var s float64
	for _, it := range o.Items {
		s += it.Price
	}
	return s
}

// OrderView is a read-only surface for expressions. DoRefund() on the
// full struct would be unreachable from the expression because
// OrderView doesn't have it.
type OrderView struct {
	ID       string
	Items    []LineItem
	Meta     map[string]any
	subtotal float64
}

func (v OrderView) Subtotal() float64 { return v.subtotal }

func forExpr(o *Order) OrderView {
	return OrderView{ID: o.ID, Items: o.Items, Meta: o.Meta, subtotal: o.Subtotal()}
}

func main() {
	ctx := context.Background()

	o := &Order{
		ID: "A-1",
		Items: []LineItem{
			{SKU: "widget", Price: 60},
			{SKU: "gadget", Price: 80},
		},
		Meta: map[string]any{"source": "web"},
	}

	src := `Subtotal() > 100 && len(Items) >= 2 && !has(Meta, "refunded")`
	p, err := expr.Compile(src, expr.WithBuiltins())
	if err != nil {
		panic(err)
	}

	// 1. Pointer-to-struct: pointer receivers on Subtotal() work.
	v1, _ := p.Run(ctx, o)

	// 2. Read-only view: same expression, different env type. DoRefund
	//    (if it existed on Order) would not be reachable here.
	v2, _ := p.Run(ctx, forExpr(o))

	// 3. Map: same shape, but built by hand. Missing keys would error,
	//    so every field the expression touches must be present.
	m := map[string]any{
		"Subtotal": func() float64 { return o.Subtotal() },
		"Items":    o.Items,
		"Meta":     o.Meta,
	}
	// Maps can't call functions via selector syntax — `Subtotal()` only
	// works on structs/methods — so drop that leg from the expression:
	p2, _ := expr.Compile(`len(Items) >= 2 && !has(Meta, "refunded")`, expr.WithBuiltins())
	v3, _ := p2.Run(ctx, m)

	fmt.Println("pointer:", v1)
	fmt.Println("view:   ", v2)
	fmt.Println("map:    ", v3)
}
