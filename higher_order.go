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
	"strings"
)

// itEnv is a scope chain used by the higher-order special forms. It
// binds `it` (current element) and `index` (0-based position) while
// delegating every other identifier to the parent env. Scopes nest
// naturally: an itEnv whose parent is itself an itEnv resolves inner
// `it`/`index` to the innermost loop, matching lexical expectations
// for `map(users, map(it.friends, it.name))`.
type itEnv struct {
	parent any
	it     any
	index  int64
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
	fn       higherOrderForm
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

func init() {
	userForms = []userForm{
		{name: "map", internal: mapFormName, callHint: "map(xs, predicate)", fn: formMap},
		{name: "filter", internal: "filter", callHint: "filter(xs, predicate)", fn: formFilter},
		{name: "any", internal: "any", callHint: "any(xs, predicate)", fn: formAny},
		{name: "all", internal: "all", callHint: "all(xs, predicate)", fn: formAll},
		{name: "find", internal: "find", callHint: "find(xs, predicate)", fn: formFind},
		{name: "count", internal: "count", callHint: "count(xs, predicate)", fn: formCount},
		{name: "try", internal: "try", callHint: "try(value, default)", fn: formTry},
	}
	higherOrderForms = make(map[string]higherOrderForm, len(userForms)+2)
	for _, f := range userForms {
		higherOrderForms[f.internal] = f.fn
	}
	// Sentinel forms emitted by the optaccess pre-parse rewrite.
	// Users do not type these names; they appear only as the
	// callee of synthesized CallExpr nodes.
	higherOrderForms[trySelectFormName] = formTrySelect
	higherOrderForms[tryIndexFormName] = formTryIndex
}

// iterItems evaluates collExpr and converts the result to a []any for
// predicate iteration. nil is treated as an empty list so
// `map(nil, it)` / `filter(nil, it > 0)` return empty without error.
// Maps and other non-list shapes return a user-friendly error naming
// the form, so users do not have to guess which argument was wrong.
func (p *Program) iterItems(ctx context.Context, name string, collExpr ast.Expr, env any, depth int) ([]any, error) {
	coll, err := p.eval(ctx, collExpr, env, depth)
	if err != nil {
		return nil, err
	}
	if coll == nil {
		return nil, nil
	}
	if s, ok := coll.([]any); ok {
		return s, nil
	}
	rv := reflect.ValueOf(coll)
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		out := make([]any, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			out[i] = rv.Index(i).Interface()
		}
		return out, nil
	}
	return nil, fmt.Errorf("%w: %s expects a list as its first argument, got %T",
		ErrEvaluate, name, coll)
}

// checkFormArity reports a consistent error across every form.
func checkFormArity(name string, got int) error {
	if got == 2 {
		return nil
	}
	return fmt.Errorf("%w: %s expects 2 arguments (collection, predicate), got %d",
		ErrEvaluate, name, got)
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
	items []any,
	predicate ast.Expr,
	env any,
	depth int,
	body func(item any, result any) (stop bool, err error),
) error {
	scope := &itEnv{parent: env}
	for i, item := range items {
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
// inclusion in error messages. The internal map sentinel is
// translated back to `map` so nested forms read the way users wrote
// them. Returns "" if printing fails or if the result contains a
// backtick (which would clash with the surrounding error format),
// letting wrapPredicateErr fall back to a position-only message.
func formatPredicate(node ast.Expr) string {
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, token.NewFileSet(), node); err != nil {
		return ""
	}
	out := strings.ReplaceAll(buf.String(), mapFormName, "map")
	if strings.ContainsRune(out, '`') {
		return ""
	}
	return out
}

func formMap(p *Program, ctx context.Context, n *ast.CallExpr, env any, depth int) (any, error) {
	if err := checkFormArity("map", len(n.Args)); err != nil {
		return nil, err
	}
	items, err := p.iterItems(ctx, "map", n.Args[0], env, depth)
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, len(items))
	err = p.forEach(ctx, "map", items, n.Args[1], env, depth, func(_ any, v any) (bool, error) {
		out = append(out, v)
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func formFilter(p *Program, ctx context.Context, n *ast.CallExpr, env any, depth int) (any, error) {
	if err := checkFormArity("filter", len(n.Args)); err != nil {
		return nil, err
	}
	items, err := p.iterItems(ctx, "filter", n.Args[0], env, depth)
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, len(items))
	err = p.forEach(ctx, "filter", items, n.Args[1], env, depth, func(item any, v any) (bool, error) {
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

func formAny(p *Program, ctx context.Context, n *ast.CallExpr, env any, depth int) (any, error) {
	if err := checkFormArity("any", len(n.Args)); err != nil {
		return nil, err
	}
	items, err := p.iterItems(ctx, "any", n.Args[0], env, depth)
	if err != nil {
		return nil, err
	}
	found := false
	err = p.forEach(ctx, "any", items, n.Args[1], env, depth, func(_ any, v any) (bool, error) {
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
	if err := checkFormArity("all", len(n.Args)); err != nil {
		return nil, err
	}
	items, err := p.iterItems(ctx, "all", n.Args[0], env, depth)
	if err != nil {
		return nil, err
	}
	ok := true
	err = p.forEach(ctx, "all", items, n.Args[1], env, depth, func(_ any, v any) (bool, error) {
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
	if err := checkFormArity("find", len(n.Args)); err != nil {
		return nil, err
	}
	items, err := p.iterItems(ctx, "find", n.Args[0], env, depth)
	if err != nil {
		return nil, err
	}
	var match any
	matched := false
	err = p.forEach(ctx, "find", items, n.Args[1], env, depth, func(item any, v any) (bool, error) {
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
	if err := checkFormArity("count", len(n.Args)); err != nil {
		return nil, err
	}
	items, err := p.iterItems(ctx, "count", n.Args[0], env, depth)
	if err != nil {
		return nil, err
	}
	var total int64
	err = p.forEach(ctx, "count", items, n.Args[1], env, depth, func(_ any, v any) (bool, error) {
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
