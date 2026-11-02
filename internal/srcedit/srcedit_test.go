package srcedit

import (
	"go/token"
	"math/rand"
	"strings"
	"testing"
)

func TestScanEnds(t *testing.T) {
	// The scanner drops '\r' from a raw string's literal; End must still
	// be the end of the token in the source.
	src := "`a\r\nb` + x /* c\r\n */ ?.y"
	toks := Scan(src)
	var kinds []token.Token
	for _, tk := range toks {
		kinds = append(kinds, tk.Kind)
		if tk.Pos < 0 || tk.End > len(src) || tk.Pos >= tk.End {
			t.Fatalf("bad range %+v", tk)
		}
	}
	if got := src[toks[0].Pos:toks[0].End]; got != "`a\r\nb`" {
		t.Fatalf("raw string token is %q", got)
	}
	want := []token.Token{token.STRING, token.ADD, token.IDENT, token.ILLEGAL, token.PERIOD, token.IDENT}
	if len(kinds) != len(want) {
		t.Fatalf("kinds %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("kinds %v, want %v", kinds, want)
		}
	}
}

func TestScanKeepsExplicitSemicolon(t *testing.T) {
	toks := Scan("a\nb;c")
	if len(toks) != 4 || toks[2].Kind != token.SEMICOLON {
		t.Fatalf("got %+v", toks)
	}
}

func TestBrackets(t *testing.T) {
	// The `[` before `0` is unclosed, and the `)` in "[)" closes nothing.
	toks := Scan("f([a, {b: (c)}])[0 + [)")
	got := Brackets(toks)
	want := map[int]int{1: 13, 2: 12, 5: 11, 8: 10}
	for i, c := range got {
		if w, ok := want[i]; ok {
			if c != w {
				t.Errorf("token %d (%v) closes at %d, want %d", i, toks[i].Kind, c, w)
			}
		} else if c != -1 {
			t.Errorf("token %d (%v) closes at %d, want unpaired", i, toks[i].Kind, c)
		}
	}
}

// randomEdits returns non-overlapping edits of src.
func randomEdits(r *rand.Rand, src string) []Edit {
	var edits []Edit
	pos := 0
	for pos < len(src) {
		pos += r.Intn(4)
		if pos > len(src) {
			break
		}
		end := pos + r.Intn(3)
		if end > len(src) {
			end = len(src)
		}
		edits = append(edits, Edit{Pos: pos, End: end, Str: strings.Repeat("#", r.Intn(4))})
		pos = end + 1
	}
	return edits
}

func randomSource(r *rand.Rand) string {
	const alphabet = "abcdefg \n"
	b := make([]byte, r.Intn(30))
	for i := range b {
		b[i] = alphabet[r.Intn(len(alphabet))]
	}
	return string(b)
}

// copiedOffsets reports, for each output offset, whether Apply copied it
// from the input (rather than inserting it).
func checkMap(t *testing.T, src string, edits []Edit, out string, m Map) {
	t.Helper()
	inserted := make([]bool, len(out)+1)
	o := 0
	last := 0
	for _, e := range edits {
		o += e.Pos - last
		for k := 0; k < len(e.Str); k++ {
			inserted[o+k] = true
		}
		o += len(e.Str)
		last = e.End
	}
	for off := 0; off < len(out); off++ {
		in := m.Orig(off)
		if in < 0 || in > len(src) {
			t.Fatalf("%q -> %q: Orig(%d) = %d out of range", src, out, off, in)
		}
		if !inserted[off] && src[in] != out[off] {
			t.Fatalf("%q -> %q: Orig(%d) = %d maps %q to %q", src, out, off, in, out[off], src[in])
		}
	}
	if got := m.Orig(len(out)); got != len(src) {
		t.Fatalf("%q -> %q: Orig(end) = %d, want %d", src, out, got, len(src))
	}
}

func TestApplyMap(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	for i := 0; i < 5000; i++ {
		src := randomSource(r)
		edits := randomEdits(r, src)
		out, m := Apply(src, append([]Edit(nil), edits...))
		checkMap(t, src, edits, out, m)
	}
}

func TestThenComposes(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	for i := 0; i < 5000; i++ {
		s0 := randomSource(r)
		s1, m1 := Apply(s0, randomEdits(r, s0))
		s2, m2 := Apply(s1, randomEdits(r, s1))
		s3, m3 := Apply(s2, randomEdits(r, s2))
		all := m3.Then(m2).Then(m1)
		for off := 0; off <= len(s3); off++ {
			want := m1.Orig(m2.Orig(m3.Orig(off)))
			if got := all.Orig(off); got != want {
				t.Fatalf("%q -> %q -> %q -> %q: composed Orig(%d) = %d, want %d", s0, s1, s2, s3, off, got, want)
			}
		}
	}
}

func TestZeroMapIsIdentity(t *testing.T) {
	var m Map
	if m.Orig(7) != 7 || m.Then(Map{}).Orig(3) != 3 {
		t.Fatal("zero map is not the identity")
	}
	out, m := Apply("abc", nil)
	if out != "abc" || m.Orig(2) != 2 {
		t.Fatal("no edits is not the identity")
	}
}
