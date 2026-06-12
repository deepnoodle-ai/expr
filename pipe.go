package expr

import (
	"go/ast"
	"go/token"
	"strings"
)

// This file implements the pipeline operator (RFC 0001,
// docs/rfcs/0001-pipe-operator.md).
//
// desugarPipes rewrites every pipe expression `a | f(x, y)` in the
// tree into the call `f(a, x, y)`. The rewrite runs once at Compile
// time, between parsing and validation, so the evaluator, the
// identifier collector, and every fast path see only ordinary call
// nodes — the pipe has no runtime representation at all. `|` was
// rejected at Compile time as an unsupported bitwise operator before
// the pipe existed, so repurposing the token changes the meaning of
// no previously-compilable expression. Special forms compose
// naturally because they receive the rewritten CallExpr like any
// other call: `xs | map(it*2)` is exactly `map(xs, it*2)`, including
// the lazy/per-element evaluation rules.
//
// The right-hand side must be a call expression syntactically; any
// other right side is an ErrCompile (see pipeNonCallErr). A pipe
// appearing un-parenthesized as the right operand of a comparison is
// also an ErrCompile: `a == b | f()` parses as `a == (b | f())`,
// which silently consumes the comparison's right operand, so expr
// demands parentheses for that one shape (RFC 0001 §3.3).
//
// The walk recurses only into the node types validate accepts. A pipe
// nested inside an unsupported construct (say, a slice expression) is
// left alone; validate rejects the enclosing construct first, which is
// the better error.
func desugarPipes(fset *token.FileSet, node ast.Expr) (ast.Expr, error) {
	switch n := node.(type) {
	case *ast.BinaryExpr:
		if n.Op == token.OR {
			return desugarPipe(fset, n)
		}
		if isComparisonOp(n.Op) {
			if pipe, ok := n.Y.(*ast.BinaryExpr); ok && pipe.Op == token.OR {
				return nil, validateErr(fset, pipe.OpPos,
					"ambiguous expression: | on the right of %s may parse differently than expected; use parentheses to clarify: write (a | f()) %s b or a %s f(b)",
					n.Op, n.Op, n.Op)
			}
		}
		x, err := desugarPipes(fset, n.X)
		if err != nil {
			return nil, err
		}
		y, err := desugarPipes(fset, n.Y)
		if err != nil {
			return nil, err
		}
		n.X, n.Y = x, y
		return n, nil
	case *ast.ParenExpr:
		x, err := desugarPipes(fset, n.X)
		if err != nil {
			return nil, err
		}
		n.X = x
		return n, nil
	case *ast.UnaryExpr:
		x, err := desugarPipes(fset, n.X)
		if err != nil {
			return nil, err
		}
		n.X = x
		return n, nil
	case *ast.SelectorExpr:
		x, err := desugarPipes(fset, n.X)
		if err != nil {
			return nil, err
		}
		n.X = x
		return n, nil
	case *ast.IndexExpr:
		x, err := desugarPipes(fset, n.X)
		if err != nil {
			return nil, err
		}
		idx, err := desugarPipes(fset, n.Index)
		if err != nil {
			return nil, err
		}
		n.X, n.Index = x, idx
		return n, nil
	case *ast.CallExpr:
		fun, err := desugarPipes(fset, n.Fun)
		if err != nil {
			return nil, err
		}
		n.Fun = fun
		for i, a := range n.Args {
			da, err := desugarPipes(fset, a)
			if err != nil {
				return nil, err
			}
			n.Args[i] = da
		}
		return n, nil
	case *ast.CompositeLit:
		for i, e := range n.Elts {
			if kv, ok := e.(*ast.KeyValueExpr); ok {
				k, err := desugarPipes(fset, kv.Key)
				if err != nil {
					return nil, err
				}
				v, err := desugarPipes(fset, kv.Value)
				if err != nil {
					return nil, err
				}
				kv.Key, kv.Value = k, v
				continue
			}
			de, err := desugarPipes(fset, e)
			if err != nil {
				return nil, err
			}
			n.Elts[i] = de
		}
		return n, nil
	}
	return node, nil
}

func isComparisonOp(op token.Token) bool {
	switch op {
	case token.EQL, token.NEQ, token.LSS, token.LEQ, token.GTR, token.GEQ:
		return true
	}
	return false
}

// desugarPipe rewrites a single `lhs | call(args...)` node. The left
// side desugars first (pipe chains are left-associative, so
// `a | f() | g()` arrives as `(a | f()) | g()` and the recursion
// bottoms out at the leftmost stage), then splices in as the call's
// first argument. The call node is reused so its position info — used
// by validate for any later error — stays intact.
func desugarPipe(fset *token.FileSet, n *ast.BinaryExpr) (ast.Expr, error) {
	lhs, err := desugarPipes(fset, n.X)
	if err != nil {
		return nil, err
	}
	call, ok := n.Y.(*ast.CallExpr)
	if !ok || isOptAccessCall(call) {
		// The optaccess pre-parse rewrite turns `a?.b` / `a?[i]` into
		// sentinel calls, so without the extra check `xs | a?.b` would
		// silently desugar into a three-argument sentinel call. The
		// user wrote an optional access, not a call; reject it like
		// any other non-call right side.
		return nil, pipeNonCallErr(fset, n.OpPos, n.Y)
	}
	fun, err := desugarPipes(fset, call.Fun)
	if err != nil {
		return nil, err
	}
	args := make([]ast.Expr, 0, len(call.Args)+1)
	args = append(args, lhs)
	for _, a := range call.Args {
		da, err := desugarPipes(fset, a)
		if err != nil {
			return nil, err
		}
		args = append(args, da)
	}
	call.Fun = fun
	call.Args = args
	return call, nil
}

func isOptAccessCall(call *ast.CallExpr) bool {
	id, ok := call.Fun.(*ast.Ident)
	return ok && (id.Name == trySelectFormName || id.Name == tryIndexFormName)
}

// pipeNonCallErr builds the rejection for a non-call right-hand side
// of `|`, following the error taxonomy in RFC 0001 §6. A bare
// identifier earns a "did you mean to write name(...)?" nudge, and an
// identifier naming a special form shows the form's signature with
// the collection argument dropped, since the pipe supplies it.
func pipeNonCallErr(fset *token.FileSet, opPos token.Pos, rhs ast.Expr) error {
	const base = "pipe operator | requires a function call on the right-hand side"
	if id, ok := rhs.(*ast.Ident); ok {
		name := displayIdent(id.Name)
		for _, f := range userForms {
			if f.name == name {
				return validateErr(fset, opPos,
					"%s; %q is a special form, did you mean to write %s?",
					base, name, pipeCallHint(f.callHint))
			}
		}
		return validateErr(fset, opPos,
			"%s; %q is not a call (did you mean to write %s(...)?)", base, name, name)
	}
	if src := exprDisplayString(rhs); src != "" {
		return validateErr(fset, opPos, "%s; %q is not a call", base, src)
	}
	return validateErr(fset, opPos, "%s", base)
}

// pipeCallHint rewrites a form's call signature for pipe position by
// dropping the first parameter, which the pipe supplies:
// `filter(xs, predicate)` becomes `filter(predicate)`.
func pipeCallHint(hint string) string {
	open := strings.IndexByte(hint, '(')
	comma := strings.IndexByte(hint, ',')
	if open < 0 || comma < open {
		return hint
	}
	return hint[:open+1] + strings.TrimSpace(hint[comma+1:])
}
