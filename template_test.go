package expr

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/deepnoodle-ai/expr/internal/require"
)

// lookupCompile resolves dotted identifier paths against a
// map[string]any environment. It is deliberately tiny; we only need
// enough behavior to exercise value interpolation, not the full expr
// language. Expression bodies that don't look like dotted idents are
// still accepted so parser tests that never Eval can use this compile.
func lookupCompile(code string) (runner, error) {
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

// recordingCompile remembers the exact source strings passed to
// compile. The resulting scripts are no-ops; these tests don't call
// Eval, they only assert what the parser extracted.
type recordingCompile struct {
	bodies []string
}

func (c *recordingCompile) Compile(code string) (runner, error) {
	c.bodies = append(c.bodies, code)
	return noopScript{}, nil
}

type noopScript struct{}

func (noopScript) Run(context.Context, any) (any, error) { return nil, nil }

// constCompile returns a script that produces a fixed value regardless
// of input. Useful for driving Eval paths (empty string, typed values).
func constCompile(value any) func(string) (runner, error) {
	return func(string) (runner, error) { return constScript{value: value}, nil }
}

type constScript struct{ value any }

func (s constScript) Run(context.Context, any) (any, error) { return s.value, nil }

// errCompile fails on any Compile call. Used to verify error wrapping.
var errBoom = errors.New("boom")

func errCompile(string) (runner, error) { return nil, errBoom }

func TestTemplate_PlainString(t *testing.T) {
	tmpl, err := parseTemplate("Hello World", lookupCompile)
	require.NoError(t, err)
	got, err := tmpl.Render(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, "Hello World", got)
}

func TestTemplate_EmptyString(t *testing.T) {
	tmpl, err := parseTemplate("", lookupCompile)
	require.NoError(t, err)
	got, err := tmpl.Render(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, "", got)
}

func TestTemplate_SingleExpression(t *testing.T) {
	tmpl, err := parseTemplate("Hello ${state.name}", lookupCompile)
	require.NoError(t, err)
	got, err := tmpl.Render(context.Background(), map[string]any{
		"state": map[string]any{"name": "Alice"},
	})
	require.NoError(t, err)
	require.Equal(t, "Hello Alice", got)
}

func TestTemplate_MultipleExpressions(t *testing.T) {
	tmpl, err := parseTemplate("${state.greeting} ${state.name}!", lookupCompile)
	require.NoError(t, err)
	got, err := tmpl.Render(context.Background(), map[string]any{
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
	c := dispatchCompile{
		"A": "first",
		"B": "",
		"C": "third",
	}
	tmpl, err := parseTemplate("[${A}][${B}][${C}]", c.Compile)
	require.NoError(t, err)
	got, err := tmpl.Render(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, "[first][][third]", got)
}

// Regression: expressions that contain `}` inside a nested composite
// literal must not terminate early. With the old regex-based parser
// this would have extracted `map[string]any{"k": 1` as the body.
func TestTemplate_BraceInsideCompositeLiteral(t *testing.T) {
	c := &recordingCompile{}
	_, err := parseTemplate(`x=${ map[string]any{"k": 1}["k"] }`, c.Compile)
	require.NoError(t, err)
	require.Equal(t, []string{`map[string]any{"k": 1}["k"]`}, c.bodies)
}

// Regression: `}` inside a double-quoted string literal must not close
// the expression.
func TestTemplate_BraceInsideStringLiteral(t *testing.T) {
	c := &recordingCompile{}
	_, err := parseTemplate(`${ f("}") }`, c.Compile)
	require.NoError(t, err)
	require.Equal(t, []string{`f("}")`}, c.bodies)
}

// Regression: `}` inside a raw string literal must not close.
func TestTemplate_BraceInsideRawStringLiteral(t *testing.T) {
	c := &recordingCompile{}
	_, err := parseTemplate("${ f(`}`) }", c.Compile)
	require.NoError(t, err)
	require.Equal(t, []string{"f(`}`)"}, c.bodies)
}

// Regression: `}` inside a rune literal must not close.
func TestTemplate_BraceInsideRuneLiteral(t *testing.T) {
	c := &recordingCompile{}
	_, err := parseTemplate(`${ r == '}' }`, c.Compile)
	require.NoError(t, err)
	require.Equal(t, []string{`r == '}'`}, c.bodies)
}

// Regression: `}` inside a block comment must not close. Comments are
// skipped with mode 0, but the scanner still advances past them
// correctly, so the first post-comment `}` is the real closer.
func TestTemplate_BraceInsideBlockComment(t *testing.T) {
	c := &recordingCompile{}
	_, err := parseTemplate(`${ a /* } */ + b }`, c.Compile)
	require.NoError(t, err)
	require.Equal(t, []string{`a /* } */ + b`}, c.bodies)
}

func TestTemplate_UnclosedExpression(t *testing.T) {
	_, err := parseTemplate("Hello ${name", lookupCompile)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unclosed `${`")
	require.Contains(t, err.Error(), "offset 6")
	require.Contains(t, err.Error(), "missing `}`")
}

// Regression: the previous open/close counter accepted `${a}} tail ${b`
// because the brace counts happened to match, then silently dropped
// `${b`. The scanner approach catches the second opener.
func TestTemplate_UnclosedAfterValidExpression(t *testing.T) {
	_, err := parseTemplate("${a}} tail ${b", lookupCompile)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unclosed `${`")
	// Offset should point at the second, unclosed opener, not the first.
	require.Contains(t, err.Error(), "offset 11")
}

// Regression: the previous regex silently accepted `${}` and treated
// the whole template as a constant string.
func TestTemplate_EmptyExpressionIsRejected(t *testing.T) {
	_, err := parseTemplate("prefix ${} suffix", lookupCompile)
	require.Error(t, err)
	require.Contains(t, err.Error(), "empty expression")
	require.Contains(t, err.Error(), "offset 7")
}

func TestTemplate_WhitespaceOnlyExpressionIsRejected(t *testing.T) {
	_, err := parseTemplate("${   }", lookupCompile)
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
			_, err := parseTemplate(src, lookupCompile)
			require.Error(t, err)
			require.Contains(t, err.Error(), "empty expression")
		})
	}
}

func TestTemplate_DollarDollarEscape(t *testing.T) {
	tmpl, err := parseTemplate("price: $${amount}", lookupCompile)
	require.NoError(t, err)
	got, err := tmpl.Render(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, "price: ${amount}", got)
}

func TestTemplate_BareDollarIsLiteral(t *testing.T) {
	tmpl, err := parseTemplate("cost is $5 and $ is fine", lookupCompile)
	require.NoError(t, err)
	got, err := tmpl.Render(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, "cost is $5 and $ is fine", got)
}

func TestTemplate_TrailingDollarIsLiteral(t *testing.T) {
	tmpl, err := parseTemplate("ends with $", lookupCompile)
	require.NoError(t, err)
	got, err := tmpl.Render(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, "ends with $", got)
}

func TestTemplate_AdjacentExpressions(t *testing.T) {
	tmpl, err := parseTemplate("${a.x}${a.y}", lookupCompile)
	require.NoError(t, err)
	got, err := tmpl.Render(context.Background(), map[string]any{
		"a": map[string]any{"x": "AB", "y": "CD"},
	})
	require.NoError(t, err)
	require.Equal(t, "ABCD", got)
}

func TestTemplate_NilValueRendersEmpty(t *testing.T) {
	tmpl, err := parseTemplate("[${x}]", constCompile(nil))
	require.NoError(t, err)
	got, err := tmpl.Render(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, "[]", got)
}

func TestTemplate_IntValueFormattedWithV(t *testing.T) {
	tmpl, err := parseTemplate("n=${x}", constCompile(42))
	require.NoError(t, err)
	got, err := tmpl.Render(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, "n=42", got)
}

func TestTemplate_CompileErrorIsWrapped(t *testing.T) {
	_, err := parseTemplate("hi ${ x } there", errCompile)
	require.Error(t, err)
	require.ErrorIs(t, err, errBoom)
	require.Contains(t, err.Error(), "invalid expression")
	require.Contains(t, err.Error(), "offset 3")
	require.Contains(t, err.Error(), "`${x}`")
}

// Runtime errors should point at the exact `${...}` that failed, so
// users can find the problem in a template with many expressions.
func TestTemplate_RuntimeErrorIncludesSourceAndOffset(t *testing.T) {
	tmpl, err := parseTemplate("hi ${state.name} there ${missing}!", lookupCompile)
	require.NoError(t, err)
	_, err = tmpl.Render(context.Background(), map[string]any{
		"state": map[string]any{"name": "Alice"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "template:")
	require.Contains(t, err.Error(), "evaluating ${missing}")
	require.Contains(t, err.Error(), "offset 23")
}

func TestTemplate_Source(t *testing.T) {
	src := "hello ${name}"
	tmpl, err := parseTemplate(src, lookupCompile)
	require.NoError(t, err)
	require.Equal(t, src, tmpl.Source())
}

// Raw strings can span newlines and contain `}`. The scanner must
// treat the whole backtick block as one token so the internal `}`
// doesn't close the template expression.
func TestTemplate_MultilineRawStringWithBrace(t *testing.T) {
	c := &recordingCompile{}
	_, err := parseTemplate("${ `line1\n}\nline2` }", c.Compile)
	require.NoError(t, err)
	require.Equal(t, []string{"`line1\n}\nline2`"}, c.bodies)
}

// Line comments extend to end-of-line, so the `}` inside the comment
// must not close the expression. The real closer is after the newline.
func TestTemplate_LineCommentSkipsBraces(t *testing.T) {
	c := &recordingCompile{}
	_, err := parseTemplate("${ a // } not yet\n + b }", c.Compile)
	require.NoError(t, err)
	require.Equal(t, 1, len(c.bodies))
	require.Contains(t, c.bodies[0], "+ b")
}

// A BOM at the start of the template must not throw off offset
// reporting or swallow content.
func TestTemplate_BOMAtStart(t *testing.T) {
	tmpl, err := parseTemplate("\ufeff${x}!", constCompile("X"))
	require.NoError(t, err)
	got, err := tmpl.Render(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, "\ufeffX!", got)
}

// Unicode identifiers should be handled by go/scanner without trouble.
func TestTemplate_UnicodeInBody(t *testing.T) {
	c := &recordingCompile{}
	_, err := parseTemplate("${ café + naïve }", c.Compile)
	require.NoError(t, err)
	require.Equal(t, []string{"café + naïve"}, c.bodies)
}

// Deeply nested braces (e.g. a map of maps) must balance correctly.
func TestTemplate_DeeplyNestedBraces(t *testing.T) {
	body := "map[string]any{\"a\": map[string]any{\"b\": map[string]any{\"c\": 1}}}"
	c := &recordingCompile{}
	_, err := parseTemplate("${ "+body+" }", c.Compile)
	require.NoError(t, err)
	require.Equal(t, []string{body}, c.bodies)
}

// A stray `}` outside any expression is just literal text.
func TestTemplate_StrayClosingBraceIsLiteral(t *testing.T) {
	tmpl, err := parseTemplate("${a}} tail", constCompile("A"))
	require.NoError(t, err)
	got, err := tmpl.Render(context.Background(), nil)
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
		{"$$$", "$$"},  // "$$" → "$", then trailing "$" is literal
		{"$$$$", "$$"}, // two "$$" escapes
		{"a$$b$$c", "a$b$c"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			tmpl, err := parseTemplate(tc.in, lookupCompile)
			require.NoError(t, err)
			got, err := tmpl.Render(context.Background(), nil)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// An unclosed string literal inside the body must not crash the
// scanner, and it must surface a clear "unclosed" template error.
func TestTemplate_UnclosedStringInBodyReportsUnclosed(t *testing.T) {
	_, err := parseTemplate(`${ "never ends }`, lookupCompile)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unclosed `${`")
}

// Same for a raw string that never closes.
func TestTemplate_UnclosedRawStringInBodyReportsUnclosed(t *testing.T) {
	_, err := parseTemplate("${ `never ends }", lookupCompile)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unclosed `${`")
}

// Same for a block comment that never closes.
func TestTemplate_UnclosedBlockCommentReportsUnclosed(t *testing.T) {
	_, err := parseTemplate("${ /* never ends }", lookupCompile)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unclosed `${`")
}

// Many adjacent expressions should all be preserved in order and
// extracted correctly.
func TestTemplate_ManyAdjacentExpressions(t *testing.T) {
	c := dispatchCompile{
		"a": "1", "b": "2", "c": "3", "d": "4", "e": "5",
	}
	tmpl, err := parseTemplate("${a}${b}${c}${d}${e}", c.Compile)
	require.NoError(t, err)
	got, err := tmpl.Render(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, "12345", got)
}

// Templates containing only literal text must take the fast path and
// not allocate a builder.
func TestTemplate_ConstantTemplateFastPath(t *testing.T) {
	tmpl, err := parseTemplate("no dollars here", lookupCompile)
	require.NoError(t, err)
	got, err := tmpl.Render(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, "no dollars here", got)
}

// Concurrent Eval calls against the same Template must be safe: the
// compiled state is read-only, and the builder is local to each call.
func TestTemplate_ConcurrentEval(t *testing.T) {
	tmpl, err := parseTemplate("${a}-${b}-${c}", constCompile("X"))
	require.NoError(t, err)
	done := make(chan struct{})
	for i := 0; i < 16; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 200; j++ {
				got, err := tmpl.Render(context.Background(), nil)
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

// NewTemplate exercises the public entry point against the real expr
// compiler so the wiring from opts → parseTemplate is covered.
func TestNewTemplate_WithBuiltins(t *testing.T) {
	tmpl, err := NewTemplate("Hello ${upper(name)}!", WithBuiltins())
	require.NoError(t, err)
	got, err := tmpl.Render(context.Background(), map[string]any{"name": "ada"})
	require.NoError(t, err)
	require.Equal(t, "Hello ADA!", got)
}

// dispatchCompile returns a different string for each distinct
// expression body. It's used to verify that the parser preserves
// expression-to-slot ordering even when an expression's result is "".
type dispatchCompile map[string]string

func (d dispatchCompile) Compile(code string) (runner, error) {
	v, ok := d[strings.TrimSpace(code)]
	if !ok {
		return nil, fmt.Errorf("unknown expr %q", code)
	}
	return constScript{value: v}, nil
}
