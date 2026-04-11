# expr

A small, embeddable expression language for Go, built on `go/parser`. It
accepts a strict subset of Go's expression syntax and is intended for
conditions, templates, and parameter interpolation inside a larger Go
program.

## Install

```sh
go get github.com/deepnoodle-ai/expr
```

## Quickstart

`Compile` parses an expression once and returns a `*Program` that can
be run against many inputs. Programs are immutable and safe for
concurrent use. No functions are registered by default — pass
`WithBuiltins` to opt in to the standard set: `len`, `contains`, `has`,
`keys`, `upper`, `lower`, `int`, `float`, `string`, `bool`, `sprintf`.

```go
package main

import (
	"context"
	"fmt"

	"github.com/deepnoodle-ai/expr"
)

func main() {
	ctx := context.Background()

	p, err := expr.Compile(
		`user.age >= 18 && contains(user.roles, "admin")`,
		expr.WithBuiltins(),
	)
	if err != nil {
		panic(err)
	}

	env := map[string]any{
		"user": map[string]any{
			"name":  "ada",
			"age":   36,
			"roles": []any{"admin", "editor"},
		},
	}

	v, err := p.Run(ctx, env)
	if err != nil {
		panic(err)
	}
	fmt.Println(v) // true
}
```

Runnable copy: [`examples/basic`](examples/basic).

## Struct environments

The env passed to `Run` can also be a struct (or a pointer to one).
Exported fields and zero-argument methods are both reachable as
identifiers inside the expression.

```go
package main

import (
	"context"
	"fmt"

	"github.com/deepnoodle-ai/expr"
)

type Order struct {
	ID       string
	Customer string
	Items    []LineItem
}

type LineItem struct {
	SKU      string
	Quantity int
	Price    float64
}

func (o Order) Subtotal() float64 {
	var total float64
	for _, it := range o.Items {
		total += it.Price * float64(it.Quantity)
	}
	return total
}

func main() {
	ctx := context.Background()

	order := Order{
		ID:       "A-1042",
		Customer: "Ada Lovelace",
		Items: []LineItem{
			{SKU: "widget", Quantity: 2, Price: 19.99},
			{SKU: "gadget", Quantity: 1, Price: 89.52},
		},
	}

	p, err := expr.Compile(`Subtotal() > 100 && len(Items) >= 2`, expr.WithBuiltins())
	if err != nil {
		panic(err)
	}
	v, err := p.Run(ctx, order)
	if err != nil {
		panic(err)
	}
	fmt.Println(v) // true
}
```

Runnable copy: [`examples/structs`](examples/structs).

## Calling Go functions from expressions

`WithFunctions` registers arbitrary Go functions as callable identifiers.
Arguments are converted to the declared parameter types at call time, and
`(T, error)` return pairs surface errors naturally.

```go
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
```

Runnable copy: [`examples/funcs`](examples/funcs).

## Compile once, run many

Parsing is cheap but not free. When the same expression runs against many
inputs, compile once and reuse the `*Program`. Programs are immutable and
safe to evaluate from multiple goroutines.

```go
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
```

Runnable copy: [`examples/compile_once`](examples/compile_once).

## Templates

`NewTemplate` is a tiny `${...}` interpolator that compiles each
expression once at construction time and re-evaluates them per call.

```go
package main

import (
	"context"
	"fmt"

	"github.com/deepnoodle-ai/expr"
)

func main() {
	ctx := context.Background()

	tmpl, err := expr.NewTemplate(
		`Hello ${user.name}! You have ${len(user.tasks)} task(s).`,
		expr.WithBuiltins())
	if err != nil {
		panic(err)
	}

	out, err := tmpl.Render(ctx, map[string]any{
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
```

Runnable copy: [`examples/template`](examples/template).

## More

- [`docs/SPEC.md`](docs/SPEC.md) for the full language specification
- [`examples/`](examples/) for runnable examples, including
  [`examples/higher_order`](examples/higher_order) which covers
  `map`, `filter`, `any`, `all`, `find`, and `count`

## License

Apache 2.0. See [LICENSE](LICENSE).
