package expr

import (
	"strings"
	"testing"

	"github.com/deepnoodle-ai/expr/internal/require"
)

// These tests pin the reflect-space index-chain fast path
// (evalIndexChainRV) to the general path's behavior. Struct envs route
// ident-rooted chains like Grid[1][2] or Inner.Vals[0] through the fast
// path, so values and error messages must match what indexValue and
// selectField produce for the same shapes.

type indexChainEnv struct {
	Grid  [2][3]int
	Items []string
	Meta  map[string]int
	Mixed map[string]any
	Inner struct {
		Vals []int
	}
	Text string
}

func newIndexChainEnv() indexChainEnv {
	env := indexChainEnv{
		Grid:  [2][3]int{{1, 2, 3}, {4, 5, 6}},
		Items: []string{"a", "b", "c"},
		Meta:  map[string]int{"x": 7},
		Mixed: map[string]any{"list": []any{int64(10), int64(20)}},
		Text:  "héllo",
	}
	env.Inner.Vals = []int{42, 43}
	return env
}

func TestIndexChainStructEnv(t *testing.T) {
	env := newIndexChainEnv()
	cases := []struct {
		src  string
		want any
	}{
		{src: `Grid[1][2]`, want: 6},
		{src: `Grid[0][0]`, want: 1},
		{src: `Items[1]`, want: "b"},
		{src: `Meta["x"]`, want: 7},
		{src: `Mixed["list"][1]`, want: int64(20)},
		{src: `Inner.Vals[0]`, want: 42},
		{src: `Text[1]`, want: "é"},
		{src: `Items[Grid[0][0]]`, want: "b"},
	}
	for _, tc := range cases {
		t.Run(tc.src, func(t *testing.T) {
			got, err := evalExpr(t.Context(), tc.src, env)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestIndexChainStructEnvErrors(t *testing.T) {
	env := newIndexChainEnv()
	cases := []struct {
		src     string
		wantErr string
	}{
		{src: `Items[5]`, wantErr: "index 5 out of range [0, 3)"},
		{src: `Grid[0][9]`, wantErr: "index 9 out of range [0, 3)"},
		{src: `Items[-1]`, wantErr: "index -1 out of range"},
		// Non-map[string]any maps format the missing key with %v,
		// matching indexValue's general map branch.
		{src: `Meta["nope"]`, wantErr: `key nope not found`},
		{src: `Mixed[0]`, wantErr: "map index must be string, got int64"},
		{src: `Grid[0]["x"]`, wantErr: "index must be integer"},
		{src: `Meta["x"][0]`, wantErr: "cannot index int"},
		{src: `Inner.Nope[0]`, wantErr: `field "Nope" not found`},
		{src: `Text[99]`, wantErr: "index 99 out of range [0, 5)"},
	}
	for _, tc := range cases {
		t.Run(tc.src, func(t *testing.T) {
			_, err := evalExpr(t.Context(), tc.src, env)
			require.Error(t, err)
			require.ErrorIs(t, err, ErrEvaluate)
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// The same expressions evaluated against a map env take the general
// path (single hops) or enter the chain with a boxed root (multi hop);
// results must agree with the struct-env fast path.
func TestIndexChainMapEnvParity(t *testing.T) {
	structEnv := newIndexChainEnv()
	mapEnv := map[string]any{
		"Grid":  structEnv.Grid,
		"Items": structEnv.Items,
		"Meta":  structEnv.Meta,
		"Mixed": structEnv.Mixed,
		"Inner": structEnv.Inner,
		"Text":  structEnv.Text,
	}
	srcs := []string{
		`Grid[1][2]`, `Items[1]`, `Meta["x"]`, `Mixed["list"][1]`,
		`Inner.Vals[0]`, `Text[1]`, `Items[Grid[0][0]]`,
	}
	for _, src := range srcs {
		t.Run(src, func(t *testing.T) {
			fromStruct, err := evalExpr(t.Context(), src, structEnv)
			require.NoError(t, err)
			fromMap, err := evalExpr(t.Context(), src, mapEnv)
			require.NoError(t, err)
			require.Equal(t, fromStruct, fromMap)
		})
	}
}
