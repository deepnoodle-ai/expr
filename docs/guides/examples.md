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
    "top_user":  find(events, it.kind == "purchase").user,
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

Caveat worth knowing: inside a nested higher-order form, `it` and
`index` always refer to the **innermost** form's current element.
There is no `let` or outer-binding. If you need to reference both
the outer element and inner element in the same predicate, stop
nesting and do the join in Go, or register a helper function.

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

The `${...}` result is stringified via `fmt.Sprintf("%v", ...)`, so a
`[]any` of strings prints as a Go slice. For real templating of lists,
either build the final string with `sprintf` in a single expression, or
join the list in the host program and interpolate the joined string back
in through the env.

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

## 8. Extracting + sorting via a registered function

Higher-order forms don't include `sort`, on purpose — sorting needs
stable comparators and expr stays out of that business. Register a Go
function instead:

```go
take(
    sortBy(
        filter(users, it.active),
        "age",
    ),
    3,
)
```

Host-side registration:

```go
expr.WithFunctions(map[string]any{
    "sortBy": func(xs []any, key string) []any { ... },
    "take":   func(xs []any, n int) []any { ... },
})
```

The philosophy: if expr doesn't have it, register a Go function for it.
Don't fight the language.

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
