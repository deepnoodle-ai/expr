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
		fmt.Fprintf(os.Stderr, "Flags may appear before or after the expression.\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nExamples:\n")
		fmt.Fprintf(os.Stderr, "  expr '1 + 2 * 3'\n")
		fmt.Fprintf(os.Stderr, "  expr -i '{\"user\":{\"age\":36}}' 'user.age >= 18'\n")
		fmt.Fprintf(os.Stderr, "  expr 'user.age >= 18' -i @user.json\n")
		fmt.Fprintf(os.Stderr, "  echo '{\"x\":41}' | expr -i - 'x + 1'\n")
	}

	// Reorder arguments so that flags may appear before or after the
	// positional expression. The stdlib `flag` package stops parsing at the
	// first non-flag argument, which makes `expr 'a + b' -i {...}` treat the
	// `-i` as extra positional input. Shuffling flags to the front keeps the
	// parser simple while matching what most users expect.
	os.Args = append([]string{os.Args[0]}, reorderArgs(os.Args[1:])...)
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

// reorderArgs splits args into flags and positionals, returning flags first
// so `flag.Parse` can consume them all before hitting the first positional.
// A flag whose value is the following arg (e.g. `-i foo`) carries its value
// along; attached forms (`-i=foo`, `--input=foo`) are handled naturally.
// Bool flags, and `--` / `-`, are treated as terminal tokens in the usual way.
func reorderArgs(args []string) []string {
	var flagArgs, posArgs []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		// `--` ends flag parsing; everything after is positional.
		if a == "--" {
			flagArgs = append(flagArgs, a)
			posArgs = append(posArgs, args[i+1:]...)
			break
		}
		// A bare `-` is a positional (convention: stdin).
		if a == "-" || !strings.HasPrefix(a, "-") {
			posArgs = append(posArgs, a)
			continue
		}
		flagArgs = append(flagArgs, a)

		// Attached value: `-i=foo` or `--input=foo`.
		name := strings.TrimLeft(a, "-")
		if strings.ContainsRune(name, '=') {
			continue
		}
		f := flag.Lookup(name)
		if f == nil {
			continue // unknown flag — let flag.Parse surface the error
		}
		// Bool flags don't consume a following value.
		if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
			continue
		}
		// Separated value: `-i foo`.
		if i+1 < len(args) {
			i++
			flagArgs = append(flagArgs, args[i])
		}
	}
	return append(flagArgs, posArgs...)
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
