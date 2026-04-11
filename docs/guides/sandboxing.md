# Sandboxing untrusted expressions

expr is designed to run expressions you don't fully trust — policy
rules edited at runtime, webhook predicates from a database, template
fragments pasted into an admin UI. This guide is the checklist for
making that safe in practice.

A runnable companion lives in
[`../../examples/sandboxing/`](../../examples/sandboxing/).

## What the language already denies

The language itself is the first line of defense. These constructs
parse but **error at evaluation time**, so a hostile expression cannot
use them no matter what you register:

- Statements, assignments, `if`, `for`, blocks
- Function literals (`func() {}`)
- Bitwise operators (`& | ^ << >> &^`)
- Pointer/address ops (`*x`, `&x`)
- Channel ops (`<-ch`, `ch <- v`)
- Slice expressions (`x[a:b]`, `x[a:b:c]`)
- Type assertions (`x.(T)`)
- Spread args (`f(xs...)`)
- Imaginary literals (`1i`)
- Composite literals with any type other than `[]any` or
  `map[string]any`

An expression has no way to mutate state, define a new function, or
construct an arbitrary Go value. The entire influence an expression
can exert on your program comes from **(a) the env** and **(b) the
functions you registered**.

That shrinks the problem to: be careful about what you put in the env,
and careful about what you register.

## The two bounds: `MaxSourceLength` and `MaxEvalDepth`

Two constants keep pathological inputs from eating
resources. Both are `const`, set at library build time:

```go
const MaxSourceLength = 64 * 1024 // bytes
const MaxEvalDepth    = 256       // nested AST frames
```

- **`MaxSourceLength`** is checked *before* the parser runs. A 500 MB
  string never reaches `go/parser`. 64 KiB already permits expressions
  far larger than any real policy. If your inputs come from users and
  that feels too generous, add your own length check before `Compile`.
- **`MaxEvalDepth`** caps recursion in the AST walker. Deeply nested
  parens or operator chains that would blow the Go stack become an
  `ErrEvaluate` at the limit. 256 is comfortably above anything you'd
  write by hand.

Both are enforced by adversarial and fuzz tests in the repo, so
regressions show up as test failures rather than as crashes.

## Context cancellation

`Program.Run` takes a `context.Context` and checks `ctx.Err()` before
dispatching each AST node. A cancelled or expired context causes the
next node to return the raw `context.Canceled` /
`context.DeadlineExceeded` without wrapping, so callers can
`errors.Is` on it directly.

```go
ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
defer cancel()

_, err := p.Run(ctx, env)
if errors.Is(err, context.DeadlineExceeded) { /* time budget blown */ }
```

The caveat: expr only checks the context **between** nodes. Once a
registered function has control, only that function can notice
cancellation. A registered function that blocks forever without
checking `ctx` cannot be interrupted — Go has no way to kill a
goroutine. If you register I/O, take a `context.Context` as the first
parameter (expr injects it automatically) and honor it.

## What to register, and what not to

Every function you register is a capability the expression can
invoke. Audit the set the way you'd audit an IPC surface.

Register freely:
- Pure, total, deterministic functions (`math.Abs`, `strings.ToLower`).
- Read-only lookups that already have their own authorization
  (`featureEnabled(ctx, name)`).
- Small domain helpers that clearly terminate (`startsWith`, `len`).

Register with care:
- Anything that takes `ctx context.Context` and does I/O. Fine if it
  honors the context. Dangerous otherwise.
- Anything that allocates memory proportional to an input
  (`repeat(s, n)` — what if `n` is `1<<30`?). Bound it yourself.

Do not register:
- Functions that mutate shared state (`cache.Set`, `db.Exec`).
- Functions that reach out to arbitrary URLs
  (`httpGet(url)` — an expression author can now make your host a
  proxy for SSRF).
- Functions that execute code or shell commands
  (`exec.Command`, `template.Execute`, `reflect.Call`).
- Functions that panic on bad input.

The default set from `WithBuiltins()` is deliberately small and all
deterministic / side-effect free: `len`, `string`, `int`, `float`,
`bool`, `contains`, `has`, `keys`, `upper`, `lower`, `sprintf`.
Nothing there can reach outside the process. If you want a minimal
sandbox, start there.

## Auditing a function surface

When you add a new registered function, walk through these questions:

1. Does it terminate quickly on every input an attacker can craft?
2. Does it allocate memory bounded by something the attacker controls?
3. Does it honor context cancellation?
4. Does it touch shared state, disk, network, or other processes?
5. Can its error messages leak information (stack traces, file paths,
   credentials)?

If any answer is "yes" and you can't mitigate, the function doesn't
belong in the sandbox.

## What to put in the env

The env has the same audit surface as registered functions. Struct
methods on env values are callable from the expression, so the question
"what can this expression call?" includes every zero-arg method on
every struct you pass.

The safest env is a `map[string]any` of pre-computed values and
`[]any` / `map[string]any` subtrees. No methods, no live handles, no
pointers into domain objects.

If you do pass a struct, build a **view struct** (see
[designing-an-env.md](designing-an-env.md)) that only exposes the
read-only fields and methods you intend. Never pass a
`*sql.DB`, `*http.Client`, or `*exec.Cmd`. Never pass a struct whose
methods can trigger writes.

## Error handling at the top level

Wrap the eval in a clear error boundary:

```go
out, err := p.Run(ctx, env)
switch {
case err == nil:
    // success
case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
    // user-facing: "expression took too long"
case errors.Is(err, expr.ErrEvaluate):
    // user-facing: "expression failed to evaluate"
    // internal log: err.Error()
default:
    // unexpected — something registered returned an error that
    // wasn't wrapped through ErrEvaluate. Log and investigate.
}
```

Don't surface raw error messages to end users unless you trust them
with that level of detail. "Did you mean…?" hints are helpful for
authors but can reveal the shape of the env to attackers.

## A checklist, if you just want the answers

- [ ] `MaxSourceLength` tuned to your inputs (default 64 KiB).
- [ ] `MaxEvalDepth` left at 256 unless you have a reason to raise it.
- [ ] Every `Run` call uses a context with a deadline.
- [ ] No registered function does I/O without honoring context.
- [ ] No registered function mutates shared state.
- [ ] The env is either a `map[string]any` of values, or a view struct
      whose exported fields/methods are all read-only.
- [ ] Top-level error handling distinguishes timeout from eval error.
- [ ] Fuzz / unit tests cover the adversarial inputs you care about.
