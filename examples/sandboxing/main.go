// Sandboxing: compile an untrusted policy expression, run it with a
// deadline, and catch both eval errors and cancellation.
package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/deepnoodle-ai/expr"
)

func main() {
	// A policy rule, the kind of thing you'd load from a database.
	src := `user.role == "admin" || (user.role == "viewer" && resource.public)`

	// Belt-and-suspenders: reject inputs over a host-side cap even
	// before Compile. expr.MaxSourceLength is the library's own bound
	// (64 KiB); this is an extra policy-specific bound.
	const policyMax = 8 * 1024
	if len(src) > policyMax {
		panic("policy too long")
	}

	// Compile once. Only builtins are registered — no I/O, no mutation.
	// The eval budget caps total work per Run, so hostile nesting like
	// map(xs, map(xs, map(xs, it))) fails deterministically instead of
	// burning a core until the deadline.
	p, err := expr.Compile(src, expr.WithBuiltins(), expr.WithEvalBudget(100_000))
	if err != nil {
		panic(err)
	}

	env := map[string]any{
		"user":     map[string]any{"role": "viewer"},
		"resource": map[string]any{"public": true},
	}

	// Always run with a deadline. The default should be "if this takes
	// more than a few ms, something is wrong."
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	out, err := p.Run(ctx, env)
	switch {
	case err == nil:
		fmt.Println("allow:", out)
	case errors.Is(err, context.DeadlineExceeded):
		fmt.Println("policy eval exceeded deadline")
	case errors.Is(err, expr.ErrEvaluate):
		fmt.Println("policy eval failed:", err)
	default:
		fmt.Println("unexpected error:", err)
	}
}
