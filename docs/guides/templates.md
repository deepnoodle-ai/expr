# Templates in anger

`NewTemplate` is the smallest string interpolator that still pulls its
weight. You write `${...}` around an expression, the template compiles
every expression **once** at construction time, and `Render` walks the
pre-compiled segments against an env. This guide is about using it
well — the patterns, the stringification rules, and the places where
it pays to preprocess in Go instead of cramming everything inside
`${...}`.

A runnable companion lives in
[`../../examples/templates_in_anger/`](../../examples/templates_in_anger/).

## Compile once, render many

```go
tmpl, err := expr.NewTemplate(
    `Hi ${user.name}! You have ${len(user.tasks)} task(s).`,
    expr.WithBuiltins(),
)
if err != nil { /* bad template */ }

for _, user := range users {
    out, _ := tmpl.Render(ctx, map[string]any{"user": user})
    // ...
}
```

Every `${...}` body is parsed and compiled during `NewTemplate`. Each
`Render` is pure AST walking against the fresh env — no parsing, no
reflection on the template shape itself. If your template is
request-scoped, cache it. If it's static, compile at package init:

```go
var orderConfirm = mustTemplate(`Order ${id}: ${len(items)} item(s) for $${total}`)
```

(`$$` escapes to a literal `$`, so `$${total}` emits the literal `$`
followed by the interpolated `total`. That's how you print a dollar
sign without confusing the template parser.)

## How `${...}` stringifies

Each `${...}` result is converted to a string with these rules:

| Result type | Output                                          |
| ----------- | ----------------------------------------------- |
| `nil`       | empty string                                    |
| `string`    | passthrough                                     |
| anything else | `fmt.Sprintf("%v", v)`                        |

The `nil` → empty-string rule is deliberate: optional fields that
resolve to `nil` produce no output rather than the literal `"<nil>"`.
Matches Jinja / Liquid / Handlebars convention. Downside: a template
can't distinguish "value was nil" from "value was the empty string"
in its output. If you need to, emit a sentinel in the expression:

```
Nickname: ${if(user.nickname == nil, "(none)", user.nickname)}
```

`if(cond, t, f)` is the canonical ternary in expr. Both branches
evaluate eagerly, so reach for `try(...)` or operand-returning
`||` when you need to dodge a runtime error in one branch.

## The list-stringification footgun

`fmt.Sprintf("%v", []any{...})` produces Go's slice syntax:
`[a b c]`. That's almost never what you want in a user-facing
template. Two ways to handle it:

**Option 1 — build the final string inside the expression** with
`sprintf` and friends. Good for small, fixed-shape lists:

```
${sprintf("%s, %s, and %s", items[0], items[1], items[2])}
```

But expr has no `join` builtin and no loop, so arbitrary-length lists
don't work this way.

**Option 2 — preprocess in Go** and pass the joined string back in
through the env. This is the idiomatic answer for variable-length
lists:

```go
env := map[string]any{
    "user": user,
    "taskList": strings.Join(titles(user.OpenTasks), ", "),
}
```

```
Hi ${user.name}! Open tasks: ${taskList}.
```

A template's job is to interpolate values. A Go function's job is
data shaping. When in doubt, reach for Go.

## Multi-line expressions inside `${...}`

Expression bodies can span multiple lines, same rules as elsewhere in
expr: every continuation line must end with an operator, comma, or
opening bracket. Useful for templates that compute a value:

```
Total: ${
    sum
    + tax
    + shipping
}
```

The parser keeps reading until the closing `}`, using `go/scanner` to
track brace depth, so expression bodies with JSON literals or nested
calls work without extra escaping.

## When to register a helper instead

Three signs that you're pushing the template too hard:

1. You're writing the same non-trivial expression in multiple
   templates. Register a Go function and call it from both.
2. You need a `join` or `format-date` or `pluralize`. Not present by
   default; register what you need.
3. The `${...}` body is longer than the literal text around it. The
   template is now a wrapper around an expression — just use
   `Program.Run` directly and format in Go.

A useful helper set for text templates, registered once at startup:

```go
var textOpts = []expr.Option{
    expr.WithBuiltins(),
    expr.WithFunctions(map[string]any{
        "join": func(xs []any, sep string) string {
            parts := make([]string, len(xs))
            for i, v := range xs {
                parts[i] = fmt.Sprintf("%v", v)
            }
            return strings.Join(parts, sep)
        },
        "pluralize": func(n int64, singular, plural string) string {
            if n == 1 { return singular }
            return plural
        },
    }),
}
```

Now your templates can say
`${pluralize(count, "task", "tasks")}` and
`${join(names, ", ")}` without resorting to in-Go preprocessing. Trade
gain: every template that uses these options pays for the full set.
Keep helpers cheap and pure.

## Errors from `Render`

A failed expression inside a `${...}` body bubbles up through
`Render` as an `ErrEvaluate`-wrapped error that names the original
source of the expression. Other segments before the failure are still
evaluated — but the final output is discarded on error, so `Render`
is either "here's the full rendered string" or "here's an error," not
"here's a partially rendered string."

## A few patterns worth knowing

- **Boolean flags:** `${admin && " (admin)"}` emits
  `" (admin)"` when truthy and `false` otherwise; `false` renders as
  `false` via `%v`, which is usually what you want for debugging but
  wrong for user-facing text. Use `if` to give the false branch an
  explicit value: `${if(admin, " (admin)", "")}`.
- **Counts with the right singular/plural:** register a `pluralize`
  helper, or compute the label in Go and pass it in.
- **Currency:** format in Go. Templates are not the right place to
  deal with locale-aware money formatting.
- **Escaping HTML:** expr templates do no escaping. If your output is
  HTML, run the result through `html/template` or escape inside a
  registered helper.
