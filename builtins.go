package expr

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Builtins returns a fresh copy of the default builtin function set made
// available to every expression. The returned map is owned by the caller
// and safe to mutate.
//
// The defaults are chosen to be small, deterministic, side-effect free,
// and useful for typical condition/template work:
//
//	len(v)            rune count for strings; element count for
//	                  slice/array/map/chan
//	string(v)         stringified form of v
//	int(v)            numeric conversion to int64; strings parse strictly
//	                  as base-10 integers
//	float(v)          numeric conversion to float64; strings parse strictly
//	bool(v)           truthiness check (matches IsTruthy)
//	contains(h, n)    substring for strings, element membership for
//	                  slices/arrays (using loose numeric equality), or
//	                  key presence for string-keyed maps
//	has(m, k)         true if map m has key k; errors if m is not a map
//	keys(m)           sorted string keys of a map
//	lower(s), upper(s)  case conversion
//	sprintf(fmt, ...) fmt.Sprintf-style formatting with cycle guards
//
// if(cond, then, else) is not in this map: it is a special form
// (always available, lazily evaluated) — see higher_order.go. Opt-in
// extension sets live in [MathFuncs], [StringFuncs], and
// [CollectionFuncs].
func Builtins() map[string]any {
	return map[string]any{
		"len":      Func(nativeLen),
		"string":   Func(nativeString),
		"int":      Func(nativeInt),
		"float":    Func(nativeFloat),
		"bool":     Func(nativeBool),
		"contains": Func(nativeContains),
		"has":      Func(nativeHas),
		"keys":     Func(nativeKeys),
		"lower":    Func(nativeLower),
		"upper":    Func(nativeUpper),
		"sprintf":  Func(nativeSprintf),
	}
}

// checkArity returns a uniform error for builtin arity mismatches.
func checkArity(name string, want, got int) error {
	if want == got {
		return nil
	}
	return fmt.Errorf("%w: %s: expected %d args, got %d", ErrEvaluate, name, want, got)
}

func nativeLen(_ context.Context, args []any) (any, error) {
	if err := checkArity("len", 1, len(args)); err != nil {
		return nil, err
	}
	return builtinLen(args[0])
}

func nativeString(_ context.Context, args []any) (any, error) {
	if err := checkArity("string", 1, len(args)); err != nil {
		return nil, err
	}
	return builtinString(args[0]), nil
}

func nativeInt(_ context.Context, args []any) (any, error) {
	if err := checkArity("int", 1, len(args)); err != nil {
		return nil, err
	}
	return builtinInt(args[0])
}

func nativeFloat(_ context.Context, args []any) (any, error) {
	if err := checkArity("float", 1, len(args)); err != nil {
		return nil, err
	}
	return builtinFloat(args[0])
}

func nativeBool(_ context.Context, args []any) (any, error) {
	if err := checkArity("bool", 1, len(args)); err != nil {
		return nil, err
	}
	return IsTruthy(args[0]), nil
}

func nativeContains(_ context.Context, args []any) (any, error) {
	if err := checkArity("contains", 2, len(args)); err != nil {
		return nil, err
	}
	return builtinContains(args[0], args[1])
}

func nativeHas(_ context.Context, args []any) (any, error) {
	if err := checkArity("has", 2, len(args)); err != nil {
		return nil, err
	}
	return builtinHas(args[0], args[1])
}

func nativeKeys(_ context.Context, args []any) (any, error) {
	if err := checkArity("keys", 1, len(args)); err != nil {
		return nil, err
	}
	return builtinKeys(args[0])
}

func nativeLower(_ context.Context, args []any) (any, error) {
	if err := checkArity("lower", 1, len(args)); err != nil {
		return nil, err
	}
	s, ok := asString(args[0])
	if !ok {
		return nil, fmt.Errorf("%w: lower: expected string, got %T", ErrEvaluate, args[0])
	}
	return strings.ToLower(s), nil
}

func nativeUpper(_ context.Context, args []any) (any, error) {
	if err := checkArity("upper", 1, len(args)); err != nil {
		return nil, err
	}
	s, ok := asString(args[0])
	if !ok {
		return nil, fmt.Errorf("%w: upper: expected string, got %T", ErrEvaluate, args[0])
	}
	return strings.ToUpper(s), nil
}

func nativeSprintf(_ context.Context, args []any) (any, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("%w: sprintf expects at least 1 arg, got 0", ErrEvaluate)
	}
	format, ok := asString(args[0])
	if !ok {
		return nil, fmt.Errorf("%w: sprintf: format must be string, got %T", ErrEvaluate, args[0])
	}
	wrapped := make([]any, len(args)-1)
	for i, arg := range args[1:] {
		wrapped[i] = safeFormatArg{v: arg}
	}
	return fmt.Sprintf(format, wrapped...), nil
}

func builtinLen(v any) (int, error) {
	if v == nil {
		return 0, nil
	}
	if s, ok := v.(string); ok {
		return utf8.RuneCountInString(s), nil
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.String:
		return utf8.RuneCountInString(rv.String()), nil
	case reflect.Slice, reflect.Array, reflect.Map, reflect.Chan:
		return rv.Len(), nil
	}
	return 0, fmt.Errorf("%w: len: unsupported type %T", ErrEvaluate, v)
}

func builtinString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return safeFormatValue(v)
}

func builtinInt(v any) (int64, error) {
	if v == nil {
		return 0, nil
	}
	if i, ok := toInt64(v); ok {
		return i, nil
	}
	if f, ok := toFloat64(v); ok {
		i, err := truncFloatToInt64(f, "int")
		if err != nil {
			return 0, fmt.Errorf("%w: int: %v", ErrEvaluate, err)
		}
		return i, nil
	}
	if s, ok := asString(v); ok {
		i, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("%w: int: cannot parse %q", ErrEvaluate, s)
		}
		return i, nil
	}
	return 0, fmt.Errorf("%w: int: unsupported type %T", ErrEvaluate, v)
}

func builtinFloat(v any) (float64, error) {
	if v == nil {
		return 0, nil
	}
	if f, ok := toFloat64(v); ok {
		return f, nil
	}
	if s, ok := asString(v); ok {
		f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return 0, fmt.Errorf("%w: float: cannot parse %q", ErrEvaluate, s)
		}
		return f, nil
	}
	return 0, fmt.Errorf("%w: float: unsupported type %T", ErrEvaluate, v)
}

func builtinContains(haystack, needle any) (bool, error) {
	if haystack == nil {
		return false, nil
	}
	if s, ok := asString(haystack); ok {
		sub, ok := asString(needle)
		if !ok {
			return false, fmt.Errorf("%w: contains: needle must be string for string haystack, got %T", ErrEvaluate, needle)
		}
		return strings.Contains(s, sub), nil
	}
	rv := reflect.ValueOf(haystack)
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		for i := 0; i < rv.Len(); i++ {
			eq, _ := looseEqual(rv.Index(i).Interface(), needle)
			if eq {
				return true, nil
			}
		}
		return false, nil
	case reflect.Map:
		if rv.Type().Key().Kind() != reflect.String {
			return false, fmt.Errorf("%w: contains: map key must be string", ErrEvaluate)
		}
		key, ok := asString(needle)
		if !ok {
			return false, fmt.Errorf("%w: contains: map lookup needs string needle, got %T", ErrEvaluate, needle)
		}
		return rv.MapIndex(mapStringKey(rv.Type().Key(), key)).IsValid(), nil
	}
	return false, fmt.Errorf("%w: contains: unsupported haystack type %T", ErrEvaluate, haystack)
}

func builtinHas(m, key any) (bool, error) {
	if m == nil {
		return false, nil
	}
	rv := reflect.ValueOf(m)
	if rv.Kind() != reflect.Map {
		return false, fmt.Errorf("%w: has: expected map, got %T", ErrEvaluate, m)
	}
	if rv.Type().Key().Kind() != reflect.String {
		return false, fmt.Errorf("%w: has: map key must be string", ErrEvaluate)
	}
	k, ok := asString(key)
	if !ok {
		return false, fmt.Errorf("%w: has: key must be string, got %T", ErrEvaluate, key)
	}
	return rv.MapIndex(mapStringKey(rv.Type().Key(), k)).IsValid(), nil
}

func asString(v any) (string, bool) {
	if v == nil {
		return "", false
	}
	if s, ok := v.(string); ok {
		return s, true
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.String {
		return rv.String(), true
	}
	return "", false
}

func builtinKeys(m any) ([]any, error) {
	if m == nil {
		return nil, nil
	}
	rv := reflect.ValueOf(m)
	if rv.Kind() != reflect.Map || rv.Type().Key().Kind() != reflect.String {
		return nil, fmt.Errorf("%w: keys: expected map with string keys, got %T", ErrEvaluate, m)
	}
	mapKeys := rv.MapKeys()
	strs := make([]string, len(mapKeys))
	for i, k := range mapKeys {
		strs[i] = k.String()
	}
	sort.Strings(strs)
	out := make([]any, len(strs))
	for i, s := range strs {
		out[i] = s
	}
	return out, nil
}
