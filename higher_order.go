package expr

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/printer"
	"go/token"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// itEnv is a scope chain used by the higher-order special forms. It
// binds the current element and `index` (0-based position) while
// delegating every other identifier to the parent env. The two-arg
// form of an iterating form binds the element as `it`; the three-arg
// form binds it under the user-chosen name instead, recorded here in
// name. Scopes nest naturally: an itEnv whose parent is itself an
// itEnv resolves inner bindings to the innermost loop, matching
// lexical expectations for `map(users, map(it.friends, it.name))`.
// Because a named scope does not bind `it` at all, a nested two-arg
// form's `it` stays reachable from inside a named form and vice
// versa: `map(reviews, r, map(r.comments, sprintf("%s: %s", r.author, it)))`.
type itEnv struct {
	parent any
	it     any
	index  int64
	// name is the element binding's identifier for the three-arg
	// form. Empty means the two-arg form, which binds `it`.
	name string
}

// elementName returns the identifier under which this scope binds the
// current element: `it` for the two-arg form, the user-chosen name
// for the three-arg form.
func (s *itEnv) elementName() string {
	if s.name != "" {
		return s.name
	}
	return "it"
}

// higherOrderForm is the uniform signature for every built-in
// special form so evalCall can dispatch via a single map lookup.
type higherOrderForm func(*Program, context.Context, *ast.CallExpr, any, int) (any, error)

// userForm describes one user-visible higher-order special form. It
// is the single source of truth that drives the dispatch table, the
// suggester's "did you mean" candidate set, and the "is a special
// form" hint shown for bare-identifier references. Adding a form
// means appending one entry here; the three derived tables stay in
// lockstep automatically.
type userForm struct {
	// name is the spelling users type. For `map` this differs from
	// the dispatch key because Go's parser reserves `map`; the
	// engine rewrites the spelling to mapFormName before parsing.
	name string
	// internal is the key under which fn is registered in
	// higherOrderForms. Equal to name for every form except `map`.
	internal string
	// callHint is the signature shown in the "is a special form"
	// suggester message, e.g. `map(xs, predicate)`.
	callHint string
	// bindsIt marks the iterating forms, which bind an element and
	// `index` inside their body: `it` in the two-arg form, a named
	// binding in the three-arg form. Drives the identifier collector.
	bindsIt bool
	fn      higherOrderForm
}

// userForms enumerates every user-visible special form. Order is
// stable for deterministic iteration in the suggester. Populated in
// init for the same reason as higherOrderForms: the fn references
// reach back through the evaluator into specialFormHint, which reads
// this slice — Go's initialization-cycle check flags that as a
// direct cycle.
var userForms []userForm

// higherOrderForms maps dispatch keys to their special-form
// evaluators. Unlike ordinary functions in the engine's funcs map,
// these receive the raw *ast.CallExpr so they can re-evaluate the
// predicate argument per element with `it`/`index` in scope.
//
// User-visible form names are not reserved: a user-registered
// function or an env entry of the same name shadows the form,
// matching the identifier-resolution order used everywhere else in
// expr. See the dispatch in evalCall.
var higherOrderForms map[string]higherOrderForm

// itBindingForms holds the dispatch keys of the forms that bind
// `it`/`index` in their second argument, derived from userForms.
var itBindingForms map[string]bool

func init() {
	userForms = []userForm{
		{name: "map", internal: mapFormName, callHint: "map(xs, predicate)", bindsIt: true, fn: formMap},
		{name: "filter", internal: "filter", callHint: "filter(xs, predicate)", bindsIt: true, fn: formFilter},
		{name: "flatMap", internal: "flatMap", callHint: "flatMap(xs, predicate)", bindsIt: true, fn: formFlatMap},
		{name: "any", internal: "any", callHint: "any(xs, predicate)", bindsIt: true, fn: formAny},
		{name: "all", internal: "all", callHint: "all(xs, predicate)", bindsIt: true, fn: formAll},
		{name: "find", internal: "find", callHint: "find(xs, predicate)", bindsIt: true, fn: formFind},
		{name: "count", internal: "count", callHint: "count(xs, predicate)", bindsIt: true, fn: formCount},
		{name: "sortBy", internal: "sortBy", callHint: "sortBy(xs, key)", bindsIt: true, fn: formSortBy},
		{name: "try", internal: "try", callHint: "try(value, default)", fn: formTry},
		{name: "if", internal: ifFuncName, callHint: "if(cond, then, else)", fn: formIf},
	}
	higherOrderForms = make(map[string]higherOrderForm, len(userForms)+2)
	itBindingForms = make(map[string]bool, len(userForms))
	for _, f := range userForms {
		higherOrderForms[f.internal] = f.fn
		if f.bindsIt {
			itBindingForms[f.internal] = true
		}
	}
	// Sentinel forms emitted by the optaccess pre-parse rewrite.
	// Users do not type these names; they appear only as the
	// callee of synthesized CallExpr nodes.
	higherOrderForms[trySelectFormName] = formTrySelect
	higherOrderForms[tryIndexFormName] = formTryIndex
}

// itemSeq is an indexable view over a list-shaped collection. Typed
// slices stay behind a reflect.Value and box elements lazily in at(),
// so forms never materialize an intermediate []any just to iterate.
type itemSeq struct {
	items []any         // set when the collection already is a []any
	rv    reflect.Value // used otherwise (typed slices and arrays)
	n     int
}

func (s itemSeq) at(i int) any {
	if s.items != nil {
		return s.items[i]
	}
	return s.rv.Index(i).Interface()
}

// iterItems evaluates collExpr and wraps the result in an itemSeq for
// predicate iteration. nil is treated as an empty list so
// `map(nil, it)` / `filter(nil, it > 0)` return empty without error.
// Maps and other non-list shapes return a user-friendly error naming
// the form, so users do not have to guess which argument was wrong.
func (p *Program) iterItems(ctx context.Context, name string, collExpr ast.Expr, env any, depth int) (itemSeq, error) {
	coll, err := p.eval(ctx, collExpr, env, depth)
	if err != nil {
		return itemSeq{}, err
	}
	if coll == nil {
		return itemSeq{}, nil
	}
	if s, ok := coll.([]any); ok {
		return itemSeq{items: s, n: len(s)}, nil
	}
	rv := reflect.ValueOf(coll)
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		return itemSeq{rv: rv, n: rv.Len()}, nil
	}
	return itemSeq{}, fmt.Errorf("%w: %s expects a list as its first argument, got %T",
		ErrEvaluate, name, coll)
}

// splitFormArgs splits an iterating form's arguments into the
// collection expression, the element binding name, and the body
// expression. The two-arg form binds the element as `it` (bind is
// ""); the three-arg form names the binding explicitly: argument 2
// must be a plain identifier, and the body then sees the element
// under that name instead of `it`, so nested forms can still
// reference an outer `it`. The arity and binding checks happen at
// eval time, like every other form check, because forms can be
// shadowed by env entries that only exist at Run.
func splitFormArgs(name string, n *ast.CallExpr) (coll ast.Expr, bind string, body ast.Expr, err error) {
	switch len(n.Args) {
	case 2:
		return n.Args[0], "", n.Args[1], nil
	case 3:
		ident, ok := n.Args[1].(*ast.Ident)
		if !ok {
			if src := exprDisplayString(n.Args[1]); src != "" {
				return nil, "", nil, fmt.Errorf("%w: %s binding must be a plain identifier, got `%s`",
					ErrEvaluate, name, src)
			}
			return nil, "", nil, fmt.Errorf("%w: %s binding must be a plain identifier",
				ErrEvaluate, name)
		}
		bind = displayIdent(ident.Name)
		switch {
		case bind == "it", bind == "index",
			bind == "true", bind == "false", bind == "nil",
			bind != ident.Name: // keyword sentinel: user wrote `map` or `if`
			return nil, "", nil, fmt.Errorf("%w: %s binding cannot be named %q",
				ErrEvaluate, name, bind)
		}
		return n.Args[0], bind, n.Args[2], nil
	}
	return nil, "", nil, fmt.Errorf("%w: %s expects 2 arguments (collection, predicate) or 3 (collection, name, predicate), got %d",
		ErrEvaluate, name, len(n.Args))
}

// forEach is the shared loop used by every higher-order form. The
// body closure observes `ctx.Err()` through `p.eval` automatically,
// so a cancelled context aborts the iteration at the next element.
//
// Predicate errors are wrapped with the form name, the failing
// element's index, and the predicate's source text so users can
// locate the failure inside nested expressions. Cancellation errors
// and anything else that is not an ErrEvaluate pass through unchanged
// so callers can still match on context.Canceled /
// context.DeadlineExceeded.
func (p *Program) forEach(
	ctx context.Context,
	name string,
	items itemSeq,
	bind string,
	predicate ast.Expr,
	env any,
	depth int,
	body func(item any, result any) (stop bool, err error),
) error {
	scope := &itEnv{parent: env, name: bind}
	for i := 0; i < items.n; i++ {
		item := items.at(i)
		scope.it = item
		scope.index = int64(i)
		v, err := p.eval(ctx, predicate, scope, depth)
		if err != nil {
			return wrapPredicateErr(name, predicate, i, err)
		}
		stop, err := body(item, v)
		if err != nil {
			return err
		}
		if stop {
			return nil
		}
	}
	return nil
}

// wrapPredicateErr decorates a predicate-evaluation error with the
// form name, the predicate's source text, and the failing element's
// index. Errors that do not wrap ErrEvaluate (notably
// context.Canceled and context.DeadlineExceeded, which are returned
// raw by the evaluator) pass through unchanged so callers can still
// match on them with errors.Is.
func wrapPredicateErr(name string, predicate ast.Expr, index int, err error) error {
	if err == nil {
		return nil
	}
	if !errors.Is(err, ErrEvaluate) {
		return err
	}
	src := formatPredicate(predicate)
	if src == "" {
		return fmt.Errorf("%s predicate failed on element %d: %w", name, index, err)
	}
	return fmt.Errorf("%s predicate `%s` failed on element %d: %w", name, src, index, err)
}

// formatPredicate prints a predicate AST back to source text for
// inclusion in error messages. Internal sentinels are translated
// back to the syntax the user wrote: keyword idents (`map`, `if`)
// recover their names, optional-access sentinel calls print as
// `recv?.field` / `recv?[idx]`, and `[]any{...}` /
// `map[string]any{...}` composite literals print in the JSON style
// users type. Returns "" if printing fails or if the result contains
// a backtick (which would clash with the surrounding error format),
// letting wrapPredicateErr fall back to a position-only message.
func formatPredicate(node ast.Expr) string {
	out := exprDisplayString(node)
	if out == "" || strings.ContainsRune(out, '`') {
		return ""
	}
	return out
}

// exprDisplayString prints node via go/printer after rewriting it
// into user-facing display form. Returns "" when printing fails.
func exprDisplayString(node ast.Expr) string {
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, token.NewFileSet(), displayExpr(node)); err != nil {
		return ""
	}
	return buf.String()
}

// displayExpr returns a copy of node with internal sentinel forms
// translated back to user-visible syntax. The original tree is never
// mutated — it is shared by concurrent Runs. Constructs the printer
// cannot represent directly (`?.`, `?[`, JSON-style literals) are
// smuggled through as synthetic Idents whose Name carries the exact
// display text; go/printer emits Ident names verbatim, and every
// receiver position they can occupy is a primary expression, so no
// precedence parentheses are lost.
func displayExpr(node ast.Expr) ast.Expr {
	switch n := node.(type) {
	case *ast.Ident:
		if d := displayIdent(n.Name); d != n.Name {
			return &ast.Ident{Name: d}
		}
		return &ast.Ident{Name: n.Name}
	case *ast.BasicLit:
		return &ast.BasicLit{Kind: n.Kind, Value: n.Value}
	case *ast.ParenExpr:
		return &ast.ParenExpr{X: displayExpr(n.X)}
	case *ast.UnaryExpr:
		return &ast.UnaryExpr{Op: n.Op, X: displayExpr(n.X)}
	case *ast.BinaryExpr:
		return &ast.BinaryExpr{X: displayExpr(n.X), Op: n.Op, Y: displayExpr(n.Y)}
	case *ast.SelectorExpr:
		sel, _ := displayExpr(n.Sel).(*ast.Ident)
		if sel == nil {
			sel = n.Sel
		}
		return &ast.SelectorExpr{X: displayExpr(n.X), Sel: sel}
	case *ast.IndexExpr:
		return &ast.IndexExpr{X: displayExpr(n.X), Index: displayExpr(n.Index)}
	case *ast.CallExpr:
		if out, ok := displayOptAccess(n); ok {
			return out
		}
		args := make([]ast.Expr, len(n.Args))
		for i, a := range n.Args {
			args[i] = displayExpr(a)
		}
		return &ast.CallExpr{Fun: displayExpr(n.Fun), Args: args}
	case *ast.CompositeLit:
		if out, ok := displayJSONLit(n); ok {
			return out
		}
	}
	return node
}

// displayOptAccess turns the optaccess sentinel calls back into the
// `recv?.field` / `recv?[idx]` source forms, carried as a synthetic
// Ident (see displayExpr). ok is false when the call does not match
// the exact shape the rewrite emits.
func displayOptAccess(n *ast.CallExpr) (ast.Expr, bool) {
	ident, ok := n.Fun.(*ast.Ident)
	if !ok || len(n.Args) != 2 {
		return nil, false
	}
	switch ident.Name {
	case trySelectFormName:
		lit, ok := n.Args[1].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return nil, false
		}
		field, err := strconv.Unquote(lit.Value)
		if err != nil {
			return nil, false
		}
		recv := exprDisplayString(n.Args[0])
		if recv == "" {
			return nil, false
		}
		return &ast.Ident{Name: recv + "?." + field}, true
	case tryIndexFormName:
		recv := exprDisplayString(n.Args[0])
		idx := exprDisplayString(n.Args[1])
		if recv == "" || idx == "" {
			return nil, false
		}
		return &ast.Ident{Name: recv + "?[" + idx + "]"}, true
	}
	return nil, false
}

// displayJSONLit prints []any{...} / map[string]any{...} composite
// literals in the bare JSON style expr accepts in source, carried as
// a synthetic Ident (see displayExpr). ok is false for any other
// composite shape.
func displayJSONLit(n *ast.CompositeLit) (ast.Expr, bool) {
	switch typ := n.Type.(type) {
	case *ast.ArrayType:
		if typ.Len != nil {
			return nil, false
		}
		if ident, ok := typ.Elt.(*ast.Ident); !ok || ident.Name != "any" {
			return nil, false
		}
		parts := make([]string, len(n.Elts))
		for i, e := range n.Elts {
			s := exprDisplayString(e)
			if s == "" {
				return nil, false
			}
			parts[i] = s
		}
		return &ast.Ident{Name: "[" + strings.Join(parts, ", ") + "]"}, true
	case *ast.MapType:
		kIdent, kOk := typ.Key.(*ast.Ident)
		vIdent, vOk := typ.Value.(*ast.Ident)
		if !kOk || !vOk || kIdent.Name != "string" || vIdent.Name != "any" {
			return nil, false
		}
		parts := make([]string, len(n.Elts))
		for i, e := range n.Elts {
			kv, ok := e.(*ast.KeyValueExpr)
			if !ok {
				return nil, false
			}
			k := exprDisplayString(kv.Key)
			v := exprDisplayString(kv.Value)
			if k == "" || v == "" {
				return nil, false
			}
			parts[i] = k + ": " + v
		}
		return &ast.Ident{Name: "{" + strings.Join(parts, ", ") + "}"}, true
	}
	return nil, false
}

func formMap(p *Program, ctx context.Context, n *ast.CallExpr, env any, depth int) (any, error) {
	coll, bind, body, err := splitFormArgs("map", n)
	if err != nil {
		return nil, err
	}
	items, err := p.iterItems(ctx, "map", coll, env, depth)
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, items.n)
	err = p.forEach(ctx, "map", items, bind, body, env, depth, func(_ any, v any) (bool, error) {
		out = append(out, v)
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func formFilter(p *Program, ctx context.Context, n *ast.CallExpr, env any, depth int) (any, error) {
	coll, bind, body, err := splitFormArgs("filter", n)
	if err != nil {
		return nil, err
	}
	items, err := p.iterItems(ctx, "filter", coll, env, depth)
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, items.n)
	err = p.forEach(ctx, "filter", items, bind, body, env, depth, func(item any, v any) (bool, error) {
		if isTruthy(v) {
			out = append(out, item)
		}
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// formFlatMap implements `flatMap(xs, body)` / `flatMap(xs, x, body)`.
// Like map, except a body result that is a list is spliced into the
// output element-by-element, and a nil body result is spliced as
// nothing (mirroring iterItems, which treats nil as an empty list).
// Any other body result is appended as a single element, so flatMap
// over mixed data never errors on a non-list value.
func formFlatMap(p *Program, ctx context.Context, n *ast.CallExpr, env any, depth int) (any, error) {
	coll, bind, body, err := splitFormArgs("flatMap", n)
	if err != nil {
		return nil, err
	}
	items, err := p.iterItems(ctx, "flatMap", coll, env, depth)
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, items.n)
	err = p.forEach(ctx, "flatMap", items, bind, body, env, depth, func(_ any, v any) (bool, error) {
		switch s := v.(type) {
		case nil:
			return false, nil
		case []any:
			out = append(out, s...)
			return false, nil
		}
		rv := reflect.ValueOf(v)
		if rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array {
			for i := 0; i < rv.Len(); i++ {
				out = append(out, rv.Index(i).Interface())
			}
			return false, nil
		}
		out = append(out, v)
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// formSortBy implements `sortBy(xs, key)` / `sortBy(xs, x, key)`. The
// key expression is evaluated once per element; elements are then
// reordered by their keys with a stable sort, so equal keys preserve
// input order. Keys follow the same comparison rules as sort: all
// numbers (any int/float mix) or all strings, anything else is an
// ErrEvaluate naming the offending element.
func formSortBy(p *Program, ctx context.Context, n *ast.CallExpr, env any, depth int) (any, error) {
	coll, bind, body, err := splitFormArgs("sortBy", n)
	if err != nil {
		return nil, err
	}
	items, err := p.iterItems(ctx, "sortBy", coll, env, depth)
	if err != nil {
		return nil, err
	}
	keys := make([]any, 0, items.n)
	elems := make([]any, 0, items.n)
	err = p.forEach(ctx, "sortBy", items, bind, body, env, depth, func(item any, v any) (bool, error) {
		keys = append(keys, v)
		elems = append(elems, item)
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	less, err := scalarLessFunc("sortBy", keys)
	if err != nil {
		return nil, err
	}
	sort.Stable(&keyedSorter{keys: keys, elems: elems, less: less})
	return elems, nil
}

// keyedSorter reorders elems and keys in tandem so sortBy can sort
// elements by their computed keys.
type keyedSorter struct {
	keys  []any
	elems []any
	less  func(i, j int) bool
}

func (s *keyedSorter) Len() int { return len(s.keys) }
func (s *keyedSorter) Swap(i, j int) {
	s.keys[i], s.keys[j] = s.keys[j], s.keys[i]
	s.elems[i], s.elems[j] = s.elems[j], s.elems[i]
}
func (s *keyedSorter) Less(i, j int) bool { return s.less(i, j) }

func formAny(p *Program, ctx context.Context, n *ast.CallExpr, env any, depth int) (any, error) {
	coll, bind, body, err := splitFormArgs("any", n)
	if err != nil {
		return nil, err
	}
	items, err := p.iterItems(ctx, "any", coll, env, depth)
	if err != nil {
		return nil, err
	}
	found := false
	err = p.forEach(ctx, "any", items, bind, body, env, depth, func(_ any, v any) (bool, error) {
		if isTruthy(v) {
			found = true
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	return found, nil
}

func formAll(p *Program, ctx context.Context, n *ast.CallExpr, env any, depth int) (any, error) {
	coll, bind, body, err := splitFormArgs("all", n)
	if err != nil {
		return nil, err
	}
	items, err := p.iterItems(ctx, "all", coll, env, depth)
	if err != nil {
		return nil, err
	}
	ok := true
	err = p.forEach(ctx, "all", items, bind, body, env, depth, func(_ any, v any) (bool, error) {
		if !isTruthy(v) {
			ok = false
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	return ok, nil
}

func formFind(p *Program, ctx context.Context, n *ast.CallExpr, env any, depth int) (any, error) {
	coll, bind, body, err := splitFormArgs("find", n)
	if err != nil {
		return nil, err
	}
	items, err := p.iterItems(ctx, "find", coll, env, depth)
	if err != nil {
		return nil, err
	}
	var match any
	matched := false
	err = p.forEach(ctx, "find", items, bind, body, env, depth, func(item any, v any) (bool, error) {
		if isTruthy(v) {
			match = item
			matched = true
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	if !matched {
		return nil, nil
	}
	return match, nil
}

func formCount(p *Program, ctx context.Context, n *ast.CallExpr, env any, depth int) (any, error) {
	coll, bind, body, err := splitFormArgs("count", n)
	if err != nil {
		return nil, err
	}
	items, err := p.iterItems(ctx, "count", coll, env, depth)
	if err != nil {
		return nil, err
	}
	var total int64
	err = p.forEach(ctx, "count", items, bind, body, env, depth, func(_ any, v any) (bool, error) {
		if isTruthy(v) {
			total++
		}
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	return total, nil
}

// formTrySelect implements the optaccess `?.` rewrite target,
// `__try_select__(receiver, "field")`. It evaluates the receiver;
// when the receiver is nil, returns nil. Otherwise it performs a
// field/key lookup via trySelectName, which returns nil for missing
// fields or keys without producing an error. Type errors (e.g.,
// selecting on a non-struct, non-map value) propagate so real bugs
// still surface.
func formTrySelect(p *Program, ctx context.Context, n *ast.CallExpr, env any, depth int) (any, error) {
	if len(n.Args) != 2 {
		return nil, fmt.Errorf("%w: %s expects 2 arguments (receiver, field), got %d",
			ErrEvaluate, trySelectFormName, len(n.Args))
	}
	recv, err := p.eval(ctx, n.Args[0], env, depth)
	if err != nil {
		return nil, err
	}
	if recv == nil {
		return nil, nil
	}
	name, err := p.eval(ctx, n.Args[1], env, depth)
	if err != nil {
		return nil, err
	}
	s, ok := name.(string)
	if !ok {
		return nil, fmt.Errorf("%w: %s expects a string field name, got %T",
			ErrEvaluate, trySelectFormName, name)
	}
	return trySelectName(recv, s, p.fieldTags)
}

// formTryIndex implements the optaccess `?[` rewrite target,
// `__try_index__(receiver, idx)`. Mirrors formTrySelect: nil
// receiver returns nil, missing keys and out-of-range indices return
// nil, and type errors (wrong index kind for the receiver) propagate.
func formTryIndex(p *Program, ctx context.Context, n *ast.CallExpr, env any, depth int) (any, error) {
	if len(n.Args) != 2 {
		return nil, fmt.Errorf("%w: %s expects 2 arguments (receiver, index), got %d",
			ErrEvaluate, tryIndexFormName, len(n.Args))
	}
	recv, err := p.eval(ctx, n.Args[0], env, depth)
	if err != nil {
		return nil, err
	}
	if recv == nil {
		return nil, nil
	}
	idx, err := p.eval(ctx, n.Args[1], env, depth)
	if err != nil {
		return nil, err
	}
	return tryIndexValue(recv, idx)
}

// trySelectName mirrors selectField but returns nil for missing
// fields or keys. Method values are not surfaced; selectField does
// not surface them either, so the two paths agree on what counts as
// a "field" lookup.
func trySelectName(recv any, name string, fieldTags *structTagConfig) (any, error) {
	if recv == nil {
		return nil, nil
	}
	if m, ok := recv.(map[string]any); ok {
		v := m[name]
		return v, nil
	}
	rv := reflect.ValueOf(recv)
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil, nil
		}
		rv = rv.Elem()
	}
	switch rv.Kind() {
	case reflect.Struct:
		fv, ok, err := structFieldByName(rv, name, fieldTags)
		if err != nil {
			return nil, err
		}
		if !ok || !fv.IsValid() || !fv.CanInterface() {
			return nil, nil
		}
		return fv.Interface(), nil
	case reflect.Map:
		if rv.Type().Key().Kind() != reflect.String {
			return nil, fmt.Errorf("%w: cannot select %q on map with non-string keys",
				ErrEvaluate, name)
		}
		mv := rv.MapIndex(mapStringKey(rv.Type().Key(), name))
		if !mv.IsValid() {
			return nil, nil
		}
		return mv.Interface(), nil
	}
	return nil, fmt.Errorf("%w: cannot select %q on %T", ErrEvaluate, name, recv)
}

// tryIndexValue mirrors indexValue but returns nil for missing
// keys and out-of-range slice/string indices. Wrong-kind errors
// (e.g., string index into a slice, non-integer slice index) still
// propagate.
func tryIndexValue(recv, idx any) (any, error) {
	if recv == nil {
		return nil, nil
	}
	if m, ok := recv.(map[string]any); ok {
		key, ok := idx.(string)
		if !ok {
			return nil, fmt.Errorf("%w: map index must be string, got %T",
				ErrEvaluate, idx)
		}
		v := m[key]
		return v, nil
	}
	rv := reflect.ValueOf(recv)
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		i, err := toIndexInt(idx)
		if err != nil {
			return nil, err
		}
		if i < 0 || i >= int64(rv.Len()) {
			return nil, nil
		}
		return rv.Index(int(i)).Interface(), nil
	case reflect.String:
		i, err := toIndexInt(idx)
		if err != nil {
			return nil, err
		}
		runes := []rune(rv.String())
		if i < 0 || i >= int64(len(runes)) {
			return nil, nil
		}
		return string(runes[i]), nil
	case reflect.Map:
		keyType := rv.Type().Key()
		if idx == nil {
			return nil, fmt.Errorf("%w: cannot use nil as map key %v", ErrEvaluate, keyType)
		}
		kv := reflect.ValueOf(idx)
		if !kv.Type().AssignableTo(keyType) {
			if isNumericKind(keyType.Kind()) && isNumericKind(kv.Kind()) {
				converted, err := safeNumericConvert(kv, keyType)
				if err != nil {
					return nil, fmt.Errorf("%w: map key conversion: %v", ErrEvaluate, err)
				}
				kv = converted
			} else if kv.Type().ConvertibleTo(keyType) {
				kv = kv.Convert(keyType)
			} else {
				return nil, fmt.Errorf("%w: cannot use %T as map key %v",
					ErrEvaluate, idx, keyType)
			}
		}
		mv := rv.MapIndex(kv)
		if !mv.IsValid() {
			return nil, nil
		}
		return mv.Interface(), nil
	}
	return nil, fmt.Errorf("%w: cannot index %T", ErrEvaluate, recv)
}

// formIf implements `if(cond, then, else)` as a lazy special form:
// only the branch selected by the condition's truthiness is
// evaluated, so the guard idiom `if(n != 0, total/n, 0)` works
// without tripping over the untaken branch. `if` binds no implicit
// `it`/`index`; all three arguments see the enclosing scope.
//
// Like every special form it can be shadowed: a function registered
// under "if" or an env entry of that name wins, restoring eager
// argument evaluation through the normal call path.
func formIf(p *Program, ctx context.Context, n *ast.CallExpr, env any, depth int) (any, error) {
	if len(n.Args) != 3 {
		return nil, fmt.Errorf("%w: if expects 3 arguments (cond, then, else), got %d",
			ErrEvaluate, len(n.Args))
	}
	cond, err := p.eval(ctx, n.Args[0], env, depth)
	if err != nil {
		return nil, err
	}
	if isTruthy(cond) {
		return p.eval(ctx, n.Args[1], env, depth)
	}
	return p.eval(ctx, n.Args[2], env, depth)
}

// formTry implements `try(value, default)`. It evaluates the first
// argument; if evaluation returns an ErrEvaluate, it evaluates and
// returns the second argument instead. The default expression is
// only evaluated when the primary expression failed, so users can
// safely supply expensive or side-effecting fallbacks.
//
// Context cancellation (context.Canceled / context.DeadlineExceeded)
// is propagated unwrapped. Anything wrapping ErrCompile is also
// propagated; expr should not normally surface compile errors at
// Run time, but if it does they signal a programmer mistake that
// try is not the right place to swallow.
func formTry(p *Program, ctx context.Context, n *ast.CallExpr, env any, depth int) (any, error) {
	if len(n.Args) != 2 {
		return nil, fmt.Errorf("%w: try expects 2 arguments (value, default), got %d",
			ErrEvaluate, len(n.Args))
	}
	v, err := p.eval(ctx, n.Args[0], env, depth)
	if err == nil {
		return v, nil
	}
	if !errors.Is(err, ErrEvaluate) || errors.Is(err, ErrCompile) {
		return nil, err
	}
	return p.eval(ctx, n.Args[1], env, depth)
}
