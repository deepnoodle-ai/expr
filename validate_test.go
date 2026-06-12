package expr

import (
	"strings"
	"testing"

	"github.com/deepnoodle-ai/expr/internal/require"
)

// TestValidate_RejectsAtCompile asserts that every unsupported
// syntactic form fails at Compile, not at Run, with an
// ErrCompile-wrapped error whose message names the form. The runtime
// evaluator keeps equivalent rejections as defense in depth, but
// users compiling expressions ahead of time should learn about
// breakage at load time.
func TestValidate_RejectsAtCompile(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string // substring required in the error message
	}{
		// Literals
		{"imag", "1i", "imaginary"},

		// Unary
		{"address_of", "&x", "address-of"},
		{"chan_recv", "<-c", "channel receive"},
		{"bitwise_not", "^x", "bitwise complement"},

		// Binary (bitwise). `|` is the pipe operator, so a non-call
		// right side gets the pipe message rather than a bitwise one
		// (see pipe_test.go).
		{"bitwise_and", "1 & 2", "bitwise"},
		{"pipe_non_call", "1 | 2", "pipe operator | requires a function call"},
		{"bitwise_xor", "1 ^ 2", "bitwise"},
		{"shift_left", "1 << 2", "bitwise"},
		{"shift_right", "1 >> 2", "bitwise"},
		{"and_not", "1 &^ 2", "bitwise"},

		// Calls
		{"spread", "f(xs...)", "spread"},
		{"call_target_index", "fns[0]()", "call target"},
		{"call_target_paren", "(f)(1)", "call target"},
		{"call_target_call", "f()(1)", "call target"},
		{"call_target_optional", "a?.b()", "optional access"},

		// Compound expression nodes
		{"slice_expr", "a[1:2]", "slice expression"},
		{"slice_expr_3", "a[1:2:3]", "slice expression"},
		{"type_assert", "x.(int)", "type assertion"},
		{"pointer_deref", "*p", "pointer dereference"},
		{"func_lit", "func() int { return 1 }", "function literal"},

		// Composite literals
		{"untyped_composite",
			"struct{X int}{X: 1}",
			"composite"},
		{"fixed_size_array", "[3]any{1, 2, 3}", "fixed-size array"},
		{"slice_int", "[]int{1, 2}", "[]any"},
		{"map_int_key", `map[int]any{1: "a"}`, "map[string]any"},
		{"map_int_value", `map[string]int{"a": 1}`, "map[string]any"},
		{"slice_of_struct",
			"[]struct{X int}{{X: 1}}",
			"[]any"},

		// Generics: Go-1.18+ instantiation parses but expr does not
		// accept generic types as composite literal heads.
		{"generic_composite", "List[int]{1, 2}", "composite"},

		// Nested rejection: an unsupported node deep inside a call's
		// argument is still caught.
		{"nested_in_call", "f(1, &x)", "address-of"},
		{"nested_in_index", "a[*p]", "pointer dereference"},
		{"nested_in_composite", `[1, *p, 2]`, "pointer dereference"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Compile(tc.src)
			require.Error(t, err)
			require.ErrorIs(t, err, ErrCompile)
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}

// TestValidate_PositionInError pins the position-in-error-message
// behavior. Errors from go/parser already include a "line:col"
// prefix; the validator does too, so users can locate the offending
// token in long expressions without re-parsing.
func TestValidate_PositionInError(t *testing.T) {
	_, err := Compile("1 + &x + 2")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrCompile)
	if !strings.Contains(err.Error(), "1:5") {
		t.Fatalf("expected position 1:5 in error, got %q", err)
	}
}
