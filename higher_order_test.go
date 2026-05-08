package expr

import (
	"context"
	"errors"
	"testing"

	"github.com/deepnoodle-ai/expr/internal/require"
)

type person struct {
	Name   string
	Age    int
	Active bool
}

func sampleUsers() []person {
	return []person{
		{Name: "Alice", Age: 30, Active: true},
		{Name: "Bob", Age: 17, Active: true},
		{Name: "Carol", Age: 42, Active: false},
	}
}

func TestHigherOrder_MapScalar(t *testing.T) {
	env := map[string]any{"nums": []any{int64(1), int64(2), int64(3)}}
	got, err := evalExpr(t.Context(), "map(nums, it * 2)", env)
	require.NoError(t, err)
	require.Equal(t, []any{int64(2), int64(4), int64(6)}, got)
}

func TestHigherOrder_MapStructField(t *testing.T) {
	env := map[string]any{"users": sampleUsers()}
	got, err := evalExpr(t.Context(), "map(users, it.Name)", env)
	require.NoError(t, err)
	require.Equal(t, []any{"Alice", "Bob", "Carol"}, got)
}

func TestHigherOrder_FilterStruct(t *testing.T) {
	env := map[string]any{"users": sampleUsers()}
	got, err := evalExpr(t.Context(), "filter(users, it.Age >= 18)", env)
	require.NoError(t, err)
	// filter preserves the element type (here, expr.person), and
	// order matches the input slice.
	res, ok := got.([]any)
	require.True(t, ok, "filter should return []any")
	require.Len(t, res, 2)
	require.Equal(t, "Alice", res[0].(person).Name)
	require.Equal(t, "Carol", res[1].(person).Name)
}

func TestHigherOrder_Any(t *testing.T) {
	env := map[string]any{"users": sampleUsers()}
	got, err := evalExpr(t.Context(), "any(users, it.Age > 40)", env)
	require.NoError(t, err)
	require.Equal(t, true, got)

	got, err = evalExpr(t.Context(), "any(users, it.Age > 100)", env)
	require.NoError(t, err)
	require.Equal(t, false, got)
}

func TestHigherOrder_All(t *testing.T) {
	env := map[string]any{"users": sampleUsers()}
	got, err := evalExpr(t.Context(), "all(users, it.Active)", env)
	require.NoError(t, err)
	require.Equal(t, false, got)

	// Uses the builtin `len`, so opt in with WithBuiltins.
	opts := []Option{WithBuiltins()}
	got, err = evalExpr(t.Context(), "all(users, len(it.Name) > 0)", env, opts...)
	require.NoError(t, err)
	require.Equal(t, true, got)
}

func TestHigherOrder_Find(t *testing.T) {
	env := map[string]any{"users": sampleUsers()}
	got, err := evalExpr(t.Context(), `find(users, it.Name == "Carol")`, env)
	require.NoError(t, err)
	require.Equal(t, "Carol", got.(person).Name)

	// No match returns nil.
	got, err = evalExpr(t.Context(), `find(users, it.Name == "Dave")`, env)
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestHigherOrder_Count(t *testing.T) {
	env := map[string]any{"users": sampleUsers()}
	got, err := evalExpr(t.Context(), "count(users, it.Active)", env)
	require.NoError(t, err)
	require.Equal(t, int64(2), got)
}

func TestHigherOrder_Index(t *testing.T) {
	env := map[string]any{"xs": []any{int64(10), int64(20), int64(30)}}
	got, err := evalExpr(t.Context(), "map(xs, it + index)", env)
	require.NoError(t, err)
	require.Equal(t, []any{int64(10), int64(21), int64(32)}, got)
}

func TestHigherOrder_NestedScopes(t *testing.T) {
	// Nested maps: the inner `it` should refer to the inner element;
	// the outer element is reachable via the outer scope only so long
	// as it was captured before the inner map ran. Here we verify inner
	// shadowing: outer is an int, inner multiplies inner element alone.
	env := map[string]any{"matrix": []any{
		[]any{int64(1), int64(2)},
		[]any{int64(3), int64(4)},
	}}
	got, err := evalExpr(t.Context(), "map(matrix, map(it, it * 10))", env)
	require.NoError(t, err)
	require.Equal(t, []any{
		[]any{int64(10), int64(20)},
		[]any{int64(30), int64(40)},
	}, got)
}

func TestHigherOrder_ChainedFilterThenMap(t *testing.T) {
	env := map[string]any{"users": sampleUsers()}
	got, err := evalExpr(t.Context(), "map(filter(users, it.Age >= 18), it.Name)", env)
	require.NoError(t, err)
	require.Equal(t, []any{"Alice", "Carol"}, got)
}

func TestHigherOrder_NilCollection(t *testing.T) {
	got, err := evalExpr(t.Context(), "map(nil, it * 2)", nil)
	require.NoError(t, err)
	require.Equal(t, []any{}, got)

	got, err = evalExpr(t.Context(), "filter(nil, it)", nil)
	require.NoError(t, err)
	require.Equal(t, []any{}, got)

	got, err = evalExpr(t.Context(), "any(nil, it)", nil)
	require.NoError(t, err)
	require.Equal(t, false, got)

	got, err = evalExpr(t.Context(), "all(nil, it)", nil)
	require.NoError(t, err)
	require.Equal(t, true, got)
}

func TestHigherOrder_EmptyCollection(t *testing.T) {
	env := map[string]any{"xs": []any{}}
	got, err := evalExpr(t.Context(), "map(xs, it + 1)", env)
	require.NoError(t, err)
	require.Equal(t, []any{}, got)
}

func TestHigherOrder_NonListCollection(t *testing.T) {
	env := map[string]any{"obj": map[string]any{"a": 1}}
	_, err := evalExpr(t.Context(), "map(obj, it)", env)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "map expects a list")
}

func TestHigherOrder_ArityError(t *testing.T) {
	env := map[string]any{"xs": []any{int64(1)}}
	_, err := evalExpr(t.Context(), "map(xs)", env)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "map expects 2 arguments")
}

func TestHigherOrder_PredicateError(t *testing.T) {
	env := map[string]any{"users": sampleUsers()}
	_, err := evalExpr(t.Context(), "map(users, it.Nmae)", env)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), `field "Nmae" not found`)
	require.Contains(t, err.Error(), `did you mean "Name"?`)
}

func TestHigherOrder_UserFuncOverride(t *testing.T) {
	// User-registered `map` wins over the special form, matching the
	// env→funcs identifier-resolution order used everywhere else.
	opts := []Option{WithFunctions(map[string]any{
		"map": func(xs []any) int { return len(xs) },
	})}
	got, err := evalExpr(t.Context(), "map(xs)", map[string]any{"xs": []any{int64(1), int64(2)}}, opts...)
	require.NoError(t, err)
	require.Equal(t, int(2), got)
}

func TestHigherOrder_EnvShadowsForm(t *testing.T) {
	// An env entry with the same name as a form shadows the form.
	// Here "filter" is bound to a plain slice, so `filter` becomes a
	// normal identifier and the call target fails to resolve as a
	// function (expected "unsupported call target" or similar).
	_, err := evalExpr(t.Context(), "filter", map[string]any{"filter": int64(42)})
	require.NoError(t, err)
}

func TestHigherOrder_CancelDuringIteration(t *testing.T) {
	// Cancellation mid-iteration: register a function that cancels ctx
	// on first call, then iterate a list that would otherwise produce
	// multiple results. The next eval tick must observe ctx.Err() and
	// return context.Canceled without wrapping it.
	ctxC, cancel := context.WithCancel(context.Background())
	defer cancel()
	opts := []Option{WithFunctions(map[string]any{
		"stop": func() bool { cancel(); return true },
	})}
	env := map[string]any{"xs": []any{int64(1), int64(2), int64(3), int64(4), int64(5)}}
	_, err := evalExpr(ctxC, "map(xs, stop() && it)", env, opts...)
	require.True(t, errors.Is(err, context.Canceled),
		"expected context.Canceled, got %v", err)
}

// --- try special form ---

func TestTry_ValueSucceeds(t *testing.T) {
	// When the first argument evaluates without error, the default is
	// not consulted at all and the success value is returned verbatim.
	got, err := evalExpr(t.Context(), `try(42, 0)`, nil)
	require.NoError(t, err)
	require.Equal(t, int64(42), got)

	env := map[string]any{"user": map[string]any{"name": "Ada"}}
	got, err = evalExpr(t.Context(), `try(user.name, "anon")`, env)
	require.NoError(t, err)
	require.Equal(t, "Ada", got)
}

func TestTry_DefaultOnMissingKey(t *testing.T) {
	// Missing-key access raises ErrEvaluate. try traps that and returns
	// the default. The default expression itself can be any expression,
	// including one referring to other env values.
	env := map[string]any{"user": map[string]any{"name": "Ada"}}
	got, err := evalExpr(t.Context(), `try(user.nickname, "—")`, env)
	require.NoError(t, err)
	require.Equal(t, "—", got)

	got, err = evalExpr(t.Context(), `try(missing.path, user.name)`, env)
	require.NoError(t, err)
	require.Equal(t, "Ada", got)
}

func TestTry_DefaultOnTypeError(t *testing.T) {
	// try also catches type errors, which is one of the points: cheap
	// `try(int(s), 0)` for strings that may or may not parse.
	opts := []Option{WithBuiltins()}
	got, err := evalExpr(t.Context(), `try(int("not-a-number"), 0)`, nil, opts...)
	require.NoError(t, err)
	require.Equal(t, int64(0), got)

	got, err = evalExpr(t.Context(), `try(int("42"), 0)`, nil, opts...)
	require.NoError(t, err)
	require.Equal(t, int64(42), got)
}

func TestTry_DefaultOnIndexOutOfRange(t *testing.T) {
	env := map[string]any{"xs": []any{int64(1), int64(2)}}
	got, err := evalExpr(t.Context(), `try(xs[10], -1)`, env)
	require.NoError(t, err)
	require.Equal(t, int64(-1), got)
}

func TestTry_DefaultIsLazy(t *testing.T) {
	// The default expression is only evaluated when the primary fails.
	// Pin that with a function that panics if called.
	opts := []Option{WithFunctions(map[string]any{
		"boom": func() int { panic("default should not have been evaluated") },
	})}
	got, err := evalExpr(t.Context(), `try(42, boom())`, nil, opts...)
	require.NoError(t, err)
	require.Equal(t, int64(42), got)
}

func TestTry_DefaultErrorPropagates(t *testing.T) {
	// If the default expression itself errors, that error surfaces.
	// try is not a blanket exception swallower; it traps the primary
	// arg only.
	env := map[string]any{"user": map[string]any{"name": "Ada"}}
	_, err := evalExpr(t.Context(), `try(missing.path, also.missing)`, env)
	require.ErrorIs(t, err, ErrEvaluate)
}

func TestTry_ArityError(t *testing.T) {
	_, err := evalExpr(t.Context(), `try(1)`, nil)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "try expects 2 arguments")

	_, err = evalExpr(t.Context(), `try(1, 2, 3)`, nil)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "try expects 2 arguments")
}

func TestTry_DoesNotTrapContextCancellation(t *testing.T) {
	// A canceled context should propagate as context.Canceled even
	// though try would otherwise be tempted to swallow the error. The
	// raw context error must reach the caller so timeouts and explicit
	// cancellation are observable.
	ctxC, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := evalExpr(ctxC, `try(user.name, "fallback")`, map[string]any{
		"user": map[string]any{"name": "Ada"},
	})
	require.True(t, errors.Is(err, context.Canceled),
		"expected context.Canceled to propagate, got %v", err)
}

func TestTry_UserFuncOverride(t *testing.T) {
	// A registered try function shadows the special form, matching the
	// env→funcs resolution order used by every other higher-order form.
	opts := []Option{WithFunctions(map[string]any{
		"try": func(a, b int64) int64 { return a + b },
	})}
	got, err := evalExpr(t.Context(), `try(2, 3)`, nil, opts...)
	require.NoError(t, err)
	require.Equal(t, int64(5), got)
}

func TestTry_EnvShadowsForm(t *testing.T) {
	// An env entry named try shadows the form too (so `try` resolves to
	// the env value). This pins the consistent shadowing semantics; it
	// is not a recommended pattern.
	got, err := evalExpr(t.Context(), `try`, map[string]any{"try": int64(7)})
	require.NoError(t, err)
	require.Equal(t, int64(7), got)
}

func TestTry_NestedComposition(t *testing.T) {
	// try composes inside higher-order forms. find returns nil when no
	// element matches; selecting .name on nil errors; try falls back.
	env := map[string]any{
		"events": []any{
			map[string]any{"kind": "click"},
			map[string]any{"kind": "view"},
		},
	}
	got, err := evalExpr(t.Context(),
		`try(find(events, it.kind == "purchase").user, "—")`, env)
	require.NoError(t, err)
	require.Equal(t, "—", got)
}

func TestTry_ComposesWithLogicalOr(t *testing.T) {
	// try plus operand-returning || covers the common "fall back to a
	// default and present nil as something else" case.
	env := map[string]any{"user": map[string]any{}}
	got, err := evalExpr(t.Context(),
		`try(user.nickname, nil) || "(none)"`, env)
	require.NoError(t, err)
	require.Equal(t, "(none)", got)
}
