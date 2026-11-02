// Package srcedit holds the token scanning and splicing shared by expr's
// pre-parse source rewrites (keyword fields, optional access, JSON
// literals, the map/if keywords).
//
// Each rewrite scans the source once with [Scan], plans a list of
// [Edit]s, and splices them with [Apply]. Apply also returns a [Map]
// from offsets in its output back to offsets in its input, so the
// compiler can report parse and validation errors at the position the
// user wrote, whatever the rewrites inserted before it.
package srcedit

import (
	"go/scanner"
	"go/token"
	"sort"
	"strings"
)

// Token is one significant token of a source string: [Pos, End) are
// byte offsets into it.
type Token struct {
	Pos, End int
	Kind     token.Token
	Lit      string
}

// Scan returns the significant tokens of src. Comments and the
// semicolons Go inserts at newlines are dropped, so "previous token"
// logic sees only what the user wrote; an explicit `;` is kept. The
// scanner reports `?` (and other stray bytes) as token.ILLEGAL.
func Scan(src string) []Token {
	fs := token.NewFileSet()
	file := fs.AddFile("", fs.Base(), len(src))
	var s scanner.Scanner
	// A no-op error handler keeps the scanner going through stray
	// bytes such as `?`; the parser reports real errors later.
	s.Init(file, []byte(src), func(token.Position, string) {}, 0)
	out := make([]Token, 0, len(src)/3+4)
	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		if tok == token.SEMICOLON && lit != ";" {
			continue
		}
		off := file.Offset(pos)
		out = append(out, Token{Pos: off, End: tokenEnd(src, off, tok, lit), Kind: tok, Lit: lit})
	}
	return out
}

// tokenEnd is the offset just past the token at off. The scanner's lit
// is not always the source text: it drops carriage returns from raw
// strings, so the end of a raw string is found in src.
func tokenEnd(src string, off int, tok token.Token, lit string) int {
	if tok == token.STRING && strings.HasPrefix(src[off:], "`") {
		if i := strings.IndexByte(src[off+1:], '`'); i >= 0 {
			return off + i + 2
		}
		return len(src)
	}
	if lit != "" {
		return off + len(lit)
	}
	return off + len(tok.String())
}

// Brackets returns, for each token index, the index of the token that
// closes it when it is `(`, `[` or `{`, and -1 otherwise (or when it is
// unclosed). Mismatched closers are skipped, so malformed input still
// pairs what it can and the parser reports the error.
func Brackets(toks []Token) []int {
	out := make([]int, len(toks))
	var stack []int
	for i, t := range toks {
		out[i] = -1
		switch t.Kind {
		case token.LPAREN, token.LBRACK, token.LBRACE:
			stack = append(stack, i)
		case token.RPAREN, token.RBRACK, token.RBRACE:
			n := len(stack)
			if n > 0 && closer(toks[stack[n-1]].Kind) == t.Kind {
				out[stack[n-1]] = i
				stack = stack[:n-1]
			}
		}
	}
	return out
}

func closer(open token.Token) token.Token {
	switch open {
	case token.LPAREN:
		return token.RPAREN
	case token.LBRACK:
		return token.RBRACK
	}
	return token.RBRACE
}

// Edit replaces src[Pos:End] with Str; Pos == End inserts.
type Edit struct {
	Pos, End int
	Str      string
}

// Apply splices edits into src. Edits must not overlap; they are applied
// in order of position, and insertions at the same offset keep their
// order. It returns the result and the map from its offsets back to
// src's.
func Apply(src string, edits []Edit) (string, Map) {
	if len(edits) == 0 {
		return src, Map{}
	}
	less := func(i, j int) bool { return edits[i].Pos < edits[j].Pos }
	if !sort.SliceIsSorted(edits, less) {
		sort.SliceStable(edits, less)
	}
	var b strings.Builder
	n := len(src)
	for _, e := range edits {
		n += len(e.Str) - (e.End - e.Pos)
	}
	b.Grow(n)
	m := Map{segs: make([]segment, 0, 2*len(edits)+1)}
	last := 0
	for _, e := range edits {
		m.add(b.Len(), last, false)
		b.WriteString(src[last:e.Pos])
		m.add(b.Len(), e.Pos, true)
		b.WriteString(e.Str)
		last = e.End
	}
	m.add(b.Len(), last, false)
	b.WriteString(src[last:])
	return b.String(), m
}

// Map translates offsets in a rewrite's output back to its input. The
// zero Map is the identity.
type Map struct {
	segs []segment
}

// segment starts at output offset out. A copied segment continues the
// input at in byte for byte; an inserted one (replacement text) maps
// every offset in it to in, where the replaced text began.
type segment struct {
	out, in  int
	inserted bool
}

func (m *Map) add(out, in int, inserted bool) {
	if k := len(m.segs); k > 0 && m.segs[k-1].out == out {
		m.segs[k-1] = segment{out, in, inserted}
		return
	}
	m.segs = append(m.segs, segment{out, in, inserted})
}

// Identity reports whether m maps every offset to itself because no
// edit was made.
func (m Map) Identity() bool { return len(m.segs) == 0 }

// Orig returns the input offset that output offset off came from.
func (m Map) Orig(off int) int {
	if len(m.segs) == 0 {
		return off
	}
	i := sort.Search(len(m.segs), func(i int) bool { return m.segs[i].out > off }) - 1
	if i < 0 {
		return off
	}
	s := m.segs[i]
	if s.inserted {
		return s.in
	}
	return s.in + off - s.out
}

// Then returns the map that applies m and then next: m translates the
// last rewrite's output to its input, next translates that input to an
// earlier one.
func (m Map) Then(next Map) Map {
	if len(next.segs) == 0 {
		return m
	}
	if len(m.segs) == 0 {
		return next
	}
	// Every boundary of either map is a boundary of the composition:
	// m's own, and the output offsets where m's copied segments cross
	// one of next's.
	var out Map
	for i, s := range m.segs {
		end := -1
		if i+1 < len(m.segs) {
			end = m.segs[i+1].out
		}
		if s.inserted {
			out.add(s.out, next.Orig(s.in), true)
			continue
		}
		inEnd := -1
		if end >= 0 {
			inEnd = s.in + end - s.out
		}
		// next's segment containing s.in, and every later one that
		// starts inside this copied range.
		j := sort.Search(len(next.segs), func(j int) bool { return next.segs[j].out > s.in }) - 1
		if j < 0 {
			out.add(s.out, s.in, false)
			j = 0
		}
		for ; j < len(next.segs); j++ {
			ns := next.segs[j]
			start := ns.out
			if start < s.in {
				start = s.in
			}
			if inEnd >= 0 && start >= inEnd {
				break
			}
			o := s.out + start - s.in
			if ns.inserted {
				out.add(o, ns.in, true)
			} else {
				out.add(o, ns.in+start-ns.out, false)
			}
		}
	}
	return out
}

// Segments calls fn with the start of each segment of m, in output
// order, with the input offset it maps to. Offsets after a copied
// segment's start advance with the input; an inserted segment's all map
// to in.
func (m Map) Segments(fn func(out, in int, inserted bool)) {
	for _, s := range m.segs {
		fn(s.out, s.in, s.inserted)
	}
}
