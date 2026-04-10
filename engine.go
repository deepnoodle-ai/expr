// Package expr is a zero-dependency expression evaluator built on top of
// go/parser. It accepts the subset of Go expression syntax useful for
// conditions, templates, and parameter interpolation: identifiers,
// selectors, index expressions, arithmetic, comparisons, logical
// operators, and calls to registered functions.
//
// expr is intentionally small and adds no external dependencies.
//
// A fresh engine has no functions registered. Opt in to the standard
// builtin set with WithBuiltins, supply your own with WithFunctions, or
// mix both:
//
//	e := expr.New(
//	    expr.WithBuiltins(),
//	    expr.WithFunctions(map[string]any{"upper": strings.ToUpper}),
//	)
//	v, err := e.Eval(ctx, "upper(user.name)", env)
//
// Compile once, evaluate many:
//
//	e := expr.New(expr.WithBuiltins())
//	p, err := e.Compile("state.count * inputs.multiplier")
//	v, err := p.Run(ctx, env)
//
// env may be a map[string]any, a struct, or a pointer to a struct.
package expr

import (
	"context"
	"errors"
	"fmt"
	"go/parser"
	"go/scanner"
	"go/token"
	"strings"

	"github.com/deepnoodle-ai/expr/jsonlit"
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

// Engine compiles expressions into reusable programs. An Engine is safe
// for concurrent use once configured. Create one with New.
type Engine struct {
	funcs    map[string]any
	prepared map[string]*preparedFunc
}

// Option configures an Engine.
type Option func(*Engine)

// New returns an Engine with no functions registered. Use WithBuiltins to
// opt in to the standard builtin set, WithFunctions to register your own,
// or both.
func New(opts ...Option) *Engine {
	e := &Engine{funcs: map[string]any{}, prepared: map[string]*preparedFunc{}}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// WithBuiltins registers the standard builtin function set returned by
// Builtins. Pair it with WithFunctions to extend or override individual
// entries; options apply in the order they are passed to New, so a later
// WithFunctions wins over an earlier WithBuiltins for any shared name.
func WithBuiltins() Option {
	return WithFunctions(Builtins())
}

// WithFunctions registers the given functions as callable identifiers in
// every compiled expression. Entries merge into (and override) whatever is
// already registered.
//
// Functions may take any Go types as arguments; expr converts evaluated
// values to the declared parameter types at call time. Return signatures of
// `T`, `(T, error)`, and `()` are supported. Variadic functions are also
// supported.
func WithFunctions(funcs map[string]any) Option {
	return func(e *Engine) {
		for name, fn := range funcs {
			e.funcs[name] = fn
			pf, err := prepareFunc(name, fn)
			if err != nil {
				// Defer error surfacing to call-time; store a
				// nil-native preparedFunc entry so lookups still
				// find the name (useful for better error hints).
				e.prepared[name] = &preparedFunc{name: name}
				continue
			}
			e.prepared[name] = pf
		}
	}
}

// CompileOption configures a single Compile or Eval call. Unlike Option,
// which configures the Engine as a whole, CompileOptions are per-invocation
// and never mutate the Engine.
type CompileOption func(*compileConfig)

type compileConfig struct {
	jsonLiterals bool
}

// WithJSONLiterals enables a source rewrite that lets callers write
// JSON-style array and object literals directly in an expression:
//
//	[1, 2, 3]         // becomes []any{1, 2, 3}
//	{"name": "ada"}   // becomes map[string]any{"name": "ada"}
//
// The rewrite is opt-in and applies only to the single Compile or Eval
// call it is passed to. It is implemented by the jsonlit subpackage; see
// that package's documentation for the exact classification rules.
// When the option is not set, bare bracket/brace literals are rejected
// by the Go parser as before.
func WithJSONLiterals() CompileOption {
	return func(c *compileConfig) {
		c.jsonLiterals = true
	}
}

// Compile parses an expression once for repeated evaluation. The returned
// Program is immutable and safe for concurrent use. Input longer than
// MaxSourceLength is rejected without calling the parser.
//
// Pass WithJSONLiterals to enable JSON-style bracket/brace literals for
// this compile.
func (e *Engine) Compile(code string, opts ...CompileOption) (*Program, error) {
	if len(code) > MaxSourceLength {
		return nil, fmt.Errorf("%w: source length %d exceeds maximum %d",
			ErrCompile, len(code), MaxSourceLength)
	}
	var cfg compileConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	parsed := code
	if cfg.jsonLiterals {
		parsed = jsonlit.Rewrite(parsed)
	}
	parsed = preprocessSource(parsed)
	node, err := parser.ParseExpr(parsed)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCompile, err)
	}
	p := &Program{source: code, root: node, funcs: e.funcs, prepared: e.prepared}
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

// Eval compiles and runs code in one step. Prefer Compile+Run when the
// same expression will be evaluated multiple times.
//
// env may be a map[string]any, a struct, or a pointer to a struct; see
// Program.Run for details. ctx is threaded into evaluation and auto-
// injected into registered functions whose first parameter is
// context.Context.
//
// Pass WithJSONLiterals to enable JSON-style bracket/brace literals for
// this evaluation.
func (e *Engine) Eval(ctx context.Context, code string, env any, opts ...CompileOption) (any, error) {
	p, err := e.Compile(code, opts...)
	if err != nil {
		return nil, err
	}
	return p.Run(ctx, env)
}

// Compiler returns this Engine wrapped in the package-level Compiler
// interface, suitable for embedding in host programs that accept any
// generic scripting backend (Template, for example).
func (e *Engine) Compiler() Compiler {
	return engineCompiler{engine: e}
}

// --- package-level convenience over a default engine ---

// defaultEngine backs the package-level Compile and Eval helpers. Like
// New(), it has no functions registered; callers who want builtins or
// custom functions should construct their own Engine.
var defaultEngine = New()

// Compile is shorthand for defaultEngine.Compile. The default engine
// has no functions registered, so expressions that call builtins must
// go through an engine created with expr.New(expr.WithBuiltins()).
func Compile(code string, opts ...CompileOption) (*Program, error) {
	return defaultEngine.Compile(code, opts...)
}

// Eval is shorthand for defaultEngine.Eval. The default engine has no
// functions registered, so expressions that call builtins must go
// through an engine created with expr.New(expr.WithBuiltins()). env
// may be a map[string]any, a struct, or a pointer to a struct.
func Eval(ctx context.Context, code string, env any, opts ...CompileOption) (any, error) {
	return defaultEngine.Eval(ctx, code, env, opts...)
}

// --- Compiler adapter ---

// engineCompiler bridges the *Engine concrete type to the Compiler
// interface. The wrapper exists only because Go has no covariant return
// types: Engine.Compile returns *Program, but Compiler.Compile is
// declared to return Script. *Program already satisfies Script, so the
// wrapper is a one-line forwarder.
type engineCompiler struct{ engine *Engine }

func (s engineCompiler) Compile(code string) (Script, error) {
	return s.engine.Compile(code)
}
