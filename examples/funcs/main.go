package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/deepnoodle-ai/expr"
)

func main() {
	ctx := context.Background()

	env := map[string]any{"name": "ada"}

	p, err := expr.Compile(`greet(upper(name))`, expr.WithFunctions(map[string]any{
		"upper":     strings.ToUpper,
		"hasPrefix": strings.HasPrefix,
		"greet": func(name string) string {
			return "Hello, " + name + "!"
		},
	}))
	if err != nil {
		panic(err)
	}
	v, err := p.Run(ctx, env)
	if err != nil {
		panic(err)
	}
	fmt.Println(v) // Hello, ADA!
}
