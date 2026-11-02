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
	"go/token"
	"strings"

	"github.com/deepnoodle-ai/expr/internal/srcedit"
)

// Rewrite returns src with bare JSON-style bracket and brace literals
// rewritten into their typed Go composite-literal equivalents. When
// src contains neither '[' nor '{', Rewrite returns src unchanged
// without invoking the scanner.
func Rewrite(src string) string {
	out, _ := RewriteMap(src)
	return out
}

// RewriteMap is Rewrite that also returns the map from offsets in the
// result back to offsets in src.
func RewriteMap(src string) (string, srcedit.Map) {
	if !strings.ContainsAny(src, "[{") {
		return src, srcedit.Map{}
	}
	toks := srcedit.Scan(src)
	if len(toks) == 0 {
		return src, srcedit.Map{}
	}
	return srcedit.Apply(src, plan(src, toks))
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
func plan(src string, toks []srcedit.Token) []srcedit.Edit {
	matches := srcedit.Brackets(toks)
	commas := topLevelCommas(toks, matches)
	var edits []srcedit.Edit
	var stack []byte
	var braces []byte
	prev := token.ILLEGAL

	for i, t := range toks {
		switch t.Kind {
		case token.LBRACK:
			// '[' immediately followed by ']' is either a slice-type
			// prefix like []T / [][]T / []map[K]V, or an empty array
			// literal. We disambiguate by looking at the token just
			// past the ']': if it could start a type, we treat the
			// pair as a type prefix and leave it alone.
			if i+1 < len(toks) && toks[i+1].Kind == token.RBRACK {
				if isSliceTypeFollow(toks, i+2) {
					stack = append(stack, frameSlice)
					prev = t.Kind
					continue
				}
				// Emit two edits rather than a single splice over the
				// whole pair so that any comments between '[' and ']'
				// are preserved verbatim in the output.
				edits = append(edits, srcedit.Edit{
					Pos: t.Pos, End: t.End, Str: "[]any{",
				})
				edits = append(edits, srcedit.Edit{
					Pos: toks[i+1].Pos, End: toks[i+1].End, Str: "}",
				})
				stack = append(stack, frameEmpty)
				prev = t.Kind
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
			if close := matches[i]; close >= 0 &&
				!commas[i] &&
				isArrayTypeFollow(toks, close+1, matches) {
				stack = append(stack, frameIndex)
				break
			}
			edits = append(edits, srcedit.Edit{
				Pos: t.Pos, End: t.End, Str: "[]any{",
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
				edits = append(edits, srcedit.Edit{
					Pos: t.Pos, End: t.End, Str: "}",
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
				edits = append(edits, srcedit.Edit{
					Pos: t.Pos, End: t.Pos, Str: "map[string]any",
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

		prev = t.Kind
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
func isSliceTypeFollow(toks []srcedit.Token, i int) bool {
	if i >= len(toks) {
		return false
	}
	switch toks[i].Kind {
	case token.IDENT, token.LBRACK,
		token.MAP, token.INTERFACE, token.STRUCT,
		token.CHAN, token.FUNC, token.ARROW:
		return true
	case token.MUL:
		return isPointerTypeFollow(toks, i, nil)
	}
	return false
}

// isPointerTypeFollow reports whether the '*' at toks[i] starts a pointer
// element type ([]*T{...}, [N]*T{...}) rather than a multiplication
// ([6][0] * 2, [] * n). After a value, '*' multiplies, so it is a type
// only when no operand can follow it — a type keyword or another '*' —
// or when a named type is followed by the literal's '{'. matches is nil
// for the "[]" prefix, whose following '[' is already a type.
func isPointerTypeFollow(toks []srcedit.Token, i int, matches []int) bool {
	j := i + 1
	if j >= len(toks) {
		return false
	}
	switch toks[j].Kind {
	case token.MAP, token.INTERFACE, token.STRUCT, token.CHAN, token.FUNC, token.ARROW:
		return true
	case token.MUL:
		return isPointerTypeFollow(toks, j, matches)
	case token.LBRACK:
		if matches == nil {
			return isSliceTypeFollow(toks, j)
		}
		if j+1 < len(toks) && toks[j+1].Kind == token.RBRACK {
			return isSliceTypeFollow(toks, j+2)
		}
		close := matches[j]
		return close >= 0 && isArrayTypeFollow(toks, close+1, matches)
	case token.IDENT:
		// T{ or pkg.T{
		k := j + 1
		if k+1 < len(toks) && toks[k].Kind == token.PERIOD && toks[k+1].Kind == token.IDENT {
			k += 2
		}
		return k < len(toks) && toks[k].Kind == token.LBRACE
	}
	return false
}

// isArrayTypeFollow is the variant used after a non-empty "[N]" to
// decide whether the pair is the length prefix of an array-type
// expression like [N]T or [N][M]T. To keep "[1][0]" working as
// literal-then-index, an inner '[' is only accepted when it is
// itself part of an array/slice type expression — i.e., its own
// matching ']' is followed by something that begins a type.
func isArrayTypeFollow(toks []srcedit.Token, i int, matches []int) bool {
	if i >= len(toks) {
		return false
	}
	switch toks[i].Kind {
	case token.IDENT,
		token.MAP, token.INTERFACE, token.STRUCT,
		token.CHAN, token.FUNC, token.ARROW:
		return true
	case token.MUL:
		return isPointerTypeFollow(toks, i, matches)
	case token.LBRACK:
		// "[N][...]": classify the outer "[N]" as an array type only
		// when the inner bracket pair is itself a type prefix.
		close := matches[i]
		if close < 0 {
			return false
		}
		return isArrayTypeFollow(toks, close+1, matches)
	}
	return false
}

// topLevelCommas reports, for each opening bracket with a match, whether
// a COMMA sits directly inside its pair (not nested in another bracket).
// A top-level comma inside "[...]" rules out the array-type-length
// interpretation: [1, 2]int is never valid Go, but [1, 2] is a perfectly
// good array literal. One pass keeps nested literals linear.
func topLevelCommas(toks []srcedit.Token, matches []int) []bool {
	out := make([]bool, len(toks))
	var stack []int
	for i, t := range toks {
		n := len(stack)
		switch {
		case matches[i] >= 0:
			stack = append(stack, i)
		case t.Kind == token.COMMA:
			if n > 0 {
				out[stack[n-1]] = true
			}
		case n > 0 && matches[stack[n-1]] == i:
			stack = stack[:n-1]
		}
	}
	return out
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
func elementIsAnyOrInterface(src string, toks []srcedit.Token, idx int) bool {
	if idx < 1 {
		return false
	}
	j := idx - 1
	var bracketEnd int
	switch toks[j].Kind {
	case token.IDENT:
		if literalAt(src, toks[j]) != "any" {
			return false
		}
		bracketEnd = j - 1
	case token.RBRACE:
		// `interface{}` body: ... interface { } {
		if j < 2 ||
			toks[j-1].Kind != token.LBRACE ||
			toks[j-2].Kind != token.INTERFACE {
			return false
		}
		bracketEnd = j - 3
	default:
		return false
	}
	if bracketEnd < 0 || toks[bracketEnd].Kind != token.RBRACK {
		return false
	}
	// Find the matching '['.
	depth := 1
	for k := bracketEnd - 1; k >= 0; k-- {
		switch toks[k].Kind {
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
				toks[k+1].Kind == token.IDENT &&
				literalAt(src, toks[k+1]) == "string" &&
				k > 0 && toks[k-1].Kind == token.MAP {
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
func typeWraps(toks []srcedit.Token, k int) bool {
	if k == 0 {
		return false
	}
	switch toks[k-1].Kind {
	case token.RBRACK, token.RBRACE:
		return true
	}
	return false
}

// literalAt returns the source text for the given token. Identifier
// and string tokens are stored with positions only, so we read them
// out of the original source.
func literalAt(src string, t srcedit.Token) string {
	if t.Pos < 0 || t.End > len(src) || t.Pos > t.End {
		return ""
	}
	return src[t.Pos:t.End]
}
