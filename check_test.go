package expr

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/deepnoodle-ai/expr/internal/require"
)

var checkEnv = map[string]*Type{
	"classify": ObjectOf(map[string]*Type{
		"kind":       EnumOf("bug_fix", "feature", "other"),
		"confidence": Number(),
	}),
	"findings": ObjectOf(map[string]*Type{
		"items": ListOf(ObjectOf(map[string]*Type{"title": String(), "severity": Number()})),
	}),
	"report": ObjectOf(map[string]*Type{"verdict": String(), "body": String()}).OrNil(),
	"event":  OpenObject(map[string]*Type{"pull_request": OpenObject(map[string]*Type{"number": Number()})}),
	"inputs": ObjectOf(map[string]*Type{"note": String().OrNil(), "repo": String()}),
	"blob":   Any,
}

func checkSrc(t *testing.T, src string) (*Type, error) {
	t.Helper()
	p, err := Compile(src, WithBuiltins(), WithFunctions(StringFuncs()), WithFunctions(CollectionFuncs()))
	require.NoError(t, err, src)
	return p.Check(checkEnv)
}

func TestCheckAccepts(t *testing.T) {
	for src, want := range map[string]string{
		`classify.kind == "bug_fix"`:                            "bool",
		`classify.confidence > 0.5 && classify.kind != "other"`: "bool",
		`findings.items`:                                        "list<{severity: number, title: string}>",
		`map(findings.items, it.title)`:                         "list<string>",
		`filter(findings.items, f, f.severity > 2)`:             "list<{severity: number, title: string}>",
		`len(findings.items) + 1`:                               "number",
		`report?.verdict`:                                       "string?",
		`try(report.verdict, "none")`:                           "string",
		`event.pull_request.number`:                             "number",
		`event.anything.at.all`:                                 "any",
		`blob.x.y[3]`:                                           "any",
		`"#" + string(event.pull_request.number)`:               "string",
		`{"n": len(findings.items), "k": classify.kind}`:        `{k: "bug_fix" | "feature" | "other", n: number}`,
		`[classify.kind, "x"]`:                                  `list<"bug_fix" | "feature" | "other" | "x">`,
		`first(findings.items)?.title`:                          "string?",
		`inputs.note == nil`:                                    "bool",
		`if(classify.confidence > 1, "high", "low")`:            `"high" | "low"`,
		`upper(inputs.repo)`:                                    "string",
		`findings.items | map(it.severity) | sum()`:             "number",
		`find(findings.items, it.severity > 3)`:                 "{severity: number, title: string}?",
	} {
		got, err := checkSrc(t, src)
		require.NoError(t, err, src)
		require.Equal(t, want, got.String(), src)
	}
}

func TestCheckRejects(t *testing.T) {
	for src, want := range map[string]string{
		`classify.knd`:                  `no field "knd" in {confidence: number, kind: "bug_fix" | "feature" | "other"} (did you mean "kind"?)`,
		`classify.kind == "bugfix"`:     `never "bugfix"`,
		`classify.kind == 3`:            "comparing",
		`report.verdict`:                "`report`: may be nil here",
		`inputs.note + "!"`:             "may be nil",
		`classif.kind`:                  `undefined name "classif" (did you mean "classify"?)`,
		`findings.items.title`:          `cannot read field "title" of list`,
		`map(classify, it)`:             "map expects a list",
		`classify.confidence + "x"`:     "cannot add number and",
		`-inputs.repo`:                  "not a number",
		`upper(classify.confidence)`:    "upper expects a string",
		`nosuch(1)`:                     `undefined function "nosuch"`,
		`map(findings.items, it.titel)`: `no field "titel"`,
		`findings.items["x"]`:           "list index must be a number",
		`classify?.missing`:             `no field "missing"`,
	} {
		_, err := checkSrc(t, src)
		require.Error(t, err, src)
		require.True(t, errors.Is(err, ErrType), src)
		require.Contains(t, err.Error(), want, src)
	}
}

func TestCheckReportsEveryProblem(t *testing.T) {
	_, err := checkSrc(t, `classify.knd == "x" || report.body == "" || nope`)
	var te *TypeError
	require.True(t, errors.As(err, &te))
	require.Len(t, te.Problems, 3, strings.Join(te.Problems, "\n"))
}

func TestCheckFuncTypes(t *testing.T) {
	p, err := Compile(`concat(findings.items, findings.items)`, WithFunctions(map[string]any{
		"concat": func(a, b []any) []any { return append(a, b...) },
	}))
	require.NoError(t, err)
	got, err := p.Check(checkEnv)
	require.NoError(t, err)
	require.Equal(t, "any", got.String())

	got, err = p.Check(checkEnv, WithFuncType("concat", func(args []*Type) (*Type, error) {
		var elem *Type
		for _, a := range args {
			elem = Join(elem, a.Elem)
		}
		return ListOf(elem), nil
	}))
	require.NoError(t, err)
	require.Equal(t, "list<{severity: number, title: string}>", got.String())
}

func TestCheckTemplate(t *testing.T) {
	tmpl, err := NewTemplate(`PR #${event.pull_request.number}: ${classify.knd}`, WithBuiltins())
	require.NoError(t, err)
	err = tmpl.Check(checkEnv)
	require.Error(t, err)
	require.Contains(t, err.Error(), `no field "knd"`)
}

func TestJoin(t *testing.T) {
	a := ObjectOf(map[string]*Type{"verdict": String(), "body": String()})
	b := ObjectOf(map[string]*Type{"verdict": EnumOf("APPROVE"), "extra": Number()})
	require.Equal(t, "{body: string?, extra: number?, verdict: string}", Join(a, b).String())
	require.Equal(t, "string?", Join(String(), Null()).String())
	require.Equal(t, "number | string", Join(String(), Number()).String())
}

// Regressions from review: each was accepted by Check and failed in Run, or
// the reverse.
func TestCheckMatchesRuntime(t *testing.T) {
	env := map[string]*Type{
		"a": ObjectOf(map[string]*Type{
			"n":     Number().OrNil(),
			"name":  String().OrNil(),
			"items": ListOf(String()).OrNil(),
			"flag":  Bool(),
			"t":     ListOf(String()),
		}),
		"scores": &Type{Kind: KindObject, Open: true, Elem: Number()},
		"loose":  OpenObject(nil),
	}
	check := func(src string) (*Type, error) {
		p, err := Compile(src, WithBuiltins(), WithFunctions(StringFuncs()), WithFunctions(MathFuncs()))
		require.NoError(t, err, src)
		return p.Check(env)
	}
	for src, want := range map[string]string{
		`a.flag && a.flag`:                     "bool",
		`a.items && len(a.items) > 0`:          "(bool | list<string>)?",
		`a.name || "anon"`:                     "string",
		`a.n != nil && a.n > 3`:                "bool",
		`a.n == nil || a.n > 3`:                "bool",
		`if(a.name != nil, upper(a.name), "")`: "string",
		`scores?.alice`:                        "number?",
		`loose.anything`:                       "any",
		`len(a.items)`:                         "number",
		`string(a.n)`:                          "string",
	} {
		got, err := check(src)
		require.NoError(t, err, src)
		require.Equal(t, want, got.String(), src)
	}
	for src, want := range map[string]string{
		`upper(a.name)`:         "may be nil",
		`round(a.n)`:            "may be nil",
		`try(a.n)`:              "try expects 2 arguments",
		`if(a.flag, 1)`:         "if expects 3 arguments",
		`a.t == a.t`:            "cannot compare",
		`scores.alice + 1`:      `field "alice" may be absent`,
		`a.n > 3 && a.n != nil`: "may be nil",
	} {
		_, err := check(src)
		require.Error(t, err, src)
		require.Contains(t, err.Error(), want, src)
	}
}

func TestUnions(t *testing.T) {
	env := map[string]*Type{"s": String().OrNil(), "n": Number(), "l": ListOf(String()).OrNil()}
	check := func(src string) (*Type, error) {
		p, err := Compile(src, WithBuiltins())
		require.NoError(t, err, src)
		return p.Check(env)
	}
	got, err := check(`s || 0`)
	require.NoError(t, err)
	require.Equal(t, "0 | string", got.String())
	require.True(t, got.IsUnion())

	got, err = check(`l && len(l) > 0`)
	require.NoError(t, err)
	require.Equal(t, "(bool | list<string>)?", got.String())

	for _, src := range []string{`(s || 0) + 1`, `(s || 0) > 1`, `(s || 0).x`} {
		_, err := check(src)
		require.Error(t, err, src)
		require.Contains(t, err.Error(), "the operation needs one kind", src)
	}
	require.Equal(t, "string", Join(EnumOf("a"), String()).String())
}

// Regressions from review of #29.
func TestCheckReviewFixes(t *testing.T) {
	env := map[string]*Type{
		"obj": ObjectOf(map[string]*Type{"items": nil, "k": nil}),
		"x":   ObjectOf(map[string]*Type{"a": Number()}).OrNil(),
		"xs":  ListOf(ObjectOf(map[string]*Type{"a": Number()}).OrNil()),
		"e":   EnumOf("a"),
		"z":   Null(),
	}
	check := func(src string, opts ...Option) error {
		p, err := Compile(src, append([]Option{WithBuiltins(), WithFunctions(CollectionFuncs())}, opts...)...)
		require.NoError(t, err, src)
		_, err = p.Check(env)
		return err
	}
	// S1: nil field types are unknown, never a panic.
	for _, src := range []string{`first(obj.items)`, `reverse(obj.items)`, `obj[obj.k]`, `obj.items.deep`} {
		require.NoError(t, check(src), src)
	}
	// S2: a binding that shadows a narrowed name is not narrowed.
	err := check(`x != nil && any(xs, x, x.a > 1)`)
	require.Error(t, err)
	require.Contains(t, err.Error(), "may be nil")
	require.NoError(t, check(`x != nil && any(xs, y, y != nil && y.a > 1 && x.a > 0)`))
	// S3: a replaced builtin does not keep the builtin's type.
	require.NoError(t, check(`len(5)`, WithFunctions(map[string]any{"len": func(n float64) float64 { return n }})))
	require.Error(t, check(`len(5)`))
	// S4: indexing a value that is always nil.
	require.Error(t, check(`z[0]`))
	// S5: single-value enums are compared too.
	err = check(`e == "b"`)
	require.Error(t, err)
	require.Contains(t, err.Error(), `never "b"`)
	require.NoError(t, check(`e == "a"`))
}

func TestKeywordPrefixOnlyRestoresKeywords(t *testing.T) {
	p, err := Compile(`x.__expr_kw_foo`)
	require.NoError(t, err)
	v, err := p.Run(context.Background(), map[string]any{"x": map[string]any{"__expr_kw_foo": 1}})
	require.NoError(t, err)
	require.Equal(t, "1", fmt.Sprint(v))
}
