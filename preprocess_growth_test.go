package expr

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
)

// growthShape builds a source of roughly n units of one syntax feature,
// to measure how compile cost grows with input size.
type growthShape struct {
	name     string
	build    func(n int) string
	template bool
}

func nest(n int, open, leaf, close string) string {
	return strings.Repeat(open, n) + leaf + strings.Repeat(close, n)
}

var growthShapes = []growthShape{
	{name: "optional_chain", build: func(n int) string { return "a" + strings.Repeat("?.a", n) }},
	{name: "optional_index_chain", build: func(n int) string { return "a" + strings.Repeat("?[0]", n) }},
	{name: "optional_paren_nest", build: func(n int) string {
		// ((x)?.a)?.a: each `?` walks back over a growing group.
		return nest(n, "(", "x", ")?.a")
	}},
	{name: "optional_in_index", build: func(n int) string { return nest(n, "a?[", "0", "]") }},
	{name: "json_array_nest", build: func(n int) string { return nest(n, "[", "1", "]") }},
	{name: "json_object_nest", build: func(n int) string { return nest(n, `{"a": `, "1", "}") }},
	{name: "json_flat", build: func(n int) string { return "[" + strings.Repeat(`{"a": [1]}, `, n) + "]" }},
	{name: "typed_literal_optional", build: func(n int) string { return strings.Repeat("[]any{1}?[0] + ", n) + "0" }},
	{name: "keyword_fields", build: func(n int) string { return strings.Repeat("x.type + x?.map + ", n) + "0" }},
	{name: "map_if_keywords", build: func(n int) string { return nest(n, "if(c, map(xs, it), ", "0", ")") }},
	{name: "pipe_chain", build: func(n int) string { return "x" + strings.Repeat(" | f()", n) }},
	{name: "comments", build: func(n int) string { return strings.Repeat("x /* [ { ?. map */ ?.a + ", n) + "0" }},
	{name: "template_segments", template: true, build: func(n int) string { return strings.Repeat("a ${x?.a} b ", n) }},
}

func compileShape(s growthShape, src string) {
	if s.template {
		_, _ = NewTemplate(src)
		return
	}
	_, _ = Compile(src)
}

// sizeFor returns the largest unit count whose source fits in limit.
func sizeFor(s growthShape, limit int) int {
	lo, hi := 1, limit
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if len(s.build(mid)) <= limit {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo
}

func allocatedBytes(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// Compile (preprocessing, parse and tree passes) must grow about
// linearly with input size: doubling the input may at most ~2.5x the
// allocated bytes, measured up to MaxSourceLength. Allocation is
// deterministic, unlike wall time, so the bound does not flake.
func TestCompileGrowthIsLinear(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates up to MaxSourceLength per shape")
	}
	for _, s := range growthShapes {
		t.Run(s.name, func(t *testing.T) {
			big := sizeFor(s, MaxSourceLength)
			small := big / 2
			srcSmall, srcBig := s.build(small), s.build(big)
			// Warm up so one-time initialization is not counted.
			compileShape(s, srcSmall)
			a := allocatedBytes(func() { compileShape(s, srcSmall) })
			b := allocatedBytes(func() { compileShape(s, srcBig) })
			ratio := float64(b) / float64(a)
			lenRatio := float64(len(srcBig)) / float64(len(srcSmall))
			t.Logf("%d -> %d bytes of source: %d -> %d bytes allocated (x%.2f)", len(srcSmall), len(srcBig), a, b, ratio)
			if ratio > 1.25*lenRatio {
				t.Errorf("allocation grew x%.2f for x%.2f input: superlinear", ratio, lenRatio)
			}
		})
	}
}

// Each preprocessing stage's output must stay within a constant factor
// of its input, so later stages and the parser see bounded input.
func TestRewriteOutputIsLinear(t *testing.T) {
	for _, s := range growthShapes {
		if s.template {
			continue
		}
		src := s.build(sizeFor(s, MaxSourceLength))
		out, _ := rewriteSource(src)
		// The largest expansion is `?[` → `__try_index__(` plus `, `:
		// 16 bytes for 2.
		if len(out) > 9*len(src) {
			t.Errorf("%s: %d bytes rewritten to %d", s.name, len(src), len(out))
		}
	}
}

func BenchmarkCompileGrowth(b *testing.B) {
	for _, s := range growthShapes {
		for _, limit := range []int{MaxSourceLength / 16, MaxSourceLength / 4, MaxSourceLength} {
			src := s.build(sizeFor(s, limit))
			b.Run(fmt.Sprintf("%s/%dKiB", s.name, limit/1024), func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(len(src)))
				for i := 0; i < b.N; i++ {
					compileShape(s, src)
				}
			})
		}
	}
}

// BenchmarkRewriteGrowth measures the token rewrites alone, without the
// parser and tree passes.
func BenchmarkRewriteGrowth(b *testing.B) {
	for _, s := range growthShapes {
		if s.template {
			continue
		}
		for _, limit := range []int{MaxSourceLength / 16, MaxSourceLength} {
			src := s.build(sizeFor(s, limit))
			b.Run(fmt.Sprintf("%s/%dKiB", s.name, limit/1024), func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(len(src)))
				for i := 0; i < b.N; i++ {
					rewriteSource(src)
				}
			})
		}
	}
}
