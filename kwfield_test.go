package expr

import (
	"context"
	"testing"

	"github.com/deepnoodle-ai/expr/internal/require"
)

func TestKeywordFieldNames(t *testing.T) {
	env := map[string]any{
		"event": map[string]any{"type": "push", "range": []any{1, 2}, "map": "m", "if": true,
			"sender": map[string]any{"type": "Bot"}},
	}
	for src, want := range map[string]any{
		`event.type`:                       "push",
		`event.sender.type == "Bot"`:       true,
		`len(event.range) == 2`:            true,
		`event.map`:                        "m",
		`event.if`:                         true,
		`event?.type`:                      "push",
		`map(event.range, it * 2)[1] == 4`: true,
		`"a.type"`:                         "a.type",
		`{"type": event.type}.type`:        "push",
	} {
		p, err := Compile(src, WithBuiltins())
		require.NoError(t, err, src)
		got, err := p.Run(context.Background(), env)
		require.NoError(t, err, src)
		require.Equal(t, want, got, src)
	}
	// Keywords are still keywords where they are not field names.
	_, err := Compile(`type`)
	require.Error(t, err)
}

func TestKeywordFieldNamesOptional(t *testing.T) {
	env := map[string]any{"a": map[string]any{"type": map[string]any{"b": 1}}, "n": nil}
	for src, want := range map[string]any{
		`a?.type?.b == 1`: true,
		`a.type?.b == 1`:  true,
		`n?.type`:         nil,
	} {
		p, err := Compile(src, WithBuiltins())
		require.NoError(t, err, src)
		got, err := p.Run(context.Background(), env)
		require.NoError(t, err, src)
		require.Equal(t, want, got, src)
	}
}
