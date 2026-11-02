package expr

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"runtime/debug"
	"sort"
	"strings"
	"testing"
	"time"
)

// Crash regressions run in child processes. A stack overflow or an
// out-of-memory abort cannot be recovered, so a test that provokes one
// in-process takes the whole suite down with it and hides every other
// result. Each case below runs in its own child, with a small stack
// limit so a regression fails fast, and a timeout so a hang fails too.

const isolatedEnvVar = "EXPR_ISOLATED_CASE"

// isolatedCase returns nil when the guarantee holds.
type isolatedCase func() error

func isolatedCases() map[string]isolatedCase {
	cases := map[string]isolatedCase{}
	// Formatting: every formatter on every awkward value shape.
	for name, build := range map[string]func() (any, string){
		"cyclic-map-keys": func() (any, string) {
			type node struct{ M map[*node]int }
			m := map[*node]int{}
			m[&node{M: m}] = 1
			m[&node{M: m}] = 2
			return m, "<cycle>"
		},
		"cyclic-slice": func() (any, string) {
			s := []any{1, nil}
			s[1] = s
			return s, "<cycle>"
		},
		"cyclic-map": func() (any, string) {
			m := map[string]any{"a": 1}
			m["self"] = m
			return m, "<cycle>"
		},
		"pointer-cycle": func() (any, string) {
			type node struct {
				V    int
				Next *node
			}
			n := &node{V: 1}
			n.Next = &node{V: 2, Next: n}
			return n, "<cycle>"
		},
		"self-pointer": func() (any, string) {
			var p mxSelf
			p = mxSelf(&p)
			return p, "<cycle>"
		},
		"deep-list": func() (any, string) {
			var v any = "leaf"
			for i := 0; i < 200_000; i++ {
				v = []any{v}
			}
			return v, "<...>"
		},
		"deep-interface-struct": func() (any, string) {
			var v any = "leaf"
			for i := 0; i < 200_000; i++ {
				v = mxHolder{V: v}
			}
			return v, "<...>"
		},
		"shared-dag": func() (any, string) {
			// Not a cycle: each level holds the level below twice, so a
			// full rendering has 2^64 leaves.
			var v any = []any{1}
			for i := 0; i < 64; i++ {
				v = []any{v, v}
			}
			return v, "<truncated>"
		},
		"shared-dag-map": func() (any, string) {
			var v any = map[string]any{"x": 1}
			for i := 0; i < 64; i++ {
				v = map[string]any{"a": v, "b": v}
			}
			return v, "<truncated>"
		},
	} {
		for _, how := range []string{"string", "sprintf", "template"} {
			cases["format/"+name+"/"+how] = func() error {
				v, marker := build()
				out, err := formatWith(how, v)
				if err != nil {
					return err
				}
				if !strings.Contains(out, marker) {
					return fmt.Errorf("output does not mark the bound with %s: %.200q", marker, out)
				}
				if len(out) > 64<<20 {
					return fmt.Errorf("output is %d bytes", len(out))
				}
				return nil
			}
		}
	}

	// Evaluation over cyclic and deep values must terminate with a value
	// or an ErrEvaluate.
	cyclicSlice := func() any { s := []any{1, nil}; s[1] = s; return s }
	cyclicMap := func() any { m := map[string]any{"a": 1}; m["self"] = m; return m }
	selfPtr := func() any { var p mxSelf; p = mxSelf(&p); return p }
	deepPtr := func() any {
		// A 10,000-level pointer chain to a struct.
		v := reflect.ValueOf(mxInner{B: 1})
		for i := 0; i < 10_000; i++ {
			p := reflect.New(v.Type())
			p.Elem().Set(v)
			v = p
		}
		return v.Interface()
	}
	for name, tc := range map[string]struct {
		v   func() any
		src []string
	}{
		"cyclic-slice": {cyclicSlice, []string{"V == V", "contains(V, V)", "len(V)", "map(V, it)[1][1][1][0]", "sort(V)", "V[1][1][1][1][0]", "flatMap(V, it)", "reverse(V)", "V | string() | len() > 0"}},
		"cyclic-map":   {cyclicMap, []string{"V == V", "keys(V)", "entries(V)", "V.self.self.self.a", "V?.self?.self?.a", "has(V, \"self\")", "contains(V, \"self\")"}},
		"self-pointer": {selfPtr, []string{"V.x", "V?.x", "V[0]", "V?[0]", "V == nil", "!V", "V == V"}},
		"deep-pointer": {deepPtr, []string{"V.B", "V?.B", "V == nil", "string(V) != \"\""}},
	} {
		for _, src := range tc.src {
			cases["eval/"+name+"/"+src] = func() error {
				p, err := Compile(src, WithBuiltins(), WithFunctions(CollectionFuncs()))
				if err != nil {
					return err
				}
				_, err = p.Run(context.Background(), map[string]any{"V": tc.v()})
				if err != nil && !errors.Is(err, ErrEvaluate) {
					return fmt.Errorf("error does not wrap ErrEvaluate: %v", err)
				}
				return nil
			}
		}
	}

	// Compile, Check and Run each bound their own recursion on maximal
	// sources: MaxSourceLength bounds the parser and the AST walkers,
	// MaxEvalDepth bounds Run.
	half := MaxSourceLength/2 - 2
	for name, src := range map[string]string{
		"parens":        strings.Repeat("(", half) + "1" + strings.Repeat(")", half),
		"unary":         strings.Repeat("!", MaxSourceLength-1) + "x",
		"index":         "x" + strings.Repeat("[0]", (MaxSourceLength-1)/3),
		"selector":      "x" + strings.Repeat(".a", (MaxSourceLength-1)/2),
		"optional":      "x" + strings.Repeat("?.a", (MaxSourceLength-1)/3),
		"json-list":     strings.Repeat("[", half) + strings.Repeat("]", half),
		"json-object":   strings.Repeat(`{"a":`, (MaxSourceLength-2)/6) + "1" + strings.Repeat("}", (MaxSourceLength-2)/6),
		"calls":         strings.Repeat("f(", (MaxSourceLength-1)/3) + "x" + strings.Repeat(")", (MaxSourceLength-1)/3),
		"pipes":         "x" + strings.Repeat(" | f()", (MaxSourceLength-1)/6),
		"binary":        "1" + strings.Repeat(" + 1", (MaxSourceLength-1)/4),
		"binary-nested": strings.Repeat("1 + (", (MaxSourceLength-1)/6) + "1" + strings.Repeat(")", (MaxSourceLength-1)/6),
		"if":            strings.Repeat("if(c, ", (MaxSourceLength-1)/10) + "1" + strings.Repeat(", 2)", (MaxSourceLength-1)/10),
		"map-form":      strings.Repeat("map(x, ", (MaxSourceLength-1)/10) + "it" + strings.Repeat(")", (MaxSourceLength-1)/10),
	} {
		cases["compile/"+name] = func() error {
			if len(src) > MaxSourceLength {
				return fmt.Errorf("case source is %d bytes, over MaxSourceLength", len(src))
			}
			p, err := Compile(src, WithFunctions(map[string]any{"f": func(v any) any { return v }}))
			if err != nil {
				if !errors.Is(err, ErrCompile) {
					return fmt.Errorf("compile error does not wrap ErrCompile: %v", err)
				}
				return nil
			}
			if _, err := p.Check(map[string]*Type{"x": Any, "c": Bool()}); err != nil && !errors.Is(err, ErrType) {
				return fmt.Errorf("check error does not wrap ErrType: %v", err)
			}
			_ = p.Identifiers()
			_ = p.Calls("f")
			_, err = p.Run(context.Background(), map[string]any{"x": []any{1}, "c": true})
			if err != nil && !errors.Is(err, ErrEvaluate) {
				return fmt.Errorf("run error does not wrap ErrEvaluate: %v", err)
			}
			return nil
		}
	}
	cases["compile/template-segments"] = func() error {
		src := strings.Repeat("${x}", MaxSourceLength/4)
		tmpl, err := NewTemplate(src)
		if err != nil {
			return err
		}
		_, err = tmpl.Render(context.Background(), map[string]any{"x": 1})
		return err
	}
	return cases
}

// formatWith renders v through one of the three formatters.
func formatWith(how string, v any) (string, error) {
	env := map[string]any{"V": v}
	if how == "template" {
		tmpl, err := NewTemplate(`${V}`)
		if err != nil {
			return "", err
		}
		return tmpl.Render(context.Background(), env)
	}
	src := `string(V)`
	if how == "sprintf" {
		src = `sprintf("%v", V)`
	}
	p, err := Compile(src, WithBuiltins())
	if err != nil {
		return "", err
	}
	out, err := p.Run(context.Background(), env)
	if err != nil {
		return "", err
	}
	s, ok := out.(string)
	if !ok {
		return "", fmt.Errorf("formatting returned %T", out)
	}
	return s, nil
}

func TestIsolatedCrashRegressions(t *testing.T) {
	cases := isolatedCases()
	if name := os.Getenv(isolatedEnvVar); name != "" {
		// Child: run one case. 64 MiB of stack is ample for every bound
		// expr promises and small enough that a regression into unbounded
		// recursion dies in well under a second.
		debug.SetMaxStack(64 << 20)
		c, ok := cases[name]
		if !ok {
			t.Fatalf("unknown isolated case %q", name)
		}
		if err := c(); err != nil {
			t.Fatal(err)
		}
		return
	}
	if testing.Short() {
		t.Skip("spawns a child process per case")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(cases))
	for name := range cases {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestIsolatedCrashRegressions$", "-test.count=1")
			cmd.Env = append(os.Environ(), isolatedEnvVar+"="+name)
			start := time.Now()
			out, err := cmd.CombinedOutput()
			if err != nil {
				// A fatal stack dump is large; keep its cause and first frames.
				if len(out) > 2000 {
					out = append(out[:2000], "\n[output truncated]"...)
				}
				t.Fatalf("child failed after %v: %v\n%s", time.Since(start).Round(time.Millisecond), err, out)
			}
		})
	}
}
