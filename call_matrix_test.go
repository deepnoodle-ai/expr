package expr

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// The call matrix invokes the same host functions with the same
// arguments through every way a host can supply one: registered with
// WithFunctions (the prepared path, with and without the compile-time
// call cache), stored in the env map, and stored in a struct field. The
// paths must agree on the result, the error category and message, and
// on whether the function body ran.

type callProbe struct {
	name string
	// fn builds the function, reporting each run through called.
	fn func(called *int) any
}

type mxStringer struct{}

func (mxStringer) String() string { return "stringer" }

func callProbes() []callProbe {
	return []callProbe{
		{"int", func(c *int) any { return func(x int) int { *c++; return x + 1 } }},
		{"int8", func(c *int) any { return func(x int8) int8 { *c++; return x } }},
		{"uint8", func(c *int) any { return func(x uint8) uint8 { *c++; return x } }},
		{"float32", func(c *int) any { return func(x float32) float32 { *c++; return x } }},
		{"string", func(c *int) any { return func(s string) string { *c++; return s + "!" } }},
		{"named string", func(c *int) any { return func(s mxName) mxName { *c++; return s } }},
		{"any", func(c *int) any { return func(v any) any { *c++; return v } }},
		{"[]int", func(c *int) any { return func(xs []int) int { *c++; return len(xs) } }},
		{"[2]int", func(c *int) any { return func(xs [2]int) int { *c++; return xs[0] } }},
		{"*[2]int", func(c *int) any { return func(xs *[2]int) bool { *c++; return xs == nil } }},
		{"map[string]int", func(c *int) any { return func(m map[string]int) int { *c++; return len(m) } }},
		{"error param", func(c *int) any { return func(e error) bool { *c++; return e == nil } }},
		{"Stringer param", func(c *int) any { return func(s fmt.Stringer) string { *c++; return s.String() } }},
		{"variadic", func(c *int) any { return func(p string, xs ...int) int { *c++; return len(p) + len(xs) } }},
		{"ctx", func(c *int) any {
			return func(ctx context.Context, s string) string { *c++; return fmt.Sprint(ctx != nil, s) }
		}},
		{"no result", func(c *int) any { return func(any) { *c++ } }},
		{"(T, nil error)", func(c *int) any { return func(any) (int, error) { *c++; return 1, nil } }},
		{"(T, error)", func(c *int) any { return func(any) (int, error) { *c++; return 0, errors.New("host failed") } }},
		{"(T, ErrEvaluate)", func(c *int) any {
			return func(any) (int, error) { *c++; return 0, fmt.Errorf("%w: already wrapped", ErrEvaluate) }
		}},
		{"(T, concrete error)", func(c *int) any { return func(any) (int, mxErr) { *c++; return 0, mxErr{"concrete"} } }},
		{"(T, nil *ptr error)", func(c *int) any { return func(any) (int, *mxPtrErr) { *c++; return 2, nil } }},
		{"(T, typed nil in error)", func(c *int) any {
			// The Go gotcha: a nil *mxPtrErr boxed in error is a non-nil
			// error, and expr follows Go.
			return func(any) (int, error) { *c++; var e *mxPtrErr; return 3, e }
		}},
		{"runtime panic", func(c *int) any { return func(xs []int) int { *c++; return xs[5] } }},
		{"two results", func(c *int) any { return func() (int, int) { *c++; return 1, 2 } }},
		{"error first", func(c *int) any { return func() (error, int) { *c++; return nil, 1 } }},
		{"three results", func(c *int) any { return func() (int, int, error) { *c++; return 1, 2, nil } }},
		{"Func", func(c *int) any {
			return Func(func(_ context.Context, args []any) (any, error) { *c++; return len(args), nil })
		}},
		{"Func error", func(c *int) any {
			return Func(func(context.Context, []any) (any, error) { *c++; return nil, mxErr{"native"} })
		}},
	}
}

// callArgs are argument lists, written as expr source.
var callArgs = []string{
	"", "1", "300", "-1", "1.0", "1.5", `"s"`, "nil", "[1, 2]", "[1]", "A", "S", "E", "N", "M", `"p", 1, 2`, `"p"`,
}

// callEnv supplies the host-typed arguments: A an []int too short for
// [2]int, S a Stringer, E a concrete error, N a named string, M a
// map[string]any (not convertible to map[string]int).
func callArgEnv() map[string]any {
	return map[string]any{
		"A": []int{1}, "S": mxStringer{}, "E": mxErr{"e"}, "N": mxName("n"),
		"M": map[string]any{"a": 1},
	}
}

type callFieldEnv struct {
	F any
	A any
	S any
	E any
	N any
	M any
}

type callOutcome struct {
	mxOutcome
	called int
}

func (o callOutcome) String() string { return fmt.Sprintf("%v (ran %d)", o.mxOutcome, o.called) }

// errorsAsMx reports whether err carries the mxErr a host returned, so
// the original cause stays reachable through the wrapping.
func errorsAsMx(err error) bool {
	var target mxErr
	return errors.As(err, &target)
}

func runCall(probe callProbe, args, how string) (out callOutcome, causeKept bool) {
	called := 0
	fn := probe.fn(&called)
	argEnv := callArgEnv()
	src := "F(" + args + ")"
	var (
		p   *Program
		err error
		env any
	)
	defer func() {
		if r := recover(); r != nil {
			out = callOutcome{mxOutcome{panicked: r}, called}
		}
	}()
	switch how {
	case "registered", "registered/noopt":
		opts := []Option{WithFunctions(map[string]any{"F": fn})}
		if how == "registered/noopt" {
			opts = append(opts, withoutOptimizations())
		}
		p, err = Compile(src, opts...)
		env = argEnv
	case "env":
		p, err = Compile(src)
		argEnv["F"] = fn
		env = argEnv
	case "field":
		p, err = Compile(src)
		env = callFieldEnv{F: fn, A: argEnv["A"], S: argEnv["S"], E: argEnv["E"], N: argEnv["N"], M: argEnv["M"]}
	}
	if err != nil {
		return callOutcome{mxOutcome{category: errCategory(err), msg: err.Error()}, called}, true
	}
	v, err := p.Run(context.Background(), env)
	if err != nil {
		returnsMx := probe.name == "(T, concrete error)" || probe.name == "Func error"
		return callOutcome{mxOutcome{category: errCategory(err), msg: err.Error()}, called}, !returnsMx || called == 0 || errorsAsMx(err)
	}
	return callOutcome{mxOutcome{value: v}, called}, true
}

func TestCallMatrixPathsAgree(t *testing.T) {
	for _, probe := range callProbes() {
		t.Run(probe.name, func(t *testing.T) {
			for _, args := range callArgs {
				reg, _ := runCall(probe, args, "registered")
				if reg.category == "compile" {
					// Registration rejected the signature; the other paths
					// must reject it at run time without calling it.
					for _, how := range []string{"env", "field"} {
						o, _ := runCall(probe, args, how)
						if o.category != "evaluate" || o.called != 0 {
							t.Errorf("F(%s) via %s = %v; registration rejects this signature, so Run must fail before calling", args, how, o)
						}
					}
					continue
				}
				for _, how := range []string{"registered/noopt", "env", "field"} {
					o, kept := runCall(probe, args, how)
					if !sameOutcome(reg.mxOutcome, o.mxOutcome) || reg.called != o.called {
						t.Errorf("F(%s):\n  registered: %v\n  %-11s %v", args, reg, how+":", o)
					}
					if !kept {
						t.Errorf("F(%s) via %s lost the host error's cause: %v", args, how, o)
					}
				}
				if reg.panicked != nil {
					t.Errorf("F(%s) panicked: %v", args, reg.panicked)
				}
				if reg.category == "UNWRAPPED" {
					t.Errorf("F(%s): error does not wrap ErrEvaluate: %s", args, reg.msg)
				}
				// A conversion failure must stop the call before the body runs.
				if reg.category != "" && reg.called != 0 && !isHostFailure(probe.name) && !strings.Contains(reg.msg, "panicked during call") {
					t.Errorf("F(%s) failed (%s) after running the function", args, reg.msg)
				}
			}
		})
	}
}

// isHostFailure reports whether the probe's own body returns an error
// or panics, so a failed call that ran it is expected.
func isHostFailure(name string) bool {
	switch name {
	case "(T, error)", "(T, ErrEvaluate)", "(T, concrete error)", "(T, typed nil in error)", "runtime panic", "Func error":
		return true
	}
	return false
}

// TestCallMatrixConversions pins the argument conversions themselves,
// beyond agreement between paths.
func TestCallMatrixConversions(t *testing.T) {
	probes := map[string]callProbe{}
	for _, p := range callProbes() {
		probes[p.name] = p
	}
	for _, tc := range []struct {
		probe, args string
		want        any // a value, or the error category
	}{
		{"int", "1", int(2)},
		{"int", "1.0", int(2)},
		{"int", "1.5", int(2)}, // float → int truncates, as Go's conversion does
		{"int8", "300", "evaluate"},
		{"uint8", "-1", "evaluate"},
		{"float32", "1.5", float32(1.5)},
		{"string", "1", "evaluate"}, // no integer → string conversion
		{"named string", `"s"`, mxName("s")},
		{"string", "N", "n!"},
		{"[]int", "[1, 2]", "evaluate"}, // []any does not convert to []int
		{"[]int", "A", 1},
		{"[2]int", "A", "evaluate"}, // a slice shorter than the array
		{"*[2]int", "A", "evaluate"},
		{"*[2]int", "nil", true},
		{"map[string]int", "M", "evaluate"},
		{"error param", "E", false},
		{"error param", "nil", true},
		{"Stringer param", "S", "stringer"},
		{"variadic", `"p", 1, 2`, 3},
		{"variadic", `"p"`, 1},
		{"variadic", "", "evaluate"},
		{"ctx", `"s"`, "trues"},
		{"no result", "1", nil},
		{"(T, nil error)", "1", 1},
		{"(T, error)", "1", "evaluate"},
		{"(T, concrete error)", "1", "evaluate"},
		{"(T, nil *ptr error)", "1", 2},
		{"(T, typed nil in error)", "1", "evaluate"},
		{"runtime panic", "A", "evaluate"},
		{"two results", "", "compile"},
		{"error first", "", "compile"},
		{"three results", "", "compile"},
		{"Func", `"p", 1`, 2},
		{"Func error", "", "evaluate"},
	} {
		got, _ := runCall(probes[tc.probe], tc.args, "registered")
		want := mxOutcome{value: tc.want}
		if cat, isCat := tc.want.(string); isCat && (cat == "evaluate" || cat == "compile") {
			if got.category != cat {
				t.Errorf("%s: F(%s) = %v, want %s error", tc.probe, tc.args, got, cat)
			}
			continue
		}
		if !reflect.DeepEqual(got.value, want.value) || got.category != "" {
			t.Errorf("%s: F(%s) = %v, want %#v", tc.probe, tc.args, got, tc.want)
		}
	}
}
