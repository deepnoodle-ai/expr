package expr

import (
	"testing"

	"github.com/deepnoodle-ai/expr/internal/require"
)

func bindingEnv() map[string]any {
	return map[string]any{
		"orders": []any{
			map[string]any{"status": "paid", "n": int64(2)},
			map[string]any{"status": "open", "n": int64(1)},
			map[string]any{"status": "paid", "n": int64(3)},
		},
		"reviews": []any{
			map[string]any{"author": "ann", "comments": []any{"a1", "a2"}},
			map[string]any{"author": "bob", "comments": []any{"b1"}},
		},
		"users": []any{
			map[string]any{"orders": []any{int64(1), int64(2)}},
			map[string]any{"orders": []any{int64(3)}},
		},
	}
}

func TestNamedBinding_AllIteratingForms(t *testing.T) {
	env := bindingEnv()

	v, err := evalExpr(t.Context(), `map(orders, o, o.n)`, env)
	require.NoError(t, err)
	require.Equal(t, []any{int64(2), int64(1), int64(3)}, v)

	v, err = evalExpr(t.Context(), `filter(orders, o, o.status == "paid")`, env)
	require.NoError(t, err)
	require.Len(t, v.([]any), 2)

	v, err = evalExpr(t.Context(), `any(orders, o, o.n > 2)`, env)
	require.NoError(t, err)
	require.Equal(t, true, v)

	v, err = evalExpr(t.Context(), `all(orders, o, o.n > 0)`, env)
	require.NoError(t, err)
	require.Equal(t, true, v)

	v, err = evalExpr(t.Context(), `find(orders, o, o.status == "open")`, env)
	require.NoError(t, err)
	require.Equal(t, map[string]any{"status": "open", "n": int64(1)}, v)

	v, err = evalExpr(t.Context(), `count(orders, o, o.status == "paid")`, env)
	require.NoError(t, err)
	require.Equal(t, int64(2), v)
}

// The three-arg form binds only the chosen name plus index. `it` is
// not bound, so it resolves to an enclosing two-arg form (or fails as
// undefined at top level). This is the motivating nested case: the
// guide used to say there was no way to spell outer `it` from inside
// an inner body.
func TestNamedBinding_NestedScoping(t *testing.T) {
	env := bindingEnv()

	// Outer named, inner two-arg: `it` is the inner element, `r` the outer.
	v, err := evalExpr(t.Context(), `map(reviews, r, join(map(r.comments, r.author + "/" + it), ","))`, env,
		WithBuiltins(), WithFunctions(StringFuncs()))
	require.NoError(t, err)
	require.Equal(t, []any{"ann/a1,ann/a2", "bob/b1"}, v)

	// Outer two-arg, inner named: outer `it` reachable from inner body.
	v, err = evalExpr(t.Context(), `map(reviews, map(it.comments, c, it.author + "/" + c))`, env)
	require.NoError(t, err)
	require.Equal(t, []any{[]any{"ann/a1", "ann/a2"}, []any{"bob/b1"}}, v)

	// index inside a named form refers to the innermost form.
	v, err = evalExpr(t.Context(), `map(orders, o, index)`, env)
	require.NoError(t, err)
	require.Equal(t, []any{int64(0), int64(1), int64(2)}, v)

	// Inner named binding shadows an equally-named outer binding.
	v, err = evalExpr(t.Context(), `map(users, u, map(u.orders, u, u))[0]`, env)
	require.NoError(t, err)
	require.Equal(t, []any{int64(1), int64(2)}, v)
}

func TestNamedBinding_ItUndefinedInsideNamedForm(t *testing.T) {
	env := bindingEnv()
	_, err := evalExpr(t.Context(), `map(orders, o, it)`, env)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), `undefined identifier "it"`)
}

func TestNamedBinding_ShadowsEnvName(t *testing.T) {
	env := map[string]any{
		"x":  "outer",
		"xs": []any{"inner"},
	}
	v, err := evalExpr(t.Context(), `map(xs, x, x)`, env)
	require.NoError(t, err)
	require.Equal(t, []any{"inner"}, v)
}

func TestNamedBinding_InvalidBindings(t *testing.T) {
	env := bindingEnv()
	cases := []struct {
		src  string
		want string
	}{
		{`map(orders, it, 1)`, `map binding cannot be named "it"`},
		{`map(orders, index, 1)`, `map binding cannot be named "index"`},
		{`map(orders, nil, 1)`, `map binding cannot be named "nil"`},
		{`map(orders, true, 1)`, `map binding cannot be named "true"`},
		{`map(orders, map, 1)`, `map binding cannot be named "map"`},
		{`map(orders, if, 1)`, `map binding cannot be named "if"`},
		{`map(orders, o.x, 1)`, "map binding must be a plain identifier, got `o.x`"},
		{`map(orders, (o), 1)`, "map binding must be a plain identifier, got `(o)`"},
		{`map(orders, o, 1, 2)`, "map expects 2 arguments (collection, predicate) or 3 (collection, name, predicate), got 4"},
	}
	for _, tc := range cases {
		_, err := evalExpr(t.Context(), tc.src, env)
		require.ErrorIs(t, err, ErrEvaluate)
		require.Contains(t, err.Error(), tc.want)
	}
}

// The streamed filter(xs, p)[n] fast path must honor the three-arg
// form, including its binding validation errors.
func TestNamedBinding_FilterIndexFastPath(t *testing.T) {
	env := bindingEnv()

	v, err := evalExpr(t.Context(), `filter(orders, o, o.status == "paid")[1]`, env)
	require.NoError(t, err)
	require.Equal(t, map[string]any{"status": "paid", "n": int64(3)}, v)

	_, err = evalExpr(t.Context(), `filter(orders, it, 1)[0]`, env)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), `filter binding cannot be named "it"`)
}

func TestFlatMap(t *testing.T) {
	env := bindingEnv()

	v, err := evalExpr(t.Context(), `flatMap(users, u, u.orders)`, env)
	require.NoError(t, err)
	require.Equal(t, []any{int64(1), int64(2), int64(3)}, v)

	v, err = evalExpr(t.Context(), `flatMap(users, it.orders)`, env)
	require.NoError(t, err)
	require.Equal(t, []any{int64(1), int64(2), int64(3)}, v)

	// Non-list body results append as single elements; lists splice.
	v, err = evalExpr(t.Context(), `flatMap([1, [2, 3], 4], it)`, env)
	require.NoError(t, err)
	require.Equal(t, []any{int64(1), int64(2), int64(3), int64(4)}, v)

	// Splicing is one level deep only.
	v, err = evalExpr(t.Context(), `flatMap([[1, [2]], [3]], it)`, env)
	require.NoError(t, err)
	require.Equal(t, []any{int64(1), []any{int64(2)}, int64(3)}, v)

	// nil body results are spliced as nothing, mirroring the
	// nil-is-an-empty-list rule used by the forms' first argument.
	v, err = evalExpr(t.Context(), `flatMap([1, 2, 3], if(it > 1, [it, it], nil))`, env)
	require.NoError(t, err)
	require.Equal(t, []any{int64(2), int64(2), int64(3), int64(3)}, v)

	// Strings are not lists: they append whole, never splice to runes.
	v, err = evalExpr(t.Context(), `flatMap(["ab", "c"], it)`, env)
	require.NoError(t, err)
	require.Equal(t, []any{"ab", "c"}, v)

	// Typed slices from the env splice like []any does.
	v, err = evalExpr(t.Context(), `flatMap(pairs, it)`, map[string]any{
		"pairs": []any{[]int{1, 2}, []int{3}},
	})
	require.NoError(t, err)
	require.Equal(t, []any{1, 2, 3}, v)

	// Empty and nil collections behave like the other forms.
	v, err = evalExpr(t.Context(), `flatMap(nil, it)`, env)
	require.NoError(t, err)
	require.Equal(t, []any{}, v)

	_, err = evalExpr(t.Context(), `flatMap(orders)`, env)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "flatMap expects 2 arguments")
}

func TestSortBy(t *testing.T) {
	env := bindingEnv()

	v, err := evalExpr(t.Context(), `map(sortBy(orders, o, o.n), o, o.n)`, env)
	require.NoError(t, err)
	require.Equal(t, []any{int64(1), int64(2), int64(3)}, v)

	v, err = evalExpr(t.Context(), `map(sortBy(orders, it.n), it.n)`, env)
	require.NoError(t, err)
	require.Equal(t, []any{int64(1), int64(2), int64(3)}, v)

	// String keys sort lexically.
	v, err = evalExpr(t.Context(), `map(sortBy(orders, o, o.status), o, o.status)`, env)
	require.NoError(t, err)
	require.Equal(t, []any{"open", "paid", "paid"}, v)

	// Mixed int/float keys compare numerically.
	v, err = evalExpr(t.Context(), `sortBy([2.5, 1, 3], it)`, env)
	require.NoError(t, err)
	require.Equal(t, []any{int64(1), 2.5, int64(3)}, v)

	// Stable: equal keys preserve input order.
	v, err = evalExpr(t.Context(), `map(sortBy(orders, o, o.status), o, o.n)`, env)
	require.NoError(t, err)
	require.Equal(t, []any{int64(1), int64(2), int64(3)}, v)

	// The input list is reordered as a copy, never in place.
	orders := env["orders"].([]any)
	require.Equal(t, map[string]any{"status": "paid", "n": int64(2)}, orders[0])

	// Mixed or non-comparable key types are errors.
	_, err = evalExpr(t.Context(), `sortBy([1, "a"], it)`, env)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "sortBy: element 1 is string, not a number")

	_, err = evalExpr(t.Context(), `sortBy(orders, o, o)`, env)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "sortBy: elements must be all numbers or all strings")

	// Key expressions that error are reported like any predicate error.
	_, err = evalExpr(t.Context(), `sortBy(orders, o, o.missing)`, env)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), "sortBy predicate `o.missing` failed on element 0")

	// Empty and nil collections sort to empty.
	v, err = evalExpr(t.Context(), `sortBy(nil, it)`, env)
	require.NoError(t, err)
	require.Equal(t, []any{}, v)
}

func TestNamedBinding_Identifiers(t *testing.T) {
	p, err := Compile(`map(orders, o, o.n + tax)`)
	require.NoError(t, err)
	require.Equal(t, []string{"orders", "tax"}, p.Identifiers())

	// The binding identifier itself is not an env reference.
	p, err = Compile(`flatMap(users, u, u.orders)`)
	require.NoError(t, err)
	require.Equal(t, []string{"users"}, p.Identifiers())

	// `it` inside a three-arg form is NOT bound by it: only an
	// enclosing two-arg form can bind it, otherwise it is an env name.
	p, err = Compile(`map(orders, o, it)`)
	require.NoError(t, err)
	require.Equal(t, []string{"it", "orders"}, p.Identifiers())

	// Nested: outer two-arg binds it, inner named binds c; index is
	// bound by both.
	p, err = Compile(`map(reviews, map(it.comments, c, c + it.author + index))`)
	require.NoError(t, err)
	require.Equal(t, []string{"reviews"}, p.Identifiers())

	// Outside the body, the binding name is a plain env reference.
	p, err = Compile(`map(orders, o, o.n) + len(o)`, WithBuiltins())
	require.NoError(t, err)
	require.Equal(t, []string{"o", "orders"}, p.Identifiers())

	// sortBy participates like the other binding forms.
	p, err = Compile(`sortBy(files, f, f.additions)`)
	require.NoError(t, err)
	require.Equal(t, []string{"files"}, p.Identifiers())
}

// A binding name should appear in did-you-mean candidates for typos
// inside the body.
func TestNamedBinding_Suggestion(t *testing.T) {
	env := bindingEnv()
	_, err := evalExpr(t.Context(), `map(orders, order, ordr.n)`, env)
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), `did you mean "order"?`)
}

// Env entries and registered functions still shadow the forms,
// three-arg calls included: a user function named flatMap receives
// three evaluated arguments instead of form treatment.
func TestNamedBinding_FormShadowing(t *testing.T) {
	env := bindingEnv()
	called := false
	fn := func(args ...any) any {
		called = true
		return "shadowed"
	}
	v, err := evalExpr(t.Context(), `flatMap(users, 1, 2)`, env, WithFunctions(map[string]any{
		"flatMap": fn,
	}))
	require.NoError(t, err)
	require.Equal(t, "shadowed", v)
	require.True(t, called)
}
