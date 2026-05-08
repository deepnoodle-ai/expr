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
