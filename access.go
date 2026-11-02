package expr

import (
	"fmt"
	"reflect"
)

// This file holds the one implementation of field selection (`x.y`) and
// indexing (`x[i]`) on Go values. Every evaluation path goes through it:
// the general evaluator (selectField, indexValue), the reflect-space
// chain fast paths (selectFieldRV, indexValueRV), and optional access
// (trySelectName, tryIndexValue). The rules, also stated in the spec
// under "Selectors" and "Optional access":
//
//   - The receiver is unwrapped through interfaces and pointers, at any
//     depth. A nil met on the way is a nil receiver.
//   - A nil receiver is an error, or nil under optional access.
//   - A missing field or key, or an out-of-range index, is an error, or
//     nil under optional access.
//   - Any other failure (selecting on a non-struct, non-map value; an
//     index of the wrong kind; an unhashable key) is an error in both
//     modes.
//
// The wrappers keep a type-asserted map[string]any branch in front of
// the reflect code because it is the hot case; it gives the same
// results as the reflect branch.

// accessMode selects how a missing value is reported.
type accessMode bool

const (
	strictAccess   accessMode = false // missing → ErrEvaluate
	optionalAccess accessMode = true  // missing → nil (`?.`, `?[`)
)

// maxPointerHops bounds unwrapReceiver so a self-referential pointer
// type (`type P *P`) cannot loop forever.
const maxPointerHops = 64

// unwrapReceiver follows interfaces and pointers from rv to the value
// they lead to. It returns an invalid Value when the chain ends at nil;
// nilPtr then reports whether that nil was a pointer (for the error
// message). A chain longer than maxPointerHops is returned as is, and
// its pointer kind then fails as an unsupported receiver.
func unwrapReceiver(rv reflect.Value) (out reflect.Value, nilPtr bool) {
	for hops := 0; rv.IsValid() && hops < maxPointerHops; hops++ {
		switch rv.Kind() {
		case reflect.Interface:
			if rv.IsNil() {
				return reflect.Value{}, false
			}
			rv = rv.Elem()
		case reflect.Pointer:
			if rv.IsNil() {
				return reflect.Value{}, true
			}
			rv = rv.Elem()
		default:
			return rv, false
		}
	}
	return rv, false
}

// boxValue materializes rv as an any; an invalid Value is nil.
func boxValue(rv reflect.Value) any {
	if !rv.IsValid() {
		return nil
	}
	return rv.Interface()
}

// selectRV looks up the field or string key name on rv. Under
// optionalAccess an invalid Value with a nil error means nil.
func selectRV(rv reflect.Value, name string, fieldTags *structTagConfig, mode accessMode) (reflect.Value, error) {
	rv, nilPtr := unwrapReceiver(rv)
	if !rv.IsValid() {
		if mode == optionalAccess {
			return reflect.Value{}, nil
		}
		if nilPtr {
			return reflect.Value{}, fmt.Errorf("%w: cannot access %q on nil pointer", ErrEvaluate, name)
		}
		return reflect.Value{}, fmt.Errorf("%w: cannot access %q on nil", ErrEvaluate, name)
	}
	switch rv.Kind() {
	case reflect.Struct:
		fv, ok, err := structFieldByName(rv, name, fieldTags)
		if err != nil {
			return reflect.Value{}, err
		}
		// An unexported field is treated as missing rather than
		// panicking in Interface().
		if ok && fv.IsValid() && fv.CanInterface() {
			return fv, nil
		}
		if mode == optionalAccess {
			return reflect.Value{}, nil
		}
		return reflect.Value{}, fmt.Errorf("%w: field %q not found on %v%s",
			ErrEvaluate, name, rv.Type(), fieldHint(rv.Interface(), name, fieldTags))
	case reflect.Map:
		if rv.Type().Key().Kind() != reflect.String {
			return reflect.Value{}, fmt.Errorf("%w: cannot select %q on map with non-string keys", ErrEvaluate, name)
		}
		mv := rv.MapIndex(mapStringKey(rv.Type().Key(), name))
		if mv.IsValid() {
			return mv, nil
		}
		if mode == optionalAccess {
			return reflect.Value{}, nil
		}
		return reflect.Value{}, fmt.Errorf("%w: key %q not found%s",
			ErrEvaluate, name, fieldHint(rv.Interface(), name, fieldTags))
	}
	return reflect.Value{}, fmt.Errorf("%w: cannot select %q on %v", ErrEvaluate, name, rv.Type())
}

// indexRV indexes rv by idx. Under optionalAccess an invalid Value with
// a nil error means nil.
func indexRV(rv reflect.Value, idx any, mode accessMode) (reflect.Value, error) {
	rv, _ = unwrapReceiver(rv)
	if !rv.IsValid() {
		if mode == optionalAccess {
			return reflect.Value{}, nil
		}
		return reflect.Value{}, fmt.Errorf("%w: cannot index nil", ErrEvaluate)
	}
	if rv.Type() == mapStringAnyType {
		key, ok := idx.(string)
		if !ok {
			return reflect.Value{}, fmt.Errorf("%w: map index must be string, got %T", ErrEvaluate, idx)
		}
		mv := rv.MapIndex(reflect.ValueOf(key))
		if mv.IsValid() {
			return mv, nil
		}
		if mode == optionalAccess {
			return reflect.Value{}, nil
		}
		return reflect.Value{}, fmt.Errorf("%w: key %q not found%s",
			ErrEvaluate, key, fieldHint(rv.Interface(), key, nil))
	}
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		i, err := toIndexInt(idx)
		if err != nil {
			return reflect.Value{}, err
		}
		if i >= 0 && i < int64(rv.Len()) {
			return rv.Index(int(i)), nil
		}
		if mode == optionalAccess {
			return reflect.Value{}, nil
		}
		return reflect.Value{}, fmt.Errorf("%w: index %d out of range [0, %d)", ErrEvaluate, i, rv.Len())
	case reflect.String:
		i, err := toIndexInt(idx)
		if err != nil {
			return reflect.Value{}, err
		}
		runes := []rune(rv.String())
		if i >= 0 && i < int64(len(runes)) {
			return reflect.ValueOf(string(runes[i])), nil
		}
		if mode == optionalAccess {
			return reflect.Value{}, nil
		}
		return reflect.Value{}, fmt.Errorf("%w: index %d out of range [0, %d)", ErrEvaluate, i, len(runes))
	case reflect.Map:
		kv, err := mapKey(rv.Type().Key(), idx)
		if err != nil {
			return reflect.Value{}, err
		}
		mv := rv.MapIndex(kv)
		if mv.IsValid() {
			return mv, nil
		}
		if mode == optionalAccess {
			return reflect.Value{}, nil
		}
		return reflect.Value{}, fmt.Errorf("%w: key %v not found", ErrEvaluate, idx)
	}
	return reflect.Value{}, fmt.Errorf("%w: cannot index %v", ErrEvaluate, rv.Type())
}

// mapStringAnyType is the hot map type, given its own branch in indexRV
// so its errors name the key the way the type-asserted wrappers do.
var mapStringAnyType = reflect.TypeOf(map[string]any(nil))

// selectField is `recv.name`.
func selectField(recv any, name string, fieldTags *structTagConfig) (any, error) {
	// Translate rewritten identifiers (currently only `map`) back to
	// their user-visible form so field/key/error lookups match what
	// the user actually typed.
	name = displayIdent(name)
	if m, ok := recv.(map[string]any); ok {
		v, ok := m[name]
		if !ok {
			return nil, fmt.Errorf("%w: key %q not found%s",
				ErrEvaluate, name, fieldHint(recv, name, fieldTags))
		}
		return v, nil
	}
	v, err := selectRV(reflect.ValueOf(recv), name, fieldTags, strictAccess)
	if err != nil {
		return nil, err
	}
	return boxValue(v), nil
}

// selectFieldRV is selectField for a receiver already in reflect space,
// so a chain of hops never boxes an intermediate struct.
func selectFieldRV(rv reflect.Value, name string, fieldTags *structTagConfig) (reflect.Value, error) {
	return selectRV(rv, name, fieldTags, strictAccess)
}

// trySelectName is `recv?.name` once recv is known to be non-nil.
func trySelectName(recv any, name string, fieldTags *structTagConfig) (any, error) {
	if m, ok := recv.(map[string]any); ok {
		return m[name], nil
	}
	v, err := selectRV(reflect.ValueOf(recv), name, fieldTags, optionalAccess)
	if err != nil {
		return nil, err
	}
	return boxValue(v), nil
}

// indexValue is `recv[idx]`.
func indexValue(recv, idx any) (any, error) {
	if m, ok := recv.(map[string]any); ok {
		key, ok := idx.(string)
		if !ok {
			return nil, fmt.Errorf("%w: map index must be string, got %T", ErrEvaluate, idx)
		}
		v, ok := m[key]
		if !ok {
			return nil, fmt.Errorf("%w: key %q not found%s",
				ErrEvaluate, key, fieldHint(recv, key, nil))
		}
		return v, nil
	}
	v, err := indexRV(reflect.ValueOf(recv), idx, strictAccess)
	if err != nil {
		return nil, err
	}
	return boxValue(v), nil
}

// indexValueRV is indexValue for a receiver already in reflect space.
func indexValueRV(rv reflect.Value, idx any) (reflect.Value, error) {
	return indexRV(rv, idx, strictAccess)
}

// tryIndexValue is `recv?[idx]` once recv is known to be non-nil.
func tryIndexValue(recv, idx any) (any, error) {
	if m, ok := recv.(map[string]any); ok {
		key, ok := idx.(string)
		if !ok {
			return nil, fmt.Errorf("%w: map index must be string, got %T", ErrEvaluate, idx)
		}
		return m[key], nil
	}
	v, err := indexRV(reflect.ValueOf(recv), idx, optionalAccess)
	if err != nil {
		return nil, err
	}
	return boxValue(v), nil
}

// lookupEnv resolves a top-level identifier against env. env may be a
// map[string]any, any map with string keys, a struct, a pointer to a
// struct, or an *itEnv wrapping one of the above. For structs, configured
// field tags participate in field lookup, and fields are preferred over
// methods when both match. Methods are returned as bound function values
// so they can be invoked by a CallExpr node. An itEnv binds its element
// name (`it`, or the named binding of a three-arg form) and `index`
// ahead of anything in its parent.
func lookupEnv(env any, name string, fieldTags *structTagConfig) (any, bool, error) {
	if m, ok := env.(map[string]any); ok {
		v, ok := m[name]
		return v, ok, nil
	}
	rv, ok, err := lookupEnvRV(env, name, fieldTags)
	if err != nil || !ok {
		return nil, ok, err
	}
	return boxValue(rv), true, nil
}

// lookupEnvRV is lookupEnv returning a reflect.Value, so callers can
// chain field accesses without boxing intermediate struct values.
func lookupEnvRV(env any, name string, fieldTags *structTagConfig) (reflect.Value, bool, error) {
	if env == nil {
		return reflect.Value{}, false, nil
	}
	if it, ok := env.(*itEnv); ok {
		switch name {
		case it.elementName():
			return reflect.ValueOf(it.it), true, nil
		case "index":
			return reflect.ValueOf(it.index), true, nil
		}
		return lookupEnvRV(it.parent, name, fieldTags)
	}
	if m, ok := env.(map[string]any); ok {
		v, ok := m[name]
		if !ok {
			return reflect.Value{}, false, nil
		}
		return reflect.ValueOf(v), true, nil
	}
	rv := reflect.ValueOf(env)
	// Methods are looked up on the original (possibly pointer) value so
	// pointer-receiver methods are visible, but a struct field of the
	// same name wins.
	orig := rv
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return reflect.Value{}, false, nil
		}
		rv = rv.Elem()
	}
	switch rv.Kind() {
	case reflect.Struct:
		fv, ok, err := structFieldByName(rv, name, fieldTags)
		if err != nil {
			return reflect.Value{}, false, err
		}
		if ok && fv.CanInterface() {
			return fv, true, nil
		}
		if mv := orig.MethodByName(name); mv.IsValid() {
			return reflect.ValueOf(methodCallable(name, mv)), true, nil
		}
	case reflect.Map:
		if rv.Type().Key().Kind() == reflect.String {
			mv := rv.MapIndex(mapStringKey(rv.Type().Key(), name))
			if mv.IsValid() {
				return mv, true, nil
			}
		}
	}
	return reflect.Value{}, false, nil
}
