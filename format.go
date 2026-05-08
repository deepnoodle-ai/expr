package expr

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

type visit struct {
	typ reflect.Type
	ptr uintptr
}

func safeFormatValue(v any) string {
	if v == nil {
		return "<nil>"
	}
	var b strings.Builder
	formatValue(&b, reflect.ValueOf(v), map[visit]bool{})
	return b.String()
}

func formatValue(b *strings.Builder, rv reflect.Value, seen map[visit]bool) {
	if !rv.IsValid() {
		b.WriteString("<nil>")
		return
	}
	for rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			b.WriteString("<nil>")
			return
		}
		rv = rv.Elem()
	}

	switch rv.Kind() {
	case reflect.Bool:
		b.WriteString(strconv.FormatBool(rv.Bool()))
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		b.WriteString(strconv.FormatInt(rv.Int(), 10))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		b.WriteString(strconv.FormatUint(rv.Uint(), 10))
	case reflect.Float32:
		b.WriteString(strconv.FormatFloat(rv.Float(), 'g', -1, 32))
	case reflect.Float64:
		b.WriteString(strconv.FormatFloat(rv.Float(), 'g', -1, 64))
	case reflect.Complex64, reflect.Complex128:
		b.WriteString(fmt.Sprint(rv.Interface()))
	case reflect.String:
		b.WriteString(rv.String())
	case reflect.Pointer:
		if rv.IsNil() {
			b.WriteString("<nil>")
			return
		}
		v, ok := enterVisit(rv, seen)
		if !ok {
			b.WriteString("<cycle>")
			return
		}
		defer delete(seen, v)
		b.WriteByte('&')
		formatValue(b, rv.Elem(), seen)
	case reflect.Map:
		if rv.IsNil() {
			b.WriteString("map[]")
			return
		}
		v, ok := enterVisit(rv, seen)
		if !ok {
			b.WriteString("<cycle>")
			return
		}
		defer delete(seen, v)
		b.WriteString("map[")
		keys := rv.MapKeys()
		sort.Slice(keys, func(i, j int) bool {
			return safeFormatValue(keys[i].Interface()) < safeFormatValue(keys[j].Interface())
		})
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(' ')
			}
			formatValue(b, k, seen)
			b.WriteByte(':')
			formatValue(b, rv.MapIndex(k), seen)
		}
		b.WriteByte(']')
	case reflect.Slice:
		if rv.IsNil() {
			b.WriteString("[]")
			return
		}
		v, ok := enterVisit(rv, seen)
		if !ok {
			b.WriteString("<cycle>")
			return
		}
		defer delete(seen, v)
		formatList(b, rv, seen)
	case reflect.Array:
		formatList(b, rv, seen)
	case reflect.Struct:
		b.WriteByte('{')
		for i := 0; i < rv.NumField(); i++ {
			if i > 0 {
				b.WriteByte(' ')
			}
			f := rv.Field(i)
			if !f.CanInterface() {
				b.WriteString("<unexported>")
				continue
			}
			formatValue(b, f, seen)
		}
		b.WriteByte('}')
	case reflect.Chan, reflect.Func, reflect.UnsafePointer:
		b.WriteString(fmt.Sprint(rv.Interface()))
	default:
		if rv.CanInterface() {
			b.WriteString(fmt.Sprint(rv.Interface()))
			return
		}
		b.WriteString("<unexported>")
	}
}

func formatList(b *strings.Builder, rv reflect.Value, seen map[visit]bool) {
	b.WriteByte('[')
	for i := 0; i < rv.Len(); i++ {
		if i > 0 {
			b.WriteByte(' ')
		}
		formatValue(b, rv.Index(i), seen)
	}
	b.WriteByte(']')
}

func enterVisit(rv reflect.Value, seen map[visit]bool) (visit, bool) {
	ptr := rv.Pointer()
	if ptr == 0 {
		return visit{}, true
	}
	v := visit{typ: rv.Type(), ptr: ptr}
	if seen[v] {
		return v, false
	}
	seen[v] = true
	return v, true
}

type safeFormatArg struct {
	v any
}

func (a safeFormatArg) Format(s fmt.State, verb rune) {
	if isRecursiveFormatKind(a.v) {
		switch verb {
		case 'v', 's':
			fmt.Fprint(s, safeFormatValue(a.v))
		case 'q':
			fmt.Fprintf(s, formatDirective(s, verb), safeFormatValue(a.v))
		case 'T':
			if a.v == nil {
				fmt.Fprint(s, "<nil>")
				return
			}
			fmt.Fprint(s, reflect.TypeOf(a.v).String())
		default:
			fmt.Fprintf(s, "%%!%c(%T=%s)", verb, a.v, safeFormatValue(a.v))
		}
		return
	}
	fmt.Fprintf(s, formatDirective(s, verb), a.v)
}

func isRecursiveFormatKind(v any) bool {
	if v == nil {
		return false
	}
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			return false
		}
		rv = rv.Elem()
	}
	switch rv.Kind() {
	case reflect.Map, reflect.Pointer, reflect.Slice, reflect.Array, reflect.Struct:
		return true
	}
	return false
}

func formatDirective(s fmt.State, verb rune) string {
	var b strings.Builder
	b.WriteByte('%')
	for _, f := range "#+- 0" {
		if s.Flag(int(f)) {
			b.WriteRune(f)
		}
	}
	if w, ok := s.Width(); ok {
		b.WriteString(strconv.Itoa(w))
	}
	if p, ok := s.Precision(); ok {
		b.WriteByte('.')
		b.WriteString(strconv.Itoa(p))
	}
	b.WriteRune(verb)
	return b.String()
}
