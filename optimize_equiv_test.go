package expr

import (
	"context"
	"errors"
	"fmt"
	"go/token"
	"math"
	"strings"
	"testing"
)

// Every optimization must be unobservable. Each one below has an
// equivalence test here, comparing a program compiled normally with the
// same program compiled withoutOptimizations, and a benchmark measuring
// what it buys:
//
//	optimization                         equivalence test                      benchmark
//	constant folding (prewalk)           TestEquivalenceConstantFolding        BenchmarkConstantFolding
//	prepared-call cache (callCache)      TestEquivalencePreparedCallCache      Benchmark_len, Benchmark_callFunc
//	reflect selector chain               TestEquivalenceSelectorChain          Benchmark_envStruct, Benchmark_largeNestedStructAccess
//	reflect index chain                  TestEquivalenceIndexChain             Benchmark_largeNestedArrayAccess
//	filter(xs, p)[k] streaming           TestEquivalenceFilterIndex            Benchmark_filterFirst
//	typed binary operators               TestApplyBinaryFastMatchesGeneral     Benchmark_expr
//	map[string]any access branches       TestValueMatrixPathsAgree             Benchmark_envMap
//
// "Equivalent" means the same returned value, the same error category
// and message, and the same host-function calls in the same order.
// Check is not compared: it reads the folded AST, so a constant
// subexpression types as its literal (`1 + 2` as `3`, not number), a
// narrower type that every run still fits.

// hostLog records calls to the host functions the corpus uses: rec(x)
// logs x and returns it, boom() logs and fails.
type hostLog struct{ calls []string }

func (h *hostLog) funcs() map[string]any {
	return map[string]any{
		"rec": Func(func(_ context.Context, args []any) (any, error) {
			h.calls = append(h.calls, fmt.Sprint(args...))
			if len(args) == 0 {
				return nil, nil
			}
			return args[0], nil
		}),
		"boom": Func(func(context.Context, []any) (any, error) {
			h.calls = append(h.calls, "boom")
			return nil, errors.New("boom")
		}),
		"inc": func(x int) int { h.calls = append(h.calls, fmt.Sprint("inc ", x)); return x + 1 },
	}
}

type equivOutcome struct {
	mxOutcome
	calls []string
}

func (o equivOutcome) String() string {
	return fmt.Sprintf("%v calls=%q", o.mxOutcome, o.calls)
}

func runEquiv(src string, env any, noopt bool, opts ...Option) (out equivOutcome) {
	h := &hostLog{}
	defer func() {
		if r := recover(); r != nil {
			out = equivOutcome{mxOutcome: mxOutcome{panicked: r}, calls: h.calls}
		}
	}()
	all := append([]Option{WithBuiltins(), WithFunctions(h.funcs())}, opts...)
	if noopt {
		all = append(all, withoutOptimizations())
	}
	p, err := Compile(src, all...)
	if err != nil {
		return equivOutcome{mxOutcome: mxOutcome{category: errCategory(err), msg: err.Error()}}
	}
	v, err := p.Run(context.Background(), env)
	if err != nil {
		return equivOutcome{mxOutcome: mxOutcome{category: errCategory(err), msg: err.Error()}, calls: h.calls}
	}
	return equivOutcome{mxOutcome: mxOutcome{value: v}, calls: h.calls}
}

func sameEquiv(a, b equivOutcome) bool {
	return sameOutcome(a.mxOutcome, b.mxOutcome) && fmt.Sprint(a.calls) == fmt.Sprint(b.calls)
}

// assertEquivalent compiles src with and without optimizations, runs
// both against each env, and reports any observable difference.
func assertEquivalent(t *testing.T, src string, envs ...any) {
	t.Helper()
	for i, env := range envs {
		opt, gen := runEquiv(src, env, false), runEquiv(src, env, true)
		if opt.panicked != nil || gen.panicked != nil {
			t.Errorf("%s (env %d) panicked:\n  optimized: %v\n  general:   %v", src, i, opt, gen)
			continue
		}
		if !sameEquiv(opt, gen) {
			t.Errorf("%s (env %d):\n  optimized: %v\n  general:   %v", src, i, opt, gen)
		}
	}
}

func TestEquivalenceConstantFolding(t *testing.T) {
	env := map[string]any{"V": 2}
	for _, src := range []string{
		"1 + 2 * 3", "-(1)", "- -1", "+1", `"a" + "b"`, "1 / 0", "1 % 0", "1.0 / 0", "7 / 2", "7.0 / 2",
		"9223372036854775807 + 1", "-9223372036854775808", "-(-9223372036854775808)",
		"-9223372036854775807 - 1", "-9223372036854775808 / -1", "-9223372036854775808 % -1",
		"2.0 * 3", "(1)", "((1 + 2))", "!0", `!""`, "!nil", "1 + rec(2)", "rec(1) + 2 * 3",
		"1.0 / 3", "1e308 * 10", "-1e308 * 10", `+"a"`, `-"a"`, `"a" * 2`, "1 + nil", "nil + nil",
		"(1 + 2) == 3", "[1 + 1][0]", `{"k": 2 * 3}.k`, "1 < 2", `"a" < "b"`, "2 > 1.5",
		"1 + 2 | string()", "try(1 / 0, \"d\")", "if(1 + 1 == 2, \"y\", 1 / 0)", "V * (2 + 3)",
		"0x10 + 0o7 + 0b1", "1_000 * 2", "'a' + 1", "1 + 2 + V", "V + 1 + 2", `"a" + 1`,
		"true && 1 / 0", "false || 2 * 3", "(1 + 2) * (3 + 4)", "1 << 2", "5 & 3", "1 + 2.5",
		"9007199254740993 == 9007199254740992", "9007199254740993 + 0 == 9007199254740992 + 0",
	} {
		assertEquivalent(t, src, env)
	}
}

// BenchmarkConstantFolding measures a constant-heavy expression with and
// without folding.
func BenchmarkConstantFolding(b *testing.B) {
	env := map[string]any{"x": 3}
	for _, noopt := range []bool{false, true} {
		name := "folded"
		opts := []Option{}
		if noopt {
			name = "unfolded"
			opts = append(opts, withoutOptimizations())
		}
		p, err := Compile(`x * (60 * 60 * 24) + (1024 * 1024) - (2 + 3) * 4`, opts...)
		if err != nil {
			b.Fatal(err)
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := p.Run(context.Background(), env); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

type equivCallEnv struct {
	V   any
	Len any `expr:"len"`
}

func TestEquivalencePreparedCallCache(t *testing.T) {
	envs := []any{
		map[string]any{"V": []any{1, 2}},
		map[string]any{"V": "héllo"},
		map[string]any{"V": nil},
		// An env entry shadows the registered function: the cache must
		// not bypass it.
		map[string]any{"V": []any{1}, "len": func(any) int { return 99 }},
		map[string]any{"V": []any{1}, "len": 5},
		map[string]any{"V": []any{1}, "rec": func(x any) string { return "env rec" }},
		equivCallEnv{V: []int{1, 2, 3}},
	}
	for _, src := range []string{
		"len(V)", "len(rec(V))", "rec(len(V))", "len(V) + len(V)", "rec(1, 2)", "rec()",
		"boom()", "try(boom(), 1)", "rec(boom())", "inc(1)", "inc(1.5)", "inc(\"x\")", "inc()",
		"inc(9223372036854775807)", "contains(rec(V), 1)", "V | len()", "V | rec() | len()",
		"len(V) > 1 && rec(true)", "upper(\"a\")", "sprintf(\"%v-%v\", rec(1), rec(2))",
	} {
		assertEquivalent(t, src, envs...)
	}
	// A struct env with a tagged field named like the function.
	for _, src := range []string{"len(V)"} {
		for _, noopt := range []bool{false, true} {
			o := runEquiv(src, equivCallEnv{V: []int{1}, Len: func(any) int { return 7 }}, noopt, WithFieldTags("expr"))
			if o.value != 7 {
				t.Errorf("%s with a tagged env field shadowing len (noopt=%v) = %v, want 7", src, noopt, o)
			}
		}
	}
}

type eqInner struct {
	B any
	C *eqInner
	M map[string]any
}

func (e eqInner) Method() string   { return "m" }
func (e *eqInner) PMethod() string { return "pm" }

type eqEnv struct {
	A  eqInner
	P  *eqInner
	N  *eqInner
	I  any
	L  []eqInner
	D  [2][3]int
	M  map[string]any
	S  map[string]eqInner
	K  any
	PP **eqInner
}

func equivStructEnvs() []any {
	inner := &eqInner{B: 1, C: &eqInner{B: "deep"}, M: map[string]any{"x": []any{1, 2}}}
	env := eqEnv{
		A: *inner, P: inner, I: inner, L: []eqInner{*inner, {}},
		D: [2][3]int{{1, 2, 3}, {4, 5, 6}}, M: map[string]any{"a": map[string]any{"b": 1}, "l": []any{1}},
		S: map[string]eqInner{"k": *inner}, K: "a", PP: &inner,
	}
	return []any{env, &env, eqEnv{}, map[string]any{"A": env.A, "P": env.P, "N": nil, "I": env.I, "L": env.L, "D": env.D, "M": env.M, "S": env.S, "K": "a", "PP": env.PP}}
}

func TestEquivalenceSelectorChain(t *testing.T) {
	for _, src := range []string{
		"A.B", "A.C.B", "A.C.C.B", "P.B", "P.C.B", "N.B", "I.B", "I.C.B", "A.M.x", "M.a.b", "M.a.c",
		"S.k.B", "S.missing.B", "A.missing", "A.b", "P.missing", "N.C", "PP.B", "PP.C.B",
		"A.Method", "P.PMethod", "A.Method()", "P.PMethod()", "P.Method()", "I.Method()",
		"A.B.x", "D.x", "L.x", "K.x", "A?.C?.B", "N?.C?.B",
	} {
		assertEquivalent(t, src, equivStructEnvs()...)
	}
}

func TestEquivalenceIndexChain(t *testing.T) {
	for _, src := range []string{
		"D[0][1]", "D[rec(1)][rec(2)]", "D[1][3]", "D[2][0]", "D[-1][0]", "D[0][1.0]", "D[0][1.5]",
		"L[0].B", "L[rec(0)].C.B", "L[1].C.B", "L[5].B", "L[boom()].B", "L[1 / 0].B", "L[rec(0)].M[rec(\"x\")][rec(1)]",
		"M[K].b", "M[rec(K)][rec(\"b\")]", "M[\"l\"][0]", "M[\"l\"][rec(5)]", "M[1]", "S[K]", "S[\"k\"].B",
		"A.M[\"x\"][1]", "P.M[\"x\"][0]", "N.M[\"x\"]", "I.M[\"x\"][0]", "PP.M[\"x\"][0]",
		"D[rec(0)][boom()]", "D[boom()][rec(0)]", "D[0][rec(1)] + D[rec(1)][0]",
		"L[0].B[0]", "A.B[0]", "D[0][0][0]", "M[K][K]", "D[nil]",
	} {
		assertEquivalent(t, src, equivStructEnvs()...)
	}
}

func TestEquivalenceFilterIndex(t *testing.T) {
	envs := []any{
		map[string]any{"xs": []any{1, 2, 3, 4}, "I": 0},
		map[string]any{"xs": []int{5, 0, 7}, "I": 1},
		map[string]any{"xs": [3]int{1, 2, 3}, "I": 2},
		map[string]any{"xs": nil, "I": 0},
		map[string]any{"xs": "abc", "I": 0},
		map[string]any{"xs": []any{}, "I": 0},
		map[string]any{"xs": []any{1, 2}, "I": 0, "filter": func(xs any, f any) []any { return []any{"shadow"} }},
	}
	// Each literal-index form must behave like the general path and like
	// the same expression with the index in a variable.
	for _, tc := range []struct{ lit, variable string }{
		{"filter(xs, rec(it) > 1)[0]", "filter(xs, rec(it) > 1)[I]"},
		{"filter(xs, rec(it) > 1)[1]", "filter(xs, rec(it) > 1)[I + 1]"},
		{"filter(xs, true)[2]", "filter(xs, true)[I + 2]"},
		{"filter(xs, false)[0]", "filter(xs, false)[I]"},
		{"filter(xs, x, rec(x) > 1)[0]", "filter(xs, x, rec(x) > 1)[I]"},
		{"filter(xs, 1 / it > 0)[0]", "filter(xs, 1 / it > 0)[I]"},
		{"filter(xs, it == 2 || boom())[0]", "filter(xs, it == 2 || boom())[I]"},
		{"filter(xs, rec(index) >= 0)[0x1]", "filter(xs, rec(index) >= 0)[I + 1]"},
		{"filter(xs, it)[99]", "filter(xs, it)[I + 99]"},
		{"filter(xs, 1, it)[0]", "filter(xs, 1, it)[I]"},
		{"filter(filter(xs, rec(it) > 1), rec(it) < 4)[0]", "filter(filter(xs, rec(it) > 1), rec(it) < 4)[I]"},
	} {
		assertEquivalent(t, tc.lit, envs...)
		assertEquivalent(t, tc.variable, envs...)
		for i, env := range envs {
			m := env.(map[string]any)
			lit := runEquiv(tc.lit, env, false)
			// Shift the variable so both forms read the same position.
			shifted := map[string]any{}
			for k, v := range m {
				shifted[k] = v
			}
			shifted["I"] = 0
			variable := runEquiv(tc.variable, shifted, false)
			if !sameOutcome(lit.mxOutcome, variable.mxOutcome) || fmt.Sprint(lit.calls) != fmt.Sprint(variable.calls) {
				t.Errorf("env %d: literal and variable index disagree:\n  %s: %v\n  %s: %v", i, tc.lit, lit, tc.variable, variable)
			}
		}
	}
}

// TestApplyBinaryFastMatchesGeneral checks the typed fast path against
// the general operator implementation on every operand pair it handles.
func TestApplyBinaryFastMatchesGeneral(t *testing.T) {
	values := []any{
		int64(0), int64(1), int64(-1), int64(7), int64(math.MaxInt64), int64(math.MinInt64), int64(1 << 53), int64(1<<53 + 1),
		0, 1, -3, math.MaxInt64, math.MinInt64,
		0.0, 1.0, -1.5, 2.5, 1e300, math.Inf(1), math.Inf(-1), math.NaN(), float64(1 << 53),
		"", "a", "b", "ab",
		true, false,
	}
	ops := []token.Token{token.ADD, token.SUB, token.MUL, token.QUO, token.REM,
		token.EQL, token.NEQ, token.LSS, token.GTR, token.LEQ, token.GEQ,
		token.AND, token.OR, token.XOR, token.SHL, token.SHR, token.AND_NOT}
	checked := 0
	for _, op := range ops {
		for _, l := range values {
			for _, r := range values {
				fv, ferr, ok := applyBinaryFast(op, l, r)
				if !ok {
					continue
				}
				checked++
				gv, gerr := applyBinaryGeneral(op, l, r)
				same := fmt.Sprint(ferr) == fmt.Sprint(gerr) && sameNumber(fv, gv)
				if !same {
					t.Errorf("%#v %v %#v: fast = (%#v, %v), general = (%#v, %v)", l, op, r, fv, ferr, gv, gerr)
				}
			}
		}
	}
	if checked < 500 {
		t.Fatalf("only %d operand pairs reached the fast path", checked)
	}
}

// sameNumber is equality that treats NaN as equal to NaN, so the
// fast/general comparison is about which value is produced.
func sameNumber(a, b any) bool {
	if fa, ok := a.(float64); ok {
		if fb, ok := b.(float64); ok && math.IsNaN(fa) && math.IsNaN(fb) {
			return true
		}
	}
	return a == b
}

// TestEquivalenceCorpus runs the shared value matrix expressions through
// the equivalence check too, with host-call logging, so every
// optimization sees the full set of host value shapes.
func TestEquivalenceCorpus(t *testing.T) {
	for _, nv := range matrixValues() {
		env := map[string]any{"V": nv.v, "K": "a", "I": 0}
		for _, src := range mxExprs {
			if strings.Contains(src, "sprintf") {
				continue // covered by the matrix; formatting is not optimized
			}
			assertEquivalent(t, src, env, mxEnv{V: nv.v, K: "a", I: 0})
		}
	}
}
