package expr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/scanner"
	"go/token"
	"reflect"
	"strings"
)

// defaultTemplateOpen and defaultTemplateClose are the delimiters
// used when WithTemplateDelimiters is not supplied.
const (
	defaultTemplateOpen  = "${"
	defaultTemplateClose = "}"
)

// runner is the minimal interface templateSegment needs to evaluate a
// compiled expression. *Program is the only production implementation;
// the interface exists so template tests can drive parseTemplate with
// mock scripts that never touch the real engine.
type runner interface {
	Run(ctx context.Context, env any) (any, error)
}

// Template is a pre-compiled `${...}` string interpolator. Each
// expression inside the template is compiled once by [NewTemplate] and
// re-evaluated per call.
//
// Parsing uses go/scanner to find the closing `}` of each expression,
// so expression bodies may contain braces inside string, rune, and
// comment tokens as well as nested composite literals like
// `map[string]any{"k": 1}` without tripping the template parser.
//
// Escaping: `$$` is always rewritten to a literal `$`. `$${name}`
// therefore emits the literal text `${name}`. A bare `$` that is not
// followed by `$` or `{` is emitted verbatim, so `$5` or `$foo` pass
// through unchanged.
//
// The `${` / `}` delimiters can be replaced per template with
// [WithTemplateDelimiters]; the `$$` escape applies only when the
// configured opener starts with `$`.
type Template struct {
	raw       string
	segments  []templateSegment
	open      string
	close     string
	formatter func(v any) (string, bool)
}

// templateSegment is either a literal chunk of the source (script ==
// nil) or a compiled expression to evaluate at runtime. For script
// segments, source and offset describe the original `${...}` body and
// the starting byte offset of its opening delimiter in the raw
// template; line and column are the 1-based position of that offset,
// so runtime errors can point at the exact expression that failed.
type templateSegment struct {
	literal string
	script  runner
	source  string
	offset  int
	line    int
	column  int
}

// TemplateSegment is the public projection of one parsed template
// segment, exposed by [Template.Segments]. A segment is either a
// literal run of text (Literal non-empty, Source empty) or an
// interpolated expression (Source holds the expression body).
//
// Offset is the byte offset of the segment's start in the raw
// template: the first byte of the literal text, or the first byte of
// the opening delimiter for expression segments. Line and Column are
// the 1-based line and byte-based column of that offset. For literal
// segments containing `$$` escapes, Literal holds the decoded text,
// which may be shorter than the raw source it spans.
type TemplateSegment struct {
	Literal string
	Source  string
	Offset  int
	Line    int
	Column  int
	// Program is the compiled expression for expression segments,
	// nil for literal segments. Hosts can call Identifiers() on it
	// for per-segment variable extraction, editor hints, or live
	// validation.
	Program *Program
}

// WithTemplateDelimiters replaces the default `${` / `}` expression
// delimiters for a [NewTemplate] call. The opener must end with at
// least one `{` and the closer must be the matching run of `}`:
//
//	tmpl, err := expr.NewTemplate(src, expr.WithTemplateDelimiters("${{", "}}"))
//
// A GitHub-Actions-style `${{ expr }}` opener avoids collisions with
// shell parameter expansion and JavaScript template literals, so text
// like `echo ${HOME}` passes through as a literal. The `$$` escape
// applies only when the opener starts with `$`.
//
// The option applies only to NewTemplate; passing it to [Compile]
// fails with ErrCompile.
func WithTemplateDelimiters(open, close string) Option {
	return func(c *compileConfig) {
		c.templateOnly = append(c.templateOnly, "WithTemplateDelimiters")
		braces := trailingBraces(open)
		switch {
		case open == "":
			c.errs = append(c.errs, errors.New("WithTemplateDelimiters: opener must not be empty"))
		case braces == 0:
			c.errs = append(c.errs, fmt.Errorf("WithTemplateDelimiters: opener %q must end with `{`", open))
		case strings.Contains(open, "$$"):
			c.errs = append(c.errs, fmt.Errorf("WithTemplateDelimiters: opener %q collides with the `$$` escape", open))
		case close != strings.Repeat("}", braces):
			c.errs = append(c.errs, fmt.Errorf("WithTemplateDelimiters: closer %q must be %q to match opener %q",
				close, strings.Repeat("}", braces), open))
		default:
			c.tmplOpen = open
			c.tmplClose = close
		}
	}
}

// WithTemplateFormatter installs a custom value renderer for a
// [NewTemplate] call. It runs first for every interpolated result,
// including nil and strings; returning false falls through to the
// default rendering chain (nil to empty, strings pass through,
// composites to JSON, everything else fmt-style).
//
//	expr.WithTemplateFormatter(func(v any) (string, bool) {
//	    if t, ok := v.(time.Time); ok {
//	        return t.Format(time.Stamp), true
//	    }
//	    return "", false
//	})
//
// The option applies only to NewTemplate; passing it to [Compile]
// fails with ErrCompile.
func WithTemplateFormatter(fn func(v any) (string, bool)) Option {
	return func(c *compileConfig) {
		c.templateOnly = append(c.templateOnly, "WithTemplateFormatter")
		c.tmplFormatter = fn
	}
}

// trailingBraces counts the `{` run at the end of s.
func trailingBraces(s string) int {
	n := 0
	for i := len(s) - 1; i >= 0 && s[i] == '{'; i-- {
		n++
	}
	return n
}

// NewTemplate parses raw and pre-compiles every `${...}` expression
// with the given Options. Strings without any `${...}` are
// accepted and become constant templates that return raw unchanged
// from [Template.Render].
func NewTemplate(raw string, opts ...Option) (*Template, error) {
	cfg := newCompileConfig()
	for _, opt := range opts {
		opt(cfg)
	}
	if len(cfg.errs) > 0 {
		return nil, fmt.Errorf("%w: %w", ErrCompile, errors.Join(cfg.errs...))
	}
	open, close := cfg.tmplOpen, cfg.tmplClose
	if open == "" {
		open, close = defaultTemplateOpen, defaultTemplateClose
	}
	t, err := parseTemplateWith(raw, open, close, func(code string) (runner, error) {
		return compileWithConfig(code, cfg)
	})
	if err != nil {
		return nil, err
	}
	t.formatter = cfg.tmplFormatter
	return t, nil
}

// Source returns the unparsed template source.
func (t *Template) Source() string { return t.raw }

// Segments returns the parsed segments of the template in source
// order: literal runs and compiled expressions, each carrying its
// offset and line:column position in the raw source. Hosts use it for
// syntax highlighting, per-expression variable extraction (via
// TemplateSegment.Program's Identifiers method), and live validation,
// without re-implementing the template scanner.
func (t *Template) Segments() []TemplateSegment {
	out := make([]TemplateSegment, 0, len(t.segments))
	for _, seg := range t.segments {
		s := TemplateSegment{
			Literal: seg.literal,
			Source:  seg.source,
			Offset:  seg.offset,
			Line:    seg.line,
			Column:  seg.column,
		}
		if p, ok := seg.script.(*Program); ok {
			s.Program = p
		}
		out = append(out, s)
	}
	return out
}

// Render evaluates each `${...}` expression against env and
// concatenates the results with the surrounding literal text. env
// follows the same rules as [Program.Run]: it may be a map[string]any,
// a struct, or a pointer to a struct. Templates with no expressions
// return the raw source unchanged without invoking any script.
//
// Value rendering rules for each `${...}` result (a formatter
// installed with [WithTemplateFormatter] runs first and can override
// any of them):
//
//   - nil renders as the empty string. Optional fields that resolve
//     to nil silently produce no output rather than the literal
//     "<nil>". This matches the convention used by Jinja, Liquid,
//     Handlebars, and most text-oriented interpolators, and means
//     callers do not need a null-coalescing operator for the common
//     case of optional values.
//   - string values pass through unchanged.
//   - maps, slices, arrays, and structs render as compact JSON with
//     HTML escaping disabled, so `${config}` interpolates as
//     `{"retries":3}` rather than Go's map syntax. A composite that
//     marshals to a JSON string (time.Time, custom marshalers)
//     renders as the string itself, unquoted. Values JSON cannot
//     represent (cycles, channels, funcs) fall back to the fmt-style
//     formatting used for scalars.
//   - Everything else is formatted with fmt.Sprintf("%v", v).
//
// The nil-to-empty rule means a template cannot distinguish "value
// was nil" from "value was the empty string" in its output. Callers
// that need that distinction should wrap the expression in something
// that returns a sentinel.
func (t *Template) Render(ctx context.Context, env any) (string, error) {
	// Fast path: no expressions means the raw string is the answer.
	if len(t.segments) == 1 && t.segments[0].script == nil {
		return t.segments[0].literal, nil
	}

	var b strings.Builder
	b.Grow(len(t.raw))
	for _, seg := range t.segments {
		if seg.script == nil {
			b.WriteString(seg.literal)
			continue
		}
		v, err := seg.script.Run(ctx, env)
		if err != nil {
			return "", fmt.Errorf("template: evaluating %s%s%s at %d:%d (offset %d): %w",
				t.open, seg.source, t.close, seg.line, seg.column, seg.offset, err)
		}
		b.WriteString(t.formatValue(v))
	}
	return b.String(), nil
}

// formatValue renders a Run result for interpolation per the rules
// documented on Render.
func (t *Template) formatValue(v any) string {
	if t.formatter != nil {
		if s, ok := t.formatter(v); ok {
			return s
		}
	}
	return formatTemplateValue(v)
}

// formatTemplateValue is the default rendering chain: nil becomes the
// empty string, strings pass through unchanged, composite values
// marshal to compact JSON, and everything else (or a value JSON
// cannot encode) uses Go's default formatting.
func formatTemplateValue(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	}
	if isCompositeKind(v) {
		if s, ok := marshalTemplateJSON(v); ok {
			return s
		}
	}
	return safeFormatValue(v)
}

// isCompositeKind reports whether v is a map, slice, array, or struct
// (following pointers), the shapes that render as JSON in templates.
func isCompositeKind(v any) bool {
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return false
		}
		rv = rv.Elem()
	}
	switch rv.Kind() {
	case reflect.Map, reflect.Slice, reflect.Array, reflect.Struct:
		return true
	}
	return false
}

// marshalTemplateJSON renders v as compact JSON with HTML escaping
// disabled, so `&`, `<`, and `>` survive intact in webhook payloads
// and prompts. A result that is itself a JSON string (a composite
// with a custom marshaler, like time.Time) is unquoted so it renders
// like any other string. ok is false when v cannot be marshaled
// (cycles, channels, funcs, NaN) and the caller should fall back.
func marshalTemplateJSON(v any) (string, bool) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", false
	}
	out := strings.TrimSuffix(buf.String(), "\n")
	if len(out) >= 2 && out[0] == '"' {
		var s string
		if err := json.Unmarshal([]byte(out), &s); err == nil {
			return s, true
		}
	}
	return out, true
}

// parseTemplate walks raw once with the default `${` / `}` delimiters,
// emitting literal segments for plain text and compiling each `${...}`
// body via the supplied compile function. It preserves a single literal
// segment for constant templates so Render can take the fast path. The
// compile parameter is a function so tests can drive the parser with
// mock compile functions that never touch the real engine.
func parseTemplate(raw string, compile func(string) (runner, error)) (*Template, error) {
	return parseTemplateWith(raw, defaultTemplateOpen, defaultTemplateClose, compile)
}

// parseTemplateWith is parseTemplate generalized over the expression
// delimiters. open must end with one or more `{` and close must be
// the matching `}` run; WithTemplateDelimiters validates this before
// any caller reaches here.
func parseTemplateWith(raw, open, close string, compile func(string) (runner, error)) (*Template, error) {
	segs, err := parseTemplateSegments(raw, open, close, compile)
	if err != nil {
		return nil, err
	}
	return &Template{raw: raw, segments: segs, open: open, close: close}, nil
}

func parseTemplateSegments(raw, open, close string, compile func(string) (runner, error)) ([]templateSegment, error) {
	if raw == "" {
		return []templateSegment{{literal: "", line: 1, column: 1}}, nil
	}

	braces := trailingBraces(open)
	// The `$$` escape only exists for `$`-prefixed openers; hosts that
	// switch to delimiters like `{{` chose them to avoid `$` entirely.
	escape := ""
	if open[0] == '$' {
		escape = "$$"
	}

	var segs []templateSegment
	var lit strings.Builder
	litStart := 0
	appendLit := func(s string, at int) {
		if lit.Len() == 0 {
			litStart = at
		}
		lit.WriteString(s)
	}
	flushLit := func() {
		if lit.Len() > 0 {
			line, col := templateLineCol(raw, litStart)
			segs = append(segs, templateSegment{literal: lit.String(), offset: litStart, line: line, column: col})
			lit.Reset()
		}
	}

	i := 0
	for i < len(raw) {
		// Find whichever comes first: the escape sequence or the
		// opening delimiter. Everything before it is literal text, as
		// is the entire tail when neither occurs again. The escape
		// wins when both match at the same region (`$${` is an
		// escaped `$` followed by plain text), which is what makes
		// `$${name}` emit the literal `${name}`.
		openIdx := strings.Index(raw[i:], open)
		escIdx := -1
		if escape != "" {
			escIdx = strings.Index(raw[i:], escape)
		}
		if openIdx < 0 && escIdx < 0 {
			appendLit(raw[i:], i)
			break
		}
		useEscape := escIdx >= 0 && (openIdx < 0 || escIdx <= openIdx)
		cut := openIdx
		if useEscape {
			cut = escIdx
		}
		appendLit(raw[i:i+cut], i)
		i += cut
		if useEscape {
			appendLit("$", i)
			i += 2
			continue
		}

		// Expression opener. Flush any buffered literal, locate the
		// matching close, compile the body, emit a script segment.
		flushLit()
		openOffset := i
		exprStart := i + len(open)
		bodyEnd, hadContent, err := scanTemplateExprEnd(raw, exprStart, openOffset, open, close, braces)
		if err != nil {
			return nil, err
		}
		line, col := templateLineCol(raw, openOffset)
		body := strings.TrimSpace(raw[exprStart:bodyEnd])
		if body == "" || !hadContent {
			return nil, fmt.Errorf("template: empty expression `%s%s` at %d:%d (offset %d)",
				open, close, line, col, openOffset)
		}
		script, err := compile(body)
		if err != nil {
			return nil, fmt.Errorf("template: invalid expression `%s%s%s` at %d:%d (offset %d): %w",
				open, body, close, line, col, openOffset, err)
		}
		segs = append(segs, templateSegment{script: script, source: body, offset: openOffset, line: line, column: col})
		i = bodyEnd + len(close)
	}
	flushLit()

	// A template with no expressions still needs one segment so Eval
	// can return the raw string directly.
	if len(segs) == 0 {
		segs = []templateSegment{{literal: raw, line: 1, column: 1}}
	}
	return segs, nil
}

// templateLineCol converts a byte offset in raw to a 1-based line and
// byte-based column, the form editors and humans expect from
// multiline templates.
func templateLineCol(raw string, offset int) (line, col int) {
	if offset > len(raw) {
		offset = len(raw)
	}
	line = 1 + strings.Count(raw[:offset], "\n")
	col = offset - strings.LastIndexByte(raw[:offset], '\n')
	return line, col
}

// scanTemplateExprEnd returns the byte offset within src of the first
// `}` of the run that closes the opener whose body begins at start.
// It drives go/scanner over src[start:] and counts brace depth, so
// string literals, rune literals, comments, and nested composite
// literals are all handled correctly by virtue of using the same
// tokenizer that go/parser uses when it later compiles the expression
// body.
//
// For multi-brace openers like `${{`, depth starts at the opener's
// brace count and the closing `}` run must be contiguous: the scanner
// reaching depth zero on a `}` that is not the end of a `}}` run is
// reported as an error rather than silently splitting the closer.
//
// hadContent reports whether the scanner saw at least one token inside
// the body that wasn't a brace or auto-inserted semicolon, so that
// comment-only bodies like `${/* hi */}` are rejected up front with
// the same "empty expression" error as `${}`.
//
// openOffset is the byte offset of the opening delimiter in the
// original raw template source; it's only used for error messages.
func scanTemplateExprEnd(src string, start, openOffset int, open, close string, braces int) (end int, hadContent bool, err error) {
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src)-start)

	var s scanner.Scanner
	// A no-op error handler keeps the scanner advancing through any
	// lex errors. Compile will surface the real diagnostic when the
	// body is later fed to go/parser.
	s.Init(file, []byte(src[start:]), func(token.Position, string) {}, 0)

	line, col := templateLineCol(src, openOffset)
	depth := braces // we are already inside the opener's brace run
	for {
		pos, tok, _ := s.Scan()
		switch tok {
		case token.EOF:
			return 0, false, fmt.Errorf("template: unclosed `%s` at %d:%d (offset %d, missing `%s`)",
				open, line, col, openOffset, close)
		case token.LBRACE:
			depth++
		case token.RBRACE:
			depth--
			if depth == 0 {
				last := start + file.Offset(pos)
				first := last - (braces - 1)
				for k := first; k <= last; k++ {
					if k < 0 || src[k] != '}' {
						return 0, false, fmt.Errorf("template: expression `%s` at %d:%d (offset %d) must be closed by `%s`",
							open, line, col, openOffset, close)
					}
				}
				return first, hadContent, nil
			}
		case token.SEMICOLON:
			// Automatic semicolon insertion produces these even when
			// the body has no real content, so ignore them.
		default:
			hadContent = true
		}
	}
}
