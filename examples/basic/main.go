package main

import (
	"context"
	"fmt"

	"github.com/deepnoodle-ai/expr"
)

func main() {
	ctx := context.Background()

	e := expr.New(expr.WithBuiltins())

	env := map[string]any{
		"user": map[string]any{
			"name":  "ada",
			"age":   36,
			"roles": []any{"admin", "editor"},
		},
	}

	v, err := e.Eval(ctx, `user.age >= 18 && contains(user.roles, "admin")`, env)
	if err != nil {
		panic(err)
	}
	fmt.Println(v) // true
}
