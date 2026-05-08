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
	edits := plan(src, toks)
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

// Stack markers for bracket pairing. We push on '[' and pop on ']'.
const (
	frameArray byte = 'a' // array literal: '['→'[]any{', ']'→'}'
	frameIndex byte = 'i' // index or type bracket: leave both alone
	frameSlice byte = 's' // slice-type prefix "[]T": leave both alone
	frameEmpty byte = 'e' // '[]' empty array: single edit spans both
)

// Brace-stack markers. We track each '{' so the rule for nested '{'
// can depend on whether the enclosing brace is an object literal or
// a typed composite literal — and, in the typed case, whether its
// element type is one for which jsonlit's bare-{ rewrite is desired.
const (
	braceObject byte = 'o' // bare '{...}' rewritten to map[string]any{...}
	braceAny    byte = 'a' // typed composite whose element/value type is `any` or `interface{}`
	braceInert  byte = 'i' // any other typed composite — leave nested {...} alone
)

// plan walks the token stream once and records the edits needed to
// turn bare bracket/brace literals into typed composite literals.
// The algorithm is: precompute each '[' to its matching ']', then
// classify each '[' by context (prev token, content, following
// token) and pair it with its close via a small stack. Each '{' is
// classified by its preceding token and the brace stack.
func plan(src string, toks []tokenInfo) []edit {
	matches := matchBrackets(toks)
	var edits []edit
	var stack []byte
	var braces []byte
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
				// Emit two edits rather than a single splice over the
				// whole pair so that any comments between '[' and ']'
				// are preserved verbatim in the output.
				edits = append(edits, edit{
					pos: t.pos, end: t.end, str: "[]any{",
				})
				edits = append(edits, edit{
					pos: toks[i+1].pos, end: toks[i+1].end, str: "}",
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
			// [N]T (including [N][M]T, [N][]T, etc.). We classify it
			// as an array type only when it is unambiguously one: no
			// top-level comma in the brackets, and the token after
			// the matching ']' starts a type expression.
			if close, ok := matches[i]; ok &&
				!hasTopLevelComma(toks, i, close) &&
				isArrayTypeFollow(toks, close+1, matches) {
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
			// could be a type name or type expression — or when the
			// enclosing typed composite has a structured element type
			// (e.g. []struct{X int}{{1}}, [][]int{{1,2}}), where Go's
			// type-omission rule applies and we must not splice a
			// bogus map[string]any prefix in front of it.
			leave := followsTypeName(prev)
			if !leave && len(braces) > 0 &&
				braces[len(braces)-1] == braceInert &&
				(prev == token.LBRACE || prev == token.COMMA || prev == token.COLON) {
				leave = true
			}
			if !leave {
				edits = append(edits, edit{
					pos: t.pos, end: t.pos, str: "map[string]any",
				})
				braces = append(braces, braceObject)
				break
			}
			// Typed composite. Decide whether its element type is
			// `any`/`interface{}` (in which case nested bare {...}
			// should still be rewritten to map[string]any) or some
			// other structured type (leave nested {...} alone).
			if followsTypeName(prev) && elementIsAnyOrInterface(src, toks, i) {
				braces = append(braces, braceAny)
			} else {
				braces = append(braces, braceInert)
			}

		case token.RBRACE:
			if len(braces) > 0 {
				braces = braces[:len(braces)-1]
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
// 'struct', 'chan', 'func', '*' for pointer types, and '<-' for
// receive-only channel types ([]<-chan T). If i runs off the end,
// we conservatively say no — bare "[]" at end of input is an empty
// array literal.
func isSliceTypeFollow(toks []tokenInfo, i int) bool {
	if i >= len(toks) {
		return false
	}
	switch toks[i].kind {
	case token.IDENT, token.LBRACK,
		token.MAP, token.INTERFACE, token.STRUCT,
		token.CHAN, token.FUNC, token.MUL, token.ARROW:
		return true
	}
	return false
}

// isArrayTypeFollow is the variant used after a non-empty "[N]" to
// decide whether the pair is the length prefix of an array-type
// expression like [N]T or [N][M]T. To keep "[1][0]" working as
// literal-then-index, an inner '[' is only accepted when it is
// itself part of an array/slice type expression — i.e., its own
// matching ']' is followed by something that begins a type.
func isArrayTypeFollow(toks []tokenInfo, i int, matches map[int]int) bool {
	if i >= len(toks) {
		return false
	}
	switch toks[i].kind {
	case token.IDENT,
		token.MAP, token.INTERFACE, token.STRUCT,
		token.CHAN, token.FUNC, token.MUL, token.ARROW:
		return true
	case token.LBRACK:
		// "[N][...]": classify the outer "[N]" as an array type only
		// when the inner bracket pair is itself a type prefix.
		close, ok := matches[i]
		if !ok {
			return false
		}
		return isArrayTypeFollow(toks, close+1, matches)
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
// body of a typed composite literal or function literal and should
// be left alone. We accept identifiers (user types or builtins),
// the 'map'/'struct'/'interface'/'chan'/'func' keywords, '}' for
// compound type forms like []interface{}{1} and struct{X int}{X: 1}
// where the composite-literal body opens immediately after a
// type-body close brace, ')' for function-literal bodies like
// `func() {}`, and ']' for generic instantiations like `List[int]{}`
// (in expression position '][{' only occurs after a type, since
// indexing followed by a composite literal is not a valid Go
// expression). Everything else is treated as a bare object literal.
func followsTypeName(prev token.Token) bool {
	switch prev {
	case token.IDENT,
		token.MAP, token.STRUCT, token.INTERFACE,
		token.CHAN, token.FUNC,
		token.RBRACE, token.RPAREN, token.RBRACK:
		return true
	}
	return false
}

// elementIsAnyOrInterface reports whether the type immediately
// preceding the '{' at toks[idx] is exactly `[]any`, `map[string]any`,
// `[]interface{}`, or `map[string]interface{}`. These are the typed
// composite forms whose element/value type is `any` or `interface{}`,
// for which Go does not allow type omission and so jsonlit's bare-{
// rewrite is needed (e.g. `map[string]any{"k": {"j": 1}}`). For any
// other typed outer (struct slices, slice-of-slice, simple typed
// slices, etc.) Go accepts type omission, and we must leave nested
// {...} alone.
func elementIsAnyOrInterface(src string, toks []tokenInfo, idx int) bool {
	if idx < 1 {
		return false
	}
	j := idx - 1
	var bracketEnd int
	switch toks[j].kind {
	case token.IDENT:
		if literalAt(src, toks[j]) != "any" {
			return false
		}
		bracketEnd = j - 1
	case token.RBRACE:
		// `interface{}` body: ... interface { } {
		if j < 2 ||
			toks[j-1].kind != token.LBRACE ||
			toks[j-2].kind != token.INTERFACE {
			return false
		}
		bracketEnd = j - 3
	default:
		return false
	}
	if bracketEnd < 0 || toks[bracketEnd].kind != token.RBRACK {
		return false
	}
	// Find the matching '['.
	depth := 1
	for k := bracketEnd - 1; k >= 0; k-- {
		switch toks[k].kind {
		case token.RBRACK:
			depth++
		case token.LBRACK:
			depth--
			if depth != 0 {
				continue
			}
			// Slice/array prefix: empty brackets `[]any` / `[]interface{}`.
			if bracketEnd-k == 1 {
				return !typeWraps(toks, k)
			}
			// Map prefix: `map[string]any` / `map[string]interface{}`.
			if bracketEnd-k == 2 &&
				toks[k+1].kind == token.IDENT &&
				literalAt(src, toks[k+1]) == "string" &&
				k > 0 && toks[k-1].kind == token.MAP {
				return !typeWraps(toks, k-1)
			}
			return false
		}
	}
	return false
}

// typeWraps reports whether the type starting at toks[k] is itself
// the value/element of an outer composite type — i.e., whether
// toks[k-1] closes a wrapping `]` or `}`. When that is the case,
// the type at toks[k:] is not a top-level `[]any`/`map[string]any`
// (e.g. it's `[]any` inside `[][]any` or `map[string]any` inside
// `[]map[string]any`), and Go's type-omission rules govern nested
// composite literals — leave them alone.
func typeWraps(toks []tokenInfo, k int) bool {
	if k == 0 {
		return false
	}
	switch toks[k-1].kind {
	case token.RBRACK, token.RBRACE:
		return true
	}
	return false
}

// literalAt returns the source text for the given token. Identifier
// and string tokens are stored with positions only, so we read them
// out of the original source.
func literalAt(src string, t tokenInfo) string {
	if t.pos < 0 || t.end > len(src) || t.pos > t.end {
		return ""
	}
	return src[t.pos:t.end]
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
