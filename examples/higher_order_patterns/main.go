// Higher-order patterns: validation bag, summary object, and
// filter+map projection. All three are single-expression shapes.
package main

import (
	"context"
	"fmt"

	"github.com/deepnoodle-ai/expr"
)

func main() {
	ctx := context.Background()
	opts := []expr.Option{expr.WithBuiltins()}

	run := func(label, src string, env any) {
		p, err := expr.Compile(src, opts...)
		if err != nil {
			panic(err)
		}
		v, err := p.Run(ctx, env)
		if err != nil {
			panic(err)
		}
		fmt.Printf("== %s ==\n%v\n\n", label, v)
	}

	// 1. Validation bag.
	run("validation bag",
		`{
            "ok": user.age >= 18 && len(user.email) > 0 && contains(user.email, "@"),
            "errors": map(
                filter(
                    [
                        {"ok": user.age >= 18,            "msg": "must be 18 or older"},
                        {"ok": len(user.email) > 0,       "msg": "email is required"},
                        {"ok": contains(user.email, "@"), "msg": "email must contain @"},
                    ],
                    !it.ok,
                ),
                it.msg,
            ),
        }`,
		map[string]any{
			"user": map[string]any{"age": 15, "email": "ada"},
		},
	)

	// 2. Summary object over an event list.
	run("event summary",
		`{
            "total":     len(events),
            "clicks":    count(events, it.kind == "click"),
            "purchases": count(events, it.kind == "purchase"),
            "has_sale":  any(events, it.kind == "purchase"),
        }`,
		map[string]any{
			"events": []any{
				map[string]any{"kind": "view"},
				map[string]any{"kind": "click"},
				map[string]any{"kind": "purchase"},
			},
		},
	)

	// 3. Filter+map projection.
	run("paid order ids",
		`map(filter(orders, it.status == "paid"), it.id)`,
		map[string]any{
			"orders": []any{
				map[string]any{"id": "o-1", "status": "paid"},
				map[string]any{"id": "o-2", "status": "pending"},
				map[string]any{"id": "o-3", "status": "paid"},
			},
		},
	)
}
