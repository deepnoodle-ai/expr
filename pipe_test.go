package expr

import (
	"context"
	"strings"
	"testing"

	"github.com/deepnoodle-ai/expr/internal/require"
)

// The pipe operator `a | f(x)` (RFC 0001) desugars at Compile time
// into `f(a, x)`. These tests pin the rewrite itself, its precedence
// behavior, its composition with the special forms and optional
// access, the compile errors for non-call right sides, and the
// ambiguous-comparison diagnostic.

func pipeOpts(extra ...Option) []Option {
	return append([]Option{WithBuiltins()}, extra...)
}

func TestPipe_Basic(t *testing.T) {
	got, err := evalExpr(t.Context(), `"ada" | upper()`, nil, pipeOpts()...)
	require.NoError(t, err)
	require.Equal(t, "ADA", got)
}

func TestPipe_ExtraArgsShiftRight(t *testing.T) {
	funcs := map[string]any{
		"add": func(a, b int64) int64 { return a + b },
	}
	got, err := evalExpr(t.Context(), `5 | add(3)`, nil, pipeOpts(WithFunctions(funcs))...)
	require.NoError(t, err)
	require.Equal(t, int64(8), got)
}

func TestPipe_Chain(t *testing.T) {
	env := map[string]any{
		"checks": []any{
			map[string]any{"name": "fmt", "ok": true, "msg": ""},
			map[string]any{"name": "vet", "ok": false, "msg": "shadowed var"},
			map[string]any{"name": "test", "ok": false, "msg": "2 failures"},
		},
	}
	got, err := evalExpr(t.Context(),
		`checks | filter(!it.ok) | map(sprintf("- %s: %s", it.name, it.msg)) | join("\n")`,
		env, pipeOpts(WithFunctions(StringFuncs()))...)
	require.NoError(t, err)
	require.Equal(t, "- vet: shadowed var\n- test: 2 failures", got)
}

func TestPipe_IteratingForms(t *testing.T) {
	env := map[string]any{"xs": []any{3, 1, 4, 1, 5}}
	cases := []struct {
		src  string
		want any
	}{
		{`xs | filter(it > 2)`, []any{3, 4, 5}},
		{`xs | map(it * 10)`, []any{int64(30), int64(10), int64(40), int64(10), int64(50)}},
		{`xs | any(it > 4)`, true},
		{`xs | all(it > 0)`, true},
		{`xs | find(it > 3)`, 4},
		{`xs | count(it == 1)`, int64(2)},
		{`xs | sortBy(-it)`, []any{5, 4, 3, 1, 1}},
		{`xs | flatMap([it, it])`, []any{3, 3, 1, 1, 4, 4, 1, 1, 5, 5}},
	}
	for _, tc := range cases {
		t.Run(tc.src, func(t *testing.T) {
			got, err := evalExpr(t.Context(), tc.src, env)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestPipe_NamedBindingForm(t *testing.T) {
	env := map[string]any{
		"orders": []any{
			map[string]any{"id": 1, "paid": true},
			map[string]any{"id": 2, "paid": false},
			map[string]any{"id": 3, "paid": true},
		},
	}
	got, err := evalExpr(t.Context(), `orders | filter(o, o.paid) | map(o, o.id)`,
		env)
	require.NoError(t, err)
	require.Equal(t, []any{1, 3}, got)
}

func TestPipe_IntoIfAndTry(t *testing.T) {
	// Special forms receive the rewritten CallExpr like any other
	// call, so piping into `if` and `try` keeps their lazy semantics.
	env := map[string]any{"x": int64(7)}

	// `>` binds looser than `|`, so the comparison needs parens to be
	// the piped value.
	got, err := evalExpr(t.Context(), `(x > 2) | if("big", "small")`, env)
	require.NoError(t, err)
	require.Equal(t, "big", got)

	// try(value, default): the failing division is the piped value and
	// is still evaluated inside the form's error scope.
	got, err = evalExpr(t.Context(), `x / 0 | try(-1)`, env)
	require.NoError(t, err)
	require.Equal(t, int64(-1), got)
}

func TestPipe_Precedence(t *testing.T) {
	env := map[string]any{
		"xs": []any{1, 2, 3},
		"a":  "ADA",
		"b":  "ada",
		"n":  int64(4),
	}
	funcs := map[string]any{
		"double": func(n int64) int64 { return 2 * n },
	}
	cases := []struct {
		src  string
		want any
	}{
		// `|` binds tighter than comparisons: a pipe on the left is
		// the comparison operand.
		{`xs | count(it > 1) == 2`, true},
		{`xs | len() > 2`, true},
		// A parenthesized pipe is fine on the right of a comparison
		// (the bare spelling is the ambiguity diagnostic, tested
		// below).
		{`a == (b | upper())`, true},
		// Same precedence as `+`/`-`, left-associative: arithmetic on
		// the left becomes part of the piped value.
		{`n + 1 | double()`, int64(10)},
		// And a pipe result is an ordinary arithmetic operand.
		{`n | double() + 1`, int64(9)},
		// Logical operators bind looser than `|`.
		{`true && xs | any(it == 3)`, true},
		{`false || xs | all(it >= 1)`, true},
	}
	for _, tc := range cases {
		t.Run(tc.src, func(t *testing.T) {
			got, err := evalExpr(t.Context(), tc.src, env, pipeOpts(WithFunctions(funcs))...)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestPipe_AmbiguousComparisonDiagnostic(t *testing.T) {
	// RFC 0001 §3.3: a bare pipe as the right operand of a comparison
	// silently consumes the comparison's right operand, so it is an
	// ErrCompile demanding parentheses.
	bad := []struct {
		src string
		op  string
	}{
		{`a == b | upper()`, "=="},
		{`len(errors) == 0 | bool()`, "=="},
		{`a != b | upper()`, "!="},
		{`a < b | len()`, "<"},
		{`f(a >= b | len())`, ">="},
	}
	for _, tc := range bad {
		t.Run(tc.src, func(t *testing.T) {
			_, err := Compile(tc.src, pipeOpts()...)
			require.Error(t, err)
			require.ErrorIs(t, err, ErrCompile)
			require.Contains(t, err.Error(), "ambiguous expression: | on the right of "+tc.op)
		})
	}
	// Both parenthesized spellings the diagnostic suggests compile.
	for _, src := range []string{`(a | upper()) == b`, `a == (b | upper())`} {
		_, err := Compile(src, pipeOpts()...)
		require.NoError(t, err)
	}
	// A pipe as the LEFT operand of a comparison is the useful,
	// unambiguous order and never trips the diagnostic.
	_, err := Compile(`xs | count(it > 1) == 2`, pipeOpts()...)
	require.NoError(t, err)
}

func TestPipe_NestedInsideFormBody(t *testing.T) {
	env := map[string]any{
		"users": []any{
			map[string]any{"name": "ada", "tags": []any{"a", "b"}},
			map[string]any{"name": "bob", "tags": []any{"c"}},
		},
	}
	got, err := evalExpr(t.Context(),
		`users | map(it.tags | join("+"))`,
		env, WithFunctions(StringFuncs()))
	require.NoError(t, err)
	require.Equal(t, []any{"a+b", "c"}, got)
}

func TestPipe_OptionalAccess(t *testing.T) {
	env := map[string]any{
		"user":  map[string]any{"name": "ada"},
		"ghost": nil,
		"xs": []any{
			map[string]any{"name": "ada", "ok": false},
			map[string]any{"name": "bob", "ok": true},
		},
	}
	got, err := evalExpr(t.Context(), `user?.name | upper()`, env, pipeOpts()...)
	require.NoError(t, err)
	require.Equal(t, "ADA", got)

	// RFC 0001 §4.1: a nil optional-access result pipes nil into the
	// call; the iterating forms treat nil as an empty list.
	got, err = evalExpr(t.Context(), `ghost?.orders | filter(it.paid)`, env, pipeOpts()...)
	require.NoError(t, err)
	require.Equal(t, []any{}, got)

	// Optional access composes on a parenthesized pipe result.
	got, err = evalExpr(t.Context(), `(xs | find(it.ok))?.name`, env)
	require.NoError(t, err)
	require.Equal(t, "bob", got)

	// ... including when the pipeline found nothing.
	got, err = evalExpr(t.Context(), `(xs | find(it.name == "eve"))?.name`, env)
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestPipe_EnvCallable(t *testing.T) {
	env := map[string]any{
		"x":      int64(3),
		"triple": func(n int64) int64 { return 3 * n },
	}
	got, err := evalExpr(t.Context(), `x | triple()`, env)
	require.NoError(t, err)
	require.Equal(t, int64(9), got)
}

func TestPipe_InsideStringLiteralUntouched(t *testing.T) {
	got, err := evalExpr(t.Context(), `"a|b" | upper()`, nil, pipeOpts()...)
	require.NoError(t, err)
	require.Equal(t, "A|B", got)
}

func TestPipe_NonCallRHSErrors(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string // substring required in the error message
	}{
		{"literal_rhs", `1 | 2`, `"2" is not a call`},
		{"ident_rhs", `xs | foo`, `"foo" is not a call (did you mean to write foo(...)?)`},
		{"form_ident_rhs", `xs | filter`, `"filter" is a special form, did you mean to write filter(predicate)?`},
		{"form_keyword_rhs", `xs | map`, `"map" is a special form, did you mean to write map(predicate)?`},
		{"form_try_rhs", `xs | try`, `"try" is a special form, did you mean to write try(default)?`},
		{"selector_rhs", `xs | f().name`, `"f().name" is not a call`},
		{"index_rhs", `xs | f()[0]`, `"f()[0]" is not a call`},
		{"paren_rhs", `xs | (f())`, `is not a call`},
		{"chained_bad_stage", `xs | f() | 2`, `"2" is not a call`},
		{"opt_select_rhs", `xs | a?.b`, `"a?.b" is not a call`},
		{"opt_index_rhs", `xs | a?[0]`, `"a?[0]" is not a call`},
		{"nested_in_args", `f(1 | 2)`, `is not a call`},
		{"nested_in_list", `[1 | 2]`, `is not a call`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Compile(tc.src, pipeOpts()...)
			require.Error(t, err)
			require.ErrorIs(t, err, ErrCompile)
			require.Contains(t, err.Error(), "pipe operator | requires a function call on the right-hand side")
			require.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestPipe_UnknownFunctionErrorsAtRun(t *testing.T) {
	// Like any other call, an unregistered pipe target compiles (the
	// env may provide the callable at Run) and fails at evaluation.
	p, err := Compile(`xs | nosuchfn()`, pipeOpts()...)
	require.NoError(t, err)
	_, err = p.Run(context.Background(), map[string]any{"xs": []any{1}})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "nosuchfn")
}

func TestPipe_Identifiers(t *testing.T) {
	p, err := Compile(`orders | filter(o, o.paid && o.total < limit) | map(o, o.id)`,
		pipeOpts()...)
	require.NoError(t, err)
	require.Equal(t, []string{"limit", "orders"}, p.Identifiers())
}

func TestPipe_Template(t *testing.T) {
	tmpl, err := NewTemplate(
		"failing: ${checks | filter(!it.ok) | map(it.name) | join(\", \")}",
		pipeOpts(WithFunctions(StringFuncs()))...)
	require.NoError(t, err)
	out, err := tmpl.Render(t.Context(), map[string]any{
		"checks": []any{
			map[string]any{"name": "vet", "ok": false},
			map[string]any{"name": "fmt", "ok": true},
			map[string]any{"name": "test", "ok": false},
		},
	})
	require.NoError(t, err)
	require.Equal(t, "failing: vet, test", out)
}

func TestPipe_SourceUnchanged(t *testing.T) {
	src := `xs | filter(it > 0)`
	p, err := Compile(src)
	require.NoError(t, err)
	require.Equal(t, src, p.Source())
}

func TestPipe_EquivalentToNestedCalls(t *testing.T) {
	env := map[string]any{"xs": []any{5, -2, 9, 0}}
	opts := pipeOpts(WithFunctions(StringFuncs()))
	piped, err := evalExpr(t.Context(),
		`xs | filter(it > 0) | map(string(it)) | join("-")`, env, opts...)
	require.NoError(t, err)
	nested, err := evalExpr(t.Context(),
		`join(map(filter(xs, it > 0), string(it)), "-")`, env, opts...)
	require.NoError(t, err)
	require.Equal(t, nested, piped)
	require.Equal(t, "5-9", piped)
}

func TestPipe_LongChainStaysWithinDepthLimit(t *testing.T) {
	// A pipeline desugars to nested calls, so an absurdly long chain
	// must hit MaxEvalDepth as an error, not a stack overflow.
	src := `"x"` + strings.Repeat(` | upper()`, MaxEvalDepth+8)
	p, err := Compile(src, pipeOpts()...)
	require.NoError(t, err)
	_, err = p.Run(context.Background(), nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "nested too deeply")
}
