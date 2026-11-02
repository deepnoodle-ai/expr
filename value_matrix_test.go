package expr

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
)

// The value matrix runs one set of expressions over a common set of Go
// values through every evaluation path that applies, and checks that
// the paths agree and that the documented relations between operators
// hold. The paths are the env shape (a map, so lookups take the
// type-asserted branches, or a struct, so selector and index chains
// take the reflect-space fast paths) crossed with optimization (on, or
// off via withoutOptimizations).

type mxInner struct{ B any }

type mxEmb struct{ E int }

type mxStruct struct {
	A      any
	N      int
	L      []mxInner
	M      map[string]any
	P      *mxInner
	hidden int //nolint:unused // read reflectively: must stay invisible
}

// mxWithEmbPtr promotes E through a nil embedded pointer.
type mxWithEmbPtr struct {
	*mxEmb
	A int
}

type (
	mxID     int64
	mxName   string
	mxFlag   bool
	mxHolder struct{ V any }
	mxSelf   *mxSelf
)

type mxErr struct{ msg string }

func (e mxErr) Error() string { return e.msg }

type mxPtrErr struct{ msg string }

func (e *mxPtrErr) Error() string { return e.msg }

type namedValue struct {
	name string
	v    any
}

// matrixValues is the shared set of host values. Cyclic values are not
// here: a regression in cycle handling overflows the stack, which no
// in-process test survives, so they live in isolated_test.go.
func matrixValues() []namedValue {
	s := mxStruct{A: map[string]any{"B": 1}, N: 2, L: []mxInner{{B: "b0"}, {B: nil}}, M: map[string]any{"a": 1}, P: &mxInner{B: 3}}
	ps := &s
	return []namedValue{
		{"untyped nil", nil},
		{"nil *struct", (*mxStruct)(nil)},
		{"struct", s},
		{"*struct", ps},
		{"**struct", &ps},
		{"zero struct", mxStruct{}},
		{"embedded nil ptr", mxWithEmbPtr{A: 1}},
		{"embedded ptr", mxWithEmbPtr{mxEmb: &mxEmb{E: 4}, A: 1}},
		{"holder of slice", mxHolder{V: []int{1}}},
		{"holder of int", mxHolder{V: 1}},
		{"array of slice", [1]any{[]int{1}}},
		{"[]int", []int{1, 2}},
		{"nil []int", []int(nil)},
		{"empty []any", []any{}},
		{"[]any of maps", []any{map[string]any{"a": 1}, nil}},
		{"[]any nested", []any{[]any{"x"}}},
		{"[3]int", [3]int{7, 8, 9}},
		{"*[3]int", &[3]int{7, 8, 9}},
		{"string", "héllo"},
		{"empty string", ""},
		{"named string", mxName("ab")},
		{"map[string]any", map[string]any{"a": 1, "A": map[string]any{"B": 2}, "B": nil}},
		{"map[string]int", map[string]int{"a": 1}},
		{"nil map[string]int", map[string]int(nil)},
		{"map[mxName]int", map[mxName]int{"a": 1}},
		{"map[any]any", map[any]any{1: "x", "a": 2, int64(0): "zero"}},
		{"map[[2]int]string", map[[2]int]string{{1, 2}: "x"}},
		{"map[int]string", map[int]string{0: "z", 1: "o"}},
		{"map[mxID]string", map[mxID]string{1: "one"}},
		{"map[int8]string", map[int8]string{1: "one"}},
		{"int", 1},
		{"int8", int8(5)},
		{"int64 min", int64(math.MinInt64)},
		{"uint64 max", uint64(math.MaxUint64)},
		{"uint64 max-1", uint64(math.MaxUint64 - 1)},
		{"mxID 2^53+1", mxID(1<<53 + 1)},
		{"float32", float32(1.5)},
		{"float64 int", float64(1)},
		{"bool", true},
		{"named bool", mxFlag(true)},
		{"mxErr", mxErr{"x"}},
		{"*mxPtrErr", &mxPtrErr{"y"}},
		{"nil *mxPtrErr", (*mxPtrErr)(nil)},
		{"nil func", (func() int)(nil)},
		{"func", func() int { return 1 }},
		{"nil Func", Func(nil)},
		{"nil chan", (chan int)(nil)},
	}
}

// Expressions over V alone, and over V with a key K or an index I.
var (
	mxExprs = []string{
		"V", "V.A", "V?.A", "V.a", "V?.a", "V.A.B", "V?.A?.B", "V.E", "V?.E",
		"V.hidden", "V?.hidden", "V.P.B", "V?.P?.B", "V.L[0].B", "V.L[1].B", "V?.L?[5]",
		"V[0]", "V?[0]", "V[1]", "V?[1]", "V[5]", "V?[5]", `V["a"]`, `V?["a"]`,
		"V[0][0]", "V?[0]?[0]", "V[0].a", "V?[0]?.a",
		"V == V", "V != V", "V == nil", "V != nil", "!V", "bool(V)", "if(V, 1, 2)",
		"len(V)", "string(V)", `sprintf("%v", V)`, "V | len()",
		"V + 1", "V - 1", "V < 1", "V >= 0", "-V", "V == 9007199254740992", "V == 18446744073709551614.0",
		"contains([V], V)", "[V][0]", `{"x": V}.x`, `try(V.A, "d")`, `try(V[0], "d")`,
		"filter([V], true)[0]", "filter([V, V], true)[1]", "map([V], it)[0]", "count([V], it == V)",
		"keys(V)", "has(V, \"a\")",
	}
	mxKeyExprs   = []string{"V[K]", "V?[K]", "V.M[K]", "V?.M?[K]", "try(V[K], \"d\")"}
	mxKeys       = []any{"a", "A", "missing", 1, int64(0), mxName("a"), []int{1, 2}, []any{1}, nil, 1.5}
	mxIndexExprs = []string{"V[I]", "V?[I]", "V.L[I]", "V?.L?[I]", "try(V[I], \"d\")"}
	mxIndexes    = []any{0, 1, 5, -1, 1.0, 1.5, "a", nil, uint64(math.MaxUint64), mxID(1)}
)

// mxEnv is the struct form of the matrix env.
type mxEnv struct{ V, K, I any }

type mxOutcome struct {
	value    any
	category string // "", "evaluate", "compile", or "UNWRAPPED"
	msg      string
	panicked any
}

func (o mxOutcome) String() string {
	if o.panicked != nil {
		return fmt.Sprintf("panic(%v)", o.panicked)
	}
	if o.category != "" {
		return o.category + ": " + o.msg
	}
	return fmt.Sprintf("%#v", o.value)
}

func errCategory(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrCompile):
		return "compile"
	case errors.Is(err, ErrEvaluate):
		return "evaluate"
	}
	return "UNWRAPPED"
}

func mxRun(src string, env any, opts ...Option) (out mxOutcome) {
	defer func() {
		if r := recover(); r != nil {
			out = mxOutcome{panicked: r}
		}
	}()
	p, err := Compile(src, append([]Option{WithBuiltins()}, opts...)...)
	if err != nil {
		return mxOutcome{category: errCategory(err), msg: err.Error()}
	}
	v, err := p.Run(context.Background(), env)
	if err != nil {
		return mxOutcome{category: errCategory(err), msg: err.Error()}
	}
	return mxOutcome{value: v}
}

func sameOutcome(a, b mxOutcome) bool {
	if a.panicked != nil || b.panicked != nil {
		return false
	}
	return a.category == b.category && a.msg == b.msg && mxDeepEqual(a.value, b.value)
}

// mxDeepEqual is reflect.DeepEqual, except that funcs are equal when
// they are the same function (DeepEqual only equates nil funcs).
func mxDeepEqual(a, b any) bool {
	if a != nil && b != nil && reflect.TypeOf(a).Kind() == reflect.Func && reflect.TypeOf(a) == reflect.TypeOf(b) {
		return reflect.ValueOf(a).Pointer() == reflect.ValueOf(b).Pointer()
	}
	return reflect.DeepEqual(a, b)
}

type mxCase struct {
	src  string
	k, i any
}

func mxCases() []mxCase {
	var cases []mxCase
	for _, src := range mxExprs {
		cases = append(cases, mxCase{src: src})
	}
	for _, src := range mxKeyExprs {
		for _, k := range mxKeys {
			cases = append(cases, mxCase{src: src, k: k})
		}
	}
	for _, src := range mxIndexExprs {
		for _, i := range mxIndexes {
			cases = append(cases, mxCase{src: src, i: i})
		}
	}
	return cases
}

// mxEval runs c over v through every path and returns the outcome
// they agree on, reporting any disagreement or panic.
func mxEval(t *testing.T, label string, c mxCase, v any) mxOutcome {
	t.Helper()
	mapEnv := map[string]any{"V": v, "K": c.k, "I": c.i}
	structEnv := mxEnv{V: v, K: c.k, I: c.i}
	paths := []struct {
		name string
		out  mxOutcome
	}{
		{"map", mxRun(c.src, mapEnv)},
		{"map/noopt", mxRun(c.src, mapEnv, withoutOptimizations())},
		{"struct", mxRun(c.src, structEnv)},
		{"struct/noopt", mxRun(c.src, &structEnv, withoutOptimizations())},
	}
	for _, p := range paths {
		if p.out.panicked != nil {
			t.Errorf("%s: %s via %s panicked: %v", label, c.src, p.name, p.out.panicked)
		}
		if p.out.category == "UNWRAPPED" {
			t.Errorf("%s: %s via %s: error does not wrap ErrEvaluate/ErrCompile: %s", label, c.src, p.name, p.out.msg)
		}
	}
	for _, p := range paths[1:] {
		if !sameOutcome(paths[0].out, p.out) {
			t.Errorf("%s: %s (K=%#v I=%#v)\n  map:     %v\n  %-8s %v", label, c.src, c.k, c.i, paths[0].out, p.name+":", p.out)
		}
	}
	return paths[0].out
}

func TestValueMatrixPathsAgree(t *testing.T) {
	for _, nv := range matrixValues() {
		t.Run(nv.name, func(t *testing.T) {
			got := map[string]mxOutcome{}
			for _, c := range mxCases() {
				key := c.src
				if c.k != nil || c.i != nil {
					key = fmt.Sprintf("%s|%#v|%#v", c.src, c.k, c.i)
				}
				got[key] = mxEval(t, nv.name, c, nv.v)
			}
			checkMatrixRelations(t, nv, got)
		})
	}
}

// checkMatrixRelations asserts the documented relations between the
// outcomes of related expressions on one value.
func checkMatrixRelations(t *testing.T, nv namedValue, got map[string]mxOutcome) {
	t.Helper()
	ok := func(o mxOutcome) bool { return o.category == "" && o.panicked == nil }

	// Strict and optional access: ?. and ?[ give the strict result when
	// there is one, nil when the receiver is nil or the value is
	// missing, and the strict error otherwise.
	pairs := [][2]string{
		{"V.A", "V?.A"}, {"V.a", "V?.a"}, {"V.E", "V?.E"}, {"V.hidden", "V?.hidden"},
		{"V[0]", "V?[0]"}, {"V[1]", "V?[1]"}, {`V["a"]`, `V?["a"]`},
	}
	for _, k := range mxKeys {
		pairs = append(pairs, [2]string{fmt.Sprintf("V[K]|%#v|<nil>", k), fmt.Sprintf("V?[K]|%#v|<nil>", k)})
	}
	for _, i := range mxIndexes {
		pairs = append(pairs, [2]string{fmt.Sprintf("V[I]|<nil>|%#v", i), fmt.Sprintf("V?[I]|<nil>|%#v", i)})
	}
	for _, p := range pairs {
		strict, opt := got[p[0]], got[p[1]]
		switch {
		case ok(strict) && !sameOutcome(strict, opt):
			t.Errorf("%s: %s = %v but %s = %v", nv.name, p[0], strict, p[1], opt)
		case !ok(strict) && ok(opt) && opt.value != nil:
			t.Errorf("%s: %s fails (%v) but %s = %v; optional access may only add nil", nv.name, p[0], strict, p[1], opt)
		case !ok(opt) && ok(strict):
			t.Errorf("%s: %s fails (%v) but %s succeeds", nv.name, p[1], opt, p[0])
		case !ok(opt) && opt.msg != strict.msg:
			t.Errorf("%s: %s and %s fail differently:\n  %s\n  %s", nv.name, p[0], p[1], strict.msg, opt.msg)
		}
		if !ok(strict) && ok(opt) && isNilValue(nv.v) == false && strings.Contains(strict.msg, "cannot select") {
			t.Errorf("%s: wrong-kind %s became nil under %s", nv.name, p[0], p[1])
		}
	}

	// try(x, d) is x when x succeeds and d when it fails.
	for strict, tried := range map[string]string{"V.A": `try(V.A, "d")`, "V[0]": `try(V[0], "d")`} {
		s, tr := got[strict], got[tried]
		want := mxOutcome{value: "d"}
		if ok(s) {
			want = s
		}
		if !sameOutcome(want, tr) {
			t.Errorf("%s: %s = %v, want %v (from %s = %v)", nv.name, tried, tr, want, strict, s)
		}
	}

	// != is exactly !(==), and the two fail together.
	eq, ne := got["V == V"], got["V != V"]
	if ok(eq) != ok(ne) || (ok(eq) && eq.value == ne.value) {
		t.Errorf("%s: V == V is %v but V != V is %v", nv.name, eq, ne)
	}
	// Equality is reflexive on comparable values (no NaN here).
	if ok(eq) && eq.value != true {
		t.Errorf("%s: V == V = %v", nv.name, eq)
	}
	// == nil agrees with the documented typed-nil rule.
	if n := got["V == nil"]; !ok(n) || n.value != isNilValue(nv.v) {
		t.Errorf("%s: V == nil = %v, want %v", nv.name, n, isNilValue(nv.v))
	}

	// Truthiness is one rule, wherever it is consulted.
	truthy := IsTruthy(nv.v)
	for src, want := range map[string]any{"!V": !truthy, "bool(V)": truthy, "if(V, 1, 2)": map[bool]int64{true: 1, false: 2}[truthy]} {
		if o := got[src]; !sameOutcome(o, mxOutcome{value: want}) {
			t.Errorf("%s: %s = %v, want %v", nv.name, src, o, want)
		}
	}

	// Moving a value through a literal or a form does not change it.
	for _, src := range []string{"[V][0]", `{"x": V}.x`, "filter([V], true)[0]", "filter([V, V], true)[1]", "map([V], it)[0]"} {
		if o := got[src]; !sameOutcome(o, got["V"]) {
			t.Errorf("%s: %s = %v, want %v", nv.name, src, o, got["V"])
		}
	}

	// Formatting never fails, and both formatters agree.
	if s, f := got["string(V)"], got[`sprintf("%v", V)`]; !ok(s) || !ok(f) {
		t.Errorf("%s: formatting failed: string(V) = %v, sprintf = %v", nv.name, s, f)
	}
}
