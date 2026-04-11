package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/deepnoodle-ai/expr"
)

func main() {
	var (
		input  string
		format string
	)
	flag.StringVar(&input, "input", "", "JSON input: literal, @file, or - for stdin")
	flag.StringVar(&input, "i", "", "JSON input (shorthand)")
	flag.StringVar(&format, "format", "text", "output format: text or json")
	flag.StringVar(&format, "f", "text", "output format (shorthand)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: expr [flags] <expression>\n\n")
		fmt.Fprintf(os.Stderr, "Evaluate an expression against optional JSON input.\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}

	if err := run(flag.Arg(0), input, format); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run(source, input, format string) error {
	env, err := loadInput(input)
	if err != nil {
		return fmt.Errorf("load input: %w", err)
	}

	program, err := expr.Compile(source, expr.WithBuiltins())
	if err != nil {
		return err
	}
	result, err := program.Run(context.Background(), env)
	if err != nil {
		return err
	}

	switch format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	case "text":
		fmt.Println(formatValue(result))
		return nil
	default:
		return fmt.Errorf("unknown format %q (want text or json)", format)
	}
}

func loadInput(input string) (map[string]any, error) {
	if input == "" {
		return map[string]any{}, nil
	}

	var data []byte
	var err error
	switch {
	case input == "-":
		data, err = io.ReadAll(os.Stdin)
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
