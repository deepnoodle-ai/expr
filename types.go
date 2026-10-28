package expr

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Kind is the shape of a Type.
type Kind int

const (
	// KindAny is a value the checker knows nothing about. Every operation
	// on it is allowed and produces Any.
	KindAny Kind = iota
	KindNull
	KindBool
	KindNumber
	KindString
	KindList
	KindObject
)

// Type describes the values an expression may produce, for static checking
// with [Program.Check]. Build types with the constructors ([String],
// [ListOf], [ObjectOf], ...) or as literals.
//
// Types are structural and deliberately small: they catch misspelled
// fields, operations on the wrong kind of value, comparisons that can
// never be true, and reads of values that may be nil.
type Type struct {
	Kind Kind
	// Elem is the element type of a list. For an open object it is the
	// type of fields not listed in Fields (nil means Any).
	Elem *Type
	// Fields are an object's known fields.
	Fields map[string]*Type
	// Open allows fields beyond Fields. Selecting an unlisted field of a
	// closed object is an error.
	Open bool
	// Optional means the value may be nil.
	Optional bool
	// Enum, if set, lists every value the type can hold.
	Enum []any
	// Variants, on a KindAny type, are the known types of different kinds
	// the value may have, as Join produces for `x || 0` when x is a
	// string. Operations that need one kind report a union.
	Variants []*Type
}

// Any is the type of values the checker knows nothing about.
var Any = &Type{Kind: KindAny}

func Null() *Type   { return &Type{Kind: KindNull} }
func Bool() *Type   { return &Type{Kind: KindBool} }
func Number() *Type { return &Type{Kind: KindNumber} }
func String() *Type { return &Type{Kind: KindString} }

// ListOf is a list whose elements have type elem.
func ListOf(elem *Type) *Type { return &Type{Kind: KindList, Elem: elem} }

// ObjectOf is a closed object with exactly the given fields.
func ObjectOf(fields map[string]*Type) *Type { return &Type{Kind: KindObject, Fields: fields} }

// OpenObject is an object that has the given fields and possibly others.
func OpenObject(fields map[string]*Type) *Type {
	return &Type{Kind: KindObject, Fields: fields, Open: true}
}

// EnumOf is a string type limited to the given values.
func EnumOf(values ...string) *Type {
	t := String()
	for _, v := range values {
		t.Enum = append(t.Enum, v)
	}
	return t
}

// OrNil returns a copy of t that may also be nil.
func (t *Type) OrNil() *Type {
	if t.isAny() || t.Kind == KindNull {
		return t
	}
	c := *t
	c.Optional = true
	return &c
}

// NonNil returns a copy of t that is never nil.
func (t *Type) NonNil() *Type {
	if t == nil || !t.Optional {
		return t
	}
	c := *t
	c.Optional = false
	return &c
}

// isAny reports whether t is unknown. A union is not: its variants are
// known, and each must allow an operation.
func (t *Type) isAny() bool { return t == nil || (t.Kind == KindAny && len(t.Variants) == 0) }

// IsUnion reports whether t is a union of known types of different kinds.
func (t *Type) IsUnion() bool { return t != nil && t.Kind == KindAny && len(t.Variants) > 0 }

// String renders the type, for example `{kind: "bug_fix" | "feature", items: list<string>}?`.
func (t *Type) String() string {
	if t == nil {
		return "any"
	}
	var s string
	switch t.Kind {
	case KindAny:
		if len(t.Variants) == 0 {
			return "any"
		}
		parts := make([]string, len(t.Variants))
		for i, v := range t.Variants {
			parts[i] = v.String()
		}
		s = strings.Join(parts, " | ")
		if t.Optional {
			return "(" + s + ")?"
		}
		return s
	case KindNull:
		return "nil"
	case KindBool:
		s = "bool"
	case KindNumber:
		s = "number"
	case KindString:
		s = "string"
	case KindList:
		s = "list<" + t.Elem.String() + ">"
	case KindObject:
		names := make([]string, 0, len(t.Fields))
		for k := range t.Fields {
			names = append(names, k)
		}
		sort.Strings(names)
		parts := make([]string, 0, len(names)+1)
		for _, k := range names {
			parts = append(parts, k+": "+t.Fields[k].String())
		}
		if t.Open {
			parts = append(parts, "...")
		}
		s = "{" + strings.Join(parts, ", ") + "}"
	}
	if len(t.Enum) > 0 {
		vals := make([]string, len(t.Enum))
		for i, v := range t.Enum {
			vals[i] = formatEnumValue(v)
		}
		s = strings.Join(vals, " | ")
		if t.Optional && len(t.Enum) > 1 {
			s = "(" + s + ")"
		}
	}
	if t.Optional {
		s += "?"
	}
	return s
}

func formatEnumValue(v any) string {
	if s, ok := v.(string); ok {
		return strconv.Quote(s)
	}
	return fmt.Sprint(v)
}

func (t *Type) hasEnumValue(v any) bool {
	for _, e := range t.Enum {
		if enumEqual(e, v) {
			return true
		}
	}
	return false
}

func enumEqual(a, b any) bool {
	if af, ok := enumFloat(a); ok {
		bf, ok := enumFloat(b)
		return ok && af == bf
	}
	return a == b
}

func enumFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case float64:
		return n, true
	}
	return 0, false
}

// union joins types of different kinds, merging those of the same kind.
func union(a, b *Type) *Type {
	var parts []*Type
	optional := false
	for _, t := range []*Type{a, b} {
		optional = optional || t.Optional
		if t.IsUnion() {
			parts = append(parts, t.Variants...)
		} else {
			parts = append(parts, t.NonNil())
		}
	}
	var out []*Type
	for _, p := range parts {
		merged := false
		for i, o := range out {
			if o.Kind == p.Kind {
				out[i] = Join(o, p)
				merged = true
				break
			}
		}
		if !merged {
			out = append(out, p.NonNil())
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	if len(out) == 1 {
		if optional {
			return out[0].OrNil()
		}
		return out[0]
	}
	return &Type{Kind: KindAny, Variants: out, Optional: optional}
}

func orAny(t *Type) *Type {
	if t == nil {
		return Any
	}
	return t
}

// Join is the type of a value that has either type a or type b.
func Join(a, b *Type) *Type {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	case a.isAny() || b.isAny():
		return Any
	case a.Kind == KindNull:
		return b.OrNil()
	case b.Kind == KindNull:
		return a.OrNil()
	case a.IsUnion() || b.IsUnion() || a.Kind != b.Kind:
		return union(a, b)
	}
	out := &Type{Kind: a.Kind, Optional: a.Optional || b.Optional}
	switch a.Kind {
	case KindList:
		out.Elem = Join(orAny(a.Elem), orAny(b.Elem))
	case KindObject:
		out.Open = a.Open || b.Open
		out.Fields = map[string]*Type{}
		for k, at := range a.Fields {
			// A nil field type is Any, not "no type yet".
			if bt, ok := b.Fields[k]; ok {
				out.Fields[k] = Join(orAny(at), orAny(bt))
			} else {
				out.Fields[k] = orAny(at).OrNil()
			}
		}
		for k, bt := range b.Fields {
			if _, ok := a.Fields[k]; !ok {
				out.Fields[k] = orAny(bt).OrNil()
			}
		}
		switch {
		case a.Open && b.Open:
			out.Elem = Join(orAny(a.Elem), orAny(b.Elem))
		case a.Open:
			out.Elem = a.Elem
		case b.Open:
			out.Elem = b.Elem
		}
	}
	if len(a.Enum) > 0 && len(b.Enum) > 0 {
		out.Enum = append([]any(nil), a.Enum...)
		for _, v := range b.Enum {
			if !out.hasEnumValue(v) {
				out.Enum = append(out.Enum, v)
			}
		}
	}
	return out
}
