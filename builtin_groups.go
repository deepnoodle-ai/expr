package expr

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"strings"
)

// MathFuncs returns the opt-in numeric helper set. Like [Builtins],
// the returned map is a fresh copy owned by the caller. Register it
// with [WithFunctions]:
//
//	expr.Compile(src, expr.WithBuiltins(), expr.WithFunctions(expr.MathFuncs()))
//
// The groups are separate from Builtins so a minimal sandbox stays
// minimal: hosts opt in to exactly the surface they want.
//
//	min(a, ...), max(a, ...)  smallest/largest argument; int64 when every
//	                          argument is integral, float64 otherwise
//	abs(n)                    absolute value; int64 in, int64 out
//	floor(v), ceil(v), round(v)  float64 results; integers pass through
func MathFuncs() map[string]any {
	return map[string]any{
		"min":   Func(nativeMin),
		"max":   Func(nativeMax),
		"abs":   Func(nativeAbs),
		"floor": Func(nativeFloor),
		"ceil":  Func(nativeCeil),
		"round": Func(nativeRound),
	}
}

// StringFuncs returns the opt-in string helper set. Like [Builtins],
// the returned map is a fresh copy owned by the caller. Register it
// with [WithFunctions].
//
//	trim(s)                strings.TrimSpace
//	split(s, sep)          list of substrings
//	join(xs, sep)          concatenate a list of strings
//	replace(s, old, new)   strings.ReplaceAll
//	startsWith(s, prefix)  strings.HasPrefix
//	endsWith(s, suffix)    strings.HasSuffix
func StringFuncs() map[string]any {
	return map[string]any{
		"trim":       Func(nativeTrim),
		"split":      Func(nativeSplit),
		"join":       Func(nativeJoin),
		"replace":    Func(nativeReplace),
		"startsWith": Func(nativeStartsWith),
		"endsWith":   Func(nativeEndsWith),
	}
}

// CollectionFuncs returns the opt-in list helper set. Like
// [Builtins], the returned map is a fresh copy owned by the caller.
// Register it with [WithFunctions].
//
//	first(xs), last(xs)  first/last element; nil for empty or nil lists
//	sum(xs)              numeric sum; int64 when every element is
//	                     integral, float64 otherwise; empty → 0
//	slice(xs, i, j)      elements [i, j) of a list, or the rune range
//	                     of a string; negative indices count from the
//	                     end and out-of-range bounds clamp
func CollectionFuncs() map[string]any {
	return map[string]any{
		"first": Func(nativeFirst),
		"last":  Func(nativeLast),
		"sum":   Func(nativeSum),
		"slice": Func(nativeSlice),
	}
}

func nativeMin(_ context.Context, args []any) (any, error) {
	return builtinMinMax("min", args, false)
}

func nativeMax(_ context.Context, args []any) (any, error) {
	return builtinMinMax("max", args, true)
}

// builtinMinMax compares all arguments, staying in int64 when every
// argument is integral and promoting the comparison to float64
// otherwise. NaN propagates (matching math.Min / math.Max).
func builtinMinMax(name string, args []any, wantMax bool) (any, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("%w: %s expects at least 1 arg, got 0", ErrEvaluate, name)
	}
	allInt := true
	for _, a := range args {
		if _, ok := toInt64(a); ok {
			continue
		}
		if _, ok := toFloat64(a); !ok {
			return nil, fmt.Errorf("%w: %s: expected number, got %T", ErrEvaluate, name, a)
		}
		allInt = false
	}
	if allInt {
		best, _ := toInt64(args[0])
		for _, a := range args[1:] {
			v, _ := toInt64(a)
			if (wantMax && v > best) || (!wantMax && v < best) {
				best = v
			}
		}
		return best, nil
	}
	best, _ := toFloat64(args[0])
	for _, a := range args[1:] {
		v, _ := toFloat64(a)
		if wantMax {
			best = math.Max(best, v)
		} else {
			best = math.Min(best, v)
		}
	}
	return best, nil
}

func nativeAbs(_ context.Context, args []any) (any, error) {
	if err := checkArity("abs", 1, len(args)); err != nil {
		return nil, err
	}
	if i, ok := toInt64(args[0]); ok {
		if i == math.MinInt64 {
			return nil, fmt.Errorf("%w: integer overflow", ErrEvaluate)
		}
		if i < 0 {
			return -i, nil
		}
		return i, nil
	}
	if f, ok := toFloat64(args[0]); ok {
		return math.Abs(f), nil
	}
	return nil, fmt.Errorf("%w: abs: expected number, got %T", ErrEvaluate, args[0])
}

func nativeFloor(_ context.Context, args []any) (any, error) {
	return builtinRounding("floor", args, math.Floor)
}

func nativeCeil(_ context.Context, args []any) (any, error) {
	return builtinRounding("ceil", args, math.Ceil)
}

func nativeRound(_ context.Context, args []any) (any, error) {
	return builtinRounding("round", args, math.Round)
}

// builtinRounding applies fn to float arguments and passes integral
// arguments through unchanged (already exact, and keeping int64 avoids
// a surprising type change for `ceil(n)` over an int env value).
func builtinRounding(name string, args []any, fn func(float64) float64) (any, error) {
	if err := checkArity(name, 1, len(args)); err != nil {
		return nil, err
	}
	if i, ok := toInt64(args[0]); ok {
		return i, nil
	}
	if f, ok := toFloat64(args[0]); ok {
		return fn(f), nil
	}
	return nil, fmt.Errorf("%w: %s: expected number, got %T", ErrEvaluate, name, args[0])
}

func nativeTrim(_ context.Context, args []any) (any, error) {
	if err := checkArity("trim", 1, len(args)); err != nil {
		return nil, err
	}
	s, ok := asString(args[0])
	if !ok {
		return nil, fmt.Errorf("%w: trim: expected string, got %T", ErrEvaluate, args[0])
	}
	return strings.TrimSpace(s), nil
}

func nativeSplit(_ context.Context, args []any) (any, error) {
	if err := checkArity("split", 2, len(args)); err != nil {
		return nil, err
	}
	s, ok := asString(args[0])
	if !ok {
		return nil, fmt.Errorf("%w: split: expected string, got %T", ErrEvaluate, args[0])
	}
	sep, ok := asString(args[1])
	if !ok {
		return nil, fmt.Errorf("%w: split: separator must be string, got %T", ErrEvaluate, args[1])
	}
	parts := strings.Split(s, sep)
	out := make([]any, len(parts))
	for i, p := range parts {
		out[i] = p
	}
	return out, nil
}

func nativeJoin(_ context.Context, args []any) (any, error) {
	if err := checkArity("join", 2, len(args)); err != nil {
		return nil, err
	}
	sep, ok := asString(args[1])
	if !ok {
		return nil, fmt.Errorf("%w: join: separator must be string, got %T", ErrEvaluate, args[1])
	}
	if args[0] == nil {
		return "", nil
	}
	rv := reflect.ValueOf(args[0])
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return nil, fmt.Errorf("%w: join: expected list, got %T", ErrEvaluate, args[0])
	}
	parts := make([]string, rv.Len())
	for i := 0; i < rv.Len(); i++ {
		elem := rv.Index(i).Interface()
		s, ok := asString(elem)
		if !ok {
			return nil, fmt.Errorf("%w: join: element %d is %T, not string", ErrEvaluate, i, elem)
		}
		parts[i] = s
	}
	return strings.Join(parts, sep), nil
}

func nativeReplace(_ context.Context, args []any) (any, error) {
	if err := checkArity("replace", 3, len(args)); err != nil {
		return nil, err
	}
	s, ok := asString(args[0])
	if !ok {
		return nil, fmt.Errorf("%w: replace: expected string, got %T", ErrEvaluate, args[0])
	}
	old, ok := asString(args[1])
	if !ok {
		return nil, fmt.Errorf("%w: replace: old must be string, got %T", ErrEvaluate, args[1])
	}
	new_, ok := asString(args[2])
	if !ok {
		return nil, fmt.Errorf("%w: replace: new must be string, got %T", ErrEvaluate, args[2])
	}
	return strings.ReplaceAll(s, old, new_), nil
}

func nativeStartsWith(_ context.Context, args []any) (any, error) {
	return builtinAffix("startsWith", args, strings.HasPrefix)
}

func nativeEndsWith(_ context.Context, args []any) (any, error) {
	return builtinAffix("endsWith", args, strings.HasSuffix)
}

func builtinAffix(name string, args []any, fn func(s, affix string) bool) (any, error) {
	if err := checkArity(name, 2, len(args)); err != nil {
		return nil, err
	}
	s, ok := asString(args[0])
	if !ok {
		return nil, fmt.Errorf("%w: %s: expected string, got %T", ErrEvaluate, name, args[0])
	}
	affix, ok := asString(args[1])
	if !ok {
		return nil, fmt.Errorf("%w: %s: expected string, got %T", ErrEvaluate, name, args[1])
	}
	return fn(s, affix), nil
}

func nativeFirst(_ context.Context, args []any) (any, error) {
	return builtinEnd("first", args, 0)
}

func nativeLast(_ context.Context, args []any) (any, error) {
	return builtinEnd("last", args, -1)
}

// builtinEnd returns the element at the front (at == 0) or back
// (at == -1) of a list, or nil when the list is nil or empty —
// mirroring how the higher-order forms treat nil as an empty list.
func builtinEnd(name string, args []any, at int) (any, error) {
	if err := checkArity(name, 1, len(args)); err != nil {
		return nil, err
	}
	if args[0] == nil {
		return nil, nil
	}
	rv := reflect.ValueOf(args[0])
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return nil, fmt.Errorf("%w: %s: expected list, got %T", ErrEvaluate, name, args[0])
	}
	if rv.Len() == 0 {
		return nil, nil
	}
	if at < 0 {
		return rv.Index(rv.Len() - 1).Interface(), nil
	}
	return rv.Index(0).Interface(), nil
}

func nativeSum(_ context.Context, args []any) (any, error) {
	if err := checkArity("sum", 1, len(args)); err != nil {
		return nil, err
	}
	if args[0] == nil {
		return int64(0), nil
	}
	rv := reflect.ValueOf(args[0])
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return nil, fmt.Errorf("%w: sum: expected list, got %T", ErrEvaluate, args[0])
	}
	var intSum int64
	var floatSum float64
	floating := false
	for i := 0; i < rv.Len(); i++ {
		elem := rv.Index(i).Interface()
		if !floating {
			if v, ok := toInt64(elem); ok {
				next, ok := checkedAddInt64(intSum, v)
				if !ok {
					return nil, fmt.Errorf("%w: integer overflow", ErrEvaluate)
				}
				intSum = next
				continue
			}
			// First non-integral element: switch to float accumulation.
			floating = true
			floatSum = float64(intSum)
		}
		v, ok := toFloat64(elem)
		if !ok {
			return nil, fmt.Errorf("%w: sum: element %d is %T, not a number", ErrEvaluate, i, elem)
		}
		floatSum += v
	}
	if floating {
		return floatSum, nil
	}
	return intSum, nil
}

// nativeSlice implements slice(xs, i, j): the half-open range [i, j)
// of a list (returned as []any) or string (rune-based, returned as
// string). Negative indices count from the end, out-of-range bounds
// clamp, and i > j yields an empty result — so slice never fails on
// range, only on type. This stands in for `xs[i:j]` syntax, which the
// language rejects by design.
func nativeSlice(_ context.Context, args []any) (any, error) {
	if err := checkArity("slice", 3, len(args)); err != nil {
		return nil, err
	}
	i, err := toIndexInt(args[1])
	if err != nil {
		return nil, err
	}
	j, err := toIndexInt(args[2])
	if err != nil {
		return nil, err
	}
	if args[0] == nil {
		return []any{}, nil
	}
	if s, ok := asString(args[0]); ok {
		runes := []rune(s)
		lo, hi := clampRange(i, j, len(runes))
		return string(runes[lo:hi]), nil
	}
	rv := reflect.ValueOf(args[0])
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return nil, fmt.Errorf("%w: slice: expected list or string, got %T", ErrEvaluate, args[0])
	}
	lo, hi := clampRange(i, j, rv.Len())
	out := make([]any, 0, hi-lo)
	for k := lo; k < hi; k++ {
		out = append(out, rv.Index(k).Interface())
	}
	return out, nil
}

// clampRange resolves negative indices against n and clamps both
// bounds into [0, n], collapsing inverted ranges to empty.
func clampRange(i, j int64, n int) (int, int) {
	lo := resolveIndex(i, n)
	hi := resolveIndex(j, n)
	if hi < lo {
		hi = lo
	}
	return lo, hi
}

func resolveIndex(i int64, n int) int {
	if i < 0 {
		i += int64(n)
	}
	if i < 0 {
		return 0
	}
	if i > int64(n) {
		return n
	}
	return int(i)
}
