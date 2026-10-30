package expr

import "go/ast"

// Call is one call of a registered function, as Program.Calls reports it.
type Call struct {
	// Name is the function's name as written.
	Name string
	// Args holds each argument as its own Program, compiled with the
	// options of the program it came from. A pipe is already desugared:
	// `a | f(x)` reports f with Args a and x. An argument of a Bound call
	// that reads `it`, `index` or a named element has those only per
	// element, inside the form: run on its own, it fails to resolve them.
	Args []*Program
	// Bound is true when the call sits in the body of an iterating form
	// (map, filter, flatMap, any, all, find, count, sortBy), where the
	// form binds `it`, `index` or a named element, so the call runs once
	// per element.
	Bound bool
}

// Calls returns each call of the named functions, in source order,
// including calls nested in another call's arguments. Hosts use it to
// find calls they must handle before a Run, such as functions whose
// answers come from outside the program.
//
// Only calls by bare name count: a name shadowed by an iterating form's
// named binding is not a call of the function. The analysis is static,
// like Identifiers: an env entry that shadows a registered function at
// Run time is still reported.
func (p *Program) Calls(names ...string) []Call {
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[n] = true
	}
	var out []Call
	p.walkCalls(p.root, nil, want, &out)
	return out
}

// walkCalls records the calls of the wanted functions under node. bound
// holds the element bindings of the enclosing iterating forms: non-nil
// means the node runs per element, and a call of a bound name calls the
// element, not the function.
func (p *Program) walkCalls(node ast.Expr, bound *boundIdents, want map[string]bool, out *[]Call) {
	switch n := node.(type) {
	case *ast.ParenExpr:
		p.walkCalls(n.X, bound, want, out)
	case *ast.UnaryExpr:
		p.walkCalls(n.X, bound, want, out)
	case *ast.BinaryExpr:
		p.walkCalls(n.X, bound, want, out)
		p.walkCalls(n.Y, bound, want, out)
	case *ast.SelectorExpr:
		p.walkCalls(n.X, bound, want, out)
	case *ast.IndexExpr:
		p.walkCalls(n.X, bound, want, out)
		p.walkCalls(n.Index, bound, want, out)
	case *ast.CompositeLit:
		for _, e := range n.Elts {
			if kv, ok := e.(*ast.KeyValueExpr); ok {
				p.walkCalls(kv.Key, bound, want, out)
				p.walkCalls(kv.Value, bound, want, out)
				continue
			}
			p.walkCalls(e, bound, want, out)
		}
	case *ast.CallExpr:
		ident, _ := n.Fun.(*ast.Ident)
		if ident != nil && itBindingForms[ident.Name] {
			if _, shadowed := p.funcs[displayIdent(ident.Name)]; !shadowed {
				if coll, bind, body, err := splitFormArgs(ident.Name, n); err == nil {
					p.walkCalls(coll, bound, want, out)
					if bind == "" {
						bind = "it"
					}
					p.walkCalls(body, &boundIdents{element: bind, parent: bound}, want, out)
					return
				}
			}
		}
		if ident != nil && want[displayIdent(ident.Name)] && !bound.has(displayIdent(ident.Name)) {
			c := Call{Name: displayIdent(ident.Name), Bound: bound != nil}
			for _, a := range n.Args {
				c.Args = append(c.Args, p.sub(a))
			}
			*out = append(*out, c)
		} else {
			p.walkCalls(n.Fun, bound, want, out)
		}
		for _, a := range n.Args {
			p.walkCalls(a, bound, want, out)
		}
	}
}

// sub returns the subtree node as a Program of its own. It shares the
// parent's caches, which are read-only after compile, and its Source is
// the subtree printed back in display form.
func (p *Program) sub(node ast.Expr) *Program {
	return &Program{
		source:      exprDisplayString(node),
		root:        node,
		funcs:       p.funcs,
		prepared:    p.prepared,
		fieldTags:   p.fieldTags,
		callCache:   p.callCache,
		litCache:    p.litCache,
		identifiers: collectIdentifiers(node, p.funcs),
		evalBudget:  p.evalBudget,
	}
}

// Literal returns the program's value when the whole expression is a
// literal: a string, number, bool or nil, or a list or object built only
// from literals (`["a", "b"]`, `{"k": 1}`). Constant arithmetic folded at
// compile time (`-1`, `2 * 3`) counts as a literal. Numbers come back as
// int64 or float64, lists as []any and objects as map[string]any.
func (p *Program) Literal() (any, bool) {
	return p.literal(p.root)
}

func (p *Program) literal(node ast.Expr) (any, bool) {
	if v, ok := p.constValue(node); ok {
		return v, true
	}
	switch n := node.(type) {
	case *ast.ParenExpr:
		return p.literal(n.X)
	case *ast.CompositeLit:
		switch n.Type.(type) {
		case *ast.ArrayType:
			out := make([]any, 0, len(n.Elts))
			for _, e := range n.Elts {
				v, ok := p.literal(e)
				if !ok {
					return nil, false
				}
				out = append(out, v)
			}
			return out, true
		case *ast.MapType:
			out := make(map[string]any, len(n.Elts))
			for _, e := range n.Elts {
				kv, ok := e.(*ast.KeyValueExpr)
				if !ok {
					return nil, false
				}
				k, ok := p.literal(kv.Key)
				key, isString := k.(string)
				if !ok || !isString {
					return nil, false
				}
				v, ok := p.literal(kv.Value)
				if !ok {
					return nil, false
				}
				out[key] = v
			}
			return out, true
		}
	}
	return nil, false
}
