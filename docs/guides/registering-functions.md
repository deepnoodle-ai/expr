# Registering Go functions

expr doesn't let expressions define their own functions — the host
program decides what's callable. `WithFunctions(map[string]any)`
registers any Go function as a name the expression can invoke. This
guide covers the rules, the ergonomics, and the corner cases.

Everything here has a runnable companion in
[`../../examples/register_functions/`](../../examples/register_functions/)
and is pinned by tests so it stays honest.

## The basic shape

```go
p, err := expr.Compile(`greet(upper(name))`, expr.WithFunctions(map[string]any{
    "upper": strings.ToUpper,
    "greet": func(name string) string { return "Hello, " + name + "!" },
}))
```

That's the whole surface. The map is copied into the `Program` at
compile time; registering after `Compile` is not a thing. If you want a
different set of functions for a different call site, compile a
different `Program`.

`WithFunctions` is additive and last-wins. Calling it twice, or mixing
it with `WithBuiltins()`, merges the two maps and lets later options
shadow earlier ones of the same name:

```go
expr.Compile(src,
    expr.WithBuiltins(),                                   // registers `upper`
    expr.WithFunctions(map[string]any{"upper": myUpper}),  // shadows it
)
```

## Supported return signatures

Only three shapes are legal:

| Shape         | Example                                        |
| ------------- | ---------------------------------------------- |
| `T`           | `func(s string) string`                        |
| `(T, error)`  | `func(url string) ([]byte, error)`             |
| `()`          | `func(event Event)` (returns `nil` to expr)    |

Anything else — multiple non-error returns, a second return that
isn't `error` — errors at call time, not compile time. expr can't tell
what's in your map without actually looking at the function's reflect
type at dispatch time.

A function that returns `(T, error)` and returns a non-nil error
propagates that error up through `Program.Run`. The error chain is
preserved, so `errors.Is`/`errors.As` on the final error still match
whatever your function returned.

## Automatic `context.Context` injection

If a registered function's **first parameter** is `context.Context`,
expr passes the live `ctx` from `Program.Run(ctx, env)` automatically.
The caller-visible arity excludes that parameter:

```go
expr.WithFunctions(map[string]any{
    "fetch": func(ctx context.Context, url string) (string, error) {
        req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
        // ...
    },
})
// called from expr as: fetch("https://example.com")
```

This is the canonical way to make registered functions cancellable.
expr itself checks `ctx.Err()` before each AST node, but once control
has entered your function, only *your* function can notice. A
registered function that ignores its context and blocks forever cannot
be interrupted — Go has no way to kill a goroutine, and expr will not
fake it. If you register network or disk I/O, take a `context.Context`.

## Variadics

Variadic Go functions work as expected, except for one detail: expr
has no spread-call syntax. You call them with positional args only.

```go
expr.WithFunctions(map[string]any{
    "sprintf": fmt.Sprintf, // func(string, ...any) string
})
// sprintf("%s is %d", name, age)  ✓
// sprintf("%s", args...)           ✗ — parses, errors at eval
```

If you need to splat a runtime list, do the join in Go before calling
`Run`, or register a non-variadic helper that takes a `[]any`.

## Numeric conversions

Integer literals in expr are `int64`; float literals are `float64`.
When you register `func(int)` or `func(float32)`, expr converts at the
call site — but only if the value fits. A range check happens on every
integer narrowing, so a `2_000_000_000` passed to a `func(int16)`
errors at runtime instead of silently wrapping.
Integer-to-string conversion is deliberately rejected: `fn(65)` will not
be treated as `fn("A")`.

```go
expr.WithFunctions(map[string]any{
    // Fine: int64 → int is safe on every supported platform.
    "double": func(n int) int { return n * 2 },

    // Fine: expr passes a float64, Go narrows to float32.
    "nearHalf": func(f float32) bool { return f > 0.49 && f < 0.51 },

    // Risky: expr will range-check. Calling `clamp(999)` from expr errors.
    "clamp": func(n int8) int8 { return n },
})
```

Take `int64` / `float64` / `any` when you can; you get the fewest
surprises. Reserve narrower kinds for places where the narrower kind is
genuinely part of the contract.

## Nil-ability and `any`

A nil passed to a nilable parameter kind (`any`, pointer, map, slice,
chan, func) becomes the zero value of that kind. A nil passed to a
non-nilable parameter (`int`, `string`, `bool`, struct) errors — it
cannot mean anything.

```go
expr.WithFunctions(map[string]any{
    // Safe — any and []any both accept nil.
    "dump":    func(v any) string { return fmt.Sprintf("%v", v) },
    "joinAll": func(xs []any) string { /* ... */ },

    // Dangerous — a nil from a missing lookup errors at the call,
    // even though the spelled-out signature looks fine.
    "greet": func(name string) string { return "Hi " + name },
})
```

If a registered function needs to accept "maybe nil, maybe a string,"
take `any` and switch on the type inside.

## Shadowing builtins and higher-order forms

Any name you register shadows a builtin or higher-order form of the
same name. This is how you specialize: if `filter` as-shipped doesn't
match what you want, register your own `filter`, and the special form
steps aside.

```go
// The shipped `filter` returns []any. If your domain wants []string:
expr.WithFunctions(map[string]any{
    "filter": func(xs []string, allow string) []string { /* ... */ },
})
```

Be careful: shadowing `filter`/`any`/`all`/`find`/`count`/`map` gives
you a regular function call, not a special form, so the predicate
feature is gone. The second argument is an *evaluated value*, not an
AST re-evaluated per element. If you want per-element evaluation,
don't shadow the form — register a different name.

## Errors from registered functions

A `(T, error)` return becomes an `ErrEvaluate`-wrapped error out of
`Run`. The original error is still reachable:

```go
_, err := p.Run(ctx, env)
if errors.Is(err, expr.ErrEvaluate) { /* it's an eval error */ }

var parseErr *strconv.NumError
if errors.As(err, &parseErr)        { /* your specific error */ }
```

If you want callers to distinguish "the expression was bad" from "my
function failed," wrap your own error in the function and match on it
at the top level.

## Gotchas worth remembering

- **Registration is by name, not by signature.** You cannot register
  two functions called `fmt` that differ only in argument count.
- **Methods are not callable at the top level.** `WithFunctions` takes
  plain function values. To expose methods, either wrap them in a
  closure, or put the receiver in the env — struct methods on the env
  are already callable via `Receiver.Method(...)`.
- **The map is shared by reference.** Don't mutate it after passing to
  `Compile` — expr doesn't expect that and may read from it again.
- **`context.Context` detection is exact.** The parameter type must be
  literally `context.Context` (the interface), not a type alias.
- **A registered function that panics panics the caller.** expr does
  not `recover` around user code. If you want panic safety, wrap in the
  function, not the expression.
