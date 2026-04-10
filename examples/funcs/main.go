package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/deepnoodle-ai/expr"
)

func main() {
	ctx := context.Background()

	e := expr.New(expr.WithFunctions(map[string]any{
		"upper":     strings.ToUpper,
		"hasPrefix": strings.HasPrefix,
		"greet": func(name string) string {
			return "Hello, " + name + "!"
		},
	}))

	env := map[string]any{"name": "ada"}

	v, err := e.Eval(ctx, `greet(upper(name))`, env)
	if err != nil {
		panic(err)
	}
	fmt.Println(v) // Hello, ADA!
}
