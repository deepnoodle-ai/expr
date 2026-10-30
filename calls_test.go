package expr

import (
	"context"
	"testing"

	"github.com/deepnoodle-ai/expr/internal/require"
)

func askFuncs() Option {
	return WithFunctions(map[string]any{
		"ask":  func(q string, x any) float64 { return 0.5 },
		"pick": func(q string, x any, opts []any) any { return opts[0] },
	})
}

func compileCalls(t *testing.T, src string, names ...string) []Call {
	t.Helper()
	p, err := Compile(src, askFuncs(), WithBuiltins())
	require.NoError(t, err)
	return p.Calls(names...)
}

func TestCalls_SourceOrderAndArgs(t *testing.T) {
	calls := compileCalls(t, `ask("a?", x.body) > 0.8 || pick("b?", y, ["p", "q"]) == "p"`, "ask", "pick")
	require.Len(t, calls, 2)
	require.Equal(t, "ask", calls[0].Name)
	require.Equal(t, "pick", calls[1].Name)
	require.Len(t, calls[0].Args, 2)
	require.Len(t, calls[1].Args, 3)
	require.Equal(t, "x.body", calls[0].Args[1].Source())
	require.Equal(t, []string{"x"}, calls[0].Args[1].Identifiers())
	require.False(t, calls[0].Bound)
}

func TestCalls_OnlyNamedFunctions(t *testing.T) {
	require.Len(t, compileCalls(t, `ask("a?", len(x)) > 0`, "pick"), 0)
	require.Len(t, compileCalls(t, `ask("a?", len(x)) > 0`, "ask"), 1)
}

func TestCalls_ArgumentsRunAlone(t *testing.T) {
	calls := compileCalls(t, `ask("a?", {"n": len(x.items), "t": x?.title}) > 0.5`, "ask")
	require.Len(t, calls, 1)
	v, err := calls[0].Args[1].Run(context.Background(), map[string]any{
		"x": map[string]any{"items": []any{1, 2}, "title": "hi"},
	})
	require.NoError(t, err)
	require.Equal(t, map[string]any{"n": 2, "t": "hi"}, v)
}

func TestCalls_NestedAndShortCircuitBranches(t *testing.T) {
	// Every call is reported, even in a branch Run would skip.
	calls := compileCalls(t, `false && ask("a?", x) > 0 || if(c, ask("b?", ask("c?", y)) > 0, false)`, "ask")
	require.Len(t, calls, 3)
	q := func(c Call) any { v, _ := c.Args[0].Literal(); return v }
	require.Equal(t, "a?", q(calls[0]))
	require.Equal(t, "b?", q(calls[1]))
	require.Equal(t, "c?", q(calls[2]))
}

func TestCalls_Bound(t *testing.T) {
	calls := compileCalls(t, `any(filter(xs, ask("a?", it) > 0.5), ask("b?", it) > 0) && ask("c?", xs) > 0`, "ask")
	require.Len(t, calls, 3)
	require.True(t, calls[0].Bound)  // filter body
	require.True(t, calls[1].Bound)  // any body
	require.False(t, calls[2].Bound) // top level
	named := compileCalls(t, `map(xs, x, ask("a?", x))`, "ask")
	require.Len(t, named, 1)
	require.True(t, named[0].Bound)
	coll := compileCalls(t, `map([ask("a?", xs)], it)`, "ask")
	require.Len(t, coll, 1)
	require.False(t, coll[0].Bound) // the collection is evaluated once
}

func TestCalls_NamedBindingShadows(t *testing.T) {
	// Inside the body, ask names the element, so ask(...) is not a call
	// of the function; its argument still is walked.
	require.Len(t, compileCalls(t, `map(xs, ask, ask("a?", ask))`, "ask"), 0)
	calls := compileCalls(t, `map(xs, ask, ask(pick("b?", ask, ["p"])))`, "ask", "pick")
	require.Len(t, calls, 1)
	require.Equal(t, "pick", calls[0].Name)
	// Outside the body the name is the function again.
	require.Len(t, compileCalls(t, `map(xs, ask, ask) == [] || ask("c?", xs) > 0`, "ask"), 1)
}

func TestCalls_BoundArgumentRunsOnlyPerElement(t *testing.T) {
	calls := compileCalls(t, `any(xs, ask("a?", it.body) > 0.5)`, "ask")
	require.Len(t, calls, 1)
	require.True(t, calls[0].Bound)
	_, err := calls[0].Args[1].Run(context.Background(), map[string]any{"xs": []any{}})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrEvaluate)
}

func TestCalls_Pipe(t *testing.T) {
	calls := compileCalls(t, `x | ask("a?") > 0`, "ask")
	require.Len(t, calls, 1)
	require.Equal(t, "x", calls[0].Args[0].Source())
	_, isLit := calls[0].Args[0].Literal()
	require.False(t, isLit)
}

func TestLiteral(t *testing.T) {
	for _, tc := range []struct {
		src  string
		want any
	}{
		{`"a"`, "a"},
		{`3`, int64(3)},
		{`-1.5`, -1.5},
		{`2 * 3`, int64(6)},
		{`true`, true},
		{`nil`, nil},
		{`["a", "b"]`, []any{"a", "b"}},
		{`{"k": [1, {"n": false}]}`, map[string]any{"k": []any{int64(1), map[string]any{"n": false}}}},
		{`[]`, []any{}},
		{`("a")`, "a"},
	} {
		p, err := Compile(tc.src)
		require.NoError(t, err)
		v, ok := p.Literal()
		require.True(t, ok, tc.src)
		require.Equal(t, tc.want, v, tc.src)
	}
	for _, src := range []string{`x`, `"a" + x`, `["a", x]`, `{"k": x}`, `len("a")`, `x == 1`} {
		p, err := Compile(src, WithBuiltins())
		require.NoError(t, err)
		_, ok := p.Literal()
		require.False(t, ok, src)
	}
}
