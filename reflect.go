package expr

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"runtime"
)

// ctxType and errValType are the reflect.Types of context.Context and
// error; cached at package init so buildCallArgs and finishCall don't
// construct fresh ones on every call.
var (
	ctxType    = reflect.TypeOf((*context.Context)(nil)).Elem()
	errValType = reflect.TypeOf((*error)(nil)).Elem()
)

// callFunction invokes fn with args using reflection. Argument values are
// converted to the function's declared parameter types where possible.
// Return signatures supported: (), (T), (T, error). If the function's
// first parameter is context.Context, ctx is passed through automatically
// and is not counted against the caller-supplied argument list.
func callFunction(ctx context.Context, name string, fn any, args []any) (any, error) {
	if nf, ok := fn.(Func); ok {
		if nf == nil {
			return nil, fmt.Errorf("%w: %q is a nil function value", ErrEvaluate, name)
		}
		v, err := nf(ctx, args)
		return v, wrapCallErr(name, err)
	}
	return callFuncValue(ctx, name, reflect.ValueOf(fn), args)
}

// callFuncValue invokes fv, an env function, struct field or bound
// method, through the reflect path registered functions use.
func callFuncValue(ctx context.Context, name string, fv reflect.Value, args []any) (any, error) {
	if fv.Kind() != reflect.Func {
		return nil, fmt.Errorf("%w: %q is not a function (got %v)", ErrEvaluate, name, describeValue(fv))
	}
	if fv.IsNil() {
		return nil, fmt.Errorf("%w: %q is a nil function value", ErrEvaluate, name)
	}
	sig, err := sigOf(fv.Type())
	if err != nil {
		return nil, fmt.Errorf("%w: %q %v", ErrEvaluate, name, err)
	}
	return invokeReflect(ctx, name, fv, sig, args)
}

// describeValue names rv's type for an error message: its Go type, or
// nil for an invalid Value.
func describeValue(rv reflect.Value) string {
	if !rv.IsValid() {
		return "nil"
	}
	return rv.Type().String()
}

// callReflectRecoverRuntime invokes fv and converts runtime.Error
// panics (nil deref, index out of range, ...) in the callee into an
// ErrEvaluate so one buggy function cannot take down the host through
// Run. Every reflect-based dispatch path — bound methods, env-stored
// functions, and WithFunctions-registered functions — goes through
// this wrapper. Deliberate panics with non-runtime values are
// re-raised: those signal programmer intent that expr should not
// swallow.
func callReflectRecoverRuntime(name string, fv reflect.Value, in []reflect.Value) (out []reflect.Value, err error) {
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(runtime.Error); ok {
				out = nil
				err = fmt.Errorf("%w: %q panicked during call: %v", ErrEvaluate, name, r)
				return
			}
			panic(r)
		}
	}()
	out = fv.Call(in)
	return out, nil
}

// callResultErr extracts the error a function returned in v, whose type
// implements error. A nil interface or nil pointer-like value is no
// error; a value of a non-nilable kind (a struct implementing error) is
// always one, as it is in Go once boxed in an error interface.
func callResultErr(v reflect.Value) error {
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func:
		if v.IsNil() {
			return nil
		}
	}
	err, _ := v.Interface().(error)
	return err
}

// wrapCallErr wraps an error returned by a host function in ErrEvaluate,
// keeping the original cause reachable through errors.Is / errors.As.
// Errors that already wrap ErrEvaluate (builtins, nested Runs) pass
// through unchanged, as do context cancellation errors, which try must
// not swallow.
func wrapCallErr(name string, err error) error {
	if err == nil || errors.Is(err, ErrEvaluate) ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("%w: %q: %w", ErrEvaluate, name, err)
}

func convertArg(name string, idx int, value any, want reflect.Type) (reflect.Value, error) {
	if value == nil {
		switch want.Kind() {
		case reflect.Interface, reflect.Pointer, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func:
			return reflect.Zero(want), nil
		}
		return reflect.Value{}, fmt.Errorf("%w: %q arg %d: cannot pass nil as %v", ErrEvaluate, name, idx, want)
	}
	rv := reflect.ValueOf(value)
	if rv.Type().AssignableTo(want) {
		return rv, nil
	}
	// Numeric coercion: expr stores ints as int64 and floats as float64,
	// but callers often declare fn(int) / fn(float32) / etc. We range-check
	// every integer narrowing so silent wraparound can't hide bugs.
	if isNumericKind(want.Kind()) && isNumericKind(rv.Kind()) {
		converted, err := safeNumericConvert(rv, want)
		if err != nil {
			return reflect.Value{}, fmt.Errorf("%w: %q arg %d: %v", ErrEvaluate, name, idx, err)
		}
		return converted, nil
	}
	if isIntegerKind(rv.Kind()) && want.Kind() == reflect.String {
		return reflect.Value{}, fmt.Errorf("%w: %q arg %d: cannot convert integer to string", ErrEvaluate, name, idx)
	}
	if want.Kind() == reflect.Interface && rv.Type().Implements(want) {
		return rv, nil
	}
	if converted, ok := convertValue(rv, want); ok {
		return converted, nil
	}
	return reflect.Value{}, fmt.Errorf("%w: %q arg %d: cannot use %T as %v", ErrEvaluate, name, idx, value, want)
}

// convertValue converts rv to want when Go allows it for this value.
// reflect.Type.ConvertibleTo only checks types: converting a slice to an
// array (or array pointer) also needs the slice to be long enough, and
// reflect panics otherwise.
func convertValue(rv reflect.Value, want reflect.Type) (reflect.Value, bool) {
	if !rv.Type().ConvertibleTo(want) {
		return reflect.Value{}, false
	}
	if rv.Kind() == reflect.Slice {
		switch {
		case want.Kind() == reflect.Array:
			if rv.Len() < want.Len() {
				return reflect.Value{}, false
			}
		case want.Kind() == reflect.Pointer && want.Elem().Kind() == reflect.Array:
			if rv.Len() < want.Elem().Len() {
				return reflect.Value{}, false
			}
		}
	}
	return rv.Convert(want), true
}

// mapKey converts idx to a key usable with a map of keyType. Beyond the
// type conversion, the key must be hashable at run time: an interface
// key type accepts any dynamic value, and hashing a slice (or a struct
// or array holding one) panics.
func mapKey(keyType reflect.Type, idx any) (reflect.Value, error) {
	if idx == nil {
		// Untyped nil is only a valid key for nilable key kinds; rather
		// than handling every combination via reflect we reject it.
		return reflect.Value{}, fmt.Errorf("%w: cannot use nil as map key %v", ErrEvaluate, keyType)
	}
	kv := reflect.ValueOf(idx)
	if !kv.Type().AssignableTo(keyType) {
		if isNumericKind(keyType.Kind()) && isNumericKind(kv.Kind()) {
			// An integer key follows the index rule (xs[1.5] fails):
			// a float must be integral, not truncated.
			if isIntegerKind(keyType.Kind()) && (kv.Kind() == reflect.Float32 || kv.Kind() == reflect.Float64) {
				if f := kv.Float(); f != math.Trunc(f) {
					return reflect.Value{}, fmt.Errorf("%w: map key must be integer, got %v", ErrEvaluate, f)
				}
			}
			converted, err := safeNumericConvert(kv, keyType)
			if err != nil {
				return reflect.Value{}, fmt.Errorf("%w: map key conversion: %v", ErrEvaluate, err)
			}
			kv = converted
		} else if converted, ok := convertValue(kv, keyType); ok {
			kv = converted
		} else {
			return reflect.Value{}, fmt.Errorf("%w: cannot use %T as map key %v", ErrEvaluate, idx, keyType)
		}
	}
	if !canCompare(kv) {
		return reflect.Value{}, fmt.Errorf("%w: cannot use unhashable %T as map key", ErrEvaluate, idx)
	}
	return kv, nil
}

// canCompare reports whether Go's == (and map hashing) accepts v without
// panicking. Both checks are needed: Type.Comparable misses an
// interface field holding a slice, and Value.Comparable reports a
// zero-length array of uncomparable elements ([0]map[K]V) comparable
// although == on it panics.
func canCompare(v reflect.Value) bool {
	return v.IsValid() && v.Type().Comparable() && v.Comparable()
}

func isNumericKind(k reflect.Kind) bool {
	switch k {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64:
		return true
	}
	return false
}

func isIntegerKind(k reflect.Kind) bool {
	switch k {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return true
	}
	return false
}

// safeNumericConvert performs a numeric conversion that rejects any value
// whose magnitude cannot be represented in the target type. Float→int
// truncates toward zero (matching Go's built-in conversion), but only if
// the truncated integer still fits in the target kind. float64→float32
// converts even if precision is lost, since that is always expected.
func safeNumericConvert(rv reflect.Value, want reflect.Type) (reflect.Value, error) {
	k := want.Kind()
	// Float targets
	if k == reflect.Float32 || k == reflect.Float64 {
		if rv.Kind() == reflect.Float32 || rv.Kind() == reflect.Float64 {
			return rv.Convert(want), nil
		}
		// int/uint → float
		return rv.Convert(want), nil
	}
	// Integer targets — compute the int64-equivalent of rv and bounds-check.
	switch rv.Kind() {
	case reflect.Float32, reflect.Float64:
		f := rv.Float()
		i, err := truncFloatToInt64(f, want.String())
		if err != nil {
			return reflect.Value{}, err
		}
		return intToKind(i, want)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		u := rv.Uint()
		if u > math.MaxInt64 && isSignedInt(k) {
			return reflect.Value{}, fmt.Errorf("uint %d overflows %v", u, want)
		}
		if isSignedInt(k) {
			return intToKind(int64(u), want)
		}
		return uintToKind(u, want)
	default:
		// Signed int sources.
		return intToKind(rv.Int(), want)
	}
}

func isSignedInt(k reflect.Kind) bool {
	switch k {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return true
	}
	return false
}

func intToKind(v int64, want reflect.Type) (reflect.Value, error) {
	switch want.Kind() {
	case reflect.Int:
		// int is 64 bits on every supported platform; the int64 range covers it.
		return reflect.ValueOf(int(v)).Convert(want), nil
	case reflect.Int8:
		if v < math.MinInt8 || v > math.MaxInt8 {
			return reflect.Value{}, fmt.Errorf("int %d out of range for int8", v)
		}
		return reflect.ValueOf(int8(v)).Convert(want), nil
	case reflect.Int16:
		if v < math.MinInt16 || v > math.MaxInt16 {
			return reflect.Value{}, fmt.Errorf("int %d out of range for int16", v)
		}
		return reflect.ValueOf(int16(v)).Convert(want), nil
	case reflect.Int32:
		if v < math.MinInt32 || v > math.MaxInt32 {
			return reflect.Value{}, fmt.Errorf("int %d out of range for int32", v)
		}
		return reflect.ValueOf(int32(v)).Convert(want), nil
	case reflect.Int64:
		return reflect.ValueOf(v).Convert(want), nil
	case reflect.Uint:
		if v < 0 {
			return reflect.Value{}, fmt.Errorf("negative int %d cannot become uint", v)
		}
		return reflect.ValueOf(uint(v)).Convert(want), nil
	case reflect.Uint8:
		if v < 0 || v > math.MaxUint8 {
			return reflect.Value{}, fmt.Errorf("int %d out of range for uint8", v)
		}
		return reflect.ValueOf(uint8(v)).Convert(want), nil
	case reflect.Uint16:
		if v < 0 || v > math.MaxUint16 {
			return reflect.Value{}, fmt.Errorf("int %d out of range for uint16", v)
		}
		return reflect.ValueOf(uint16(v)).Convert(want), nil
	case reflect.Uint32:
		if v < 0 || v > math.MaxUint32 {
			return reflect.Value{}, fmt.Errorf("int %d out of range for uint32", v)
		}
		return reflect.ValueOf(uint32(v)).Convert(want), nil
	case reflect.Uint64:
		if v < 0 {
			return reflect.Value{}, fmt.Errorf("negative int %d cannot become uint64", v)
		}
		return reflect.ValueOf(uint64(v)).Convert(want), nil
	case reflect.Uintptr:
		if v < 0 {
			return reflect.Value{}, fmt.Errorf("negative int %d cannot become uintptr", v)
		}
		return reflect.ValueOf(uintptr(v)).Convert(want), nil
	default:
		// safeNumericConvert only calls this with numeric targets, and
		// every numeric integer kind is handled above.
		panic(fmt.Sprintf("expr: intToKind called with unsupported kind %v", want.Kind()))
	}
}

func uintToKind(v uint64, want reflect.Type) (reflect.Value, error) {
	switch want.Kind() {
	case reflect.Uint:
		return reflect.ValueOf(uint(v)).Convert(want), nil
	case reflect.Uint8:
		if v > math.MaxUint8 {
			return reflect.Value{}, fmt.Errorf("uint %d out of range for uint8", v)
		}
		return reflect.ValueOf(uint8(v)).Convert(want), nil
	case reflect.Uint16:
		if v > math.MaxUint16 {
			return reflect.Value{}, fmt.Errorf("uint %d out of range for uint16", v)
		}
		return reflect.ValueOf(uint16(v)).Convert(want), nil
	case reflect.Uint32:
		if v > math.MaxUint32 {
			return reflect.Value{}, fmt.Errorf("uint %d out of range for uint32", v)
		}
		return reflect.ValueOf(uint32(v)).Convert(want), nil
	case reflect.Uint64:
		return reflect.ValueOf(v).Convert(want), nil
	case reflect.Uintptr:
		return reflect.ValueOf(uintptr(v)).Convert(want), nil
	default:
		panic(fmt.Sprintf("expr: uintToKind called with unsupported kind %v", want.Kind()))
	}
}

// --- numeric / equality / truthiness helpers ---

const int64LimitFloat = float64(uint64(1) << 63)

func truncFloatToInt64(f float64, target string) (int64, error) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("cannot convert %v to %s", f, target)
	}
	t := math.Trunc(f)
	if t >= int64LimitFloat || t < -int64LimitFloat {
		return 0, fmt.Errorf("float %v out of range for %s", f, target)
	}
	return int64(t), nil
}

// toIndexInt converts an evaluated index value to int64. Integers of
// any kind pass through. Floats are accepted only when their value is
// finite and exactly integral, matching expr's "ints and floats are
// fungible when integral" arithmetic rule. Non-integer or non-numeric
// indices return an ErrEvaluate-wrapped error suitable for surfacing
// directly to callers.
func toIndexInt(v any) (int64, error) {
	if i, ok := toInt64(v); ok {
		return i, nil
	}
	if f, ok := toFloat64(v); ok {
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, fmt.Errorf("%w: index must be integer, got %v", ErrEvaluate, f)
		}
		t := math.Trunc(f)
		if t != f {
			return 0, fmt.Errorf("%w: index must be integer, got %v", ErrEvaluate, f)
		}
		if t >= int64LimitFloat || t < -int64LimitFloat {
			return 0, fmt.Errorf("%w: index %v out of int64 range", ErrEvaluate, f)
		}
		return int64(t), nil
	}
	return 0, fmt.Errorf("%w: index must be integer, got %T", ErrEvaluate, v)
}

func mapStringKey(keyType reflect.Type, key string) reflect.Value {
	kv := reflect.ValueOf(key)
	if kv.Type().AssignableTo(keyType) {
		return kv
	}
	return kv.Convert(keyType)
}

func toInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int8:
		return int64(n), true
	case int16:
		return int64(n), true
	case int32:
		return int64(n), true
	case int64:
		return n, true
	case uint:
		if uint64(n) > uint64(math.MaxInt64) {
			return 0, false
		}
		return int64(n), true
	case uint8:
		return int64(n), true
	case uint16:
		return int64(n), true
	case uint32:
		return int64(n), true
	case uint64:
		if n > uint64(math.MaxInt64) {
			return 0, false
		}
		return int64(n), true
	case uintptr:
		if uint64(n) > uint64(math.MaxInt64) {
			return 0, false
		}
		return int64(n), true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int(), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		u := rv.Uint()
		if u > uint64(math.MaxInt64) {
			return 0, false
		}
		return int64(u), true
	}
	return 0, false
}

func toFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case float32:
		return float64(n), true
	case float64:
		return n, true
	case uint:
		return float64(n), true
	case uint8:
		return float64(n), true
	case uint16:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	case uintptr:
		return float64(n), true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Float32, reflect.Float64:
		return rv.Float(), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return float64(rv.Uint()), true
	}
	if i, ok := toInt64(v); ok {
		return float64(i), true
	}
	return 0, false
}

// looseEqual compares values across compatible numeric types and falls
// back to Go's native equality for everything else. Typed nils (e.g. a
// nil slice stored in an `any`) compare equal to the untyped `nil`
// literal, matching user expectation. The second return is false when
// the values are not comparable at all (e.g. slice == map).
func looseEqual(lhs, rhs any) (bool, bool) {
	if lhs == nil && rhs == nil {
		return true, true
	}
	if lhs == nil {
		return isNilValue(rhs), true
	}
	if rhs == nil {
		return isNilValue(lhs), true
	}
	// Two integers compare exactly, whatever their widths and
	// signedness; float64 would round above 2^53.
	if c, ok := compareIntegers(lhs, rhs); ok {
		return c == 0, true
	}
	if lf, lok := toFloat64(lhs); lok {
		if rf, rok := toFloat64(rhs); rok {
			return lf == rf, true
		}
	}
	if ls, lok := asString(lhs); lok {
		if rs, rok := asString(rhs); rok {
			return ls == rs, true
		}
	}
	if lb, lok := asBool(lhs); lok {
		if rb, rok := asBool(rhs); rok {
			return lb == rb, true
		}
	}
	// Value.Comparable, unlike Type.Comparable, also looks inside
	// interface fields and elements: a struct or array holding a slice
	// has a comparable type but panics when compared.
	lv, rv := reflect.ValueOf(lhs), reflect.ValueOf(rhs)
	if !canCompare(lv) || !canCompare(rv) {
		return false, false
	}
	if lv.Type() != rv.Type() {
		return false, true
	}
	return lhs == rhs, true
}

// asInteger reads v as an integer of any Go integer kind (including
// named types). A value above math.MaxInt64 comes back in u with big
// set; otherwise it is in i.
func asInteger(v any) (i int64, u uint64, big, ok bool) {
	switch n := v.(type) {
	case int64:
		return n, 0, false, true
	case int:
		return int64(n), 0, false, true
	case nil:
		return 0, 0, false, false
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int(), 0, false, true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		if u := rv.Uint(); u > math.MaxInt64 {
			return 0, u, true, true
		}
		return int64(rv.Uint()), 0, false, true
	}
	return 0, 0, false, false
}

// compareIntegers compares two integers exactly, returning -1, 0 or +1.
// ok is false unless both are of an integer kind.
func compareIntegers(lhs, rhs any) (int, bool) {
	li, lu, lbig, lok := asInteger(lhs)
	if !lok {
		return 0, false
	}
	ri, ru, rbig, rok := asInteger(rhs)
	if !rok {
		return 0, false
	}
	switch {
	case lbig && rbig:
		return cmpOrdered(lu, ru), true
	case lbig:
		return 1, true // above every int64
	case rbig:
		return -1, true
	}
	return cmpOrdered(li, ri), true
}

func cmpOrdered[T int64 | uint64](a, b T) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func asBool(v any) (bool, bool) {
	if b, ok := v.(bool); ok {
		return b, true
	}
	if v == nil {
		return false, false
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Bool {
		return rv.Bool(), true
	}
	return false, false
}

// isNilValue reports whether v is nil at the Go level, including typed
// nils for nilable reference kinds.
func isNilValue(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	}
	return false
}

func isTruthy(v any) bool { return IsTruthy(v) }
