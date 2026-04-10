package main

import (
	"context"
	"fmt"

	"github.com/deepnoodle-ai/expr"
)

type Order struct {
	ID       string
	Customer string
	Items    []LineItem
}

type LineItem struct {
	SKU      string
	Quantity int
	Price    float64
}

func (o Order) Subtotal() float64 {
	var total float64
	for _, it := range o.Items {
		total += it.Price * float64(it.Quantity)
	}
	return total
}

func main() {
	ctx := context.Background()

	e := expr.New(expr.WithBuiltins())

	order := Order{
		ID:       "A-1042",
		Customer: "Ada Lovelace",
		Items: []LineItem{
			{SKU: "widget", Quantity: 2, Price: 19.99},
			{SKU: "gadget", Quantity: 1, Price: 89.52},
		},
	}

	v, err := e.Eval(ctx, `Subtotal() > 100 && len(Items) >= 2`, order)
	if err != nil {
		panic(err)
	}
	fmt.Println(v) // true
}
