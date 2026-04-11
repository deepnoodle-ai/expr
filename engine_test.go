package expr

import (
	"errors"
	"strings"
	"testing"

	"github.com/deepnoodle-ai/expr/internal/require"
)

func TestEval_Literals(t *testing.T) {
	cases := []struct {
		expr string
		want any
	}{
		{"42", int64(42)},
		{"3.14", 3.14},
		{`"hello"`, "hello"},
		{"true", true},
		{"false", false},
		{"nil", nil},
		{"-5", int64(-5)},
		{"!true", false},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := Eval(t.Context(), tc.expr, nil)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestEval_Arithmetic(t *testing.T) {
	cases := []struct {
		expr string
		want any
	}{
		{"1 + 2", int64(3)},
		{"5 - 3", int64(2)},
		{"4 * 5", int64(20)},
		{"10 / 3", int64(3)},
		{"10 % 3", int64(1)},
		{"2 + 3 * 4", int64(14)},
		{"(2 + 3) * 4", int64(20)},
		{"1.5 + 2.5", 4.0},
		{"10 / 4.0", 2.5},
		{`"foo" + "bar"`, "foobar"},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := Eval(t.Context(), tc.expr, nil)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestEval_Comparison(t *testing.T) {
	cases := []struct {
		expr string
		want bool
	}{
		{"1 < 2", true},
		{"2 < 1", false},
		{"3 == 3", true},
		{"3 != 4", true},
		{"3 >= 3", true},
		{"3 <= 2", false},
		{`"a" < "b"`, true},
		{`"foo" == "foo"`, true},
		{"1 == 1.0", true},
		{"1 != 1.0", false},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := Eval(t.Context(), tc.expr, nil)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestEval_Logical(t *testing.T) {
	cases := []struct {
		expr string
		want bool
	}{
		{"true && true", true},
		{"true && false", false},
		{"false || true", true},
		{"false || false", false},
		{"1 < 2 && 3 < 4", true},
		{"1 > 2 || 3 < 4", true},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := Eval(t.Context(), tc.expr, nil)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestEval_ShortCircuit(t *testing.T) {
	env := map[string]any{
		"exploder": func() bool { panic("should not be called") },
	}
	// && short-circuits when lhs is false
	got, err := Eval(t.Context(), "false && exploder()", env)
	require.NoError(t, err)
	require.Equal(t, false, got)

	// || short-circuits when lhs is true
	got, err = Eval(t.Context(), "true || exploder()", env)
	require.NoError(t, err)
	require.Equal(t, true, got)
}

func TestEval_Selectors(t *testing.T) {
	env := map[string]any{
		"state": map[string]any{
			"counter": int64(5),
			"user": map[string]any{
				"name": "Alice",
				"age":  int64(30),
			},
		},
	}
	cases := []struct {
		expr string
		want any
	}{
		{"state.counter", int64(5)},
		{"state.user.name", "Alice"},
		{"state.user.age >= 18", true},
		{"state.counter + 10", int64(15)},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := Eval(t.Context(), tc.expr, env)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestEval_IndexExpressions(t *testing.T) {
	env := map[string]any{
		"state": map[string]any{
			"items":  []any{"a", "b", "c"},
			"counts": map[string]any{"apples": int64(3), "oranges": int64(7)},
		},
	}
	cases := []struct {
		expr string
		want any
	}{
		{`state.items[0]`, "a"},
		{`state.items[2]`, "c"},
		{`state["counts"]["apples"]`, int64(3)},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := Eval(t.Context(), tc.expr, env)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestEval_StructAndPointer(t *testing.T) {
	type User struct {
		Name string
		Age  int
	}
	u := &User{Name: "Bob", Age: 42}
	env := map[string]any{"user": u}

	got, err := Eval(t.Context(), "user.Name", env)
	require.NoError(t, err)
	require.Equal(t, "Bob", got)

	got, err = Eval(t.Context(), "user.Age >= 18", env)
	require.NoError(t, err)
	require.Equal(t, true, got)
}

type testEnv struct {
	Count int
	Name  string
	Items []int
}

func (e testEnv) Double() int             { return e.Count * 2 }
func (e testEnv) Greet(who string) string { return "Hello, " + who }

type ptrEnv struct {
	Value int
}

func (e *ptrEnv) Triple() int { return e.Value * 3 }

func TestEval_StructEnv(t *testing.T) {
	opts := []CompileOption{WithBuiltins()}
	env := testEnv{Count: 5, Name: "Alice", Items: []int{1, 2, 3}}

	cases := []struct {
		expr string
		want any
	}{
		{"Count", 5},
		{"Count * 2", int64(10)},
		{"Count >= 5", true},
		{`Name + " says hi"`, "Alice says hi"},
		{"len(Items)", 3},
		{"Items[1]", 2},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := Eval(t.Context(), tc.expr, env, opts...)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestEval_StructEnv_Methods(t *testing.T) {
	env := testEnv{Count: 21}

	got, err := Eval(t.Context(), "Double()", env)
	require.NoError(t, err)
	require.Equal(t, 42, got)

	got, err = Eval(t.Context(), `Greet("world")`, env)
	require.NoError(t, err)
	require.Equal(t, "Hello, world", got)
}

func TestEval_PointerEnv_WithPointerMethod(t *testing.T) {
	env := &ptrEnv{Value: 7}

	got, err := Eval(t.Context(), "Value * 2", env)
	require.NoError(t, err)
	require.Equal(t, int64(14), got)

	got, err = Eval(t.Context(), "Triple()", env)
	require.NoError(t, err)
	require.Equal(t, 21, got)
}

func TestEval_StructEnv_FieldBeatsFunction(t *testing.T) {
	// A struct field named "Len" should shadow the len builtin at the
	// root-lookup stage. The engine opts in to WithBuiltins so the
	// shadowing is actually meaningful.
	opts := []CompileOption{WithBuiltins()}
	type hasLen struct{ Len int }
	env := hasLen{Len: 99}

	got, err := Eval(t.Context(), "Len", env, opts...)
	require.NoError(t, err)
	require.Equal(t, 99, got)
}

func TestEval_EngineEval_StructEnv(t *testing.T) {
	env := testEnv{Count: 10}
	got, err := Eval(t.Context(), "Count * 4", env)
	require.NoError(t, err)
	require.Equal(t, int64(40), got)
}

func TestEval_Builtins(t *testing.T) {
	opts := []CompileOption{WithBuiltins()}
	env := map[string]any{
		"state": map[string]any{
			"name":  "Alice",
			"items": []any{1, 2, 3, 4},
			"tags":  map[string]any{"red": true, "blue": false},
		},
	}
	cases := []struct {
		expr string
		want any
	}{
		{`len(state.items)`, 4},
		{`len(state.name)`, 5},
		{`upper(state.name)`, "ALICE"},
		{`lower("WORLD")`, "world"},
		{`contains(state.name, "lic")`, true},
		{`contains(state.items, 3)`, true},
		{`has(state.tags, "red")`, true},
		{`has(state.tags, "green")`, false},
		{`int("42")`, int64(42)},
		{`float("3.14")`, 3.14},
		{`string(42)`, "42"},
		{`sprintf("%d + %d = %d", 1, 2, 3)`, "1 + 2 = 3"},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, err := Eval(t.Context(), tc.expr, env, opts...)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestEval_Keys(t *testing.T) {
	opts := []CompileOption{WithBuiltins()}
	env := map[string]any{
		"m": map[string]any{"b": 2, "a": 1, "c": 3},
	}
	got, err := Eval(t.Context(), "keys(m)", env, opts...)
	require.NoError(t, err)
	require.Equal(t, []any{"a", "b", "c"}, got)
}

func TestEngine_CustomFunctions(t *testing.T) {
	opts := []CompileOption{WithFunctions(map[string]any{
		"double": func(n int) int { return n * 2 },
		"greet":  func(name string) string { return "Hello, " + name },
	})}
	got, err := Eval(t.Context(), "double(state.count)", map[string]any{
		"state": map[string]any{"count": int64(21)},
	}, opts...)
	require.NoError(t, err)
	require.Equal(t, 42, got)

	got, err = Eval(t.Context(), `greet("world")`, nil, opts...)
	require.NoError(t, err)
	require.Equal(t, "Hello, world", got)
}

func TestEngine_CustomFunction_WithError(t *testing.T) {
	boom := errors.New("boom")
	opts := []CompileOption{WithFunctions(map[string]any{
		"fail": func() (int, error) { return 0, boom },
	})}
	_, err := Eval(t.Context(), "fail()", nil, opts...)
	require.ErrorIs(t, err, boom)
}

func TestEngine_DefaultNoBuiltins(t *testing.T) {
	// Compile/Eval with no options has no functions registered by default;
	// calling a builtin without opting in should surface an "unknown function" error.
	_, err := Eval(t.Context(), "len(state.items)", map[string]any{"state": map[string]any{"items": []any{1}}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown function")
}

func TestEngine_WithBuiltins(t *testing.T) {
	// Opting in to WithBuiltins makes the standard set callable.
	opts := []CompileOption{WithBuiltins()}
	got, err := Eval(t.Context(), "len(state.items)", map[string]any{"state": map[string]any{"items": []any{1, 2, 3}}}, opts...)
	require.NoError(t, err)
	require.Equal(t, 3, got)
}

func TestCompile_Reuse(t *testing.T) {
	p, err := Compile("state.x * 2")
	require.NoError(t, err)

	r1, err := p.Run(t.Context(), map[string]any{"state": map[string]any{"x": int64(5)}})
	require.NoError(t, err)
	require.Equal(t, int64(10), r1)

	r2, err := p.Run(t.Context(), map[string]any{"state": map[string]any{"x": int64(21)}})
	require.NoError(t, err)
	require.Equal(t, int64(42), r2)

	require.Equal(t, "state.x * 2", p.Source())
}

func TestCompile_SyntaxError(t *testing.T) {
	_, err := Compile("1 + + +")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrCompile)
}

func TestEval_UnsupportedSyntax(t *testing.T) {
	cases := []string{
		"state.items[1:3]",    // slice expression
		"x.(int)",             // type assertion
		"func() int { 1 }()",  // function literal
		"[]int{1, 2, 3}",      // composite literal
	}
	for _, expr := range cases {
		t.Run(expr, func(t *testing.T) {
			_, err := Eval(t.Context(), expr, map[string]any{"state": map[string]any{"items": []any{1, 2, 3}}, "x": 1})
			require.Error(t, err)
		})
	}
}

func TestEngine_ProgramRun(t *testing.T) {
	p, err := Compile("state.x > 5")
	require.NoError(t, err)

	v, err := p.Run(t.Context(), map[string]any{"state": map[string]any{"x": int64(10)}})
	require.NoError(t, err)
	require.Equal(t, true, v)
	require.True(t, IsTruthy(v))
}

func TestEngine_Template(t *testing.T) {
	tmpl, err := NewTemplate("Hello ${state.name}, you are ${state.age} years old")
	require.NoError(t, err)
	got, err := tmpl.Render(t.Context(), map[string]any{
		"state": map[string]any{"name": "Alice", "age": int64(30)},
	})
	require.NoError(t, err)
	require.Equal(t, "Hello Alice, you are 30 years old", got)
}

// Callables stored directly in env must be invocable as if they had
// been registered via WithFunctions. This is the hybrid path: it
// covers per-request closures and any host that wants to rebind
// helpers on every Run without re-Compiling.
func TestEval_EnvCallable(t *testing.T) {
	env := map[string]any{
		"name":  "ada",
		"upper": strings.ToUpper,
		"addN": func(n, m int) int { return n + m },
		"greet": func(who string) (string, error) {
			if who == "" {
				return "", errors.New("empty name")
			}
			return "hi " + who, nil
		},
	}

	v, err := Eval(t.Context(), `upper(name)`, env)
	require.NoError(t, err)
	require.Equal(t, "ADA", v)

	v, err = Eval(t.Context(), `addN(2, 3)`, env)
	require.NoError(t, err)
	require.Equal(t, 5, v)

	v, err = Eval(t.Context(), `greet(name)`, env)
	require.NoError(t, err)
	require.Equal(t, "hi ada", v)

	_, err = Eval(t.Context(), `greet("")`, env)
	require.Error(t, err)

	// An env callable must shadow an identically-named registered
	// function, matching the env→funcs lookup order.
	v, err = Eval(t.Context(), `upper(name)`, env,
		WithFunctions(map[string]any{"upper": strings.ToLower}))
	require.NoError(t, err)
	require.Equal(t, "ADA", v)
}

func TestEval_UndefinedIdentifier(t *testing.T) {
	_, err := Eval(t.Context(), "no_such_var", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "undefined identifier")
}

func TestEval_StringMethods(t *testing.T) {
	// String builtins via WithFunctions — demonstrates Go interop.
	opts := []CompileOption{WithFunctions(map[string]any{
		"trim": strings.TrimSpace,
	})}
	got, err := Eval(t.Context(), `trim("  hi  ")`, nil, opts...)
	require.NoError(t, err)
	require.Equal(t, "hi", got)
}

func TestEval_CompositeLit_SliceAny(t *testing.T) {
	// Typed []any literals evaluate without any option.
	got, err := Eval(t.Context(), `[]any{1, 2, 3}`, nil)
	require.NoError(t, err)
	require.Equal(t, []any{int64(1), int64(2), int64(3)}, got)

	got, err = Eval(t.Context(), `[]any{}`, nil)
	require.NoError(t, err)
	require.Equal(t, []any{}, got)

	// Mixed element types and sub-expressions.
	got, err = Eval(t.Context(), `[]any{1 + 2, "hi", true, nil}`, nil)
	require.NoError(t, err)
	require.Equal(t, []any{int64(3), "hi", true, nil}, got)
}

func TestEval_CompositeLit_MapStringAny(t *testing.T) {
	got, err := Eval(t.Context(), `map[string]any{"a": 1, "b": "two"}`, nil)
	require.NoError(t, err)
	require.Equal(t, map[string]any{"a": int64(1), "b": "two"}, got)

	got, err = Eval(t.Context(), `map[string]any{}`, nil)
	require.NoError(t, err)
	require.Equal(t, map[string]any{}, got)
}

func TestEval_CompositeLit_Nested(t *testing.T) {
	got, err := Eval(t.Context(),
		`map[string]any{"items": []any{1, 2, 3}, "meta": map[string]any{"ok": true}}`,
		nil)
	require.NoError(t, err)
	require.Equal(t, map[string]any{
		"items": []any{int64(1), int64(2), int64(3)},
		"meta":  map[string]any{"ok": true},
	}, got)
}

func TestEval_CompositeLit_IndexAndSelect(t *testing.T) {
	// Composite literals are first-class expressions, so they can be
	// indexed and selected into.
	got, err := Eval(t.Context(), `[]any{10, 20, 30}[1]`, nil)
	require.NoError(t, err)
	require.Equal(t, int64(20), got)

	got, err = Eval(t.Context(), `map[string]any{"k": 42}["k"]`, nil)
	require.NoError(t, err)
	require.Equal(t, int64(42), got)

	got, err = Eval(t.Context(), `map[string]any{"k": 42}.k`, nil)
	require.NoError(t, err)
	require.Equal(t, int64(42), got)
}

func TestEval_CompositeLit_Unsupported(t *testing.T) {
	cases := []string{
		`[]int{1, 2}`,
		`[3]any{1, 2, 3}`,
		`map[int]any{1: "a"}`,
		`map[string]int{"a": 1}`,
	}
	for _, src := range cases {
		t.Run(src, func(t *testing.T) {
			_, err := Eval(t.Context(), src, nil)
			require.Error(t, err)
			require.ErrorIs(t, err, ErrEvaluate)
		})
	}
}

func TestEval_JSONLiterals(t *testing.T) {
	cases := []struct {
		src  string
		want any
	}{
		{`[1, 2, 3]`, []any{int64(1), int64(2), int64(3)}},
		{`[]`, []any{}},
		{`{"k": 1}`, map[string]any{"k": int64(1)}},
		{`{}`, map[string]any{}},
		{
			`{"items": [1, 2, 3], "ok": true}`,
			map[string]any{"items": []any{int64(1), int64(2), int64(3)}, "ok": true},
		},
		{`[{"n": 1}, {"n": 2}]`, []any{
			map[string]any{"n": int64(1)},
			map[string]any{"n": int64(2)},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.src, func(t *testing.T) {
			got, err := Eval(t.Context(), tc.src, nil)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestEval_JSONLiterals_MapInterop(t *testing.T) {
	// map higher-order form still works on bare-literal slices.
	opts := []CompileOption{WithBuiltins()}
	got, err := Eval(t.Context(), `map([1, 2, 3], it * 10)`, nil, opts...)
	require.NoError(t, err)
	require.Equal(t, []any{int64(10), int64(20), int64(30)}, got)
}
