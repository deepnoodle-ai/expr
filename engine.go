// Package expr is a zero-dependency expression evaluator built on top of
// go/parser. It accepts the subset of Go expression syntax useful for
// conditions, templates, and parameter interpolation: identifiers,
// selectors, index expressions, arithmetic, comparisons, logical
// operators, and calls to registered functions.
//
// expr is intentionally small and adds no external dependencies.
//
// # Evaluating an expression
//
// [Compile] parses an expression once and returns a [*Program] that can
// be run against many inputs. Programs are immutable and safe for
// concurrent evaluation.
//
//	p, err := expr.Compile("upper(user.name)",
//	    expr.WithBuiltins(),
//	    expr.WithFunctions(map[string]any{"upper": strings.ToUpper}),
//	)
//	v, err := p.Run(ctx, env)
//
// # Environments
//
// env may be a map[string]any, a struct, or a pointer to a struct.
// Identifiers resolve to map keys, struct fields, or bound methods.
// Callables stored in env are also invocable (see [Program.Run]).
//
// # Templates
//
// [NewTemplate] pre-compiles a `${...}` interpolator:
//
//	t, err := expr.NewTemplate("Hello ${user.name}!", expr.WithBuiltins())
//	out, err := t.Render(ctx, env)
package expr

import (
	"errors"
	"fmt"
	"go/parser"
	"go/scanner"
	"go/token"
	"strings"

	"github.com/deepnoodle-ai/expr/internal/jsonlit"
)

// ErrCompile wraps parse failures so callers can match with errors.Is.
var ErrCompile = errors.New("expr: compile error")

// ErrEvaluate wraps runtime failures so callers can match with errors.Is.
var ErrEvaluate = errors.New("expr: evaluate error")

// MaxSourceLength is the maximum number of bytes Compile will accept.
// Longer inputs return ErrCompile without invoking the Go parser, which
// protects against adversarial nesting depths that could exhaust the
// parser's own stack.
const MaxSourceLength = 64 * 1024

// MaxEvalDepth bounds the recursion depth of Program.Run. Expressions
// whose AST nests deeper return ErrEvaluate. 256 is enough for any
// hand-written expression and keeps the Go stack well under 1 MiB.
const MaxEvalDepth = 256

// Option configures a [Compile] or [NewTemplate] call. Options are
// applied in order, so a later WithFunctions overrides an earlier
// WithBuiltins for any shared name.
type Option func(*compileConfig)

// compileConfig is the resolved set of options for a single Compile.
// It is consumed during parsing to build the function dispatch tables
// baked into the resulting Program.
type compileConfig struct {
	funcs    map[string]any
	prepared map[string]*preparedFunc
}

func newCompileConfig() *compileConfig {
	return &compileConfig{
		funcs:    map[string]any{},
		prepared: map[string]*preparedFunc{},
	}
}

// WithBuiltins registers the standard builtin function set returned by
// [Builtins]. Pair it with [WithFunctions] to extend or override individual
// entries; options apply in the order they are passed, so a later
// WithFunctions wins over an earlier WithBuiltins for any shared name.
func WithBuiltins() Option {
	return WithFunctions(Builtins())
}

// WithFunctions registers the given functions as callable identifiers in
// the compiled expression. Entries merge into (and override) whatever is
// already registered by earlier options.
//
// Functions may take any Go types as arguments; expr converts evaluated
// values to the declared parameter types at call time. Return signatures of
// `T`, `(T, error)`, and `()` are supported. Variadic functions are also
// supported.
func WithFunctions(funcs map[string]any) Option {
	return func(c *compileConfig) {
		for name, fn := range funcs {
			c.funcs[name] = fn
			pf, err := prepareFunc(name, fn)
			if err != nil {
				// Defer error surfacing to call-time; store a
				// nil-native preparedFunc entry so lookups still
				// find the name (useful for better error hints).
				c.prepared[name] = &preparedFunc{name: name}
				continue
			}
			c.prepared[name] = pf
		}
	}
}

// Compile parses an expression once for repeated evaluation. The returned
// Program is immutable and safe for concurrent use. Input longer than
// MaxSourceLength is rejected without calling the parser.
//
// Functions are registered via Options and baked into the returned
// Program. JSON-style array and object literals ([1, 2, 3], {"k": v}) are
// always accepted; see docs/SPEC.md for the exact rules.
func Compile(code string, opts ...Option) (*Program, error) {
	if len(code) > MaxSourceLength {
		return nil, fmt.Errorf("%w: source length %d exceeds maximum %d",
			ErrCompile, len(code), MaxSourceLength)
	}
	cfg := newCompileConfig()
	for _, opt := range opts {
		opt(cfg)
	}
	parsed := preprocessSource(jsonlit.Rewrite(code))
	node, err := parser.ParseExpr(parsed)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCompile, err)
	}
	p := &Program{source: code, root: node, funcs: cfg.funcs, prepared: cfg.prepared}
	p.compile()
	return p, nil
}

// mapFormName is the internal identifier that source-level `map(`
// calls are rewritten to before parsing. Go's parser treats `map` as
// a keyword (the start of a map-type literal), so expr cannot
// accept `map(xs, pred)` as-is; preprocessSource rewrites the token
// to this name and higherOrderForms dispatches on it. The value is
// deliberately ugly so it cannot collide with a user-facing
// identifier.
const mapFormName = "__expr_map__"

// preprocessSource rewrites Go keyword tokens that expr wants to
// accept as ordinary identifiers. The only such token is `map`: Go's
// parser treats `map` as the start of a map-type literal everywhere
// it appears, so expr cannot accept `map(xs, pred)`, `obj.map(...)`,
// or any other construct that names `map` as an identifier.
//
// The rewrite replaces every `map` token with mapFormName *unless*
// the next token is `[`, which would indicate a Go map type literal
// like `map[string]int{}`. Composite literals are unsupported by
// expr anyway, so leaving that one case alone lets the parser emit
// its normal error for unsupported syntax. Selector, call, and
// method-call forms all carry the rewritten identifier through to
// the evaluator, which translates it back to "map" in lookups and
// error messages via mapFormDisplayName.
func preprocessSource(src string) string {
	// Fast path: most expr expressions do not contain `map` at all,
	// so the scanner pass is skipped. `strings.Contains` on a short
	// expression is much cheaper than spinning up go/scanner.
	if !strings.Contains(src, "map") {
		return src
	}
	fs := token.NewFileSet()
	file := fs.AddFile("expr", fs.Base(), len(src))
	var s scanner.Scanner
	s.Init(file, []byte(src), nil, 0)

	type tokInfo struct {
		pos token.Pos
		tok token.Token
	}
	var toks []tokInfo
	for {
		pos, tok, _ := s.Scan()
		if tok == token.EOF {
			break
		}
		toks = append(toks, tokInfo{pos, tok})
	}

	var out strings.Builder
	out.Grow(len(src) + 16)
	last := 0
	for i := 0; i < len(toks); i++ {
		if toks[i].tok != token.MAP {
			continue
		}
		// Leave `map[...]` alone so Go map type literals continue to
		// produce a normal "unsupported syntax" error at eval time.
		if i+1 < len(toks) && toks[i+1].tok == token.LBRACK {
			continue
		}
		off := file.Offset(toks[i].pos)
		out.WriteString(src[last:off])
		out.WriteString(mapFormName)
		last = off + len("map")
	}
	if last == 0 {
		return src
	}
	out.WriteString(src[last:])
	return out.String()
}

// displayIdent converts an internal rewritten identifier back to the
// name the user originally typed, for use in error messages and
// field/method lookups. Currently this only matters for `map`.
func displayIdent(name string) string {
	if name == mapFormName {
		return "map"
	}
	return name
}
