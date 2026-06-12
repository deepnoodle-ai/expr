// Package expr is a zero-dependency expression evaluator built on top of
// go/parser. It accepts the subset of Go expression syntax useful for
// conditions, templates, and parameter interpolation: identifiers,
// selectors, index expressions, arithmetic, comparisons, logical
// operators, calls to registered functions, and pipelines
// (`a | f(x)` compiles as `f(a, x)`).
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
	"github.com/deepnoodle-ai/expr/internal/optaccess"
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

// compileConfig is the resolved set of options for a single Compile
// or NewTemplate. It is consumed during parsing to build the function
// dispatch tables baked into the resulting Program. The tmpl* fields
// are consumed only by NewTemplate; Compile rejects options that set
// them (tracked in templateOnly) so a misplaced template option fails
// at load time instead of being silently ignored.
type compileConfig struct {
	funcs      map[string]any
	prepared   map[string]*preparedFunc
	fieldTags  *structTagConfig
	evalBudget int
	errs       []error

	tmplOpen      string
	tmplClose     string
	tmplFormatter func(v any) (string, bool)
	templateOnly  []string
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
//
// Invalid registrations — a nil entry, a value that is not a Go
// function, or a function with an unsupported signature (more than two
// return values, or a second return that is not error) — cause Compile
// to fail with ErrCompile. Surfacing the mistake at load time is the
// point of the Compile/Run split; before this check the error would
// hide until the expression first called the bad entry.
func WithFunctions(funcs map[string]any) Option {
	return func(c *compileConfig) {
		for name, fn := range funcs {
			c.funcs[name] = fn
			pf, err := prepareFunc(name, fn)
			if err != nil {
				c.errs = append(c.errs, err)
				// Keep a name-only entry so later options can still
				// override it and lookups find the name.
				c.prepared[name] = &preparedFunc{name: name}
				continue
			}
			c.prepared[name] = pf
		}
	}
}

// WithEvalBudget bounds the total work a single Run may perform. Each
// AST node evaluated — including every per-element re-evaluation of a
// higher-order form's predicate — consumes one unit; when the budget
// is exhausted, Run fails with an ErrEvaluate-wrapped error.
//
// MaxSourceLength and MaxEvalDepth bound memory and stack, but not
// CPU: nested higher-order forms multiply, so a ~60-byte expression
// like map(xs, map(xs, map(xs, it))) over a 10k-element list is 10^12
// predicate evaluations. Context deadlines cap wall-clock time but
// still let one expression burn a core for the full timeout; a budget
// makes hostile-input behavior deterministic and cheap to reject.
//
// n <= 0 means unlimited (the default). The budget counts evaluator
// steps, not time spent inside registered functions — bound those
// separately (see docs/guides/sandboxing.md).
func WithEvalBudget(n int) Option {
	return func(c *compileConfig) {
		c.evalBudget = n
	}
}

// WithStructTags enables struct field lookup by the named struct tags.
// Tags are checked in the order provided before falling back to the Go
// exported field name. Tag options after a comma are ignored, so
// `json:"name,omitempty"` resolves as `name` and `json:",omitempty"`
// falls back to the Go field name.
//
// The option is opt-in; without it, struct fields resolve only by Go field
// name, preserving expr's default behavior. `expr:"-"` hides a field when
// the expr tag is configured; duplicate resolved names return an ambiguity
// error at evaluation time.
func WithStructTags(names ...string) Option {
	return func(c *compileConfig) {
		c.fieldTags = newStructTagConfig(names)
	}
}

// WithFieldTags is an alias for [WithStructTags].
func WithFieldTags(names ...string) Option {
	return WithStructTags(names...)
}

// Compile parses an expression once for repeated evaluation. The returned
// Program is immutable and safe for concurrent use. Input longer than
// MaxSourceLength is rejected without calling the parser.
//
// Functions are registered via Options and baked into the returned
// Program. JSON-style array and object literals ([1, 2, 3], {"k": v}) are
// always accepted; see docs/reference/spec.md for the exact rules.
func Compile(code string, opts ...Option) (*Program, error) {
	cfg := newCompileConfig()
	for _, opt := range opts {
		opt(cfg)
	}
	if len(cfg.templateOnly) > 0 {
		return nil, fmt.Errorf("%w: %s applies only to NewTemplate",
			ErrCompile, strings.Join(cfg.templateOnly, ", "))
	}
	return compileWithConfig(code, cfg)
}

// compileWithConfig compiles code against an already-resolved option
// set. NewTemplate uses it to compile every `${...}` segment without
// re-applying the option closures per segment (and without tripping
// the template-only option check that guards Compile).
func compileWithConfig(code string, cfg *compileConfig) (*Program, error) {
	if len(code) > MaxSourceLength {
		return nil, fmt.Errorf("%w: source length %d exceeds maximum %d",
			ErrCompile, len(code), MaxSourceLength)
	}
	if len(cfg.errs) > 0 {
		return nil, fmt.Errorf("%w: %w", ErrCompile, errors.Join(cfg.errs...))
	}
	// Pipeline order: optaccess turns `?.`/`?[` into sentinel calls
	// while the source still uses raw operator syntax; jsonlit then
	// rewrites bare composite literals; preprocessSource handles the
	// keyword rewrites (`map`) so the parser accepts them as
	// identifiers.
	parsed := preprocessSource(jsonlit.Rewrite(optaccess.Rewrite(code)))
	fset := token.NewFileSet()
	node, err := parser.ParseExprFrom(fset, "", parsed, 0)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCompile, err)
	}
	// Desugar pipe expressions (`a | f(x)` → `f(a, x)`) before
	// validation so validate and the evaluator only ever see ordinary
	// call nodes. See pipe.go.
	node, err = desugarPipes(fset, node)
	if err != nil {
		return nil, err
	}
	if err := validate(fset, node); err != nil {
		return nil, err
	}
	p := &Program{
		source:     code,
		root:       node,
		funcs:      cfg.funcs,
		prepared:   cfg.prepared,
		fieldTags:  cfg.fieldTags,
		evalBudget: cfg.evalBudget,
	}
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

// ifFuncName is the internal identifier that `if` is rewritten to
// before parsing. `if` is a Go statement keyword, so the expression
// parser refuses it as an operand; the rewrite makes the builtin
// callable as `if(cond, t, f)` while keeping the user-visible name
// in error messages and method lookups.
const ifFuncName = "__expr_if__"

// trySelectFormName and tryIndexFormName are the internal sentinel
// identifiers emitted by the optaccess pre-parse rewrite for the
// optional-access operators `?.` and `?[`. The evaluator dispatches
// them as special forms in higherOrderForms so they can short-circuit
// on a nil receiver and treat missing fields / out-of-range indices
// as nil rather than as errors. Users never type these names
// directly.
const (
	trySelectFormName = "__try_select__"
	tryIndexFormName  = "__try_index__"
)

// keywordRewrites lists the Go keyword tokens that expr accepts as
// ordinary identifiers. Each entry maps the source spelling to its
// internal sentinel; preprocessSource walks tokens and substitutes
// occurrences in place.
var keywordRewrites = []struct {
	tok      token.Token
	src      string
	internal string
}{
	{token.MAP, "map", mapFormName},
	{token.IF, "if", ifFuncName},
}

// preprocessSource rewrites Go keyword tokens that expr wants to
// accept as ordinary identifiers (`map`, `if`). Go's parser treats
// these as the start of a map-type literal or an if-statement, so
// expr cannot accept `map(xs, pred)` or `if(cond, t, f)` as-is;
// preprocessSource substitutes a sentinel identifier so the parser
// sees a plain CallExpr, and the evaluator translates the sentinel
// back via displayIdent for lookups and error messages.
//
// The `map` rewrite skips occurrences immediately followed by `[`, so
// Go map type literals such as `map[string]int{}` still produce the
// usual "unsupported syntax" error at evaluation rather than being
// silently turned into a meaningless call. `if` has no analogous
// safe context inside expr expressions, so every `if` token is
// rewritten unconditionally.
func preprocessSource(src string) string {
	// Fast path: skip the scanner unless the source mentions one of
	// the rewritable keywords. Substring checks on short inputs are
	// much cheaper than spinning up go/scanner.
	if !containsAnyRewriteKeyword(src) {
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
		rule, ok := matchRewrite(toks[i].tok)
		if !ok {
			continue
		}
		// Leave `map[...]` alone so Go map type literals continue to
		// produce a normal "unsupported syntax" error at eval time.
		if rule.tok == token.MAP && i+1 < len(toks) && toks[i+1].tok == token.LBRACK {
			continue
		}
		off := file.Offset(toks[i].pos)
		out.WriteString(src[last:off])
		out.WriteString(rule.internal)
		last = off + len(rule.src)
	}
	if last == 0 {
		return src
	}
	out.WriteString(src[last:])
	return out.String()
}

func containsAnyRewriteKeyword(src string) bool {
	for _, r := range keywordRewrites {
		if strings.Contains(src, r.src) {
			return true
		}
	}
	return false
}

func matchRewrite(tok token.Token) (struct {
	tok      token.Token
	src      string
	internal string
}, bool) {
	for _, r := range keywordRewrites {
		if r.tok == tok {
			return r, true
		}
	}
	return struct {
		tok      token.Token
		src      string
		internal string
	}{}, false
}

// displayIdent converts an internal rewritten identifier back to the
// name the user originally typed, for use in error messages and
// field/method lookups.
func displayIdent(name string) string {
	switch name {
	case mapFormName:
		return "map"
	case ifFuncName:
		return "if"
	}
	return name
}
