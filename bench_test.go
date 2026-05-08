package expr_test

// These benchmarks mirror the shape and naming of
// github.com/expr-lang/expr/bench_test.go so that results from the two
// libraries can be compared directly with `go test -bench=.`. Expressions
// that rely on features this subset does not support (pipelines,
// #/#acc placeholders, `1..100` ranges, negative indexing, sort/reduce/
// min/max/groupBy, a separate VM, etc.) are omitted. The ones that
// remain use the same env shapes, the same literal data, and the same
// shape of work as upstream, with `#` rewritten to `it`.

import (
	"context"
	"testing"

	"github.com/deepnoodle-ai/expr"
)

// engOpts is shared across every benchmark so startup cost is not folded
// into steady-state measurements.
var engOpts = []expr.Option{expr.WithBuiltins()}

func Benchmark_expr(b *testing.B) {
	params := map[string]any{
		"Origin":  "MOW",
		"Country": "RU",
		"Adults":  1,
		"Value":   100,
	}

	program, err := expr.Compile(`(Origin == "MOW" || Country == "RU") && (Value >= 100 || Adults == 1)`, engOpts...)
	if err != nil {
		b.Fatal(err)
	}

	var out any
	ctx := context.Background()

	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		out, err = program.Run(ctx, params)
	}
	b.StopTimer()

	if err != nil {
		b.Fatal(err)
	}
	if !out.(bool) {
		b.Fatalf("unexpected result %v", out)
	}
}

func Benchmark_len(b *testing.B) {
	env := map[string]any{
		"arr": make([]int, 100),
	}

	program, err := expr.Compile(`len(arr)`, engOpts...)
	if err != nil {
		b.Fatal(err)
	}

	var out any
	ctx := context.Background()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		out, err = program.Run(ctx, env)
	}
	b.StopTimer()

	if err != nil {
		b.Fatal(err)
	}
	if out.(int) != 100 {
		b.Fatalf("unexpected result %v", out)
	}
}

func Benchmark_filter(b *testing.B) {
	ints := make([]int, 1000)
	for i := 1; i <= len(ints); i++ {
		ints[i-1] = i
	}
	env := map[string]any{"Ints": ints}

	program, err := expr.Compile(`filter(Ints, it % 7 == 0)`, engOpts...)
	if err != nil {
		b.Fatal(err)
	}

	var out any
	ctx := context.Background()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		out, err = program.Run(ctx, env)
	}
	b.StopTimer()

	if err != nil {
		b.Fatal(err)
	}
	if got := len(out.([]any)); got != 142 {
		b.Fatalf("unexpected length %d", got)
	}
}

func Benchmark_filterLen(b *testing.B) {
	ints := make([]int, 1000)
	for i := 1; i <= len(ints); i++ {
		ints[i-1] = i
	}
	env := map[string]any{"Ints": ints}

	program, err := expr.Compile(`len(filter(Ints, it % 7 == 0))`, engOpts...)
	if err != nil {
		b.Fatal(err)
	}

	var out any
	ctx := context.Background()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		out, err = program.Run(ctx, env)
	}
	b.StopTimer()

	if err != nil {
		b.Fatal(err)
	}
	if out.(int) != 142 {
		b.Fatalf("unexpected result %v", out)
	}
}

func Benchmark_filterFirst(b *testing.B) {
	ints := make([]int, 1000)
	for i := 1; i <= len(ints); i++ {
		ints[i-1] = i
	}
	env := map[string]any{"Ints": ints}

	program, err := expr.Compile(`filter(Ints, it % 7 == 0)[0]`, engOpts...)
	if err != nil {
		b.Fatal(err)
	}

	var out any
	ctx := context.Background()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		out, err = program.Run(ctx, env)
	}
	b.StopTimer()

	if err != nil {
		b.Fatal(err)
	}
	if out.(int) != 7 {
		b.Fatalf("unexpected result %v", out)
	}
}

func Benchmark_filterMap(b *testing.B) {
	ints := make([]int, 100)
	for i := 1; i <= len(ints); i++ {
		ints[i-1] = i
	}
	env := map[string]any{"Ints": ints}

	program, err := expr.Compile(`map(filter(Ints, it % 7 == 0), it * 2)`, engOpts...)
	if err != nil {
		b.Fatal(err)
	}

	var out any
	ctx := context.Background()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		out, err = program.Run(ctx, env)
	}
	b.StopTimer()

	if err != nil {
		b.Fatal(err)
	}
	arr := out.([]any)
	if len(arr) != 14 || arr[0].(int64) != 14 {
		b.Fatalf("unexpected result %v", out)
	}
}

func Benchmark_arrayIndex(b *testing.B) {
	arr := make([]int, 100)
	for i := 0; i < 100; i++ {
		arr[i] = i
	}
	env := map[string]any{"arr": arr}

	program, err := expr.Compile(`arr[50]`, engOpts...)
	if err != nil {
		b.Fatal(err)
	}

	var out any
	ctx := context.Background()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		out, err = program.Run(ctx, env)
	}
	b.StopTimer()

	if err != nil {
		b.Fatal(err)
	}
	if out.(int) != 50 {
		b.Fatalf("unexpected result %v", out)
	}
}

type Price struct {
	Value int
}
type priceEnv struct {
	Price Price
}

func Benchmark_envStruct(b *testing.B) {
	program, err := expr.Compile(`Price.Value > 0`, engOpts...)
	if err != nil {
		b.Fatal(err)
	}

	env := priceEnv{Price: Price{Value: 1}}

	var out any
	ctx := context.Background()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		out, err = program.Run(ctx, env)
	}
	b.StopTimer()

	if err != nil {
		b.Fatal(err)
	}
	if !out.(bool) {
		b.Fatalf("unexpected result %v", out)
	}
}

type taggedPrice struct {
	Value int `json:"value"`
}
type taggedPriceEnv struct {
	Price taggedPrice `json:"price"`
}

func Benchmark_structTagsNested(b *testing.B) {
	env := taggedPriceEnv{Price: taggedPrice{Value: 1}}
	cases := []struct {
		name string
		src  string
		opts []expr.Option
	}{
		{name: "go_names", src: `Price.Value > 0`, opts: engOpts},
		{name: "json_tags", src: `price.value > 0`, opts: []expr.Option{expr.WithBuiltins(), expr.WithStructTags("json")}},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			program, err := expr.Compile(tc.src, tc.opts...)
			if err != nil {
				b.Fatal(err)
			}

			var out any
			ctx := context.Background()
			b.ResetTimer()
			for n := 0; n < b.N; n++ {
				out, err = program.Run(ctx, env)
			}
			b.StopTimer()

			if err != nil {
				b.Fatal(err)
			}
			if !out.(bool) {
				b.Fatalf("unexpected result %v", out)
			}
		})
	}
}

type wideTaggedEnv struct {
	Field00 int `json:"field_00"`
	Field01 int `json:"field_01"`
	Field02 int `json:"field_02"`
	Field03 int `json:"field_03"`
	Field04 int `json:"field_04"`
	Field05 int `json:"field_05"`
	Field06 int `json:"field_06"`
	Field07 int `json:"field_07"`
	Field08 int `json:"field_08"`
	Field09 int `json:"field_09"`
	Field10 int `json:"field_10"`
	Field11 int `json:"field_11"`
	Field12 int `json:"field_12"`
	Field13 int `json:"field_13"`
	Field14 int `json:"field_14"`
	Field15 int `json:"field_15"`
	Field16 int `json:"field_16"`
	Field17 int `json:"field_17"`
	Field18 int `json:"field_18"`
	Field19 int `json:"field_19"`
	Field20 int `json:"field_20"`
	Field21 int `json:"field_21"`
	Field22 int `json:"field_22"`
	Field23 int `json:"field_23"`
	Field24 int `json:"field_24"`
	Field25 int `json:"field_25"`
	Field26 int `json:"field_26"`
	Field27 int `json:"field_27"`
	Field28 int `json:"field_28"`
	Field29 int `json:"field_29"`
	Field30 int `json:"field_30"`
	Field31 int `json:"field_31"`
}

func Benchmark_structTagsWide(b *testing.B) {
	env := wideTaggedEnv{Field31: 42}
	cases := []struct {
		name string
		src  string
		opts []expr.Option
	}{
		{name: "go_names", src: `Field31 > 0 && Field31 < 99`, opts: engOpts},
		{name: "json_tags", src: `field_31 > 0 && field_31 < 99`, opts: []expr.Option{expr.WithBuiltins(), expr.WithStructTags("json")}},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			program, err := expr.Compile(tc.src, tc.opts...)
			if err != nil {
				b.Fatal(err)
			}

			var out any
			ctx := context.Background()
			b.ResetTimer()
			for n := 0; n < b.N; n++ {
				out, err = program.Run(ctx, env)
			}
			b.StopTimer()

			if err != nil {
				b.Fatal(err)
			}
			if !out.(bool) {
				b.Fatalf("unexpected result %v", out)
			}
		})
	}
}

func Benchmark_envMap(b *testing.B) {
	env := map[string]any{
		"price": Price{Value: 1},
	}

	program, err := expr.Compile(`price.Value > 0`, engOpts...)
	if err != nil {
		b.Fatal(err)
	}

	var out any
	ctx := context.Background()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		out, err = program.Run(ctx, env)
	}
	b.StopTimer()

	if err != nil {
		b.Fatal(err)
	}
	if !out.(bool) {
		b.Fatalf("unexpected result %v", out)
	}
}

type CallEnv struct {
	A   int
	B   int
	C   int
	Fn  func() bool
	Foo CallFoo
}

func (CallEnv) Func() string {
	return "func"
}

type CallFoo struct {
	D int
	E int
	F int
}

func (CallFoo) Method() string {
	return "method"
}

func Benchmark_callFunc(b *testing.B) {
	program, err := expr.Compile(`Func()`, engOpts...)
	if err != nil {
		b.Fatal(err)
	}

	env := CallEnv{}

	var out any
	ctx := context.Background()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		out, err = program.Run(ctx, env)
	}
	b.StopTimer()

	if err != nil {
		b.Fatal(err)
	}
	if out.(string) != "func" {
		b.Fatalf("unexpected result %v", out)
	}
}

func Benchmark_callMethod(b *testing.B) {
	program, err := expr.Compile(`Foo.Method()`, engOpts...)
	if err != nil {
		b.Fatal(err)
	}

	env := CallEnv{}

	var out any
	ctx := context.Background()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		out, err = program.Run(ctx, env)
	}
	b.StopTimer()

	if err != nil {
		b.Fatal(err)
	}
	if out.(string) != "method" {
		b.Fatalf("unexpected result %v", out)
	}
}

func Benchmark_callField(b *testing.B) {
	program, err := expr.Compile(`Fn()`, engOpts...)
	if err != nil {
		b.Fatal(err)
	}

	env := CallEnv{
		Fn: func() bool { return true },
	}

	var out any
	ctx := context.Background()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		out, err = program.Run(ctx, env)
	}
	b.StopTimer()

	if err != nil {
		b.Fatal(err)
	}
	if !out.(bool) {
		b.Fatalf("unexpected result %v", out)
	}
}

func Benchmark_largeStructAccess(b *testing.B) {
	type Env struct {
		Data  [1024 * 1024 * 10]byte
		Field int
	}

	program, err := expr.Compile(`Field > 0 && Field > 1 && Field < 99`, engOpts...)
	if err != nil {
		b.Fatal(err)
	}

	env := Env{Field: 21}

	var out any
	ctx := context.Background()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		out, err = program.Run(ctx, &env)
	}
	b.StopTimer()

	if err != nil {
		b.Fatal(err)
	}
	if !out.(bool) {
		b.Fatalf("unexpected result %v", out)
	}
}

func Benchmark_largeNestedStructAccess(b *testing.B) {
	type Env struct {
		Inner struct {
			Data  [1024 * 1024 * 10]byte
			Field int
		}
	}

	program, err := expr.Compile(`Inner.Field > 0 && Inner.Field > 1 && Inner.Field < 99`, engOpts...)
	if err != nil {
		b.Fatal(err)
	}

	env := Env{}
	env.Inner.Field = 21

	var out any
	ctx := context.Background()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		out, err = program.Run(ctx, &env)
	}
	b.StopTimer()

	if err != nil {
		b.Fatal(err)
	}
	if !out.(bool) {
		b.Fatalf("unexpected result %v", out)
	}
}

func Benchmark_largeNestedArrayAccess(b *testing.B) {
	type Env struct {
		Data [1][1024 * 1024 * 10]byte
	}

	program, err := expr.Compile(`Data[0][0] > 0`, engOpts...)
	if err != nil {
		b.Fatal(err)
	}

	env := Env{}
	env.Data[0][0] = 1

	var out any
	ctx := context.Background()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		out, err = program.Run(ctx, &env)
	}
	b.StopTimer()

	if err != nil {
		b.Fatal(err)
	}
	if !out.(bool) {
		b.Fatalf("unexpected result %v", out)
	}
}
