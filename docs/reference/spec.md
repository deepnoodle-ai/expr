# expr language specification

`expr` is a small expression language for embedding in Go programs that
need to evaluate user-supplied conditions, templates, and parameter
interpolation. Source text is parsed
with Go's `go/parser.ParseExpr`, then walked directly — there is no
bytecode. This document is the authoritative description of what expr
accepts and what each construct means. Behavior outside this document
should be treated as an accident, not a guarantee.

The language borrows Go's expression syntax but is not Go. When we diverge
from Go semantics we call it out explicitly.

## Scope

expr evaluates **expressions**. There are no statements, blocks,
assignments, loops, declarations, or function literals. The grammar
accepted is exactly the subset of `ast.Expr` listed in
[Supported syntax](#supported-syntax).

## Lexical elements

### Integer literals

- Decimal (`42`), hex (`0xFF`, `0Xff`), octal (`0o17`, `0O17`), binary
  (`0b1010`, `0B1010`), and underscore-separated digits (`1_000_000`).
- All integer literals become `int64`. Values outside the `int64` range
  return an `ErrEvaluate` at Run time, not at Compile time — the parser
  accepts them, but evaluation fails.
- A negated literal is read as a single signed value, so
  `-9223372036854775808` (`MinInt64`) evaluates fine even though the
  bare digits overflow on their own.

### Floating-point literals

- Standard Go floats (`3.14`, `.5`, `1e6`, `1E-9`, `0x1p-10`). All float
  literals become `float64`.

### Character literals

- Single-quoted runes (`'a'`, `'\n'`, `'\u00e9'`). A rune literal
  evaluates to its Unicode code point as an `int64`, matching Go. Multi-
  rune or empty char literals return `ErrEvaluate`.

### String literals

- Double-quoted strings and raw backtick strings (`"hello"`,
  `` `hello` ``), with all Go escape sequences inside double-quoted form.

### Boolean and nil literals

- `true`, `false`, `nil` are reserved identifiers. They are not
  shadowable — even if `env` has a key named `true`, it is not reachable.

### Imaginary literals

- Rejected at evaluation with `ErrEvaluate`. expr does not model
  complex numbers.

## Operators

Precedence and associativity come from `go/parser`. They match Go:

| Precedence | Operators                      | Associativity |
| ---------- | ------------------------------ | ------------- |
| 5 (high)   | `*  /  %`                      | left          |
| 4          | `+  -  \|`                     | left          |
| 3          | `==  !=  <  <=  >  >=`         | left          |
| 2          | `&&`                           | left          |
| 1 (low)    | `\|\|`                         | left          |

Unary `!`, `-`, `+` bind tighter than any binary operator. Parentheses
group expressions as usual. The `|` token is the [pipeline
operator](#pipeline-), not bitwise or. Go's remaining bitwise
operators (`&`, `^`, `<<`, `>>`, `&^`) parse but are rejected at
Compile time with `ErrCompile`.

### Arithmetic (`+ - * / %`)

- Both operands `int64` → `int64` result. `+` on two strings is
  concatenation. Integer `/` and `%` by zero return `ErrEvaluate`.
- Any mix of int and float promotes both to `float64`. `%` on floats
  uses `math.Mod`. Float `/` and `%` by zero return `ErrEvaluate`.
- Integer arithmetic is overflow-checked: `+`, `-`, and `*` whose exact
  result does not fit in `int64` return `ErrEvaluate`, as do `-MinInt64`
  and `MinInt64 / -1`. Nothing wraps silently and nothing panics.
- `+` on any other type combination is an error.

### Comparison (`== != < <= >= >`)

- String vs string uses `strings` comparison.
- Numeric comparisons work across any combination of integer kinds and
  floats (see [Equality](#equality)).
- Other comparable Go types use native equality when both sides are the
  same type. Mismatched-but-comparable types yield `false` without an
  error. Uncomparable types (slices, maps, funcs) return `ErrEvaluate`.

### Logical (`&& ||`)

- Short-circuit. `falsey && X` does not evaluate `X`; `truthy || X`
  does not evaluate `X`. Whether an operand is "truthy" is decided by
  the [truthiness](#truthiness) rules.
- The result is the **deciding operand**, not a coerced bool. This
  matches Python `and`/`or`, JavaScript `&&`/`||`, Lua, and Ruby:

  ```
  x || y    // x if truthy, else y
  x && y    // x if falsey, else y
  ```

  So `"ada" || "(none)"` is `"ada"`, `"" || "(none)"` is `"(none)"`,
  and `count || 0` falls back to `0` only when `count` is falsey.
  Where a strict bool is required, wrap with `bool(...)`.

### Pipeline (`|`)

`a | f(x, y)` evaluates exactly as `f(a, x, y)`: the left side becomes
the first argument of the call on the right side. The rewrite happens once at Compile time,
so the pipeline has no runtime semantics of its own: a piped call is
the call, including for the higher-order special forms and their lazy
evaluation rules. Pipes chain left to right:

```
checks | filter(!it.ok) | map(sprintf("- %s: %s", it.name, it.msg)) | join("\n")
// identical to:
join(map(filter(checks, !it.ok), sprintf("- %s: %s", it.name, it.msg)), "\n")
```

The pipe is a deliberate deviation from expr's strict-Go-subset
identity: in Go this token means bitwise or. Before v1.2.0, expr
rejected `|` at Compile time as an unsupported bitwise operator, so no
previously-compilable expression changes meaning under the pipeline
reading. The full design rationale lives in
[RFC 0001](../rfcs/0001-pipe-operator.md).

The right-hand side must be written as a call. Anything else (an
identifier, a selector, an index, an optional access, a parenthesized
expression, a literal) is rejected at Compile time with `ErrCompile`:

```
xs | len()        // ok: len(xs)
xs | f            // ErrCompile: "f" is not a call (did you mean to write f(...)?)
xs | filter       // ErrCompile: "filter" is a special form, did you mean
                  //   to write filter(predicate)?
xs | f()[0]       // ErrCompile: the right side parses as the index f()[0]
(xs | f())[0]     // ok: index the piped result
xs | a?.b         // ErrCompile: optional access is not a call
```

Because the rewrite is purely syntactic, it composes with every call
shape: the iterating forms (`xs | filter(it > 0)` is
`filter(xs, it > 0)`), the three-arg named-binding forms
(`orders | filter(o, o.paid)`), the lazy forms (`v | try(fallback)` is
`try(v, fallback)`, with `v` still evaluated under try's error
handling), env-provided callables, and selector calls on env values.
`map` and `if` work even though they are Go keywords; the keyword
rewrite runs before parsing.

Pipe targets follow the same resolution rules as any other call: an
unregistered name compiles (the env may provide the callable at Run)
and fails at evaluation with the usual unknown-function error.

**Precedence.** `|` keeps Go's precedence: level 4, the same as `+`
and `-`. That is tighter than comparisons, `&&`, and `||`, and looser
than `*`, `/`, `%`, and unary operators. Postfix syntax (calls, `.field`,
`[idx]`, `?.`, `?[`) binds tighter still. Consequences:

```
xs | count(it > 1) == 2      // (xs | count(it > 1)) == 2: pipe, then compare
n + 1 | double()             // (n + 1) | double(): same level, left-assoc
n | double() + 1             // (n | double()) + 1
ok && xs | any(it > 0)       // ok && (xs | any(it > 0))
x > 2 | if("big", "small")   // ErrCompile: ambiguous, see below
(x > 2) | if("big", "small") // parenthesize to pipe a comparison
```

One shape is rejected outright rather than silently mis-grouping: a
bare pipe as the **right** operand of a comparison. `a == b | f()`
parses as `a == (b | f())`, with the pipe consuming the comparison's
right operand, so expr fails compilation with an "ambiguous expression"
error demanding parentheses; write `(a | f()) == b` or `a == (b | f())`
to state which one you meant. A pipe on the *left* of a comparison
(`xs | count(it > 1) == 2`) is the useful, unambiguous order and needs
no parentheses.

**Optional access.** An optional-access result pipes normally
(`user?.name | upper()` is `upper(user?.name)`; a nil receiver pipes
`nil` into the call, which the iterating forms treat as an empty
list). The reverse needs parentheses: `?.` binds tighter than `|`, so
accessing a field on a piped result is written
`(xs | find(it.ok))?.name`.

### Unary

- `!x` is logical negation using truthiness (so `!0` is `true`).
- `-x` negates a numeric value; any other type is an error.
- `+x` is a numeric no-op; any other type is an error.

## Equality

`==` and `!=` use a **loose** comparison:

1. `nil == nil` is `true`.
2. `X == nil` is `true` if `X` is a typed nil value of a nilable kind
   (chan, func, interface, map, pointer, slice). This means a `(*T)(nil)`
   or `[]any(nil)` stored in `any` compares equal to the literal `nil`.
3. If both sides are any combination of integer or float kinds, they
   convert to `float64` and compare. `int32(7) == int64(7) == float64(7)`
   is `true`.
4. Otherwise: if both runtime types are comparable, Go's `==` is used.
   Different types compare as `false` (no error). Uncomparable types
   return `ErrEvaluate`.

`!=` is exactly `!(==)`.

## Truthiness

Used by `!`, `&&`, `||`, and `bool(v)`. Delegated to `IsTruthy`,
which treats these as **falsey**:

- `nil`
- `false`
- Zero numeric values of any integer or float kind
- Empty string
- Empty slice, array, or map
- A nil channel, function, interface, map, pointer, or slice

Everything else is truthy. String content is not inspected:
`bool("false")` is `true` because the string is non-empty. Callers who
need to parse boolean strings should do so explicitly.

## Identifier resolution

A bare identifier `foo` is resolved in this order:

1. The literals `true`, `false`, `nil`.
2. The `env` argument:
   - If `env` is `nil`, skip.
   - If `env` is `map[string]any`, look up `env["foo"]`.
   - If `env` is any other map with string keys, look up via reflection.
   - If `env` is a struct or a pointer to a struct, take the exported
     field named `foo`; if no field matches, take the bound method
     named `foo`. **Fields beat methods.**
3. The functions registered via the `Option`s passed to `Compile`
   (`WithBuiltins`, `WithFunctions`, or any combination).
4. Otherwise: `ErrEvaluate: undefined identifier`.

Unexported struct fields are **not** reachable by name. Attempting to
select one returns `ErrEvaluate: field ... not found` — we deliberately
do not panic.

Struct tags are ignored by default. Opt in with
`expr.WithStructTags("expr", "json")` (or the alias `WithFieldTags`) to
resolve exported struct fields by tag before Go field names. Tag names
are tried in the order configured, and the first non-empty tag name wins
for that field:

```go
type User struct {
    DisplayName string `expr:"name" json:"display_name"`
    Email       string `json:"email,omitempty"`
    SourceID    string `json:",omitempty"`
    Secret      string `expr:"-"`
}

p, _ := expr.Compile(`user.name == "Ada" && user.email != ""`,
    expr.WithStructTags("expr", "json"))
```

Tag options after a comma are ignored, so `json:"email,omitempty"`
resolves as `email`. An empty tag name such as `json:",omitempty"`
falls back to the Go exported field name. `expr:"-"` hides the field
entirely when the `expr` tag is configured. Other `"-"` tags only hide
that tag lookup; lower-priority tags or the Go field name may still
expose the field.

Tag lookup uses strict precedence per field. In the example above,
`user.name` resolves to `DisplayName`, while `user.display_name` and
`user.DisplayName` do not. If two exported fields resolve to the same
expression name, field access returns an `ErrEvaluate` ambiguity error
rather than choosing one by declaration order.

## Selectors (`x.y`)

`x.y` evaluates `x`, then looks up `y` on the result:

- Nil receiver → `ErrEvaluate`.
- `map[string]any` or any map with string keys → `y` is a key. Missing
  keys return an error (not a zero value).
- Map with non-string keys → `ErrEvaluate`.
- Struct → the exported field `y`, using configured struct tags if any,
  or `ErrEvaluate` if missing.
- Pointer to struct → dereferenced and re-tried. Nil pointer →
  `ErrEvaluate`.
- Anything else → `ErrEvaluate`.

Selectors chain left-to-right: `a.b.c` is `(a.b).c`. Selector chains are
limited by [evaluation depth](#limits-and-safety).

## Index expressions (`x[i]`)

- `map[string]any`: `i` must be a string. Missing key → `ErrEvaluate`.
- Other maps: `i` is converted to the map's key type if assignable or
  convertible. A nil index on a typed map is an error (not a panic).
- Slice, array: `i` must be an integer or an integer-valued float
  (`xs[1.0]` works, `xs[1.5]` is an error). Negative indices and
  indices `>= len(x)` return `ErrEvaluate` (expr does not support
  Python-style negative indexing).
- String: `i` selects the `i`-th **rune** (Unicode code point) and
  returns it as a one-rune string. `len(s)` is also in runes, so indexing
  and length stay consistent for non-ASCII strings. Integer-valued
  floats are accepted here as well.
- Anything else → `ErrEvaluate`.

Slice expressions (`x[a:b]`), full slices (`x[a:b:c]`), and type
assertions (`x.(T)`) are rejected.

## Calls (`f(a, b, ...)`)

### Call targets

The callable is resolved in order:

1. If the target is a bare identifier, `lookupEnv` runs, then the
   engine's functions.
2. If the target is a selector `x.f`, expr evaluates `x` and then
   looks for a method, struct field, or map entry named `f` on it.
3. Any other call target (index expression, call expression, paren
   expression, optional access) is rejected at Compile time with
   `ErrCompile: call target must be a function name or selector`.

### Method resolution order (for selector calls)

Given `x.f()` where `x` evaluates to `recv`:

1. If `recv` is `map[string]any`, the entry `recv["f"]` is used. Missing
   → error.
2. Else, `reflect.Value.MethodByName("f")` on the pointer or original
   receiver (so pointer-receiver methods are visible).
3. Else, if the dereferenced kind is `Struct`, the exported field `f`
   (as a function value), using configured struct tags if any.
4. Else, if the dereferenced kind is a `Map` with string keys, the entry
   `recv["f"]`.
5. Else: `ErrEvaluate: method ... not found`.

Nil pointer receivers produce `ErrEvaluate: cannot call ... on nil pointer`
before any reflect call that would panic.

### Argument handling

- Each argument is evaluated left to right. There is no support for the
  `...` spread syntax (passing a slice as variadic args).
- Non-variadic functions must receive exactly `NumIn()` arguments.
- Variadic functions accept `len(args) >= NumIn()-1`.
- expr represents ints as `int64` and floats as `float64`. It performs
  **range-checked** conversion to the declared parameter type. For
  example, `int64(10)` → `int8` succeeds; `int64(300)` → `int8` is an
  error (not a silent wraparound). Negative → unsigned fails.
  `float64`↔`float32` is allowed and may lose precision.
- Nil may be passed for any nilable-kind parameter (interface, pointer,
  map, slice, chan, func). Passing nil to a non-nilable parameter is an
  error.
- Any other conversion uses `reflect.Value.ConvertibleTo` + `Convert`.
  A nil function value (`var fn func(); fn == nil`) is detected and
  reported; it is never invoked.

### Return signatures

Supported:

- `func(...)` → result is `nil`.
- `func(...) T` → result is `T`.
- `func(...) (T, error)` → result is `T`; if the error is non-nil it
  replaces the normal result.

Anything else (two non-error returns, three returns, `(error, T)`)
returns `ErrEvaluate`. Errors from functions propagate wrapped inside
an `ErrEvaluate` chain via `errors.Is`.

## Builtins

By default no functions are registered. Opt in to the standard set
below by passing `expr.WithBuiltins()` as an `Option`, register your
own with `expr.WithFunctions(...)`, or combine both:

```go
p, err := expr.Compile(`upper(user.name)`,
    expr.WithBuiltins(),
    expr.WithFunctions(map[string]any{"upper": strings.ToUpper}),
)
v, err := p.Run(ctx, env)
```

Options apply in order, so a later `WithFunctions` wins over an earlier
`WithBuiltins` for any shared name. The same options passed to `Compile`
are baked into the returned `*Program`, so `Run` needs no further
configuration.

`WithStructTags("expr", "json")` / `WithFieldTags(...)` controls struct
field names only. It does not change map key lookup, method names, or
registered function names.

The standard set is:

| Name            | Signature                       | Notes |
| --------------- | ------------------------------- | ----- |
| `len(v)`        | `(any) -> int, error`           | Rune count for strings, element count for slice/array/map/chan, `0` for nil, error otherwise. |
| `string(v)`     | `(any) -> string`               | Passthrough for strings, `fmt.Sprintf("%v", v)` otherwise, `""` for nil. |
| `int(v)`        | `(any) -> int64, error`         | Numeric values convert (float truncates toward zero). Strings are parsed strictly with `strconv.ParseInt` base-10 (trimmed whitespace, no `0x`, no trailing garbage). |
| `float(v)`      | `(any) -> float64, error`       | Like `int`, but `strconv.ParseFloat` 64-bit. |
| `bool(v)`       | `(any) -> bool`                 | Same semantics as [truthiness](#truthiness). |
| `contains(h,n)` | `(any, any) -> bool, error`     | Substring for string haystacks, element membership for slices/arrays (using [loose equality](#equality)), key presence for string-keyed maps. |
| `has(m,k)`      | `(any, string) -> bool, error`  | True if map `m` has key `k`. Maps only. Nil → `false`. |
| `keys(m)`       | `(any) -> []any, error`         | Sorted string keys. Other key types → error. |
| `entries(m)`    | `(any) -> []any, error`         | Sorted key-value pairs of a string-keyed map. Each element is `map[string]any{"key": k, "value": v}`. Nil → `nil`. Other key types → error. Useful for iterating maps through the higher-order forms: `map(entries(m), e, e.key + "=" + e.value)`. |
| `lower(s)`      | `(string) -> string`            | `strings.ToLower`. |
| `upper(s)`      | `(string) -> string`            | `strings.ToUpper`. |
| `sprintf(f,...)`| `(string, ...any) -> string`    | `fmt.Sprintf`. |

`if(cond, then, else)` is not a builtin: it is a lazily evaluated
[special form](#higher-order-special-forms), always available without
`WithBuiltins`.

### Registration validation

`WithFunctions` entries are validated when `Compile` applies its
options. A nil entry, a value that is not a Go function, or a function
with an unsupported signature (more than two return values, or a
second return that is not `error`) fails `Compile` with `ErrCompile` —
the mistake surfaces at load time rather than on the expression's
first call. Constants belong in the env, not in `WithFunctions`.

### Optional builtin groups

Three opt-in helper sets extend the default builtins without widening
a minimal sandbox. Each returns a fresh `map[string]any` for
`WithFunctions`; all entries are deterministic and side-effect free:

```go
p, err := expr.Compile(src,
    expr.WithBuiltins(),
    expr.WithFunctions(expr.MathFuncs()),
    expr.WithFunctions(expr.StringFuncs()),
    expr.WithFunctions(expr.CollectionFuncs()),
)
```

`expr.MathFuncs()`:

| Name              | Signature                       | Notes |
| ----------------- | ------------------------------- | ----- |
| `min(a, ...)`     | `(num, ...num) -> num, error`   | Smallest argument. `int64` when every argument is integral, `float64` otherwise. At least one argument. |
| `max(a, ...)`     | `(num, ...num) -> num, error`   | Largest argument; same typing rule as `min`. |
| `abs(n)`          | `(num) -> num, error`           | Absolute value. `int64` in → `int64` out; `abs(MinInt64)` errors like unary minus. |
| `floor(v)`        | `(num) -> num, error`           | `math.Floor` for floats; integers pass through unchanged. |
| `ceil(v)`         | `(num) -> num, error`           | `math.Ceil`; integers pass through. |
| `round(v)`        | `(num) -> num, error`           | `math.Round` (half away from zero); integers pass through. |

`expr.StringFuncs()`:

| Name                    | Signature                            | Notes |
| ----------------------- | ------------------------------------ | ----- |
| `trim(s)`               | `(string) -> string, error`          | `strings.TrimSpace`. |
| `split(s, sep)`         | `(string, string) -> []any, error`   | `strings.Split`; elements are strings. |
| `join(xs, sep)`         | `(list, string) -> string, error`    | Elements must be strings. Nil list → `""`. |
| `replace(s, old, new)`  | `(string, string, string) -> string, error` | `strings.ReplaceAll`. |
| `startsWith(s, prefix)` | `(string, string) -> bool, error`    | `strings.HasPrefix`. |
| `endsWith(s, suffix)`   | `(string, string) -> bool, error`    | `strings.HasSuffix`. |

`expr.CollectionFuncs()`:

| Name              | Signature                       | Notes |
| ----------------- | ------------------------------- | ----- |
| `first(xs)`       | `(list) -> any, error`          | First element; `nil` for nil or empty lists. |
| `last(xs)`        | `(list) -> any, error`          | Last element; `nil` for nil or empty lists. |
| `sum(xs)`         | `(list) -> num, error`          | Numeric sum. `int64` (overflow-checked) when every element is integral, `float64` otherwise. Nil/empty → `0`. |
| `slice(xs, i, j)` | `(list\|string, int, int) -> list\|string, error` | Half-open range `[i, j)`. Lists yield `[]any`; strings slice by rune. Negative indices count from the end; out-of-range bounds clamp; `i > j` → empty. Covers the rejected `xs[i:j]` syntax. |
| `sort(xs)`        | `(list) -> []any, error`        | Ascending stable sort. All elements must be numbers (any int/float mix, compared numerically) or all strings (lexicographic). Elements are not converted: ints stay ints. Nil/empty → `[]any{}`. Mixed or non-comparable types → error. Never mutates the input. |
| `reverse(xs)`     | `(list) -> []any, error`        | Reversed copy. Never mutates the input. Nil/empty → `[]any{}`. |

## Higher-order special forms

expr provides a fixed set of **special forms**. Unlike the standard
builtins, special forms are always registered and do not require
`WithBuiltins`. They look like ordinary function calls in source, but
their arguments are not all evaluated eagerly. Eight of them iterate
lists (`map`, `filter`, `flatMap`, `any`, `all`, `find`, `count`,
`sortBy`); `try` and `if` instead use laziness for error recovery and
branching.

### Two-arg vs. three-arg iterating forms

Every iterating form accepts two call shapes:

```
form(collection, body)           // two-arg: binds `it` and `index`
form(collection, name, body)     // three-arg: binds `name` and `index`
```

**Two-arg form.** The body is re-evaluated once per element with `it`
(current element) and `index` (0-based position as `int64`) in scope.
Both shadow any outer identifier of the same name.

**Three-arg form.** The second argument is the element binding name.
It must be a plain identifier. Only the chosen name and `index` are
bound inside the body; `it` is **not** bound, so an enclosing
two-arg form's `it` remains reachable from inside the body. This
closes the gap where nested `it`-based forms offered no way to refer
to the outer element.

Example: outer named form, inner two-arg — `r` is the outer element,
`it` is the inner element from the two-arg `map`:

```
map(reviews, r, join(map(r.comments, r.author + "/" + it), ","))
```

Example: outer two-arg, inner named — outer `it` stays visible from
inside the named form body because the named form does not bind `it`:

```
map(reviews, map(it.comments, c, it.author + "/" + c))
```

Bindings shadow env names lexically. An inner named form whose name
matches an outer named form's name shadows the outer one for the
duration of its own body:

```
map(users, u, map(u.orders, u, u))   // inner u shadows outer u
```

### Reserved binding names

The following identifiers may not be used as a binding name in the
three-arg form. Any of them produces `ErrEvaluate: <form> binding
cannot be named "<name>"`:

- `it`, `index` (already have special meaning in the two-arg form)
- `true`, `false`, `nil` (reserved literals)
- `map`, `if` (Go keyword rewrites used internally by expr)

A non-identifier in the name position (selector, call, parenthesized
expression) produces `ErrEvaluate: <form> binding must be a plain
identifier, got <src>`. Wrong arity produces `ErrEvaluate: <form>
expects 2 arguments (collection, predicate) or 3 (collection, name,
predicate), got N`.

### Form table

| Name                       | Returns          | Description |
| -------------------------- | ---------------- | ----------- |
| `map(list, expr)`          | `[]any`          | New list with `expr` evaluated per element. |
| `map(list, name, expr)`    | `[]any`          | Same, binding element as `name`. |
| `filter(list, pred)`       | `[]any`          | Elements where `pred` is truthy, in original order. |
| `filter(list, name, pred)` | `[]any`          | Same, binding element as `name`. |
| `flatMap(list, expr)`      | `[]any`          | Like `map`, but a list body result is spliced element-by-element; nil splices as nothing; non-list appends as one element. Splicing is one level deep only. Strings are never split to runes. |
| `flatMap(list, name, expr)`| `[]any`          | Same, binding element as `name`. |
| `any(list, pred)`          | `bool`           | `true` if `pred` is truthy for any element; short-circuits. |
| `any(list, name, pred)`    | `bool`           | Same, binding element as `name`. |
| `all(list, pred)`          | `bool`           | `true` if `pred` is truthy for every element; short-circuits. Empty list → `true`. |
| `all(list, name, pred)`    | `bool`           | Same, binding element as `name`. |
| `find(list, pred)`         | element or `nil` | First element for which `pred` is truthy, or `nil`. |
| `find(list, name, pred)`   | element or `nil` | Same, binding element as `name`. |
| `count(list, pred)`        | `int64`          | Number of elements for which `pred` is truthy. |
| `count(list, name, pred)`  | `int64`          | Same, binding element as `name`. |
| `sortBy(list, key)`        | `[]any`          | Stable sort of a copy of list by the key expression. Keys must be all numbers or all strings. |
| `sortBy(list, name, key)`  | `[]any`          | Same, binding element as `name`. |
| `try(value, default)`      | value or default | Evaluates `value`; returns `default` if `value` raised an `ErrEvaluate`. The `default` is only evaluated when the primary fails. |
| `if(cond, then, else)`     | `then` or `else` | Lazy ternary: evaluates `cond`, then only the branch selected by `cond`'s [truthiness](#truthiness). |

The `list` argument must be a slice or array (or `nil`, which is
treated as empty). Maps are not iterated by these forms; use
`keys(m)` or `entries(m)` (both in `WithBuiltins`) to drive map
iteration manually.

### flatMap splicing rules

`flatMap(xs, body)` / `flatMap(xs, name, body)`:

- Body result is `[]any` or a typed slice/array: each element is
  appended individually to the output (splice).
- Body result is `nil`: nothing is appended (nil is treated as an
  empty list, matching `iterItems`).
- Body result is any other value, including a string: appended as a
  single element. Strings are never split into runes.
- Splicing is one level deep only: `flatMap([[1, [2]], [3]], it)`
  yields `[1, [2], 3]`, not `[1, 2, 3]`.

### sortBy key comparison rules

`sortBy` evaluates the key expression once per element, then sorts a
copy of the list using a stable sort. Key comparison follows the same
rules as the `<` operator and `sort`:

- All numbers (any int/float mix): both-integral values compare as
  `int64`; any float in either operand promotes both to `float64`.
- All strings: lexicographic order.
- Mixed or non-comparable key types: `ErrEvaluate` naming the
  offending element and its type. The sort always returns a fresh copy;
  the input is never mutated.

Key-expression errors are reported like other predicate errors:
`sortBy predicate '<key>' failed on element N: ...`.

### Predicate error wrapping

When a predicate raises an `ErrEvaluate`, the iterating forms wrap
the error with the form name, the predicate's source text, and the
failing element's index. A typo inside `map(users, it.Nmae)` therefore
reads:

```
map predicate `it.Nmae` failed on element 0: ... field "Nmae" not found ...
```

Nested forms each contribute their own layer, so the wrapping reads
top-down through the iteration tree. The internal `map` rewrite is
reversed in the printed predicate, so users see the source as they
typed it. The wrapping preserves the underlying error chain
(`errors.Is(err, ErrEvaluate)` still matches); context cancellation
passes through unchanged.

### `try` and `if`

`try(value, default)` does not iterate a list and binds no implicit
`it`/`index`. Both arguments are arbitrary expressions. The `default`
is **only** evaluated when `value` failed, so users can supply
expensive or side-effecting fallbacks safely.

`try` traps anything wrapping `ErrEvaluate`: missing fields/keys, nil
selectors, out-of-range indices, type-coercion failures from `int`,
`float`, etc. It does **not** trap raw `context.Canceled` or
`context.DeadlineExceeded` (cancellation must remain observable), nor
anything wrapping `ErrCompile`. Errors from evaluating the `default`
expression itself surface unchanged. Combine with operand-returning
`||` for the common case of presenting `nil` as a sentinel:

```
try(find(events, it.kind == "purchase")?.user, "—")
try(int(input), 0) > 0
try(user.nickname, nil) || "(none)"
```

`if(cond, then, else)` is the other non-iterating form. It is the
language's ternary: the condition decides via truthiness, and only
the selected branch evaluates, so an error in the untaken branch is
never raised. That makes the guard idiom safe:

```
if(n != 0, total / n, 0)        // no division-by-zero from the guard
if(user != nil, user.name, "?") // no nil-selector error
```

All three arguments see the enclosing scope; `if` binds no implicit
`it`/`index`. (`if` is a Go statement keyword, so like `map` it is
rewritten to an internal token before parsing, invisible except that
it is why `if` can be called at all.)

Special-form names can be shadowed: if `WithFunctions` registers a
function with the same name, or the caller's env contains an entry
with that name, the user binding wins. This lets consumers replace
the built-in behavior when they need to. A shadowed `if` or `try`
goes through the ordinary call path, where all arguments evaluate
eagerly. Three-arg calls to shadowed forms are equally shadowed: a
user function named `flatMap` receiving three arguments is called as
an ordinary function with three evaluated arguments. The `map`
keyword is special because Go's parser reserves it: expr rewrites
`map` to an internal token before parsing so the form can still be
called as `map(xs, it * 2)`, and translates it back for error
messages and method lookups.

## Optional access (`?.` and `?[`)

`?.field` and `?[idx]` are pre-parse rewrites for "look this up, but
return `nil` if the receiver is missing or the lookup falls off the
end." They cover the common case where a JSON-shaped env may or may
not include a particular branch, without forcing the user to wrap
every access in `try(...)`.

```
user?.profile?.nickname || "(none)"
events?[0]?.user
config?.feature?.enabled
```

The semantics:

- If the receiver is `nil`, the result is `nil` and the right-hand
  side is not consulted.
- For `?.`, a missing struct field or absent map key resolves to
  `nil`.
- For `?[`, a missing map key or an out-of-range slice/string index
  resolves to `nil`.
- A wrong-kind error (selecting on a value that is not a struct or
  map, indexing a slice with a non-integer, indexing into a map with
  the wrong key type) still surfaces as `ErrEvaluate`. `?.` and `?[`
  swallow "not there" errors, not "real bugs."

The receiver can be any primary expression: an identifier, a selector
or index chain, a call (including `map(...)` and `if(...)`), a
parenthesized group, or an array/object literal (`[1, 2]?[0]`,
`{"a": 1}?.a`). `?.map` and `?.if` work the same way `.map` and
`.if` do. Calling the result of an optional access (`a?.b()`) is not
supported and is rejected at Compile time.

`?.` and `?[` are pure source-level sugar. The rewrite happens
before the parser sees the source, so they behave like calls on
internal sentinel functions (`__try_select__` and `__try_index__`).
Users do not interact with those names directly, but they may
appear in error chains for diagnostic purposes. Strings, runes, and
comments are not rewritten — `?.` written inside `"..."` or a
comment is preserved verbatim.

A nil-coalescing `??` operator is **not** provided. The combination
of `?.` / `?[`, operand-returning `||`, and `try(value, default)`
covers the same use cases. When the LHS is a meaningful falsey value
that should be kept (`0`, `""`, `[]`), use `try(x, default)`
explicitly.

## Helpful errors

expr annotates "not found" errors with a short hint drawn from the
names actually in scope:

- `undefined identifier "usernmae" (did you mean "username"?)`
- `field "Nmae" not found on User (did you mean "Name"?)`
- `key "naem" not found (did you mean "name"?)`

The hint is computed by Levenshtein distance against the set of
candidate names (env keys/fields/methods, registered functions, and
the higher-order form names). When there is no close match but the
candidate set is small enough to list compactly, the hint instead
lists the available names. When neither condition is useful, the
original error is returned unchanged so callers can still pattern-
match on it.

A higher-order form referenced as a value gets a tailored hint with
the call signature, so users see what shape the form expects rather
than a self-referential "did you mean":

- `undefined identifier "count" ("count" is a special form, did you mean to call count(xs, predicate)?)`
- `undefined identifier "try" ("try" is a special form, did you mean to call try(value, default)?)`

## Limits and safety

expr is meant to evaluate untrusted expression text without panicking.
The following are hard limits:

- **Max source length**: `MaxSourceLength` (64 KiB by default). `Compile`
  rejects longer input with `ErrCompile`.
- **Max evaluation depth**: `MaxEvalDepth` (256 by default). Expression
  trees deeper than this return `ErrEvaluate: expression nested too
  deeply`. This caps selector chains (`a.b.c...`), nested binary
  expressions, and nested calls.
- **Evaluation budget** (opt-in): `WithEvalBudget(n)` bounds the total
  number of AST nodes a single `Run` may evaluate, counting every
  per-element re-evaluation of a higher-order predicate. Exhausting
  the budget returns `ErrEvaluate: evaluation budget exceeded`. This
  is the only deterministic CPU bound — source length and depth limits
  do not stop nested higher-order forms from multiplying work
  (`map(xs, map(xs, map(xs, it)))` is `len(xs)³` predicate
  evaluations from a ~30-byte expression). The default is unlimited.

Under adversarial input, expr must never:

- Panic (nil-deref, slice bounds, reflection on invalid values).
- Enter unbounded recursion.
- Silently produce out-of-range numeric conversions for call arguments.
- Expose unexported struct fields.

See `FuzzCompile` and `FuzzEval` for the enforcing test targets.

## Cancellation and termination guarantees

expr has no loop or recursion constructs of its own — `go/parser`
accepts only expressions, the evaluator makes strict downward progress
through the AST, and `MaxEvalDepth` caps the tree. Therefore a program
with no registered functions and no env-method calls has a hard
termination bound proportional to the AST size.

`Program.Run(ctx, env)` adds cooperative cancellation on top of that bound:

- Every AST node visit checks `ctx.Err()` before dispatching. A
  cancelled or expired context causes the next node to return the raw
  `context.Canceled` / `context.DeadlineExceeded` error without wrapping
  it in `ErrEvaluate`, so callers can match with `errors.Is`.
- `Run` is the only evaluation entry point — there is no ctx-less form.
- Passing a nil `ctx` to `Run` falls back to `context.Background`.

**Automatic context injection for registered functions.** When a
function registered via `WithFunctions` declares `context.Context` as
its *first* parameter, expr passes the live context automatically.
The user-visible call surface excludes that parameter: arity checks,
argument positions, and error messages all refer to the caller's args.
Injection only fires when `context.Context` is the first parameter;
later positions are treated as ordinary arguments.

```go
p, _ := expr.Compile(`fetch("https://...")`, expr.WithFunctions(map[string]any{
    "fetch": func(ctx context.Context, url string) (string, error) { ... },
}))
// expression calls it as fetch("https://..."), the ctx from Run
// is threaded through automatically.
```

**Non-goal: forced termination of blocking user code.** Go provides no
mechanism to kill a goroutine. If a registered function or env method
ignores its context and blocks forever, expr cannot interrupt it —
that goroutine will not return until the user code chooses to. The
library deliberately does not wrap evaluation in a `select` on
`ctx.Done()` because early-returning the caller while the user code
keeps running would silently leak goroutines and hide real bugs in
caller code. Well-behaved integrations pass `context.Context` through
to any blocking call.

## Supported syntax

Only these `ast.Expr` node kinds are accepted; everything else returns
`ErrEvaluate: unsupported syntax ...`:

- `*ast.BasicLit` — literals
- `*ast.Ident` — identifiers
- `*ast.ParenExpr` — `( x )`
- `*ast.UnaryExpr` — `!x`, `-x`, `+x`
- `*ast.BinaryExpr` — arithmetic, comparison, logical, pipeline
  (`a | f(x)`, desugared to `f(a, x)` at Compile time)
- `*ast.SelectorExpr` — `x.y`
- `*ast.IndexExpr` — `x[i]`
- `*ast.CallExpr` — `f(a, b, ...)`
- `*ast.CompositeLit` — restricted to `[]any{...}` and `map[string]any{...}`;
  see [Composite literals](#composite-literals)

Explicitly **not** supported (parses, but rejected at Compile time
with `ErrCompile`):

- Slice expressions (`x[a:b]`, `x[a:b:c]`)
- Type assertions (`x.(T)`)
- Composite literals with any type other than `[]any` or `map[string]any`
  (e.g. `[]int{1, 2}`, `[3]int{}`, `T{...}`, `map[int]string{}`)
- Function literals (`func() {}`)
- Channel ops (`<-ch`, `ch <- v`)
- Pointer/address ops (`*x`, `&x`)
- Bitwise operators (`& ^ << >> &^`). The `|` token is the
  [pipeline operator](#pipeline-); a `|` whose right side is not a
  call is rejected at Compile time.
- Imaginary number literals (`1i`)
- Spread call arguments (`f(xs...)`)
- Label and selector type names (`pkg.Type`)

## Composite literals

expr evaluates two composite-literal shapes at run time:

- `[]any{...}` — produces a `[]any`. Elements are evaluated left to right.
  Keyed elements (`{0: x}`) are rejected.
- `map[string]any{...}` — produces a `map[string]any`. Each element must be
  a `key: value` pair; the key expression is evaluated and must yield a
  `string`. Duplicate keys are last-write-wins (Go's normal map behavior).

Any other composite-literal form (`[]int{}`, `[3]int{}`, `map[int]any{}`,
struct literals, etc.) is rejected at `Compile` with `ErrCompile`. expr
is untyped at the value level, so widening the accepted set would not
change what the evaluator can represent.

### JSON-style literals

expr accepts bare bracket/brace literals like `[1, 2, 3]` or
`{"name": "ada"}` directly. Before parsing, every source string is run
through a token-based rewrite (implemented in `internal/jsonlit`):

- `[a, b, c]` → `[]any{a, b, c}`
- `{"k": v}` → `map[string]any{"k": v}`
- `[]` → `[]any{}`
- `{}` → `map[string]any{}`

```go
p, err := expr.Compile(`{"items": [1, 2, 3], "ok": true}`)
v, err := p.Run(ctx, env)
```

The rewrite leaves strings, runes, comments, and already-typed Go
composite literals (`[]any{1, 2}`, `map[string]any{...}`, `[]int{}`,
slice/index expressions like `xs[0]`, array types like `[3]int`)
untouched, so expressions that never use bare literals are unaffected.

## Templates

`NewTemplate` pre-compiles a `${...}` string interpolator. Every
`${...}` body is compiled once at construction time and re-evaluated
on each `Render` call. The same options accepted by `Compile` are
accepted by `NewTemplate`.

```go
t, err := expr.NewTemplate("Hello ${user.name}!", expr.WithBuiltins())
out, err := t.Render(ctx, env)
```

### Value rendering

Each `${...}` result is converted to a string with these rules
(a custom formatter installed with `WithTemplateFormatter` runs first
and can override any of them):

1. `nil` → empty string. Optional fields that resolve to `nil` silently
   produce no output, matching Jinja/Liquid/Handlebars convention.
2. `string` → passthrough unchanged.
3. Maps, slices, arrays, and structs (following pointers) → compact JSON
   with HTML escaping disabled, so `&`, `<`, and `>` survive intact.
   A composite that marshals to a JSON string (e.g. `time.Time` with its
   default `MarshalJSON`, or any custom marshaler) renders as the
   unquoted string rather than as a JSON string literal.
   Values JSON cannot encode (cycles, channels, functions) fall back to
   `fmt.Sprintf("%v", v)`.
4. Everything else → `fmt.Sprintf("%v", v)`.

**Behavior change from earlier versions.** In prior releases, composite
values rendered via `fmt.Sprintf("%v", v)`, producing Go syntax like
`map[retries:3]`. They now render as compact JSON: `{"retries":3}`.
Callers that relied on the old rendering must either install a
`WithTemplateFormatter` that replicates the old behavior, or update
their expected output.

### Escaping

`$$` is rewritten to a literal `$`. `$${name}` therefore emits the
literal text `${name}`. A bare `$` not followed by `$` or `{` is
emitted verbatim, so `$5` and `$foo` pass through unchanged. The `$$`
escape applies only when the configured opener starts with `$`.

### Custom delimiters: `WithTemplateDelimiters(open, close)`

Replaces `${` / `}` for a single `NewTemplate` call:

```go
t, err := expr.NewTemplate(src, expr.WithTemplateDelimiters("${{", "}}"))
```

Rules:

- The opener must end with one or more `{`; the closer must be the
  matching `}` run. `${{` requires `}}`, `${{{` requires `}}}`.
- The opener may not contain `$$` (collides with the escape).
- The `$$` escape applies only when the opener starts with `$`.
  With `WithTemplateDelimiters("${{", "}}")`, `$${{expr}}` emits
  the literal `${{expr}}`.
- With `WithTemplateDelimiters("{{", "}}")`, no `$$` escape applies
  and `$$` passes through as two literal dollar signs.

GitHub-Actions-style `${{ expr }}` avoids collisions with shell
parameter expansion (`${HOME}`) and JavaScript template literals.

Passing `WithTemplateDelimiters` to `Compile` fails with `ErrCompile`.

### Custom formatter: `WithTemplateFormatter(fn)`

Installs a custom value renderer that runs first for every
interpolated result, including `nil` and strings. Returning `false`
falls through to the default rendering chain described above:

```go
expr.WithTemplateFormatter(func(v any) (string, bool) {
    if t, ok := v.(time.Time); ok {
        return t.Format(time.Stamp), true
    }
    return "", false
})
```

Passing `WithTemplateFormatter` to `Compile` fails with `ErrCompile`.

### Error messages

Runtime errors from `Render` include a 1-based `line:column`
(byte-based columns) with the offset as supplementary detail:

```
template: evaluating ${boom} at 3:3 (offset 21): expr: evaluate error: ...
```

Parse-time errors (empty expression, invalid expression, unclosed
opener) carry the same `line:column (offset N)` format.

### `Template.Segments()`

Returns the parsed segments of the template in source order, as a
`[]TemplateSegment`. Each segment carries:

```go
type TemplateSegment struct {
    Literal string   // non-empty for literal runs; empty for expressions
    Source  string   // expression body text; empty for literals
    Offset  int      // byte offset of the segment start in the raw template
    Line    int      // 1-based line number
    Column  int      // 1-based byte column
    Program *Program // compiled expression; nil for literals
}
```

Literal segments have `Source == ""` and `Program == nil`. Expression
segments carry the compiled `*Program`, so hosts can call
`Program.Identifiers()` per segment for editor hints, live validation,
or variable extraction. For literal segments containing `$$` escapes,
`Literal` holds the decoded text.

## `Program.Identifiers()`

Returns the sorted, deduplicated set of top-level identifier names the
expression references through the environment. Hosts use it to validate
an expression against a known env shape at load time, to track
dependencies for cache invalidation, or to decide which values are worth
computing before a `Run`.

Excluded from the result:

- The literals `true`, `false`, and `nil`.
- `it` and `index` where they are bound by an enclosing iterating form
  (two-arg form binds `it`; all forms bind `index`).
- The named element binding of a three-arg form: both the binding
  identifier itself (the `o` in `filter(orders, o, o.paid)`) and all
  references to that name inside the body are excluded.
- `it` inside a three-arg form body is **not** excluded: the three-arg
  form does not bind `it`, so `it` inside such a body is a genuine env
  reference (it would be bound by an enclosing two-arg form at run time,
  or fail as undefined).
- Names registered via `WithFunctions` / `WithBuiltins`, which resolve
  without the env.
- Special-form names (`map`, `filter`, `flatMap`, `try`, `if`, `sortBy`,
  etc.) in call position, which are always available.

The analysis is static and best-effort in one corner: env entries can
shadow registered functions and special forms at run time, so an
excluded name may still be read from the env when a host deliberately
shadows it. Every name that can only resolve through the env is always
included.

## Error model

All runtime failures wrap `ErrEvaluate`; all parse failures wrap
`ErrCompile`. Callers check with `errors.Is(err, ErrEvaluate)` /
`errors.Is(err, ErrCompile)`. User function errors returned from
`(T, error)` signatures are wrapped such that both `errors.Is` and
`errors.As` still find the original cause.
