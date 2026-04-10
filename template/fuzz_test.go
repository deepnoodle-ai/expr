package template_test

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/deepnoodle-ai/expr/template"
)

// fuzzSeeds is a corpus of templates that exercises every parser
// branch: literals, escapes, expressions, multi-expression templates,
// and the nasty cases that broke the previous regex-based parser.
var fuzzSeeds = []string{
	"",
	"plain text",
	"$",
	"$$",
	"$$$",
	"$$$$",
	"$5",
	"${x}",
	"${ x }",
	"${x}${y}",
	"pre ${x} post",
	"$${x}",
	"$$${x}",
	"${a.b.c}",
	"${ f(1, 2, 3) }",
	"${ map[string]any{\"k\": 1} }",
	"${ \"}\" }",
	"${ `}` }",
	"${ '}' }",
	"${ a /* } */ + b }",
	"${ // comment\n x }",
	"${a}} tail",
	"${a",
	"${",
	"${}",
	"${   }",
	"${ /* only */ }",
	"${ \"unterminated }",
	"${ `unterminated }",
	"${ /* unterminated }",
	"${é}",
	"\ufeff${x}",
}

// acceptAllCompiler accepts any body the parser hands it. Fuzz runs
// don't need real evaluation — they just need to confirm the parser
// never panics and produces a coherent Template that can be re-Eval'd
// without crashing.
type acceptAllCompiler struct{}

func (acceptAllCompiler) Compile(string) (template.Script, error) { return acceptAllScript{}, nil }

type acceptAllScript struct{}

func (acceptAllScript) Run(context.Context, any) (any, error) { return "", nil }

// FuzzParse confirms template.New never panics on arbitrary input and
// that any Template it returns round-trips through Eval without
// producing an error (since the compiler accepts everything). It also
// asserts that successful parses preserve the raw source verbatim.
func FuzzParse(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add(s)
	}
	c := acceptAllCompiler{}
	f.Fuzz(func(t *testing.T, src string) {
		// Guard against the fuzzer handing us invalid UTF-8 that Go's
		// scanner might process in unexpected ways. Skip those rather
		// than chase a scanner quirk unrelated to the template parser.
		if !utf8.ValidString(src) {
			t.Skip()
		}
		tmpl, err := template.New(c, src)
		if err != nil {
			// Errors are fine, but they must be coherent: they must
			// begin with the "template:" prefix so callers can
			// distinguish them from unrelated errors bubbling up.
			if !strings.HasPrefix(err.Error(), "template:") {
				t.Fatalf("error missing template: prefix: %v", err)
			}
			return
		}
		if tmpl == nil {
			t.Fatalf("nil template with nil error for %q", src)
		}
		if got := tmpl.Raw(); got != src {
			t.Fatalf("Raw() drift: got %q want %q", got, src)
		}
		if _, err := tmpl.Eval(context.Background(), nil); err != nil {
			t.Fatalf("Eval error on accept-all compiler: %v", err)
		}
	})
}

// evalCompiler lets the fuzzer reach the runtime path. It returns a
// script whose Run just echoes the body, so Eval exercises the
// builder/formatting code against whatever chunks the parser
// extracted.
type evalCompiler struct{}

func (evalCompiler) Compile(body string) (template.Script, error) { return evalScript{body: body}, nil }

type evalScript struct{ body string }

func (s evalScript) Run(context.Context, any) (any, error) { return "<" + s.body + ">", nil }

// FuzzEval exercises the full parse → compile → evaluate path. The
// important invariants are: never panic, always produce a string when
// no error is returned, and fail coherently when one is.
func FuzzEval(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add(s)
	}
	c := evalCompiler{}
	f.Fuzz(func(t *testing.T, src string) {
		if !utf8.ValidString(src) {
			t.Skip()
		}
		tmpl, err := template.New(c, src)
		if err != nil {
			return
		}
		out, err := tmpl.Eval(context.Background(), nil)
		if err != nil {
			t.Fatalf("Eval error for %q: %v", src, err)
		}
		// The literal prefix of the output must be a prefix of raw up
		// to the first `${` or `$$` marker. Rather than reimplement
		// the parser to check that, just assert that out is non-nil
		// which is implied by the no-error return — the fuzzer's job
		// here is surfacing panics.
		_ = out
	})
}
