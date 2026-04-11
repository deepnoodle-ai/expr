# Designing an env

The env is whatever you pass as the second argument to
`Program.Run(ctx, env)`. It's what the expression sees. Getting its
shape right is the single biggest ergonomic lever you have over how
the expressions written against it will read.

This guide covers the three realistic shapes (map, struct, pointer to
struct), the rules that govern name resolution, and the patterns that
keep the surface both usable and safe. A runnable companion lives in
[`../../examples/env_shapes/`](../../examples/env_shapes/).

## The three shapes

expr accepts three kinds of env, plus `nil`:

- `map[string]any` (or any other `map[string]T` with string keys)
- a struct value
- a pointer to a struct
- `nil` — only functions and higher-order forms resolve; any
  identifier lookup errors

That's it. expr does not reach through channels, interface values, or
embedded maps nested behind method calls. You compose with selectors:
`user.roles`, `order.items[0].price`, `Subtotal()`.

## When to use a map

A `map[string]any` is the right default when:

- The shape is dynamic (request bodies, JSON payloads, policy inputs
  from a database row).
- You want to allow the author of the expression to add new keys
  without recompiling the host.
- The env is already a map in your host code and converting it would
  just move work around.

The trade-off is that **missing keys are errors**, not zero values.
`user.nickname` on an env that has no `nickname` key returns an
`ErrEvaluate` with a "did you mean…?" suggestion. This is almost
always what you want for correctness, but it means you should
normalize nullable fields to an explicit `nil` entry, not leave them
absent:

```go
env := map[string]any{
    "user": map[string]any{
        "id":       42,
        "nickname": nil, // explicit: expression can test it with == nil
    },
}
```

## When to use a struct

A struct (or pointer to one) is the right default when:

- The shape is fixed and defined in Go.
- You already have the struct — this is your domain model.
- You want methods on the env to be callable from expressions.

Both exported fields and zero-arg methods become identifiers, and
**field lookup beats method lookup**: if your struct has a `Meta`
field and also a `Meta()` method, the field wins. Unexported fields
and methods are unreachable, exactly like normal Go.

```go
type Order struct {
    ID    string
    Items []LineItem
    Meta  map[string]any
}

func (o *Order) Subtotal() float64 { /* ... */ }

p, _ := expr.Compile(
    `Subtotal() > 100 && len(Items) >= 2 && !has(Meta, "refunded")`,
    expr.WithBuiltins(),
)
p.Run(ctx, &order)
```

Pointer vs value only matters if you want pointer-receiver methods.
`*Order` satisfies both value and pointer receivers; `Order` only
satisfies value receivers. When in doubt, pass a pointer.

## The field-beats-method rule

Field-beats-method isn't a quirk — it's the reason expressions can
read consistently across structs and maps. A map has only keys, a
struct has fields, and the mental model is "names that select into a
container." Method dispatch is a last resort for struct envs.

If you want a method to take priority over a same-named field, give
one of them a different name in the env. There is no sigil to force
method dispatch.

## Exposing a read-only surface

A common worry: "my `Order` has a `DoRefund()` method, I don't want
expressions to call it." Two options, ordered from most to least
paranoid:

**Wrap it.** Define a view struct that holds only the read-only
fields and methods you want to expose, and build it at the call site.

```go
type OrderView struct {
    ID       string
    Items    []LineItem
    Meta     map[string]any
    subtotal float64
}

func (v OrderView) Subtotal() float64 { return v.subtotal }

func forExpr(o *Order) OrderView {
    return OrderView{
        ID: o.ID, Items: o.Items, Meta: o.Meta,
        subtotal: o.Subtotal(),
    }
}
```

This is the Go equivalent of handing expressions a read-only snapshot.
The wrapper never exposes `DoRefund`, so expressions literally cannot
name it.

**Project into a map.** If the view is simple, skip the struct and
build a `map[string]any`. The trade-off is losing type safety on the
host side — you'll catch typos at eval time instead of compile time.

## Shadowing higher-order forms intentionally

`map`, `filter`, `any`, `all`, `find`, `count` are always registered
as special forms. If your env has a field or key with one of those
names, it still works — field lookup beats function lookup when the
source says `user.count`, because `count` is only resolved as a form
when it appears at the head of a call.

But a bare top-level `count` as an identifier (not `count(...)`) would
resolve to the form's function value, which is rarely what you want.
Don't name env fields `count` unless you'll always use them in a
selector context.

## Missing-key semantics, concretely

| Situation                            | Result                                 |
| ------------------------------------ | -------------------------------------- |
| Map lookup on missing key            | `ErrEvaluate` with "did you mean…?"    |
| Struct field on missing name         | `ErrEvaluate` with "did you mean…?"    |
| `nil` map, any key                   | `ErrEvaluate`                          |
| Nil pointer to struct                | `ErrEvaluate`                          |
| `has(m, "k")` on a map               | `false` if absent; never an error      |

If you need "look this up, zero if missing," use `has(m, "k")` first
or put an explicit `nil` in the map. expr makes the distinction between
"missing" and "present but nil" deliberate.

## Multiple roots

Nothing forces the env to be a single struct or a single map. The
common pattern is a `map[string]any` at the top that holds everything:

```go
env := map[string]any{
    "user":     userStruct,     // struct — exposes fields and methods
    "request":  requestMap,     // dynamic map — request body
    "tenant":   tenant.ID,      // scalar — free identifier
    "features": features,       // []string — easy to contains() against
}
```

Each top-level key is an identifier, and expressions select into
whichever shape fits. This composes well because you can add a new
root without touching existing expressions.

## When to preprocess in Go vs in the expression

If the same derivation shows up in every expression — `totalCents / 100`,
`strings.ToLower(user.name)`, `user.age - today.year` — compute it
once in the host and expose the result in the env. Expressions should
read as close to the decision as possible; plumbing belongs in Go.

Conversely: resist the urge to pre-compute anything the author might
legitimately want to override. If you pre-compute `isAdult` on the way
in, expressions can't ask about 21-or-older instead.

The rule of thumb: pre-compute for correctness and cost, not for
guessing what an expression author might want.
