package main

import (
	"context"
	"fmt"

	"github.com/deepnoodle-ai/expr"
)

func main() {
	ctx := context.Background()

	pred, err := expr.Compile(`age >= 18 && contains(roles, "admin")`, expr.WithBuiltins())
	if err != nil {
		panic(err)
	}

	people := []map[string]any{
		{"name": "Ada", "age": 36, "roles": []any{"admin"}},
		{"name": "Bob", "age": 17, "roles": []any{"admin"}},
		{"name": "Eve", "age": 41, "roles": []any{"viewer"}},
	}

	for _, p := range people {
		v, _ := pred.Run(ctx, p)
		fmt.Printf("%s => %v\n", p["name"], v)
	}
}
