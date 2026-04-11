# expr examples

Small, runnable programs showing how the `expr` expression language
looks and works. Each subdirectory is a standalone `main.go`; run any
of them with `go run ./examples/<name>`.

| Directory       | What it shows                                              |
| --------------- | ---------------------------------------------------------- |
| `basic/`        | default built-ins over a `map[string]any` environment      |
| `structs/`      | struct envs with fields and bound methods                  |
| `funcs/`        | registering custom Go functions as callable identifiers    |
| `compile_once/` | compile once, evaluate many, the hot-path pattern          |
| `template/`     | `${...}` string interpolation via `NewTemplate` / `Render` |
| `higher_order/` | `map` / `filter` / `any` / `all` / `find` / `count`        |

Each of the first five directories corresponds to one of the code
blocks in the top-level [README](../README.md); you can copy the
example and run it as-is.

`expr` is a zero-dependency evaluator that accepts the subset of Go
expression syntax useful for embedding conditions, templates, and
parameter interpolation in larger Go programs. Expressions only: no
statements, no loops, no declarations.
