package expr

import (
	"context"
	"testing"

	"github.com/deepnoodle-ai/expr/internal/require"
)

func compileIdents(t *testing.T, src string, opts ...Option) []string {
	t.Helper()
	p, err := Compile(src, opts...)
	require.NoError(t, err)
	return p.Identifiers()
}

func TestIdentifiers_Basic(t *testing.T) {
	require.Equal(t, []string{"a", "b", "d"}, compileIdents(t, "a + b.c[d]"))
}

func TestIdentifiers_SortedAndDeduped(t *testing.T) {
	require.Equal(t, []string{"a", "z"}, compileIdents(t, "z + a + z + a"))
}

func TestIdentifiers_LiteralsExcluded(t *testing.T) {
	require.Equal(t, []string{"x"}, compileIdents(t, "x == nil || true || false"))
}

func TestIdentifiers_ItIndexBoundInsideForms(t *testing.T) {
	// it/index inside an iterating form are bound by the form, not
	// read from the env.
	require.Equal(t, []string{"xs"}, compileIdents(t, "filter(xs, it.age > 18 && index < 10)"))
	// The map keyword goes through the keyword rewrite; same rule.
	require.Equal(t, []string{"xs"}, compileIdents(t, "map(xs, it * 2)"))
	// Nested forms keep the binding.
	require.Equal(t, []string{"users"}, compileIdents(t, "map(users, map(it.friends, it.name))"))
}

func TestIdentifiers_ItAtTopLevelIncluded(t *testing.T) {
	// Outside a form, `it` is an ordinary env identifier.
	require.Equal(t, []string{"it"}, compileIdents(t, "it + 1"))
}

func TestIdentifiers_PredicateOuterNamesIncluded(t *testing.T) {
	// Names other than it/index inside a predicate still resolve
	// through the env.
	require.Equal(t, []string{"threshold", "xs"}, compileIdents(t, "any(xs, it > threshold)"))
}

func TestIdentifiers_FormNamesExcluded(t *testing.T) {
	require.Equal(t, []string{"a", "b", "v"}, compileIdents(t, "try(v.x, nil) || if(a, b, 0)"))
}

func TestIdentifiers_RegisteredFunctionsExcluded(t *testing.T) {
	require.Equal(t, []string{"name"}, compileIdents(t, "upper(name)", WithBuiltins()))
	// Without registration the same call target needs the env.
	require.Equal(t, []string{"name", "upper"}, compileIdents(t, "upper(name)"))
}

func TestIdentifiers_ShadowedFormIsOrdinaryCall(t *testing.T) {
	// A registered "filter" turns the call into an ordinary function
	// call: no it/index binding, and the name resolves via funcs.
	idents := compileIdents(t, "filter(xs, it)", WithFunctions(map[string]any{
		"filter": Func(func(_ context.Context, _ []any) (any, error) { return nil, nil }),
	}))
	require.Equal(t, []string{"it", "xs"}, idents)
}

func TestIdentifiers_OptionalAccess(t *testing.T) {
	require.Equal(t, []string{"i", "user"}, compileIdents(t, "user?.profile?.name == user?[i]"))
}

func TestIdentifiers_JSONLiterals(t *testing.T) {
	require.Equal(t, []string{"v", "x", "y"}, compileIdents(t, `{"k": v, "xs": [x, y]}`))
}

func TestIdentifiers_ReturnsCopy(t *testing.T) {
	p, err := Compile("a + b")
	require.NoError(t, err)
	first := p.Identifiers()
	first[0] = "mutated"
	require.Equal(t, []string{"a", "b"}, p.Identifiers())
}

func TestIdentifiers_EmptyForPureLiteral(t *testing.T) {
	require.Len(t, compileIdents(t, `1 + 2`), 0)
}
