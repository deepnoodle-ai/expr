# Templates in anger

`NewTemplate` is the smallest string interpolator that still pulls its
weight. You write `${...}` around an expression, the template compiles
every expression **once** at construction time, and `Render` walks the
pre-compiled segments against an env. This guide is about using it well:
the patterns, the stringification rules, custom delimiters, formatters,
and the places where it pays to preprocess in Go instead of cramming
everything inside `${...}`.

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
`Render` is pure AST walking against the fresh env: no parsing, no
reflection on the template shape itself. If your template is
request-scoped, cache it. If it is static, compile at package init:

```go
var orderConfirm = mustTemplate(`Order ${id}: ${len(items)} item(s) for $${total}`)
```

(`$$` escapes to a literal `$`, so `$${total}` emits the literal `$`
followed by the interpolated `total`. That is how you print a dollar
sign without confusing the template parser.)

## How `${...}` stringifies

Each `${...}` result is converted to a string with these rules (a
custom formatter from `WithTemplateFormatter` runs first and can
override any of them):

| Result type                  | Output                                                  |
| ---------------------------- | ------------------------------------------------------- |
| `nil`                        | empty string                                            |
| `string`                     | passthrough unchanged                                   |
| map, slice, array, struct    | compact JSON (see below)                                |
| everything else              | `fmt.Sprintf("%v", v)`                                  |

**JSON rendering for composite values.** Maps, slices, arrays, and
structs (following pointers) now render as compact JSON with HTML
escaping disabled, so `&`, `<`, and `>` survive intact in webhook
payloads and prompts:

```
${config}          // was: map[retries:3 timeout:30s]
                   // now: {"retries":3,"timeout":"30s"}

${files}           // was: [a.go b.go]
                   // now: ["a.go","b.go"]
```

A composite that marshals to a JSON string (e.g. `time.Time`, or a
type with a custom `MarshalJSON`) renders as the unquoted string rather
than as a JSON string literal with surrounding quotes:

```
${createdAt}       // time.Time value renders as: 2026-06-12T09:30:00Z
```

Values JSON cannot encode (cycles, channels, functions) fall back to
`fmt.Sprintf("%v", v)` rather than erroring.

**This is a behavior change from earlier releases.** If you relied on
the previous `fmt.Sprintf("%v", v)` rendering for composite values, use
`WithTemplateFormatter` to restore the old behavior for the types you
need:

```go
expr.WithTemplateFormatter(func(v any) (string, bool) {
    if m, ok := v.(map[string]any); ok {
        return fmt.Sprintf("%v", m), true
    }
    return "", false
})
```

The `nil` to empty-string rule is deliberate: optional fields that
resolve to `nil` produce no output rather than the literal `"<nil>"`.
This matches Jinja/Liquid/Handlebars convention. A template cannot
distinguish "value was nil" from "value was the empty string" in its
output. If you need that distinction, emit a sentinel:

```
Nickname: ${if(user.nickname == nil, "(none)", user.nickname)}
```

`if(cond, t, f)` is the canonical ternary in expr. It is lazy: only
the branch the condition selects evaluates, so guards like
`${if(n != 0, total/n, 0)}` are safe.

## Custom delimiters: `WithTemplateDelimiters`

When the default `${` / `}` collides with your surrounding text (shell
scripts, JavaScript template literals, Kubernetes YAML with `${...}`
references), switch delimiters per template:

```go
tmpl, err := expr.NewTemplate(src, expr.WithTemplateDelimiters("${{", "}}"))
```

With `${{ }}`, text like `echo ${HOME}` passes through as a literal.
Only `${{ ... }}` is treated as an expression:

```
Deploy ${{ service.name }} via: echo ${HOME}
// renders: Deploy api via: echo ${HOME}
```

Delimiter rules:

- The opener must end with one or more `{`; the closer must be the
  matching `}` run. `${{` requires `}}`.
- The opener may not contain `$$` (collides with the escape).
- The `$$` escape applies only when the opener starts with `$`.
  With `${{`, `$${{foo}}` emits the literal text `${{foo}}`.
- With a non-`$` opener like `{{`, no `$$` escape applies and `$$`
  passes through as two literal dollar signs.

Passing `WithTemplateDelimiters` to `Compile` fails with `ErrCompile`.

## Custom value formatter: `WithTemplateFormatter`

Install a hook that runs first for every interpolated result, including
`nil` and strings. Return `false` to fall through to the default chain:

```go
tmpl, err := expr.NewTemplate(src,
    expr.WithBuiltins(),
    expr.WithTemplateFormatter(func(v any) (string, bool) {
        switch x := v.(type) {
        case time.Time:
            return x.Format("Jan 2 2006"), true
        case nil:
            return "N/A", true
        }
        return "", false
    }),
)
```

Passing `WithTemplateFormatter` to `Compile` fails with `ErrCompile`.

## Error messages

Runtime errors from `Render` include a 1-based `line:column` (byte
columns) with the offset as supplementary detail:

```
template: evaluating ${boom} at 3:3 (offset 21): expr: evaluate error: ...
```

Parse-time errors (empty expression, invalid expression, unclosed opener)
use the same format:

```
template: empty expression `${}` at 2:1 (offset 9)
template: invalid expression `${x +}` at 2:3 (offset 11): ...
template: unclosed `${` at 3:1 (offset 18, missing `}`)
```

## Inspecting segments: `Template.Segments()`

`Segments()` returns the parsed segments in source order as
`[]TemplateSegment`. Each segment carries:

- `Literal` and `Source`: one is non-empty (literal text vs. expression
  body); never both.
- `Offset`, `Line`, `Column`: 1-based position in the raw template.
- `Program`: the compiled `*Program` for expression segments, `nil` for
  literals. Call `Program.Identifiers()` to get the env references for
  that expression alone: useful for editor hints, live validation, and
  variable dependency tracking.

```go
tmpl, _ := expr.NewTemplate("Hi ${user.name}, you have ${count} items")
for _, seg := range tmpl.Segments() {
    if seg.Program != nil {
        fmt.Printf("expr at %d:%d: %s (needs: %v)\n",
            seg.Line, seg.Column, seg.Source,
            seg.Program.Identifiers())
    }
}
```

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
calls work without extra escaping. Multi-line bodies work with custom
delimiters too:

```go
tmpl, _ := expr.NewTemplate(
    "header\n${{\n  join(names, \", \")\n}}\nfooter",
    expr.WithTemplateDelimiters("${{", "}}"),
    expr.WithFunctions(expr.StringFuncs()),
)
```

## When to register a helper instead

Three signs that you are pushing the template too hard:

1. You are writing the same non-trivial expression in multiple templates.
   Register a Go function and call it from both.
2. You need a `join`, `format-date`, or `pluralize`. Not present by
   default; register what you need.
3. The `${...}` body is longer than the literal text around it. The
   template is now a wrapper around an expression: just use
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

Now your templates can say `${pluralize(count, "task", "tasks")}` and
`${join(names, ", ")}` without resorting to in-Go preprocessing.

## Errors from `Render`

A failed expression inside a `${...}` body bubbles up through `Render`
as an `ErrEvaluate`-wrapped error that names the original source of the
expression. Other segments before the failure are still evaluated, but
the final output is discarded on error, so `Render` is either "here is
the full rendered string" or "here is an error," not "here is a
partially rendered string."

## A few patterns worth knowing

- **Boolean flags:** `${admin && " (admin)"}` emits `" (admin)"` when
  truthy and `false` otherwise; use `if` to give the false branch an
  explicit value: `${if(admin, " (admin)", "")}`.
- **Counts with the right singular/plural:** register a `pluralize`
  helper, or compute the label in Go and pass it in.
- **Currency:** format in Go. Templates are not the right place for
  locale-aware money formatting.
- **Escaping HTML:** expr templates do no escaping. If your output is
  HTML, run the result through `html/template` or escape inside a
  registered helper.
