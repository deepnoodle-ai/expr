package expr

import (
	"testing"

	"github.com/deepnoodle-ai/expr/internal/require"
)

func mathOpts() []Option       { return []Option{WithFunctions(MathFuncs())} }
func stringOpts() []Option     { return []Option{WithFunctions(StringFuncs())} }
func collectionOpts() []Option { return []Option{WithFunctions(CollectionFuncs())} }

func TestMathFuncs_MinMax(t *testing.T) {
	cases := []struct {
		expr string
		want any
	}{
		{`min(3, 1, 2)`, int64(1)},
		{`max(3, 1, 2)`, int64(3)},
		{`min(5)`, int64(5)},
		{`min(2, 1.5)`, 1.5},
		{`max(2, 1.5)`, float64(2)},
		{`min(-1, 1)`, int64(-1)},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := evalExpr(t.Context(), tc.expr, nil, mathOpts()...)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}

	_, err := evalExpr(t.Context(), `min()`, nil, mathOpts()...)
	require.ErrorIs(t, err, ErrEvaluate)

	_, err = evalExpr(t.Context(), `min(1, "x")`, nil, mathOpts()...)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "expected number")
}

func TestMathFuncs_Abs(t *testing.T) {
	got, err := evalExpr(t.Context(), `abs(-3)`, nil, mathOpts()...)
	require.NoError(t, err)
	require.Equal(t, int64(3), got)

	got, err = evalExpr(t.Context(), `abs(2.5) + abs(-2.5)`, nil, mathOpts()...)
	require.NoError(t, err)
	require.Equal(t, float64(5), got)

	// MinInt64 has no positive counterpart; checked like unary minus.
	_, err = evalExpr(t.Context(), `abs(n)`, map[string]any{"n": int64(-9223372036854775808)}, mathOpts()...)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "integer overflow")
}

func TestMathFuncs_Rounding(t *testing.T) {
	cases := []struct {
		expr string
		want any
	}{
		{`floor(2.7)`, float64(2)},
		{`ceil(2.1)`, float64(3)},
		{`round(2.5)`, float64(3)},
		{`round(-2.5)`, float64(-3)},
		// Integers pass through without a type change.
		{`floor(4)`, int64(4)},
		{`ceil(4)`, int64(4)},
		{`round(4)`, int64(4)},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := evalExpr(t.Context(), tc.expr, nil, mathOpts()...)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestStringFuncs(t *testing.T) {
	cases := []struct {
		expr string
		want any
	}{
		{`trim("  hi  ")`, "hi"},
		{`split("a,b,c", ",")`, []any{"a", "b", "c"}},
		{`join(["a", "b"], "-")`, "a-b"},
		{`join([], "-")`, ""},
		{`replace("a.b.c", ".", "/")`, "a/b/c"},
		{`startsWith("hello", "he")`, true},
		{`startsWith("hello", "lo")`, false},
		{`endsWith("hello", "lo")`, true},
		{`endsWith("hello", "he")`, false},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := evalExpr(t.Context(), tc.expr, nil, stringOpts()...)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestStringFuncs_SplitJoinRoundTrip(t *testing.T) {
	got, err := evalExpr(t.Context(), `join(split("a b c", " "), "_")`, nil, stringOpts()...)
	require.NoError(t, err)
	require.Equal(t, "a_b_c", got)
}

func TestStringFuncs_Errors(t *testing.T) {
	for _, src := range []string{
		`trim(1)`,
		`split(1, ",")`,
		`split("a", 1)`,
		`join("not-a-list", ",")`,
		`join([1], ",")`,
		`replace(1, "a", "b")`,
		`startsWith(1, "a")`,
		`endsWith("a", 1)`,
	} {
		_, err := evalExpr(t.Context(), src, nil, stringOpts()...)
		require.ErrorIs(t, err, ErrEvaluate, src)
	}
}

func TestCollectionFuncs_FirstLast(t *testing.T) {
	env := map[string]any{"xs": []any{int64(1), int64(2), int64(3)}}

	got, err := evalExpr(t.Context(), `first(xs)`, env, collectionOpts()...)
	require.NoError(t, err)
	require.Equal(t, int64(1), got)

	got, err = evalExpr(t.Context(), `last(xs)`, env, collectionOpts()...)
	require.NoError(t, err)
	require.Equal(t, int64(3), got)

	// Empty and nil lists yield nil, mirroring find's no-match result.
	for _, src := range []string{`first([])`, `last([])`, `first(nil)`, `last(nil)`} {
		got, err = evalExpr(t.Context(), src, nil, collectionOpts()...)
		require.NoError(t, err, src)
		require.Nil(t, got, src)
	}

	_, err = evalExpr(t.Context(), `first("str")`, nil, collectionOpts()...)
	require.ErrorIs(t, err, ErrEvaluate)
}

func TestCollectionFuncs_Sum(t *testing.T) {
	cases := []struct {
		expr string
		want any
	}{
		{`sum([1, 2, 3])`, int64(6)},
		{`sum([1, 2.5])`, 3.5},
		{`sum([])`, int64(0)},
		{`sum(nil)`, int64(0)},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := evalExpr(t.Context(), tc.expr, nil, collectionOpts()...)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}

	_, err := evalExpr(t.Context(), `sum([1, "x"])`, nil, collectionOpts()...)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "not a number")

	env := map[string]any{"big": []any{int64(9223372036854775807), int64(1)}}
	_, err = evalExpr(t.Context(), `sum(big)`, env, collectionOpts()...)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "integer overflow")
}

func TestCollectionFuncs_Slice(t *testing.T) {
	env := map[string]any{"xs": []any{int64(0), int64(1), int64(2), int64(3)}}
	cases := []struct {
		expr string
		want any
	}{
		{`slice(xs, 1, 3)`, []any{int64(1), int64(2)}},
		{`slice(xs, 0, 99)`, []any{int64(0), int64(1), int64(2), int64(3)}},
		{`slice(xs, -2, 99)`, []any{int64(2), int64(3)}},
		{`slice(xs, 3, 1)`, []any{}},
		{`slice(nil, 0, 2)`, []any{}},
		{`slice("héllo", 1, 3)`, "él"},
		{`slice("hello", -3, 99)`, "llo"},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := evalExpr(t.Context(), tc.expr, env, collectionOpts()...)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}

	_, err := evalExpr(t.Context(), `slice(42, 0, 1)`, nil, collectionOpts()...)
	require.ErrorIs(t, err, ErrEvaluate)

	_, err = evalExpr(t.Context(), `slice(xs, "a", 1)`, env, collectionOpts()...)
	require.ErrorIs(t, err, ErrEvaluate)
}

// The groups must not leak into the default builtin set.
func TestGroups_NotInDefaultBuiltins(t *testing.T) {
	_, err := evalExpr(t.Context(), `min(1, 2)`, nil, WithBuiltins())
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "unknown function")
}

// Groups compose with Builtins and with each other through the usual
// last-wins option ordering.
func TestGroups_ComposeWithBuiltins(t *testing.T) {
	got, err := evalExpr(t.Context(),
		`upper(join(slice(split("a,b,c,d", ","), 0, max(2, 1)), "-"))`, nil,
		WithBuiltins(),
		WithFunctions(MathFuncs()),
		WithFunctions(StringFuncs()),
		WithFunctions(CollectionFuncs()),
	)
	require.NoError(t, err)
	require.Equal(t, "A-B", got)
}
