// Package optaccess rewrites the optional-access operators `?.` and
// `?[` into calls on internal sentinel functions that the evaluator
// dispatches as special forms.
//
//	obj?.field   becomes   __try_select__(obj, "field")
//	obj?[i]      becomes   __try_index__(obj, i)
//
// The rewrite is token-based. Strings, runes, and comments are
// scanner tokens we never look inside, so a `?.` written inside a
// string literal or comment is preserved verbatim. The transform is
// also a no-op when src contains no `?` byte: the scanner pass is
// skipped entirely.
//
// The LHS of `?.` / `?[` is the primary expression that ends just
// before the `?`. The walker matches balanced parens, brackets, and
// braces so `f(a)?.b`, `a[0]?.b`, `(a + b)?.c`, `[1, 2]?[0]`,
// `{"k": 1}?.k` and `[]any{1}?[0]` all rewrite with the expected LHS, as do calls on
// the `map` and `if` keywords that expr accepts as call targets.
// Chained optional access (`a?.b?.c`) is processed by
// repeated single-rewrite passes until the source no longer contains
// `?.` / `?[`, producing nested calls like
// `__try_select__(__try_select__(a, "b"), "c")`.
//
// optaccess does not validate. Anything it cannot classify
// unambiguously is left alone for the parser to reject.
package optaccess

import (
	"go/scanner"
	"go/token"
	"strings"
)

// Rewrite returns src with `?.field` and `?[idx]` rewritten to calls
// on the sentinel functions __try_select__ and __try_index__.
//
// When src contains no `?`, Rewrite returns src unchanged without
// invoking the scanner. Otherwise it scans once and decides every
// rewrite from the original token stream: the LHS of a `?` is found by
// walking back from it, and a chain (`a?.b?.c`) gives later operators a
// LHS that starts where the earlier one's does, so their calls nest:
// `__try_select__(__try_select__(a, "b"), "c")`. The text is then
// spliced in a single forward pass, keeping compile time linear in the
// length of a chain.
func Rewrite(src string) string {
	if !strings.Contains(src, "?") {
		return src
	}
	toks := scanTokens(src)
	if len(toks) == 0 {
		return src
	}
	closes := matchBracketsForward(toks)
	// lhsOf[e] is the LHS start of the rewrite whose last token is e
	// (the field of `?.f`, the `]` of `?[i]`), or -1.
	lhsOf := make([]int, len(toks))
	for i := range lhsOf {
		lhsOf[i] = -1
	}
	// prefixes[i] are the calls that open before token i, innermost
	// first; repl[i] replaces tokens i..repl[i].last with repl[i].str.
	type replacement struct {
		str  string
		last int
	}
	prefixes := make(map[int][]string)
	repl := make(map[int]replacement)
	for i := 0; i < len(toks); i++ {
		if !isQuestion(src, toks[i]) || i+1 >= len(toks) {
			continue
		}
		switch toks[i+1].kind {
		case token.PERIOD:
			if i+2 >= len(toks) || !isFieldToken(toks[i+2].kind) {
				continue
			}
			lhs := lhsStartIdx(toks, src, i, lhsOf)
			if lhs < 0 || lhs >= i {
				continue
			}
			field := toks[i+2].lit
			if field == "" {
				field = src[toks[i+2].pos:toks[i+2].end]
			}
			lhsOf[i+2] = lhs
			prefixes[lhs] = append(prefixes[lhs], "__try_select__(")
			repl[i] = replacement{str: ", " + quoteString(field) + ")", last: i + 2}
			i += 2
		case token.LBRACK:
			closeIdx := closes[i+1]
			if closeIdx < 0 {
				continue
			}
			lhs := lhsStartIdx(toks, src, i, lhsOf)
			if lhs < 0 || lhs >= i {
				continue
			}
			lhsOf[closeIdx] = lhs
			prefixes[lhs] = append(prefixes[lhs], "__try_index__(")
			repl[i] = replacement{str: ", ", last: i + 1}
			repl[closeIdx] = replacement{str: ")", last: closeIdx}
			// The index expression may hold optional access of its own;
			// the loop continues into it.
			i++
		}
	}
	if len(repl) == 0 {
		return src
	}

	var b strings.Builder
	b.Grow(len(src) + 20*len(repl))
	last := 0
	for i := 0; i < len(toks); i++ {
		if ps, ok := prefixes[i]; ok {
			b.WriteString(src[last:toks[i].pos])
			// Keep the call name from fusing with a preceding word in
			// malformed input such as `A.0?.b`.
			if p := toks[i].pos; p > 0 && isWordByte(src[p-1]) {
				b.WriteByte(' ')
			}
			// A later operator wraps an earlier one: outermost first.
			for k := len(ps) - 1; k >= 0; k-- {
				b.WriteString(ps[k])
			}
			last = toks[i].pos
		}
		if r, ok := repl[i]; ok {
			b.WriteString(src[last:toks[i].pos])
			b.WriteString(r.str)
			last = toks[r.last].end
			i = r.last
		}
	}
	b.WriteString(src[last:])
	return b.String()
}

func isWordByte(c byte) bool {
	return c == '_' || '0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || c >= 0x80
}

// matchBracketsForward returns, for each `[` token index, the index of
// its matching `]`, and -1 for every other token or an unclosed `[`.
func matchBracketsForward(toks []tokenInfo) []int {
	out := make([]int, len(toks))
	var stack []int
	for i, t := range toks {
		out[i] = -1
		switch t.kind {
		case token.LBRACK:
			stack = append(stack, i)
		case token.RBRACK:
			if n := len(stack); n > 0 {
				out[stack[n-1]] = i
				stack = stack[:n-1]
			}
		}
	}
	return out
}

// tokenInfo records one scanned token's byte range and kind. lit is
// retained for IDENT tokens so we can splice the field name into the
// rewrite without re-reading the source.
type tokenInfo struct {
	pos  int
	end  int
	kind token.Token
	lit  string
}

// scanTokens runs go/scanner over src and returns every significant
// token. Comments and auto-inserted semicolons are filtered so
// "previous token" tracking sees only meaningful tokens.
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
		out = append(out, tokenInfo{
			pos:  off,
			end:  off + tokLen(t, lit),
			kind: t,
			lit:  lit,
		})
	}
	return out
}

func tokLen(t token.Token, lit string) int {
	if lit != "" {
		return len(lit)
	}
	return len(t.String())
}

// isQuestion reports whether t is a single `?` byte. The scanner
// flags `?` as ILLEGAL but other bytes (e.g. `@`, `#`) end up there
// too, so the byte check is needed to avoid rewriting around them.
func isQuestion(src string, t tokenInfo) bool {
	return t.kind == token.ILLEGAL && t.end-t.pos == 1 && src[t.pos] == '?'
}

// isFieldToken reports whether t can be the field name after `?.`.
// Plain identifiers are the normal case; any Go keyword is accepted too,
// because expr allows keywords as field names (`event?.type`). The field
// name is spliced into a string literal here, so no keyword rewrite is
// needed downstream.
func isFieldToken(t token.Token) bool {
	return t == token.IDENT || t.IsKeyword()
}

// lhsStartIdx returns the token index where the LHS of the `?` at
// qIdx starts. The walk extends backwards through identifiers,
// selector chains (`.IDENT`), balanced index brackets, balanced
// parens (call args or paren groups), and previous `?` tokens.
//
// Returns -1 if the LHS would extend past the start of the input
// without ever closing — that is, when the source is unbalanced or
// the `?` has nothing to the left of it.
//
// lhsOf maps the last token of each earlier rewrite to its LHS start.
// That rewrite becomes one call, a complete primary, so a walk reaching
// its last token ends at its start — which also keeps `a?.type?.b`,
// whose keyword field the walk could not step over, working.
func lhsStartIdx(toks []tokenInfo, src string, qIdx int, lhsOf []int) int {
	if qIdx == 0 {
		return -1
	}
	i := qIdx - 1
	for i >= 0 {
		if lhsOf[i] >= 0 {
			return lhsOf[i]
		}
		t := toks[i]
		switch t.kind {
		case token.IDENT, token.INT, token.FLOAT, token.STRING, token.CHAR,
			token.MAP, token.IF:
			// `map` and `if` walk like identifiers: expr accepts them
			// as ordinary names in selector chains (`a.map?.x`).
			if i-1 >= 0 && toks[i-1].kind == token.PERIOD {
				i -= 2
				continue
			}
			return i
		case token.RBRACK:
			j := matchBracketBack(toks, i)
			if j < 0 {
				return -1
			}
			if j == 0 {
				// The bracket pair opens the input, so it can only be
				// an array literal: `[1, 2]?[0]`.
				return 0
			}
			i = j - 1
			continue
		case token.RBRACE:
			// Brace-closed primary: a bare object literal `{...}?.x`,
			// or a typed composite literal (`[]any{...}?[0]`) whose
			// type the walk steps back over. Other typed forms decline
			// and leave the parser to report the stray `?`.
			j := matchBraceBack(toks, i)
			if j < 0 {
				return -1
			}
			if j == 0 {
				return 0
			}
			if startsTypedComposite(toks[j-1].kind) {
				return compositeTypeStart(toks, j)
			}
			return j
		case token.RPAREN:
			j := matchParenBack(toks, i)
			if j < 0 {
				return -1
			}
			prev := j - 1
			if prev >= 0 && toks[prev].kind == token.PERIOD {
				// A type assertion `x.(T)`, which expr rejects; splicing
				// inside it would only garble the parser's error.
				return -1
			}
			if prev >= 0 && extendsPrimary(toks[prev].kind) {
				i = prev
				continue
			}
			return j
		case token.ILLEGAL:
			if isQuestion(src, t) {
				i--
				continue
			}
			return i + 1
		default:
			return i + 1
		}
	}
	// Walking off the start of the stream means the LHS would
	// extend past the beginning of the input — i.e., it never
	// resolved to a complete primary. Decline the rewrite and let
	// the parser produce its normal error on the `?` token.
	return -1
}

// matchBracketBack returns the index of the `[` that opens the `]`
// at rbrack. Negative on imbalance.
func matchBracketBack(toks []tokenInfo, rbrack int) int {
	depth := 1
	for j := rbrack - 1; j >= 0; j-- {
		switch toks[j].kind {
		case token.RBRACK:
			depth++
		case token.LBRACK:
			depth--
			if depth == 0 {
				return j
			}
		}
	}
	return -1
}

// matchParenBack returns the index of the `(` that opens the `)`
// at rparen. Negative on imbalance.
func matchParenBack(toks []tokenInfo, rparen int) int {
	depth := 1
	for j := rparen - 1; j >= 0; j-- {
		switch toks[j].kind {
		case token.RPAREN:
			depth++
		case token.LPAREN:
			depth--
			if depth == 0 {
				return j
			}
		}
	}
	return -1
}

// matchBraceBack returns the index of the `{` that opens the `}`
// at rbrace. Negative on imbalance.
func matchBraceBack(toks []tokenInfo, rbrace int) int {
	depth := 1
	for j := rbrace - 1; j >= 0; j-- {
		switch toks[j].kind {
		case token.RBRACE:
			depth++
		case token.LBRACE:
			depth--
			if depth == 0 {
				return j
			}
		}
	}
	return -1
}

// startsTypedComposite reports whether a `{` directly preceded by t
// belongs to a typed composite literal (`[]any{...}`, `T{...}`,
// `map[string]any{...}`) rather than a bare object literal.
func startsTypedComposite(t token.Token) bool {
	switch t {
	case token.IDENT, token.RBRACK, token.RBRACE, token.RPAREN,
		token.MAP, token.STRUCT, token.INTERFACE, token.CHAN, token.FUNC:
		return true
	}
	return false
}

// compositeTypeStart returns the index of the first token of the type
// of the composite literal whose body opens at toks[lbrace], or -1 when
// that type is not a slice, array or map type. The element (or value)
// type is a single name (`any`, `T`, `pkg.T`) or `interface{}`; anything
// fancier is not a literal expr accepts, so declining it costs nothing.
func compositeTypeStart(toks []tokenInfo, lbrace int) int {
	elem := typeNameStart(toks, lbrace-1)
	if elem < 1 || toks[elem-1].kind != token.RBRACK {
		return -1
	}
	open := matchBracketBack(toks, elem-1)
	if open < 0 {
		return -1
	}
	if open > 0 && toks[open-1].kind == token.MAP {
		// map[K]V: K is a single name too.
		if typeNameStart(toks, elem-2) != open+1 {
			return -1
		}
		return open - 1
	}
	return open
}

// typeNameStart returns the index of the first token of a type name that
// ends at toks[end]: `T`, `pkg.T` or `interface{}`; -1 otherwise.
func typeNameStart(toks []tokenInfo, end int) int {
	if end < 0 {
		return -1
	}
	switch toks[end].kind {
	case token.IDENT:
		if end >= 2 && toks[end-1].kind == token.PERIOD && toks[end-2].kind == token.IDENT {
			return end - 2
		}
		return end
	case token.RBRACE:
		if end >= 2 && toks[end-1].kind == token.LBRACE && toks[end-2].kind == token.INTERFACE {
			return end - 2
		}
	}
	return -1
}

// extendsPrimary reports whether t can directly precede a `(` that
// is part of a call expression. A `(` after one of these tokens is
// always a call; after anything else it begins a paren group. `map`
// and `if` are included because expr accepts them as call targets
// (`map(xs, it)?[0]`, `if(c, a, b)?.x`).
func extendsPrimary(t token.Token) bool {
	switch t {
	case token.IDENT, token.RBRACK, token.RPAREN, token.MAP, token.IF:
		return true
	}
	return false
}

// quoteString returns a Go double-quoted string literal whose
// decoded value is s. Field names from the scanner are valid Go
// identifiers, so the body never contains characters that need
// escaping. The simple `"` wrapping is therefore correct without
// invoking strconv.Quote.
func quoteString(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	b.WriteString(s)
	b.WriteByte('"')
	return b.String()
}
