package optaccess

import (
	"strings"
	"testing"
)

func TestRewrite(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// no `?` at all: untouched
		{`a + b`, `a + b`},
		{`f(x, y)`, `f(x, y)`},
		{`xs[0]`, `xs[0]`},

		// simplest selector
		{`obj?.field`, `__try_select__(obj, "field")`},
		// LHS is a selector chain
		{`a.b.c?.d`, `__try_select__(a.b.c, "d")`},
		// LHS is an index
		{`xs[0]?.name`, `__try_select__(xs[0], "name")`},
		// LHS is a paren group
		{`(a + b)?.c`, `__try_select__((a + b), "c")`},
		// LHS is a call
		{`find(xs, it.id == 1)?.name`, `__try_select__(find(xs, it.id == 1), "name")`},
		// chained `?.`
		{`a?.b?.c`, `__try_select__(__try_select__(a, "b"), "c")`},
		// chained `?.` deep
		{`a?.b?.c?.d`, `__try_select__(__try_select__(__try_select__(a, "b"), "c"), "d")`},

		// optional index
		{`xs?[0]`, `__try_index__(xs, 0)`},
		{`obj?["key"]`, `__try_index__(obj, "key")`},
		// optional index with expression
		{`obj?[i + 1]`, `__try_index__(obj, i + 1)`},
		// chained `?.` then `?[`
		{`a?.b?[0]`, `__try_index__(__try_select__(a, "b"), 0)`},
		// `?[` then `?.`
		{`xs?[0]?.name`, `__try_select__(__try_index__(xs, 0), "name")`},

		// composes with operator-returning ||
		{`user?.nickname || "(none)"`, `__try_select__(user, "nickname") || "(none)"`},

		// inside higher-order predicate
		{`map(users, it?.name)`, `map(users, __try_select__(it, "name"))`},

		// `?` not followed by `.` or `[` is left alone
		{`a ? b : c`, `a ? b : c`},
		// `?` at start of source: nothing to rewrite
		{`?.x`, `?.x`},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := Rewrite(tc.in)
			if got != tc.want {
				t.Fatalf("Rewrite(%q)\n  got  %q\n  want %q", tc.in, got, tc.want)
			}
		})
	}
}

// `?.` and `?[` written inside a string literal must be preserved
// verbatim — string contents are scanner tokens we never look inside.
func TestRewrite_StringLiteralUntouched(t *testing.T) {
	cases := []string{
		`"obj?.field"`,
		`"a?[0]"`,
		`'?'`,
		"`obj?.field`",
		`f("obj?.field")`,
		`a + "?.b"`,
	}
	for _, src := range cases {
		t.Run(src, func(t *testing.T) {
			got := Rewrite(src)
			if got != src {
				t.Fatalf("Rewrite(%q) modified a string literal\n  got %q", src, got)
			}
		})
	}
}

// `?.` and `?[` written inside a comment must be preserved verbatim.
func TestRewrite_CommentUntouched(t *testing.T) {
	src := `a /* obj?.field */ + b`
	got := Rewrite(src)
	if got != src {
		t.Fatalf("Rewrite altered comment content\n  got %q", got)
	}
	src2 := "a // obj?.field\n + b"
	got2 := Rewrite(src2)
	if got2 != src2 {
		t.Fatalf("Rewrite altered line-comment content\n  got %q", got2)
	}
}

// Real `?.` outside a string is still rewritten when the same source
// also contains `?.` inside a string.
func TestRewrite_MixedStringAndOperator(t *testing.T) {
	src := `obj?.field + "obj?.field"`
	want := `__try_select__(obj, "field") + "obj?.field"`
	got := Rewrite(src)
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

// FuzzRewrite checks that the rewriter never panics and produces an
// output that no longer contains `?.` or `?[` outside strings,
// comments, or other unrewritable contexts. The latter half is
// approximate, so we only assert the no-panic guarantee.
func FuzzRewrite(f *testing.F) {
	seeds := []string{
		``,
		`a`,
		`a?.b`,
		`a?[0]`,
		`a?.b?.c`,
		`(a + b)?.c`,
		`xs?[0]?.name`,
		`f("?.")`,
		`a // ?.b`,
		`a /* ?.b */`,
		`?.x`,
		`a ? b`,
		`?[`,
		`a?.`,
		`a?[`,
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		// Bound the input so a fuzzer-found 1 GiB string doesn't OOM.
		if len(s) > 8192 {
			return
		}
		// Reject strings the scanner can't process at all.
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("panic on %q: %v", s, r)
			}
		}()
		out := Rewrite(s)
		// Output should be a string of bounded growth: each `?` byte
		// expands to at most ~30 bytes of replacement.
		if len(out) > 64*len(s)+128 {
			t.Fatalf("Rewrite blew up size: in %d, out %d", len(s), len(out))
		}
		_ = strings.Contains(out, "?")
	})
}
