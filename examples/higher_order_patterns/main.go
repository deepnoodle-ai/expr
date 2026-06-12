// Higher-order patterns: validation bag, summary object, filter+map projection,
// named bindings, flatMap, sortBy, and entries. All are single-expression shapes.
package main

import (
	"context"
	"fmt"

	"github.com/deepnoodle-ai/expr"
)

func main() {
	ctx := context.Background()
	opts := []expr.Option{expr.WithBuiltins()}

	run := func(label, src string, env any, extra ...expr.Option) {
		allOpts := append(opts, extra...)
		p, err := expr.Compile(src, allOpts...)
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

	// 4. Named binding: named form + two-arg inner form.
	reviews := []any{
		map[string]any{"author": "ann", "comments": []any{"a1", "a2"}},
		map[string]any{"author": "bob", "comments": []any{"b1"}},
	}
	// Outer named (r), inner two-arg (it = comment): r.author stays visible.
	run("nested named binding",
		`map(reviews, r, map(r.comments, r.author + "/" + it))`,
		map[string]any{"reviews": reviews},
	)

	// 5. flatMap: flatten orders across users.
	run("flatMap orders",
		`flatMap(users, u, u.orders)`,
		map[string]any{
			"users": []any{
				map[string]any{"orders": []any{int64(1), int64(2)}},
				map[string]any{"orders": []any{int64(3)}},
			},
		},
	)

	// 6. sortBy: sort orders by a field.
	run("sortBy total",
		`map(sortBy(orders, o, o.n), o, o.n)`,
		map[string]any{
			"orders": []any{
				map[string]any{"n": int64(3)},
				map[string]any{"n": int64(1)},
				map[string]any{"n": int64(2)},
			},
		},
	)

	// 7. entries: iterate a map's key-value pairs.
	run("entries",
		`map(entries(headers), e, e.key + ": " + e.value)`,
		map[string]any{
			"headers": map[string]any{
				"content-type": "application/json",
				"x-request-id": "abc123",
			},
		},
	)
}
