package expr

import (
	"errors"
	"testing"

	"github.com/deepnoodle-ai/expr/internal/require"
)

type taggedUser struct {
	DisplayName string `expr:"name" json:"display_name"`
	Email       string `json:"email,omitempty"`
	SourceID    string `json:",omitempty"`
	Secret      string `expr:"-"`
	Internal    string `json:"-"`
}

func TestStructTags_OptInPreservesDefaultBehavior(t *testing.T) {
	type environment struct {
		Status string `json:"status"`
	}
	type output struct {
		Environment environment `json:"environment"`
	}
	env := map[string]any{
		"result": output{Environment: environment{Status: "ready"}},
	}

	got, err := evalExpr(t.Context(), `result.Environment.Status`, env)
	require.NoError(t, err)
	require.Equal(t, "ready", got)

	_, err = evalExpr(t.Context(), `result.environment.status`, env)
	require.ErrorIs(t, err, ErrEvaluate)

	got, err = evalExpr(t.Context(), `result.environment.status`, env, WithStructTags("json"))
	require.NoError(t, err)
	require.Equal(t, "ready", got)
}

func TestStructTags_PrecedenceFallbackAndHiding(t *testing.T) {
	env := map[string]any{
		"user": taggedUser{
			DisplayName: "Ada Lovelace",
			Email:       "ada@example.test",
			SourceID:    "src-1",
			Secret:      "nope",
			Internal:    "available-by-go-name",
		},
	}
	opts := []Option{WithStructTags("expr", "json")}

	got, err := evalExpr(t.Context(), `user.name`, env, opts...)
	require.NoError(t, err)
	require.Equal(t, "Ada Lovelace", got)

	got, err = evalExpr(t.Context(), `user.email`, env, opts...)
	require.NoError(t, err)
	require.Equal(t, "ada@example.test", got)

	got, err = evalExpr(t.Context(), `user.SourceID`, env, opts...)
	require.NoError(t, err)
	require.Equal(t, "src-1", got)

	got, err = evalExpr(t.Context(), `user.Internal`, env, opts...)
	require.NoError(t, err)
	require.Equal(t, "available-by-go-name", got)

	for _, src := range []string{
		`user.display_name`,
		`user.DisplayName`,
		`user.Secret`,
	} {
		_, err := evalExpr(t.Context(), src, env, opts...)
		require.ErrorIs(t, err, ErrEvaluate)
	}
}

func TestStructTags_TopLevelStructEnv(t *testing.T) {
	type env struct {
		User taggedUser `json:"user"`
	}

	got, err := evalExpr(t.Context(), `user.email`, env{
		User: taggedUser{Email: "ada@example.test"},
	}, WithFieldTags("json"))
	require.NoError(t, err)
	require.Equal(t, "ada@example.test", got)
}

func TestStructTags_NestedPointersAndEmbeddedStructs(t *testing.T) {
	type environment struct {
		Status string `json:"status"`
	}
	type embedded struct {
		Ready bool `json:"ready"`
	}
	type output struct {
		*embedded
		Environment *environment `json:"environment"`
	}
	env := map[string]any{
		"result": &output{
			embedded:    &embedded{Ready: true},
			Environment: &environment{Status: "ready"},
		},
	}
	opts := []Option{WithStructTags("json")}

	got, err := evalExpr(t.Context(), `result.environment.status == "ready" && result.ready`, env, opts...)
	require.NoError(t, err)
	require.Equal(t, true, got)
}

func TestStructTags_AmbiguousFieldAccessErrors(t *testing.T) {
	type conflict struct {
		Foo string `json:"value"`
		Bar string `json:"value"`
	}

	_, err := evalExpr(t.Context(), `x.value`, map[string]any{
		"x": conflict{Foo: "a", Bar: "b"},
	}, WithStructTags("json"))
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), `field "value" is ambiguous`)
}

func TestStructTags_CallableFieldAndSuggestions(t *testing.T) {
	type helpers struct {
		Double func(int64) int64 `json:"double"`
	}
	env := map[string]any{
		"h": helpers{Double: func(n int64) int64 { return n * 2 }},
	}

	got, err := evalExpr(t.Context(), `h.double(21)`, env, WithStructTags("json"))
	require.NoError(t, err)
	require.Equal(t, int64(42), got)

	_, err = evalExpr(t.Context(), `h.dobule`, env, WithStructTags("json"))
	require.ErrorIs(t, err, ErrEvaluate)
	require.Contains(t, err.Error(), `did you mean "double"?`)
}

func TestStructTags_AmbiguityStillWrapsErrEvaluate(t *testing.T) {
	type conflict struct {
		Foo string `json:"value"`
		Bar string `json:"value"`
	}

	p, err := Compile(`value`, WithStructTags("json"))
	require.NoError(t, err)
	_, err = p.Run(t.Context(), conflict{Foo: "a", Bar: "b"})
	if !errors.Is(err, ErrEvaluate) {
		t.Fatalf("err = %v, want ErrEvaluate", err)
	}
}
