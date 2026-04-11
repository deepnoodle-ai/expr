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

	v, err := expr.Eval(ctx, `user.age >= 18 && contains(user.roles, "admin")`, env, expr.WithBuiltins())
	if err != nil {
		panic(err)
	}
	fmt.Println(v) // true
}
