# expr

A small, embeddable expression language for Go programs. Intended for evaluating
expressions inside a larger Go program. For example: conditions, templates,
parameter interpolation.

## Design

- **Built on `go/parser`.** Source is fed to `parser.ParseExpr` and the resulting
  `ast.Expr` is walked directly — no custom lexer, no bytecode. expr accepts a
  strict subset of Go's expression syntax; `docs/reference/spec.md` is authoritative.
- **Expressions only.** No statements, assignments, loops, or function literals.
- **Minimal dependencies.** Zero external deps; keep it that way.
- **Safe by default.** `MaxSourceLength` and `MaxEvalDepth` in `engine.go` bound
  parser and evaluator stack usage; the adversarial and fuzz tests keep that honest.

## Layout

| File                | Purpose                                                            |
| ------------------- | ------------------------------------------------------------------ |
| `engine.go`         | `Compile`, `Option`, `map`/`if` and keyword field preprocessing    |
| `program.go`        | AST walker — the evaluator                                         |
| `access.go`         | The one implementation of `x.y`, `x[i]`, `?.`, `?[` and env lookup |
| `reflect.go` + `prepared.go` | Conversions, equality, function dispatch, cached `callSig`s |
| `format.go`         | Bounded, cycle-safe rendering for `string`, `sprintf`, templates   |
| `builtins.go`       | Default function set exposed by `Builtins()` / `WithBuiltins()`    |
| `builtin_groups.go` | Opt-in `MathFuncs()` / `StringFuncs()` / `CollectionFuncs()` sets  |
| `higher_order.go`   | `map`, `filter`, `any`, `all`, `find`, `count`, `try`, `if` forms  |
| `identifiers.go`    | `Program.Identifiers()` — env-referenced name collection           |
| `calls.go`          | `Program.Calls()` / `Program.Literal()` — call sites and literals  |
| `types.go` + `check.go` | `Type` and `Program.Check` / `Template.Check` — static type checking |
| `template.go`       | `${...}` interpolation via `NewTemplate` / `Render`                |
| `truthy.go`         | `IsTruthy` rules used by `!`, `&&`, `\|\|`, `bool(v)`              |
| `suggest.go`        | "Did you mean…" for unknown identifiers                            |
| `internal/srcedit/` | Shared scanner, bracket matcher and splicer for the source rewrites |
| `internal/jsonlit/` | Pre-parse rewrite of bare `[...]` / `{...}` JSON literals          |
| `internal/optaccess/` | Pre-parse rewrite of `?.` / `?[` into sentinel calls             |
| `cmd/expr/`         | Small CLI for one-off evaluation                                   |
| `docs/reference/spec.md`   | Authoritative language spec                                 |
| `docs/guides/`             | User-facing guides (examples, functions, env, sandboxing, …) |
| `docs/assets/`             | Benchmark SVGs used by the README                           |
| `llms.txt`                 | Condensed LLM-oriented reference                            |

## Conventions

- The `map` identifier is rewritten to an internal sentinel (`mapFormName`) before
  parsing because Go treats `map` as a keyword.
- Errors wrap `ErrCompile` or `ErrEvaluate` — preserve that so callers can `errors.Is`.
  `Check` errors wrap `ErrType`.
- `Check` must agree with `Run`. When you change what an operator, builtin or
  form accepts at run time, change `check.go` to match (`builtinSigs` for
  builtins), and add the case to `TestCheckMatchesRun`, which runs every
  rejected expression to confirm `Run` fails too.
  The one intended exception: Check rejects a registered function used as a
  value (`[upper, 1]`), which `Run` still evaluates.
- When changing language behavior, update `docs/reference/spec.md` in the same change; if the
  change affects how expressions *look*, update `docs/guides/examples.md` and `llms.txt` too.
- Every optimization must be unobservable. A fast path or fold needs an
  equivalence test in `optimize_equiv_test.go` (compiled with and without
  `withoutOptimizations()`) next to its benchmark.
- Value-shape rules live in one place (`access.go`, `mapKey`, `convertValue`,
  `canCompare`, `callSig`). `value_matrix_test.go`, `call_matrix_test.go` and
  `hostgen_test.go` hold every path to them; keep new paths on the shared code.
- A test that could overflow the stack or hang belongs in
  `isolated_test.go`, which runs each case in a child process.
- Guide pages under `docs/guides/` have runnable companions under `examples/` and
  tests in `docs_guides_test.go` / `docs_examples_test.go`. When you change a guide's
  code snippets, update the matching test so the docs stay honest.
