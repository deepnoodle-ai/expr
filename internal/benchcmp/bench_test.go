package benchcmp

import (
	"context"
	"testing"

	dn "github.com/deepnoodle-ai/expr"
	exprlang "github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
	"github.com/google/cel-go/cel"
)

// These benchmarks mirror the canonical cases from the top-level
// bench_test.go (which itself mirrors expr-lang's upstream shape), and
// run the same expressions across deepnoodle/expr, expr-lang/expr, and
// cel-go so ns/op and allocations can be compared directly.
//
// Only cases that work cleanly with map[string]any envs across all three
// libraries are included here; struct-env and method-call cases live in
// the top-level file and are exercised against deepnoodle/expr only.

type benchCase struct {
	name   string
	src    string         // deepnoodle/expr source (default for all)
	elSrc  string         // expr-lang/expr override (uses `#` for element)
	celSrc string         // cel-go override
	env    map[string]any // env, shared across all three libraries
}

func (c benchCase) elSource() string {
	if c.elSrc != "" {
		return c.elSrc
	}
	return c.src
}

func (c benchCase) celSource() string {
	if c.celSrc != "" {
		return c.celSrc
	}
	return c.src
}

func makeInts(n int) []int64 {
	out := make([]int64, n)
	for i := range out {
		out[i] = int64(i + 1)
	}
	return out
}

var benchCases = []benchCase{
	{
		name: "predicate",
		src:  `(Origin == "MOW" || Country == "RU") && (Value >= 100 || Adults == 1)`,
		env: map[string]any{
			"Origin":  "MOW",
			"Country": "RU",
			"Adults":  int64(1),
			"Value":   int64(100),
		},
	},
	{
		name:   "len",
		src:    `len(arr)`,
		celSrc: `size(arr)`,
		env:    map[string]any{"arr": make([]int64, 100)},
	},
	{
		name:   "filter",
		src:    `filter(Ints, it % 7 == 0)`,
		elSrc:  `filter(Ints, # % 7 == 0)`,
		celSrc: `Ints.filter(x, x % 7 == 0)`,
		env:    map[string]any{"Ints": makeInts(1000)},
	},
	{
		name:   "filterLen",
		src:    `len(filter(Ints, it % 7 == 0))`,
		elSrc:  `len(filter(Ints, # % 7 == 0))`,
		celSrc: `size(Ints.filter(x, x % 7 == 0))`,
		env:    map[string]any{"Ints": makeInts(1000)},
	},
	{
		name: "arrayIndex",
		src:  `arr[50]`,
		env:  map[string]any{"arr": makeInts(100)},
	},
}

// ------- deepnoodle/expr -------

func BenchmarkDNCompile(b *testing.B) {
	opts := []dn.Option{dn.WithBuiltins()}
	for _, bc := range benchCases {
		b.Run(bc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := dn.Compile(bc.src, opts...); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkDNRun(b *testing.B) {
	opts := []dn.Option{dn.WithBuiltins()}
	ctx := context.Background()
	for _, bc := range benchCases {
		prog, err := dn.Compile(bc.src, opts...)
		if err != nil {
			b.Fatalf("%s: %v", bc.name, err)
		}
		b.Run(bc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := prog.Run(ctx, bc.env); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// ------- expr-lang/expr -------

func BenchmarkELCompile(b *testing.B) {
	for _, bc := range benchCases {
		b.Run(bc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := exprlang.Compile(bc.elSource(), exprlang.Env(bc.env)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkELRun(b *testing.B) {
	for _, bc := range benchCases {
		program, err := exprlang.Compile(bc.elSource(), exprlang.Env(bc.env))
		if err != nil {
			b.Fatalf("%s: %v", bc.name, err)
		}
		b.Run(bc.name, func(b *testing.B) {
			b.ReportAllocs()
			v := vm.VM{}
			for i := 0; i < b.N; i++ {
				if _, err := v.Run(program, bc.env); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// ------- google/cel-go -------

func celEnvFor(b *testing.B, env map[string]any) *cel.Env {
	b.Helper()
	opts := make([]cel.EnvOption, 0, len(env))
	for k := range env {
		opts = append(opts, cel.Variable(k, cel.DynType))
	}
	e, err := cel.NewEnv(opts...)
	if err != nil {
		b.Fatal(err)
	}
	return e
}

func BenchmarkCELCompile(b *testing.B) {
	for _, bc := range benchCases {
		e := celEnvFor(b, bc.env)
		b.Run(bc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				ast, iss := e.Compile(bc.celSource())
				if iss != nil && iss.Err() != nil {
					b.Fatal(iss.Err())
				}
				if _, err := e.Program(ast); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkCELRun(b *testing.B) {
	for _, bc := range benchCases {
		e := celEnvFor(b, bc.env)
		ast, iss := e.Compile(bc.celSource())
		if iss != nil && iss.Err() != nil {
			b.Fatalf("%s: %v", bc.name, iss.Err())
		}
		prg, err := e.Program(ast)
		if err != nil {
			b.Fatalf("%s: %v", bc.name, err)
		}
		b.Run(bc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, _, err := prg.Eval(bc.env); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
