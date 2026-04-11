// Registering Go functions with WithFunctions. Shows the three
// supported return signatures, automatic context.Context injection,
// variadics, and shadowing of a higher-order form.
package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/deepnoodle-ai/expr"
)

func main() {
	ctx := context.Background()

	funcs := map[string]any{
		// T return.
		"upper": strings.ToUpper,

		// (T, error) return — errors propagate out of Program.Run.
		"parseAge": func(s string) (int64, error) {
			var n int64
			if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
				return 0, fmt.Errorf("parseAge: %w", err)
			}
			return n, nil
		},

		// () return — reads as `nil` to the expression.
		"trace": func(msg string) { fmt.Println("trace:", msg) },

		// context.Context as first parameter — injected from Run(ctx, env).
		// Called from expr as `fetchLen("prefix")` (ctx is invisible).
		"fetchLen": func(ctx context.Context, prefix string) int64 {
			select {
			case <-ctx.Done():
				return -1
			default:
				return int64(len(prefix) + 10)
			}
		},

		// Variadic fmt.Sprintf.
		"sprintf": fmt.Sprintf,

		// Shadowing `upper` below with a specialized version would work
		// the same way — last WithFunctions wins.
	}

	sources := []string{
		`upper(name)`,
		`parseAge("36") + 4`,
		`fetchLen("hello-")`,
		`sprintf("%s is %d", name, parseAge("42"))`,
	}

	env := map[string]any{"name": "ada"}

	for _, src := range sources {
		p, err := expr.Compile(src, expr.WithFunctions(funcs))
		if err != nil {
			panic(err)
		}
		v, err := p.Run(ctx, env)
		if err != nil {
			panic(err)
		}
		fmt.Printf("%-40s => %v\n", src, v)
	}
}
