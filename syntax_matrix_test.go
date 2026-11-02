package expr

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// The syntax matrix checks that expr's source rewrites (keyword fields,
// optional access, JSON literals, the map/if keywords, pipes and template
// delimiters) compose. Every receiver is crossed with every access
// operator, every surrounding context and every comment placement, and
// each expression must behave exactly like its explicitly parenthesized
// equivalent: the same value, or the same error category, or a compile
// error in both forms.

// matrixReceiver is a primary expression. obj receivers hold the object
// {"a": {"b": 2}, "type": 3, "map": 4, "n": nil}; list receivers hold
// [1, 2, 3] (or the string "abc").
type matrixReceiver struct {
	src  string
	list bool
}

var matrixReceivers = []matrixReceiver{
	{src: `x`},
	{src: `x.self`},
	{src: `x["self"]`},
	{src: `{"a": {"b": 2}, "type": 3, "map": 4, "n": nil}`},
	{src: `map[string]any{"a": {"b": 2}, "type": 3, "map": 4}`},
	{src: `id(x)`},
	{src: `if(true, x, nil)`},
	{src: `map([x], it)[0]`},
	{src: `[x][0]`},
	{src: `x?.self`},
	{src: `xs`, list: true},
	{src: `[1, 2, 3]`, list: true},
	{src: `[]any{1, 2, 3}`, list: true},
	{src: `[1, 2, 3][0:0]`, list: true}, // rejected slice syntax
	{src: `map(xs, it)`, list: true},
	{src: `if(true, xs, nil)`, list: true},
	{src: `id([1, 2, 3])`, list: true},
	{src: `"abc"`, list: true},
	{src: `{"self": x}.self`},
	{src: `{"self": x}?.self`},
	{src: `[{"a": {"b": 2}, "type": 3}][0]`},
	{src: `[]any{x}[0]`},
	{src: `[]any{x}?[0]`},
	{src: `[x]?[0]`},
	{src: `[1]`, list: true},
	{src: `[6]`, list: true},
	{src: `[xs][0]`, list: true},
	{src: `[[1, 2, 3]][0]`, list: true},
	{src: `[[1, 2, 3]]?[0]`, list: true},
	{src: `[]any{[]any{1}}[0]`, list: true},
	{src: `[2]any{1, 2}`, list: true},
	{src: `{"k": [1, 2]}.k`, list: true},
	{src: `{"k": [1, 2]}?.k`, list: true},
	{src: `{"k": [1, 2]}["k"]`, list: true},
	{src: `n`},
	{src: `n?.self`},
}

var matrixObjectOps = []string{
	`.a.b`, `?.a.b`, `?.a?.b`, `.a?.b`, `["a"]["b"]`, `?["a"]?["b"]`,
	`["a"]?.b`, `?["a"].b`, `.type`, `?.type`, `.map`, `?.map`, `.if`,
	`?.missing`, `?.n?.b`, `.self.type`, `?.self?.map`,
}

var matrixListOps = []string{
	`[0]`, `?[0]`, `[1 + 1]`, `?[5]`, `[len("ab")]`, `?[0]?.b`, `[-1]`,
}

// matrixContexts surround a primary; %s is the primary.
var matrixContexts = []string{
	`%s`,
	`%s * 2`,
	`2 * %s`,
	`%s - 1`,
	`1 - %s`,
	`-%s`,
	`!%s`,
	`%s == %s`,
	`%s * %s`,
	`%s * -%s`,
	`[%s, %s]`,
	`{"k": %s, "j": [%s]}`,
	`id(%s) * 2`,
	`%s | id() == 2`,
	`%s | id()`,
	`id(%s)`,
	`id(1, %s)`,
	`[%s]`,
	`[1, %s]`,
	`{"k": %s}`,
	`map([1], %s)`,
	`filter([1], %s != nil)`,
	`if(%s == nil, 0, 1)`,
	`[%s][0]`,
	`{"k": %s}.k`,
	`{"k": %s}?.k`,
}

// matrixCommentContexts are the contexts crossed with every comment
// placement.
var matrixCommentContexts = []string{`%s`, `%s * 2`, `-%s`, `%s | id()`, `[1, %s]`, `{"k": %s}?.k`}

// matrixComments are placements for comments inside a primary: before
// the access operator, after it (where no semicolon is inserted), and
// around the whole primary.
var matrixComments = []func(recv, op string) string{
	func(recv, op string) string { return recv + op },
	func(recv, op string) string { return recv + " /* c */ " + op },
	func(recv, op string) string { return "/* c */ " + recv + op + " /* c */" },
	func(recv, op string) string { return recv + insertAfterOperator(op, " /* c */ ") },
	func(recv, op string) string { return recv + insertAfterOperator(op, " // c\n ") },
}

// insertAfterOperator places c after the operator that starts op (`.`,
// `?.`, `[`, `?[`), where Go inserts no semicolon at a newline.
func insertAfterOperator(op, c string) string {
	for _, p := range []string{"?.", "?[", ".", "["} {
		if strings.HasPrefix(op, p) {
			return p + c + op[len(p):]
		}
	}
	return op
}

func matrixEnv() map[string]any {
	x := map[string]any{"a": map[string]any{"b": int64(2)}, "type": int64(3), "map": int64(4), "if": int64(5), "n": nil}
	x["self"] = map[string]any{"a": map[string]any{"b": int64(2)}, "type": int64(3), "map": int64(4)}
	return map[string]any{
		"x":  x,
		"xs": []any{int64(1), int64(2), int64(3)},
		"n":  nil,
	}
}

var matrixOpts = []Option{WithFunctions(map[string]any{
	"id": func(args ...any) any { return args[len(args)-1] },
	"len": func(s string) int {
		return len(s)
	},
})}

type matrixOutcome struct {
	compileErr bool
	val        any
	errKind    string
}

func (o matrixOutcome) String() string {
	switch {
	case o.compileErr:
		return "compile error"
	case o.errKind != "":
		return o.errKind
	}
	return fmt.Sprintf("%#v", o.val)
}

func runMatrix(src string, env any) (matrixOutcome, error) {
	p, err := Compile(src, matrixOpts...)
	if err != nil {
		if !errors.Is(err, ErrCompile) {
			return matrixOutcome{errKind: "unwrapped compile error"}, err
		}
		return matrixOutcome{compileErr: true}, err
	}
	v, err := p.Run(context.Background(), env)
	if err != nil {
		if errors.Is(err, ErrEvaluate) {
			return matrixOutcome{errKind: "evaluate error"}, err
		}
		return matrixOutcome{errKind: "other error"}, err
	}
	return matrixOutcome{val: v}, nil
}

func sameSyntaxOutcome(a, b matrixOutcome) bool {
	if a.compileErr || b.compileErr {
		return a.compileErr == b.compileErr
	}
	if a.errKind != b.errKind {
		return false
	}
	return reflect.DeepEqual(a.val, b.val)
}

// matrixCases yields each expression with its parenthesized forms.
func matrixCases(yield func(name, src string, alts []string) bool) {
	for _, r := range matrixReceivers {
		ops := matrixObjectOps
		if r.list {
			ops = matrixListOps
		}
		for _, op := range ops {
			for ci, comment := range matrixComments {
				primary := comment(r.src, op)
				parenRecv := comment("("+r.src+")", op)
				contexts := matrixContexts
				if ci > 0 {
					// Comments are a lexical concern: a few contexts cover them.
					contexts = matrixCommentContexts
				}
				for _, c := range contexts {
					fill := func(p string) string { return strings.ReplaceAll(c, "%s", p) }
					name := fmt.Sprintf("%s|%s|c%d|%s", r.src, op, ci, c)
					if !yield(name, fill(primary), []string{fill(parenRecv), fill("(" + primary + ")")}) {
						return
					}
				}
			}
		}
	}
}

func TestSyntaxMatrix(t *testing.T) {
	env := matrixEnv()
	var failures, total int
	compiled := map[bool]int{}
	matrixCases(func(name, src string, alts []string) bool {
		total++
		want, werr := runMatrix(src, env)
		compiled[!want.compileErr]++
		for _, alt := range alts {
			got, gerr := runMatrix(alt, env)
			if !sameSyntaxOutcome(want, got) {
				failures++
				if failures <= 40 {
					t.Errorf("%s\n  %q -> %v (%v)\n  %q -> %v (%v)", name, src, want, werr, alt, got, gerr)
				}
			}
		}
		return true
	})
	if failures > 0 {
		t.Errorf("%d of %d matrix cells disagree with their parenthesized form", failures, total)
	}
	// Guard against the matrix degenerating into compile errors.
	if compiled[true] < total/2 {
		t.Errorf("only %d of %d matrix expressions compile", compiled[true], total)
	}
	t.Logf("%d cells, %d compile", total, compiled[true])
}

// Templates must render each expression the same way whatever the
// delimiters, and the same as the parenthesized expression.
func TestSyntaxMatrixTemplates(t *testing.T) {
	env := matrixEnv()
	delims := [][2]string{{"${", "}"}, {"{{", "}}"}, {"${{", "}}"}, {"@{{{", "}}}"}}
	var failures int
	contexts := matrixContexts[:1]
	for _, r := range matrixReceivers {
		ops := matrixObjectOps
		if r.list {
			ops = matrixListOps
		}
		for _, op := range ops {
			for ci, comment := range matrixComments {
				for _, c := range contexts {
					e := strings.ReplaceAll(c, "%s", comment(r.src, op))
					type out struct {
						s   string
						err bool
					}
					var outs []out
					var srcs []string
					for _, d := range delims {
						for _, body := range []string{e, "(" + e + ")", " " + e + " "} {
							raw := "<" + d[0] + body + d[1] + ">"
							var opts []Option
							opts = append(opts, matrixOpts...)
							if d[0] != "${" {
								opts = append(opts, WithTemplateDelimiters(d[0], d[1]))
							}
							tmpl, err := NewTemplate(raw, opts...)
							if err != nil {
								outs = append(outs, out{err: true})
								srcs = append(srcs, raw+" (compile: "+err.Error()+")")
								continue
							}
							s, err := tmpl.Render(context.Background(), env)
							outs = append(outs, out{s: s, err: err != nil})
							srcs = append(srcs, raw)
						}
					}
					for i := 1; i < len(outs); i++ {
						if outs[i] != outs[0] {
							failures++
							if failures <= 20 {
								t.Errorf("c%d %q: %q -> %+v, but %q -> %+v", ci, e, srcs[0], outs[0], srcs[i], outs[i])
							}
							break
						}
					}
				}
			}
		}
	}
	if failures > 0 {
		t.Errorf("%d template cells disagree", failures)
	}
}

// Compile errors must point at the source the user wrote, however many
// bytes the rewrites inserted before the error. Each matrix primary is
// followed by an operator expr rejects at validation (`^`) and a byte the
// scanner rejects (`$`); the error must name that token's line:column.
func TestSyntaxMatrixErrorPositions(t *testing.T) {
	var failures int
	for _, r := range matrixReceivers {
		if _, err := Compile(r.src, matrixOpts...); err != nil {
			continue // the receiver reports its own error first
		}
		ops := matrixObjectOps
		if r.list {
			ops = matrixListOps
		}
		for _, op := range ops {
			for ci, comment := range matrixComments {
				primary := comment(r.src, op)
				for _, bad := range []string{" ^ 1", " $"} {
					for _, src := range []string{primary + bad, "[" + primary + bad + "]", "id(" + primary + "," + bad + ")"} {
						at := strings.LastIndexAny(src, "^$")
						line := 1 + strings.Count(src[:at], "\n")
						col := at - strings.LastIndex(src[:at], "\n")
						want := fmt.Sprintf("%d:%d: ", line, col)
						_, err := Compile(src, matrixOpts...)
						if err == nil || !strings.Contains(err.Error(), want) {
							failures++
							if failures <= 20 {
								t.Errorf("c%d %q: error %v, want position %s", ci, src, err, want)
							}
						}
					}
				}
			}
		}
	}
	if failures > 0 {
		t.Errorf("%d misplaced error positions", failures)
	}
}
