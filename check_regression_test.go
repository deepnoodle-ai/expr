package expr

import (
	"context"
	"testing"

	"github.com/deepnoodle-ai/expr/internal/require"
)

func TestCheckRegressionNullableEquality(t *testing.T) {
	for _, tc := range []struct {
		name string
		a, b *Type
	}{
		{"different kinds", String().OrNil(), Number().OrNil()},
		{"disjoint enums", EnumOf("a").OrNil(), EnumOf("b").OrNil()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, comparison := range []struct {
				source string
				want   bool
			}{
				{"a == b", true},
				{"a != b", false},
			} {
				t.Run(comparison.source, func(t *testing.T) {
					p, err := Compile(comparison.source)
					require.NoError(t, err)
					got, err := p.Run(context.Background(), map[string]any{"a": nil, "b": nil})
					require.NoError(t, err)
					require.Equal(t, comparison.want, got)

					// Disjoint non-nil values still share nil, so comparing
					// these nullable types is valid.
					typ, err := p.Check(map[string]*Type{"a": tc.a, "b": tc.b})
					require.NoError(t, err)
					require.Equal(t, KindBool, typ.Kind)
				})
			}
		})
	}
}

func TestCheckRegressionDynamicKeyOverwrite(t *testing.T) {
	p, err := Compile(`{"x": 1, k: "s"}.x + 1`)
	require.NoError(t, err)
	_, err = p.Run(context.Background(), map[string]any{"k": "x"})
	require.ErrorIs(t, err, ErrEvaluate)

	// The later dynamic key can replace x with a string. Its original
	// numeric field type must not make the addition appear safe.
	_, err = p.Check(map[string]*Type{"k": String()})
	require.ErrorIs(t, err, ErrType)
}

func TestCheckRegressionNullableEnumObjectKey(t *testing.T) {
	p, err := Compile(`{k: 1}`)
	require.NoError(t, err)
	_, err = p.Run(context.Background(), map[string]any{"k": nil})
	require.ErrorIs(t, err, ErrEvaluate)

	// Knowing the sole non-nil key does not make a nullable key safe.
	_, err = p.Check(map[string]*Type{"k": EnumOf("x").OrNil()})
	require.ErrorIs(t, err, ErrType)
}

func TestCheckRegressionSliceUnion(t *testing.T) {
	const source = `slice(if(c, "abc", [1]), 0, 1)`
	env := map[string]*Type{"c": Bool()}

	t.Run("result retains both kinds", func(t *testing.T) {
		p, err := Compile(source, WithFunctions(CollectionFuncs()))
		require.NoError(t, err)
		for _, tc := range []struct {
			condition bool
			want      any
		}{
			{true, "a"},
			{false, []any{int64(1)}},
		} {
			got, err := p.Run(context.Background(), map[string]any{"c": tc.condition})
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		}

		typ, err := p.Check(env)
		require.NoError(t, err)
		require.True(t, typ.IsUnion(), "slice must preserve its string and list result possibilities: "+typ.String())
		kinds := map[Kind]bool{}
		for _, variant := range typ.Variants {
			kinds[variant.Kind] = true
		}
		require.Equal(t, map[Kind]bool{KindString: true, KindList: true}, kinds)
	})

	t.Run("list-only consumer is rejected", func(t *testing.T) {
		p, err := Compile("sum("+source+")", WithFunctions(CollectionFuncs()))
		require.NoError(t, err)
		_, err = p.Run(context.Background(), map[string]any{"c": true})
		require.ErrorIs(t, err, ErrEvaluate)

		_, err = p.Check(env)
		require.ErrorIs(t, err, ErrType)
	})
}
