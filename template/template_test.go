package template_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/deepnoodle-ai/expr/template"
	"github.com/deepnoodle-ai/expr/internal/require"
)

// lookupCompiler resolves dotted identifier paths against a
// map[string]any environment. It is deliberately tiny; we only need
// enough behavior to exercise value interpolation, not the full expr
// language. Expression bodies that don't look like dotted idents are
// still accepted so parser tests that never Eval can use this compiler.
type lookupCompiler struct{}

func (lookupCompiler) Compile(code string) (template.Script, error) {
	return &lookupScript{expr: code}, nil
}

type lookupScript struct{ expr string }

func (s *lookupScript) Run(_ context.Context, env any) (any, error) {
	m, ok := env.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("env is not map[string]any")
	}
	var current any = m
	for _, part := range strings.Split(s.expr, ".") {
		mm, ok := current.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("not a map at %q", part)
		}
		v, ok := mm[part]
		if !ok {
			return nil, fmt.Errorf("undefined variable %q", part)
		}
		current = v
	}
	return current, nil
}

// recordingCompiler remembers the exact source strings passed to
// Compile. The resulting scripts are no-ops; these tests don't call
// Eval, they only assert what the parser extracted.
type recordingCompiler struct {
	bodies []string
}

func (c *recordingCompiler) Compile(code string) (template.Script, error) {
	c.bodies = append(c.bodies, code)
	return noopScript{}, nil
}

type noopScript struct{}

func (noopScript) Run(context.Context, any) (any, error) { return nil, nil }

// constCompiler returns a script that produces a fixed value regardless
// of input. Useful for driving Eval paths (empty string, typed values).
type constCompiler struct{ value any }

func (c constCompiler) Compile(string) (template.Script, error) {
	return constScript{value: c.value}, nil
}

type constScript struct{ value any }

func (s constScript) Run(context.Context, any) (any, error) { return s.value, nil }

// errCompiler fails on any Compile call. Used to verify error
// wrapping.
type errCompiler struct{}

var errBoom = errors.New("boom")

func (errCompiler) Compile(string) (template.Script, error) { return nil, errBoom }

func TestTemplate_PlainString(t *testing.T) {
	tmpl, err := template.New(lookupCompiler{}, "Hello World")
	require.NoError(t, err)
	got, err := tmpl.Eval(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, "Hello World", got)
}

func TestTemplate_EmptyString(t *testing.T) {
	tmpl, err := template.New(lookupCompiler{}, "")
	require.NoError(t, err)
	got, err := tmpl.Eval(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, "", got)
}

func TestTemplate_SingleExpression(t *testing.T) {
	tmpl, err := template.New(lookupCompiler{}, "Hello ${state.name}")
	require.NoError(t, err)
	got, err := tmpl.Eval(context.Background(), map[string]any{
		"state": map[string]any{"name": "Alice"},
	})
	require.NoError(t, err)
	require.Equal(t, "Hello Alice", got)
}

func TestTemplate_MultipleExpressions(t *testing.T) {
	tmpl, err := template.New(lookupCompiler{}, "${state.greeting} ${state.name}!")
	require.NoError(t, err)
	got, err := tmpl.Eval(context.Background(), map[string]any{
		"state": map[string]any{"greeting": "Hello", "name": "Bob"},
	})
	require.NoError(t, err)
	require.Equal(t, "Hello Bob!", got)
}

// Regression: the previous implementation matched codes to part slots
// by searching for the first "" entry. If any expression evaluated to
// an empty string, subsequent expressions would overwrite the wrong
// slot. Here we force the middle expression to produce "" and assert
// the surrounding expressions still land in the right place.
func TestTemplate_EmptyStringResultOrderingIsStable(t *testing.T) {
	// We need different values from different expressions. Use a
	// compiler that dispatches on the body.
	c := dispatchCompiler{
		"A": "first",
		"B": "",
		"C": "third",
	}
	tmpl, err := template.New(c, "[${A}][${B}][${C}]")
	require.NoError(t, err)
	got, err := tmpl.Eval(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, "[first][][third]", got)
}

// Regression: expressions that contain `}` inside a nested composite
// literal must not terminate early. With the old regex-based parser
// this would have extracted `map[string]any{"k": 1` as the body.
func TestTemplate_BraceInsideCompositeLiteral(t *testing.T) {
	c := &recordingCompiler{}
	_, err := template.New(c, `x=${ map[string]any{"k": 1}["k"] }`)
	require.NoError(t, err)
	require.Equal(t, []string{`map[string]any{"k": 1}["k"]`}, c.bodies)
}

// Regression: `}` inside a double-quoted string literal must not close
// the expression.
func TestTemplate_BraceInsideStringLiteral(t *testing.T) {
	c := &recordingCompiler{}
	_, err := template.New(c, `${ f("}") }`)
	require.NoError(t, err)
	require.Equal(t, []string{`f("}")`}, c.bodies)
}

// Regression: `}` inside a raw string literal must not close.
func TestTemplate_BraceInsideRawStringLiteral(t *testing.T) {
	c := &recordingCompiler{}
	_, err := template.New(c, "${ f(`}`) }")
	require.NoError(t, err)
	require.Equal(t, []string{"f(`}`)"}, c.bodies)
}

// Regression: `}` inside a rune literal must not close.
func TestTemplate_BraceInsideRuneLiteral(t *testing.T) {
	c := &recordingCompiler{}
	_, err := template.New(c, `${ r == '}' }`)
	require.NoError(t, err)
	require.Equal(t, []string{`r == '}'`}, c.bodies)
}

// Regression: `}` inside a block comment must not close. Comments are
// skipped with mode 0, but the scanner still advances past them
// correctly, so the first post-comment `}` is the real closer.
func TestTemplate_BraceInsideBlockComment(t *testing.T) {
	c := &recordingCompiler{}
	_, err := template.New(c, `${ a /* } */ + b }`)
	require.NoError(t, err)
	require.Equal(t, []string{`a /* } */ + b`}, c.bodies)
}

func TestTemplate_UnclosedExpression(t *testing.T) {
	_, err := template.New(lookupCompiler{}, "Hello ${name")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unclosed `${`")
	require.Contains(t, err.Error(), "offset 6")
	require.Contains(t, err.Error(), "missing `}`")
}

// Regression: the previous open/close counter accepted `${a}} tail ${b`
// because the brace counts happened to match, then silently dropped
// `${b`. The scanner approach catches the second opener.
func TestTemplate_UnclosedAfterValidExpression(t *testing.T) {
	_, err := template.New(lookupCompiler{}, "${a}} tail ${b")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unclosed `${`")
	// Offset should point at the second, unclosed opener, not the first.
	require.Contains(t, err.Error(), "offset 11")
}

// Regression: the previous regex silently accepted `${}` and treated
// the whole template as a constant string.
func TestTemplate_EmptyExpressionIsRejected(t *testing.T) {
	_, err := template.New(lookupCompiler{}, "prefix ${} suffix")
	require.Error(t, err)
	require.Contains(t, err.Error(), "empty expression")
	require.Contains(t, err.Error(), "offset 7")
}

func TestTemplate_WhitespaceOnlyExpressionIsRejected(t *testing.T) {
	_, err := template.New(lookupCompiler{}, "${   }")
	require.Error(t, err)
	require.Contains(t, err.Error(), "empty expression")
}

// Regression: bodies that contain only comments are semantically empty
// but the previous implementation handed them straight to the compiler,
// which produced a confusing "expected operand, found EOF" message.
// Make sure we catch the common cases up front.
func TestTemplate_CommentOnlyExpressionIsRejected(t *testing.T) {
	cases := []string{
		"${ /* just a comment */ }",
		"${ // line comment\n }",
		"${\n/* one */\n// two\n}",
	}
	for _, src := range cases {
		t.Run(src, func(t *testing.T) {
			_, err := template.New(lookupCompiler{}, src)
			require.Error(t, err)
			require.Contains(t, err.Error(), "empty expression")
		})
	}
}

func TestTemplate_DollarDollarEscape(t *testing.T) {
	tmpl, err := template.New(lookupCompiler{}, "price: $${amount}")
	require.NoError(t, err)
	got, err := tmpl.Eval(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, "price: ${amount}", got)
}

func TestTemplate_BareDollarIsLiteral(t *testing.T) {
	tmpl, err := template.New(lookupCompiler{}, "cost is $5 and $ is fine")
	require.NoError(t, err)
	got, err := tmpl.Eval(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, "cost is $5 and $ is fine", got)
}

func TestTemplate_TrailingDollarIsLiteral(t *testing.T) {
	tmpl, err := template.New(lookupCompiler{}, "ends with $")
	require.NoError(t, err)
	got, err := tmpl.Eval(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, "ends with $", got)
}

func TestTemplate_AdjacentExpressions(t *testing.T) {
	tmpl, err := template.New(lookupCompiler{}, "${a.x}${a.y}")
	require.NoError(t, err)
	got, err := tmpl.Eval(context.Background(), map[string]any{
		"a": map[string]any{"x": "AB", "y": "CD"},
	})
	require.NoError(t, err)
	require.Equal(t, "ABCD", got)
}

func TestTemplate_NilValueRendersEmpty(t *testing.T) {
	tmpl, err := template.New(constCompiler{value: nil}, "[${x}]")
	require.NoError(t, err)
	got, err := tmpl.Eval(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, "[]", got)
}

func TestTemplate_IntValueFormattedWithV(t *testing.T) {
	tmpl, err := template.New(constCompiler{value: 42}, "n=${x}")
	require.NoError(t, err)
	got, err := tmpl.Eval(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, "n=42", got)
}

func TestTemplate_CompileErrorIsWrapped(t *testing.T) {
	_, err := template.New(errCompiler{}, "hi ${ x } there")
	require.Error(t, err)
	require.ErrorIs(t, err, errBoom)
	require.Contains(t, err.Error(), "invalid expression")
	require.Contains(t, err.Error(), "offset 3")
	require.Contains(t, err.Error(), "`${x}`")
}

// Runtime errors should point at the exact `${...}` that failed, so
// users can find the problem in a template with many expressions.
func TestTemplate_RuntimeErrorIncludesSourceAndOffset(t *testing.T) {
	tmpl, err := template.New(lookupCompiler{}, "hi ${state.name} there ${missing}!")
	require.NoError(t, err)
	_, err = tmpl.Eval(context.Background(), map[string]any{
		"state": map[string]any{"name": "Alice"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "template:")
	require.Contains(t, err.Error(), "evaluating ${missing}")
	require.Contains(t, err.Error(), "offset 23")
}

func TestTemplate_Raw(t *testing.T) {
	src := "hello ${name}"
	tmpl, err := template.New(lookupCompiler{}, src)
	require.NoError(t, err)
	require.Equal(t, src, tmpl.Raw())
}

// Raw strings can span newlines and contain `}`. The scanner must
// treat the whole backtick block as one token so the internal `}`
// doesn't close the template expression.
func TestTemplate_MultilineRawStringWithBrace(t *testing.T) {
	c := &recordingCompiler{}
	_, err := template.New(c, "${ `line1\n}\nline2` }")
	require.NoError(t, err)
	require.Equal(t, []string{"`line1\n}\nline2`"}, c.bodies)
}

// Line comments extend to end-of-line, so the `}` inside the comment
// must not close the expression. The real closer is after the newline.
func TestTemplate_LineCommentSkipsBraces(t *testing.T) {
	c := &recordingCompiler{}
	_, err := template.New(c, "${ a // } not yet\n + b }")
	require.NoError(t, err)
	require.Equal(t, 1, len(c.bodies))
	require.Contains(t, c.bodies[0], "+ b")
}

// A BOM at the start of the template must not throw off offset
// reporting or swallow content.
func TestTemplate_BOMAtStart(t *testing.T) {
	tmpl, err := template.New(constCompiler{value: "X"}, "\ufeff${x}!")
	require.NoError(t, err)
	got, err := tmpl.Eval(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, "\ufeffX!", got)
}

// Unicode identifiers should be handled by go/scanner without trouble.
func TestTemplate_UnicodeInBody(t *testing.T) {
	c := &recordingCompiler{}
	_, err := template.New(c, "${ café + naïve }")
	require.NoError(t, err)
	require.Equal(t, []string{"café + naïve"}, c.bodies)
}

// Deeply nested braces (e.g. a map of maps) must balance correctly.
func TestTemplate_DeeplyNestedBraces(t *testing.T) {
	body := "map[string]any{\"a\": map[string]any{\"b\": map[string]any{\"c\": 1}}}"
	c := &recordingCompiler{}
	_, err := template.New(c, "${ "+body+" }")
	require.NoError(t, err)
	require.Equal(t, []string{body}, c.bodies)
}

// A stray `}` outside any expression is just literal text.
func TestTemplate_StrayClosingBraceIsLiteral(t *testing.T) {
	tmpl, err := template.New(constCompiler{value: "A"}, "${a}} tail")
	require.NoError(t, err)
	got, err := tmpl.Eval(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, "A} tail", got)
}

// `$$` outside an escape context still collapses to a literal `$`.
// Document this with explicit cases so any future change to the
// escape rule is caught immediately.
func TestTemplate_DollarDollarCollapses(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"$$", "$"},
		{"$$x", "$x"},
		{"$$$", "$$"},   // "$$" → "$", then trailing "$" is literal
		{"$$$$", "$$"},  // two "$$" escapes
		{"a$$b$$c", "a$b$c"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			tmpl, err := template.New(lookupCompiler{}, tc.in)
			require.NoError(t, err)
			got, err := tmpl.Eval(context.Background(), nil)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// An unclosed string literal inside the body must not crash the
// scanner, and it must surface a clear "unclosed" template error.
func TestTemplate_UnclosedStringInBodyReportsUnclosed(t *testing.T) {
	_, err := template.New(lookupCompiler{}, `${ "never ends }`)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unclosed `${`")
}

// Same for a raw string that never closes.
func TestTemplate_UnclosedRawStringInBodyReportsUnclosed(t *testing.T) {
	_, err := template.New(lookupCompiler{}, "${ `never ends }")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unclosed `${`")
}

// Same for a block comment that never closes.
func TestTemplate_UnclosedBlockCommentReportsUnclosed(t *testing.T) {
	_, err := template.New(lookupCompiler{}, "${ /* never ends }")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unclosed `${`")
}

// Many adjacent expressions should all be preserved in order and
// extracted correctly.
func TestTemplate_ManyAdjacentExpressions(t *testing.T) {
	c := dispatchCompiler{
		"a": "1", "b": "2", "c": "3", "d": "4", "e": "5",
	}
	tmpl, err := template.New(c, "${a}${b}${c}${d}${e}")
	require.NoError(t, err)
	got, err := tmpl.Eval(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, "12345", got)
}

// Templates containing only literal text must take the fast path and
// not allocate a builder.
func TestTemplate_ConstantTemplateFastPath(t *testing.T) {
	tmpl, err := template.New(lookupCompiler{}, "no dollars here")
	require.NoError(t, err)
	got, err := tmpl.Eval(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, "no dollars here", got)
}

// Concurrent Eval calls against the same Template must be safe: the
// compiled state is read-only, and the builder is local to each call.
func TestTemplate_ConcurrentEval(t *testing.T) {
	tmpl, err := template.New(constCompiler{value: "X"}, "${a}-${b}-${c}")
	require.NoError(t, err)
	done := make(chan struct{})
	for i := 0; i < 16; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 200; j++ {
				got, err := tmpl.Eval(context.Background(), nil)
				if err != nil || got != "X-X-X" {
					t.Errorf("bad result: %q err=%v", got, err)
					return
				}
			}
		}()
	}
	for i := 0; i < 16; i++ {
		<-done
	}
}

// dispatchCompiler returns a different string for each distinct
// expression body. It's used to verify that the parser preserves
// expression-to-slot ordering even when an expression's result is "".
type dispatchCompiler map[string]string

func (d dispatchCompiler) Compile(code string) (template.Script, error) {
	v, ok := d[strings.TrimSpace(code)]
	if !ok {
		return nil, fmt.Errorf("unknown expr %q", code)
	}
	return constScript{value: v}, nil
}
