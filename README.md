# expr

A small expression language for Go programs. You write a line that looks like
Go, `expr` compiles it, and you run it against whatever data you have lying
around: a map, a struct, a pointer to a struct. Handy for conditions,
templates, and little bits of user-supplied logic you don't want to turn into
a full plugin system.

It is built directly on `go/parser`, so the syntax will feel familiar: it is
mostly a subset of Go's own expression grammar, with a few ergonomic additions
like JSON-style object and array literals.

## Install

```sh
go get github.com/deepnoodle-ai/expr
```

## A taste

```go
p, err := expr.Compile(
    `user.age >= 18 && contains(user.roles, "admin")`,
    expr.WithBuiltins(),
)
if err != nil {
    panic(err)
}

ok, err := p.Run(ctx, map[string]any{
    "user": map[string]any{
        "age":   36,
        "roles": []any{"admin", "editor"},
    },
})
if err != nil {
    panic(err)
}
fmt.Println(ok) // true
```

`Compile` does the parsing work once. The returned `*Program` is immutable and
safe to share between goroutines, so the usual pattern is: compile at startup,
run per request.

No functions are registered by default, which keeps the surface area exactly
as wide as you want it. `WithBuiltins()` opts you into a small standard set
(`len`, `contains`, `has`, `keys`, `upper`, `lower`, `int`, `float`, `string`,
`bool`, `sprintf`), and `WithFunctions` lets you register any Go function you
like as a callable identifier:

```go
p, err := expr.Compile(`greet(upper(name))`, expr.WithFunctions(map[string]any{
    "upper": strings.ToUpper,
    "greet": func(name string) string { return "Hello, " + name + "!" },
}))
```

Mix and match, or skip the builtins entirely and expose only the handful of
functions that make sense for your sandbox.

## What the environment can be

Whatever you pass to `Run` is what the expression sees. A `map[string]any`
works. So does a struct, or a pointer to one: exported fields and zero-arg
methods both become identifiers inside the expression.

```go
p, err := expr.Compile(`Subtotal() > 100 && len(Items) >= 2`, expr.WithBuiltins())
v, err := p.Run(ctx, order) // order is some struct with a Subtotal() method
```

## JSON-style literals

Object and array literals work the way you'd hope, without the Go ceremony:

```go
p, err := expr.Compile(`{"items": [1, 2, 3], "count": 3, "ok": true}`)
```

Under the hood, bare `[...]` becomes `[]any{...}` and `{"k": v}` becomes
`map[string]any{"k": v}`. Strings, comments, and real Go composite literals
are left alone, so nothing you already had stops working.

## Templates

There is also a tiny `${...}` interpolator for when you want expressions
embedded in a string:

```go
tmpl, err := expr.NewTemplate(
    `Hello ${user.name}! You have ${len(user.tasks)} task(s).`,
    expr.WithBuiltins(),
)
out, err := tmpl.Render(ctx, env)
```

Each `${...}` is compiled once at construction time and re-evaluated on every
`Render`.

## Higher-order forms

For working with lists there is a small set of always-available forms:
`map`, `filter`, `any`, `all`, `find`, and `count`. Inside the second
argument, `it` is the current element and `index` is its position:

```go
p, err := expr.Compile(`filter(users, it.age >= 18 && index < 10)`)
```

The predicate is re-evaluated per element, so you can compose them
naturally: `any(orders, count(it.items, it.price > 100) > 0)`. These
forms are always registered (they don't need `WithBuiltins`), but you
can shadow any of them by registering a function or env value of the
same name if you want your own behavior.

## Safety

`expr` is meant to run expressions you do not fully trust, so the parser and
evaluator both have bounds (`MaxSourceLength`, `MaxEvalDepth`) to keep a
pathological input from eating your stack. There are no loops, no
assignments, and no way to define new functions from inside an expression.

## More

- [`docs/SPEC.md`](docs/SPEC.md) is the authoritative language reference.
- [`examples/`](examples/) has runnable versions of everything above.

## License

Apache 2.0. See [LICENSE](LICENSE).
