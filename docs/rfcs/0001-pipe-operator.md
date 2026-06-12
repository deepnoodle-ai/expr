# RFC 0001: Pipe Operator (`|`)

**Status:** Implemented (always on)  
**Date:** 2026-06-12  
**Implementation notes:** The desugar, the non-call right-side errors,
and the ambiguous-comparison diagnostic (§3.3) shipped as recommended.
The opt-in recommendation (§7.3) was overridden: the default-on
question (§9.2 step 5) was resolved in favor of enabling the pipe
unconditionally, since `|` never compiled before and the token reuse
therefore breaks no existing expression. Normative language
documentation lives in [the spec](../reference/spec.md); this document
records the design rationale.

---

## Summary

Repurpose the bitwise-OR token `|` as a pipeline operator. `a | f(x, y)`
desugars at compile time to `f(a, x, y)`, threading the left operand as
the first argument of the right-side call. The desugar is purely an AST
rewrite; the evaluator and all existing behavior remain untouched.

---

## 1. Motivation

Expressions that chain several higher-order forms on a single collection
are the most common "hard to read" complaint from expr users. The nesting
grows from the outside in, the innermost operation is buried in the middle
of the source, and each closing parenthesis has to be mentally matched to
its opener:

```
join(map(filter(checks, !it.ok), sprintf("- %s: %s", it.name, it.msg)), "\n")
```

With a pipe operator the same logic reads left-to-right, matching the
order operations actually run:

```
checks | filter(!it.ok) | map(sprintf("- %s: %s", it.name, it.msg)) | join("\n")
```

A second common case is a chain of string or collection operations where
the data flows through several transforms:

```
split(replace(lower(trim(input)), " ", "_"), ",")
```

versus

```
input | trim() | lower() | replace(" ", "_") | split(",")
```

(The right side of each pipe must be a call expression, so
single-argument functions are written with empty parentheses:
`input | trim()` desugars to `trim(input)`. See section 5 for why a
bare `input | trim` is rejected.)

Neither example is contrived. Both appear in real expr usage in the
codebase that motivated this RFC. The motivating case is a health-check
report renderer where the before version requires four levels of nesting
that obscure what is being computed.

---

## 2. Semantics

### 2.1 Basic desugar

The pipe operator `|` desugars at compile time, during the AST
validation pass, before the evaluator ever sees the tree. The rule is:

> `a | f(x, y, ...)` rewrites to `f(a, x, y, ...)`

The left operand `a` is injected as the **first** argument of the
right-side call. The remaining arguments shift right. The result is an
ordinary `*ast.CallExpr` that the evaluator handles through the normal
function-call path.

### 2.2 Chaining is left-associative

Go's parser makes `|` left-associative at the same precedence level as
`+` and `-` (precedence 4). A chain like:

```
a | f() | g()
```

parses as:

```
(a | f()) | g()
```

The first rewrite produces `f(a)`, then the outer `|` desugars that
result as `g(f(a))`. Left-to-right order is preserved: `a` flows into
`f`, and `f`'s result flows into `g`. This matches the intuition that
reading left-to-right follows execution order.

For the motivating example:

```
checks | filter(!it.ok) | map(sprintf("- %s: %s", it.name, it.msg)) | join("\n")
```

The chain desugars step by step as:

```
join(map(filter(checks, !it.ok), sprintf("- %s: %s", it.name, it.msg)), "\n")
```

which is exactly the nested form.

### 2.3 Special forms receive the rewritten CallExpr

Because the desugar happens at compile time before dispatch, special
forms (`filter`, `map`, `flatMap`, `any`, `all`, `find`, `count`,
`sortBy`, `try`, `if`) receive the rewritten `*ast.CallExpr` like any
other call. Their arity checks run against the post-rewrite argument
count. So:

```
checks | filter(!it.ok)
```

desugars to `filter(checks, !it.ok)`, which has two arguments and passes
the forms' argument validation (`splitFormArgs`) normally. The named
three-arg binding form composes the same way: `checks | filter(c, !c.ok)`
desugars to `filter(checks, c, !c.ok)`. The predicate argument remains in its
original AST position (now index 1 rather than 0); no special handling is
needed for `it`/`index` binding.

The `try` and `if` forms work the same way. `value | try(fallback)`
desugars to `try(value, fallback)`, which is the standard two-argument
form. `cond | if(then, else)` is syntactically valid but semantically
odd; no special case is needed since `if(cond, then, else)` already does
what a user would expect.

### 2.4 Non-call right-hand side

`a | b` where `b` is not a call expression is rejected at compile time
with:

```
compile error: pipe operator | requires a function call on the right-hand side
```

See section 6 for the full error taxonomy.

---

## 3. Precedence

### 3.1 The table

Go's precedence levels, and where `|` sits, are fixed by the parser:

| Precedence | Operators                    | Associativity |
| ---------- | ---------------------------- | ------------- |
| 5 (high)   | `*  /  %`                    | left          |
| 4          | `+  -  \|  ^`                | left          |
| 3          | `==  !=  <  <=  >  >=`       | left          |
| 2          | `&&`                         | left          |
| 1 (low)    | `\|\|`                       | left          |

`|` is at precedence level 4, the same as `+` and `-`. Unary `!`, `-`,
`+` bind tighter than any binary operator.

The key consequences:

- `|` binds **tighter** than comparisons (`==`, `!=`, `<`, etc.).
- `|` binds **tighter** than logical operators (`&&`, `||`).
- `|` binds at the **same level** as `+` and `-`, left-associative.

### 3.2 Worked examples

**Example 1: pipe with comparison**

```
checks | filter(!it.ok) == []any{}
```

Parses as:

```
(checks | filter(!it.ok)) == []any{}
```

which desugars to:

```
filter(checks, !it.ok) == []any{}
```

This is probably what the user meant: compare the filtered result to an
empty list. The precedence works in the user's favor here.

**Example 2: pipe on the right of a comparison**

```
len(errors) == 0 | f()
```

Parses as:

```
len(errors) == (0 | f())
```

which desugars to:

```
len(errors) == f(0)
```

This is almost certainly not what the user meant. They likely wanted
`(len(errors) == 0) | f()`, i.e., pipe the boolean result into `f`. The
silent misparsing is a trap.

**Example 3: pipe and `&&`**

```
active | filter(it.enabled) && len(errors) == 0
```

Parses as:

```
(active | filter(it.enabled)) && (len(errors) == 0)
```

which desugars to:

```
filter(active, it.enabled) && (len(errors) == 0)
```

This is the natural reading: "the filtered list, and the error count is
zero." The precedence works correctly because `&&` binds looser than `|`.

**Example 4: pipe with unary `!`**

```
checks | filter(!it.ok) | any(it.severity == "critical")
```

Parses as:

```
((checks | filter(!it.ok)) | any(it.severity == "critical"))
```

which desugars to:

```
any(filter(checks, !it.ok), it.severity == "critical")
```

The unary `!` inside the filter predicate binds to `it.ok` before any
binary operator is considered, so the pipe sees `filter(!it.ok)` as a
complete call. This is correct and unsurprising.

### 3.3 Recommendation: compile error for suspicious mixes

Example 2 demonstrates a real trap: when `|` appears as an operand of a
comparison without parentheses, the user almost certainly did not intend
the pipe to consume the comparison's right operand. The same applies to
`|` appearing as the right operand of `+` or `-`.

This RFC recommends adding a compile-time diagnostic for one pattern,
where "pipe node" means a `*ast.BinaryExpr` with `Op == token.OR` that
will be rewritten as a pipe: a pipe node appearing as the `Y` (right
operand) of a comparison operator (`==`, `!=`, `<`, `<=`, `>`, `>=`).
This catches `a == b | f()` parsing as `a == f(b)`.

Mixing `|` with `+` and `-` needs no diagnostic: they share precedence
level 4 and group left-to-right, so `a + b | f()` is `(a + b) | f()`,
which desugars to the intended `f(a + b)`.

The one genuinely unsafe case is `|` appearing as the **right** operand
of a **comparison**, because the comparison then consumes the pipe's left
operand rather than the other way around. The exact compile error is:

```
compile error: ambiguous expression: | on the right of == may parse differently
    than expected; use parentheses to clarify: write (a | f()) == b or a == f(b)
```

More precisely, the diagnostic fires when the parent node of a `|`
BinaryExpr is a comparison BinaryExpr and the pipe is the right-hand
child (`parent.Y`). This is a conservative set: it catches the known trap
without requiring heuristics about intent.

Adopting this diagnostic is strongly recommended. Silent precedence
surprises in an embedded expression language are especially damaging
because the author of an expression is often not the author of the
embedding code, and there may be no test coverage for the specific
combination.

---

## 4. Interaction with optional access (`?.` and `?[`)

### 4.1 Nil propagation

The `?.` and `?[` operators already handle the "receiver may be nil"
case: they short-circuit to `nil` when the receiver is nil or the lookup
produces nothing, rather than raising `ErrEvaluate`. The pipe operator
does not change this behavior.

Consider:

```
user?.orders | filter(it.paid)
```

This desugars to:

```
filter(user?.orders, it.paid)
```

`user?.orders` evaluates via the existing `__try_select__` sentinel. If
`user` is nil or has no `orders` field, the result is `nil`. `filter`
receives `nil` as its collection, which the existing `iterItems` function
already handles: `nil` is treated as an empty list. So the full
expression returns `[]any{}` when `user` is nil, with no error.

The behavior is therefore:

- nil receiver through `?.` or `?[`: pipe receives `nil`, passes it to
  the called function, which sees an empty or nil collection.
- For `filter`, `map`, `any`, `all`, `find`, `count`: nil input returns
  the appropriate empty result.
- For other functions that do not handle `nil`: they receive `nil` and
  behave according to their own contract. This is no different from
  calling them directly with a nil argument.

### 4.2 Chained optional access before a pipe

```
events?[0]?.tags | filter(!it.internal)
```

desugars to:

```
filter(events?[0]?.tags, !it.internal)
```

The `events?[0]?.tags` subexpression is evaluated first (as it always
would be). If the index is out of range or the `tags` field is absent,
the result is `nil`, and `filter` returns `[]any{}`.

### 4.3 Calling the result of a pipe

`(a | f())?.field` is allowed: the pipe desugars to `f(a)`, a call
expression, and `?.field` on a call result is already supported.
`(a | f())()` is rejected because calling the result of a call is
already rejected for all calls (`call target must be a function name or
selector`).

### 4.4 Consistency verdict

Optional access and pipe compose cleanly. The nil-propagation semantics
are consistent with the existing `?.`/`?[` design: missing data flows
forward as `nil` rather than erroring, and functions that already handle
`nil` (the higher-order forms) absorb it silently. No new nil-handling
rules are needed.

---

## 5. Non-call right-hand side

`a | b` where `b` is not a call expression cannot be given a useful
meaning without introducing semantics that are not present elsewhere in
expr. Possible interpretations (bitwise OR, value threading to a
non-function) are either already rejected or do not fit the model.

This RFC recommends rejecting with a compile error at the same point
where the pipe desugar would otherwise fire:

```
compile error: pipe operator | requires a function call on the right-hand side;
    "b" is not a call (did you mean to write b(...)?)
```

If `b` is an identifier that resolves to a known higher-order form, the
error can reference the expected signature, following the same "did you
mean" style the rest of expr uses:

```
compile error: pipe operator | requires a function call on the right-hand side;
    "filter" is a special form, did you mean to write filter(predicate)?
```

---

## 6. Error message quality

A summary of all new compile errors introduced by this feature, with
exact text:

**Non-call right-hand side:**

```
compile error: pipe operator | requires a function call on the right-hand side;
    "<rhs>" is not a call
```

When the RHS is a recognized special form name (an identifier matching a
known higher-order form):

```
compile error: pipe operator | requires a function call on the right-hand side;
    "<name>" is a special form, did you mean to write <name>(<callHint args>)?
```

**Ambiguous precedence (pipe as right operand of comparison):**

```
compile error: ambiguous expression: | on the right of <op> may parse differently
    than expected; use parentheses to clarify: write (<lhs> | <call>()) <op> <rhs>
    or <lhs> <op> <call>(<rhs>)
```

Where `<op>` is the comparison token (`==`, `!=`, `<`, `<=`, `>`, `>=`).

All errors wrap `ErrCompile` so `errors.Is(err, ErrCompile)` continues
to work.

---

## 7. The identity question

### 7.1 The case against

expr's identity claim is that it "accepts a strict subset of Go's
expression syntax." Users who know Go can read expr expressions without
learning anything new; Go tooling (syntax highlighting, formatters) works
on expr source with no modification. This identity is a meaningful part
of the value proposition: the language is small because it has edges, and
those edges are Go's edges.

Repurposing `|` breaks this. In Go, `a | b` is bitwise OR. An expr
expression containing `a | f()` is not valid Go and does not mean the
same thing as the closest valid Go. A Go developer reading an expr
expression with `|` cannot apply their existing mental model. Syntax
highlighting will not help, because the token looks like bitwise OR.

The existing `?.` and `?[` operators are a precedent for divergence, but
they are unusual in that Go simply does not have those tokens, so there is
no conflict with existing Go meaning. `|` is different: it has a Go
meaning that expr users will know, and repurposing it silently replaces
that meaning.

There is also a maintenance argument. Every new operator that diverges
from Go adds surface area to the "not quite Go" subset that users and
tools must track. The current divergences (`?.`, `?[`, `map` rewrite, `if`
rewrite) each had a clear necessity. Pipelines are a convenience, not a
necessity: the nested form works today.

### 7.2 The case for

The "strict Go subset" claim already has caveats. Bitwise operators are
rejected, so `|` is not available to users at all today. A user writing
`a | b` already gets a compile error; the change from "bitwise OR is
rejected" to "this is a pipe operator" does not take away any working
functionality. In that sense, the token is genuinely free.

The ergonomic case is real. The motivating example is not contrived, and
deeply nested higher-order calls are the most common readability complaint
about expr. Pipelines are an established idiom (shells, Elixir, Haskell,
Rust iterators, JavaScript `.then()` chains) that many developers
recognize immediately, even if the specific token `|` evokes bitwise OR
in C-family languages.

An opt-in option (`WithPipeOperator()`) would let the host application
decide. Users of that application would encounter a clear capability
boundary, and the spec could document it explicitly.

### 7.3 Recommendation

Adopt the feature behind an opt-in `WithPipeOperator()` compile option,
clearly documented as a deviation from the strict Go subset. Keep the
default behavior unchanged: without `WithPipeOperator()`, `a | b`
continues to produce "bitwise operator | is not supported."

The opt-in framing resolves the identity tension: the core language
remains a Go subset, and applications that want pipeline ergonomics
declare that choice explicitly. Documentation should be frank about the
trade-off rather than framing the opt-in as a mere "safety valve."

---

## 8. Alternatives considered

### 8.1 Method-chain style (`xs.filter(...).map(...)`)

A chaining syntax like `xs.filter(!it.ok).map(it.name)` would look more
Go-idiomatic and sidestep the token-reuse issue. However, expr's selector
calls (`x.f(...)`) resolve `f` on the runtime value of `x`, not on the
type. There is no mechanism to attach `filter` as a method of a `[]any`.
Implementing this would require either method injection at compile time
(significant complexity) or a distinct parse pass that recognizes
`.filter(...)` differently from ordinary selector calls (surprising
behavior for users). Either path is substantially more invasive than the
pipe desugar and produces a syntax that looks like Go but behaves
differently in a less obvious way.

### 8.2 A `pipe()` builtin function

```
pipe(checks, filter(!it.ok), map(sprintf("- %s: %s", it.name, it.msg)), join("\n"))
```

This is honest about being different from Go, avoids any token-reuse
issue, and could be implemented as a variadic higher-order form. The
problems: the syntax is verbose and does not improve readability much
over the nested form; the arguments are positionally significant in a
non-obvious way (the forms are not called, their ASTs are passed and
re-dispatched, which is surprising); and the "pipe" concept buried in a
function call loses the visual left-to-right flow that makes pipelines
useful in the first place.

### 8.3 Nested calls as status quo

The current nested form is unambiguous, compiles fine, and is what all
existing users already know. The readability cost is real but bounded:
expr expressions are usually short, and the host application can provide
a multi-line editor with parenthesis matching. This is the lowest-risk
option and the correct choice if the ergonomic gains of the pipe do not
justify the language complexity cost.

---

## 9. Open questions and suggested decision process

### 9.1 Open questions

**Q1. Desugar timing.** The proposal places the desugar in the compile-
time validation pass. Should it instead be a pre-parse source rewrite
(like `map`/`if`), a post-parse AST rewrite (like `?.`/`?[`), or a
phase of its own? The answer affects how error positions and the
displayed predicate text in higher-order error messages behave.

**Q2. Error position reporting.** After desugaring, the injected first
argument's source position may not exist in the original source. How
should compiler error positions be reported for the synthesized
arguments?

**Q3. Interaction with `identifiers.go`.** `Program.Identifiers()` walks
the AST to collect env-referenced names. If the desugar rewrites the AST
in place, `Identifiers()` sees the desugared tree and reports correctly.
If the desugar is a source-level rewrite, the stored source (`p.source`)
diverges from the tree. Which canonical form is preferred?

**Q4. Display in error messages.** `formatPredicate` and `exprDisplayString`
reverse internal rewrites back to user-visible form. Should the pipe form
be reversed in error messages (showing `a | f()` rather than `f(a)`) or
shown in desugared form?

**Q5. Right-to-left composition.** Is there any use case for `f(x) | g`
where `g` receives `f(x)` as a first argument? This is the normal pipe
direction and is handled. But the converse (inserting as the *last*
argument) would require a different token or explicit syntax.

**Q6. Interaction with `WithEvalBudget`.** The desugar is purely
structural; it does not change how many AST nodes are evaluated. No
budget impact expected, but worth verifying in the prototype.

### 9.2 Suggested decision process

1. **Settle the identity question first.** If the team decides that
   strict Go-subset identity is non-negotiable, the feature is closed.
   If the team is open to a documented deviation, proceed.

2. **Prototype behind `WithPipeOperator()`.** Implement the desugar in a
   branch, adding the option flag, the non-call RHS error, and the
   ambiguous-precedence diagnostic. Do not change the default behavior.
   No documentation changes outside the option itself.

3. **Validate with real expressions.** Run the prototype against the
   motivating health-check expressions and any other real-world pipelines
   that motivated the RFC. Confirm that the desugar and error messages
   behave as expected.

4. **Resolve open questions Q1-Q4** based on prototype experience.
   Particularly, confirm that `identifiers.go` and `formatPredicate`
   are correct across the rewrite.

5. **Decide on default-on vs. opt-in.** After the prototype is proven,
   the team can reconsider whether `WithPipeOperator()` should become
   the default (which makes it part of the baseline language) or remain
   opt-in (which keeps the strict Go-subset default). This decision
   carries the most long-term weight and should not be rushed.

6. **Update spec and docs in the same change.** If the feature ships,
   `docs/reference/spec.md`, `docs/guides/higher-order-patterns.md`,
   `docs/guides/examples.md`, and `llms.txt` all need updating in the
   same commit, per the project conventions.
