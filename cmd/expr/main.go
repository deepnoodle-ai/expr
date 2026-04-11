package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/deepnoodle-ai/expr"
	"github.com/deepnoodle-ai/wonton/cli"
)

func main() {
	app := cli.New("expr").
		Description("Utilities for the expr expression language").
		Version("0.1.0")

	app.Main().
		Description("Evaluate an expression against optional JSON input").
		Args("expression").
		Flags(
			cli.String("input", "i").Help("JSON input: literal, @file, or - for stdin"),
			cli.String("format", "f").Default("text").Enum("text", "json").Help("Output format"),
		).
		Run(runEval)

	app.Command("parse").
		Description("Parse an expression and print its AST").
		Args("expression").
		Run(runParse)

	app.Command("builtins").
		Description("List registered builtin functions").
		Run(runBuiltins)

	if err := app.Execute(); err != nil {
		if cli.IsHelpRequested(err) {
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(cli.GetExitCode(err))
	}
}

func runEval(ctx *cli.Context) error {
	source := ctx.Arg(0)

	env, err := loadInput(ctx, ctx.String("input"))
	if err != nil {
		return cli.Errorf("load input: %v", err)
	}

	program, err := expr.Compile(source, expr.WithBuiltins())
	if err != nil {
		return cli.Errorf("%v", err)
	}
	result, err := program.Run(ctx.Context(), env)
	if err != nil {
		return cli.Errorf("%v", err)
	}

	switch ctx.String("format") {
	case "json":
		enc := json.NewEncoder(ctx.Stdout())
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	default:
		fmt.Fprintln(ctx.Stdout(), formatValue(result))
		return nil
	}
}

func runParse(ctx *cli.Context) error {
	source := ctx.Arg(0)
	// Parse the user's literal source with go/parser directly so the
	// printed AST reflects what they typed, rather than expr's internal
	// preprocessing (map-keyword rewrite, jsonlit).
	node, err := parser.ParseExpr(source)
	if err != nil {
		return cli.Errorf("%v", err)
	}
	return ast.Fprint(ctx.Stdout(), token.NewFileSet(), node, ast.NotNilFilter)
}

func runBuiltins(ctx *cli.Context) error {
	names := make([]string, 0, len(expr.Builtins()))
	for name := range expr.Builtins() {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintln(ctx.Stdout(), name)
	}
	return nil
}

func loadInput(ctx *cli.Context, input string) (map[string]any, error) {
	if input == "" {
		return map[string]any{}, nil
	}

	var data []byte
	var err error
	switch {
	case input == "-":
		data, err = io.ReadAll(ctx.Stdin())
	case strings.HasPrefix(input, "@"):
		data, err = os.ReadFile(input[1:])
	default:
		data = []byte(input)
	}
	if err != nil {
		return nil, err
	}

	var env map[string]any
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}
	return env, nil
}

func formatValue(v any) string {
	switch x := v.(type) {
	case nil:
		return "nil"
	case string:
		return x
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(b)
	}
}
