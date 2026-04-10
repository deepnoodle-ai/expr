// Package template is a tiny `${...}` string interpolator that compiles
// each expression once at construction time and re-evaluates them per
// call. The expression body inside `${...}` is compiled by any
// [Compiler], so the same Template type works whether the host wires in
// expr or some other engine.
//
// Parsing uses go/scanner to find the closing `}` of each expression,
// which means the expression body can contain braces inside string,
// rune, and comment tokens, as well as nested composite literals like
// `map[string]any{"k": 1}`, without the scanner mistaking them for the
// template's closing delimiter.
//
// Escaping: `$$` is always rewritten to a literal `$`. This means
// `$${name}` emits the literal text `${name}` (the `$$` becomes `$`,
// and the following `{name}` is plain text because it no longer has a
// leading `$`). A bare `$` that is not followed by `$` or `{` is
// emitted verbatim, so things like `$5` or `$foo` pass through
// unchanged.
package template

import (
	"context"
	"fmt"
	"go/scanner"
	"go/token"
	"strings"
)

// Script is a compiled expression that can be evaluated against an
// environment. It matches the shape used throughout expr so any
// *expr.Program satisfies it directly.
type Script interface {
	Run(ctx context.Context, env any) (any, error)
}

// Compiler turns an expression source string into a [Script]. Any type
// that implements Compile(string) (Script, error) satisfies this, so
// an expr.Engine's Compiler() result plugs in without conversion.
type Compiler interface {
	Compile(code string) (Script, error)
}

// Template is a parsed, pre-compiled interpolation template. Its zero
// value is not useful; construct instances with [New].
type Template struct {
	raw      string
	segments []segment
}

// segment is either a literal chunk of the source (script == nil) or a
// compiled expression to evaluate at runtime (script != nil). For
// script segments, source and offset describe the original `${...}`
// body and its starting byte offset in the raw template, so runtime
// errors can point at the exact expression that failed.
type segment struct {
	literal string
	script  Script
	source  string
	offset  int
}

// New parses raw and pre-compiles every `${...}` expression using the
// given Compiler. Strings without any `${...}` are accepted and become
// constant templates that return raw unchanged from [Template.Eval].
func New(c Compiler, raw string) (*Template, error) {
	segs, err := parse(raw, c)
	if err != nil {
		return nil, err
	}
	return &Template{raw: raw, segments: segs}, nil
}

// Raw returns the unparsed template source.
func (t *Template) Raw() string { return t.raw }

// Eval evaluates each `${...}` expression against globals and
// concatenates the results with the surrounding literal text.
// Templates with no expressions return the raw source unchanged
// without invoking any script.
//
// Value rendering rules for each `${...}` result:
//
//   - nil renders as the empty string. Optional fields that resolve
//     to nil silently produce no output rather than the literal
//     "<nil>". This matches the convention used by Jinja, Liquid,
//     Handlebars, and most text-oriented interpolators, and means
//     callers do not need a null-coalescing operator for the common
//     case of optional values.
//   - string values pass through unchanged.
//   - Everything else is formatted with fmt.Sprintf("%v", v).
//
// The nil-to-empty rule means a template cannot distinguish "value
// was nil" from "value was the empty string" in its output. Callers
// that need that distinction should wrap the expression in something
// that returns a sentinel.
func (t *Template) Eval(ctx context.Context, globals map[string]any) (string, error) {
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
		v, err := seg.script.Run(ctx, globals)
		if err != nil {
			return "", fmt.Errorf("template: evaluating ${%s} at offset %d: %w", seg.source, seg.offset, err)
		}
		b.WriteString(formatValue(v))
	}
	return b.String(), nil
}

// formatValue renders a Run result for interpolation. nil becomes the
// empty string; strings pass through unchanged; everything else uses
// Go's default formatting.
func formatValue(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	default:
		return fmt.Sprintf("%v", v)
	}
}

// parse walks raw once, emitting literal segments for plain text and
// compiling each `${...}` body into a script segment. It preserves a
// single literal segment for constant templates so Eval can take the
// fast path.
func parse(raw string, c Compiler) ([]segment, error) {
	if raw == "" {
		return []segment{{literal: ""}}, nil
	}

	var segs []segment
	var lit strings.Builder
	flushLit := func() {
		if lit.Len() > 0 {
			segs = append(segs, segment{literal: lit.String()})
			lit.Reset()
		}
	}

	i := 0
	for i < len(raw) {
		// Look for the next '$' that might begin something interesting.
		// Anything up to that point is literal.
		dollar := strings.IndexByte(raw[i:], '$')
		if dollar < 0 {
			lit.WriteString(raw[i:])
			break
		}
		lit.WriteString(raw[i : i+dollar])
		i += dollar

		// '$' at end of string: literal.
		if i+1 >= len(raw) {
			lit.WriteByte('$')
			i++
			continue
		}

		switch raw[i+1] {
		case '$':
			// '$$' always collapses to a single literal '$'. Advancing
			// past both characters means a following '{' is plain text,
			// which is how `$${name}` emits the literal `${name}`.
			lit.WriteByte('$')
			i += 2
		case '{':
			// Expression opener. Flush any buffered literal, locate
			// the matching '}', compile the body, emit a script
			// segment.
			flushLit()
			openOffset := i
			exprStart := i + 2
			exprEnd, hadContent, err := scanExprEnd(raw, exprStart, openOffset)
			if err != nil {
				return nil, err
			}
			body := strings.TrimSpace(raw[exprStart:exprEnd])
			if body == "" || !hadContent {
				return nil, fmt.Errorf("template: empty expression `${}` at offset %d", openOffset)
			}
			script, err := c.Compile(body)
			if err != nil {
				return nil, fmt.Errorf("template: invalid expression `${%s}` at offset %d: %w", body, openOffset, err)
			}
			segs = append(segs, segment{script: script, source: body, offset: openOffset})
			i = exprEnd + 1
		default:
			// Bare '$' followed by something else: literal.
			lit.WriteByte('$')
			i++
		}
	}
	flushLit()

	// A template with no expressions still needs one segment so Eval
	// can return the raw string directly.
	if len(segs) == 0 {
		segs = []segment{{literal: raw}}
	}
	return segs, nil
}

// scanExprEnd returns the byte offset within src of the `}` that
// closes the `${` whose body begins at start. It drives go/scanner
// over src[start:] and counts brace depth, so string literals, rune
// literals, comments, and nested composite literals are all handled
// correctly by virtue of using the same tokenizer that go/parser uses
// when it later compiles the expression body.
//
// hadContent reports whether the scanner saw at least one token inside
// the body that wasn't a brace or auto-inserted semicolon, so that
// comment-only bodies like `${/* hi */}` are rejected up front with
// the same "empty expression" error as `${}`.
//
// openOffset is the byte offset of the opening `${` in the original
// raw template source; it's only used for error messages.
func scanExprEnd(src string, start, openOffset int) (end int, hadContent bool, err error) {
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src)-start)

	var s scanner.Scanner
	// A no-op error handler keeps the scanner advancing through any
	// lex errors. Compile will surface the real diagnostic when the
	// body is later fed to go/parser.
	s.Init(file, []byte(src[start:]), func(token.Position, string) {}, 0)

	depth := 1 // we are already inside the '{' of '${'
	for {
		pos, tok, _ := s.Scan()
		switch tok {
		case token.EOF:
			return 0, false, fmt.Errorf("template: unclosed `${` at offset %d (missing `}`)", openOffset)
		case token.LBRACE:
			depth++
		case token.RBRACE:
			depth--
			if depth == 0 {
				return start + file.Offset(pos), hadContent, nil
			}
		case token.SEMICOLON:
			// Automatic semicolon insertion produces these even when
			// the body has no real content, so ignore them.
		default:
			hadContent = true
		}
	}
}
