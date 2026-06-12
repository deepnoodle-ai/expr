package expr

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/deepnoodle-ai/expr/internal/require"
)

func renderTemplate(t *testing.T, src string, env any, opts ...Option) string {
	t.Helper()
	tmpl, err := NewTemplate(src, opts...)
	require.NoError(t, err)
	out, err := tmpl.Render(context.Background(), env)
	require.NoError(t, err)
	return out
}

// Composite values render as compact JSON, not Go map/slice syntax.
func TestTemplate_CompositeValuesRenderAsJSON(t *testing.T) {
	env := map[string]any{
		"config": map[string]any{"retries": int64(3), "timeout": "30s"},
		"files":  []any{"a.go", "b.go"},
		"name":   "alice",
		"n":      int64(42),
	}

	require.Equal(t, `{"retries":3,"timeout":"30s"}`, renderTemplate(t, "${config}", env))
	require.Equal(t, `["a.go","b.go"]`, renderTemplate(t, "${files}", env))
	// Strings and scalars are untouched; nil still renders empty.
	require.Equal(t, "alice", renderTemplate(t, "${name}", env))
	require.Equal(t, "42", renderTemplate(t, "${n}", env))
	require.Equal(t, "", renderTemplate(t, "${missing_thing}", map[string]any{"missing_thing": nil}))
}

// HTML-significant characters survive: the default json.Marshal would
// rewrite & < > as &-style escapes, mangling webhook payloads
// and prompts.
func TestTemplate_JSONDoesNotEscapeHTML(t *testing.T) {
	env := map[string]any{
		"q": map[string]any{"filter": "a&b <c>"},
	}
	require.Equal(t, `{"filter":"a&b <c>"}`, renderTemplate(t, "${q}", env))
}

func TestTemplate_StructsRenderAsJSON(t *testing.T) {
	type point struct {
		X int    `json:"x"`
		Y string `json:"y"`
	}
	env := map[string]any{"p": point{X: 1, Y: "up"}}
	require.Equal(t, `{"x":1,"y":"up"}`, renderTemplate(t, "${p}", env))
}

// A composite that marshals to a JSON string (time.Time, custom
// marshalers) renders unquoted, like any other string.
func TestTemplate_TimeRendersUnquoted(t *testing.T) {
	ts := time.Date(2026, 6, 12, 9, 30, 0, 0, time.UTC)
	env := map[string]any{"at": ts}
	require.Equal(t, "2026-06-12T09:30:00Z", renderTemplate(t, "${at}", env))
}

// Values JSON cannot encode fall back to the previous fmt-style
// formatting rather than erroring.
func TestTemplate_JSONFallbackOnMarshalFailure(t *testing.T) {
	env := map[string]any{
		"bad": map[string]any{"fn": func() {}},
	}
	out := renderTemplate(t, "${bad}", env)
	require.Contains(t, out, "map[")
}

func TestTemplate_WithTemplateFormatter(t *testing.T) {
	env := map[string]any{
		"price": 12.5,
		"label": "total",
		"empty": nil,
	}
	formatter := func(v any) (string, bool) {
		switch x := v.(type) {
		case float64:
			return fmt.Sprintf("%.2f", x), true
		case nil:
			return "N/A", true
		}
		return "", false
	}
	out := renderTemplate(t, "${label}: ${price} (${empty})", env, WithTemplateFormatter(formatter))
	// Floats and nil hit the formatter; the string falls through.
	require.Equal(t, "total: 12.50 (N/A)", out)
}

func TestTemplate_CustomDelimiters(t *testing.T) {
	env := map[string]any{"service": map[string]any{"name": "api"}}

	// ${HOME} is now literal text: shell snippets pass through.
	out := renderTemplate(t, "Deploy ${{ service.name }} via: echo ${HOME}", env,
		WithTemplateDelimiters("${{", "}}"))
	require.Equal(t, "Deploy api via: echo ${HOME}", out)

	// Nested braces inside the body still scan correctly.
	out = renderTemplate(t, `v=${{ {"k": 1}["k"] }}`, nil,
		WithTemplateDelimiters("${{", "}}"))
	require.Equal(t, "v=1", out)

	// $$ escaping continues to work with a $-prefixed opener.
	out = renderTemplate(t, "$${{literal}}", nil, WithTemplateDelimiters("${{", "}}"))
	require.Equal(t, "${{literal}}", out)

	// Openers without $ work too; no escape sequence applies.
	out = renderTemplate(t, "Hello {{ service.name }}, $5 and $$ are plain", env,
		WithTemplateDelimiters("{{", "}}"))
	require.Equal(t, "Hello api, $5 and $$ are plain", out)
}

func TestTemplate_CustomDelimiterErrors(t *testing.T) {
	_, err := NewTemplate("x", WithTemplateDelimiters("", "}"))
	require.ErrorIs(t, err, ErrCompile)
	require.Contains(t, err.Error(), "opener must not be empty")

	_, err = NewTemplate("x", WithTemplateDelimiters("<<", ">>"))
	require.ErrorIs(t, err, ErrCompile)
	require.Contains(t, err.Error(), "must end with `{`")

	_, err = NewTemplate("x", WithTemplateDelimiters("${{", "}"))
	require.ErrorIs(t, err, ErrCompile)
	require.Contains(t, err.Error(), `closer "}" must be "}}"`)

	_, err = NewTemplate("x", WithTemplateDelimiters("$${", "}"))
	require.ErrorIs(t, err, ErrCompile)
	require.Contains(t, err.Error(), "collides with the `$$` escape")

	// Unclosed custom opener names the configured delimiters.
	_, err = NewTemplate("x ${{ y }", WithTemplateDelimiters("${{", "}}"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "unclosed `${{`")
	require.Contains(t, err.Error(), "missing `}}`")

	// The closing brace run must be contiguous.
	_, err = NewTemplate(`${{ {"a": 1} } }`, WithTemplateDelimiters("${{", "}}"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "must be closed by `}}`")
}

// Template-only options passed to Compile fail at load time instead
// of being silently ignored.
func TestCompile_RejectsTemplateOnlyOptions(t *testing.T) {
	_, err := Compile("1 + 1", WithTemplateDelimiters("${{", "}}"))
	require.ErrorIs(t, err, ErrCompile)
	require.Contains(t, err.Error(), "WithTemplateDelimiters applies only to NewTemplate")

	_, err = Compile("1 + 1", WithTemplateFormatter(func(any) (string, bool) { return "", false }))
	require.ErrorIs(t, err, ErrCompile)
	require.Contains(t, err.Error(), "WithTemplateFormatter applies only to NewTemplate")
}

func TestTemplate_ErrorsReportLineColumn(t *testing.T) {
	src := "line one\nline two\n  ${boom} end"
	tmpl, err := NewTemplate(src)
	require.NoError(t, err)
	_, rerr := tmpl.Render(context.Background(), map[string]any{})
	require.Error(t, rerr)
	// ${boom} starts at line 3, byte column 3.
	require.Contains(t, rerr.Error(), "at 3:3")
	require.Contains(t, rerr.Error(), "evaluating ${boom}")

	// Parse-time errors carry line:column too.
	_, err = NewTemplate("ok\n${}")
	require.Error(t, err)
	require.Contains(t, err.Error(), "empty expression `${}` at 2:1")

	_, err = NewTemplate("a\nb ${x +}")
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid expression `${x +}` at 2:3")

	_, err = NewTemplate("\n\n${open")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unclosed `${` at 3:1")
}

func TestTemplate_Segments(t *testing.T) {
	src := "Hi ${user.name},\nyou have ${count} items"
	tmpl, err := NewTemplate(src)
	require.NoError(t, err)

	segs := tmpl.Segments()
	require.Len(t, segs, 5)

	require.Equal(t, "Hi ", segs[0].Literal)
	require.Equal(t, 0, segs[0].Offset)
	require.Equal(t, 1, segs[0].Line)
	require.Equal(t, 1, segs[0].Column)
	require.Nil(t, segs[0].Program)

	require.Equal(t, "user.name", segs[1].Source)
	require.Equal(t, 3, segs[1].Offset)
	require.Equal(t, 1, segs[1].Line)
	require.Equal(t, 4, segs[1].Column)
	require.NotNil(t, segs[1].Program)
	require.Equal(t, []string{"user"}, segs[1].Program.Identifiers())

	require.Equal(t, ",\nyou have ", segs[2].Literal)

	require.Equal(t, "count", segs[3].Source)
	require.Equal(t, 2, segs[3].Line)
	require.Equal(t, 10, segs[3].Column)
	require.Equal(t, []string{"count"}, segs[3].Program.Identifiers())

	require.Equal(t, " items", segs[4].Literal)
}

// Literal segments record where they started even when `$$` escapes
// make the decoded text shorter than the raw span.
func TestTemplate_SegmentsWithEscapes(t *testing.T) {
	tmpl, err := NewTemplate("a $$ b ${x}")
	require.NoError(t, err)
	segs := tmpl.Segments()
	require.Len(t, segs, 2)
	require.Equal(t, "a $ b ", segs[0].Literal)
	require.Equal(t, 0, segs[0].Offset)
	require.Equal(t, "x", segs[1].Source)
	require.Equal(t, 7, segs[1].Offset)
}

func TestTemplate_SegmentsConstantTemplate(t *testing.T) {
	tmpl, err := NewTemplate("no expressions here")
	require.NoError(t, err)
	segs := tmpl.Segments()
	require.Len(t, segs, 1)
	require.Equal(t, "no expressions here", segs[0].Literal)
	require.Equal(t, 1, segs[0].Line)
	require.Equal(t, 1, segs[0].Column)
}

// Multiline bodies inside custom delimiters keep scanning across
// lines, and the segment position points at the opener.
func TestTemplate_CustomDelimiterMultiline(t *testing.T) {
	src := "header\n${{\n  join(names, \", \")\n}}\nfooter"
	tmpl, err := NewTemplate(src,
		WithTemplateDelimiters("${{", "}}"),
		WithFunctions(StringFuncs()))
	require.NoError(t, err)
	out, err := tmpl.Render(context.Background(), map[string]any{
		"names": []any{"a", "b"},
	})
	require.NoError(t, err)
	require.Equal(t, "header\na, b\nfooter", out)

	segs := tmpl.Segments()
	require.Len(t, segs, 3)
	require.Equal(t, 2, segs[1].Line)
	require.Equal(t, 1, segs[1].Column)
	require.True(t, strings.HasPrefix(segs[1].Source, "join"))
}
