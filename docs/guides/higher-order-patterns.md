# Higher-order patterns

`map`, `filter`, `any`, `all`, `find`, `count` are the closest thing
expr has to control flow over collections. There's no `for` and no
`let`. These six forms, plus the lazy `if(cond, t, f)` special form
(only the selected branch evaluates) and Go's short-circuit `&&` /
`||`, are how you make decisions and shape data. This guide walks the idioms that come up most often and the
ones you have to work around.

A runnable companion lives in
[`../../examples/higher_order_patterns/`](../../examples/higher_order_patterns/).

## The shape of a higher-order form

```
form(list, predicate_or_transform)
```

- `list` must be a slice, array, or `nil`. **Maps are not iterated.**
  To iterate a map, drive with `keys(m)` and index into `m[k]` inside
  the predicate.
- The second argument is an **unevaluated AST** that the form
  re-evaluates once per element. Inside that body, `it` is the
  current element and `index` is its 0-based position. Both shadow
  any outer identifier of the same name.

These forms are **always registered**. `WithBuiltins()` is not
required. You can shadow them by registering your own function of
the same name, but you lose the per-element re-evaluation — see
[registering-functions.md](registering-functions.md).

## Validation bags

The canonical shape for "run a list of rules over an input and
collect the ones that failed." Every rule is a
`{ok, msg}` literal, you filter for failures, then project to the
message.

```
{
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
}
```

Why this beats a flat `&&` chain: you get the failing reasons in
order, every rule is a one-line edit, and adding a check is adding a
row. The top-level `ok` still short-circuits — the engine doesn't
build the error list unless you ask for it in the same composite
literal.

This pattern generalizes to anywhere you want "a list of possibly-failing
predicates with associated data." For example, validation with
severities: `{"ok": ..., "severity": "warn", "msg": "..."}`.

## Summary objects

A single composite literal with many `count(...)` / `any(...)` /
`find(...)` calls is the idiomatic way to build a stats object from
a list:

```
{
    "total":     len(events),
    "clicks":    count(events, it.kind == "click"),
    "views":     count(events, it.kind == "view"),
    "purchases": count(events, it.kind == "purchase"),
    "has_sale":  any(events, it.kind == "purchase"),
    "top_user":  find(events, it.kind == "purchase")?.user,
}
```

Every field is its own pass over the list, but the engine evaluates
them in order inside the enclosing composite literal, so it reads
like a single declarative summary. If you care about doing it in one
pass, register a Go function that takes the list and returns the
summary — don't fight expr.

## Filter, then map

`filter` + `map` is the standard "keep the ones I want, then project
the field I care about" shape:

```
map(filter(orders, it.status == "paid"), it.id)
```

Reads as "the id of every paid order." Composes cleanly to any depth:

```
map(
    filter(users, it.active && len(it.roles) > 0),
    upper(it.name),
)
```

The `it` in the outer `map` refers to *the element that passed the
inner `filter`* — there is no confusion because the inner `filter`
has already been reduced to a value before the outer `map` runs.

## Nested forms and the `it` rebinding rule

Inside a nested higher-order form, `it` and `index` always refer to
the **innermost** form's current element. The outer binding is gone.

```
count(orders, it.status == "paid" && count(it.items, it.price >= 100) >= 2)
//             ^^ outer it (an order)     ^^ inner it (an item)
```

The outer `it.status` runs before the inner `count(it.items, ...)`
starts, so the two references don't collide. But inside the inner
predicate, `it` is an item, not an order. There is no way to spell
"outer it" from inside the inner body.

## Why there's no `let`, and what to do instead

People routinely ask for `let` or `with` to bind an intermediate
value. expr doesn't have it, by design — every `let` would add a
scope, and scopes are where expression languages start growing
teeth. There are three workarounds depending on what you need.

**1. Duplicate the sub-expression.** If the value is cheap, just
write it twice. The AST walker has per-element caching for many
common subexpressions, but even without that, two reads of
`user.profile.name` is usually fine.

```
{
    "greeting": sprintf("Hi %s!", user.profile.name),
    "signoff":  sprintf("Bye %s.", user.profile.name),
}
```

**2. Register a helper.** If you need the value bound across a whole
subtree (a join on the outer element from inside a nested form, for
example), register a Go function that takes both and returns the
computed shape.

```go
expr.WithFunctions(map[string]any{
    "ordersFor": func(orders []any, userID string) []any { /* ... */ },
})
```

```
ordersFor(orders, user.id)
```

**3. Pre-compute in Go.** Move the binding out of the expression
entirely. If your template needs `user.profile.name` three times,
add it as an env key:

```go
env := map[string]any{
    "user":    user,
    "name":    user.Profile.Name, // the "let"
    "isAdmin": user.Role == "admin",
}
```

Expressions read `name` and `isAdmin` directly. You lose nothing
except the ability to change the derivation from inside an
expression — and if that mattered, you probably didn't want to
precompute.

## `any` / `all` short-circuit, `count` does not

- `any(list, pred)` returns as soon as a match is found.
- `all(list, pred)` returns as soon as a non-match is found.
- `filter` and `map` run the predicate on every element.
- `count` runs the predicate on every element (no early exit).
- `find` returns as soon as a match is found.

If you need "at least two matches," `count(list, pred) >= 2` is
correct but iterates the whole list. For large lists where the
cutoff matters, write the check as a function and register it.

## Empty-list behavior

| Form                  | On empty list                        |
| --------------------- | ------------------------------------ |
| `map(xs, it)`         | `[]any{}` (empty slice)              |
| `filter(xs, pred)`    | `[]any{}`                            |
| `any(xs, pred)`       | `false`                              |
| `all(xs, pred)`       | `true` (vacuously)                   |
| `find(xs, pred)`      | `nil`                                |
| `count(xs, pred)`     | `0`                                  |

`all([]) == true` is the usual mathematical convention and usually
what you want for "every X must be valid" over a possibly-empty list.
If you need "non-empty and all valid," spell it out:
`len(xs) > 0 && all(xs, pred)`.

## When to stop reaching for higher-order

expr's higher-order set covers single-pass list operations. It does
**not** cover grouping, sorting, or zipping. If you find yourself
writing a `sort`-shaped expression, register a `sortBy` in Go and
call it — see example 8 in [examples.md](examples.md). Same for
group-by, reduce with accumulator, and anything that needs to bind
values across iterations.

The rule: if it fits in one pass with a per-element predicate, use
the forms. Otherwise, register a Go function.
