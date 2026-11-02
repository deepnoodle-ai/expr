package expr

import (
	"fmt"
	"strings"
	"testing"
	"time"

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

// Compile errors used to report positions in the rewritten source, so
// every byte a rewrite inserted (`map[string]any`, `__try_select__(`,
// `__expr_kw_`) shifted the column: `x.type ^ 1` reported 1:18.
func TestSyntaxRegression_ErrorPositionsAfterRewrites(t *testing.T) {
	for _, tc := range []struct{ source, want string }{
		{`{"a" 1}`, "1:6: missing ','"},
		{`[1, 2 3]`, "1:7: missing ','"},
		{`x.type ^ 1`, "1:8: bitwise operator ^"},
		{`x?.type & 1`, "1:9: bitwise operator &"},
		{`if(c, 1) ^ 2`, "1:10: bitwise operator ^"},
		{`[1, 2] | 3`, "1:8: pipe operator |"},
		{"x?.a?.b +\n  [1][0:1]", "2:6: slice expressions"},
		{`{"a": x?.b}.a $`, "1:15: illegal character"},
	} {
		t.Run(tc.source, func(t *testing.T) {
			_, err := Compile(tc.source)
			require.ErrorIs(t, err, ErrCompile)
			require.Contains(t, err.Error(), tc.want)
		})
	}
	// Template segments report positions within the segment.
	_, err := NewTemplate(`a ${ {"k" 1} } b`)
	require.ErrorIs(t, err, ErrCompile)
	require.Contains(t, err.Error(), "1:6: missing ','")
}

// jsonlit rescanned the contents of every nested `[` for a top-level
// comma, so a deeply nested array literal took quadratic time: ~1.8s to
// rewrite at MaxSourceLength. One pass now marks every bracket.
func TestSyntaxRegression_NestedArrayLiteralLinear(t *testing.T) {
	n := (MaxSourceLength - 1) / 2
	src := strings.Repeat("[", n) + "1" + strings.Repeat("]", n)
	start := time.Now()
	rewriteSource(src)
	if d := time.Since(start); d > time.Second {
		t.Fatalf("rewriting %d nested literals took %v", n, d)
	}
}

// Parsing a template copied the rest of the source for every `${`, and
// counted lines from the start for every segment, so a template with many
// segments allocated quadratically (209 MB for 64 KiB of `${x?.a}`).
func TestSyntaxRegression_TemplateParseLinear(t *testing.T) {
	build := func(n int) string { return strings.Repeat("a ${x} b\n", n) }
	small := allocatedBytes(func() { _, _ = NewTemplate(build(2000)) })
	big := allocatedBytes(func() { _, _ = NewTemplate(build(8000)) })
	if ratio := float64(big) / float64(small); ratio > 5 {
		t.Fatalf("4x the segments allocated x%.1f", ratio)
	}
	// Positions are still exact.
	tmpl, err := NewTemplate("a\n  ${x} ${y}")
	require.NoError(t, err)
	segs := tmpl.Segments()
	require.Equal(t, 2, segs[1].Line)
	require.Equal(t, 3, segs[1].Column)
	require.Equal(t, 8, segs[3].Column)
}
