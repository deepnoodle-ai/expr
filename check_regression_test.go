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

// Cases the soundness harness (check_soundness_test.go) found. Each pairs
// a schema with a value Run accepts and the type Check must now infer
// for it, or with a value on which Run fails and the problem Check must
// now report.
func TestCheckRegressionSoundness(t *testing.T) {
	funcs := []Option{WithBuiltins(), WithFunctions(StringFuncs()), WithFunctions(MathFuncs()), WithFunctions(CollectionFuncs())}
	for _, tc := range []struct {
		name   string
		src    string
		schema map[string]*Type
		value  map[string]any
		// want is the inferred type when Run succeeds, or a substring of
		// the Check problem when Run fails.
		want string
	}{
		{"computed key types unlisted fields", `{"a": 2, k: c}?.b`,
			map[string]*Type{"k": String(), "c": Bool()}, map[string]any{"k": "b", "c": true}, "bool?"},
		{"computed key field may be absent", `{"a": 2, k: c}.b`,
			map[string]*Type{"k": String(), "c": Bool()}, map[string]any{"k": "a", "c": true}, `field "b" may be absent`},
		{"enum key adds absent fields", `{k: 1}?.b`,
			map[string]*Type{"k": EnumOf("a", "b")}, map[string]any{"k": "b"}, "1?"},
		{"enum key field may be absent", `{k: 1}.b`,
			map[string]*Type{"k": EnumOf("a", "b")}, map[string]any{"k": "a"}, `field "b" may be absent`},
		{"literal key after computed key is present", `{k: 1, "b": "s"}.b`,
			map[string]*Type{"k": EnumOf("a", "b")}, map[string]any{"k": "b"}, `"s"`},
		{"computed key indexed by any string", `{"a": c, k: y}[trim(k)].type`,
			map[string]*Type{"k": String(), "c": Bool(), "y": String()}, map[string]any{"k": "a", "c": true, "y": ""}, "the key may be absent"},
		{"first of nil", `first(x).a`,
			map[string]*Type{"x": Null()}, map[string]any{"x": nil}, "the value is always nil"},
		{"find over nil", `-find(x, it)`,
			map[string]*Type{"x": Null()}, map[string]any{"x": nil}, "nil is not a number"},
		{"find over a nil form result", `find(map(x, it), true).type`,
			map[string]*Type{"x": Null()}, map[string]any{"x": nil}, "the value is always nil"},
		{"last of an empty list", `round(last(filter(nil, it)))`,
			nil, nil, "argument 1 is always nil"},
		{"sort of nil", `-sort(nil)?[0]`,
			nil, nil, "nil is not a number"},
		{"try of a nil index", `-try(x[nil], y)`,
			map[string]*Type{"x": ListOf(Number()), "y": String()}, map[string]any{"x": []any{1}, "y": "s"}, "is not a number"},
		{"try of a nil index types as its default", `try(x[nil], y)`,
			map[string]*Type{"x": ListOf(Number()), "y": String()}, map[string]any{"x": []any{1}, "y": "s"}, "string"},
		{"?[nil] of a nil receiver is nil", `try(x?[nil], 1)`,
			map[string]*Type{"x": ListOf(Number()).OrNil()}, map[string]any{"x": nil}, "1?"},
		{"nil index of a number", `2.5?[z]`,
			map[string]*Type{"z": Null(), "k": String()}, map[string]any{"z": nil, "k": "a"}, "cannot index 2.5"},
		{"flatMap of a non-list body", `first(flatMap(x, 0)).b`,
			map[string]*Type{"x": ListOf(Number())}, map[string]any{"x": []any{1}}, `cannot read field "b"`},
		{"flatMap of a union body", `sum(flatMap(z, it))`,
			map[string]*Type{"z": ListOf(Join(Number(), ObjectOf(map[string]*Type{"a": String()})))},
			map[string]any{"z": []any{1, map[string]any{"a": ""}}}, "elements that may be"},
		{"index of an empty object", `lower(o?[k])`,
			map[string]*Type{"o": ObjectOf(map[string]*Type{}), "k": String()}, map[string]any{"o": map[string]any{}, "k": "a"}, "argument 1 is always nil"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Compile(tc.src, funcs...)
			require.NoError(t, err)
			got, runErr := p.Run(context.Background(), tc.value)
			typ, checkErr := p.Check(tc.schema)
			if runErr != nil {
				require.ErrorIs(t, checkErr, ErrType, "Run failed, so Check must report it")
				require.Contains(t, checkErr.Error(), tc.want)
				return
			}
			require.NoError(t, checkErr)
			require.Equal(t, tc.want, typ.String())
			_ = got
		})
	}
}
