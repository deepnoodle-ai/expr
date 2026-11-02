package expr

import (
	"context"
	"fmt"
	"reflect"
	"sync"
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
// registered function, built once when the option is applied. The
// native field is the fast path; when it is nil, fv and sig drive the
// reflect path shared with env functions and bound methods.
type preparedFunc struct {
	name string

	// Native fast path.
	native Func

	// Reflect path (only populated when native is nil).
	fv  reflect.Value
	sig *callSig
}

// prepareFunc inspects fn and builds a preparedFunc. If fn already
// matches the native Func shape it is stored directly; otherwise its
// signature is validated and cached for the reflect path.
func prepareFunc(name string, fn any) (*preparedFunc, error) {
	if fn == nil {
		return nil, fmt.Errorf("function %q is nil", name)
	}
	if nf, ok := fn.(Func); ok {
		if nf == nil {
			return nil, fmt.Errorf("function %q is a nil Func", name)
		}
		return &preparedFunc{name: name, native: nf}, nil
	}
	fv := reflect.ValueOf(fn)
	if fv.Kind() != reflect.Func {
		return nil, fmt.Errorf("function %q is not a function (got %T)", name, fn)
	}
	if fv.IsNil() {
		return nil, fmt.Errorf("function %q is a nil function value", name)
	}
	sig, err := sigOf(fv.Type())
	if err != nil {
		return nil, fmt.Errorf("function %q %v", name, err)
	}
	return &preparedFunc{name: name, fv: fv, sig: sig}, nil
}

// callPrepared invokes pf with args. Uses the native dispatcher when
// available, otherwise the shared reflect path.
func callPrepared(ctx context.Context, pf *preparedFunc, args []any) (any, error) {
	if pf.native != nil {
		v, err := pf.native(ctx, args)
		return v, wrapCallErr(pf.name, err)
	}
	return invokeReflect(ctx, pf.name, pf.fv, pf.sig, args)
}

// callSig is what the reflect call path needs to know about a function
// type: how to build its arguments and read its results. It is computed
// once per type (sigOf) and shared by registered functions, env
// functions and bound methods, so the three apply the same argument
// conversions and result rules.
type callSig struct {
	paramOff     int // 1 if the function takes context.Context as its first param
	declaredIn   int // user-visible parameter count (excluding ctx)
	fixed        int // declaredIn - 1 for variadic, else declaredIn
	variadic     bool
	paramTypes   []reflect.Type // ft.In(paramOff+i) for i in [0, declaredIn)
	variadicElem reflect.Type   // element type if variadic
	numOut       int
}

var callSigs sync.Map // reflect.Type -> *callSig or error

// sigOf returns the callSig of a function type, or an error (without an
// ErrEvaluate/ErrCompile prefix or function name) for a result shape
// expr does not support.
func sigOf(ft reflect.Type) (*callSig, error) {
	if cached, ok := callSigs.Load(ft); ok {
		if err, isErr := cached.(error); isErr {
			return nil, err
		}
		return cached.(*callSig), nil
	}
	sig, err := buildSig(ft)
	if err != nil {
		callSigs.Store(ft, err)
		return nil, err
	}
	callSigs.Store(ft, sig)
	return sig, nil
}

func buildSig(ft reflect.Type) (*callSig, error) {
	s := &callSig{variadic: ft.IsVariadic(), numOut: ft.NumOut()}
	switch {
	case s.numOut > 2:
		return nil, fmt.Errorf("returns %d values (expected 0, 1, or (T, error))", s.numOut)
	case s.numOut == 2 && !ft.Out(1).Implements(errValType):
		return nil, fmt.Errorf("second return must be error, got %v", ft.Out(1))
	}
	if ft.NumIn() > 0 && ft.In(0) == ctxType {
		s.paramOff = 1
	}
	s.declaredIn = ft.NumIn() - s.paramOff
	s.fixed = s.declaredIn
	if s.variadic {
		s.fixed--
		s.variadicElem = ft.In(ft.NumIn() - 1).Elem()
	}
	s.paramTypes = make([]reflect.Type, s.declaredIn)
	for i := range s.paramTypes {
		s.paramTypes[i] = ft.In(s.paramOff + i)
	}
	return s, nil
}

// invokeReflect checks and converts args for sig, calls fv, and reads
// its results. A runtime panic in the callee becomes an ErrEvaluate.
func invokeReflect(ctx context.Context, name string, fv reflect.Value, sig *callSig, args []any) (any, error) {
	if sig.variadic {
		if len(args) < sig.fixed {
			return nil, fmt.Errorf("%w: %q expects at least %d args, got %d", ErrEvaluate, name, sig.fixed, len(args))
		}
	} else if len(args) != sig.declaredIn {
		return nil, fmt.Errorf("%w: %q expects %d args, got %d", ErrEvaluate, name, sig.declaredIn, len(args))
	}
	in := make([]reflect.Value, sig.paramOff+len(args))
	if sig.paramOff == 1 {
		in[0] = reflect.ValueOf(ctx)
	}
	for i, a := range args {
		want := sig.variadicElem
		if i < sig.fixed || !sig.variadic {
			want = sig.paramTypes[i]
		}
		v, err := convertArg(name, i, a, want)
		if err != nil {
			return nil, err
		}
		in[sig.paramOff+i] = v
	}
	out, err := callReflectRecoverRuntime(name, fv, in)
	if err != nil {
		return nil, err
	}
	switch sig.numOut {
	case 0:
		return nil, nil
	case 1:
		return out[0].Interface(), nil
	}
	if err := callResultErr(out[1]); err != nil {
		return nil, wrapCallErr(name, err)
	}
	return out[0].Interface(), nil
}
