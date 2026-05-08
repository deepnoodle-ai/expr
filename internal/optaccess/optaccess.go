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
// before the `?`. The walker matches balanced parens and brackets so
// `f(a)?.b`, `a[0]?.b`, and `(a + b)?.c` all rewrite with the
// expected LHS. Chained optional access (`a?.b?.c`) is processed by
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
// invoking the scanner. Chained optional access is handled by
// iteratively rewriting the leftmost remaining `?.` or `?[` until
// the source contains none, so each rewrite sees the previous one
// already rewritten as its LHS.
func Rewrite(src string) string {
	if !strings.Contains(src, "?") {
		return src
	}
	for {
		next, changed := rewriteOnce(src)
		if !changed {
			return src
		}
		src = next
	}
}

// rewriteOnce rewrites a single leftmost `?.` or `?[` occurrence in
// src and returns the new source. The boolean is false when no
// rewritable occurrence was found, in which case src is returned
// unchanged.
func rewriteOnce(src string) (string, bool) {
	toks := scanTokens(src)
	if len(toks) == 0 {
		return src, false
	}
	for i := 0; i < len(toks); i++ {
		if !isQuestion(src, toks[i]) {
			continue
		}
		if i+1 >= len(toks) {
			continue
		}
		next := toks[i+1]
		switch next.kind {
		case token.PERIOD:
			if i+2 >= len(toks) || toks[i+2].kind != token.IDENT {
				continue
			}
			lhsStart := lhsStartIdx(toks, src, i)
			if lhsStart < 0 {
				continue
			}
			field := toks[i+2].lit
			if field == "" {
				field = src[toks[i+2].pos:toks[i+2].end]
			}
			lhsBytes := src[toks[lhsStart].pos:toks[i].pos]
			rep := "__try_select__(" + lhsBytes + ", " + quoteString(field) + ")"
			return splice(src, toks[lhsStart].pos, toks[i+2].end, rep), true
		case token.LBRACK:
			closeIdx := matchBracketForward(toks, i+1)
			if closeIdx < 0 {
				continue
			}
			lhsStart := lhsStartIdx(toks, src, i)
			if lhsStart < 0 {
				continue
			}
			lhsBytes := src[toks[lhsStart].pos:toks[i].pos]
			idxBytes := src[toks[i+1].end:toks[closeIdx].pos]
			rep := "__try_index__(" + lhsBytes + ", " + idxBytes + ")"
			return splice(src, toks[lhsStart].pos, toks[closeIdx].end, rep), true
		}
	}
	return src, false
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

// matchBracketForward finds the index of the `]` that closes the `[`
// at lbrack. Returns -1 if no matching close exists.
func matchBracketForward(toks []tokenInfo, lbrack int) int {
	depth := 1
	for j := lbrack + 1; j < len(toks); j++ {
		switch toks[j].kind {
		case token.LBRACK:
			depth++
		case token.RBRACK:
			depth--
			if depth == 0 {
				return j
			}
		}
	}
	return -1
}

// lhsStartIdx returns the token index where the LHS of the `?` at
// qIdx starts. The walk extends backwards through identifiers,
// selector chains (`.IDENT`), balanced index brackets, balanced
// parens (call args or paren groups), and previous `?` tokens.
//
// Returns -1 if the LHS would extend past the start of the input
// without ever closing — that is, when the source is unbalanced or
// the `?` has nothing to the left of it.
func lhsStartIdx(toks []tokenInfo, src string, qIdx int) int {
	if qIdx == 0 {
		return -1
	}
	i := qIdx - 1
	for i >= 0 {
		t := toks[i]
		switch t.kind {
		case token.IDENT, token.INT, token.FLOAT, token.STRING, token.CHAR:
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
			i = j - 1
			continue
		case token.RPAREN:
			j := matchParenBack(toks, i)
			if j < 0 {
				return -1
			}
			prev := j - 1
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

// extendsPrimary reports whether t can directly precede a `(` that
// is part of a call expression. A `(` after one of these tokens is
// always a call; after anything else it begins a paren group.
func extendsPrimary(t token.Token) bool {
	switch t {
	case token.IDENT, token.RBRACK, token.RPAREN:
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

// splice replaces src[pos:end] with str.
func splice(src string, pos, end int, str string) string {
	var b strings.Builder
	b.Grow(len(src) + len(str))
	b.WriteString(src[:pos])
	b.WriteString(str)
	b.WriteString(src[end:])
	return b.String()
}
