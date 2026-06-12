# Worked examples

A tour of slightly more interesting `expr` expressions. The goal here is to
show how JSON literals, higher-order forms, selectors, and builtins compose
into real-world shapes — validation, data shaping, filtering, templating.

Every expression below is a **single expression** (expr has no statements).
Whitespace and newlines inside parentheses, brackets, and braces are free,
so expressions can be laid out across many lines for readability. Each
example includes the environment it runs against and the expected result.

The runnable companions are in [`../../examples/`](../../examples/). Start with
`examples/basic` and `examples/higher_order` for the minimum viable setup;
the expressions below just extend those patterns.

---

## 1. Shaping a response object

Build a JSON-ish response from a user record, pulling only the fields a
caller should see and computing a couple of derived values.

```go
{
    "id":        user.id,
    "name":      upper(user.name),
    "is_adult":  user.age >= 18,
    "roles":     filter(user.roles, it != "internal"),
    "role_count": len(filter(user.roles, it != "internal")),
    "primary":   find(user.roles, it != "internal"),
}
```

Env:

```go
map[string]any{
    "user": map[string]any{
        "id":    42,
        "name":  "ada",
        "age":   36,
        "roles": []any{"internal", "admin", "editor"},
    },
}
```

Result (a `map[string]any`):

```go
{
    "id":         42,
    "name":       "ADA",
    "is_adult":   true,
    "roles":      []any{"admin", "editor"},
    "role_count": 2,
    "primary":    "admin",
}
```

---

## 2. Nested higher-order forms

A matrix of order line items. Every form re-binds `it` to its own element,
so inside `count` the inner `it` shadows the order. The result is a JSON
summary combining the predicate with a few derived stats.

```go
{
    "has_whale_order": any(
        orders,
        it.status == "paid" &&
            count(it.items, it.price >= 100) >= 2,
    ),
    "paid_ids":     map(filter(orders, it.status == "paid"), it.id),
    "total_orders": len(orders),
}
```

> Go's automatic semicolon insertion applies inside expr source: a
> continuation line must end with an operator, comma, or opening bracket.
> The trailing comma after `>= 2` above is what keeps the expression a
> single parsed `any(...)` call.

Env:

```go
map[string]any{
    "orders": []any{
        map[string]any{
            "id": "o-1", "status": "paid",
            "items": []any{
                map[string]any{"sku": "A", "price": 120},
                map[string]any{"sku": "B", "price": 80},
                map[string]any{"sku": "C", "price": 150},
            },
        },
        map[string]any{
            "id": "o-2", "status": "pending",
            "items": []any{
                map[string]any{"sku": "D", "price": 999},
            },
        },
    },
}
```

Result:

```go
{
    "has_whale_order": true,  // o-1 is paid and has two items >= 100
    "paid_ids":        []any{"o-1"},
    "total_orders":    2,
}
```

---

## 3. Validation with a readable error bag

`expr` has no `if` and no ternary, and `&&` / `||` always return `bool`.
The idiomatic way to collect conditional messages is a list of
`{ok, msg}` records, filtered on `!it.ok`, then projected to `it.msg`.

```go
{
    "ok": user.age >= 18 &&
        len(user.email) > 0 &&
        contains(user.email, "@"),
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
}
```

Env:

```go
map[string]any{
    "user": map[string]any{
        "age":   15,
        "email": "ada",
    },
}
```

Result:

```go
{
    "ok": false,
    "errors": []any{
        "must be 18 or older",
        "email must contain @",
    },
}
```

The shape generalizes nicely: every rule sits on its own line, adding a
new check is a one-line edit, and the failing messages come out in the
same order they were declared.

---

## 4. Summarizing a stream of events

Build a single summary object from a list of events — counts by kind,
totals, flags. Each key is its own expression over the same `events`
list; the engine evaluates them all in one pass over the enclosing
composite literal.

```go
{
    "total":     len(events),
    "clicks":    count(events, it.kind == "click"),
    "views":     count(events, it.kind == "view"),
    "purchases": count(events, it.kind == "purchase"),
    "has_sale":  any(events, it.kind == "purchase"),
    "top_user":  find(events, it.kind == "purchase")?.user,
}
```

Env:

```go
map[string]any{
    "events": []any{
        map[string]any{"kind": "view",     "user": "ada"},
        map[string]any{"kind": "click",    "user": "ada"},
        map[string]any{"kind": "view",     "user": "grace"},
        map[string]any{"kind": "purchase", "user": "grace"},
    },
}
```

Named bindings let you reference the outer element by name from inside
an inner body:

```
filter(entries(scores), e, e.value >= 80)
```

Caveat for the two-arg form: inside a nested two-arg higher-order form,
`it` and `index` always refer to the **innermost** form's current
element. The outer `it` is shadowed. Use named bindings to keep both
visible.

---

## 5. Template-driven messages

`NewTemplate` compiles every `${...}` once, then evaluates against an env
per `Render`.

```
Hi ${user.name}! You have ${len(filter(user.tasks, !it.done))} task(s) left:
${map(
    filter(user.tasks, !it.done),
    sprintf("  - %s (due %s)", it.title, it.due)
)}
```

Env:

```go
map[string]any{
    "user": map[string]any{
        "name": "Grace",
        "tasks": []any{
            map[string]any{"title": "write spec",  "due": "Mon", "done": true},
            map[string]any{"title": "review PR",   "due": "Tue", "done": false},
            map[string]any{"title": "ship v1",     "due": "Fri", "done": false},
        },
    },
}
```

Maps, slices, arrays, and structs render as compact JSON inside `${...}`:
`${files}` produces `["a.go","b.go"]`, not `[a.go b.go]`. For
variable-length lists rendered as human-readable text, register a `join`
helper and call it from the expression, or join in Go and pass the result
through the env.

---

## 6. Mixed struct + map env

`expr` doesn't care whether the env is a `map[string]any`, a struct, or a
pointer to one. Exported fields and zero-arg methods both become
identifiers. Selectors traverse through either.

```go
Order.Subtotal() > 100 &&
    len(Order.Items) >= 2 &&
    any(Order.Items, it.Price >= 50) &&
    !contains(keys(Order.Meta), "refunded")
```

Where `Order` is a struct with an `Items` slice, a `Meta map[string]any`
field, and a `Subtotal() float64` method. Field lookup beats method
lookup: if the struct had both a `Meta` field and a `Meta()` method, the
field wins.

If your struct is the typed form of a JSON-shaped API contract, opt in to
tag lookup and keep the expression in public-schema names:

```go
result.environment.status == "ready"
```

Where `result` is a struct value like:

```go
type Output struct {
    Environment Environment `json:"environment"`
}

type Environment struct {
    Status string `json:"status"`
}

p, err := expr.Compile(
    `result.environment.status == "ready"`,
    expr.WithStructTags("json"),
)
```

---

## 7. Role-based access control

A policy expression deciding whether `user` may see `resource`, returned
as a JSON object so the caller can log both the decision and the inputs
it was made against. All the branching is done by `||` / `&&`
short-circuit; no ternary needed.

```go
{
    "allow": user.role == "admin" ||
        user.role == "editor" &&
            contains(["draft", "review", "published"], resource.state) ||
        user.role == "viewer" &&
            resource.state == "published" &&
            !resource.private ||
        resource.owner == user.id &&
            resource.state != "archived",
    "role":  user.role,
    "state": resource.state,
}
```

(The parentheses can be dropped because `&&` binds tighter than `||`,
so the grouping is the same. Flat layout keeps every continuation line
ending on an operator, which expr needs to parse multi-line sources.)

Env:

```go
map[string]any{
    "user": map[string]any{"id": "u-7", "role": "viewer"},
    "resource": map[string]any{
        "state":   "published",
        "private": false,
        "owner":   "u-1",
    },
}
```

Result:

```go
{
    "allow": true,  // the viewer branch fires
    "role":  "viewer",
    "state": "published",
}
```

The whole decision is one expression, which means you can log it, diff
it, store it per tenant, or let an operator edit it at runtime without
a redeploy. That's the entire pitch for an embedded expression language.

---

## 8. Extracting + sorting

`sortBy` is a built-in special form. It evaluates a key expression per
element and returns a stable-sorted copy of the list. Combined with
`filter` and a registered `take`:

```go
take(
    sortBy(
        filter(users, it.active),
        it.age,
    ),
    3,
)
```

Or with the named-binding form for clarity:

```go
take(
    sortBy(
        filter(users, u, u.active),
        u,
        u.age,
    ),
    3,
)
```

Host-side `take` registration:

```go
expr.WithFunctions(map[string]any{
    "take": func(xs []any, n int) []any { ... },
})
```

`sortBy` keys must be all numbers or all strings. For descending order,
compose with `reverse` from `CollectionFuncs`:

```go
reverse(sortBy(users, u, u.age))
```

---

## 9. A chunky predicate over nested JSON

Combining everything — selectors, indexing, higher-order, builtins, and
JSON literals — in one readable expression. The result is a structured
accept/reject record so the decision is self-documenting in logs.

```go
{
    "accept": request.method == "POST" &&
        contains(["application/json", "application/ld+json"], request.headers["content-type"]) &&
        len(request.body.items) > 0 &&
        all(request.body.items, it.qty > 0 && it.price >= 0) &&
        (has(request.body, "coupon") && upper(request.body.coupon) == "LAUNCH25" || request.body.total >= 100),
    "method":       request.method,
    "item_count":   len(request.body.items),
    "all_positive": all(request.body.items, it.qty > 0 && it.price >= 0),
}
```

Env is a typical webhook payload: a `request` map with `method`,
`headers`, and `body` keys. This kind of expression is exactly what
`expr` is built for — authorization and routing rules that you want your
operators to edit without redeploying the host program.

---

## 10. Guarded math and the opt-in helper groups

`if(cond, then, else)` is lazy — only the selected branch evaluates —
so it doubles as a guard: dividing by a count that might be zero,
selecting into a list that might be empty. Combined with the opt-in
helper groups (`expr.MathFuncs()`, `expr.StringFuncs()`,
`expr.CollectionFuncs()`), per-order stats stay in the expression
instead of leaking into Go:

```go
{
    "avg_price":  if(len(prices) > 0, sum(prices) / len(prices), 0),
    "spread":     if(len(prices) > 0, max(0, last(prices) - first(prices)), 0),
    "top_three":  slice(prices, 0, 3),
    "sku_list":   join(map(items, upper(it.sku)), ", "),
}
```

Env:

```go
map[string]any{
    "prices": []any{int64(10), int64(20), int64(60)},
    "items": []any{
        map[string]any{"sku": "a-1"},
        map[string]any{"sku": "b-2"},
    },
}
```

Compile with the groups registered alongside the standard builtins:

```go
p, err := expr.Compile(src,
    expr.WithBuiltins(),
    expr.WithFunctions(expr.MathFuncs()),
    expr.WithFunctions(expr.StringFuncs()),
    expr.WithFunctions(expr.CollectionFuncs()),
)
```

With an empty `prices` list, `avg_price` is `0` rather than a
division-by-zero error — the untaken branch never runs.

---

## 11. Named bindings and flatMap

Named element bindings let you refer to the outer element by name from
inside a nested form body. `flatMap` flattens one level of nesting.

```go
// Extract all order IDs from all users using flatMap with a named binding.
flatMap(users, u, u.orders)
```

Env:

```go
map[string]any{
    "users": []any{
        map[string]any{"orders": []any{int64(1), int64(2)}},
        map[string]any{"orders": []any{int64(3)}},
    },
}
```

Result: `[]any{1, 2, 3}`.

Nested named forms — the outer `r` stays visible inside the inner body
because the inner named form does not bind `it`:

```go
map(reviews, r, join(map(r.comments, c, r.author + ": " + c), "; "))
```

Env:

```go
map[string]any{
    "reviews": []any{
        map[string]any{"author": "ann", "comments": []any{"good", "clear"}},
        map[string]any{"author": "bob", "comments": []any{"ok"}},
    },
}
```

Result (requires `WithFunctions(expr.StringFuncs())`):
`[]any{"ann: good; ann: clear", "bob: ok"}`.

---

## 12. entries, sort, and reverse

`entries(m)` makes maps iterable through higher-order forms. `sort` and
`reverse` (from `CollectionFuncs`) sort and reverse lists.

```go
// Format all response headers as "key: value", sorted by key.
map(entries(headers), e, sprintf("%s: %s", e.key, e.value))
```

Env:

```go
map[string]any{
    "headers": map[string]any{
        "content-type":   "application/json",
        "x-request-id":   "abc123",
    },
}
```

Result: `[]any{"content-type: application/json", "x-request-id: abc123"}`.

```go
// Keep only entries whose value exceeds a threshold.
filter(entries(scores), e, e.value > 80)
```

`sort` and `reverse` require `WithFunctions(expr.CollectionFuncs())`:

```go
// Sort numbers ascending, then reverse for descending.
reverse(sort([3, 1, 2]))   // → [3, 2, 1]
sort(["banana", "apple"])  // → ["apple", "banana"]
```

`sort` accepts all-numbers or all-strings; mixed types produce
`ErrEvaluate`. It never mutates the input and returns a fresh `[]any`.
`reverse` works on any list type and also returns a fresh copy.
