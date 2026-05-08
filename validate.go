package expr

import (
	"fmt"
	"go/ast"
	"go/token"
)

// validate walks a parsed expression and rejects syntactic forms expr
// does not support, returning ErrCompile so callers see the failure
// at Compile time rather than when Run reaches the offending node.
//
// The runtime evaluator keeps equivalent rejections as defense in
// depth: if a future code path injects an AST node that bypasses
// Compile (e.g. a test or an unusual rewrite), Run still fails with
// ErrEvaluate. But every well-formed expression that reaches Run
// after a successful Compile is guaranteed to be evaluable.
func validate(fset *token.FileSet, node ast.Expr) error {
	switch n := node.(type) {
	case nil:
		return nil
	case *ast.BasicLit:
		if n.Kind == token.IMAG {
			return validateErr(fset, n.Pos(), "imaginary number literals are not supported")
		}
		return nil
	case *ast.Ident:
		return nil
	case *ast.ParenExpr:
		return validate(fset, n.X)
	case *ast.UnaryExpr:
		switch n.Op {
		case token.NOT, token.SUB, token.ADD:
			return validate(fset, n.X)
		case token.ARROW:
			return validateErr(fset, n.OpPos, "channel receive (<-) is not supported")
		case token.AND:
			return validateErr(fset, n.OpPos, "address-of (&) is not supported")
		case token.XOR:
			return validateErr(fset, n.OpPos, "bitwise complement (^) is not supported")
		}
		return validateErr(fset, n.OpPos, "unsupported unary operator %s", n.Op)
	case *ast.BinaryExpr:
		switch n.Op {
		case token.LAND, token.LOR,
			token.ADD, token.SUB, token.MUL, token.QUO, token.REM,
			token.EQL, token.NEQ, token.LSS, token.GTR, token.LEQ, token.GEQ:
			if err := validate(fset, n.X); err != nil {
				return err
			}
			return validate(fset, n.Y)
		case token.AND, token.OR, token.XOR, token.SHL, token.SHR, token.AND_NOT:
			return validateErr(fset, n.OpPos, "bitwise operator %s is not supported", n.Op)
		}
		return validateErr(fset, n.OpPos, "unsupported binary operator %s", n.Op)
	case *ast.SelectorExpr:
		return validate(fset, n.X)
	case *ast.IndexExpr:
		if err := validate(fset, n.X); err != nil {
			return err
		}
		return validate(fset, n.Index)
	case *ast.CallExpr:
		if n.Ellipsis != token.NoPos {
			return validateErr(fset, n.Ellipsis, "spread call arguments (...) are not supported")
		}
		if err := validate(fset, n.Fun); err != nil {
			return err
		}
		for _, a := range n.Args {
			if err := validate(fset, a); err != nil {
				return err
			}
		}
		return nil
	case *ast.CompositeLit:
		if err := validateCompositeType(fset, n); err != nil {
			return err
		}
		for _, e := range n.Elts {
			if kv, ok := e.(*ast.KeyValueExpr); ok {
				if err := validate(fset, kv.Key); err != nil {
					return err
				}
				if err := validate(fset, kv.Value); err != nil {
					return err
				}
				continue
			}
			if err := validate(fset, e); err != nil {
				return err
			}
		}
		return nil
	case *ast.FuncLit:
		return validateErr(fset, n.Pos(), "function literals are not supported")
	case *ast.SliceExpr:
		return validateErr(fset, n.Lbrack, "slice expressions (a[i:j]) are not supported")
	case *ast.TypeAssertExpr:
		return validateErr(fset, n.Lparen, "type assertions (x.(T)) are not supported")
	case *ast.StarExpr:
		return validateErr(fset, n.Star, "pointer dereference (*x) is not supported")
	case *ast.IndexListExpr:
		return validateErr(fset, n.Lbrack, "generic type instantiation is not supported")
	}
	return validateErr(fset, node.Pos(), "unsupported syntax %T", node)
}

// validateCompositeType mirrors the runtime check in evalCompositeLit
// so users see the same message whether the literal is rejected at
// Compile or at Run time. The two accepted shapes are []any{...} and
// map[string]any{...}; anything else (fixed-size arrays, typed slices
// with another element type, maps with non-string keys or non-any
// values, struct literals, generic types, etc.) is rejected.
func validateCompositeType(fset *token.FileSet, n *ast.CompositeLit) error {
	switch typ := n.Type.(type) {
	case nil:
		return validateErr(fset, n.Pos(), "untyped composite literals are not supported")
	case *ast.ArrayType:
		if typ.Len != nil {
			return validateErr(fset, typ.Pos(), "fixed-size array literals are not supported")
		}
		ident, ok := typ.Elt.(*ast.Ident)
		if !ok || ident.Name != "any" {
			return validateErr(fset, typ.Pos(), "only []any slice literals are supported")
		}
		return nil
	case *ast.MapType:
		kIdent, kOk := typ.Key.(*ast.Ident)
		vIdent, vOk := typ.Value.(*ast.Ident)
		if !kOk || !vOk || kIdent.Name != "string" || vIdent.Name != "any" {
			return validateErr(fset, typ.Pos(), "only map[string]any literals are supported")
		}
		return nil
	}
	return validateErr(fset, n.Type.Pos(), "unsupported composite literal type %T", n.Type)
}

func validateErr(fset *token.FileSet, pos token.Pos, format string, args ...any) error {
	prefix := ""
	if fset != nil && pos.IsValid() {
		prefix = fset.Position(pos).String() + ": "
	}
	return fmt.Errorf("%w: %s%s", ErrCompile, prefix, fmt.Sprintf(format, args...))
}
