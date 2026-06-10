package expr

import (
	"strings"
	"sync"
	"testing"

	"github.com/deepnoodle-ai/expr/internal/require"
)

func TestBudget_UnderLimitSucceeds(t *testing.T) {
	got, err := evalExpr(t.Context(), "1 + 2 * 3", nil, WithEvalBudget(100))
	require.NoError(t, err)
	require.Equal(t, int64(7), got)
}

func TestBudget_ExceededReturnsErrEvaluate(t *testing.T) {
	env := map[string]any{"xs": make([]any, 1000)}
	_, err := evalExpr(t.Context(), "map(xs, map(xs, map(xs, it)))", env, WithEvalBudget(10_000))
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "evaluation budget exceeded")
}

// The budget is deterministic: the same program, env, and limit either
// always succeeds or always fails, independent of wall-clock speed.
func TestBudget_Deterministic(t *testing.T) {
	env := map[string]any{"xs": []any{int64(1), int64(2), int64(3)}}
	p, err := Compile("count(xs, it > 1)", WithEvalBudget(5))
	require.NoError(t, err)
	for i := 0; i < 10; i++ {
		_, err := p.Run(t.Context(), env)
		require.ErrorIs(t, err, ErrEvaluate)
		require.Contains(t, err.Error(), "evaluation budget exceeded")
	}
}

// Each Run gets the full budget; a Run that consumed most of the
// budget must not starve the next one.
func TestBudget_PerRun(t *testing.T) {
	env := map[string]any{"xs": []any{int64(1), int64(2), int64(3)}}
	p, err := Compile("count(xs, it > 1)", WithEvalBudget(100))
	require.NoError(t, err)
	for i := 0; i < 5; i++ {
		got, err := p.Run(t.Context(), env)
		require.NoError(t, err)
		require.Equal(t, int64(2), got)
	}
}

func TestBudget_ConcurrentRunsIndependent(t *testing.T) {
	env := map[string]any{"xs": []any{int64(1), int64(2), int64(3)}}
	p, err := Compile("count(xs, it > 1)", WithEvalBudget(100))
	require.NoError(t, err)
	var wg sync.WaitGroup
	errs := make([]error, 16)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = p.Run(t.Context(), env)
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
}

// try cannot be used to escape the budget: once exhausted, every
// subsequent node evaluation (including try's fallback) fails too.
func TestBudget_TryDoesNotEscape(t *testing.T) {
	env := map[string]any{"xs": make([]any, 1000)}
	_, err := evalExpr(t.Context(), "try(map(xs, map(xs, it)), 42)", env, WithEvalBudget(100))
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "evaluation budget exceeded")
}

func TestBudget_ZeroAndNegativeMeanUnlimited(t *testing.T) {
	env := map[string]any{"xs": make([]any, 100)}
	for _, n := range []int{0, -1} {
		got, err := evalExpr(t.Context(), "len(map(xs, it))", env, WithBuiltins(), WithEvalBudget(n))
		require.NoError(t, err)
		require.Equal(t, 100, got)
	}
}

// A budget on a template bounds each placeholder expression.
func TestBudget_Template(t *testing.T) {
	tpl, err := NewTemplate("total: ${count(xs, it > 0)}", WithEvalBudget(10))
	require.NoError(t, err)
	xs := make([]any, 1000)
	for i := range xs {
		xs[i] = int64(i)
	}
	env := map[string]any{"xs": xs}
	_, err = tpl.Render(t.Context(), env)
	require.Error(t, err)
	if !strings.Contains(err.Error(), "evaluation budget exceeded") {
		t.Fatalf("expected budget error, got: %v", err)
	}
}
