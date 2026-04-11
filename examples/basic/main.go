package main

import (
	"context"
	"fmt"

	"github.com/deepnoodle-ai/expr"
)

func main() {
	ctx := context.Background()

	env := map[string]any{
		"user": map[string]any{
			"name":  "ada",
			"age":   36,
			"roles": []any{"admin", "editor"},
		},
	}

	p, err := expr.Compile(`user.age >= 18 && contains(user.roles, "admin")`, expr.WithBuiltins())
	if err != nil {
		panic(err)
	}
	v, err := p.Run(ctx, env)
	if err != nil {
		panic(err)
	}
	fmt.Println(v) // true
}
