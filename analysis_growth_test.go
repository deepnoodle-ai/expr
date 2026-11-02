package expr

import (
	"strings"
	"testing"
	"time"
)

// The static passes over a compiled program (Check, Identifiers, Calls)
// walk the syntax tree, and a walk that rescans its enclosing scopes or
// reprints subtrees is quadratic or worse in the nesting, without
// allocating anything TestCompileGrowthIsLinear would see. This test
// times the whole static pipeline on inputs four times apart: linear
// growth is about 4x, quadratic about 16x. Taking the fastest of several
// runs keeps machine load from failing it.

var analysisShapes = []growthShape{
	{name: "nested_map_form", build: func(n int) string { return nest(n, "map(x, ", "it", ")") }},
	{name: "nested_named_filter", build: func(n int) string { return nest(n, "filter(x, e, ", "e", ")") }},
	{name: "nested_if", build: func(n int) string { return nest(n, "if(c, ", "1", ", 2)") }},
	{name: "nested_calls", build: func(n int) string { return nest(n, "f(", "x", ")") }},
	{name: "pipe_chain", build: func(n int) string { return "x" + strings.Repeat(" | f()", n) }},
	{name: "nested_try", build: func(n int) string { return nest(n, "try(", "x.a", ", 1)") }},
	{name: "optional_chain", build: func(n int) string { return "x" + strings.Repeat("?.a", n) }},
	{name: "binary_chain", build: func(n int) string { return "x" + strings.Repeat(" + x", n) }},
	{name: "nested_objects", build: func(n int) string { return nest(n, `{"a": `, "x", "}") }},
}

// narrowingShapes run against nullableChain, where x and every .a below
// it may be nil, so narrowing applies at every node.
var narrowingShapes = []growthShape{
	{name: "narrowing_chain", build: func(n int) string { return "x != nil" + strings.Repeat(" && x.a != nil", n) }},
	{name: "narrowing_or_chain", build: func(n int) string { return "x == nil" + strings.Repeat(" || x.a == nil", n) }},
	{name: "narrowed_deep_optional", build: func(n int) string { return "x != nil && x" + strings.Repeat("?.a", n) }},
	{name: "narrowed_nested_if", build: func(n int) string { return nest(n, "if(x != nil && x.a != nil, x.a.a == nil && ", "true", ", false)") }},
}

// nullableChain is {a: {a: ... }?}? nested depth levels.
func nullableChain(depth int) *Type {
	t := Number().OrNil()
	for i := 0; i < depth; i++ {
		t = ObjectOf(map[string]*Type{"a": t}).OrNil()
	}
	return t
}

func staticPipeline(src string, x *Type) {
	p, err := Compile(src, WithFunctions(map[string]any{"f": func(v any) any { return v }}))
	if err != nil {
		return
	}
	_, _ = p.Check(map[string]*Type{"x": x, "c": Bool()})
	_ = p.Identifiers()
	_ = p.Calls("f")
}

func fastest(runs int, f func()) time.Duration {
	best := time.Duration(1<<63 - 1)
	for i := 0; i < runs; i++ {
		start := time.Now()
		f()
		if d := time.Since(start); d < best {
			best = d
		}
	}
	return best
}

func TestStaticAnalysisGrowthIsLinear(t *testing.T) {
	if testing.Short() {
		t.Skip("times the static passes up to MaxSourceLength per shape")
	}
	deep := nullableChain(MaxSourceLength / 3)
	shapes := append(append([]growthShape{}, analysisShapes...), narrowingShapes...)
	for i, s := range shapes {
		x := Any
		if i >= len(analysisShapes) {
			x = deep
		}
		t.Run(s.name, func(t *testing.T) {
			big := sizeFor(s, MaxSourceLength)
			srcSmall, srcBig := s.build(big/4), s.build(big)
			staticPipeline(srcSmall, x) // warm up
			a := fastest(5, func() { staticPipeline(srcSmall, x) })
			b := fastest(5, func() { staticPipeline(srcBig, x) })
			ratio := float64(b) / float64(a)
			lenRatio := float64(len(srcBig)) / float64(len(srcSmall))
			t.Logf("%d -> %d bytes: %v -> %v (x%.1f)", len(srcSmall), len(srcBig), a, b, ratio)
			if ratio > 2.5*lenRatio {
				t.Errorf("static passes took x%.1f longer for x%.1f input: superlinear", ratio, lenRatio)
			}
			if b > 2*time.Second {
				t.Errorf("static passes took %v on a %d-byte source", b, len(srcBig))
			}
		})
	}
}
