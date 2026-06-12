# Higher-order patterns

`map`, `filter`, `flatMap`, `any`, `all`, `find`, `count`, and `sortBy`
are the closest thing expr has to control flow over collections. There
is no `for` and no `let`. These eight forms, plus the lazy `if(cond, t,
f)` special form (only the selected branch evaluates) and Go's
short-circuit `&&` / `||`, are how you make decisions and shape data.
This guide walks the idioms that come up most often and the ones you
have to work around.

A runnable companion lives in
[`../../examples/higher_order_patterns/`](../../examples/higher_order_patterns/).

## The shape of a higher-order form

Every iterating form accepts two call shapes:

```
form(list, body)            // two-arg: binds `it` and `index`
form(list, name, body)      // three-arg: binds `name` and `index`
```

- `list` must be a slice, array, or `nil`. **Maps are not iterated
  directly.** Use `keys(m)` to iterate keys, or `entries(m)` to iterate
  key-value pairs.
- In the two-arg form, `it` is the current element and `index` is its
  0-based position. Both shadow any outer identifier of the same name.
- In the three-arg form, the second argument is the element binding name.
  Only the chosen name and `index` are bound inside the body; `it` is
  **not** bound, so an enclosing scope's `it` remains visible.

These forms are **always registered**. `WithBuiltins()` is not required.
You can shadow any of them by registering your own function of the same
name, but you lose the per-element re-evaluation.

## Named element bindings

The three-arg form solves a problem the two-arg form cannot: nested
forms where you need to refer to the outer element by name from inside
an inner body.

With the two-arg form only, the inner body shadows the outer `it`:

```
// Inside the inner map, `it` is a comment, not a review.
// There is no way to refer to the review from inside this body.
map(reviews, map(it.comments, it))  // it = comment here, review is gone
```

With named bindings, you choose which name is visible where:

```
// Outer two-arg, inner named: outer `it` (the review) stays visible
// because the inner named form does not bind `it`.
map(reviews, map(it.comments, c, it.author + "/" + c))
//           ^^ outer `it` = review   ^^ inner `c` = comment

// Outer named, inner two-arg: `r` (the review) is visible inside
// the inner body alongside inner `it` (the comment).
map(reviews, r, join(map(r.comments, r.author + "/" + it), ","))
//           ^^ binds r                          ^^ inner `it` = comment
```

Named bindings shadow env names and outer bindings of the same name.
An inner form that reuses a name hides the outer one for its body:

```
map(users, u, map(u.orders, u, u))   // inner u shadows outer u
```

### Reserved binding names

You cannot use `it`, `index`, `true`, `false`, `nil`, `map`, or `if`
as a binding name. Any of these produces an `ErrEvaluate: <form>
binding cannot be named "<name>"`. A non-identifier in the name
position produces `<form> binding must be a plain identifier, got ...`.

## Validation bags

The canonical shape for "run a list of rules over an input and collect
the ones that failed." Every rule is a `{ok, msg}` literal, you filter
for failures, then project to the message.

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

Why this beats a flat `&&` chain: you get the failing reasons in order,
every rule is a one-line edit, and adding a check is adding a row. The
top-level `ok` still short-circuits: the engine doesn't build the error
list unless you ask for it in the same composite literal.

This pattern generalizes to anywhere you want "a list of possibly-failing
predicates with associated data." For example, validation with
severities: `{"ok": ..., "severity": "warn", "msg": "..."}`.

## Summary objects

A single composite literal with many `count(...)` / `any(...)` /
`find(...)` calls is the idiomatic way to build a stats object from a
list:

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

Every field is its own pass over the list, but the engine evaluates them
in order inside the enclosing composite literal, so it reads like a
single declarative summary. If you care about doing it in one pass,
register a Go function that takes the list and returns the summary.

## Filter, then map

`filter` + `map` is the standard "keep the ones I want, then project
the field I care about" shape:

```
map(filter(orders, it.status == "paid"), it.id)
```

Reads as "the id of every paid order." Named bindings make the elements
explicit when nesting gets deep:

```
map(
    filter(orders, o, o.status == "paid" && o.total > 100),
    o,
    {o.id: o.total},
)
```

## Pipelines: the same chains, left to right

`a | f(x)` compiles as `f(a, x)`, so a filter-then-map chain can read
in execution order instead of inside out:

```
orders | filter(it.status == "paid") | map(it.id)
```

This is purely compile-time sugar over the nested form above. The
forms, bindings, and laziness rules are identical, and the three-arg
named-binding shape composes the same way:

```
orders | filter(o, o.status == "paid") | map(o, {o.id: o.total})
```

The right side of each `|` must be written as a call (`xs | trim()`,
not `xs | trim`), and a pipe on the right-hand side of a comparison
must be parenthesized. Precedence details live in the
[spec](../reference/spec.md#pipeline-); a worked example is in
[examples.md](examples.md#13-pipelines).

## Nested forms and the `it` rebinding rule

Inside a nested two-arg higher-order form, `it` and `index` always refer
to the **innermost** form's current element. The outer binding is gone.

```
count(orders, it.status == "paid" && count(it.items, it.price >= 100) >= 2)
//             ^^ outer it (an order)     ^^ inner it (an item)
```

The outer `it.status` runs before the inner `count(it.items, ...)`
starts, so the two references don't collide. But inside the inner
predicate, `it` is an item, not an order.

Named bindings solve this when you need the outer element inside an
inner body:

```
// Before: no way to reference the review from inside the inner body.
map(reviews, map(it.comments, it))

// After: r is the review, it is the comment.
map(reviews, r, map(r.comments, c, sprintf("%s: %s", r.author, c)))
```

## flatMap: flatten and collect

`flatMap(xs, body)` is like `map`, but body results that are lists get
spliced into the output element-by-element. Use it to flatten a
collection of collections, or to expand each element into zero or more
output elements.

```
// Flatten orders-per-user into a single order list.
flatMap(users, u, u.orders)

// Two-arg form: same thing using `it`.
flatMap(users, it.orders)
```

Splicing is one level deep only:

```
flatMap([1, [2, 3], 4], it)   // → [1, 2, 3, 4]
flatMap([[1, [2]], [3]], it)  // → [1, [2], 3]  (inner list kept whole)
```

`nil` body results splice as nothing (the nil-is-an-empty-list rule):

```
flatMap([1, 2, 3], if(it > 1, [it, it], nil))  // → [2, 2, 3, 3]
```

Strings are never split into runes; they append as a single element:

```
flatMap(["ab", "c"], it)   // → ["ab", "c"]
```

## sortBy: stable sort by key

`sortBy(xs, key)` evaluates the key expression once per element and
returns a **new** list sorted in ascending order. The sort is stable:
elements with equal keys preserve their input order. The input list is
never mutated.

```
// Sort orders by total, ascending.
sortBy(orders, it.total)

// Named binding form.
sortBy(orders, o, o.total)
```

Keys must be all numbers (any int/float mix) or all strings. Mixed or
non-comparable key types produce an `ErrEvaluate` naming the offending
element. Combined with `reverse` (from `CollectionFuncs`):

```
// Descending sort.
reverse(sortBy(orders, o, o.total))
```

## Iterating maps with `entries`

`entries(m)` (from `WithBuiltins`) returns the key-value pairs of a
string-keyed map as a sorted `[]any`, each element being
`map[string]any{"key": k, "value": v}`. It makes maps iterable through
all the higher-order forms:

```
// Format all headers as "key: value".
map(entries(headers), e, sprintf("%s: %s", e.key, e.value))

// Keep only entries whose score is above 80.
filter(entries(scores), e, e.value > 80)

// Sort a map's entries by value.
sortBy(entries(scores), e, e.value)
```

## Why there is no `let`, and what to do instead

Named bindings scoped to a single form body cover the main pain point
without general scoping machinery: the `o` in `filter(orders, o, ...)` is
a one-form binding, not a declaration that leaks into siblings. If you
need a value bound across a whole expression or across siblings of a
composite literal, the answer is still to move it outside the expression.

**1. Duplicate the sub-expression.** If the value is cheap, write it
twice:

```
{
    "greeting": sprintf("Hi %s!", user.profile.name),
    "signoff":  sprintf("Bye %s.", user.profile.name),
}
```

**2. Register a helper.** If you need to combine the outer and inner
elements in a way named bindings can't help with (e.g., you need both
elements as arguments to a Go function), register the helper:

```go
expr.WithFunctions(map[string]any{
    "ordersFor": func(orders []any, userID string) []any { /* ... */ },
})
```

```
ordersFor(orders, user.id)
```

**3. Pre-compute in Go.** Move the binding out of the expression
entirely:

```go
env := map[string]any{
    "user":    user,
    "name":    user.Profile.Name, // the "let"
    "isAdmin": user.Role == "admin",
}
```

Expressions read `name` and `isAdmin` directly. You lose nothing except
the ability to change the derivation from inside an expression.

## `any` / `all` short-circuit, `count` does not

- `any(list, pred)` returns as soon as a match is found.
- `all(list, pred)` returns as soon as a non-match is found.
- `filter` and `map` run the predicate on every element.
- `count` runs the predicate on every element (no early exit).
- `find` returns as soon as a match is found.
- `flatMap` and `sortBy` evaluate the body/key on every element.

If you need "at least two matches," `count(list, pred) >= 2` is correct
but iterates the whole list.

## Empty-list behavior

| Form                   | On empty or nil list         |
| ---------------------- | ---------------------------- |
| `map(xs, it)`          | `[]any{}` (empty slice)      |
| `filter(xs, pred)`     | `[]any{}`                    |
| `flatMap(xs, it)`      | `[]any{}`                    |
| `any(xs, pred)`        | `false`                      |
| `all(xs, pred)`        | `true` (vacuously)           |
| `find(xs, pred)`       | `nil`                        |
| `count(xs, pred)`      | `0`                          |
| `sortBy(xs, key)`      | `[]any{}`                    |

`all([]) == true` is the usual mathematical convention and usually what
you want for "every X must be valid" over a possibly-empty list. If you
need "non-empty and all valid," spell it out:
`len(xs) > 0 && all(xs, pred)`.

## When to stop reaching for higher-order

expr's higher-order set covers single-pass list operations and sorting.
It does **not** cover grouping, zipping, or reduce with an accumulator.
If you find yourself needing those, register a Go function and call it.

The rule: if it fits in one pass with a per-element predicate or key,
use the forms. Otherwise, register a Go function.
