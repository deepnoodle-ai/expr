package benchcmp

import (
	"context"
	"testing"

	dn "github.com/deepnoodle-ai/expr"
	exprlang "github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
)

// Matched benchmarks running the same expression sources in both
// libraries so we can compare ns/op and allocations directly.

func benchEnv() map[string]any {
	return map[string]any{
		"state": map[string]any{
			"counter": int64(10),
			"limit":   int64(100),
			"name":    "Alice",
			"items":   []any{int64(1), int64(2), int64(3), int64(4), int64(5)},
			"user": map[string]any{
				"age":    int64(30),
				"active": true,
			},
		},
		"inputs": map[string]any{
			"multiplier": int64(3),
			"prefix":     "user:",
		},
	}
}

var benchExprs = []struct {
	name string
	src  string
}{
	{"literal", "42"},
	{"arith", "1 + 2 * 3"},
	{"condition", "state.counter < state.limit && state.user.active"},
	{"nested_sel", "state.user.age >= 18"},
	{"index", "state.items[2]"},
	{"builtin_len", "len(state.items) > 3"},
	{"mixed", "state.counter * inputs.multiplier + len(state.items)"},
}

// ------- deepnoodle/expr (ours) -------

func BenchmarkDNCompile(b *testing.B) {
	opts := []dn.Option{dn.WithBuiltins()}
	for _, bc := range benchExprs {
		b.Run(bc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_, err := dn.Compile(bc.src, opts...)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkDNRun(b *testing.B) {
	opts := []dn.Option{dn.WithBuiltins()}
	env := benchEnv()
	ctx := context.Background()
	for _, bc := range benchExprs {
		prog, err := dn.Compile(bc.src, opts...)
		if err != nil {
			b.Fatalf("%s: %v", bc.name, err)
		}
		b.Run(bc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := prog.Run(ctx, env); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// ------- expr-lang/expr -------

func BenchmarkELCompile(b *testing.B) {
	env := benchEnv()
	for _, bc := range benchExprs {
		b.Run(bc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_, err := exprlang.Compile(bc.src, exprlang.Env(env))
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkELRun(b *testing.B) {
	env := benchEnv()
	for _, bc := range benchExprs {
		program, err := exprlang.Compile(bc.src, exprlang.Env(env))
		if err != nil {
			b.Fatalf("%s: %v", bc.name, err)
		}
		b.Run(bc.name, func(b *testing.B) {
			b.ReportAllocs()
			v := vm.VM{}
			for i := 0; i < b.N; i++ {
				if _, err := v.Run(program, env); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
