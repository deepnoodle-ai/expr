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
//
// # Type checking
//
// [Program.Check] checks an expression against the [Type] of each env
// name without running it, and reports every problem as a [*TypeError]:
// unknown names and fields, wrong-kind operations and builtin arguments,
// reads of values that may be nil, and comparisons that are never true.
//
//	env := map[string]*expr.Type{"user": expr.ObjectOf(map[string]*expr.Type{"name": expr.String()})}
//	t, err := p.Check(env)
package expr

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"sort"
	"strings"

	"github.com/deepnoodle-ai/expr/internal/jsonlit"
	"github.com/deepnoodle-ai/expr/internal/optaccess"
	"github.com/deepnoodle-ai/expr/internal/srcedit"
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
	// noOptimize turns off constant folding and every evaluator fast
	// path, so tests can compare optimized and general evaluation.
	noOptimize bool

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

// withoutOptimizations compiles a program that skips constant folding
// and every evaluator fast path (prepared-call cache, reflect-space
// selector and index chains, filter(...)[k] streaming, typed binary
// operators). Results must not depend on it; the differential tests in
// optimize_equiv_test.go hold that.
func withoutOptimizations() Option {
	return func(c *compileConfig) { c.noOptimize = true }
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
//
// # Compile pipeline
//
// Source passes through four token-level rewrites before go/parser sees
// it, then three tree passes. Each rewrite scans with go/scanner (so the
// contents of strings, runes and comments are never touched), plans
// splices with internal/srcedit, and returns a map from its output
// offsets back to its input, so errors report the position the user
// wrote. The stages, in order:
//
//  1. rewriteKeywordFields — input: user source. Renames a Go keyword
//     that follows a `.` (`x.type`, `x.map`) to __expr_kw_<name>. It
//     skips the `.` of `?.`, whose field optaccess quotes instead.
//     Output: no keyword follows a plain `.`.
//  2. optaccess.RewriteMap — input may contain `?.f` / `?[i]` (f may be
//     a keyword) and __expr_kw_ fields. Emits __try_select__(lhs, "f")
//     and __try_index__(lhs, i), finding lhs by walking back over
//     identifiers, selector chains, balanced brackets, calls (including
//     `map(`/`if(`, still keywords here), bare [..] / {..} literals and
//     typed []T{} / [N]T{} / map[K]V{} literals. Output: no `?.`/`?[`
//     left except ones it declined (which the parser rejects).
//  3. jsonlit.RewriteMap — input: may contain __try_* calls and the
//     `map`/`if` keywords. Rewrites bare `[..]` to []any{..} and bare
//     `{..}` to map[string]any{..}, leaving index brackets, typed
//     literals and type expressions alone. It relies on stage 2 having
//     removed `?`, so a `[` after `?` never reaches it.
//  4. preprocessSource — input: may contain map[string]any{ from stage
//  3. Renames the `map` keyword (except `map[`, a map type) and `if`
//     to the sentinels __expr_map__ and __expr_if__. Output: valid Go
//     expression syntax for every accepted form.
//
// After parsing: restoreKeywordFields gives __expr_kw_ selectors their
// real names; desugarPipes turns `a | f(x)` into f(a, x), so later
// passes see only calls; validate rejects Go syntax expr does not
// support. The evaluator dispatches the sentinels as special forms and
// displayIdent shows their user-visible names in errors.
func compileWithConfig(code string, cfg *compileConfig) (*Program, error) {
	if len(code) > MaxSourceLength {
		return nil, fmt.Errorf("%w: source length %d exceeds maximum %d",
			ErrCompile, len(code), MaxSourceLength)
	}
	if len(cfg.errs) > 0 {
		return nil, fmt.Errorf("%w: %w", ErrCompile, errors.Join(cfg.errs...))
	}
	parsed, srcMap := rewriteSource(code)
	fset := token.NewFileSet()
	node, err := parser.ParseExprFrom(fset, "", parsed, 0)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCompile, sourceErrors(err, code, srcMap))
	}
	node, err = checkTree(fset, node)
	if err != nil && !srcMap.Identity() {
		// The passes report positions through fset. Point them at code
		// and run them again on a fresh tree, so a successful compile
		// pays nothing for the translation.
		fset = token.NewFileSet()
		node, _ = parser.ParseExprFrom(fset, "", parsed, 0)
		mapPositions(fset.File(node.Pos()), code, srcMap)
		_, err = checkTree(fset, node)
	}
	if err != nil {
		return nil, err
	}
	p := &Program{
		source:     code,
		root:       node,
		funcs:      cfg.funcs,
		prepared:   cfg.prepared,
		fieldTags:  cfg.fieldTags,
		evalBudget: cfg.evalBudget,
		noOptimize: cfg.noOptimize,
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

// checkTree runs the tree passes of the compile pipeline on a freshly
// parsed node: keyword field names, pipes, validation.
func checkTree(fset *token.FileSet, node ast.Expr) (ast.Expr, error) {
	restoreKeywordFields(node)
	// Desugar pipe expressions (`a | f(x)` → `f(a, x)`) before
	// validation so validate and the evaluator only ever see ordinary
	// call nodes. See pipe.go.
	node, err := desugarPipes(fset, node)
	if err != nil {
		return nil, err
	}
	if err := validate(fset, node); err != nil {
		return nil, err
	}
	return node, nil
}

// rewriteSource runs the token-level rewrites of the compile pipeline
// (see compileWithConfig) and returns the parser's input with the map
// from its offsets back to code.
func rewriteSource(code string) (string, srcedit.Map) {
	s1, m1 := rewriteKeywordFields(code)
	s2, m2 := optaccess.RewriteMap(s1)
	s3, m3 := jsonlit.RewriteMap(s2)
	s4, m4 := preprocessSource(s3)
	return s4, m4.Then(m3).Then(m2).Then(m1)
}

// sourceErrors moves the positions in a parse error from the rewritten
// source to code.
func sourceErrors(err error, code string, m srcedit.Map) error {
	list, ok := err.(scanner.ErrorList)
	if !ok {
		return err
	}
	lines := lineStarts(code)
	out := make(scanner.ErrorList, len(list))
	for i, e := range list {
		c := *e
		c.Pos.Offset, c.Pos.Line, c.Pos.Column = sourcePosition(lines, len(code), m.Orig(e.Pos.Offset))
		out[i] = &c
	}
	return out
}

// mapPositions records on file, at the start of each rewritten segment,
// the line and column in code that it came from, so fset.Position
// reports positions in code.
func mapPositions(file *token.File, code string, m srcedit.Map) {
	lines := lineStarts(code)
	m.Segments(func(out, in int, _ bool) {
		_, line, col := sourcePosition(lines, len(code), in)
		file.AddLineColumnInfo(out, "", line, col)
	})
}

// lineStarts returns the offset of the start of each line of code.
func lineStarts(code string) []int {
	lines := []int{0}
	for i := 0; i < len(code); i++ {
		if code[i] == '\n' {
			lines = append(lines, i+1)
		}
	}
	return lines
}

// sourcePosition is the offset (clamped to the source), 1-based line and
// 1-based byte column of off.
func sourcePosition(lines []int, size, off int) (offset, line, col int) {
	off = max(0, min(off, size))
	i := sort.SearchInts(lines, off+1) - 1
	return off, i + 1, off - lines[i] + 1
}

// keywordRewrites lists the Go keyword tokens that expr accepts as
// ordinary identifiers, with the internal sentinel each becomes.
var keywordRewrites = map[token.Token]string{
	token.MAP: mapFormName,
	token.IF:  ifFuncName,
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
func preprocessSource(src string) (string, srcedit.Map) {
	// Fast path: skip the scanner unless the source mentions one of
	// the rewritable keywords. Substring checks on short inputs are
	// much cheaper than spinning up go/scanner.
	if !strings.Contains(src, "map") && !strings.Contains(src, "if") {
		return src, srcedit.Map{}
	}
	toks := srcedit.Scan(src)
	var edits []srcedit.Edit
	for i, t := range toks {
		internal, ok := keywordRewrites[t.Kind]
		if !ok {
			continue
		}
		// Leave `map[...]` alone so Go map type literals continue to
		// produce a normal "unsupported syntax" error at eval time.
		if t.Kind == token.MAP && i+1 < len(toks) && toks[i+1].Kind == token.LBRACK {
			continue
		}
		edits = append(edits, srcedit.Edit{Pos: t.Pos, End: t.End, Str: internal})
	}
	return srcedit.Apply(src, edits)
}

// keywordFieldPrefix marks a field name that is a Go keyword. Go's parser
// rejects `x.type`, so rewriteKeywordFields renames the field before
// parsing and restoreKeywordFields puts the real name back on the
// SelectorExpr, which the evaluator then looks up as written.
const keywordFieldPrefix = "__expr_kw_"

// rewriteKeywordFields renames every Go keyword that directly follows a
// `.` (`event.type`, `item.range`, `x.map`) so the expression parses. It
// leaves `?.type` alone: optaccess puts that name in a string literal.
func rewriteKeywordFields(src string) (string, srcedit.Map) {
	if !mayHaveKeywordField(src) {
		return src, srcedit.Map{}
	}
	toks := srcedit.Scan(src)
	var edits []srcedit.Edit
	for i := 1; i < len(toks); i++ {
		t, prev := toks[i], toks[i-1]
		if !t.Kind.IsKeyword() || prev.Kind != token.PERIOD {
			continue
		}
		if i >= 2 && toks[i-2].Kind == token.ILLEGAL && toks[i-2].Lit == "?" {
			continue
		}
		edits = append(edits, srcedit.Edit{Pos: t.Pos, End: t.End, Str: keywordFieldPrefix + t.Lit})
	}
	return srcedit.Apply(src, edits)
}

// mayHaveKeywordField is a cheap pre-check for rewriteKeywordFields: it
// reports whether some `.` is followed, after spaces, by a word that is a
// Go keyword, or whether src has a comment (which may sit between the two).
// A false positive only costs a scan.
func mayHaveKeywordField(src string) bool {
	if strings.Contains(src, "/") {
		return strings.Contains(src, ".")
	}
	for i := strings.IndexByte(src, '.'); i >= 0; {
		j := i + 1
		for j < len(src) && (src[j] == ' ' || src[j] == '\t' || src[j] == '\n' || src[j] == '\r') {
			j++
		}
		k := j
		for k < len(src) && 'a' <= src[k] && src[k] <= 'z' {
			k++
		}
		if k > j && token.Lookup(src[j:k]).IsKeyword() && (k == len(src) || !isIdentByte(src[k])) {
			return true
		}
		next := strings.IndexByte(src[i+1:], '.')
		if next < 0 {
			break
		}
		i += 1 + next
	}
	return false
}

func isIdentByte(c byte) bool {
	return c == '_' || '0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || c >= 0x80
}

// restoreKeywordFields undoes rewriteKeywordFields on the parsed tree.
func restoreKeywordFields(node ast.Node) {
	ast.Inspect(node, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			// Restore only names the rewrite made: the rest must be a keyword.
			if name, cut := strings.CutPrefix(sel.Sel.Name, keywordFieldPrefix); cut && token.Lookup(name).IsKeyword() {
				sel.Sel.Name = name
			}
		}
		return true
	})
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
