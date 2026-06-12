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
//     higher-order form (map, filter, flatMap, any, all, find,
//     count, sortBy)
//   - the named element binding of a three-arg form (the `o` in
//     `filter(orders, o, o.paid)`), both the binding identifier
//     itself and references to it inside the body
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
	walkIdentifiers(root, nil, funcs, seen)
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// boundIdents is an immutable stack of element bindings introduced by
// enclosing iterating forms. Each frame binds one element name (`it`
// for the two-arg form, the user-chosen name for the three-arg form);
// `index` is bound by every frame.
type boundIdents struct {
	element string
	parent  *boundIdents
}

func (b *boundIdents) has(name string) bool {
	if b != nil && name == "index" {
		return true
	}
	for s := b; s != nil; s = s.parent {
		if name == s.element {
			return true
		}
	}
	return false
}

// walkIdentifiers visits node and records env-resolved identifier
// names into seen. bound carries the element bindings of the
// enclosing iterating forms, where names resolve to the form rather
// than the env.
func walkIdentifiers(node ast.Expr, bound *boundIdents, funcs map[string]any, seen map[string]struct{}) {
	switch n := node.(type) {
	case *ast.Ident:
		name := displayIdent(n.Name)
		switch name {
		case "true", "false", "nil":
			return
		}
		if bound.has(name) {
			return
		}
		if _, registered := funcs[name]; registered {
			return
		}
		seen[name] = struct{}{}
	case *ast.ParenExpr:
		walkIdentifiers(n.X, bound, funcs, seen)
	case *ast.UnaryExpr:
		walkIdentifiers(n.X, bound, funcs, seen)
	case *ast.BinaryExpr:
		walkIdentifiers(n.X, bound, funcs, seen)
		walkIdentifiers(n.Y, bound, funcs, seen)
	case *ast.SelectorExpr:
		// n.Sel is a field/key/method name on the receiver, not an
		// env identifier.
		walkIdentifiers(n.X, bound, funcs, seen)
	case *ast.IndexExpr:
		walkIdentifiers(n.X, bound, funcs, seen)
		walkIdentifiers(n.Index, bound, funcs, seen)
	case *ast.CallExpr:
		if ident, ok := n.Fun.(*ast.Ident); ok {
			if _, isForm := higherOrderForms[ident.Name]; isForm {
				if _, shadowed := funcs[displayIdent(ident.Name)]; !shadowed {
					// Special-form call: the form name resolves without
					// the env, and iterating forms bind an element and
					// index inside their body argument.
					if itBindingForms[ident.Name] {
						if coll, bind, body, err := splitFormArgs(ident.Name, n); err == nil {
							walkIdentifiers(coll, bound, funcs, seen)
							element := bind
							if element == "" {
								element = "it"
							}
							// The binding identifier of a three-arg
							// form is declared by the form, not read
							// from the env, so it is never walked.
							walkIdentifiers(body, &boundIdents{element: element, parent: bound}, funcs, seen)
							return
						}
					}
					for _, a := range n.Args {
						walkIdentifiers(a, bound, funcs, seen)
					}
					return
				}
			}
		}
		walkIdentifiers(n.Fun, bound, funcs, seen)
		for _, a := range n.Args {
			walkIdentifiers(a, bound, funcs, seen)
		}
	case *ast.CompositeLit:
		for _, e := range n.Elts {
			if kv, ok := e.(*ast.KeyValueExpr); ok {
				walkIdentifiers(kv.Key, bound, funcs, seen)
				walkIdentifiers(kv.Value, bound, funcs, seen)
				continue
			}
			walkIdentifiers(e, bound, funcs, seen)
		}
	}
}
