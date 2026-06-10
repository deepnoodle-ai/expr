package expr

import (
	"go/ast"
	"sort"
)

// Identifiers returns the sorted, de-duplicated set of top-level
// identifier names the expression references — the names Run will
// try to resolve through the environment. Hosts can use it to
// validate an expression against a known env shape at load time, to
// track dependencies for cache invalidation, or to decide which
// values are worth computing before a Run.
//
// Excluded from the result:
//
//   - the literals true, false, and nil
//   - `it` and `index` where they are bound by an iterating
//     higher-order form (map, filter, any, all, find, count)
//   - names registered via WithFunctions / WithBuiltins, which
//     resolve without the env
//   - special-form names (map, filter, try, if, ...) in call
//     position, which are always available
//
// The analysis is static and therefore best-effort in one corner:
// env entries can shadow registered functions and special forms at
// Run time, so an excluded name may still be read from the env when
// a host deliberately shadows it. Every name that can only resolve
// through the env is always included.
func (p *Program) Identifiers() []string {
	out := make([]string, len(p.identifiers))
	copy(out, p.identifiers)
	return out
}

// collectIdentifiers walks the compiled AST once and gathers the
// env-resolved identifier set per the Identifiers contract.
func collectIdentifiers(root ast.Expr, funcs map[string]any) []string {
	seen := map[string]struct{}{}
	walkIdentifiers(root, false, funcs, seen)
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// walkIdentifiers visits node and records env-resolved identifier
// names into seen. itBound reports whether the walk is inside the
// predicate of an iterating form, where `it` and `index` are bound by
// the form rather than the env.
func walkIdentifiers(node ast.Expr, itBound bool, funcs map[string]any, seen map[string]struct{}) {
	switch n := node.(type) {
	case *ast.Ident:
		name := displayIdent(n.Name)
		switch name {
		case "true", "false", "nil":
			return
		}
		if itBound && (name == "it" || name == "index") {
			return
		}
		if _, registered := funcs[name]; registered {
			return
		}
		seen[name] = struct{}{}
	case *ast.ParenExpr:
		walkIdentifiers(n.X, itBound, funcs, seen)
	case *ast.UnaryExpr:
		walkIdentifiers(n.X, itBound, funcs, seen)
	case *ast.BinaryExpr:
		walkIdentifiers(n.X, itBound, funcs, seen)
		walkIdentifiers(n.Y, itBound, funcs, seen)
	case *ast.SelectorExpr:
		// n.Sel is a field/key/method name on the receiver, not an
		// env identifier.
		walkIdentifiers(n.X, itBound, funcs, seen)
	case *ast.IndexExpr:
		walkIdentifiers(n.X, itBound, funcs, seen)
		walkIdentifiers(n.Index, itBound, funcs, seen)
	case *ast.CallExpr:
		if ident, ok := n.Fun.(*ast.Ident); ok {
			if _, isForm := higherOrderForms[ident.Name]; isForm {
				if _, shadowed := funcs[displayIdent(ident.Name)]; !shadowed {
					// Special-form call: the form name resolves without
					// the env, and iterating forms bind it/index inside
					// their predicate argument.
					if itBindingForms[ident.Name] && len(n.Args) == 2 {
						walkIdentifiers(n.Args[0], itBound, funcs, seen)
						walkIdentifiers(n.Args[1], true, funcs, seen)
						return
					}
					for _, a := range n.Args {
						walkIdentifiers(a, itBound, funcs, seen)
					}
					return
				}
			}
		}
		walkIdentifiers(n.Fun, itBound, funcs, seen)
		for _, a := range n.Args {
			walkIdentifiers(a, itBound, funcs, seen)
		}
	case *ast.CompositeLit:
		for _, e := range n.Elts {
			if kv, ok := e.(*ast.KeyValueExpr); ok {
				walkIdentifiers(kv.Key, itBound, funcs, seen)
				walkIdentifiers(kv.Value, itBound, funcs, seen)
				continue
			}
			walkIdentifiers(e, itBound, funcs, seen)
		}
	}
}
