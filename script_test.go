package expr

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/deepnoodle-ai/expr/internal/require"
)

// stubCompiler is a minimal Compiler used by tests that exercise the
// engine-neutral helpers (Template, NoopCompiler, etc.) without spinning
// up the real expr Engine. It only supports dot-separated identifier
// lookups against the env (assumed to be map[string]any).
type stubCompiler struct{}

func (stubCompiler) Compile(code string) (Script, error) {
	expression := strings.TrimSpace(code)
	if expression == "" {
		return nil, fmt.Errorf("empty expression")
	}
	return &stubScript{expr: expression}, nil
}

type stubScript struct {
	expr string
}

func (s *stubScript) Run(ctx context.Context, env any) (any, error) {
	parts := strings.Split(s.expr, ".")
	var current any = env
	for _, part := range parts {
		m, ok := current.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("not a map at %q", part)
		}
		v, ok := m[part]
		if !ok {
			return nil, fmt.Errorf("undefined variable %q", part)
		}
		current = v
	}
	return current, nil
}

func TestTemplate(t *testing.T) {
	engine := stubCompiler{}

	t.Run("plain string without template variables", func(t *testing.T) {
		tmpl, err := NewTemplate(engine, "Hello World")
		require.NoError(t, err)
		got, err := tmpl.Eval(context.Background(), nil)
		require.NoError(t, err)
		require.Equal(t, "Hello World", got)
	})

	t.Run("single template variable", func(t *testing.T) {
		tmpl, err := NewTemplate(engine, "Hello ${state.name}")
		require.NoError(t, err)
		got, err := tmpl.Eval(context.Background(), map[string]any{
			"state": map[string]any{"name": "Alice"},
		})
		require.NoError(t, err)
		require.Equal(t, "Hello Alice", got)
	})

	t.Run("multiple template variables", func(t *testing.T) {
		tmpl, err := NewTemplate(engine, "${state.greeting} ${state.name}")
		require.NoError(t, err)
		got, err := tmpl.Eval(context.Background(), map[string]any{
			"state": map[string]any{
				"greeting": "Hello",
				"name":     "Bob",
			},
		})
		require.NoError(t, err)
		require.Equal(t, "Hello Bob", got)
	})

	t.Run("unclosed brace is rejected", func(t *testing.T) {
		_, err := NewTemplate(engine, "Hello ${name")
		require.Error(t, err)
		require.Contains(t, err.Error(), "unclosed `${`")
	})
}

func TestNoopCompiler(t *testing.T) {
	var c NoopCompiler
	_, err := c.Compile("anything")
	require.True(t, errors.Is(err, ErrNoCompiler))
}

func TestIsTruthyValue(t *testing.T) {
	tests := []struct {
		name   string
		value  any
		expect bool
	}{
		{"nil", nil, false},
		{"true bool", true, true},
		{"false bool", false, false},
		{"nonzero int", 42, true},
		{"zero int", 0, false},
		{"nonzero int64", int64(1), true},
		{"zero int64", int64(0), false},
		{"nonzero float64", 3.14, true},
		{"zero float64", 0.0, false},
		{"nonempty string", "hello", true},
		{"empty string", "", false},
		{"false string lowercase", "false", false},
		{"false string mixed case", "FaLsE", false},
		{"nonempty []any", []any{1}, true},
		{"empty []any", []any{}, false},
		{"nonempty map", map[string]any{"a": 1}, true},
		{"empty map", map[string]any{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.expect, IsTruthyValue(tt.value))
		})
	}
}

func TestEachValue(t *testing.T) {
	t.Run("string slice", func(t *testing.T) {
		result, err := EachValue([]string{"a", "b", "c"})
		require.NoError(t, err)
		require.Equal(t, []any{"a", "b", "c"}, result)
	})

	t.Run("int slice", func(t *testing.T) {
		result, err := EachValue([]int{1, 2, 3})
		require.NoError(t, err)
		require.Equal(t, []any{1, 2, 3}, result)
	})

	t.Run("any slice", func(t *testing.T) {
		input := []any{"hello", 42, true}
		result, err := EachValue(input)
		require.NoError(t, err)
		require.Equal(t, input, result)
	})

	t.Run("map converts to key-value pairs", func(t *testing.T) {
		result, err := EachValue(map[string]any{"key": "value"})
		require.NoError(t, err)
		require.Len(t, result, 1)
		item := result[0].(map[string]any)
		require.Equal(t, "key", item["key"])
		require.Equal(t, "value", item["value"])
	})

	t.Run("scalar errors", func(t *testing.T) {
		_, err := EachValue(42)
		require.Error(t, err)
	})

	t.Run("string errors", func(t *testing.T) {
		_, err := EachValue("hello")
		require.Error(t, err)
	})

	t.Run("struct errors", func(t *testing.T) {
		_, err := EachValue(struct{ X int }{X: 1})
		require.Error(t, err)
	})

	t.Run("nil yields nil", func(t *testing.T) {
		result, err := EachValue(nil)
		require.NoError(t, err)
		require.Nil(t, result)
	})
}
