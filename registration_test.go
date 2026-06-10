package expr

import (
	"context"
	"testing"

	"github.com/deepnoodle-ai/expr/internal/require"
)

// --- Compile-time registration validation ---------------------------------

func TestRegistration_NonFunctionFailsCompile(t *testing.T) {
	_, err := Compile("f()", WithFunctions(map[string]any{"f": 42}))
	require.ErrorIs(t, err, ErrCompile)
	require.Contains(t, err.Error(), `function "f" is not a function`)
}

func TestRegistration_NilEntryFailsCompile(t *testing.T) {
	_, err := Compile("f()", WithFunctions(map[string]any{"f": nil}))
	require.ErrorIs(t, err, ErrCompile)
	require.Contains(t, err.Error(), `function "f" is nil`)
}

func TestRegistration_TypedNilFuncFailsCompile(t *testing.T) {
	var fn func() int
	_, err := Compile("f()", WithFunctions(map[string]any{"f": fn}))
	require.ErrorIs(t, err, ErrCompile)
	require.Contains(t, err.Error(), "nil function value")
}

// The check fires even when the expression never calls the bad entry:
// the registration itself is the host bug being surfaced.
func TestRegistration_UnreferencedBadEntryStillFails(t *testing.T) {
	_, err := Compile("1 + 1", WithFunctions(map[string]any{"unused": "nope"}))
	require.ErrorIs(t, err, ErrCompile)
}

func TestRegistration_MultipleErrorsAllReported(t *testing.T) {
	_, err := Compile("1", WithFunctions(map[string]any{
		"a": 1,
		"b": func() (int, int, int) { return 0, 0, 0 },
	}))
	require.ErrorIs(t, err, ErrCompile)
	require.Contains(t, err.Error(), `"a"`)
	require.Contains(t, err.Error(), `"b"`)
}

// A later option can override a bad earlier entry... but the bad
// registration still fails Compile: options are validated as written,
// not after merging, so the mistake never hides behind an override.
func TestRegistration_BadEntryNotMaskedByOverride(t *testing.T) {
	_, err := Compile("f()",
		WithFunctions(map[string]any{"f": 42}),
		WithFunctions(map[string]any{"f": func() int { return 1 }}),
	)
	require.ErrorIs(t, err, ErrCompile)
}

// Non-function values belong in the env, which continues to work.
func TestRegistration_ConstantsGoInEnv(t *testing.T) {
	got, err := evalExpr(t.Context(), "pi * 2", map[string]any{"pi": 3.14})
	require.NoError(t, err)
	require.Equal(t, 6.28, got)
}

// --- Panic recovery across dispatch paths ----------------------------------

// A registered function that hits a runtime panic (nil deref, index
// out of range) is converted to ErrEvaluate instead of crashing the
// host through Run. Prepared fast path.
func TestPanicRecovery_RegisteredFunction(t *testing.T) {
	opts := []Option{WithFunctions(map[string]any{
		"boom": func(i int) int {
			var xs []int
			return xs[i] // index out of range
		},
	})}
	_, err := evalExpr(t.Context(), "boom(3)", nil, opts...)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), `"boom" panicked`)
}

// Same protection for callables stored in the env, which dispatch
// through the non-prepared reflect path.
func TestPanicRecovery_EnvFunction(t *testing.T) {
	env := map[string]any{
		"boom": func() int {
			var p *int
			return *p // nil dereference
		},
	}
	_, err := evalExpr(t.Context(), "boom()", env)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), `"boom" panicked`)
}

// Deliberate panics with non-runtime values are not swallowed: they
// signal programmer intent and propagate to the caller.
func TestPanicRecovery_ExplicitPanicPropagates(t *testing.T) {
	env := map[string]any{
		"boom": func() int { panic("deliberate") },
	}
	p, err := Compile("boom()")
	require.NoError(t, err)
	require.Panics(t, func() {
		_, _ = p.Run(context.Background(), env)
	})
}
