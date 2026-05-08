package jsonlit

import (
	"go/parser"
	"testing"
)

// TestRewrite covers the full surface of the transform with
// hand-crafted input/output pairs. Each case documents a specific
// rule the rewriter is supposed to follow.
func TestRewrite(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// --- identity on plain expressions ---
		{"empty", "", ""},
		{"whitespace", "   ", "   "},
		{"int", "42", "42"},
		{"arith", "1 + 2 * 3", "1 + 2 * 3"},
		{"selector", "a.b.c", "a.b.c"},
		{"call", "f(x, y)", "f(x, y)"},
		{"index", "a[0]", "a[0]"},
		{"nested_index", "a[b[0]]", "a[b[0]]"},
		{"chained_index", "a[0][1]", "a[0][1]"},
		{"logic", "a && b || !c", "a && b || !c"},
		{"compare", "a == b", "a == b"},
		{"unary", "-a + +b", "-a + +b"},
		{"paren", "(a + b) * c", "(a + b) * c"},
		{"plain_string", `"hello"`, `"hello"`},

		// --- strings / runes / comments preserve their contents ---
		{"string_with_brackets", `"[1,2,3]"`, `"[1,2,3]"`},
		{"string_with_braces", `"{\"k\": 1}"`, `"{\"k\": 1}"`},
		{"raw_string_brackets", "`[1,2,3]`", "`[1,2,3]`"},
		{"raw_string_braces", "`{k: 1}`", "`{k: 1}`"},
		{"char_lbrack", "'['", "'['"},
		{"char_lbrace", "'{'", "'{'"},
		{"concat_brackets", `"[" + "]"`, `"[" + "]"`},
		{"line_comment", "a + /* [1] */ b", "a + /* [1] */ b"},

		// --- already-valid Go composite literals pass through ---
		{"slice_any", "[]any{1, 2}", "[]any{1, 2}"},
		{"slice_int", "[]int{1, 2}", "[]int{1, 2}"},
		{"slice_string", `[]string{"a","b"}`, `[]string{"a","b"}`},
		{"slice_empty_any", "[]any{}", "[]any{}"},
		{"slice_of_slice", "[][]any{}", "[][]any{}"},
		{"slice_of_map", `[]map[string]any{}`, `[]map[string]any{}`},
		{"slice_interface", "[]interface{}{1}", "[]interface{}{1}"},
		{"map_typed", `map[string]any{"k": 1}`, `map[string]any{"k": 1}`},
		{"map_typed_nested",
			`map[string]any{"k": map[string]any{"j": 1}}`,
			`map[string]any{"k": map[string]any{"j": 1}}`},

		// --- bare array literals ---
		{"array_empty", "[]", "[]any{}"},
		{"array_one", "[1]", "[]any{1}"},
		{"array_many", "[1, 2, 3]", "[]any{1, 2, 3}"},
		{"array_mixed", `[1, "two", true, nil]`, `[]any{1, "two", true, nil}`},
		{"array_nested", "[[1,2],[3,4]]", "[]any{[]any{1,2},[]any{3,4}}"},
		{"array_with_empty_inner", "[1, [], 2]", "[]any{1, []any{}, 2}"},
		{"array_deep",
			"[1, [2, [3, [4]]]]",
			"[]any{1, []any{2, []any{3, []any{4}}}}"},

		// --- bare object literals ---
		{"object_empty", "{}", "map[string]any{}"},
		{"object_one", `{"a": 1}`, `map[string]any{"a": 1}`},
		{"object_many", `{"a": 1, "b": 2}`, `map[string]any{"a": 1, "b": 2}`},
		{"object_nested",
			`{"a": {"b": {"c": 1}}}`,
			`map[string]any{"a": map[string]any{"b": map[string]any{"c": 1}}}`},

		// --- mixed arrays and objects ---
		{"array_of_objects",
			`[{"a":1},{"b":2}]`,
			`[]any{map[string]any{"a":1},map[string]any{"b":2}}`},
		{"object_with_array",
			`{"items": [1, 2, 3]}`,
			`map[string]any{"items": []any{1, 2, 3}}`},
		{"object_with_array_of_objects",
			`{"rows": [{"id": 1}, {"id": 2}]}`,
			`map[string]any{"rows": []any{map[string]any{"id": 1}, map[string]any{"id": 2}}}`},

		// --- indexing / selector into literals ---
		{"index_array_lit", "[1,2,3][0]", "[]any{1,2,3}[0]"},
		{"index_object_lit", `{"a":1}["a"]`, `map[string]any{"a":1}["a"]`},
		{"selector_on_object_lit", `{"a":1}.a`, `map[string]any{"a":1}.a`},

		// --- literals inside calls and higher-order forms ---
		{"call_array", "f([1, 2])", "f([]any{1, 2})"},
		{"call_object", `f({"k": 1})`, `f(map[string]any{"k": 1})`},
		{"call_both", `f([1], {"a": 1})`, `f([]any{1}, map[string]any{"a": 1})`},
		{"len_of_literal", "len([1,2,3])", "len([]any{1,2,3})"},
		{"map_higher_order",
			"map(xs, it * 2)",
			"map(xs, it * 2)"},
		{"filter_with_literal",
			"filter(xs, contains([1,2,3], it))",
			"filter(xs, contains([]any{1,2,3}, it))"},

		// --- literals after operators ---
		{"plus_array", "a + [1, 2]", "a + []any{1, 2}"},
		{"eq_object", `x == {"k": 1}`, `x == map[string]any{"k": 1}`},
		{"contains_call",
			`contains([1, 2, 3], 2)`,
			`contains([]any{1, 2, 3}, 2)`},

		// --- interaction with 'map' keyword ---
		{"map_type_preserved",
			`map[string]any{"k": [1, 2]}`,
			`map[string]any{"k": []any{1, 2}}`},

		// --- disambiguation from Go array-type expressions ---
		// These are valid Go expressions that we must not mangle
		// even though expr itself never accepts array types.
		{"array_type_zero", "[0]A", "[0]A"},
		{"array_type_const", "[3]int{1,2,3}", "[3]int{1,2,3}"},
		{"array_type_computed", "[len(x)]T", "[len(x)]T"},
		// But a bracket chain with no type after must be read as
		// "literal then index": [1][0] means "index 0 of array [1]".
		{"literal_then_index", "[1][0]", "[]any{1}[0]"},
		{"literal_then_index_multi", "[1,2,3][0]", "[]any{1,2,3}[0]"},

		// --- multi-dimensional and array-of-slice type prefixes ---
		// `[N][...]T` is a Go array-of-slice or 2D-array type and
		// must pass through unchanged. The recursive form of
		// isArrayTypeFollow handles arbitrary nesting as long as the
		// innermost bracket pair is followed by a type.
		{"array_of_slice", "[2][]int{{1},{2}}", "[2][]int{{1},{2}}"},
		{"array_of_array", "[3][4]int{{1,2,3,4}}", "[3][4]int{{1,2,3,4}}"},
		{"array_of_array_of_slice", "[2][3][]int{}", "[2][3][]int{}"},

		// --- receive-only channel slice type ---
		// `[]<-chan T{}` must pass through; '<-' starts a type.
		{"slice_recv_chan", "[]<-chan int{}", "[]<-chan int{}"},

		// --- function literals ---
		// `{` after `)` is a function-literal body, not an object.
		{"func_lit_empty", "func() {}", "func() {}"},
		{"func_lit_returning_int", "func() int { return 1 }", "func() int { return 1 }"},
		{"func_lit_in_call", "f(func() int { return 1 })", "f(func() int { return 1 })"},

		// --- nested type-omitted composite literals ---
		// Go allows omitting the inner type when the element type is
		// itself a composite. jsonlit must respect that and leave the
		// inner '{...}' alone.
		{"slice_of_slice_omitted", "[][]int{{1,2},{3,4}}", "[][]int{{1,2},{3,4}}"},
		{"slice_of_struct_omitted",
			"[]struct{X int}{{1},{2}}",
			"[]struct{X int}{{1},{2}}"},
		{"slice_of_pointer_omitted", "[]*T{{}, {}}", "[]*T{{}, {}}"},
		{"slice_of_map_omitted",
			`[]map[string]int{{"a":1}}`,
			`[]map[string]int{{"a":1}}`},
		{"map_of_struct_omitted",
			"map[int]struct{X int}{1: {1}, 2: {2}}",
			"map[int]struct{X int}{1: {1}, 2: {2}}"},
		{"map_of_slice_omitted",
			`map[string][]int{"a": {1,2}}`,
			`map[string][]int{"a": {1,2}}`},

		// --- generic instantiations ---
		// `T[U]{...}` is a Go 1.18+ generic composite literal.
		// `]` must end a type expression in this context.
		{"generic_empty", "List[int]{}", "List[int]{}"},
		{"generic_with_values", "List[int]{1, 2}", "List[int]{1, 2}"},
		{"generic_struct", "Box[string]{X: 1}", "Box[string]{X: 1}"},
		{"generic_two_params", "M[K, V]{x: 1}", "M[K, V]{x: 1}"},

		// --- comment preservation inside empty brackets ---
		{"empty_array_with_block_comment",
			"[/* note */]",
			"[]any{/* note */}"},
		{"empty_array_with_line_comment",
			"[\n// note\n]",
			"[]any{\n// note\n}"},

		// --- typed-outer with bare-inner JSON literal ---
		// When the outer type is `[]any` / `map[string]any` /
		// `[]interface{}` / `map[string]interface{}`, the element
		// type is interface and Go requires explicit element types.
		// jsonlit's bare-{ rewrite stays in effect for these forms.
		{"map_typed_nested_bare",
			`map[string]any{"k": {"j": 1}}`,
			`map[string]any{"k": map[string]any{"j": 1}}`},
		{"slice_any_with_bare_obj",
			`[]any{{"k": 1}}`,
			`[]any{map[string]any{"k": 1}}`},
		{"slice_interface_with_bare_obj",
			`[]interface{}{{"k": 1}}`,
			`[]interface{}{map[string]any{"k": 1}}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Rewrite(tc.in)
			if got != tc.want {
				t.Errorf("Rewrite(%q)\n got  %q\n want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestRewriteParses asserts that the rewritten source for every
// reasonable JSON-extended expression is accepted by
// go/parser.ParseExpr. This is the "bombproof" check: if the output
// can't round-trip through the parser, the rewrite is broken.
func TestRewriteParses(t *testing.T) {
	inputs := []string{
		// plain Go expressions
		"1",
		"a + b",
		"a.b.c",
		"f(x, y, z)",
		"a[0]",
		"a[b[c]]",
		"(a + b) * (c - d)",
		`"string"`,
		`"[1,2,3]"`,
		`len("[]")`,

		// already-valid Go composites
		"[]any{1,2}",
		"[]int{1,2,3}",
		`map[string]any{"k":1}`,
		"[]interface{}{}",
		"[][]any{}",

		// bare array / object literals
		"[]",
		"{}",
		"[1, 2, 3]",
		`{"k": 1}`,
		`{"a": 1, "b": [1, 2], "c": {"d": 3}}`,
		`[{"id": 1}, {"id": 2}, {"id": 3}]`,
		"[[1,2],[3,4],[5,6]]",
		`[1, [2, [3, [4, [5]]]]]`,

		// literals interacting with indexing, calls, operators
		"[1,2,3][0]",
		`{"a":1}["a"]`,
		"f([1,2], {\"k\":1})",
		`len({"a":1})`,
		`[1,2,3] == [1,2,3]`,
	}
	for _, in := range inputs {
		t.Run(in, func(t *testing.T) {
			out := Rewrite(in)
			if _, err := parser.ParseExpr(out); err != nil {
				t.Errorf("ParseExpr(%q) (rewritten from %q) failed: %v",
					out, in, err)
			}
		})
	}
}

// TestIdempotent asserts that running Rewrite on its own output
// produces no further changes. This matters because users may mix
// bare and typed forms in the same expression, and because any
// future integration may run the rewriter more than once.
func TestIdempotent(t *testing.T) {
	inputs := []string{
		// plain expressions
		"1 + 2",
		"a.b.c",
		"f(x, y)",
		"a[0]",
		// existing composites
		"[]any{1,2}",
		"[]int{1,2,3}",
		`map[string]any{"k":1}`,
		"[]interface{}{}",
		"[][]any{}",
		// bare literals
		"[1,2,3]",
		`{"a":1}`,
		"[]",
		"{}",
		`[{"a":[1,2]}, {"b":{}}]`,
		`{"k": [1, [2, 3], {"q": 4}]}`,
	}
	for _, in := range inputs {
		t.Run(in, func(t *testing.T) {
			once := Rewrite(in)
			twice := Rewrite(once)
			if once != twice {
				t.Errorf("not idempotent:\n  in    %q\n  once  %q\n  twice %q",
					in, once, twice)
			}
		})
	}
}

// TestUnbalancedDoesNotPanic asserts that malformed input is
// tolerated: Rewrite must not panic on unbalanced brackets or
// braces, and must produce some output that the parser can
// meaningfully reject.
func TestUnbalancedDoesNotPanic(t *testing.T) {
	inputs := []string{
		"[",
		"]",
		"{",
		"}",
		"[1, 2",
		"{\"k\": 1",
		"[{]}",
	}
	for _, in := range inputs {
		t.Run(in, func(t *testing.T) {
			_ = Rewrite(in) // must not panic
		})
	}
}

// FuzzRewrite exercises the transform on random inputs. The only
// invariant we enforce is that Rewrite never panics — the output
// is allowed to be any string, because for malformed or weird
// inputs (unbalanced brackets, random punctuation) the only
// guarantee we can make is that we do not crash. Additionally, on
// any input that go/parser.ParseExpr accepted before rewriting, we
// require that it also accepts the rewritten output: that is the
// "rewriting a valid expression never invalidates it" invariant.
func FuzzRewrite(f *testing.F) {
	seeds := []string{
		"", "a", "1 + 2",
		"[1,2,3]", `{"k":1}`,
		"[]", "{}",
		"a[0]", `"text"`,
		"[]any{1}", `map[string]any{"k":1}`,
		"[      ]", // empty array with interior whitespace
		// shapes that previously broke the rewrite
		"[2][]int{{1},{2}}",
		"[3][4]int{{1,2,3,4}}",
		"[]<-chan int{}",
		"func() {}",
		"[][]int{{1,2},{3,4}}",
		"[]struct{X int}{{1},{2}}",
		`map[string]any{"k": {"j": 1}}`,
		`[]any{{"k": 1}}`,
		"List[int]{1, 2}",
		"Box[string]{X: 1}",
		"[/* note */]",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		if len(in) > 4096 {
			return
		}
		out := Rewrite(in) // must not panic
		// If the original input was a valid Go expression, the
		// rewritten output must also be a valid Go expression.
		if _, err := parser.ParseExpr(in); err == nil {
			if _, err := parser.ParseExpr(out); err != nil {
				t.Errorf("valid input became invalid after rewrite:\n  in  %q\n  out %q\n  err %v", in, out, err)
			}
		}
	})
}
