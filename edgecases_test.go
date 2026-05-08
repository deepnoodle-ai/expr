package expr

import (
	"math"
	"strings"
	"testing"

	"github.com/deepnoodle-ai/expr/internal/require"
)

type edgeCaseRoot struct {
	Next map[string]any
}

type edgeCaseStringKey string
type edgeCaseInt int
type edgeCaseString string

func TestEdgeCase_SelectorFastPathHonorsMaxEvalDepth(t *testing.T) {
	expr := "Next"
	for i := 0; i < MaxEvalDepth; i++ {
		expr += ".next"
	}

	env := edgeCaseRoot{Next: map[string]any{}}
	cursor := env.Next
	for i := 0; i < MaxEvalDepth; i++ {
		child := map[string]any{}
		cursor["next"] = child
		cursor = child
	}

	_, err := evalExpr(t.Context(), expr, env)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "nested too deeply")
}

func TestEdgeCase_NamedStringMapKeysDoNotPanic(t *testing.T) {
	env := map[string]any{
		"m": map[edgeCaseStringKey]any{
			"present": int64(42),
		},
	}

	got, err := evalExpr(t.Context(), "m.present", env)
	require.NoError(t, err)
	require.Equal(t, int64(42), got)

	got, err = evalExpr(t.Context(), `m["present"]`, env)
	require.NoError(t, err)
	require.Equal(t, int64(42), got)
}

func TestEdgeCase_NamedStringMapKeysForEnvAndBuiltins(t *testing.T) {
	opts := []Option{WithBuiltins()}
	env := map[edgeCaseStringKey]any{
		"present": int64(42),
		"nested":  map[edgeCaseStringKey]int{"k": 1},
	}

	got, err := evalExpr(t.Context(), "present", env)
	require.NoError(t, err)
	require.Equal(t, int64(42), got)

	got, err = evalExpr(t.Context(), `has(nested, "k")`, env, opts...)
	require.NoError(t, err)
	require.Equal(t, true, got)

	got, err = evalExpr(t.Context(), `contains(nested, "k")`, env, opts...)
	require.NoError(t, err)
	require.Equal(t, true, got)
}

func TestEdgeCase_MapIndexNumericConversionIsRangeChecked(t *testing.T) {
	env := map[string]any{
		"m": map[int8]string{1: "one"},
	}

	_, err := evalExpr(t.Context(), "m[257]", env)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrEvaluate)

	got, err := evalExpr(t.Context(), "m[1]", env)
	require.NoError(t, err)
	require.Equal(t, "one", got)
}

func TestEdgeCase_IntBuiltinRejectsNonFiniteAndOutOfRangeFloat(t *testing.T) {
	opts := []Option{WithBuiltins(), WithFunctions(map[string]any{
		"nan": func() float64 { return math.NaN() },
		"inf": func() float64 { return math.Inf(1) },
	})}

	for _, expr := range []string{
		"int(nan())",
		"int(inf())",
		"int(9223372036854775808.0)",
	} {
		t.Run(strings.TrimPrefix(expr, "int("), func(t *testing.T) {
			_, err := evalExpr(t.Context(), expr, nil, opts...)
			require.Error(t, err)
			require.ErrorIs(t, err, ErrEvaluate)
		})
	}
}

func TestEdgeCase_TruthinessHonorsTypedNilAndNamedScalars(t *testing.T) {
	type node struct{ Value int }

	opts := []Option{WithBuiltins()}
	env := map[string]any{
		"ptr":        (*node)(nil),
		"zeroInt":    edgeCaseInt(0),
		"nonzeroInt": edgeCaseInt(2),
		"emptyStr":   edgeCaseString(""),
		"falseStr":   edgeCaseString("false"),
		"numStr":     edgeCaseString("41"),
		"needle":     edgeCaseString("al"),
	}

	cases := []struct {
		expr string
		want any
	}{
		{"bool(ptr)", false},
		{"!ptr", true},
		{"bool(zeroInt)", false},
		{"bool(nonzeroInt)", true},
		{"zeroInt + 2", int64(2)},
		{`bool(emptyStr)`, false},
		{`bool(falseStr)`, false},
		{`int(numStr) + 1`, int64(42)},
		{`upper(falseStr)`, "FALSE"},
		{`contains(falseStr, needle)`, true},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := evalExpr(t.Context(), tc.expr, env, opts...)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestEdgeCase_CyclicValuesFormatWithoutStackOverflow(t *testing.T) {
	cycle := map[string]any{}
	cycle["self"] = cycle
	env := map[string]any{"cycle": cycle}
	opts := []Option{WithBuiltins()}

	tmpl, err := NewTemplate("value=${cycle}", opts...)
	require.NoError(t, err)
	out, err := tmpl.Render(t.Context(), env)
	require.NoError(t, err)
	require.Contains(t, out, "<cycle>")

	got, err := evalExpr(t.Context(), "string(cycle)", env, opts...)
	require.NoError(t, err)
	require.Contains(t, got, "<cycle>")

	got, err = evalExpr(t.Context(), `sprintf("%v", cycle)`, env, opts...)
	require.NoError(t, err)
	require.Contains(t, got, "<cycle>")
}
