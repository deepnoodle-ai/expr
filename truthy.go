package expr

import (
	"reflect"
	"strings"
)

// IsTruthy reports whether a Go value should be treated as truthy in
// expr conditionals. The rules are:
//
//   - bool: itself
//   - numeric: non-zero is truthy
//   - string: non-empty and not "false" (case-insensitive)
//   - slices/arrays/maps: non-empty is truthy
//   - nil: false
//   - anything else: non-nil is truthy
//
// This is also exposed as the bool() builtin.
func IsTruthy(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case bool:
		return v
	case int:
		return v != 0
	case int8:
		return v != 0
	case int16:
		return v != 0
	case int32:
		return v != 0
	case int64:
		return v != 0
	case uint:
		return v != 0
	case uint8:
		return v != 0
	case uint16:
		return v != 0
	case uint32:
		return v != 0
	case uint64:
		return v != 0
	case float32:
		return v != 0
	case float64:
		return v != 0
	case string:
		return v != "" && !strings.EqualFold(v, "false")
	}
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Bool:
		return rv.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int() != 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return rv.Uint() != 0
	case reflect.Float32, reflect.Float64:
		return rv.Float() != 0
	case reflect.String:
		s := rv.String()
		return s != "" && !strings.EqualFold(s, "false")
	case reflect.Slice, reflect.Array, reflect.Map:
		return rv.Len() > 0
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Pointer:
		return !rv.IsNil()
	}
	return value != nil
}
