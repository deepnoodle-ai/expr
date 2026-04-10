package expr

import (
	"context"
	"fmt"
	"reflect"
)

// Func is a native dispatcher signature. Functions registered with
// this type skip the reflect-based call path entirely: the evaluator
// invokes them directly with the evaluated argument list and the
// current context. Use this form for hot builtins or any user
// function where an extra allocation per call matters.
//
// If the function needs arity or type validation it must do it
// itself; the dispatcher performs no checks.
type Func func(ctx context.Context, args []any) (any, error)

// preparedFunc holds everything callPrepared needs to invoke a
// registered function, built once at Engine construction. The native
// field is the fast path; when it is nil the reflect fields drive a
// fallback that reuses the cached type/value rather than re-deriving
// them on every call.
type preparedFunc struct {
	name string

	// Native fast path.
	native Func

	// Reflect fallback metadata (only populated when native is nil).
	fv           reflect.Value
	ft           reflect.Type
	paramOff     int // 1 if the function takes context.Context as its first param
	declaredIn   int // user-visible parameter count (excluding ctx)
	fixed        int // len(declaredIn) - 1 for variadic, else declaredIn
	variadic     bool
	paramTypes   []reflect.Type // cached ft.In(paramOff+i) for i in [0, declaredIn)
	variadicElem reflect.Type   // element type if variadic
	numOut       int
	hasErrRet    bool // true if second return is error
}

// prepareFunc inspects fn and builds a preparedFunc. If fn already
// matches the native Func shape it is stored directly; otherwise the
// function's reflect metadata is cached for the fallback path.
func prepareFunc(name string, fn any) (*preparedFunc, error) {
	if fn == nil {
		return nil, fmt.Errorf("expr: function %q is nil", name)
	}
	if nf, ok := fn.(Func); ok {
		return &preparedFunc{name: name, native: nf}, nil
	}
	fv := reflect.ValueOf(fn)
	if fv.Kind() != reflect.Func {
		return nil, fmt.Errorf("expr: function %q is not a function (got %T)", name, fn)
	}
	if fv.IsNil() {
		return nil, fmt.Errorf("expr: function %q is a nil function value", name)
	}
	ft := fv.Type()
	p := &preparedFunc{
		name:     name,
		fv:       fv,
		ft:       ft,
		variadic: ft.IsVariadic(),
		numOut:   ft.NumOut(),
	}
	if ft.NumIn() > 0 && ft.In(0) == ctxType {
		p.paramOff = 1
	}
	p.declaredIn = ft.NumIn() - p.paramOff
	if p.variadic {
		p.fixed = p.declaredIn - 1
		p.variadicElem = ft.In(ft.NumIn() - 1).Elem()
	} else {
		p.fixed = p.declaredIn
	}
	p.paramTypes = make([]reflect.Type, p.declaredIn)
	for i := 0; i < p.declaredIn; i++ {
		p.paramTypes[i] = ft.In(p.paramOff + i)
	}
	if p.numOut == 2 {
		errType := reflect.TypeOf((*error)(nil)).Elem()
		if !ft.Out(1).Implements(errType) {
			return nil, fmt.Errorf("expr: function %q: second return must be error, got %v", name, ft.Out(1))
		}
		p.hasErrRet = true
	} else if p.numOut > 2 {
		return nil, fmt.Errorf("expr: function %q returns %d values (expected 0, 1, or (T, error))", name, p.numOut)
	}
	return p, nil
}

// callPrepared invokes pf with args. Uses the native dispatcher when
// available, otherwise takes the reflect fallback path using cached
// metadata.
func callPrepared(ctx context.Context, pf *preparedFunc, args []any) (any, error) {
	if pf.native != nil {
		return pf.native(ctx, args)
	}
	return callPreparedReflect(ctx, pf, args)
}

func callPreparedReflect(ctx context.Context, pf *preparedFunc, args []any) (any, error) {
	if pf.variadic {
		if len(args) < pf.fixed {
			return nil, fmt.Errorf("%w: %q expects at least %d args, got %d", ErrEvaluate, pf.name, pf.fixed, len(args))
		}
	} else if len(args) != pf.declaredIn {
		return nil, fmt.Errorf("%w: %q expects %d args, got %d", ErrEvaluate, pf.name, pf.declaredIn, len(args))
	}

	in := make([]reflect.Value, pf.paramOff+len(args))
	if pf.paramOff == 1 {
		in[0] = reflect.ValueOf(ctx)
	}
	for i := 0; i < pf.fixed; i++ {
		v, err := convertArg(pf.name, i, args[i], pf.paramTypes[i])
		if err != nil {
			return nil, err
		}
		in[pf.paramOff+i] = v
	}
	if pf.variadic {
		for i := pf.fixed; i < len(args); i++ {
			v, err := convertArg(pf.name, i, args[i], pf.variadicElem)
			if err != nil {
				return nil, err
			}
			in[pf.paramOff+i] = v
		}
	} else {
		for i := pf.fixed; i < len(args); i++ {
			v, err := convertArg(pf.name, i, args[i], pf.paramTypes[i])
			if err != nil {
				return nil, err
			}
			in[pf.paramOff+i] = v
		}
	}

	out := pf.fv.Call(in)
	switch pf.numOut {
	case 0:
		return nil, nil
	case 1:
		return out[0].Interface(), nil
	}
	// (T, error)
	if !out[1].IsNil() {
		return nil, out[1].Interface().(error)
	}
	return out[0].Interface(), nil
}
