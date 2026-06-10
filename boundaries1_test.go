package expr

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/deepnoodle-ai/expr/internal/require"
)

// These tests fill coverage gaps and pin down the sharp edges of expr's
// evaluation semantics. When a behavior is ambiguous or surprising, the
// test documents the current behavior with a comment so intentional
// changes are easy to spot in a diff.

// --- Unary operators ---

func TestUnary_NegateFloat(t *testing.T) {
	got, err := evalExpr(t.Context(), "-3.14", nil)
	require.NoError(t, err)
	require.Equal(t, -3.14, got)
}

func TestUnary_NegateUnsupported(t *testing.T) {
	_, err := evalExpr(t.Context(), `-"hi"`, nil)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "cannot negate")
}

// Unary `+` requires a numeric operand. `+42` is a no-op, `+"abc"` errors.
func TestUnary_PlusRequiresNumeric(t *testing.T) {
	got, err := evalExpr(t.Context(), "+42", nil)
	require.NoError(t, err)
	require.Equal(t, int64(42), got)

	got, err = evalExpr(t.Context(), "+3.14", nil)
	require.NoError(t, err)
	require.Equal(t, 3.14, got)

	_, err = evalExpr(t.Context(), `+"abc"`, nil)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "unary +")
}

// `!x` uses truthiness semantics, not strict bool. Documenting the
// behavior — this is consistent with isTruthy across the engine.
func TestUnary_NotUsesTruthiness(t *testing.T) {
	cases := []struct {
		expr string
		want bool
	}{
		{"!0", true},
		{`!""`, true},
		{`!"x"`, false},
		{"!1", false},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := evalExpr(t.Context(), tc.expr, nil)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// --- Division, modulo, and numeric edge cases ---

func TestBinary_DivideByZero(t *testing.T) {
	cases := []string{"1 / 0", "1 % 0", "1.0 / 0.0"}
	for _, expr := range cases {
		t.Run(expr, func(t *testing.T) {
			_, err := evalExpr(t.Context(), expr, nil)
			require.ErrorIs(t, err, ErrEvaluate)
		})
	}
}

func TestBinary_FloatModulo(t *testing.T) {
	got, err := evalExpr(t.Context(), "10.5 % 2", nil)
	require.NoError(t, err)
	require.InDelta(t, 0.5, got, 1e-9)

	got, err = evalExpr(t.Context(), "10 % 3.5", nil)
	require.NoError(t, err)
	require.InDelta(t, 3.0, got, 1e-9)

	_, err = evalExpr(t.Context(), "1.0 % 0.0", nil)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "modulo by zero")
}

func TestBinary_MixedTypesReportError(t *testing.T) {
	_, err := evalExpr(t.Context(), `"a" + 1`, nil)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "not supported")
}

// --- Literals ---

func TestLiteral_HexAndOctal(t *testing.T) {
	got, err := evalExpr(t.Context(), "0xff", nil)
	require.NoError(t, err)
	require.Equal(t, int64(255), got)

	got, err = evalExpr(t.Context(), "0o17", nil)
	require.NoError(t, err)
	require.Equal(t, int64(15), got)
}

func TestLiteral_Char(t *testing.T) {
	// CHAR literals evaluate to their rune value as an int64, matching Go.
	got, err := evalExpr(t.Context(), "'a'", nil)
	require.NoError(t, err)
	require.Equal(t, int64('a'), got)

	got, err = evalExpr(t.Context(), `'\n'`, nil)
	require.NoError(t, err)
	require.Equal(t, int64('\n'), got)
}

func TestLiteral_ImagUnsupported(t *testing.T) {
	// Imaginary literals parse but expr doesn't model complex numbers.
	_, err := evalExpr(t.Context(), "1i", nil)
	require.ErrorIs(t, err, ErrCompile)
}

func TestLiteral_IntOverflow(t *testing.T) {
	_, err := evalExpr(t.Context(), "99999999999999999999", nil)
	require.ErrorIs(t, err, ErrEvaluate)
}

// --- Equality edge cases ---

// Typed nils (nil slice, nil map, nil pointer) compare equal to the nil
// literal — expr checks nilability via reflect so interface-level
// wrapping does not hide the nil.
func TestEquality_TypedNilEqualsNil(t *testing.T) {
	type S struct{ A int }
	env := map[string]any{
		"slice": []any(nil),
		"dict":  map[string]any(nil),
		"ptr":   (*S)(nil),
	}
	for _, expr := range []string{"slice == nil", "dict == nil", "ptr == nil"} {
		got, err := evalExpr(t.Context(), expr, env)
		require.NoError(t, err, expr)
		require.Equal(t, true, got, expr)
	}

	// Non-nil values of nilable types still compare false against nil.
	env = map[string]any{"slice": []any{1}}
	got, err := evalExpr(t.Context(), "slice == nil", env)
	require.NoError(t, err)
	require.Equal(t, false, got)
}

func TestEquality_NilEqualsNil(t *testing.T) {
	got, err := evalExpr(t.Context(), "nil == nil", nil)
	require.NoError(t, err)
	require.Equal(t, true, got)
}

func TestEquality_CrossNumericTypes(t *testing.T) {
	// int via reflection coerces to int64/float64 in looseEqual.
	env := map[string]any{"a": int32(7), "b": int64(7), "c": float64(7)}
	for _, expr := range []string{"a == b", "a == c", "b == c"} {
		got, err := evalExpr(t.Context(), expr, env)
		require.NoError(t, err, expr)
		require.Equal(t, true, got, expr)
	}
}

func TestEquality_DifferentTypesNotEqual(t *testing.T) {
	// Comparable but different types: returns false, no error.
	env := map[string]any{"a": "1", "b": 1}
	got, err := evalExpr(t.Context(), "a == b", env)
	require.NoError(t, err)
	require.Equal(t, false, got)
}

// --- Indexing ---

// String indexing is rune-based: s[i] returns the i-th Unicode code
// point as a one-character string. Matches scripting-language
// expectations and is round-trip safe for non-ASCII text.
func TestIndex_StringByRune(t *testing.T) {
	got, err := evalExpr(t.Context(), `"abc"[1]`, nil)
	require.NoError(t, err)
	require.Equal(t, "b", got)

	got, err = evalExpr(t.Context(), `"héllo"[1]`, nil)
	require.NoError(t, err)
	require.Equal(t, "é", got)

	got, err = evalExpr(t.Context(), `"héllo"[4]`, nil)
	require.NoError(t, err)
	require.Equal(t, "o", got)
}

func TestIndex_StringRuneOutOfRange(t *testing.T) {
	// "héllo" is 5 runes — index 5 is out of range even though the
	// underlying byte length is 6.
	_, err := evalExpr(t.Context(), `"héllo"[5]`, nil)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "out of range")
}

func TestIndex_NegativeAndOutOfRange(t *testing.T) {
	env := map[string]any{"s": []any{1, 2, 3}}
	_, err := evalExpr(t.Context(), "s[-1]", env)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "out of range")

	_, err = evalExpr(t.Context(), "s[10]", env)
	require.ErrorIs(t, err, ErrEvaluate)
}

func TestIndex_TypedMap(t *testing.T) {
	env := map[string]any{"m": map[int]string{1: "one", 2: "two"}}
	got, err := evalExpr(t.Context(), "m[1]", env)
	require.NoError(t, err)
	require.Equal(t, "one", got)

	// Missing key surfaces as an error, not a zero value.
	_, err = evalExpr(t.Context(), "m[99]", env)
	require.ErrorIs(t, err, ErrEvaluate)
}

func TestIndex_MapWithNonStringKeyViaSelector(t *testing.T) {
	env := map[string]any{"m": map[int]string{1: "one"}}
	_, err := evalExpr(t.Context(), `m["x"]`, env)
	require.ErrorIs(t, err, ErrEvaluate)
}

func TestIndex_OnUnsupportedType(t *testing.T) {
	env := map[string]any{"x": 42}
	_, err := evalExpr(t.Context(), "x[0]", env)
	require.ErrorIs(t, err, ErrEvaluate)
}

func TestIndex_NilReceiver(t *testing.T) {
	_, err := evalExpr(t.Context(), "nil[0]", nil)
	require.ErrorIs(t, err, ErrEvaluate)
}

func TestIndex_MapKeyWrongType(t *testing.T) {
	env := map[string]any{"m": map[string]any{"k": 1}}
	_, err := evalExpr(t.Context(), "m[1]", env)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "must be string")
}

// --- Selector edge cases ---

func TestSelector_NilReceiver(t *testing.T) {
	_, err := evalExpr(t.Context(), "nil.x", nil)
	require.ErrorIs(t, err, ErrEvaluate)
}

func TestSelector_StructFieldMissing(t *testing.T) {
	type S struct{ A int }
	env := map[string]any{"s": S{A: 1}}
	_, err := evalExpr(t.Context(), "s.B", env)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "field")
}

func TestSelector_PointerToStruct(t *testing.T) {
	type S struct{ A int }
	env := map[string]any{"s": &S{A: 7}}
	got, err := evalExpr(t.Context(), "s.A", env)
	require.NoError(t, err)
	require.Equal(t, 7, got)
}

func TestSelector_NilPointer(t *testing.T) {
	type S struct{ A int }
	env := map[string]any{"s": (*S)(nil)}
	_, err := evalExpr(t.Context(), "s.A", env)
	require.ErrorIs(t, err, ErrEvaluate)
}

func TestSelector_MapKeyMissing(t *testing.T) {
	env := map[string]any{"m": map[string]any{"a": 1}}
	_, err := evalExpr(t.Context(), "m.b", env)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "not found")
}

func TestSelector_TypedMapWithStringKey(t *testing.T) {
	env := map[string]any{"m": map[string]int{"a": 1, "b": 2}}
	got, err := evalExpr(t.Context(), "m.a", env)
	require.NoError(t, err)
	require.Equal(t, 1, got)
}

// --- Calls ---

// Methods on struct and pointer receivers are callable through selectors.
func TestCall_MethodOnStructSelector(t *testing.T) {
	env := map[string]any{"u": testEnv{Count: 21}}

	got, err := evalExpr(t.Context(), "u.Double()", env)
	require.NoError(t, err)
	require.Equal(t, 42, got)

	got, err = evalExpr(t.Context(), `u.Greet("world")`, env)
	require.NoError(t, err)
	require.Equal(t, "Hello, world", got)
}

func TestCall_MethodOnPointerSelector(t *testing.T) {
	env := map[string]any{"p": &ptrEnv{Value: 7}}
	got, err := evalExpr(t.Context(), "p.Triple()", env)
	require.NoError(t, err)
	require.Equal(t, 21, got)
}

// Functions stored as values inside a map[string]any are callable via the
// same selector path — `state.fns.double(3)` style.
func TestCall_MapStoredFunctionViaSelector(t *testing.T) {
	env := map[string]any{
		"util": map[string]any{
			"double": func(n int64) int64 { return n * 2 },
		},
	}
	got, err := evalExpr(t.Context(), "util.double(21)", env)
	require.NoError(t, err)
	require.Equal(t, int64(42), got)
}

func TestCall_MethodNotFoundOnSelector(t *testing.T) {
	env := map[string]any{"u": testEnv{}}
	_, err := evalExpr(t.Context(), "u.Nope()", env)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "method")
}

func TestCall_MethodOnNilPointerSelector(t *testing.T) {
	env := map[string]any{"p": (*ptrEnv)(nil)}
	_, err := evalExpr(t.Context(), "p.Triple()", env)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "nil pointer")
}

// A struct field that happens to hold a function value is callable via
// selector — covers the Struct fallback inside resolveMethod.
func TestCall_StructFieldFunctionSelector(t *testing.T) {
	type Env struct {
		Double func(int64) int64
	}
	env := map[string]any{
		"x": Env{Double: func(n int64) int64 { return n * 2 }},
	}
	got, err := evalExpr(t.Context(), "x.Double(21)", env)
	require.NoError(t, err)
	require.Equal(t, int64(42), got)
}

// A typed map with string keys stored beneath a selector should be able
// to resolve a function-valued entry.
func TestCall_TypedStringMapFunctionSelector(t *testing.T) {
	env := map[string]any{
		"fns": map[string]func(int64) int64{
			"double": func(n int64) int64 { return n * 2 },
		},
	}
	got, err := evalExpr(t.Context(), "fns.double(21)", env)
	require.NoError(t, err)
	require.Equal(t, int64(42), got)
}

func TestCall_MapStoredFunctionMissing(t *testing.T) {
	env := map[string]any{"m": map[string]any{"x": 1}}
	_, err := evalExpr(t.Context(), "m.nope()", env)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "not found")
}

func TestCall_OnNilSelectorReceiver(t *testing.T) {
	_, err := evalExpr(t.Context(), "nil.f()", nil)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "nil")
}

func TestCall_UnsupportedCallTarget(t *testing.T) {
	// Call target is an index expression, which expr does not support
	// as a callable (only identifiers and selectors are). Rejected at
	// Compile time so the mistake surfaces before any Run.
	_, err := Compile("fns[0]()")
	require.ErrorIs(t, err, ErrCompile)
	require.Contains(t, err.Error(), "call target")
}

func TestCall_WrongArgCount(t *testing.T) {
	opts := []Option{WithFunctions(map[string]any{
		"two": func(a, b int) int { return a + b },
	})}
	_, err := evalExpr(t.Context(), "two(1)", nil, opts...)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "expects 2 args")
}

func TestCall_VariadicMinimumArgs(t *testing.T) {
	opts := []Option{WithFunctions(map[string]any{
		"join": func(sep string, parts ...string) string {
			out := ""
			for i, p := range parts {
				if i > 0 {
					out += sep
				}
				out += p
			}
			return out
		},
	})}
	// Zero variadic args is allowed.
	got, err := evalExpr(t.Context(), `join(",")`, nil, opts...)
	require.NoError(t, err)
	require.Equal(t, "", got)

	got, err = evalExpr(t.Context(), `join(",", "a", "b", "c")`, nil, opts...)
	require.NoError(t, err)
	require.Equal(t, "a,b,c", got)

	// Fewer than the fixed count errors.
	_, err = evalExpr(t.Context(), `join()`, nil, opts...)
	require.Error(t, err)
	require.Contains(t, err.Error(), "at least")
}

func TestCall_UnknownFunction(t *testing.T) {
	_, err := evalExpr(t.Context(), "nope()", nil)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "unknown function")
}

// Registering a function with an unsupported signature fails at
// Compile time — the registration could never be called successfully,
// so surfacing it at load time beats hiding it until the first call.
func TestCall_TooManyReturns(t *testing.T) {
	_, err := Compile("three()", WithFunctions(map[string]any{
		"three": func() (int, int, int) { return 1, 2, 3 },
	}))
	require.ErrorIs(t, err, ErrCompile)
	require.Contains(t, err.Error(), "returns")
}

func TestCall_SecondReturnNotError(t *testing.T) {
	_, err := Compile("bad()", WithFunctions(map[string]any{
		"bad": func() (int, string) { return 1, "x" },
	}))
	require.ErrorIs(t, err, ErrCompile)
	require.Contains(t, err.Error(), "second return must be error")
}

func TestCall_ZeroReturn(t *testing.T) {
	called := false
	opts := []Option{WithFunctions(map[string]any{
		"noop": func() { called = true },
	})}
	got, err := evalExpr(t.Context(), "noop()", nil, opts...)
	require.NoError(t, err)
	require.Nil(t, got)
	require.True(t, called)
}

func TestCall_NonFunction(t *testing.T) {
	// Identifier that resolves to a non-function value via env, then called.
	env := map[string]any{"notfn": 42}
	_, err := evalExpr(t.Context(), "notfn()", env)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "not a function")
}

// --- Argument conversion ---

func TestCall_NumericCoercion(t *testing.T) {
	// expr stores ints as int64; functions declared with int/int32/etc.
	// should still accept them via convertArg's numeric coercion.
	opts := []Option{WithFunctions(map[string]any{
		"i8":  func(n int8) int8 { return n + 1 },
		"f32": func(f float32) float32 { return f * 2 },
	})}

	got, err := evalExpr(t.Context(), "i8(10)", nil, opts...)
	require.NoError(t, err)
	require.Equal(t, int8(11), got)

	got, err = evalExpr(t.Context(), "f32(1.5)", nil, opts...)
	require.NoError(t, err)
	require.Equal(t, float32(3.0), got)
}

func TestCall_NilToNilableParam(t *testing.T) {
	opts := []Option{WithFunctions(map[string]any{
		"takesSlice": func(s []int) int { return len(s) },
		"takesMap":   func(m map[string]int) int { return len(m) },
	})}

	got, err := evalExpr(t.Context(), "takesSlice(nil)", nil, opts...)
	require.NoError(t, err)
	require.Equal(t, 0, got)

	got, err = evalExpr(t.Context(), "takesMap(nil)", nil, opts...)
	require.NoError(t, err)
	require.Equal(t, 0, got)
}

func TestCall_NilToNonNilableParamFails(t *testing.T) {
	opts := []Option{WithFunctions(map[string]any{
		"takesInt": func(n int) int { return n },
	})}
	_, err := evalExpr(t.Context(), "takesInt(nil)", nil, opts...)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "cannot pass nil")
}

func TestCall_WrongArgType(t *testing.T) {
	opts := []Option{WithFunctions(map[string]any{
		"takesMap": func(m map[string]int) int { return len(m) },
	})}
	_, err := evalExpr(t.Context(), `takesMap("not-a-map")`, nil, opts...)
	require.ErrorIs(t, err, ErrEvaluate)
}

func TestCall_InterfaceParam(t *testing.T) {
	// A param of type `any` should accept anything.
	opts := []Option{WithFunctions(map[string]any{
		"dump": func(v any) string {
			if v == nil {
				return "nil"
			}
			return "ok"
		},
	})}
	got, err := evalExpr(t.Context(), "dump(42)", nil, opts...)
	require.NoError(t, err)
	require.Equal(t, "ok", got)

	got, err = evalExpr(t.Context(), "dump(nil)", nil, opts...)
	require.NoError(t, err)
	require.Equal(t, "nil", got)
}

// --- Builtins: fill coverage and document sharp edges ---
// Compile/Eval with no options do not preload the standard builtin set,
// so each test here passes WithBuiltins as a Option.

func TestBuiltin_Len(t *testing.T) {
	opts := []Option{WithBuiltins()}
	cases := []struct {
		expr string
		env  map[string]any
		want any
	}{
		{`len(nil)`, nil, 0},
		{`len("")`, nil, 0},
		{`len(s)`, map[string]any{"s": []int{1, 2, 3}}, 3},
		{`len(m)`, map[string]any{"m": map[string]int{"a": 1}}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := evalExpr(t.Context(), tc.expr, tc.env, opts...)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestBuiltin_LenUnsupported(t *testing.T) {
	opts := []Option{WithBuiltins()}
	_, err := evalExpr(t.Context(), "len(n)", map[string]any{"n": 42}, opts...)
	require.Error(t, err)
}

// `len` counts runes (Unicode code points) for strings, not raw bytes, so
// indexing and length are consistent for non-ASCII strings.
func TestBuiltin_LenCountsRunes(t *testing.T) {
	opts := []Option{WithBuiltins()}
	got, err := evalExpr(t.Context(), `len("héllo")`, nil, opts...)
	require.NoError(t, err)
	require.Equal(t, 5, got)
}

func TestBuiltin_String(t *testing.T) {
	opts := []Option{WithBuiltins()}
	cases := []struct {
		expr string
		want string
	}{
		{`string(nil)`, ""},
		{`string("already")`, "already"},
		{`string(42)`, "42"},
		{`string(3.14)`, "3.14"},
		{`string(true)`, "true"},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := evalExpr(t.Context(), tc.expr, nil, opts...)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestBuiltin_Int(t *testing.T) {
	opts := []Option{WithBuiltins()}
	cases := []struct {
		expr string
		want int64
	}{
		{"int(nil)", 0},
		{"int(42)", 42},
		{"int(3.9)", 3}, // truncation toward zero
		{`int("42")`, 42},
		{`int("  -7 ")`, -7}, // surrounding whitespace is tolerated
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := evalExpr(t.Context(), tc.expr, nil, opts...)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// `int` uses strict base-10 parsing, so trailing garbage and non-integer
// forms error instead of silently producing a truncated value.
func TestBuiltin_IntStringStrict(t *testing.T) {
	opts := []Option{WithBuiltins()}
	for _, expr := range []string{`int("42abc")`, `int("3.14")`, `int("0xff")`} {
		_, err := evalExpr(t.Context(), expr, nil, opts...)
		require.Error(t, err, expr)
		require.Contains(t, err.Error(), "cannot parse", expr)
	}
}

func TestBuiltin_IntStringInvalid(t *testing.T) {
	opts := []Option{WithBuiltins()}
	_, err := evalExpr(t.Context(), `int("nope")`, nil, opts...)
	require.Error(t, err)
}

func TestBuiltin_IntUnsupported(t *testing.T) {
	opts := []Option{WithBuiltins()}
	_, err := evalExpr(t.Context(), "int(x)", map[string]any{"x": []any{1}}, opts...)
	require.Error(t, err)
}

func TestBuiltin_Float(t *testing.T) {
	opts := []Option{WithBuiltins()}
	cases := []struct {
		expr string
		want float64
	}{
		{"float(nil)", 0},
		{"float(1)", 1.0},
		{"float(2.5)", 2.5},
		{`float("3.14")`, 3.14},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := evalExpr(t.Context(), tc.expr, nil, opts...)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// `float` uses strict parsing so trailing garbage fails loudly.
func TestBuiltin_FloatStringStrict(t *testing.T) {
	opts := []Option{WithBuiltins()}
	for _, expr := range []string{`float("3.14xyz")`, `float("")`} {
		_, err := evalExpr(t.Context(), expr, nil, opts...)
		require.Error(t, err, expr)
		require.Contains(t, err.Error(), "cannot parse", expr)
	}
}

func TestBuiltin_FloatStringInvalid(t *testing.T) {
	opts := []Option{WithBuiltins()}
	_, err := evalExpr(t.Context(), `float("nope")`, nil, opts...)
	require.Error(t, err)
}

func TestBuiltin_FloatUnsupported(t *testing.T) {
	opts := []Option{WithBuiltins()}
	_, err := evalExpr(t.Context(), "float(x)", map[string]any{"x": []any{1}}, opts...)
	require.Error(t, err)
}

func TestBuiltin_Bool(t *testing.T) {
	opts := []Option{WithBuiltins()}
	cases := []struct {
		expr string
		want bool
	}{
		{"bool(nil)", false},
		{"bool(0)", false},
		{"bool(1)", true},
		{`bool("")`, false},
		{`bool("x")`, true},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := evalExpr(t.Context(), tc.expr, nil, opts...)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestBuiltin_If(t *testing.T) {
	opts := []Option{WithBuiltins()}
	cases := []struct {
		expr string
		env  map[string]any
		want any
	}{
		{`if(true, "yes", "no")`, nil, "yes"},
		{`if(false, "yes", "no")`, nil, "no"},
		{`if(1, "yes", "no")`, nil, "yes"},
		{`if(0, "yes", "no")`, nil, "no"},
		{`if("", "yes", "no")`, nil, "no"},
		{`if("x", "yes", "no")`, nil, "yes"},
		{`if(nil, "yes", "no")`, nil, "no"},
		{`if(score > 90, "A", "B")`, map[string]any{"score": 95}, "A"},
		{`if(score > 90, "A", "B")`, map[string]any{"score": 80}, "B"},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := evalExpr(t.Context(), tc.expr, tc.env, opts...)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestBuiltin_If_Arity(t *testing.T) {
	opts := []Option{WithBuiltins()}
	for _, expr := range []string{`if(true)`, `if(true, 1)`, `if(true, 1, 2, 3)`} {
		_, err := evalExpr(t.Context(), expr, nil, opts...)
		require.Error(t, err, expr)
		require.ErrorIs(t, err, ErrEvaluate)
	}
}

// `if` is a special form like map/filter/try: always available, no
// WithBuiltins required.
func TestBuiltin_If_NotRegistered(t *testing.T) {
	got, err := evalExpr(t.Context(), `if(true, 1, 2)`, nil)
	require.NoError(t, err)
	require.Equal(t, int64(1), got)
}

// `if` is lazy: only the branch selected by the condition evaluates,
// so the guard idiom protects against errors in the untaken branch.
func TestBuiltin_If_LazyEvaluation(t *testing.T) {
	opts := []Option{WithBuiltins()}
	env := map[string]any{"xs": []any{int64(1), int64(2), int64(3)}}

	got, err := evalExpr(t.Context(), `if(true, xs[0], xs[99])`, env, opts...)
	require.NoError(t, err)
	require.Equal(t, int64(1), got)

	// The canonical division guard.
	got, err = evalExpr(t.Context(), `if(n != 0, 10/n, 0)`, map[string]any{"n": int64(0)}, opts...)
	require.NoError(t, err)
	require.Equal(t, int64(0), got)

	// An error in the *taken* branch still propagates.
	_, err = evalExpr(t.Context(), `if(false, xs[0], xs[99])`, env, opts...)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "out of range")
}

// A user-registered `if` must shadow the builtin, matching the
// shadow rules every other builtin obeys.
func TestBuiltin_If_UserShadows(t *testing.T) {
	opts := []Option{
		WithBuiltins(),
		WithFunctions(map[string]any{
			"if": Func(func(_ context.Context, _ []any) (any, error) {
				return "shadow", nil
			}),
		}),
	}
	got, err := evalExpr(t.Context(), `if(true, 1, 2)`, nil, opts...)
	require.NoError(t, err)
	require.Equal(t, "shadow", got)
}

// An env entry named `if` must shadow the builtin, matching the
// env-wins-over-funcs rule.
func TestBuiltin_If_EnvShadows(t *testing.T) {
	opts := []Option{WithBuiltins()}
	env := map[string]any{
		"if": Func(func(_ context.Context, _ []any) (any, error) {
			return "envshadow", nil
		}),
	}
	got, err := evalExpr(t.Context(), `if(true, 1, 2)`, env, opts...)
	require.NoError(t, err)
	require.Equal(t, "envshadow", got)
}

// `if` must be usable inside higher-order predicates and templates;
// nothing about the keyword rewrite should break composition.
func TestBuiltin_If_InsidePredicate(t *testing.T) {
	opts := []Option{WithBuiltins()}
	env := map[string]any{"xs": []any{int64(1), int64(2), int64(3), int64(4)}}
	got, err := evalExpr(t.Context(), `map(xs, if(it > 2, "big", "small"))`, env, opts...)
	require.NoError(t, err)
	require.Equal(t, []any{"small", "small", "big", "big"}, got)
}

func TestBuiltin_Contains_Nil(t *testing.T) {
	opts := []Option{WithBuiltins()}
	got, err := evalExpr(t.Context(), "contains(nil, 1)", nil, opts...)
	require.NoError(t, err)
	require.Equal(t, false, got)
}

// Documents that `contains` uses looseEqual, so a float needle matches an
// int element and vice versa. Probably a feature, but worth pinning.
func TestBuiltin_Contains_NumericLoose(t *testing.T) {
	opts := []Option{WithBuiltins()}
	env := map[string]any{"xs": []any{1, 2, 3}}
	got, err := evalExpr(t.Context(), "contains(xs, 1.0)", env, opts...)
	require.NoError(t, err)
	require.Equal(t, true, got)
}

func TestBuiltin_Contains_StringNeedleWrongType(t *testing.T) {
	opts := []Option{WithBuiltins()}
	_, err := evalExpr(t.Context(), `contains("hello", 1)`, nil, opts...)
	require.Error(t, err)
}

func TestBuiltin_Contains_UnsupportedHaystack(t *testing.T) {
	opts := []Option{WithBuiltins()}
	env := map[string]any{"x": 42}
	_, err := evalExpr(t.Context(), "contains(x, 1)", env, opts...)
	require.Error(t, err)
}

func TestBuiltin_Contains_MapNonStringKeyErrors(t *testing.T) {
	opts := []Option{WithBuiltins()}
	env := map[string]any{"m": map[int]int{1: 1}}
	_, err := evalExpr(t.Context(), "contains(m, 1)", env, opts...)
	require.Error(t, err)
}

// `has` is narrowed to maps with string keys; checking slice membership
// is `contains`'s job. This keeps the two functions clearly distinct.
func TestBuiltin_Has_MapsOnly(t *testing.T) {
	opts := []Option{WithBuiltins()}
	env := map[string]any{
		"tags": map[string]any{"red": true, "blue": false},
	}
	got, err := evalExpr(t.Context(), `has(tags, "red")`, env, opts...)
	require.NoError(t, err)
	require.Equal(t, true, got)

	got, err = evalExpr(t.Context(), `has(tags, "green")`, env, opts...)
	require.NoError(t, err)
	require.Equal(t, false, got)

	// Slices are not a valid haystack for has.
	env = map[string]any{"xs": []any{"a", "b"}}
	_, err = evalExpr(t.Context(), `has(xs, "a")`, env, opts...)
	require.Error(t, err)
	require.Contains(t, err.Error(), "expected map")

	// Nil is permitted and returns false without erroring.
	got, err = evalExpr(t.Context(), "has(nil, \"k\")", nil, opts...)
	require.NoError(t, err)
	require.Equal(t, false, got)
}

func TestBuiltin_Keys_Nil(t *testing.T) {
	opts := []Option{WithBuiltins()}
	got, err := evalExpr(t.Context(), "keys(nil)", nil, opts...)
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestBuiltin_Keys_NonMapErrors(t *testing.T) {
	opts := []Option{WithBuiltins()}
	_, err := evalExpr(t.Context(), "keys(x)", map[string]any{"x": 42}, opts...)
	require.Error(t, err)
}

func TestBuiltin_Keys_TypedMap(t *testing.T) {
	opts := []Option{WithBuiltins()}
	env := map[string]any{"m": map[string]int{"b": 2, "a": 1}}
	got, err := evalExpr(t.Context(), "keys(m)", env, opts...)
	require.NoError(t, err)
	require.Equal(t, []any{"a", "b"}, got)
}

// --- Program result-type and error coverage ---

func TestProgram_RunSliceResult(t *testing.T) {
	p, err := Compile(`state.items`)
	require.NoError(t, err)
	v, err := p.Run(t.Context(), map[string]any{
		"state": map[string]any{"items": []any{1, 2, 3}},
	})
	require.NoError(t, err)
	require.Equal(t, []any{1, 2, 3}, v)
}

func TestProgram_RunNilResult(t *testing.T) {
	p, err := Compile(`nil`)
	require.NoError(t, err)
	v, err := p.Run(t.Context(), nil)
	require.NoError(t, err)
	require.Nil(t, v)
}

func TestProgram_RunIntResult(t *testing.T) {
	p, err := Compile(`42`)
	require.NoError(t, err)
	v, err := p.Run(t.Context(), nil)
	require.NoError(t, err)
	require.Equal(t, int64(42), v)
}

func TestProgram_RunCanceledCtx(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p, err := Compile(`1`)
	require.NoError(t, err)
	cancel()
	_, err = p.Run(ctx, nil)
	require.ErrorIs(t, err, context.Canceled)
}

func TestProgram_CompileError(t *testing.T) {
	_, err := Compile("1 + + +")
	require.ErrorIs(t, err, ErrCompile)
}

func TestProgram_RunRuntimeError(t *testing.T) {
	p, err := Compile("nope")
	require.NoError(t, err)
	_, err = p.Run(context.Background(), nil)
	require.ErrorIs(t, err, ErrEvaluate)
}

// --- Error bubbling: ensure custom function errors flow through unchanged ---

type customErr struct{ msg string }

func (c customErr) Error() string { return c.msg }

func TestCall_CustomErrorTypePropagates(t *testing.T) {
	sentinel := customErr{msg: "boom"}
	opts := []Option{WithFunctions(map[string]any{
		"fail": func() (int, error) { return 0, sentinel },
	})}
	_, err := evalExpr(t.Context(), "fail()", nil, opts...)
	require.Error(t, err)
	var ce customErr
	require.True(t, errors.As(err, &ce))
	require.Equal(t, "boom", ce.msg)
}

// --- Nil env with struct env expectations ---

func TestLookup_NilPointerEnv(t *testing.T) {
	var p *ptrEnv
	_, err := evalExpr(t.Context(), "Value", p)
	require.Error(t, err)
	require.Contains(t, err.Error(), "undefined")
}

// --- Depth and size limits ---

func TestLimit_DeepSelectorChain(t *testing.T) {
	// Construct a selector chain deeper than MaxEvalDepth. The expression
	// must reject with an ErrEvaluate, not stack-overflow.
	expr := "a"
	for i := 0; i < MaxEvalDepth+5; i++ {
		expr += ".f"
	}
	_, err := evalExpr(t.Context(), expr, map[string]any{"a": map[string]any{"f": nil}})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrEvaluate)
}

func TestLimit_DeepBinaryChain(t *testing.T) {
	// Left-associative: a+a+a+a+... becomes a left-leaning tree with
	// depth proportional to the operand count. Using the identifier
	// `a` keeps the tree alive past compile-time constant folding,
	// so the depth limit is exercised by the runtime walker.
	var b strings.Builder
	b.WriteString("a")
	for i := 0; i < MaxEvalDepth+5; i++ {
		b.WriteString("+a")
	}
	_, err := evalExpr(t.Context(), b.String(), map[string]any{"a": int64(1)})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "nested too deeply")
}

func TestLimit_DeepParens(t *testing.T) {
	// Parenthesis nesting is the cheapest way to build a deep AST, but
	// ParseExpr itself may stack-overflow on truly pathological input.
	// Stay well below that with something our cap still catches.
	// Use a bare identifier so compile-time folding does not collapse
	// the expression into a single literal.
	n := MaxEvalDepth + 5
	expr := strings.Repeat("(", n) + "a" + strings.Repeat(")", n)
	_, err := evalExpr(t.Context(), expr, map[string]any{"a": int64(1)})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrEvaluate)
}

func TestLimit_SourceLength(t *testing.T) {
	// Compile must refuse oversized input before handing it to go/parser.
	src := strings.Repeat("a", MaxSourceLength+1)
	_, err := Compile(src)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrCompile)
	require.Contains(t, err.Error(), "source length")
}

func TestLimit_SourceLengthBoundary(t *testing.T) {
	// Exactly at the limit is allowed (syntax-valid content).
	src := "1" + strings.Repeat(" ", MaxSourceLength-1)
	v, err := Compile(src)
	require.NoError(t, err)
	require.NotNil(t, v)
}

// --- Reflection panic paths ---

type withUnexported struct {
	secret int //nolint:unused // exercised via selector-deny path
	Public int
}

func TestReflect_UnexportedFieldDenied(t *testing.T) {
	// Reading an unexported field via reflect.Value.Interface panics. We
	// check CanInterface and report "not found" instead.
	env := map[string]any{"x": withUnexported{Public: 1}}
	_, err := evalExpr(t.Context(), "x.secret", env)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "not found")
}

func TestReflect_UnexportedFieldDeniedStructEnv(t *testing.T) {
	// Same, but the struct is the whole env. lookupEnv already guards
	// this, but we pin it so neither path regresses.
	env := withUnexported{Public: 1}
	_, err := evalExpr(t.Context(), "secret", env)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrEvaluate)
}

func TestReflect_NilIndexOnTypedMap(t *testing.T) {
	// reflect.ValueOf(nil).Type() panics — guarding nil in indexValue
	// keeps user expressions safe.
	env := map[string]any{"m": map[int]string{1: "one"}}
	_, err := evalExpr(t.Context(), "m[nil]", env)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "nil as map key")
}

func TestReflect_NilFunctionValue(t *testing.T) {
	// A typed nil function value is reflect.Func kind with IsNil() true.
	// Calling it panics, so callFunction has to reject it up front.
	env := map[string]any{"fn": (func() int)(nil)}
	_, err := evalExpr(t.Context(), "fn()", env)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "nil function")
}

func TestReflect_NilFunctionFieldValue(t *testing.T) {
	type Holder struct {
		Fn func(int) int
	}
	env := map[string]any{"h": Holder{Fn: nil}}
	_, err := evalExpr(t.Context(), "h.Fn(1)", env)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "nil function")
}

// --- Numeric conversion audit ---

// Ints should never silently wrap when narrowing to a smaller type.
func TestConvert_IntOverflowRejected(t *testing.T) {
	opts := []Option{WithFunctions(map[string]any{
		"i8":  func(n int8) int8 { return n },
		"u8":  func(n uint8) uint8 { return n },
		"u16": func(n uint16) uint16 { return n },
		"u32": func(n uint32) uint32 { return n },
		"i32": func(n int32) int32 { return n },
	})}
	cases := []struct {
		expr string
	}{
		{"i8(128)"},
		{"i8(-129)"},
		{"u8(256)"},
		{"u8(-1)"},
		{"u16(65536)"},
		{"u32(-1)"},
		{"i32(2147483648)"},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			_, err := evalExpr(t.Context(), tc.expr, nil, opts...)
			require.Error(t, err, "%s should reject overflow", tc.expr)
			require.ErrorIs(t, err, ErrEvaluate)
		})
	}
}

func TestConvert_IntInRangeAllowed(t *testing.T) {
	opts := []Option{WithFunctions(map[string]any{
		"i8":  func(n int8) int8 { return n },
		"u8":  func(n uint8) uint8 { return n },
		"i32": func(n int32) int32 { return n },
	})}
	cases := []struct {
		expr string
		want any
	}{
		{"i8(127)", int8(127)},
		{"i8(-128)", int8(-128)},
		{"u8(0)", uint8(0)},
		{"u8(255)", uint8(255)},
		{"i32(2147483647)", int32(math.MaxInt32)},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := evalExpr(t.Context(), tc.expr, nil, opts...)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestConvert_FloatToIntRange(t *testing.T) {
	opts := []Option{WithFunctions(map[string]any{
		"i8":  func(n int8) int8 { return n },
		"i64": func(n int64) int64 { return n },
	})}
	// Truncation toward zero is intentional.
	got, err := evalExpr(t.Context(), "i8(3.9)", nil, opts...)
	require.NoError(t, err)
	require.Equal(t, int8(3), got)

	got, err = evalExpr(t.Context(), "i8(-3.9)", nil, opts...)
	require.NoError(t, err)
	require.Equal(t, int8(-3), got)

	// Out-of-range float → target int kind.
	_, err = evalExpr(t.Context(), "i8(1000.0)", nil, opts...)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrEvaluate)
}

func TestConvert_NaNAndInfRejected(t *testing.T) {
	opts := []Option{WithFunctions(map[string]any{
		"i32":     func(n int32) int32 { return n },
		"makeNaN": func() float64 { return math.NaN() },
		"makeInf": func() float64 { return math.Inf(1) },
	})}
	_, err := evalExpr(t.Context(), "i32(makeNaN())", nil, opts...)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrEvaluate)

	_, err = evalExpr(t.Context(), "i32(makeInf())", nil, opts...)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrEvaluate)
}

func TestConvert_UintSource(t *testing.T) {
	// env may supply uint values; they must narrow with the same checks.
	opts := []Option{WithFunctions(map[string]any{
		"i8": func(n int8) int8 { return n },
	})}
	_, err := evalExpr(t.Context(), "i8(v)", map[string]any{"v": uint64(300)}, opts...)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrEvaluate)

	got, err := evalExpr(t.Context(), "i8(v)", map[string]any{"v": uint64(100)}, opts...)
	require.NoError(t, err)
	require.Equal(t, int8(100), got)
}

// --- Literal edge cases ---

func TestLiteral_LargeInt(t *testing.T) {
	// strconv.ParseInt rejects; we report via ErrEvaluate, not panic.
	_, err := evalExpr(t.Context(), "9999999999999999999999999999", nil)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrEvaluate)
}

func TestLiteral_HugeStringLiteral(t *testing.T) {
	// A string literal near the source limit must round-trip.
	payload := strings.Repeat("a", MaxSourceLength-10)
	src := `"` + payload + `"`
	got, err := evalExpr(t.Context(), src, nil)
	require.NoError(t, err)
	require.Equal(t, payload, got)
}

func TestLiteral_InvalidEscape(t *testing.T) {
	// Parser rejects; wraps ErrCompile.
	_, err := Compile(`"\xZZ"`)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrCompile)
}

func TestLiteral_EmptyCharLiteral(t *testing.T) {
	// Parser usually rejects '' — but the evaluator's len(runes) != 1
	// branch is still worth covering for completeness via a multi-byte
	// escaped input.
	_, err := Compile("''")
	require.Error(t, err)
}

// --- Reserved call-target shapes ---

func TestSyntax_UnsupportedNodesAllReject(t *testing.T) {
	cases := []string{
		"x[1:3]",   // SliceExpr
		"x[1:3:5]", // SliceExpr full
		"x.(int)",  // TypeAssertExpr
		"<-ch",     // ChanExpr (parses as UnaryExpr with ARROW)
		"*p",       // unary * (unsupported token)
		"&x",       // address-of
		"1 & 2",    // bitwise AND
		"1 | 2",    // bitwise OR
		"1 ^ 2",    // bitwise XOR
		"1 << 2",   // shift left
		"1 >> 2",   // shift right
		"1 &^ 2",   // AND NOT
	}
	for _, expr := range cases {
		t.Run(expr, func(t *testing.T) {
			_, err := evalExpr(t.Context(), expr, map[string]any{"x": []any{1, 2, 3}, "p": 1, "ch": 1})
			require.Error(t, err, "%s should reject", expr)
		})
	}
}

// --- Maps with interface keys ---

func TestMap_InterfaceKeys(t *testing.T) {
	// A map[any]any with string key works through the reflect fallback.
	env := map[string]any{
		"m": map[any]any{"a": 1, "b": 2},
	}
	got, err := evalExpr(t.Context(), `m["a"]`, env)
	require.NoError(t, err)
	require.Equal(t, 1, got)
}

// --- User code panics bubble up naturally ---

func TestUserCode_PanicIsNotOurBug(t *testing.T) {
	// The engine does not recover panics from user code — documented in
	// the spec. Pin the current behavior so changes are deliberate.
	opts := []Option{WithFunctions(map[string]any{
		"boom": func() int { panic("nope") },
	})}
	require.Panics(t, func() {
		_, _ = evalExpr(t.Context(), "boom()", nil, opts...)
	})
}

// --- Cross-type equality with uncomparable operands ---

func TestEquality_UncomparableReturnsError(t *testing.T) {
	// Slice == slice is not comparable under looseEqual. Matching Go's
	// runtime behavior means the binary path falls through to the error.
	env := map[string]any{
		"a": []int{1, 2, 3},
		"b": []int{1, 2, 3},
	}
	_, err := evalExpr(t.Context(), "a == b", env)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrEvaluate)
}

// --- Error bubbling from user error types via errors.As ---

type wrappedErr struct{ inner error }

func (w wrappedErr) Error() string { return w.inner.Error() }
func (w wrappedErr) Unwrap() error { return w.inner }

func TestUserError_UnwrapChainPreserved(t *testing.T) {
	sentinel := errors.New("root")
	opts := []Option{WithFunctions(map[string]any{
		"fail": func() (int, error) { return 0, wrappedErr{inner: sentinel} },
	})}
	_, err := evalExpr(t.Context(), "fail()", nil, opts...)
	require.Error(t, err)
	require.ErrorIs(t, err, sentinel)
}

type edgeCaseRoot struct {
	Next map[string]any
}

type edgeCaseStringKey string
type edgeCaseInt int
type edgeCaseString string

func TestEdgeCase_SelectorFastPathHonorsMaxEvalDepth(t *testing.T) {
	expr := "Next"
	for i := 0; i < MaxEvalDepth; i++ {
		expr += ".next"
	}

	env := edgeCaseRoot{Next: map[string]any{}}
	cursor := env.Next
	for i := 0; i < MaxEvalDepth; i++ {
		child := map[string]any{}
		cursor["next"] = child
		cursor = child
	}

	_, err := evalExpr(t.Context(), expr, env)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "nested too deeply")
}

func TestEdgeCase_NamedStringMapKeysDoNotPanic(t *testing.T) {
	env := map[string]any{
		"m": map[edgeCaseStringKey]any{
			"present": int64(42),
		},
	}

	got, err := evalExpr(t.Context(), "m.present", env)
	require.NoError(t, err)
	require.Equal(t, int64(42), got)

	got, err = evalExpr(t.Context(), `m["present"]`, env)
	require.NoError(t, err)
	require.Equal(t, int64(42), got)
}

func TestEdgeCase_NamedStringMapKeysForEnvAndBuiltins(t *testing.T) {
	opts := []Option{WithBuiltins()}
	env := map[edgeCaseStringKey]any{
		"present": int64(42),
		"nested":  map[edgeCaseStringKey]int{"k": 1},
	}

	got, err := evalExpr(t.Context(), "present", env)
	require.NoError(t, err)
	require.Equal(t, int64(42), got)

	got, err = evalExpr(t.Context(), `has(nested, "k")`, env, opts...)
	require.NoError(t, err)
	require.Equal(t, true, got)

	got, err = evalExpr(t.Context(), `contains(nested, "k")`, env, opts...)
	require.NoError(t, err)
	require.Equal(t, true, got)
}

func TestEdgeCase_MapIndexNumericConversionIsRangeChecked(t *testing.T) {
	env := map[string]any{
		"m": map[int8]string{1: "one"},
	}

	_, err := evalExpr(t.Context(), "m[257]", env)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrEvaluate)

	got, err := evalExpr(t.Context(), "m[1]", env)
	require.NoError(t, err)
	require.Equal(t, "one", got)
}

func TestEdgeCase_IntBuiltinRejectsNonFiniteAndOutOfRangeFloat(t *testing.T) {
	opts := []Option{WithBuiltins(), WithFunctions(map[string]any{
		"nan": func() float64 { return math.NaN() },
		"inf": func() float64 { return math.Inf(1) },
	})}

	for _, expr := range []string{
		"int(nan())",
		"int(inf())",
		"int(9223372036854775808.0)",
	} {
		t.Run(strings.TrimPrefix(expr, "int("), func(t *testing.T) {
			_, err := evalExpr(t.Context(), expr, nil, opts...)
			require.Error(t, err)
			require.ErrorIs(t, err, ErrEvaluate)
		})
	}
}

func TestEdgeCase_TruthinessHonorsTypedNilAndNamedScalars(t *testing.T) {
	type node struct{ Value int }

	opts := []Option{WithBuiltins()}
	env := map[string]any{
		"ptr":        (*node)(nil),
		"zeroInt":    edgeCaseInt(0),
		"nonzeroInt": edgeCaseInt(2),
		"emptyStr":   edgeCaseString(""),
		"falseStr":   edgeCaseString("false"),
		"numStr":     edgeCaseString("41"),
		"needle":     edgeCaseString("al"),
	}

	cases := []struct {
		expr string
		want any
	}{
		{"bool(ptr)", false},
		{"!ptr", true},
		{"bool(zeroInt)", false},
		{"bool(nonzeroInt)", true},
		{"zeroInt + 2", int64(2)},
		{`bool(emptyStr)`, false},
		// bool() does not inspect string content; "false" is a non-empty
		// string and is therefore truthy.
		{`bool(falseStr)`, true},
		{`int(numStr) + 1`, int64(42)},
		{`upper(falseStr)`, "FALSE"},
		{`contains(falseStr, needle)`, true},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := evalExpr(t.Context(), tc.expr, env, opts...)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestEdgeCase_CyclicValuesFormatWithoutStackOverflow(t *testing.T) {
	cycle := map[string]any{}
	cycle["self"] = cycle
	env := map[string]any{"cycle": cycle}
	opts := []Option{WithBuiltins()}

	tmpl, err := NewTemplate("value=${cycle}", opts...)
	require.NoError(t, err)
	out, err := tmpl.Render(t.Context(), env)
	require.NoError(t, err)
	require.Contains(t, out, "<cycle>")

	got, err := evalExpr(t.Context(), "string(cycle)", env, opts...)
	require.NoError(t, err)
	require.Contains(t, got, "<cycle>")

	got, err = evalExpr(t.Context(), `sprintf("%v", cycle)`, env, opts...)
	require.NoError(t, err)
	require.Contains(t, got, "<cycle>")
}
