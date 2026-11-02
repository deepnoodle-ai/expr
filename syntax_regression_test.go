package expr

import (
	"fmt"
	"strings"
	"testing"

	"github.com/deepnoodle-ai/expr/internal/require"
)

// A multiplication operator after literal indexing must not make the
// JSON-literal rewrite interpret the receiver as a Go array type.
func TestSyntaxRegression_LiteralIndexMultiplication(t *testing.T) {
	for _, source := range []string{
		`[6][0] * 2`,
		`[6][0] * factor`,
		`[[6]][0][0] * 2`,
	} {
		t.Run(source, func(t *testing.T) {
			got, err := evalExpr(t.Context(), source, map[string]any{"factor": int64(2)})
			require.NoError(t, err)
			require.Equal(t, int64(12), got)
		})
	}
}

// Braces belonging to an empty object count as expression content;
// only the template's closing delimiter is outside the expression.
func TestSyntaxRegression_TemplateEmptyObject(t *testing.T) {
	for _, tc := range []struct {
		name   string
		source string
		opts   []Option
	}{
		{name: "default", source: `${{}}`},
		{name: "comment", source: `${ { /* empty object */ } }`},
		{name: "custom_delimiters", source: `${{ {} }}`, opts: []Option{WithTemplateDelimiters("${{", "}}")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmpl, err := NewTemplate(tc.source, tc.opts...)
			require.NoError(t, err)
			got, err := tmpl.Render(t.Context(), nil)
			require.NoError(t, err)
			require.Equal(t, "{}", got)
		})
	}
}

// Supported typed literals are primary expressions too, so optional
// access must work without adding parentheses around the receiver.
func TestSyntaxRegression_OptionalAccessTypedComposite(t *testing.T) {
	for _, tc := range []struct {
		source string
		want   any
	}{
		{`[]any{6}?[0]`, int64(6)},
		{`[]any{}?[0]`, nil},
		{`map[string]any{"a": 1}?.a`, int64(1)},
		{`map[string]any{}?.missing`, nil},
	} {
		t.Run(tc.source, func(t *testing.T) {
			got, err := evalExpr(t.Context(), tc.source, nil)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// Measure preprocessing separately from evaluation. These modest inputs
// expose repeated scanning/copying without a timing-dependent assertion.
func BenchmarkCompileOptionalAccessChain(b *testing.B) {
	for _, count := range []int{100, 500, 1000} {
		b.Run(fmt.Sprintf("operators_%d", count), func(b *testing.B) {
			source := "a" + strings.Repeat("?.a", count)
			b.ReportAllocs()
			b.SetBytes(int64(len(source)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := Compile(source); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
