package expr

import (
	"testing"

	"github.com/deepnoodle-ai/expr/internal/require"
)

// ---------------------------------------------------------------------------
// entries()
// ---------------------------------------------------------------------------

func TestEntries_EmptyMap(t *testing.T) {
	got, err := evalExpr(t.Context(), `entries(m)`, map[string]any{"m": map[string]any{}}, WithBuiltins())
	require.NoError(t, err)
	require.Equal(t, []any{}, got)
}

func TestEntries_NilMap(t *testing.T) {
	got, err := evalExpr(t.Context(), `entries(m)`, map[string]any{"m": nil}, WithBuiltins())
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestEntries_SingleKey(t *testing.T) {
	m := map[string]any{"x": int64(42)}
	got, err := evalExpr(t.Context(), `entries(m)`, map[string]any{"m": m}, WithBuiltins())
	require.NoError(t, err)
	want := []any{
		map[string]any{"key": "x", "value": int64(42)},
	}
	require.Equal(t, want, got)
}

// entries must sort by key for determinism regardless of map iteration order.
func TestEntries_MultiKeySorted(t *testing.T) {
	m := map[string]any{"c": int64(3), "a": int64(1), "b": int64(2)}
	got, err := evalExpr(t.Context(), `entries(m)`, map[string]any{"m": m}, WithBuiltins())
	require.NoError(t, err)
	want := []any{
		map[string]any{"key": "a", "value": int64(1)},
		map[string]any{"key": "b", "value": int64(2)},
		map[string]any{"key": "c", "value": int64(3)},
	}
	require.Equal(t, want, got)
}

// entries must match the sort order produced by keys() for the same map.
func TestEntries_SortMatchesKeys(t *testing.T) {
	m := map[string]any{"zebra": "z", "apple": "a", "mango": "m"}
	env := map[string]any{"m": m}

	keysGot, err := evalExpr(t.Context(), `keys(m)`, env, WithBuiltins())
	require.NoError(t, err)

	entriesGot, err := evalExpr(t.Context(), `entries(m)`, env, WithBuiltins())
	require.NoError(t, err)

	keySlice := keysGot.([]any)
	entrySlice := entriesGot.([]any)
	require.Equal(t, len(keySlice), len(entrySlice))
	for i, k := range keySlice {
		entry := entrySlice[i].(map[string]any)
		require.Equal(t, k, entry["key"], "position %d key mismatch", i)
	}
}

// Non-string-keyed map must produce an ErrEvaluate, same class as keys().
func TestEntries_NonStringKeyError(t *testing.T) {
	_, err := evalExpr(t.Context(), `entries(m)`, map[string]any{"m": map[int]any{1: "x"}}, WithBuiltins())
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "entries")
}

// entries arity check.
func TestEntries_ArityError(t *testing.T) {
	_, err := evalExpr(t.Context(), `entries(m, m)`, map[string]any{"m": map[string]any{}}, WithBuiltins())
	require.ErrorIs(t, err, ErrEvaluate)
}

// Compose entries with filter: keep only entries whose value > 1.
func TestEntries_ComposeWithFilter(t *testing.T) {
	m := map[string]any{"a": int64(1), "b": int64(2), "c": int64(3)}
	env := map[string]any{"m": m}
	// filter(entries(m), it.value > 1) returns entries with value 2 and 3.
	got, err := evalExpr(t.Context(),
		`filter(entries(m), it.value > 1)`,
		env,
		WithBuiltins(),
	)
	require.NoError(t, err)
	want := []any{
		map[string]any{"key": "b", "value": int64(2)},
		map[string]any{"key": "c", "value": int64(3)},
	}
	require.Equal(t, want, got)
}

// Compose entries with map form to extract keys.
func TestEntries_ComposeWithMapForm(t *testing.T) {
	m := map[string]any{"b": int64(2), "a": int64(1)}
	env := map[string]any{"m": m}
	// map(entries(m), it.key) should equal keys(m)
	got, err := evalExpr(t.Context(), `map(entries(m), it.key)`, env, WithBuiltins())
	require.NoError(t, err)
	require.Equal(t, []any{"a", "b"}, got)
}

// ---------------------------------------------------------------------------
// sort()
// ---------------------------------------------------------------------------

func TestSort_Ints(t *testing.T) {
	env := map[string]any{"xs": []any{int64(3), int64(1), int64(2)}}
	got, err := evalExpr(t.Context(), `sort(xs)`, env, WithFunctions(CollectionFuncs()))
	require.NoError(t, err)
	require.Equal(t, []any{int64(1), int64(2), int64(3)}, got)
}

func TestSort_Floats(t *testing.T) {
	env := map[string]any{"xs": []any{3.5, 1.1, 2.2}}
	got, err := evalExpr(t.Context(), `sort(xs)`, env, WithFunctions(CollectionFuncs()))
	require.NoError(t, err)
	// All floats, all whole-or-fractional: result stays float64.
	sl := got.([]any)
	require.Equal(t, 3, len(sl))
	require.Equal(t, 1.1, sl[0])
	require.Equal(t, 2.2, sl[1])
	require.Equal(t, 3.5, sl[2])
}

func TestSort_MixedIntFloat(t *testing.T) {
	// int64 and float64 are both numeric; sort numerically.
	env := map[string]any{"xs": []any{int64(3), 1.5, int64(2)}}
	got, err := evalExpr(t.Context(), `sort(xs)`, env, WithFunctions(CollectionFuncs()))
	require.NoError(t, err)
	sl := got.([]any)
	require.Equal(t, 3, len(sl))
	// 1.5 first, then 2, then 3
	f0, ok0 := toFloat64(sl[0])
	f1, ok1 := toFloat64(sl[1])
	f2, ok2 := toFloat64(sl[2])
	require.True(t, ok0 && ok1 && ok2)
	require.Equal(t, 1.5, f0)
	require.Equal(t, float64(2), f1)
	require.Equal(t, float64(3), f2)
}

func TestSort_Strings(t *testing.T) {
	env := map[string]any{"xs": []any{"banana", "apple", "cherry"}}
	got, err := evalExpr(t.Context(), `sort(xs)`, env, WithFunctions(CollectionFuncs()))
	require.NoError(t, err)
	require.Equal(t, []any{"apple", "banana", "cherry"}, got)
}

func TestSort_Empty(t *testing.T) {
	got, err := evalExpr(t.Context(), `sort([])`, nil, WithFunctions(CollectionFuncs()))
	require.NoError(t, err)
	require.Equal(t, []any{}, got)
}

func TestSort_Nil(t *testing.T) {
	got, err := evalExpr(t.Context(), `sort(xs)`, map[string]any{"xs": nil}, WithFunctions(CollectionFuncs()))
	require.NoError(t, err)
	require.Equal(t, []any{}, got)
}

func TestSort_SingleElement(t *testing.T) {
	got, err := evalExpr(t.Context(), `sort([42])`, nil, WithFunctions(CollectionFuncs()))
	require.NoError(t, err)
	require.Equal(t, []any{int64(42)}, got)
}

func TestSort_MixedTypeError(t *testing.T) {
	env := map[string]any{"xs": []any{int64(1), "two", int64(3)}}
	_, err := evalExpr(t.Context(), `sort(xs)`, env, WithFunctions(CollectionFuncs()))
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "sort")
}

func TestSort_BoolError(t *testing.T) {
	env := map[string]any{"xs": []any{true, false}}
	_, err := evalExpr(t.Context(), `sort(xs)`, env, WithFunctions(CollectionFuncs()))
	require.ErrorIs(t, err, ErrEvaluate)
}

func TestSort_NilElementError(t *testing.T) {
	env := map[string]any{"xs": []any{int64(1), nil, int64(3)}}
	_, err := evalExpr(t.Context(), `sort(xs)`, env, WithFunctions(CollectionFuncs()))
	require.ErrorIs(t, err, ErrEvaluate)
}

func TestSort_NotAListError(t *testing.T) {
	_, err := evalExpr(t.Context(), `sort(42)`, nil, WithFunctions(CollectionFuncs()))
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "sort")
}

// sort must not mutate the input slice.
func TestSort_InputNotMutated(t *testing.T) {
	original := []any{int64(3), int64(1), int64(2)}
	// make a copy to check against
	snapshot := []any{int64(3), int64(1), int64(2)}
	env := map[string]any{"xs": original}
	_, err := evalExpr(t.Context(), `sort(xs)`, env, WithFunctions(CollectionFuncs()))
	require.NoError(t, err)
	require.Equal(t, snapshot, original)
}

// Expressions with inline literals work too.
func TestSort_InlineLiteral(t *testing.T) {
	got, err := evalExpr(t.Context(), `sort([3, 1, 2])`, nil, WithFunctions(CollectionFuncs()))
	require.NoError(t, err)
	require.Equal(t, []any{int64(1), int64(2), int64(3)}, got)
}

// ---------------------------------------------------------------------------
// reverse()
// ---------------------------------------------------------------------------

func TestReverse_List(t *testing.T) {
	env := map[string]any{"xs": []any{int64(1), int64(2), int64(3)}}
	got, err := evalExpr(t.Context(), `reverse(xs)`, env, WithFunctions(CollectionFuncs()))
	require.NoError(t, err)
	require.Equal(t, []any{int64(3), int64(2), int64(1)}, got)
}

func TestReverse_Strings(t *testing.T) {
	env := map[string]any{"xs": []any{"a", "b", "c"}}
	got, err := evalExpr(t.Context(), `reverse(xs)`, env, WithFunctions(CollectionFuncs()))
	require.NoError(t, err)
	require.Equal(t, []any{"c", "b", "a"}, got)
}

func TestReverse_Empty(t *testing.T) {
	got, err := evalExpr(t.Context(), `reverse([])`, nil, WithFunctions(CollectionFuncs()))
	require.NoError(t, err)
	require.Equal(t, []any{}, got)
}

func TestReverse_Nil(t *testing.T) {
	got, err := evalExpr(t.Context(), `reverse(xs)`, map[string]any{"xs": nil}, WithFunctions(CollectionFuncs()))
	require.NoError(t, err)
	require.Equal(t, []any{}, got)
}

func TestReverse_SingleElement(t *testing.T) {
	got, err := evalExpr(t.Context(), `reverse([99])`, nil, WithFunctions(CollectionFuncs()))
	require.NoError(t, err)
	require.Equal(t, []any{int64(99)}, got)
}

func TestReverse_NotAListError(t *testing.T) {
	_, err := evalExpr(t.Context(), `reverse("hello")`, nil, WithFunctions(CollectionFuncs()))
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "reverse")
}

// reverse must not mutate the input slice.
func TestReverse_InputNotMutated(t *testing.T) {
	original := []any{int64(1), int64(2), int64(3)}
	snapshot := []any{int64(1), int64(2), int64(3)}
	env := map[string]any{"xs": original}
	_, err := evalExpr(t.Context(), `reverse(xs)`, env, WithFunctions(CollectionFuncs()))
	require.NoError(t, err)
	require.Equal(t, snapshot, original)
}

// Compose sort + reverse for descending order.
func TestSortReverse_Descending(t *testing.T) {
	env := map[string]any{"xs": []any{int64(3), int64(1), int64(2)}}
	got, err := evalExpr(t.Context(), `reverse(sort(xs))`, env, WithFunctions(CollectionFuncs()))
	require.NoError(t, err)
	require.Equal(t, []any{int64(3), int64(2), int64(1)}, got)
}

// sort and reverse must not appear in the default Builtins set.
func TestSortReverse_NotInDefaultBuiltins(t *testing.T) {
	_, err := evalExpr(t.Context(), `sort([1, 2])`, nil, WithBuiltins())
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "unknown function")

	_, err = evalExpr(t.Context(), `reverse([1, 2])`, nil, WithBuiltins())
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "unknown function")
}
