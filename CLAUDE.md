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
| `engine.go`         | `Compile`, `Option`, `map`/`if` keyword preprocessing              |
| `program.go`        | AST walker — the evaluator                                         |
| `reflect.go` + `prepared.go` | Env lookup, function dispatch, cached signatures          |
| `builtins.go`       | Default function set exposed by `Builtins()` / `WithBuiltins()`    |
| `builtin_groups.go` | Opt-in `MathFuncs()` / `StringFuncs()` / `CollectionFuncs()` sets  |
| `higher_order.go`   | `map`, `filter`, `any`, `all`, `find`, `count`, `try`, `if` forms  |
| `identifiers.go`    | `Program.Identifiers()` — env-referenced name collection           |
| `template.go`       | `${...}` interpolation via `NewTemplate` / `Render`                |
| `truthy.go`         | `IsTruthy` rules used by `!`, `&&`, `\|\|`, `bool(v)`              |
| `suggest.go`        | "Did you mean…" for unknown identifiers                            |
| `internal/jsonlit/` | Pre-parse rewrite of bare `[...]` / `{...}` JSON literals          |
| `cmd/expr/`         | Small CLI for one-off evaluation                                   |
| `docs/reference/spec.md`   | Authoritative language spec                                 |
| `docs/guides/`             | User-facing guides (examples, functions, env, sandboxing, …) |
| `docs/assets/`             | Benchmark SVGs used by the README                           |
| `llms.txt`                 | Condensed LLM-oriented reference                            |

## Conventions

- The `map` identifier is rewritten to an internal sentinel (`mapFormName`) before
  parsing because Go treats `map` as a keyword.
- Errors wrap `ErrCompile` or `ErrEvaluate` — preserve that so callers can `errors.Is`.
- When changing language behavior, update `docs/reference/spec.md` in the same change; if the
  change affects how expressions *look*, update `docs/guides/examples.md` and `llms.txt` too.
- Guide pages under `docs/guides/` have runnable companions under `examples/` and
  tests in `docs_guides_test.go` / `docs_examples_test.go`. When you change a guide's
  code snippets, update the matching test so the docs stay honest.
