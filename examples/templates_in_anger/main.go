// Templates in anger: compile once, render many, with a registered
// `join` helper so variable-length lists render as human-readable
// text instead of Go slice syntax.
package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/deepnoodle-ai/expr"
)

func main() {
	ctx := context.Background()

	opts := []expr.Option{
		expr.WithBuiltins(),
		expr.WithFunctions(map[string]any{
			"join": func(xs []any, sep string) string {
				parts := make([]string, len(xs))
				for i, v := range xs {
					parts[i] = fmt.Sprintf("%v", v)
				}
				return strings.Join(parts, sep)
			},
			"pluralize": func(n int64, singular, plural string) string {
				if n == 1 {
					return singular
				}
				return plural
			},
		}),
	}

	tmpl, err := expr.NewTemplate(
		`Hi ${user.name}! You have ${len(user.tasks)} open ${pluralize(len(user.tasks), "task", "tasks")}: ${join(map(user.tasks, it.title), ", ")}.`,
		opts...,
	)
	if err != nil {
		panic(err)
	}

	env := map[string]any{
		"user": map[string]any{
			"name": "Grace",
			"tasks": []any{
				map[string]any{"title": "write spec"},
				map[string]any{"title": "review PR"},
				map[string]any{"title": "ship v1"},
			},
		},
	}

	out, err := tmpl.Render(ctx, env)
	if err != nil {
		panic(err)
	}
	fmt.Println(out)
}
