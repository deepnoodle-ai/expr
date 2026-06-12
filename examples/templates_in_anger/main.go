// Templates in anger: compile once, render many, with a registered
// `join` helper so variable-length lists render as human-readable
// text instead of JSON, and a formatter that overrides nil rendering.
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

	// Composite values render as compact JSON by default.
	configTmpl, err := expr.NewTemplate(`Config: ${config}`, expr.WithBuiltins())
	if err != nil {
		panic(err)
	}
	out, err = configTmpl.Render(ctx, map[string]any{
		"config": map[string]any{"retries": int64(3), "timeout": "30s"},
	})
	if err != nil {
		panic(err)
	}
	fmt.Println(out) // Config: {"retries":3,"timeout":"30s"}

	// Custom formatter overrides rendering for specific types.
	nilTmpl, err := expr.NewTemplate(
		`Nickname: ${nickname}`,
		expr.WithTemplateFormatter(func(v any) (string, bool) {
			if v == nil {
				return "(none)", true
			}
			return "", false
		}),
	)
	if err != nil {
		panic(err)
	}
	out, err = nilTmpl.Render(ctx, map[string]any{"nickname": nil})
	if err != nil {
		panic(err)
	}
	fmt.Println(out) // Nickname: (none)
}
