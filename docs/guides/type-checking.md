# Type checking

`Run` finds an expression's mistakes when it runs. If expressions are
written ahead of time, in a config file, a rule, or a workflow
definition, that may be days after someone saved them, against data
that happened to hit the broken branch. `Program.Check` finds the same
mistakes when the expression is saved, with nothing to run against.

You describe the types of the env once. `Check` walks the same parsed
expression that `Run` evaluates and reports every problem it finds. A
runnable companion lives in
[`../../examples/type_checking/`](../../examples/type_checking/).

## Describing the env

The env for `Check` is a `map[string]*expr.Type` with the same names as
the env you will pass to `Run`:

```go
env := map[string]*expr.Type{
    "issue": expr.ObjectOf(map[string]*expr.Type{
        "kind":   expr.EnumOf("bug", "feature", "question"),
        "title":  expr.String(),
        "labels": expr.ListOf(expr.String()),
        "score":  expr.Number(),
    }),
    "review": expr.ObjectOf(map[string]*expr.Type{
        "verdict": expr.EnumOf("approve", "reject"),
        "body":    expr.String(),
    }).OrNil(),
    "event": expr.OpenObject(map[string]*expr.Type{
        "type":   expr.String(),
        "sender": expr.OpenObject(map[string]*expr.Type{"login": expr.String()}),
    }),
}
```

The types:

| Type                     | Values                                                     |
| ------------------------ | ---------------------------------------------------------- |
| `expr.Any`               | anything; unchecked                                        |
| `Null()`                 | only nil                                                   |
| `Bool()`, `Number()`, `String()` | a bool, a number (int or float), a string          |
| `EnumOf("a", "b")`       | one of those strings                                       |
| `ListOf(t)`              | a list whose elements are `t`                              |
| `ObjectOf(fields)`       | an object with exactly these fields (closed)               |
| `OpenObject(fields)`     | an object with these fields and possibly others            |
| `t.OrNil()`              | `t`, or nil                                                |

Use `ObjectOf` when you know every field, such as data your own code
builds. Reading a field it does not list is a mistake. Use `OpenObject`
for payloads you only partly know, such as webhooks: unlisted fields are
`Any`. Set `Elem` on an open object (`&expr.Type{Kind: expr.KindObject,
Open: true, Elem: expr.Number()}`) for a map whose keys you don't know
but whose values you do.

`Any`, and a nil `*Type` anywhere in env or `Fields`, turns checking off
for that value and everything read from it. A partly typed env checks
what it can, so you can start with `Any` and add types where mistakes
hurt.

If your data is described by JSON Schema, convert the schema to a
`Type`: `object` with `properties` becomes `ObjectOf` (or `OpenObject`
when `additionalProperties` is not false), and `enum` becomes `EnumOf`.
A property not in `required` becomes `.OrNil()` if your host sets every
missing property to nil before `Run`. If the host leaves the key out
instead, add the property to the object's `Absent` set
(`Absent: map[string]bool{"note": true}`), so reading it needs `?.`.

## Running it

```go
p, err := expr.Compile(src, expr.WithBuiltins(), expr.WithFunctions(expr.StringFuncs()))
if err != nil {
    return err // a syntax error
}
t, err := p.Check(env)
var te *expr.TypeError
if errors.As(err, &te) {
    for _, problem := range te.Problems {
        fmt.Println(problem)
    }
}
```

`Check` returns the result type and, if anything is wrong, a
`*TypeError` that lists every problem. It wraps `expr.ErrType`, so
`errors.Is(err, expr.ErrType)` works. Compile with the same options you
run with: `Check` uses the registered functions to type calls.

The result type is useful too. A host that needs a boolean condition
can require `t.Kind == expr.KindBool`, and a host that stores the result
can check it against what it expects to store.

```text
issue.kind == "bug" && issue.score > 0.5
    ok: bool
map(filter(issue.labels, startsWith(it, "area/")), upper(it))
    ok: list<string>
{"title": issue.title, "kind": issue.kind}
    ok: {kind: "bug" | "feature" | "question", title: string}
```

## What it reports

Every problem is one that `Run` would hit with some input:

```text
issue.titel
    `issue.titel`: no field "titel" in {kind: "bug" | "feature" | "question", labels: list<string>, score: number, title: string} (did you mean "title"?)
issue.kind == "bugfix"
    `issue.kind == "bugfix"`: the value is one of "bug" | "feature" | "question", never "bugfix"
review.verdict
    `review`: may be nil here; use ?. or try(...)
issue.score + issue.title
    `issue.score + issue.title`: cannot add number and string
split(issue.title, 1)
    `1`: split: argument 2 must be string, not number
issue.labels[issue.title]
    `issue.labels[issue.title]`: list index must be a number, not string
```

In full, it reports:

- names that are not in env, a binding (`it`, `index`, a named element)
  or the registered functions;
- fields a closed object does not have;
- operations on the wrong kind of value: arithmetic on strings,
  ordering bools, iterating something that is not a list, indexing a
  list with a string;
- builtin calls with the wrong number of arguments or an argument the
  builtin rejects;
- reads of values that may be nil, and keys that may be absent;
- `==` and `!=` that can never be true, including a string outside an
  enum, and `==` on lists or objects, which `Run` rejects.

The enum check is the one that pays for itself fastest: when the values
of `kind` change, every expression still comparing against the old
value fails its check.

## Nil and absent values

A type marked `.OrNil()` may be nil, and reading through nil fails at
run time. `Check` accepts the three ways to handle it:

```go
review?.verdict                                // ("approve" | "reject")?
try(review.body, "no review")                  // string
review != nil && review.verdict == "approve"   // bool
```

A nil check narrows the value where the check guarantees it: on the
right of `x != nil && ...` and `x == nil || ...`, and in the then
branch of `if(x != nil, ..., ...)`. Narrowing is by the text of the
expression, so `review.verdict` is narrowed by `review != nil` but not
by `r != nil` where `r` happens to hold the same value.

A key may be absent from an object: an unlisted field of an open object
whose `Elem` is typed, a field only some of the joined objects have (see
below), a field in the object's `Absent` set, or `scores[name]` where
`name` is any string. Reading a missing key fails, so `Check` asks for
`?.` or `?[`, which give nil instead.

Objects are joined wherever a value may be one of several: `if(...)`,
`||`, a list literal, and a function typed with `Join`. A field that
every side has is present in the join. A field that only one side has
may be absent, and the type shows it as `name?: type`:

```text
if(review != nil, review, {"verdict": "approve"})
    ok: {body?: string, verdict: "approve" | "reject"}
if(review != nil, review, {"verdict": "approve"}).body
    `if(review != nil, review, {"verdict": "approve"}).body`: field "body" may be absent; use ?.
if(review != nil, review, {"verdict": "approve"})?.body
    ok: string?
```

Absent and nil are different. A field `body?: string` may lack its key,
so reading it needs `?.`. A field `note: string?`, typed `.OrNil()`,
always has its key and its value may be nil: `.` reads it safely, and
only what you do with the value needs a nil check.

## `&&`, `||` and unions

`&&` and `||` return one of their operands, not a boolean (see the
spec's Logical section), and `Check` types them that way:
`issue.title || "untitled"` is a string, and `issue.title || 0` is
`0 | string`, a union. A union is fine to store or compare with `==`,
but an operation that needs one kind reports it:

```text
len(issue.title || 0)
    `issue.title || 0`: len: argument 1 may be number, and must be string or a list or an object
```

## Functions

The builtins and the optional groups (`MathFuncs`, `StringFuncs`,
`CollectionFuncs`) have signatures, so `Check` knows their arguments and
result types. A function you register returns `Any` unless you declare
its type with `WithFuncType`:

```go
p, _ := expr.Compile(`concat(a, b)`, expr.WithFunctions(map[string]any{"concat": concat}))
t, err := p.Check(env, expr.WithFuncType("concat", func(args []*expr.Type) (*expr.Type, error) {
    if len(args) != 2 {
        return nil, fmt.Errorf("concat expects 2 arguments, got %d", len(args))
    }
    return expr.Join(args[0], args[1]), nil
}))
```

The function receives the argument types and returns the result type,
or an error to report a misuse. `Join(a, b)` is the type of a value that
is either `a` or `b`. For two objects, a field only one of them has may
be absent in the join.

Names resolve the way `Run` resolves them: env first, then registered
functions, then the higher-order forms. Registering `count` replaces the
form for `Check` too, and a builtin you replace with your own function
loses the builtin's signature.

## Templates

`Template.Check` checks every `${...}` in a template and returns one
`*TypeError` for all of them:

```go
tmpl, _ := expr.NewTemplate("${issue.title} (${issue.knd})", expr.WithBuiltins())
err := tmpl.Check(env)
// `issue.knd`: no field "knd" in {kind: ..., title: string} (did you mean "kind"?)
```

## What it does not promise

`Check` is static. It does not see values, so:

- a list index can still be out of range at run time;
- an `Any` value is unchecked, and so is everything read from it;
- a registered function without `WithFuncType` returns `Any`;
- the env passed to `Run` must match the types passed to `Check`. If it
  doesn't, `Check` was checking a different program.

Within those limits, `Check` agrees with `Run`: an expression that
passes should not fail with any of the problems listed above. One that
does is a bug in `Check`.
