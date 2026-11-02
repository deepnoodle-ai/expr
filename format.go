package expr

import (
	"encoding"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// Formatting renders host values that may be cyclic, deeply nested, or
// share structure, so it carries three bounds through every recursive
// step, including the rendering of map keys:
//
//   - cycles: a pointer, map or slice already being rendered higher up
//     renders as <cycle>;
//   - depth: a value nested more than maxFormatDepth levels renders as
//     <...> below that level, so the recursion's stack use is bounded;
//   - work: after maxFormatNodes values the rest renders as <truncated>.
//     Structure shared without a cycle (a list holding the same list
//     twice, nested 64 times) is otherwise rendered in full, which is
//     exponential in the input's size.
//
// Formatting cannot fail, so the bounds show up as markers in the text.
const (
	maxFormatDepth = 1000
	maxFormatNodes = 1 << 20
)

type visit struct {
	typ reflect.Type
	ptr uintptr
}

// formatState is the state one top-level formatting call threads through
// its recursion.
type formatState struct {
	seen  map[visit]bool
	depth int
	nodes int
}

func newFormatState() *formatState { return &formatState{seen: map[visit]bool{}} }

func safeFormatValue(v any) string {
	if v == nil {
		return "<nil>"
	}
	var b strings.Builder
	newFormatState().format(&b, reflect.ValueOf(v))
	return b.String()
}

// format renders rv. Every recursive call goes through here, so the
// depth and work bounds apply to all of them.
func (st *formatState) format(b *strings.Builder, rv reflect.Value) {
	if st.nodes >= maxFormatNodes {
		if st.nodes == maxFormatNodes {
			b.WriteString("<truncated>")
			st.nodes++
		}
		return
	}
	st.nodes++
	if st.depth >= maxFormatDepth {
		b.WriteString("<...>")
		return
	}
	st.depth++
	defer func() { st.depth-- }()
	st.formatValue(b, rv)
}

func (st *formatState) formatValue(b *strings.Builder, rv reflect.Value) {
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
		v, ok := st.enter(rv)
		if !ok {
			b.WriteString("<cycle>")
			return
		}
		defer delete(st.seen, v)
		b.WriteByte('&')
		st.format(b, rv.Elem())
	case reflect.Map:
		if rv.IsNil() {
			b.WriteString("map[]")
			return
		}
		v, ok := st.enter(rv)
		if !ok {
			b.WriteString("<cycle>")
			return
		}
		defer delete(st.seen, v)
		b.WriteString("map[")
		// Format each key once, with this map already marked as seen: a
		// key that leads back to the map renders <cycle> instead of
		// recursing, and the sort compares the rendered strings.
		keys := rv.MapKeys()
		entries := make([]mapEntry, len(keys))
		for i, k := range keys {
			var kb strings.Builder
			st.format(&kb, k)
			entries[i] = mapEntry{key: kb.String(), value: rv.MapIndex(k)}
		}
		sort.SliceStable(entries, func(i, j int) bool { return entries[i].key < entries[j].key })
		for i, e := range entries {
			if i > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(e.key)
			b.WriteByte(':')
			st.format(b, e.value)
		}
		b.WriteByte(']')
	case reflect.Slice:
		if rv.IsNil() {
			b.WriteString("[]")
			return
		}
		v, ok := st.enter(rv)
		if !ok {
			b.WriteString("<cycle>")
			return
		}
		defer delete(st.seen, v)
		st.formatList(b, rv)
	case reflect.Array:
		st.formatList(b, rv)
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
			st.format(b, f)
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

type mapEntry struct {
	key   string
	value reflect.Value
}

func (st *formatState) formatList(b *strings.Builder, rv reflect.Value) {
	b.WriteByte('[')
	for i := 0; i < rv.Len(); i++ {
		if i > 0 {
			b.WriteByte(' ')
		}
		st.format(b, rv.Index(i))
	}
	b.WriteByte(']')
}

// enter marks the reference rv as being rendered. ok is false when it
// already is: a cycle.
func (st *formatState) enter(rv reflect.Value) (visit, bool) {
	ptr := rv.Pointer()
	if ptr == 0 {
		return visit{}, true
	}
	v := visit{typ: rv.Type(), ptr: ptr}
	if st.seen[v] {
		return v, false
	}
	st.seen[v] = true
	return v, true
}

var (
	jsonMarshalerType = reflect.TypeOf((*json.Marshaler)(nil)).Elem()
	textMarshalerType = reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()
)

// jsonWithinBounds reports whether encoding/json can render v within
// the formatting bounds: no cycle, at most maxFormatDepth levels, and
// at most maxFormatNodes values. encoding/json has no work bound and
// recurses as deep as the value goes, so templates check this before
// marshaling and otherwise fall back to safeFormatValue, which applies
// the bounds itself. The walk is a superset of what json visits (it
// ignores `json:"-"`), and stops at a value with its own marshaler.
func jsonWithinBounds(v any) bool {
	st := newFormatState()
	return st.jsonWalk(reflect.ValueOf(v))
}

func (st *formatState) jsonWalk(rv reflect.Value) bool {
	st.nodes++
	if st.nodes > maxFormatNodes || st.depth >= maxFormatDepth {
		return false
	}
	if !rv.IsValid() {
		return true
	}
	t := rv.Type()
	if t.Implements(jsonMarshalerType) || t.Implements(textMarshalerType) {
		return true
	}
	st.depth++
	defer func() { st.depth-- }()
	switch rv.Kind() {
	case reflect.Interface:
		return rv.IsNil() || st.jsonWalk(rv.Elem())
	case reflect.Pointer, reflect.Map, reflect.Slice:
		if rv.IsNil() {
			return true
		}
		v, ok := st.enter(rv)
		if !ok {
			return false
		}
		defer delete(st.seen, v)
		switch rv.Kind() {
		case reflect.Pointer:
			return st.jsonWalk(rv.Elem())
		case reflect.Map:
			iter := rv.MapRange()
			for iter.Next() {
				if !st.jsonWalk(iter.Key()) || !st.jsonWalk(iter.Value()) {
					return false
				}
			}
			return true
		}
		return st.jsonWalkList(rv)
	case reflect.Array:
		return st.jsonWalkList(rv)
	case reflect.Struct:
		for i := 0; i < rv.NumField(); i++ {
			if f := rv.Field(i); f.CanInterface() && !st.jsonWalk(f) {
				return false
			}
		}
	}
	return true
}

func (st *formatState) jsonWalkList(rv reflect.Value) bool {
	for i := 0; i < rv.Len(); i++ {
		if !st.jsonWalk(rv.Index(i)) {
			return false
		}
	}
	return true
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
