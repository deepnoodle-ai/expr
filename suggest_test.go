package expr

import (
	"testing"

	"github.com/deepnoodle-ai/expr/internal/require"
)

func TestSuggest_UndefinedIdentDidYouMean(t *testing.T) {
	env := map[string]any{
		"username": "alice",
		"age":      30,
	}
	_, err := evalExpr(t.Context(), "usernmae", env)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), `undefined identifier "usernmae"`)
	require.Contains(t, err.Error(), `did you mean "username"?`)
}

func TestSuggest_UndefinedIdentAvailableList(t *testing.T) {
	// Small candidate set with no close match: the hint falls back to
	// listing the available names. With no CompileOptions there are no
	// functions registered, so the only candidates are the env entries
	// plus the higher-order form names. One env entry keeps the total
	// inside the cap that formatHint uses to decide whether a list is
	// short enough to be useful.
	env := map[string]any{"alpha": 1}
	_, err := evalExpr(t.Context(), "zzz", env)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), `undefined identifier "zzz"`)
	require.Contains(t, err.Error(), "available:")
	require.Contains(t, err.Error(), "alpha")
}

func TestSuggest_MissingStructField(t *testing.T) {
	p := person{Name: "Alice", Age: 30}
	env := map[string]any{"p": p}
	_, err := evalExpr(t.Context(), "p.Nmae", env)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), `field "Nmae" not found`)
	require.Contains(t, err.Error(), `did you mean "Name"?`)
}

func TestSuggest_MissingMapKey(t *testing.T) {
	env := map[string]any{
		"obj": map[string]any{"name": "Alice", "age": 30},
	}
	_, err := evalExpr(t.Context(), "obj.naem", env)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), `key "naem" not found`)
	require.Contains(t, err.Error(), `did you mean "name"?`)
}

func TestSuggest_MissingMapKeyByIndex(t *testing.T) {
	env := map[string]any{
		"obj": map[string]any{"name": "Alice"},
	}
	_, err := evalExpr(t.Context(), `obj["naem"]`, env)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), `key "naem" not found`)
	require.Contains(t, err.Error(), `did you mean "name"?`)
}

func TestSuggest_UnknownFunction(t *testing.T) {
	// `lowre` should suggest `lower` (builtin). Opts in to WithBuiltins
	// so `lower` is actually in the candidate set.
	opts := []Option{WithBuiltins()}
	_, err := evalExpr(t.Context(), `lowre("FOO")`, nil, opts...)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), `unknown function "lowre"`)
	require.Contains(t, err.Error(), `did you mean "lower"?`)
}

func TestSuggest_UnknownFunctionSuggestsForm(t *testing.T) {
	// `fliter` should suggest the `filter` higher-order form.
	_, err := evalExpr(t.Context(), `fliter([1, 2], it > 0)`, nil)
	// Composite literal [1,2] is unsupported, so the error will come
	// from the evaluator after parsing succeeds. What we actually want
	// to test is that a bad name `fliter` suggests `filter`. Use a
	// simpler form that doesn't require a composite literal.
	_ = err
	env := map[string]any{"xs": []any{int64(1), int64(2)}}
	_, err = evalExpr(t.Context(), "fliter(xs, it > 0)", env)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), `unknown function "fliter"`)
	require.Contains(t, err.Error(), `did you mean "filter"?`)
}

func TestSuggest_UnknownMethod(t *testing.T) {
	env := map[string]any{"p": &person{Name: "Alice"}}
	// No method `greet` exists — but also no field. Test the hint
	// format; struct only exports fields here, so the suggestion
	// should point at one of them.
	_, err := evalExpr(t.Context(), "p.Gree()", env)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "not found")
}

// TestPreprocess_MethodCallUnaffected verifies that a method call
// `x.map(...)` is NOT rewritten by the source preprocessor — users
// with types that expose a method literally named `map` should still
// be able to invoke it.
func TestPreprocess_MethodCallUnaffected(t *testing.T) {
	type withMap struct{}
	env := map[string]any{
		"obj": map[string]any{
			"map": func(x int) int { return x * 10 },
		},
	}
	got, err := evalExpr(t.Context(), "obj.map(5)", env)
	require.NoError(t, err)
	require.Equal(t, int(50), got)
	_ = withMap{}
}

func TestPreprocess_FastPath(t *testing.T) {
	// Expressions without the literal substring "map" must not touch
	// the scanner. This test is really a behavior check: a normal
	// expression should compile and evaluate correctly.
	got, err := evalExpr(t.Context(), "1 + 2 * 3", nil)
	require.NoError(t, err)
	require.Equal(t, int64(7), got)
}

func containsAny(haystack string, needles ...string) bool {
	for _, n := range needles {
		if len(n) == 0 {
			continue
		}
		if indexOf(haystack, n) >= 0 {
			return true
		}
	}
	return false
}

func indexOf(s, sub string) int {
	// Tiny, dependency-free version of strings.Index so this test
	// file doesn't pull in strings just for one helper.
	if len(sub) == 0 {
		return 0
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
