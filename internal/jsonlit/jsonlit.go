// Package jsonlit rewrites JSON-style array and object literals into
// the Go composite-literal syntax that go/parser.ParseExpr accepts.
//
//	[a, b, c]   becomes   []any{a, b, c}
//	{"k": v}    becomes   map[string]any{"k": v}
//	[]          becomes   []any{}
//	{}          becomes   map[string]any{}
//
// The rewrite is token-based. It uses go/scanner to produce the token
// stream and never inspects the contents of string, rune, or comment
// tokens, so literal brackets and braces inside strings are preserved
// verbatim. The transform is also a no-op on already-valid Go
// expression syntax: index expressions (x[i]), selector chains (a.b),
// typed composite literals ([]any{1,2}, map[string]any{"k":1}, []int{},
// []interface{}{}), and ordinary arithmetic all pass through
// unchanged.
//
// jsonlit makes no attempt to validate the input; it only rewrites the
// tokens it can classify unambiguously. If the input is syntactically
// malformed, jsonlit will still produce an output that go/parser will
// reject with its normal error message.
package jsonlit

import (
	"go/scanner"
	"go/token"
	"strings"
)

// Rewrite returns src with bare JSON-style bracket and brace literals
// rewritten into their typed Go composite-literal equivalents. When
// src contains neither '[' nor '{', Rewrite returns src unchanged
// without invoking the scanner.
func Rewrite(src string) string {
	if !strings.ContainsAny(src, "[{") {
		return src
	}
	toks := scanTokens(src)
	if len(toks) == 0 {
		return src
	}
	edits := plan(toks)
	if len(edits) == 0 {
		return src
	}
	return apply(src, edits)
}

// tokenInfo records one scanned token's byte range and kind. We keep
// byte offsets rather than token.Pos values so apply can splice the
// original source without consulting the FileSet.
type tokenInfo struct {
	pos  int
	end  int
	kind token.Token
}

// scanTokens runs go/scanner over src and returns every significant
// token. Comments and auto-inserted semicolons are filtered so that
// "previous token" tracking in plan sees only meaningful tokens.
func scanTokens(src string) []tokenInfo {
	fs := token.NewFileSet()
	file := fs.AddFile("", fs.Base(), len(src))
	var s scanner.Scanner
	s.Init(file, []byte(src), nil, scanner.ScanComments)

	var out []tokenInfo
	for {
		pos, t, lit := s.Scan()
		if t == token.EOF {
			break
		}
		if t == token.COMMENT || t == token.SEMICOLON {
			continue
		}
		off := file.Offset(pos)
		out = append(out, tokenInfo{pos: off, end: off + tokLen(t, lit), kind: t})
	}
	return out
}

// tokLen reports the byte length of a scanned token in the original
// source. For tokens with a non-empty literal (identifiers, numbers,
// strings, runes) we use the literal length directly. For operators
// and keywords the scanner leaves lit empty, but token.Token.String()
// returns the exact source form for those kinds, so its length is
// the right answer.
func tokLen(t token.Token, lit string) int {
	if lit != "" {
		return len(lit)
	}
	return len(t.String())
}

// edit is a single splice into the source. [pos,end) is replaced
// with str. Insertions use pos == end.
type edit struct {
	pos int
	end int
	str string
}

// Stack markers for bracket pairing. We push on '[' and pop on ']';
// we never stack braces because '{' and '}' map to themselves in the
// output (either directly or via a pre-inserted type prefix).
const (
	frameArray byte = 'a' // array literal: '['→'[]any{', ']'→'}'
	frameIndex byte = 'i' // index or type bracket: leave both alone
	frameSlice byte = 's' // slice-type prefix "[]T": leave both alone
	frameEmpty byte = 'e' // '[]' empty array: single edit spans both
)

// plan walks the token stream once and records the edits needed to
// turn bare bracket/brace literals into typed composite literals.
// The algorithm is: precompute each '[' to its matching ']', then
// classify each '[' by context (prev token, content, following
// token) and pair it with its close via a small stack. Each '{' is
// classified by its preceding token; '}' is always left alone.
func plan(toks []tokenInfo) []edit {
	matches := matchBrackets(toks)
	var edits []edit
	var stack []byte
	prev := token.ILLEGAL

	for i, t := range toks {
		switch t.kind {
		case token.LBRACK:
			// '[' immediately followed by ']' is either a slice-type
			// prefix like []T / [][]T / []map[K]V, or an empty array
			// literal. We disambiguate by looking at the token just
			// past the ']': if it could start a type, we treat the
			// pair as a type prefix and leave it alone.
			if i+1 < len(toks) && toks[i+1].kind == token.RBRACK {
				if isSliceTypeFollow(toks, i+2) {
					stack = append(stack, frameSlice)
					prev = t.kind
					continue
				}
				edits = append(edits, edit{
					pos: t.pos,
					end: toks[i+1].end,
					str: "[]any{}",
				})
				stack = append(stack, frameEmpty)
				prev = t.kind
				continue
			}
			// Non-empty '['. Leave it alone if the previous token
			// produces a value — then it's an index bracket (x[i]) or
			// a 'map[K]V' type bracket.
			if leaveBracketAlone(prev) {
				stack = append(stack, frameIndex)
				break
			}
			// The remaining case is "[N]...". It could be either an
			// array literal [a, b, c] or a Go array-type expression
			// [N]T. We classify it as an array type only when it is
			// unambiguously one: no top-level comma in the brackets,
			// and the token after the matching ']' starts a type
			// (IDENT, map, interface, etc. — but not another '['
			// because that collides with literal-then-index chains
			// like [1][0]).
			if close, ok := matches[i]; ok &&
				!hasTopLevelComma(toks, i, close) &&
				isArrayTypeFollow(toks, close+1) {
				stack = append(stack, frameIndex)
				break
			}
			edits = append(edits, edit{
				pos: t.pos, end: t.end, str: "[]any{",
			})
			stack = append(stack, frameArray)

		case token.RBRACK:
			if len(stack) == 0 {
				// Unbalanced input; let the parser produce the error.
				break
			}
			top := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if top == frameArray {
				edits = append(edits, edit{
					pos: t.pos, end: t.end, str: "}",
				})
			}
			// frameIndex / frameSlice / frameEmpty: nothing to do.

		case token.LBRACE:
			// '{' is left alone only when it follows a token that
			// could be a type name or type expression. Everything
			// else is treated as a bare map-object literal.
			if !followsTypeName(prev) {
				edits = append(edits, edit{
					pos: t.pos, end: t.pos, str: "map[string]any",
				})
			}
		}

		prev = t.kind
	}
	return edits
}

// isSliceTypeFollow reports whether the token at position i could
// start a type expression following a "[]" prefix, i.e. whether the
// "[]" should be read as a slice-type prefix rather than an empty
// array literal.
//
// The accepted starts cover everything expr might legitimately see
// in a type position: identifiers (user types and builtins like
// "any"/"int"), another '[' for [][]T chains, the 'map' keyword
// for []map[K]V, 'interface' for []interface{}, and defensively
// 'struct', 'chan', 'func', and '*' for pointer types. If i runs
// off the end, we conservatively say no — bare "[]" at end of input
// is an empty array literal.
func isSliceTypeFollow(toks []tokenInfo, i int) bool {
	if i >= len(toks) {
		return false
	}
	switch toks[i].kind {
	case token.IDENT, token.LBRACK,
		token.MAP, token.INTERFACE, token.STRUCT,
		token.CHAN, token.FUNC, token.MUL:
		return true
	}
	return false
}

// isArrayTypeFollow is the variant used after a non-empty "[N]" to
// decide whether the pair is the length prefix of an array-type
// expression like [N]T. It deliberately omits '[' from the accepted
// set that isSliceTypeFollow allows, because "[N][...]" is more
// commonly a literal-then-index chain ([1][0]) in JSON-extended
// expressions than a 2D array type ([3][4]int), and expr does not
// accept Go array-type literals anyway.
func isArrayTypeFollow(toks []tokenInfo, i int) bool {
	if i >= len(toks) {
		return false
	}
	switch toks[i].kind {
	case token.IDENT,
		token.MAP, token.INTERFACE, token.STRUCT,
		token.CHAN, token.FUNC, token.MUL:
		return true
	}
	return false
}

// hasTopLevelComma reports whether the tokens strictly between open
// and close (both LBRACK/RBRACK indices) contain a COMMA at bracket
// depth 0. A top-level comma inside "[...]" rules out the
// array-type-length interpretation: [1, 2]int is never valid Go,
// but [1, 2] is a perfectly good array literal.
func hasTopLevelComma(toks []tokenInfo, open, close int) bool {
	depth := 0
	for j := open + 1; j < close; j++ {
		switch toks[j].kind {
		case token.LPAREN, token.LBRACK, token.LBRACE:
			depth++
		case token.RPAREN, token.RBRACK, token.RBRACE:
			depth--
		case token.COMMA:
			if depth == 0 {
				return true
			}
		}
	}
	return false
}

// matchBrackets precomputes, for every LBRACK token in toks, the
// index of its matching RBRACK. Unbalanced opens are silently
// omitted from the result — the main plan loop will then fall
// through and let the parser surface the error.
func matchBrackets(toks []tokenInfo) map[int]int {
	m := make(map[int]int)
	var stack []int
	for i, t := range toks {
		switch t.kind {
		case token.LBRACK:
			stack = append(stack, i)
		case token.RBRACK:
			if n := len(stack); n > 0 {
				m[stack[n-1]] = i
				stack = stack[:n-1]
			}
		}
	}
	return m
}

// leaveBracketAlone reports whether a '[' following this token
// should be treated as an index expression or type bracket (and
// therefore left untouched), as opposed to the start of an array
// literal. The rule is: '[' after anything that could produce a
// value or begin a type is not an array literal.
func leaveBracketAlone(prev token.Token) bool {
	switch prev {
	case token.IDENT,
		token.INT, token.FLOAT, token.IMAG, token.CHAR, token.STRING,
		token.RPAREN, token.RBRACK, token.RBRACE,
		token.MAP:
		return true
	}
	return false
}

// followsTypeName reports whether a '{' following this token is the
// body of a typed composite literal and should be left alone. We
// accept identifiers (user types or builtins), the 'map'/'struct'/
// 'interface'/'chan'/'func' keywords, and '}' — the last of these
// catches compound type forms like []interface{}{1} and struct{X
// int}{X: 1}, where the composite-literal body opens immediately
// after a type-body close brace. Everything else is treated as a
// bare object literal.
func followsTypeName(prev token.Token) bool {
	switch prev {
	case token.IDENT,
		token.MAP, token.STRUCT, token.INTERFACE,
		token.CHAN, token.FUNC,
		token.RBRACE:
		return true
	}
	return false
}

// apply splices edits into src. Edits are produced by plan in
// source order and never overlap, so a single pass suffices.
func apply(src string, edits []edit) string {
	var b strings.Builder
	b.Grow(len(src) + 16*len(edits))
	last := 0
	for _, e := range edits {
		b.WriteString(src[last:e.pos])
		b.WriteString(e.str)
		last = e.end
	}
	b.WriteString(src[last:])
	return b.String()
}
