package expr

import (
	"context"
	"os"
	"os/exec"
	"runtime/debug"
	"strings"
	"testing"
	"time"
)

func TestRegressionFormattingCyclicMapKeys(t *testing.T) {
	const childEnv = "EXPR_FORMAT_CYCLE_REGRESSION"
	if mode := os.Getenv(childEnv); mode != "" {
		// Stack overflows cannot be recovered. Bound the child process's
		// stack so a regression fails promptly without crashing the suite.
		debug.SetMaxStack(1 << 20)
		type node struct{ M map[*node]int }
		m := map[*node]int{}
		m[&node{M: m}] = 1
		m[&node{M: m}] = 2
		env := map[string]any{"m": m}

		var output string
		if mode == "template" {
			tmpl, err := NewTemplate(`${m}`)
			if err != nil {
				t.Fatal(err)
			}
			output, err = tmpl.Render(t.Context(), env)
			if err != nil {
				t.Fatal(err)
			}
		} else {
			source := `string(m)`
			if mode == "sprintf" {
				source = `sprintf("%v", m)`
			}
			p, err := Compile(source, WithBuiltins())
			if err != nil {
				t.Fatal(err)
			}
			value, err := p.Run(t.Context(), env)
			if err != nil {
				t.Fatal(err)
			}
			var ok bool
			output, ok = value.(string)
			if !ok {
				t.Fatalf("formatting returned %T, want string", value)
			}
		}
		if !strings.Contains(output, "<cycle>") {
			t.Fatalf("formatting did not mark the cycle: %q", output)
		}
		return
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"string", "sprintf", "template"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestRegressionFormattingCyclicMapKeys$")
			cmd.Env = append(os.Environ(), childEnv+"="+mode)
			output, err := cmd.CombinedOutput()
			if err != nil {
				// A fatal runtime stack dump can be large; retain its cause
				// and first frames without flooding the test output.
				if len(output) > 2000 {
					output = append(output[:2000], []byte("\n[output truncated]")...)
				}
				t.Fatalf("formatting cyclic map keys must complete without crashing: %v\n%s", err, output)
			}
		})
	}
}
