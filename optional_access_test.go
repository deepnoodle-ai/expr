package expr

import (
	"testing"

	"github.com/deepnoodle-ai/expr/internal/require"
)

// --- ?. (optional selector) ---

func TestOptionalSelect_PresentField(t *testing.T) {
	env := map[string]any{
		"user": map[string]any{"nickname": "ada"},
	}
	got, err := evalExpr(t.Context(), `user?.nickname`, env)
	require.NoError(t, err)
	require.Equal(t, "ada", got)
}

func TestOptionalSelect_MissingKeyReturnsNil(t *testing.T) {
	env := map[string]any{
		"user": map[string]any{},
	}
	got, err := evalExpr(t.Context(), `user?.nickname`, env)
	require.NoError(t, err)
	require.Equal(t, nil, got)
}

func TestOptionalSelect_NilReceiverReturnsNil(t *testing.T) {
	env := map[string]any{"user": nil}
	got, err := evalExpr(t.Context(), `user?.nickname`, env)
	require.NoError(t, err)
	require.Equal(t, nil, got)
}

func TestOptionalSelect_NilPointerReturnsNil(t *testing.T) {
	type profile struct{ Name string }
	var p *profile
	env := map[string]any{"profile": p}
	got, err := evalExpr(t.Context(), `profile?.Name`, env)
	require.NoError(t, err)
	require.Equal(t, nil, got)
}

func TestOptionalSelect_StructPresent(t *testing.T) {
	type profile struct{ Name string }
	env := map[string]any{"profile": &profile{Name: "Ada"}}
	got, err := evalExpr(t.Context(), `profile?.Name`, env)
	require.NoError(t, err)
	require.Equal(t, "Ada", got)
}

// Chained optional selects collapse to nil at the first missing
// link, so the caller does not have to guard each level.
func TestOptionalSelect_Chained(t *testing.T) {
	env := map[string]any{
		"user": map[string]any{},
	}
	got, err := evalExpr(t.Context(), `user?.profile?.nickname`, env)
	require.NoError(t, err)
	require.Equal(t, nil, got)
}

// Composes with operand-returning || to provide a fallback for the
// missing case.
func TestOptionalSelect_WithFallback(t *testing.T) {
	env := map[string]any{
		"user": map[string]any{},
	}
	got, err := evalExpr(t.Context(), `user?.nickname || "(none)"`, env)
	require.NoError(t, err)
	require.Equal(t, "(none)", got)
}

// `?.` only swallows the missing-key case. A real type error (e.g.,
// trying to select on a value that isn't a struct or map) still
// surfaces so genuine bugs are not hidden.
func TestOptionalSelect_TypeErrorPropagates(t *testing.T) {
	env := map[string]any{"x": 42}
	_, err := evalExpr(t.Context(), `x?.field`, env)
	require.ErrorIs(t, err, ErrEvaluate)
}

// --- ?[ (optional index) ---

func TestOptionalIndex_PresentSlice(t *testing.T) {
	env := map[string]any{"xs": []any{int64(10), int64(20), int64(30)}}
	got, err := evalExpr(t.Context(), `xs?[1]`, env)
	require.NoError(t, err)
	require.Equal(t, int64(20), got)
}

func TestOptionalIndex_OutOfRangeReturnsNil(t *testing.T) {
	env := map[string]any{"xs": []any{int64(10)}}
	got, err := evalExpr(t.Context(), `xs?[5]`, env)
	require.NoError(t, err)
	require.Equal(t, nil, got)
}

func TestOptionalIndex_NilReceiverReturnsNil(t *testing.T) {
	env := map[string]any{"xs": nil}
	got, err := evalExpr(t.Context(), `xs?[0]`, env)
	require.NoError(t, err)
	require.Equal(t, nil, got)
}

func TestOptionalIndex_MissingMapKeyReturnsNil(t *testing.T) {
	env := map[string]any{"obj": map[string]any{"a": 1}}
	got, err := evalExpr(t.Context(), `obj?["missing"]`, env)
	require.NoError(t, err)
	require.Equal(t, nil, got)
}

func TestOptionalIndex_TypeErrorPropagates(t *testing.T) {
	env := map[string]any{"xs": []any{int64(1)}}
	// Indexing a slice with a string is a wrong-kind error, not a
	// "missing index". It must propagate.
	_, err := evalExpr(t.Context(), `xs?["zero"]`, env)
	require.ErrorIs(t, err, ErrEvaluate)
}

// `?[` then `?.` and vice versa compose without surprises.
func TestOptionalIndex_ChainedWithSelect(t *testing.T) {
	env := map[string]any{
		"users": []any{
			map[string]any{"name": "Ada"},
			map[string]any{},
		},
	}
	cases := []struct {
		expr string
		want any
	}{
		{`users?[0]?.name`, "Ada"},
		{`users?[1]?.name`, nil},
		{`users?[5]?.name`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := evalExpr(t.Context(), tc.expr, env)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// `?.` inside a higher-order predicate is the natural shape for
// "first user with a nickname" and similar shapes.
func TestOptionalSelect_InPredicate(t *testing.T) {
	env := map[string]any{
		"users": []any{
			map[string]any{},
			map[string]any{"nickname": "Ada"},
		},
	}
	got, err := evalExpr(t.Context(), `find(users, it?.nickname)`, env)
	require.NoError(t, err)
	require.Equal(t, map[string]any{"nickname": "Ada"}, got)
}

// `?.` and `?[` written inside string literals must be preserved
// verbatim: the rewrite must skip string content.
func TestOptionalAccess_StringLiteralUntouched(t *testing.T) {
	got, err := evalExpr(t.Context(), `"obj?.field"`, nil)
	require.NoError(t, err)
	require.Equal(t, "obj?.field", got)
}

// Array and object literals are primaries, so `?.` / `?[` work on
// them directly — including when the literal opens the expression.
func TestOptionalAccess_LiteralReceivers(t *testing.T) {
	cases := []struct {
		expr string
		want any
	}{
		{`[1, 2, 3]?[0]`, int64(1)},
		{`[1, 2, 3]?[9]`, nil},
		{`{"a": 1}?.a`, int64(1)},
		{`{"a": 1}?.missing`, nil},
		{`{"a": [1, 2]}?.a?[1]`, int64(2)},
		{`1 + [10, 20]?[1]`, int64(21)},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := evalExpr(t.Context(), tc.expr, nil)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// The results of `map(...)` and `if(...)` calls accept optional
// access just like ordinary function calls, even though Go reserves
// those keywords.
func TestOptionalAccess_SpecialFormReceivers(t *testing.T) {
	env := map[string]any{"xs": []any{int64(1), int64(2)}}
	cases := []struct {
		expr string
		want any
	}{
		{`map(xs, it * 2)?[0]`, int64(2)},
		{`map(xs, it * 2)?[9]`, nil},
		{`if(true, {"a": 1}, nil)?.a`, int64(1)},
		{`filter(xs, it > 1)?[0]`, int64(2)},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := evalExpr(t.Context(), tc.expr, env, WithBuiltins())
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// `?.map` and `?.if` mirror the non-optional `.map` / `.if`
// selectors, which expr accepts despite Go reserving the keywords.
func TestOptionalSelect_KeywordField(t *testing.T) {
	env := map[string]any{
		"a": map[string]any{"map": int64(7), "if": int64(8)},
		"b": map[string]any{},
	}
	cases := []struct {
		expr string
		want any
	}{
		{`a?.map`, int64(7)},
		{`a?.if`, int64(8)},
		{`b?.map`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := evalExpr(t.Context(), tc.expr, env)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// Calling the result of an optional access is not supported; the
// mistake is reported at Compile time with a message that names the
// operator rather than leaking the internal sentinel.
func TestOptionalAccess_CallResultRejectedAtCompile(t *testing.T) {
	for _, src := range []string{`a?.b()`, `a?[0]()`} {
		t.Run(src, func(t *testing.T) {
			_, err := Compile(src)
			require.ErrorIs(t, err, ErrCompile)
			require.Contains(t, err.Error(), "optional access")
		})
	}
}
