package expr

import (
	"context"
	"errors"
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
		`upper(classify.confidence)`:    "upper: argument 1 must be string, not number",
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
	// A field only one side has may be absent; one both sides have is
	// present, and stays nil-able only if a side says so.
	require.Equal(t, "{body?: string, extra?: number, verdict: string}", Join(a, b).String())
	c := ObjectOf(map[string]*Type{"verdict": String().OrNil(), "body": String()})
	require.Equal(t, "{body: string, verdict: string?}", Join(a, c).String())
	require.Equal(t, "{body?: string, extra?: number, verdict: string?}", Join(Join(a, b), c).String())
	// Against an open side, the field may also hold that side's Elem.
	open := &Type{Kind: KindObject, Open: true, Elem: Number()}
	require.Equal(t, "{body?: number | string, verdict?: number | string, ...}", Join(a, open).String())
	require.Equal(t, "{body?: any, verdict?: any, ...}", Join(a, OpenObject(nil)).String())
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
	require.Equal(t, 1, v)
}

// Regressions from the second review of #29. Each rejected expression
// also fails at run time with these values, and each accepted one runs.
func TestCheckMatchesRun(t *testing.T) {
	item := ObjectOf(map[string]*Type{"a": Number()})
	env := map[string]*Type{
		"s":      String().OrNil(),
		"w":      String().OrNil(),
		"xs":     ListOf(Number()),
		"ys":     ListOf(Number()).OrNil(),
		"a":      ObjectOf(map[string]*Type{"t": ListOf(String()), "n": Number().OrNil()}),
		"obj":    ObjectOf(map[string]*Type{"k": String()}),
		"scores": &Type{Kind: KindObject, Open: true, Elem: Number()},
		"k":      String(),
		"x":      ListOf(item),
		"xss":    ListOf(ListOf(item)),
		"map":    Number(),
		// Objects for joins: bx has every field of b, and y with
		// another type; nb is nil at run time and c is false, so the
		// joins below yield b, which lacks x.
		"b":  ObjectOf(map[string]*Type{"y": String()}),
		"bx": ObjectOf(map[string]*Type{"x": String(), "y": Number()}),
		"nb": ObjectOf(map[string]*Type{"x": String(), "y": Number()}).OrNil(),
		"c":  Bool(),
		// A host may mark a field absent itself: draft's title is
		// missing at run time.
		"draft": &Type{Kind: KindObject, Fields: map[string]*Type{"title": String()}, Absent: map[string]bool{"title": true}},
	}
	values := map[string]any{
		"b": map[string]any{"y": "s"}, "bx": map[string]any{"x": "v", "y": 1}, "nb": nil, "c": false, "draft": map[string]any{},
		"s": nil, "w": "x", "xs": []any{1, 2}, "ys": nil,
		"a":   map[string]any{"t": []any{"q"}, "n": nil},
		"obj": map[string]any{"k": "v"}, "scores": map[string]any{"alice": 1}, "k": "bob",
		"x": []any{map[string]any{"a": 1}}, "xss": []any{[]any{}}, "map": 3,
	}
	compile := func(src string) *Program {
		p, err := Compile(src, WithBuiltins(), WithFunctions(StringFuncs()), WithFunctions(MathFuncs()),
			WithFunctions(CollectionFuncs()), WithFunctions(map[string]any{
				"count":    func(xs []any) int { return len(xs) },
				"coalesce": coalesce,
			}))
		require.NoError(t, err, src)
		return p
	}
	coalesceType := WithFuncType("coalesce", func(args []*Type) (*Type, error) {
		var t *Type
		for _, a := range args {
			t = Join(t, a.NonNil())
		}
		return orAny(t), nil
	})
	for src, want := range map[string]string{
		// B1 is in kwfield_test.go. B2: a union is not Any.
		`upper(s || 0)`:           "may be number, and must be string",
		`len(s || 0)`:             "may be number",
		`sum(ys || "none")`:       "may be string, and must be a list",
		`xs[w || 0]`:              "the operation needs one kind",
		`(s || 0) == [1, 2]`:      "cannot compare",
		`(s || 0) > (w || "")`:    "the operation needs one kind",
		`sort(w || [1])`:          "may be string, and must be a list",
		`slice(xs, w || 0, 1)`:    "may be string, and must be number",
		`-(w || 0)`:               "the operation needs one kind",
		`{(s || 0): 1}`:           "the operation needs one kind",
		`has(obj, s || 0)`:        "may be number, and must be string",
		`join(xs, w || 0)`:        "must have string elements",
		`contains("abc", s || 0)`: "needs a string",
		`a.t[a.n]`:                "`a.n`: may be nil",
		`a.t?[a.n]`:               "`a.n`: may be nil",
		`"hello"["x"]`:            "string index must be a number",
		`obj[0]`:                  "object key must be a string",
		`scores[k]`:               "the key may be absent; use ?[",
		`obj[k]`:                  "the key may be absent; use ?[",
		`{1: "a"}`:                "an object key must be a string",
		`{s: "a"}`:                "`s`: may be nil",
		`upper()`:                 "upper expects 1 argument(s), got 0",
		`split("a", 1)`:           "split: argument 2 must be string, not number",
		`replace("a", "b")`:       "replace expects 3 argument(s)",
		`replace("a", 1, "c")`:    "replace: argument 2 must be string",
		`join([1, 2], ",")`:       "join: argument 1 must have string elements, not number",
		`join(["a"], nil)`:        "join: argument 2 is always nil",
		`sprintf(1)`:              "sprintf: argument 1 must be string",
		`sprintf()`:               "sprintf expects at least 1 argument(s)",
		`round("x")`:              "round: argument 1 must be number, not string",
		`abs(true)`:               "abs: argument 1 must be number, not bool",
		`min("a", "b")`:           "min: argument 1 must be number",
		`min()`:                   "min expects at least 1 argument(s)",
		`sum("x")`:                "sum: argument 1 must be a list",
		`sum(["a"])`:              "sum: argument 1 must have number elements",
		`sort("hello")`:           "sort: argument 1 must be a list, not string",
		`reverse("hello")`:        "reverse: argument 1 must be a list",
		`sort([s, "a"])`:          "elements that may be nil",
		`sortBy(xs, s)`:           "`s`: may be nil",
		`sortBy(xs, it > 1)`:      "sortBy: argument 2 must be number or string, not bool",
		`first(1)`:                "first: argument 1 must be a list",
		`last("s")`:               "last: argument 1 must be a list",
		`keys([1])`:               "keys: argument 1 must be an object",
		`entries(1)`:              "entries: argument 1 must be an object",
		`has([1], "a")`:           "has: argument 1 must be an object",
		`has(obj, nil)`:           "has: argument 2 is always nil",
		`slice(xs, 0)`:            "slice expects 3 argument(s)",
		`slice(xs, nil, 1)`:       "slice: argument 2 is always nil",
		`int([1])`:                "int: argument 1 must be number or string",
		`contains("abc", 1)`:      "contains: looking in string needs a string",
		`contains(obj, 1)`:        "contains: looking in an object needs a string",
		`first(x) != nil && any(xss, x, first(x).a > 1)`: "`first(x)`: may be nil",
		// A2.1: a field only one side of a join has may be absent.
		`[b, {"x": "q"}][0].x`:      `field "x" may be absent; use ?.`,
		`[b, bx][0].x`:              `field "x" may be absent; use ?.`,
		`[b, bx][0]["x"]`:           `field "x" may be absent; use ?[`,
		`if(c, bx, b).x`:            `field "x" may be absent; use ?.`,
		`(nb || b).x`:               `field "x" may be absent; use ?.`,
		`coalesce(nb, b).x`:         `field "x" may be absent; use ?.`,
		`upper(if(c, bx, b).x)`:     `field "x" may be absent; use ?.`,
		`[b, scores][0].alice`:      `field "alice" may be absent; use ?.`,
		`any([b, bx], it.x == "v")`: `field "x" may be absent; use ?.`,
		`[[b], [bx]][0][0].x`:       `field "x" may be absent; use ?.`,
		`{"o": if(c, bx, b)}.o.x`:   `field "x" may be absent; use ?.`,
		`draft.title`:               `field "title" may be absent; use ?.`,
	} {
		p := compile(src)
		_, err := p.Check(env, coalesceType)
		require.Error(t, err, src)
		require.Contains(t, err.Error(), want, src)
		_, err = p.Run(context.Background(), values)
		require.Error(t, err, "Run should fail too: "+src)
	}

	// Never true, though they do not fail. B5: literals are not folded
	// away before Check.
	for src, want := range map[string]string{
		`(s || 0) == true`: "is never true",
		`"a" == 1`:         "is never true",
		`"a" == "b"`:       `never "b"`,
		`1 != "1"`:         "is never true",
	} {
		_, err := compile(src).Check(env)
		require.Error(t, err, src)
		require.Contains(t, err.Error(), want, src)
	}

	for src, want := range map[string]string{
		`count(xs)`:                  "any",    // B6: a host function shadows a form
		`map + 1`:                    "number", // B7: env names that are keywords
		`scores?[k]`:                 "number?",
		`obj["k"]`:                   "string",
		`entries(scores)[0].value`:   "number",
		`slice(s, 0, 1)`:             "string | list<any>",
		`slice(ys, 0, 1)`:            "list<number>",
		`sort(xs)`:                   "list<number>",
		`sum(ys)`:                    "number",
		`keys(obj)`:                  "list<string>",
		`contains(xs, "a")`:          "bool",
		`len(scores) + len(s)`:       "number",
		`sprintf("%d-%v", 1, nil)`:   "string",
		`min(1)`:                     "number",
		`(s || 0) == 0`:              "bool",
		`try(scores[k], 0)`:          "number",
		`{(w || "k"): 1}`:            "{...}",
		`string(s) + upper(w || "")`: "string",
		// A2.1: ?. and ?[ read a field that may be absent as nil.
		`[b, {"x": "q"}][0]?.x`:       `"q"?`,
		`[b, bx][0]?["x"]`:            "string?",
		`if(c, bx, b)?.x`:             "string?",
		`(nb || b)?.x`:                "string?",
		`coalesce(nb, b)?.x`:          "string?",
		`try(if(c, bx, b).x, "none")`: "string",
		`any([b, bx], it?.x == "v")`:  "bool",
		// Fields both sides have are present, whatever their types.
		`if(c, bx, b).y`:             "number | string",
		`coalesce(nb, b).y`:          "number | string",
		`[b, {"y": "t"}][0].y`:       "string",
		`[b, scores][0]?.y`:          "(number | string)?",
		`draft?.title || "untitled"`: "string",
	} {
		p := compile(src)
		got, err := p.Check(env, coalesceType)
		require.NoError(t, err, src)
		require.Equal(t, want, got.String(), src)
		_, err = p.Run(context.Background(), values)
		require.NoError(t, err, src)
	}

	// An open object whose Elem is Any is untyped, like one with no Elem.
	anyElem := map[string]*Type{"m": &Type{Kind: KindObject, Open: true, Elem: Any}, "k": String()}
	for src, want := range map[string]string{`m[k]`: "any", `entries(m)[0].value`: "any"} {
		got, err := compile(src).Check(anyElem)
		require.NoError(t, err, src)
		require.Equal(t, want, got.String(), src)
	}

	// M2: a nil field type is Any when joined.
	require.Equal(t, "{k: any}", Join(ObjectOf(map[string]*Type{"k": nil}), ObjectOf(map[string]*Type{"k": String()})).String())
	require.Equal(t, "{k?: any}", Join(ObjectOf(map[string]*Type{"k": nil}), ObjectOf(map[string]*Type{})).String())
}

func TestCheckHints(t *testing.T) {
	for src, want := range map[string]string{
		`any([1], item, iten > 1)`: `undefined name "iten" (did you mean "item"?)`,
		`uper("a")`:                `undefined function "uper" (did you mean "upper"?)`,
		`fiter([1], it > 1)`:       `undefined function "fiter" (did you mean "filter"?)`,
	} {
		p, err := Compile(src, WithBuiltins())
		require.NoError(t, err, src)
		_, err = p.Check(nil)
		require.Error(t, err, src)
		require.Contains(t, err.Error(), want, src)
	}
}

func TestCheckIndexReportsOnce(t *testing.T) {
	_, err := checkSrc(t, `report["verdict"]`)
	var te *TypeError
	require.True(t, errors.As(err, &te))
	require.Len(t, te.Problems, 1, strings.Join(te.Problems, "\n"))
	_, err = checkSrc(t, `try(findings.items[nil], 0)`)
	require.NoError(t, err)
}

// coalesce returns its first non-nil argument, like the host function
// workflows register.
func coalesce(args ...any) any {
	for _, a := range args {
		if a != nil {
			return a
		}
	}
	return nil
}
