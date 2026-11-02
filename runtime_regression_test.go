package expr

import (
	"context"
	"errors"
	"testing"
)

// Keep a runtime panic local to its subtest so every independent regression
// remains visible in one test run.
func runtimeRegressionNoPanic(t *testing.T) {
	t.Helper()
	if recovered := recover(); recovered != nil {
		t.Errorf("evaluation panicked instead of returning a result or error: %v", recovered)
	}
}

func TestRuntimeRegressionMapKeyComparability(t *testing.T) {
	for _, source := range []string{"m[[1]]", "root.m[[1]]", "m?[[1]]"} {
		t.Run(source, func(t *testing.T) {
			defer runtimeRegressionNoPanic(t)
			m := map[any]any{}
			p, err := Compile(source)
			if err != nil {
				t.Fatal(err)
			}
			_, err = p.Run(t.Context(), map[string]any{
				"m": m, "root": map[string]any{"m": m},
			})
			if !errors.Is(err, ErrEvaluate) {
				t.Fatalf("unhashable map key: got %v, want ErrEvaluate", err)
			}
		})
	}
}

func TestRuntimeRegressionEqualityComparability(t *testing.T) {
	for _, value := range []struct {
		name string
		x    any
	}{
		{"struct", struct{ V any }{[]int{1}}},
		{"array", [1]any{[]int{1}}},
	} {
		for _, source := range []string{"x == x", "x != x", "contains([x], x)"} {
			t.Run(value.name+"/"+source, func(t *testing.T) {
				defer runtimeRegressionNoPanic(t)
				p, err := Compile(source, WithBuiltins())
				if err != nil {
					t.Fatal(err)
				}
				got, err := p.Run(t.Context(), map[string]any{"x": value.x})
				if source == "contains([x], x)" {
					// contains ignores elements that cannot be compared.
					if err != nil || got != false {
						t.Fatalf("contains incomparable value: got (%v, %v), want (false, nil)", got, err)
					}
				} else if !errors.Is(err, ErrEvaluate) {
					t.Fatalf("incomparable equality: got %v, want ErrEvaluate", err)
				}
			})
		}
	}
}

func TestRuntimeRegressionValueDependentConversion(t *testing.T) {
	for _, registered := range []bool{true, false} {
		name := "env"
		if registered {
			name = "registered"
		}
		t.Run(name, func(t *testing.T) {
			defer runtimeRegressionNoPanic(t)
			called := false
			fn := func([2]int) int { called = true; return 0 }
			env := map[string]any{"xs": []int{1}}
			var opts []Option
			if registered {
				opts = append(opts, WithFunctions(map[string]any{"f": fn}))
			} else {
				env["f"] = fn
			}
			p, err := Compile("f(xs)", opts...)
			if err != nil {
				t.Fatal(err)
			}
			_, err = p.Run(t.Context(), env)
			if !errors.Is(err, ErrEvaluate) {
				t.Fatalf("short slice conversion: got %v, want ErrEvaluate", err)
			}
			if called {
				t.Fatal("function called with an invalid argument")
			}
		})
	}
	for _, source := range []string{"m[xs]", "root.m[xs]", "m?[xs]"} {
		t.Run(source, func(t *testing.T) {
			defer runtimeRegressionNoPanic(t)
			m := map[[2]int]string{}
			p, err := Compile(source)
			if err != nil {
				t.Fatal(err)
			}
			_, err = p.Run(t.Context(), map[string]any{
				"m": m, "root": map[string]any{"m": m}, "xs": []int{1},
			})
			if !errors.Is(err, ErrEvaluate) {
				t.Fatalf("short slice map key conversion: got %v, want ErrEvaluate", err)
			}
		})
	}
}

type runtimeRegressionConcreteError struct{}

func (runtimeRegressionConcreteError) Error() string { return "concrete error" }

func TestRuntimeRegressionConcreteErrorReturn(t *testing.T) {
	for _, registered := range []bool{true, false} {
		name := "env"
		if registered {
			name = "registered"
		}
		t.Run(name, func(t *testing.T) {
			defer runtimeRegressionNoPanic(t)
			called := false
			fn := func() (int, runtimeRegressionConcreteError) {
				called = true
				return 0, runtimeRegressionConcreteError{}
			}
			env := map[string]any{}
			var opts []Option
			if registered {
				opts = append(opts, WithFunctions(map[string]any{"f": fn}))
			} else {
				env["f"] = fn
			}
			p, err := Compile("f()", opts...)
			if err != nil {
				if !registered || !errors.Is(err, ErrCompile) {
					t.Fatalf("unexpected compilation failure: %v", err)
				}
				return // Rejecting this signature at registration is also safe.
			}
			_, err = p.Run(t.Context(), env)
			if !errors.Is(err, ErrEvaluate) {
				t.Fatalf("got %v, want ErrEvaluate for signature rejection or function error", err)
			}
			if called && !errors.Is(err, runtimeRegressionConcreteError{}) {
				t.Fatalf("function error lost its original cause: %v", err)
			}
		})
	}
}

func TestRuntimeRegressionNilNativeFunction(t *testing.T) {
	for _, registered := range []bool{true, false} {
		name := "env"
		if registered {
			name = "registered"
		}
		t.Run(name, func(t *testing.T) {
			defer runtimeRegressionNoPanic(t)
			env := map[string]any{}
			var opts []Option
			if registered {
				opts = append(opts, WithFunctions(map[string]any{"f": Func(nil)}))
			} else {
				env["f"] = Func(nil)
			}
			p, err := Compile("f()", opts...)
			if registered {
				if !errors.Is(err, ErrCompile) {
					t.Fatalf("nil native function registration: got %v, want ErrCompile", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = p.Run(t.Context(), env)
			if !errors.Is(err, ErrEvaluate) {
				t.Fatalf("nil native function: got %v, want ErrEvaluate", err)
			}
		})
	}
}

func TestRuntimeRegressionFilterIndexEvaluation(t *testing.T) {
	for _, source := range []string{
		"filter([1, 0], 1 / it)[0]",
		"filter([1, 0], x, 1 / x)[0]",
		"filter([1, 0], 1 / it)[i]",
	} {
		t.Run(source, func(t *testing.T) {
			defer runtimeRegressionNoPanic(t)
			p, err := Compile(source)
			if err != nil {
				t.Fatal(err)
			}
			_, err = p.Run(t.Context(), map[string]any{"i": 0})
			if !errors.Is(err, ErrEvaluate) {
				t.Fatalf("filter must evaluate its later failing predicate before indexing: got %v", err)
			}
		})
	}
	t.Run("predicate calls", func(t *testing.T) {
		defer runtimeRegressionNoPanic(t)
		calls := 0
		p, err := Compile("filter([1, 2, 3], predicate(it))[0]", WithFunctions(map[string]any{
			"predicate": Func(func(context.Context, []any) (any, error) {
				calls++
				return true, nil
			}),
		}))
		if err != nil {
			t.Fatal(err)
		}
		got, err := p.Run(t.Context(), nil)
		if err != nil || got != int64(1) {
			t.Fatalf("got (%v, %v), want (1, nil)", got, err)
		}
		if calls != 3 {
			t.Fatalf("predicate called %d times, want 3", calls)
		}
	})
}

func TestRuntimeRegressionSumMixedNumericPromotion(t *testing.T) {
	for _, source := range []string{
		"sum([9223372036854775807, 1, 0.0])",
		"sum([0.0, 9223372036854775807, 1])",
	} {
		t.Run(source, func(t *testing.T) {
			defer runtimeRegressionNoPanic(t)
			p, err := Compile(source, WithFunctions(CollectionFuncs()))
			if err != nil {
				t.Fatal(err)
			}
			got, err := p.Run(t.Context(), nil)
			if err != nil || got != float64(uint64(1)<<63) {
				t.Fatalf("mixed sum: got (%v, %v), want (float64(2^63), nil)", got, err)
			}
		})
	}
}
