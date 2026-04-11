# expr

A small, embeddable expression language for Go programs. Intended for evaluating
expressions inside a larger Go program. For example: conditions, templates,
parameter interpolation.

## Design

- **Built on `go/parser`.** Source is fed to `parser.ParseExpr` and the resulting
  `ast.Expr` is walked directly — no custom lexer, no bytecode. expr accepts a
  strict subset of Go's expression syntax; `docs/SPEC.md` is authoritative.
- **Expressions only.** No statements, assignments, loops, or function literals.
- **Minimal dependencies.** Keep the dependency surface tiny.
- **Safe by default.** `MaxSourceLength` and `MaxEvalDepth` in `engine.go` bound
  parser and evaluator stack usage; the adversarial and fuzz tests keep that honest.

## Layout

| File              | Purpose                                                          |
| ----------------- | ---------------------------------------------------------------- |
| `engine.go`       | `Compile`/`Eval`, `CompileOption`, `map` keyword preprocessing |
| `program.go`      | AST walker — the evaluator                                       |
| `reflect.go`      | Env lookup, function dispatch, type coercion                     |
| `builtins.go`     | Default function set exposed by `Builtins()` / `WithBuiltins()`  |
| `higher_order.go` | `map`, `filter`, etc.                                            |
| `suggest.go`      | "Did you mean…" for unknown identifiers                          |
| `docs/SPEC.md`    | Language specification                                           |

## Conventions

- The `map` identifier is rewritten to an internal sentinel (`mapFormName`) before
  parsing because Go treats `map` as a keyword.
- Errors wrap `ErrCompile` or `ErrEvaluate` — preserve that so callers can `errors.Is`.
- When changing language behavior, update `docs/SPEC.md` in the same change. Drift between code and spec is a bug.
