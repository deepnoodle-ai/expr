package main

import (
	"context"
	"fmt"

	"github.com/deepnoodle-ai/expr"
)

func main() {
	ctx := context.Background()

	e := expr.New(expr.WithBuiltins())

	tmpl, err := expr.NewTemplate(e.Compiler(),
		`Hello ${user.name}! You have ${len(user.tasks)} task(s).`)
	if err != nil {
		panic(err)
	}

	out, err := tmpl.Eval(ctx, map[string]any{
		"user": map[string]any{
			"name":  "Ada",
			"tasks": []any{"ship", "deploy", "celebrate"},
		},
	})
	if err != nil {
		panic(err)
	}
	fmt.Println(out) // Hello Ada! You have 3 task(s).
}
