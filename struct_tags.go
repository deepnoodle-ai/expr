package expr

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
)

type structTagConfig struct {
	names []string
	cache sync.Map // map[reflect.Type]*structFieldPlan
}

type structFieldPlan struct {
	fields map[string]structFieldPlanEntry
	names  []string
}

type structFieldPlanEntry struct {
	index     []int
	ambiguous bool
}

func newStructTagConfig(names []string) *structTagConfig {
	clean := cleanFieldTags(names)
	if len(clean) == 0 {
		return nil
	}
	return &structTagConfig{names: clean}
}

func cleanFieldTags(names []string) []string {
	out := make([]string, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

func structFieldByName(rv reflect.Value, name string, fieldTags *structTagConfig) (reflect.Value, bool, error) {
	if fieldTags == nil {
		fv := rv.FieldByName(name)
		return fv, fv.IsValid(), nil
	}

	plan := fieldTags.plan(rv.Type())
	entry, ok := plan.fields[name]
	if !ok {
		return reflect.Value{}, false, nil
	}
	if entry.ambiguous {
		return reflect.Value{}, false, fmt.Errorf("%w: field %q is ambiguous on %v",
			ErrEvaluate, name, rv.Type())
	}
	fv, ok := fieldByIndex(rv, entry.index)
	if !ok {
		return reflect.Value{}, false, fmt.Errorf("%w: cannot access %q on nil pointer",
			ErrEvaluate, name)
	}
	return fv, true, nil
}

func (c *structTagConfig) plan(t reflect.Type) *structFieldPlan {
	if p, ok := c.cache.Load(t); ok {
		return p.(*structFieldPlan)
	}
	plan := buildStructFieldPlan(t, c.names)
	actual, _ := c.cache.LoadOrStore(t, plan)
	return actual.(*structFieldPlan)
}

func buildStructFieldPlan(t reflect.Type, fieldTags []string) *structFieldPlan {
	plan := &structFieldPlan{
		fields: make(map[string]structFieldPlanEntry),
		names:  make([]string, 0, t.NumField()),
	}
	for _, f := range reflect.VisibleFields(t) {
		if !f.IsExported() {
			continue
		}
		name, hidden := taggedFieldName(f, fieldTags)
		if hidden {
			continue
		}
		if prev, ok := plan.fields[name]; ok {
			prev.ambiguous = true
			plan.fields[name] = prev
			continue
		}
		index := append([]int(nil), f.Index...)
		plan.fields[name] = structFieldPlanEntry{index: index}
	}
	for name, entry := range plan.fields {
		if !entry.ambiguous {
			plan.names = append(plan.names, name)
		}
	}
	return plan
}

func taggedFieldName(f reflect.StructField, fieldTags []string) (name string, hidden bool) {
	for _, tagName := range fieldTags {
		if tagName != "expr" {
			continue
		}
		if raw, ok := f.Tag.Lookup("expr"); ok && tagNamePart(raw) == "-" {
			return "", true
		}
		break
	}
	for _, tagName := range fieldTags {
		raw, ok := f.Tag.Lookup(tagName)
		if !ok {
			continue
		}
		tag := tagNamePart(raw)
		switch tag {
		case "-":
			continue
		case "":
			continue
		default:
			return tag, false
		}
	}
	return f.Name, false
}

func tagNamePart(tag string) string {
	if i := strings.IndexByte(tag, ','); i >= 0 {
		return tag[:i]
	}
	return tag
}

func fieldByIndex(rv reflect.Value, index []int) (reflect.Value, bool) {
	for i, x := range index {
		if i > 0 {
			for rv.Kind() == reflect.Pointer {
				if rv.IsNil() {
					return reflect.Value{}, false
				}
				rv = rv.Elem()
			}
		}
		if rv.Kind() != reflect.Struct {
			return reflect.Value{}, false
		}
		rv = rv.Field(x)
	}
	return rv, true
}

func availableTaggedFields(recv any, fieldTags *structTagConfig) []string {
	if recv == nil {
		return nil
	}
	rv := reflect.ValueOf(recv)
	orig := rv
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return nil
	}

	plan := fieldTags.plan(rv.Type())
	names := make([]string, 0, len(plan.names)+orig.Type().NumMethod())
	names = append(names, plan.names...)
	ot := orig.Type()
	for i := 0; i < ot.NumMethod(); i++ {
		names = append(names, ot.Method(i).Name)
	}
	return names
}
