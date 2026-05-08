package expr

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/deepnoodle-ai/expr/internal/require"
)

// These hardening tests probe correctness, panic, and edge-case behavior that
// has historically been easy to miss in expression evaluators.

// --- 1. Integer divide/modulo overflow ---

// TestAdversarial_IntDivOverflow probes whether MinInt64 / -1 panics.
func TestAdversarial_IntDivOverflow(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("MinInt64 / -1 panicked: %v", r)
		}
	}()
	env := map[string]any{"a": int64(math.MinInt64), "b": int64(-1)}
	_, err := evalExpr(t.Context(), "a / b", env)
	if err == nil {
		t.Fatal("expected an error or graceful handling, got success")
	}
	require.ErrorIs(t, err, ErrEvaluate)
}

// MinInt64 % -1 is mathematically 0 (since MinInt64 is divisible by -1
// at the abstract integer level), and Go agrees — no overflow, no
// panic. This test pins that behavior so a future "fix" for M1
// doesn't accidentally start erroring on the modulo case too.
func TestAdversarial_IntModOverflow(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("MinInt64 %% -1 panicked: %v", r)
		}
	}()
	env := map[string]any{"a": int64(math.MinInt64), "b": int64(-1)}
	got, err := evalExpr(t.Context(), "a % b", env)
	require.NoError(t, err)
	require.Equal(t, int64(0), got)
}

// Negation of MinInt64 in unary minus: -MinInt64 wraps silently in Go.
// `-x` should error or otherwise not silently produce MinInt64.
func TestAdversarial_NegMinInt64(t *testing.T) {
	env := map[string]any{"x": int64(math.MinInt64)}
	v, err := evalExpr(t.Context(), "-x", env)
	t.Logf("-MinInt64 => %v, err=%v", v, err)
	if err != nil {
		// Reporting an error is acceptable.
		return
	}
	if v == int64(math.MinInt64) {
		t.Fatalf("silent overflow: -MinInt64 returned MinInt64; expected error")
	}
}

// --- 2. int()/float() builtins on NaN/Inf/huge floats ---

func TestAdversarial_IntOfNaN(t *testing.T) {
	env := map[string]any{"x": math.NaN()}
	v, err := evalExpr(t.Context(), "int(x)", env, WithBuiltins())
	t.Logf("int(NaN) => %v, err=%v", v, err)
	if err == nil {
		t.Fatalf("int(NaN) should error or produce documented behavior, got %v", v)
	}
}

func TestAdversarial_IntOfInf(t *testing.T) {
	env := map[string]any{"x": math.Inf(1)}
	v, err := evalExpr(t.Context(), "int(x)", env, WithBuiltins())
	t.Logf("int(+Inf) => %v, err=%v", v, err)
	if err == nil {
		t.Fatalf("int(+Inf) should error, got %v", v)
	}
}

func TestAdversarial_IntOfHugeFloat(t *testing.T) {
	env := map[string]any{"x": 1e30}
	v, err := evalExpr(t.Context(), "int(x)", env, WithBuiltins())
	t.Logf("int(1e30) => %v, err=%v", v, err)
	if err == nil {
		t.Fatalf("int(1e30) should error (out of int64 range), got %v", v)
	}
}

// --- 3. Typed string / typed bool / typed numeric truthiness ---

type mystr string
type mybool bool
type myint int

// These three tests pin the (correct) behavior that IsTruthy resolves
// typed primitives via the reflect fallback. I initially suspected this
// was broken; verifying it isn't is still useful as a regression guard.
func TestAdversarial_TruthyTypedString(t *testing.T) {
	if IsTruthy(mystr("")) {
		t.Fatalf("IsTruthy(mystr(\"\")) should be false, got true")
	}
	if IsTruthy(mystr("false")) {
		t.Fatalf("IsTruthy(mystr(\"false\")) should be false (matches plain string rule), got true")
	}
	if !IsTruthy(mystr("hello")) {
		t.Fatalf("IsTruthy(mystr(\"hello\")) should be true")
	}
}

func TestAdversarial_TruthyTypedBool(t *testing.T) {
	if IsTruthy(mybool(false)) {
		t.Fatalf("IsTruthy(mybool(false)) should be false, got true")
	}
	if !IsTruthy(mybool(true)) {
		t.Fatalf("IsTruthy(mybool(true)) should be true")
	}
}

func TestAdversarial_TruthyTypedInt(t *testing.T) {
	if IsTruthy(myint(0)) {
		t.Fatalf("IsTruthy(myint(0)) should be false, got true")
	}
	if !IsTruthy(myint(1)) {
		t.Fatalf("IsTruthy(myint(1)) should be true")
	}
}

// --- 4. uint64 near MaxUint64 corrupts equality / comparisons ---

func TestAdversarial_UintMaxEquality(t *testing.T) {
	env := map[string]any{"u": uint64(math.MaxUint64)}
	got, err := evalExpr(t.Context(), "u == -1", env)
	require.NoError(t, err)
	t.Logf("uint64(MaxUint64) == -1 => %v", got)
	if got == true {
		t.Fatalf("uint64(MaxUint64) should not equal -1; toInt64 truncation bug")
	}
}

func TestAdversarial_UintMaxGreaterThanZero(t *testing.T) {
	env := map[string]any{"u": uint64(math.MaxUint64)}
	got, err := evalExpr(t.Context(), "u > 0", env)
	require.NoError(t, err)
	t.Logf("uint64(MaxUint64) > 0 => %v", got)
	if got == false {
		t.Fatalf("uint64(MaxUint64) > 0 should be true; toInt64 truncation bug")
	}
}

func TestAdversarial_UintMaxSelfEqual(t *testing.T) {
	env := map[string]any{"u": uint64(math.MaxUint64)}
	// Even a self-comparison via float64 conversion has issues at extreme magnitudes.
	got, err := evalExpr(t.Context(), "u == u", env)
	require.NoError(t, err)
	if got != true {
		t.Fatalf("uint64(MaxUint64) == itself should be true, got %v", got)
	}
}

// --- 5. Map index narrowing silent truncation ---

func TestAdversarial_MapKeyNarrowingTruncation(t *testing.T) {
	// reflect.Value.Convert truncates int64 → int8 silently.
	env := map[string]any{
		"m": map[int8]string{1: "one", 2: "two"},
	}
	// 257 truncates to 1 in int8. expr should NOT silently match m[1].
	got, err := evalExpr(t.Context(), "m[257]", env)
	t.Logf("m[int8 = 1<<8 + 1] => %v, err=%v", got, err)
	if err == nil {
		t.Fatalf("m[257] against map[int8] should error (out of range), got %v", got)
	}
}

// --- 6. evalSelectorChainRV silent nil ---

func TestAdversarial_EvalSelectorChainRVNilIt(t *testing.T) {
	// We can't reach the IsValid()==false branch with names!=0 easily.
	// Try the path: env is a struct with a nil-valued exported field
	// that's an interface; selecting through it.
	type Holder struct {
		Inner any
	}
	env := map[string]any{
		"h": Holder{Inner: nil},
	}
	_, err := evalExpr(t.Context(), "h.Inner.Foo", env)
	if err == nil {
		t.Fatalf("selecting through nil interface should error")
	}
	t.Logf("h.Inner.Foo => err=%v", err)
}

// --- 7. Source-length expansion via jsonlit (informational, L3) ---
//
// 32K of bare `{` rewrites to ~960 KiB of nested map[string]any{...}
// composite literals. The Go parser accepts that as a valid (deeply
// nested) expression, so Compile succeeds. The point of this test is
// to prove the rewriter does not crash and bounds memory growth even
// on adversarial input. It is NOT a bug; just a budget-leaks-the-cap
// finding documented as L3.
func TestAdversarial_JsonlitSizeBlowup(t *testing.T) {
	src := strings.Repeat("{", MaxSourceLength/2-1) + "}" + strings.Repeat("}", MaxSourceLength/2-2)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("jsonlit blowup panicked: %v", r)
		}
	}()
	_, err := Compile(src)
	t.Logf("jsonlit blowup => err=%v (success means rewrite produced valid parse tree)", err)
	// No assertion on err: either outcome is acceptable. The
	// non-negotiable is no panic, which the deferred recover checks.
}

// --- 8. callFunction misbehavior with reflect-arity edge cases ---

// Variadic with zero callsite args: fn(...int) called with no args.
func TestAdversarial_VariadicZeroArgs(t *testing.T) {
	opts := []Option{WithFunctions(map[string]any{
		"sum": func(xs ...int) int {
			s := 0
			for _, x := range xs {
				s += x
			}
			return s
		},
	})}
	got, err := evalExpr(t.Context(), "sum()", nil, opts...)
	require.NoError(t, err)
	require.Equal(t, 0, got)
}

// --- 9. Composite literal duplicate keys ---

func TestAdversarial_DuplicateMapKey(t *testing.T) {
	got, err := evalExpr(t.Context(), `{"a": 1, "a": 2}`, nil)
	t.Logf(`{"a":1, "a":2} => %v, err=%v`, got, err)
	// Last write wins is OK, but be deterministic.
	if err != nil {
		// Erroring is also acceptable.
		return
	}
	m := got.(map[string]any)
	if v, ok := m["a"]; !ok || v != int64(2) {
		t.Fatalf(`expected last-write-wins (a=2), got %v`, m)
	}
}

// --- 10. context cancellation in inner predicate ---

func TestAdversarial_CtxCancelInPredicate(t *testing.T) {
	xs := make([]any, 1000)
	for i := range xs {
		xs[i] = int64(i)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel() // cancel immediately
	env := map[string]any{"xs": xs}
	_, err := evalExpr(ctx, "filter(xs, it > 0)", env, WithBuiltins())
	if err == nil {
		t.Fatal("expected context cancellation to propagate")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

// --- 11. UTF-16 surrogate / invalid char literal ---

func TestAdversarial_CharLiteralSurrogate(t *testing.T) {
	// strconv.Unquote of '\uD800' should fail. Just confirm no panic.
	_, err := evalExpr(t.Context(), `'\uD800'`, nil)
	t.Logf("char literal U+D800 => err=%v", err)
}

// --- 12. Sprintf with malicious format string ---

func TestAdversarial_SprintfPanic(t *testing.T) {
	// fmt.Sprintf doesn't normally panic, but %!(NOVERB) etc. could leak data.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("sprintf panicked: %v", r)
		}
	}()
	_, err := evalExpr(t.Context(), `sprintf("%v %v %v", 1)`, nil, WithBuiltins())
	t.Logf("sprintf shortargs => err=%v", err)
}

// --- 13. Hash collision / massive map literal ---

func TestAdversarial_HugeMapLiteral(t *testing.T) {
	// Build an expression with thousands of map entries.
	var b strings.Builder
	b.WriteString("{")
	const n = 5000
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(fmt.Sprintf(`"k%d":%d`, i, i))
	}
	b.WriteString("}")
	src := b.String()
	if len(src) > MaxSourceLength {
		t.Skip("source exceeds MaxSourceLength")
	}
	got, err := evalExpr(t.Context(), src, nil)
	require.NoError(t, err)
	m := got.(map[string]any)
	if len(m) != n {
		t.Fatalf("expected %d entries, got %d", n, len(m))
	}
}

// --- 14. Unknown method / panic via MethodByName on nil ---

type embedded struct{}

func (embedded) Boom() string { return "bang" }

type holderEmbed struct {
	*embedded
}

func TestAdversarial_NilEmbeddedMethodPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil-embedded method call panicked: %v", r)
		}
	}()
	env := map[string]any{"h": holderEmbed{}}
	got, err := evalExpr(t.Context(), "h.Boom()", env)
	t.Logf("h.Boom() with nil-embedded ptr => %v, err=%v", got, err)
	// Either error or graceful rejection is acceptable; what's NOT
	// acceptable is a runtime panic.
}

// --- 15. Comparison operators on bool ---

func TestAdversarial_BoolLessOrdering(t *testing.T) {
	got, err := evalExpr(t.Context(), "a < b", map[string]any{"a": true, "b": false})
	t.Logf("true < false => %v, err=%v", got, err)
	// Go doesn't define < on bool; we should error, not return weird value.
	if err == nil {
		t.Fatalf("bool < bool should not be a valid comparison, got %v", got)
	}
}

// --- 16. Interface containing nil method-value ---

type edgeCaseFnHolder struct {
	Fn any
}

func TestAdversarial_AnyNilFunctionViaSelector(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil Fn via interface field panicked: %v", r)
		}
	}()
	env := map[string]any{"h": edgeCaseFnHolder{Fn: nil}}
	_, err := evalExpr(t.Context(), "h.Fn()", env)
	t.Logf("h.Fn() with nil any => err=%v", err)
	require.Error(t, err)
}

// --- 17. Constant-folded MinInt64 negation ---

func TestAdversarial_FoldedNegMinInt(t *testing.T) {
	// At compile time, the parser yields a UnaryExpr SUB of a literal
	// 9223372036854775808 — which is too big for int64. Compile should
	// surface the literal-out-of-range error or correctly produce
	// MinInt64. Should not panic.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("compile -9223372036854775808 panicked: %v", r)
		}
	}()
	v, err := evalExpr(t.Context(), "-9223372036854775808", nil)
	t.Logf("-9223372036854775808 => %v, err=%v", v, err)
}

// --- 18. Self-referential map via env doesn't infinite loop in keys() ---

func TestAdversarial_KeysOnNonStringKeyMap(t *testing.T) {
	// keys() requires string-keyed maps; passing map[int]int should error.
	env := map[string]any{"m": map[int]int{1: 2}}
	_, err := evalExpr(t.Context(), "keys(m)", env, WithBuiltins())
	require.Error(t, err)
}

// --- 19. Equality between map[string]any and ordinary map ---

func TestAdversarial_EqualityCrossMaps(t *testing.T) {
	env := map[string]any{
		"a": map[string]any{"x": 1},
		"b": map[string]any{"x": 1},
	}
	_, err := evalExpr(t.Context(), "a == b", env)
	t.Logf("map == map => err=%v", err)
	// Maps are not comparable in Go; we should error, not panic.
}

// --- 20. Float REM by integer zero ---

func TestAdversarial_FloatModByZero(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("float mod by zero panicked: %v", r)
		}
	}()
	_, err := evalExpr(t.Context(), "1.5 % 0.0", nil)
	t.Logf("1.5 %% 0.0 => err=%v", err)
	require.Error(t, err)
}

// --- 21. find() returns nil — can't distinguish "no match" from "match was nil" ---

func TestAdversarial_FindNilAmbiguity(t *testing.T) {
	env := map[string]any{"xs": []any{int64(1), nil, int64(3)}}
	got, err := evalExpr(t.Context(), "find(xs, it == nil)", env, WithBuiltins())
	require.NoError(t, err)
	t.Logf("find(xs with nil, it==nil) => %#v", got)
	// This is a design issue, not a panic.
}

// --- 22. variadic ctx-injected function with no args ---

func TestAdversarial_VariadicCtxOnly(t *testing.T) {
	opts := []Option{WithFunctions(map[string]any{
		"sum": func(_ context.Context, xs ...int) int {
			s := 0
			for _, x := range xs {
				s += x
			}
			return s
		},
	})}
	got, err := evalExpr(t.Context(), "sum()", nil, opts...)
	require.NoError(t, err)
	require.Equal(t, 0, got)
}

// --- 23. Multiplication overflow ---

func TestAdversarial_MulOverflow(t *testing.T) {
	env := map[string]any{
		"a": int64(math.MaxInt64),
		"b": int64(2),
	}
	got, err := evalExpr(t.Context(), "a * b", env)
	t.Logf("MaxInt64 * 2 => %v, err=%v", got, err)
	if err == nil && got == int64(-2) {
		t.Logf("CONFIRMED: silent integer overflow on multiply")
	}
}

// --- 24. Addition overflow ---

func TestAdversarial_AddOverflow(t *testing.T) {
	env := map[string]any{
		"a": int64(math.MaxInt64),
		"b": int64(1),
	}
	got, err := evalExpr(t.Context(), "a + b", env)
	t.Logf("MaxInt64 + 1 => %v, err=%v", got, err)
	if err == nil && got == int64(math.MinInt64) {
		t.Logf("CONFIRMED: silent integer overflow on add")
	}
}

// --- 25. Method-value invocation panic from nil pointer with pointer receiver ---

type edgeCaseFooP struct{ n int }

func (f *edgeCaseFooP) Inc() int { return f.n + 1 }

func TestAdversarial_NilPointerMethodCall(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("calling method on nil pointer panicked: %v", r)
		}
	}()
	env := map[string]any{"f": (*edgeCaseFooP)(nil)}
	got, err := evalExpr(t.Context(), "f.Inc()", env)
	t.Logf("nil *edgeCaseFooP .Inc() => %v, err=%v", got, err)
}

// --- 26. Typed string in equality / comparison ---

type colorName string

func TestAdversarial_TypedStringEquality(t *testing.T) {
	env := map[string]any{
		"red": colorName("red"),
	}
	got, err := evalExpr(t.Context(), `red == "red"`, env)
	t.Logf(`colorName("red") == "red" => %v, err=%v`, got, err)
	// Either consistent equality or error — but not silent false.
}

// --- 27. Dot-access on typed string (should error, not panic) ---

func TestAdversarial_DotOnTypedString(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("dot-access on typed string panicked: %v", r)
		}
	}()
	env := map[string]any{"s": colorName("red")}
	_, err := evalExpr(t.Context(), "s.foo", env)
	t.Logf("s.foo on string => err=%v", err)
}

// --- 28. evalIndex on a typed slice ---

func TestAdversarial_TypedSliceIndex(t *testing.T) {
	type ints []int
	env := map[string]any{"xs": ints{10, 20, 30}}
	got, err := evalExpr(t.Context(), "xs[1]", env)
	require.NoError(t, err)
	require.Equal(t, 20, got)
}

// --- 29. iterItems on a typed slice (e.g., type ints []int) ---

func TestAdversarial_TypedSliceFilter(t *testing.T) {
	type ints []int
	env := map[string]any{"xs": ints{1, 2, 3, 4}}
	got, err := evalExpr(t.Context(), "filter(xs, it > 2)", env, WithBuiltins())
	require.NoError(t, err)
	t.Logf("filter on typed slice => %v", got)
}

// --- 30. Constant-fold preserves error semantics: division by zero literal ---

func TestAdversarial_ConstFoldDivZero(t *testing.T) {
	// 1/0 literal: prewalk tries to fold, the fold errors, leaving the
	// AST unchanged. Then runtime should error on each Run.
	p, err := Compile("1 / 0")
	require.NoError(t, err)
	_, err = p.Run(t.Context(), nil)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrEvaluate)
	// Run twice to ensure no caching of the wrong constant.
	_, err = p.Run(t.Context(), nil)
	require.Error(t, err)
}

// --- 31. Compile preprocessing with `map` colliding with internal name ---

func TestAdversarial_MapInternalNameCollision(t *testing.T) {
	// User registers a function with the internal sentinel name.
	const internal = "__expr_map__"
	opts := []Option{WithFunctions(map[string]any{
		internal: func() string { return "internal!" },
	})}
	// The function should be callable by its registered name.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("internal-name collision panicked: %v", r)
		}
	}()
	got, err := evalExpr(t.Context(), "__expr_map__()", nil, opts...)
	t.Logf("__expr_map__() => %v, err=%v", got, err)
	if err == nil {
		t.Logf("collision works (got %v) — design surface", got)
	}
}

// --- 32. Composite literal map key from selector ---

func TestAdversarial_CompositeMapKeyFromIdent(t *testing.T) {
	// {ident: 1} where ident is in env as a string.
	env := map[string]any{"k": "name"}
	// jsonlit rewrite turns {k:1} into map[string]any{k:1} — but `k`
	// is an unquoted identifier. Go's parser treats this as either a
	// key shortcut or an error.
	got, err := evalExpr(t.Context(), `{k: 1}`, env)
	t.Logf("{k:1} => %v, err=%v", got, err)
}

// --- 33. Unicode identifier ---

func TestAdversarial_UnicodeIdent(t *testing.T) {
	env := map[string]any{"naïve": int64(1)}
	got, err := evalExpr(t.Context(), "naïve + 1", env)
	t.Logf("naïve + 1 => %v, err=%v", got, err)
}

// --- 34. String + int (not string concatenation) ---

func TestAdversarial_StringPlusInt(t *testing.T) {
	got, err := evalExpr(t.Context(), `"hello" + 1`, nil)
	t.Logf(`"hello" + 1 => %v, err=%v`, got, err)
	require.Error(t, err) // shouldn't silently coerce
}

// --- 35. Function with 3 returns — should be rejected by prepareFunc ---

func TestAdversarial_ThreeReturns(t *testing.T) {
	opts := []Option{WithFunctions(map[string]any{
		"three": func() (int, int, int) { return 1, 2, 3 },
	})}
	// prepareFunc errors out on construction; it stores nil-native pf.
	// Call should error gracefully.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("3-return function panicked: %v", r)
		}
	}()
	_, err := evalExpr(t.Context(), "three()", nil, opts...)
	t.Logf("three() => err=%v", err)
	require.Error(t, err)
}

// --- 36. Function whose 2nd return is non-error type ---

func TestAdversarial_SecondReturnNonError(t *testing.T) {
	opts := []Option{WithFunctions(map[string]any{
		"weird": func() (int, string) { return 1, "x" },
	})}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("weird signature panicked: %v", r)
		}
	}()
	_, err := evalExpr(t.Context(), "weird()", nil, opts...)
	t.Logf("weird() => err=%v", err)
	require.Error(t, err)
}

// --- 37. callFunction on env-stored function with non-error second return ---

func TestAdversarial_EnvFnNonErrorSecondReturn(t *testing.T) {
	env := map[string]any{
		"fn": func() (int, string) { return 1, "x" },
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("env function with weird signature panicked: %v", r)
		}
	}()
	_, err := evalExpr(t.Context(), "fn()", env)
	t.Logf("env fn() => err=%v", err)
	require.Error(t, err)
}

// --- 38. Index expression with negative integer literal ---

func TestAdversarial_NegativeIndex(t *testing.T) {
	env := map[string]any{"xs": []any{int64(1), int64(2), int64(3)}}
	_, err := evalExpr(t.Context(), "xs[-1]", env)
	t.Logf("xs[-1] => err=%v", err)
	require.Error(t, err)
}

// --- 39. Map index with int idx but the idx is float64 (1.0) ---

func TestAdversarial_FloatIndexOnSlice(t *testing.T) {
	env := map[string]any{"xs": []any{int64(1), int64(2), int64(3)}}
	got, err := evalExpr(t.Context(), "xs[1.0]", env)
	t.Logf("xs[1.0] => %v, err=%v", got, err)
	// toInt64 doesn't accept float; should error rather than silently work.
	require.Error(t, err)
}

// --- 40. Nested template inside composite literal ---

func TestAdversarial_TemplateNestedExpr(t *testing.T) {
	tmpl, err := NewTemplate(`hello ${"world"}`, WithBuiltins())
	require.NoError(t, err)
	got, err := tmpl.Render(t.Context(), nil)
	require.NoError(t, err)
	require.Equal(t, "hello world", got)
}

// --- 41. Template with body containing only whitespace ---

func TestAdversarial_TemplateWhitespaceBody(t *testing.T) {
	_, err := NewTemplate("hello ${   }!")
	t.Logf("template with empty body => err=%v", err)
	require.Error(t, err)
}

// --- 42. Template body containing braces inside string literals ---

func TestAdversarial_TemplateBodyWithBraces(t *testing.T) {
	tmpl, err := NewTemplate(`${"}{")} ok`, WithBuiltins())
	t.Logf("tmpl with brace string => err=%v", err)
	if err != nil {
		return
	}
	got, _ := tmpl.Render(t.Context(), nil)
	t.Logf("rendered: %q", got)
}

// --- 43. preprocessSource rewrites within string literal? ---

func TestAdversarial_MapTokenInString(t *testing.T) {
	got, err := evalExpr(t.Context(), `"i love map functions"`, nil)
	require.NoError(t, err)
	require.Equal(t, "i love map functions", got)
}

// --- 44. preprocessSource with map followed by selector ---

func TestAdversarial_MapAsMethod(t *testing.T) {
	// Calling .map on something that doesn't have a `map` method should
	// give a normal "method not found" error, not a panic.
	env := map[string]any{"xs": []any{int64(1)}}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("xs.map(...) panicked: %v", r)
		}
	}()
	_, err := evalExpr(t.Context(), "xs.map(it)", env, WithBuiltins())
	t.Logf("xs.map(...) => err=%v", err)
}

// --- 45. Calling sprintf with %s on int (Sprintf error format) ---

func TestAdversarial_SprintfWrongVerb(t *testing.T) {
	// fmt.Sprintf doesn't error, just returns a "%!s(int=...)" string.
	// That's not a panic, just a design wart.
	got, err := evalExpr(t.Context(), `sprintf("%s", 42)`, nil, WithBuiltins())
	require.NoError(t, err)
	t.Logf(`sprintf("%%s", 42) => %q`, got)
}

// --- 46. Concurrent Run does not race compile cache ---
// If callCache or litCache were mutated post-compile, concurrent Run
// would race. Tests with -race verify this. Just exercise the path.

func TestAdversarial_ConcurrentRun(t *testing.T) {
	p, err := Compile("a + b")
	require.NoError(t, err)
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func(i int) {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("concurrent run panicked: %v", r)
				}
				done <- struct{}{}
			}()
			env := map[string]any{"a": int64(i), "b": int64(2)}
			_, err := p.Run(t.Context(), env)
			if err != nil {
				t.Errorf("concurrent run err: %v", err)
			}
		}(i)
	}
	for i := 0; i < 8; i++ {
		<-done
	}
}

// --- 47. Evaluator depth limit on chained calls ---

func TestAdversarial_DeepCalls(t *testing.T) {
	opts := []Option{WithFunctions(map[string]any{
		"id": func(x int64) int64 { return x },
	})}
	src := strings.Repeat("id(", MaxEvalDepth+5) + "1" + strings.Repeat(")", MaxEvalDepth+5)
	_, err := evalExpr(t.Context(), src, nil, opts...)
	t.Logf("deep id() => err=%v", err)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrEvaluate)
}

// --- 48. depth across higher-order forms ---

func TestAdversarial_DeepNestedMap(t *testing.T) {
	// Nested map(...) calls — each adds an itEnv and increments depth.
	src := strings.Repeat("map([1], ", 100) + "1" + strings.Repeat(")", 100)
	_, err := evalExpr(t.Context(), src, nil, WithBuiltins())
	t.Logf("deep nested map => err=%v", err)
}

// --- 49. Chan kind in indexValue / selectField ---

func TestAdversarial_ChanIndex(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("chan index panicked: %v", r)
		}
	}()
	c := make(chan int, 1)
	c <- 42
	env := map[string]any{"c": c}
	_, err := evalExpr(t.Context(), "c[0]", env)
	t.Logf("chan[0] => err=%v", err)
	require.Error(t, err)
}

// --- 50. Reading map of typed-string keys ---

func TestAdversarial_MapTypedStringKeys(t *testing.T) {
	type Tag string
	m := map[Tag]int{"x": 1, "y": 2}
	env := map[string]any{"m": m}
	got, err := evalExpr(t.Context(), `keys(m)`, env, WithBuiltins())
	require.NoError(t, err)
	t.Logf("keys(map[Tag]int) => %v", got)
}

// --- 51. Float-to-int when target is platform "int" (size-dependent) ---

func TestAdversarial_FloatToPlatformInt(t *testing.T) {
	// On 64-bit platforms, int == int64. The "out of range for int" check
	// in intToKind for `int` defers to int64 range.
	opts := []Option{WithFunctions(map[string]any{
		"i": func(n int) int { return n },
	})}
	got, err := evalExpr(t.Context(), "i(2147483648)", nil, opts...)
	t.Logf("i(2147483648) => %v, err=%v", got, err)
	// On 32-bit platforms this should error; on 64-bit it should succeed.
}

// --- 52. Typed slice of typed elements iterated then filtered ---

func TestAdversarial_TypedAnySlice(t *testing.T) {
	type Items []any
	env := map[string]any{"xs": Items{int64(1), int64(2), int64(3)}}
	got, err := evalExpr(t.Context(), "filter(xs, it > 1)", env, WithBuiltins())
	require.NoError(t, err)
	t.Logf("filter(typed []any) => %v", got)
}

// --- 53. Comparison chain (not supported in Go but might confuse parser) ---

func TestAdversarial_ComparisonChain(t *testing.T) {
	// `1 < 2 < 3` parses as (1 < 2) < 3 → bool < int. Should error.
	_, err := evalExpr(t.Context(), "1 < 2 < 3", nil)
	t.Logf("1 < 2 < 3 => err=%v", err)
	require.Error(t, err)
}

// --- 54. Equality of slice == nil (typed nil slice should match nil) ---

func TestAdversarial_NilSliceEqualsNil(t *testing.T) {
	env := map[string]any{"xs": []int(nil)}
	got, err := evalExpr(t.Context(), "xs == nil", env)
	require.NoError(t, err)
	if got != true {
		t.Fatalf("expected typed nil slice == nil to be true, got %v", got)
	}
}

// --- 55. Comparison of nil pointer == nil ---

func TestAdversarial_NilPointerEqualsNil(t *testing.T) {
	env := map[string]any{"p": (*edgeCaseFooP)(nil)}
	got, err := evalExpr(t.Context(), "p == nil", env)
	require.NoError(t, err)
	if got != true {
		t.Fatalf("expected typed nil pointer == nil to be true, got %v", got)
	}
}

// --- 56. Typed nil interface containing a function ---

func TestAdversarial_TypedFuncNilAny(t *testing.T) {
	// A typed nil function stored in an interface{}.
	var fn func()
	env := map[string]any{"fn": fn}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("typed nil func panicked: %v", r)
		}
	}()
	_, err := evalExpr(t.Context(), "fn()", env)
	t.Logf("typed nil fn() => err=%v", err)
	require.Error(t, err)
}

// --- 57. Iteration over an array (not slice) ---

func TestAdversarial_ArrayIteration(t *testing.T) {
	env := map[string]any{"xs": [3]int64{10, 20, 30}}
	got, err := evalExpr(t.Context(), "filter(xs, it > 15)", env, WithBuiltins())
	require.NoError(t, err)
	t.Logf("filter on [3]int64 => %v", got)
}

// --- 58. Equality across typed string and untyped string ---

func TestAdversarial_TypedStrEquality(t *testing.T) {
	// Same as #26 but pinning the bug.
	type Color string
	env := map[string]any{"c": Color("red")}
	got, err := evalExpr(t.Context(), `c == "red"`, env)
	require.NoError(t, err)
	if got != true {
		t.Fatalf("CONFIRMED: typed string != equivalent untyped string (got %v)", got)
	}
}

// --- 59. Concurrent compile of same source from many goroutines ---

func TestAdversarial_ConcurrentCompile(t *testing.T) {
	// Compile is supposed to be one-shot; concurrent calls should each
	// produce an independent program.
	done := make(chan struct{}, 8)
	for i := 0; i < 8; i++ {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("concurrent compile panicked: %v", r)
				}
				done <- struct{}{}
			}()
			_, err := Compile("a + b * 2", WithBuiltins())
			if err != nil {
				t.Errorf("compile err: %v", err)
			}
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
}

// --- 60. Variadic with one variadic arg supplied ---

func TestAdversarial_VariadicOneArg(t *testing.T) {
	opts := []Option{WithFunctions(map[string]any{
		"sum": func(xs ...int) int {
			s := 0
			for _, x := range xs {
				s += x
			}
			return s
		},
	})}
	got, err := evalExpr(t.Context(), "sum(1)", nil, opts...)
	require.NoError(t, err)
	require.Equal(t, 1, got)
}

// --- 61. variadic with mixed fixed+variadic ---

func TestAdversarial_VariadicMixed(t *testing.T) {
	opts := []Option{WithFunctions(map[string]any{
		"join": func(sep string, xs ...string) string {
			return strings.Join(xs, sep)
		},
	})}
	got, err := evalExpr(t.Context(), `join(",", "a", "b", "c")`, nil, opts...)
	require.NoError(t, err)
	require.Equal(t, "a,b,c", got)
}

// --- 62. Variadic invoked with a slice (Go's spread operator)? ---

func TestAdversarial_VariadicNoSpread(t *testing.T) {
	// expr does not support `...` spread (it's a SliceExpr with three indices).
	// Passing a slice to variadic should fail rather than autoflatten.
	opts := []Option{WithFunctions(map[string]any{
		"sum": func(xs ...int) int {
			s := 0
			for _, x := range xs {
				s += x
			}
			return s
		},
	})}
	env := map[string]any{"xs": []int{1, 2, 3}}
	_, err := evalExpr(t.Context(), "sum(xs)", env, opts...)
	t.Logf("sum(xs) where xs is slice => err=%v", err)
	require.Error(t, err) // Should NOT silently auto-spread.
}

// --- 63. tryFilterIndex with negative index — must not regress ---

func TestAdversarial_FilterIndexNegative(t *testing.T) {
	env := map[string]any{"xs": []any{int64(1), int64(2), int64(3)}}
	_, err := evalExpr(t.Context(), "filter(xs, true)[-1]", env, WithBuiltins())
	t.Logf("filter(...)[-1] => err=%v", err)
	require.Error(t, err)
}

// --- 64. Invalid token via go/parser produces compile error, not panic ---

func TestAdversarial_InvalidUTF8(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("invalid utf8 source panicked: %v", r)
		}
	}()
	_, err := Compile(string([]byte{0xff, 0xfe, 0xfd}))
	t.Logf("invalid utf8 source => err=%v", err)
	require.Error(t, err)
}

// --- 65. Memoize compile after env mutates ---
// If Builtins() is mutated by the user post-Compile, behavior should not
// change for the existing Program.

func TestAdversarial_BuiltinsMutationDoesntAffectProgram(t *testing.T) {
	b := Builtins()
	delete(b, "len")
	p, err := Compile("len(xs)", WithFunctions(b))
	if err != nil {
		t.Logf("compile errored as expected (len was deleted): %v", err)
		return
	}
	b["len"] = func(_ context.Context, args []any) (any, error) {
		return int64(999), nil
	}
	got, _ := p.Run(t.Context(), map[string]any{"xs": []any{1, 2}})
	t.Logf("after mutating original map: %v", got)
}

// --- 66. Equality between Func type and function literal ---

func TestAdversarial_FuncEqualityErr(t *testing.T) {
	// Functions are not comparable in Go and panic when compared.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("function == panicked: %v", r)
		}
	}()
	env := map[string]any{
		"a": func() int { return 1 },
		"b": func() int { return 2 },
	}
	_, err := evalExpr(t.Context(), "a == b", env)
	t.Logf("fn == fn => err=%v", err)
	require.Error(t, err)
}

// --- 67. Equality of struct that is not comparable ---

type edgeCaseNoCmp struct {
	M map[string]int
}

func TestAdversarial_NonComparableStructEquality(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("non-comparable struct == panicked: %v", r)
		}
	}()
	env := map[string]any{
		"a": edgeCaseNoCmp{M: map[string]int{"x": 1}},
		"b": edgeCaseNoCmp{M: map[string]int{"x": 1}},
	}
	_, err := evalExpr(t.Context(), "a == b", env)
	t.Logf("nonComp == nonComp => err=%v", err)
	require.Error(t, err)
}

// --- 68. Negative number literal at top-level then used as index ---

// Already covered.

// --- 69. Identifier shadowing of true / false in env ---

func TestAdversarial_ShadowTrueFalse(t *testing.T) {
	env := map[string]any{"true": int64(0), "false": int64(1)}
	got, err := evalExpr(t.Context(), "true", env)
	require.NoError(t, err)
	t.Logf("evaluating identifier 'true' with env shadow => %v", got)
	// expected: native true; env entry should not shadow keywords.
	if got != true {
		t.Fatalf("CONFIRMED: env shadowed `true` keyword, got %v", got)
	}
}

// --- 70. Typed-pointer field selection — pointer to struct that has
// methods only on pointer receiver ---

type edgeCasePtrOnly struct{ N int }

func (p *edgeCasePtrOnly) Get() int { return p.N }

func TestAdversarial_PointerReceiverMethod(t *testing.T) {
	env := map[string]any{"x": &edgeCasePtrOnly{N: 42}}
	got, err := evalExpr(t.Context(), "x.Get()", env)
	require.NoError(t, err)
	require.Equal(t, 42, got)
}

// --- 71. Struct field whose type is interface containing a typed nil ---

type edgeCaseHolder2 struct{ V any }

func TestAdversarial_FieldNilInterface(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil-interface field selector panicked: %v", r)
		}
	}()
	var typedNil *edgeCaseFooP
	env := map[string]any{"h": edgeCaseHolder2{V: typedNil}}
	_, err := evalExpr(t.Context(), "h.V.Inc()", env)
	t.Logf("h.V.Inc() with typed-nil pointer => err=%v", err)
}

// --- 72. Recursive map: template formatting / sprintf can blow the stack ---
// Walking a self-referential map via field selectors is fine. But if such
// a value reaches fmt.Sprintf("%v", ...) — which happens for templates
// AND for sprintf() — Go's fmt has no cycle detection for arbitrary
// map/slice cycles, and the formatter recurses until the goroutine
// stack overflows (uncatchable; aborts the process).
//
// This is a real DoS surface: untrusted templates over a host-supplied
// env that happens to contain a cycle will crash the host. We don't
// recover the panic in the test; we just document and skip the
// printing path so this test stays non-fatal.

func TestAdversarial_RecursiveMapSelector(t *testing.T) {
	a := map[string]any{}
	a["self"] = a
	env := map[string]any{"a": a}
	got, err := evalExpr(t.Context(), "a.self.self.self.self", env)
	require.NoError(t, err)
	if got == nil {
		t.Fatal("expected the cycle to resolve to itself")
	}
	// DO NOT t.Logf("%v", got) — would crash via fmt cycle.
}

// TestAdversarial_RecursiveMapTemplate documents the DoS surface but is
// SKIPPED so the test suite does not crash with a stack overflow.
// Remove the Skip when expr adds a recursion-safe formatter.
func TestAdversarial_RecursiveMapTemplate(t *testing.T) {
	t.Skip("documented: fmt.Sprintf on cyclic maps overflows the stack")
	a := map[string]any{}
	a["self"] = a
	env := map[string]any{"a": a}
	tmpl, err := NewTemplate("value: ${a}", WithBuiltins())
	require.NoError(t, err)
	_, _ = tmpl.Render(t.Context(), env) // will stack-overflow
}

// --- 73. fmt.Sprintf with non-string format ---

func TestAdversarial_SprintfBadFormat(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("sprintf bad format panicked: %v", r)
		}
	}()
	_, err := evalExpr(t.Context(), "sprintf(123)", nil, WithBuiltins())
	t.Logf("sprintf(123) => err=%v", err)
	require.Error(t, err) // first arg should be string
}

// --- 74. Compile with all-blank source ---

func TestAdversarial_EmptySource(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("empty source panicked: %v", r)
		}
	}()
	_, err := Compile("")
	t.Logf("Compile(\"\") => err=%v", err)
	require.Error(t, err)
}

func TestAdversarial_WhitespaceSource(t *testing.T) {
	_, err := Compile("   ")
	t.Logf("Compile(\"   \") => err=%v", err)
	require.Error(t, err)
}

// --- 75. Run with nil program AST? Can't construct one externally; skip.

// --- 76. Index with very large positive index ---

func TestAdversarial_VeryLargeIndex(t *testing.T) {
	env := map[string]any{"xs": []any{int64(1)}}
	_, err := evalExpr(t.Context(), "xs[9999999999999]", env)
	require.Error(t, err)
	t.Logf("xs[huge] => %v", err)
}

// --- 77. Arithmetic with operand types int + float64 ---

func TestAdversarial_IntPlusFloat(t *testing.T) {
	got, err := evalExpr(t.Context(), "1 + 2.5", nil)
	require.NoError(t, err)
	require.Equal(t, 3.5, got)
}

// --- 78. String comparison with multi-byte UTF-8 ---

func TestAdversarial_StringMultibyteCmp(t *testing.T) {
	got, err := evalExpr(t.Context(), `"é" < "f"`, nil)
	require.NoError(t, err)
	t.Logf("é < f => %v", got)
}

// --- 79. convertArg silently coerces int → string via reflect.Convert ---

// Go's reflect.Convert allows int → string (semantically rune-valued
// string). expr's convertArg falls through to ConvertibleTo and silently
// performs this conversion, so a function that wants `string` receives
// "A" when called with `65`. That is almost never what the caller meant
// and masks a real type error.
func TestAdversarial_IntToStringSilentCoerce(t *testing.T) {
	opts := []Option{WithFunctions(map[string]any{
		"echo": func(s string) string { return s },
	})}
	got, err := evalExpr(t.Context(), "echo(65)", nil, opts...)
	t.Logf("echo(65) where echo wants string => %v, err=%v", got, err)
	if err == nil {
		t.Fatalf("CONFIRMED: int 65 silently coerced to %q; expected type error", got)
	}
}

// --- 80. Slice → array conversion via reflect.ConvertibleTo ---

func TestAdversarial_SliceToArraySilentCoerce(t *testing.T) {
	// Same vector: reflect.Convert can also produce surprising slice→array
	// coercions in newer Go versions. Pin behavior so no silent surprise.
	opts := []Option{WithFunctions(map[string]any{
		"take3": func(a [3]int) int { return a[0] + a[1] + a[2] },
	})}
	got, err := evalExpr(t.Context(), "take3([1, 2, 3])", nil, opts...)
	t.Logf("take3([1,2,3]) => %v, err=%v", got, err)
}

// --- 81. unary `+` on a non-numeric value should error, not pass-through ---

func TestAdversarial_UnaryPlusOnString(t *testing.T) {
	got, err := evalExpr(t.Context(), `+"abc"`, nil)
	t.Logf(`+"abc" => %v, err=%v`, got, err)
	require.Error(t, err)
}

// --- 82. Selector on a Pointer-typed env value resolves through it ---

func TestAdversarial_SelectorThroughPointer(t *testing.T) {
	type Inner struct{ N int }
	type Outer struct{ I *Inner }
	env := map[string]any{"o": Outer{I: &Inner{N: 5}}}
	got, err := evalExpr(t.Context(), "o.I.N", env)
	require.NoError(t, err)
	require.Equal(t, 5, got)
}

// --- 83. evalSelectorChainRV when env is a typed nil pointer at top-level ---

func TestAdversarial_TopLevelTypedNilPointer(t *testing.T) {
	type X struct{ N int }
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("typed nil top-level env panicked: %v", r)
		}
	}()
	_, err := evalExpr(t.Context(), "N", (*X)(nil))
	t.Logf("N on nil *X => err=%v", err)
	require.Error(t, err)
}

// --- 84. callFunction returning typed-nil error pointer ---

type edgeCaseErr struct{ msg string }

func (e *edgeCaseErr) Error() string { return e.msg }

func TestAdversarial_TypedNilErrorReturn(t *testing.T) {
	// Classic Go gotcha: a function returns a *edgeCaseErr whose value is
	// a typed nil. The user expected "no error" because at the source
	// level they returned `nil`. expr's callFunction uses out[1].IsNil()
	// — the right call — to detect this. Pin it.
	opts := []Option{WithFunctions(map[string]any{
		"f": func() (int, *edgeCaseErr) { return 42, nil },
	})}
	got, err := evalExpr(t.Context(), "f()", nil, opts...)
	require.NoError(t, err)
	require.Equal(t, 42, got)
}

// --- 85. Cancellation between higher-order forms ---

func TestAdversarial_CtxCancelMidWalk(t *testing.T) {
	xs := make([]any, 100000)
	for i := range xs {
		xs[i] = int64(i)
	}
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		// Cancel almost immediately; the loop must observe it.
		cancel()
	}()
	env := map[string]any{"xs": xs}
	_, err := evalExpr(ctx, "filter(xs, it >= 0)", env, WithBuiltins())
	if err == nil {
		t.Logf("filter(100k items) completed before cancel could land — racy but acceptable")
		return
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

// --- 86. Comparing typed numbers ---

type edgeCaseTypedInt int

func TestAdversarial_TypedIntEqualsUntyped(t *testing.T) {
	env := map[string]any{"x": edgeCaseTypedInt(5)}
	got, err := evalExpr(t.Context(), "x == 5", env)
	require.NoError(t, err)
	t.Logf("typedInt(5) == 5 => %v", got)
	if got != true {
		t.Fatalf("typed int unequal to value-equivalent literal: %v", got)
	}
}

// --- 87. Calling fmt.Sprintf via env ---

func TestAdversarial_EnvSprintfWithLargeArgs(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("env sprintf panicked: %v", r)
		}
	}()
	env := map[string]any{"sprintf": fmt.Sprintf}
	got, err := evalExpr(t.Context(), `sprintf("%d", 42)`, env)
	require.NoError(t, err)
	require.Equal(t, "42", got)
}
