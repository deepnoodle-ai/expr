package expr

import (
	"testing"

	"github.com/deepnoodle-ai/expr/internal/require"
)

func TestIsTruthy(t *testing.T) {
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
		{"false string lowercase", "false", true},
		{"false string mixed case", "FaLsE", true},
		{"nonempty []any", []any{1}, true},
		{"empty []any", []any{}, false},
		{"nonempty map", map[string]any{"a": 1}, true},
		{"empty map", map[string]any{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.expect, IsTruthy(tt.value))
		})
	}
}
