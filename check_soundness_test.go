package expr

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The soundness harness checks the rule Check is built on: the type it
// infers includes every value Run can produce from inputs the schema
// allows, and an expression Check accepts does not fail at run time on
// the wrong kind of value, a nil receiver or a missing field.
//
// It pairs schemas with representative values (valuesFor), runs each
// expression Check accepts on every combination, and asserts each result
// fits the inferred type (fits). Seeds are fixed so the default run is
// deterministic; EXPR_SOUNDNESS_ITERS raises the number of random cases
// and EXPR_SOUNDNESS_SEED picks another seed.

// fits reports whether the runtime value v belongs to t, following expr's
// runtime representation: integers and floats of any width are numbers,
// slices and arrays are lists, and string-keyed maps and structs are
// objects.
func fits(v any, t *Type) bool {
	if t == nil || t == empty || (t.Kind == KindAny && len(t.Variants) == 0) {
		return true
	}
	if v == nil {
		return t.Optional || t.Kind == KindNull
	}
	if t.IsUnion() {
		for _, variant := range t.Variants {
			if fits(v, variant) {
				return true
			}
		}
		return false
	}
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			return t.Optional || t.Kind == KindNull
		}
		rv = rv.Elem()
	}
	var value any
	switch t.Kind {
	case KindNull:
		return false
	case KindBool:
		if rv.Kind() != reflect.Bool {
			return false
		}
		value = rv.Bool()
	case KindNumber:
		switch {
		case rv.CanInt():
			value = rv.Int()
		case rv.CanUint():
			value = int64(rv.Uint())
		case rv.CanFloat():
			value = rv.Float()
		default:
			return false
		}
	case KindString:
		if rv.Kind() != reflect.String {
			return false
		}
		value = rv.String()
	case KindList:
		if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
			return false
		}
		for i := 0; i < rv.Len(); i++ {
			if !fits(rv.Index(i).Interface(), t.Elem) {
				return false
			}
		}
		return true
	case KindObject:
		return fitsObject(rv, t)
	}
	return len(t.Enum) == 0 || t.hasEnumValue(value)
}

func fitsObject(rv reflect.Value, t *Type) bool {
	fields := map[string]any{}
	switch rv.Kind() {
	case reflect.Map:
		if rv.Type().Key().Kind() != reflect.String {
			return false
		}
		iter := rv.MapRange()
		for iter.Next() {
			fields[iter.Key().String()] = iter.Value().Interface()
		}
	case reflect.Struct:
		for i := 0; i < rv.NumField(); i++ {
			name := rv.Type().Field(i).Tag.Get("json")
			if name == "" {
				name = rv.Type().Field(i).Name
			}
			fields[name] = rv.Field(i).Interface()
		}
	default:
		return false
	}
	for name, ft := range t.Fields {
		v, ok := fields[name]
		if !ok {
			if !t.Absent[name] {
				return false
			}
			continue
		}
		if !fits(v, ft) {
			return false
		}
	}
	for name, v := range fields {
		if _, listed := t.Fields[name]; listed {
			continue
		}
		if !t.Open || !fits(v, t.Elem) {
			return false
		}
	}
	return true
}

// valuesFor returns representative runtime values of t: nil when t may be
// nil, a value of every union variant, every enum value, empty and
// non-empty lists, objects with every field (and without each absent
// one), and extra keys in open objects. Some objects are structs.
func valuesFor(t *Type, rng *rand.Rand, depth int) []any {
	var out []any
	if t != nil && (t.Optional || t.Kind == KindNull) {
		out = append(out, nil)
	}
	if t == nil || t.Kind == KindAny && len(t.Variants) == 0 {
		return append(out, nil, int64(1), "s", true, []any{int64(1)}, map[string]any{"a": int64(1)})
	}
	if t.IsUnion() {
		for _, v := range t.Variants {
			out = append(out, valuesFor(v, rng, depth)...)
		}
		return out
	}
	switch t.Kind {
	case KindBool:
		if len(t.Enum) > 0 {
			return append(out, t.Enum...)
		}
		return append(out, true, false)
	case KindNumber:
		if len(t.Enum) > 0 {
			return append(out, t.Enum...)
		}
		return append(out, int64(0), int64(3), 2.5, int(-1), int32(7), float32(0.5))
	case KindString:
		if len(t.Enum) > 0 {
			return append(out, t.Enum...)
		}
		return append(out, "", "a", "Hello")
	case KindList:
		out = append(out, []any{})
		if depth <= 0 {
			return out
		}
		elems := valuesFor(t.Elem, rng, depth-1)
		if len(elems) > 3 {
			rng.Shuffle(len(elems), func(i, j int) { elems[i], elems[j] = elems[j], elems[i] })
			elems = elems[:3]
		}
		out = append(out, append([]any(nil), elems...))
		if len(elems) > 0 {
			out = append(out, []any{elems[0]})
		}
		if typed := typedSlice(t.Elem, elems); typed != nil {
			out = append(out, typed)
		}
		return out
	case KindObject:
		return append(out, objectValues(t, rng, depth)...)
	}
	return out
}

// typedSlice returns elems as a []string or []int64 when the element type
// allows it, so lists also arrive as typed Go slices.
func typedSlice(elem *Type, elems []any) any {
	if elem == nil || elem.Optional || len(elems) == 0 {
		return nil
	}
	switch elem.Kind {
	case KindString:
		out := make([]string, 0, len(elems))
		for _, e := range elems {
			s, ok := e.(string)
			if !ok {
				return nil
			}
			out = append(out, s)
		}
		return out
	case KindNumber:
		out := make([]int64, 0, len(elems))
		for _, e := range elems {
			n, ok := e.(int64)
			if !ok {
				return nil
			}
			out = append(out, n)
		}
		return out
	}
	return nil
}

func objectValues(t *Type, rng *rand.Rand, depth int) []any {
	names := make([]string, 0, len(t.Fields))
	for name := range t.Fields {
		names = append(names, name)
	}
	sort.Strings(names)
	choices := map[string][]any{}
	most := 1
	for _, name := range names {
		if depth <= 0 {
			// Keep the values small: the first value of each field.
			vs := valuesFor(t.Fields[name], rng, 0)
			choices[name] = vs[:min(1, len(vs))]
		} else {
			choices[name] = valuesFor(t.Fields[name], rng, depth-1)
		}
		most = max(most, len(choices[name]))
	}
	most = min(most, 4)
	var out []any
	for i := 0; i < most; i++ {
		m := map[string]any{}
		for _, name := range names {
			vs := choices[name]
			if len(vs) == 0 {
				continue
			}
			// The i-th variant omits each absent field in turn.
			if t.Absent[name] && i%2 == 1 {
				continue
			}
			m[name] = vs[(i+rng.Intn(len(vs)))%len(vs)]
		}
		if t.Open && i == most-1 {
			extra := valuesFor(t.Elem, rng, 0)
			m["extra"] = extra[rng.Intn(len(extra))]
		}
		out = append(out, m)
	}
	if soundStructs && !t.Open && len(t.Absent) == 0 && len(names) > 0 {
		if s, ok := structValue(names, out[0].(map[string]any)); ok {
			out = append(out, s)
		}
	}
	return out
}

// soundStructs adds struct values for closed objects. It is off in the
// random runs: Run reads struct fields with . and ?. but len, has, keys,
// entries and o["k"] reject structs, which Check types as objects.
// TestCheckSoundnessStructFields covers field reads.
var soundStructs = false

// structValue builds a struct with a json tag per field, matching m.
func structValue(names []string, m map[string]any) (any, bool) {
	fields := make([]reflect.StructField, len(names))
	for i, name := range names {
		v, ok := m[name]
		if !ok || v == nil {
			return nil, false
		}
		fields[i] = reflect.StructField{
			Name: "F" + strconv.Itoa(i),
			Type: reflect.TypeOf(v),
			Tag:  reflect.StructTag(`json:"` + name + `"`),
		}
	}
	sv := reflect.New(reflect.StructOf(fields)).Elem()
	for i, name := range names {
		sv.Field(i).Set(reflect.ValueOf(m[name]))
	}
	return sv.Interface(), true
}

// schemaGen draws random schemas of bounded depth. Field names come from
// a small pool so random expressions often name a real field.
type schemaGen struct{ rng *rand.Rand }

var soundFieldNames = []string{"a", "b", "type"}

func (g schemaGen) schema(depth int) *Type {
	t := g.base(depth)
	switch g.rng.Intn(6) {
	case 0:
		t = t.OrNil()
	case 1:
		if depth > 0 {
			t = Join(t, g.base(depth-1))
		}
	}
	return t
}

func (g schemaGen) base(depth int) *Type {
	n := 6
	if depth <= 0 {
		n = 4
	}
	switch g.rng.Intn(n) {
	case 0:
		if g.rng.Intn(3) == 0 {
			return &Type{Kind: KindBool, Enum: []any{true}}
		}
		return Bool()
	case 1:
		if g.rng.Intn(3) == 0 {
			return &Type{Kind: KindNumber, Enum: []any{int64(1), int64(2)}}
		}
		return Number()
	case 2:
		if g.rng.Intn(3) == 0 {
			return EnumOf("a", "b")
		}
		return String()
	case 3:
		if g.rng.Intn(8) == 0 {
			return Null()
		}
		return Number()
	case 4:
		return ListOf(g.schema(depth - 1))
	default:
		t := ObjectOf(map[string]*Type{})
		for _, name := range soundFieldNames {
			if g.rng.Intn(3) > 0 {
				t.Fields[name] = g.schema(depth - 1)
				if g.rng.Intn(5) == 0 {
					if t.Absent == nil {
						t.Absent = map[string]bool{}
					}
					t.Absent[name] = true
				}
			}
		}
		if g.rng.Intn(4) == 0 {
			t.Open = true
			if g.rng.Intn(2) == 0 {
				t.Elem = g.schema(depth - 1)
			}
		}
		return t
	}
}

// typeHasAny reports whether t leaves anything unchecked.
func typeHasAny(t *Type) bool {
	if t == nil || t.isAny() || t == empty {
		return true
	}
	for _, v := range t.Variants {
		if typeHasAny(v) {
			return true
		}
	}
	if t.Kind == KindList && typeHasAny(t.Elem) {
		return true
	}
	if t.Kind == KindObject {
		if t.Open && typeHasAny(t.Elem) {
			return true
		}
		for _, f := range t.Fields {
			if typeHasAny(f) {
				return true
			}
		}
	}
	return false
}

// exprGen draws random expressions over the soundness env's names.
type exprGen struct{ rng *rand.Rand }

var soundVars = []string{"x", "y", "z", "k", "c"}

func (g exprGen) pick(xs ...string) string { return xs[g.rng.Intn(len(xs))] }

func (g exprGen) expr(depth int, bound string) string {
	leaf := func() string {
		vars := soundVars
		if bound != "" {
			vars = append([]string{bound, bound}, vars...)
		}
		switch g.rng.Intn(6) {
		case 0:
			return g.pick(`"a"`, `"b"`, `""`, `1`, `2`, `0`, `2.5`, `true`, `nil`, `[]`, `{}`)
		default:
			return vars[g.rng.Intn(len(vars))]
		}
	}
	if depth <= 0 {
		return leaf()
	}
	sub := func() string { return g.expr(depth-1, bound) }
	field := func() string { return g.pick(soundFieldNames...) }
	switch g.rng.Intn(19) {
	case 0:
		return leaf()
	case 1:
		return sub() + "." + field()
	case 2:
		return sub() + "?." + field()
	case 3:
		return sub() + "[" + sub() + "]"
	case 4:
		return sub() + "?[" + sub() + "]"
	case 5:
		return "(" + sub() + " " + g.pick("+", "-", "*", "/", "%", "==", "!=", "<", "<=", ">", ">=", "&&", "||") + " " + sub() + ")"
	case 6:
		return g.pick("!", "-") + sub()
	case 7:
		return "[" + sub() + ", " + sub() + "]"
	case 8:
		if g.rng.Intn(2) == 0 {
			return `{"` + field() + `": ` + sub() + `, ` + sub() + `: ` + sub() + `}`
		}
		return `{"` + field() + `": ` + sub() + `}`
	case 9:
		return "if(" + sub() + ", " + sub() + ", " + sub() + ")"
	case 10:
		return "try(" + sub() + ", " + sub() + ")"
	case 11:
		form := g.pick("map", "filter", "any", "all", "find", "count", "sortBy", "flatMap")
		if g.rng.Intn(3) == 0 {
			return form + "(" + sub() + ", v, " + g.expr(depth-1, "v") + ")"
		}
		return form + "(" + sub() + ", " + g.expr(depth-1, "it") + ")"
	case 12:
		v := leaf()
		return "(" + v + " != nil && " + v + "." + field() + ")"
	case 13:
		v := leaf()
		return "if(" + v + " != nil, " + v + "." + field() + ", " + sub() + ")"
	case 14:
		return "(" + sub() + " || " + g.pick(`0`, `""`, `[]`, `{}`, `nil`, `false`) + ")"
	case 15:
		return "[" + sub() + "]"
	default:
		name := g.pick(soundBuiltinNames...)
		n := len(builtinSigs[name].params)
		if builtinSigs[name].variadic && g.rng.Intn(2) == 0 {
			n++
		}
		args := make([]string, n)
		for i := range args {
			args[i] = sub()
		}
		return name + "(" + strings.Join(args, ", ") + ")"
	}
}

var soundBuiltinNames = func() []string {
	var names []string
	for name := range builtinSigs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}()

// soundCorpus are hand-picked expressions that reach each part of the
// checker; random expressions add combinations.
var soundCorpus = []string{
	`x`, `x.a`, `x?.a`, `x?.a?.b`, `x.a.b`, `x[0]`, `x?[0]`, `x[k]`, `x?[k]`, `x["a"]`, `x?["type"]`,
	`x.type`, `x?.type`, `x[0].a`, `x?[0]?.a`,
	`x + y`, `x - y`, `x * y`, `x / y`, `x % y`, `-x`, `!x`,
	`x == y`, `x != y`, `x < y`, `x >= y`, `x && y`, `x || y`, `x || 0`, `x || ""`, `x || []`, `x || {}`,
	`x?.a || y`, `x != nil && x.a`, `x == nil || x.a`, `if(x != nil, x.a, nil)`, `if(x, y, z)`,
	`if(c, x, y).a`, `if(c, x, y)?.a`, `try(x.a, y)`, `try(x[0], nil)`,
	`[x, y]`, `[x, y][0]`, `[]`, `{}`, `{"a": x}`, `{"a": x, k: y}`, `{"a": x, k: y}.a`,
	`{"a": x, k: y}?.a`, `{k: x}`, `{"a": x}.a`, `{"a": x, "a": y}.a`, `{x: y}`, `{"a": 1, x: 2}.a`,
	`map(x, it.a)`, `map(x, v, v.a)`, `filter(x, it)`, `filter(x, it.a)`, `any(x, it)`, `all(x, it == y)`,
	`find(x, it.a == y)`, `find(x, it)`, `count(x, it)`, `sortBy(x, it)`, `sortBy(x, it.a)`,
	`flatMap(x, it)`, `flatMap(x, it.a)`, `flatMap(x, [it])`, `map(x, index)`,
	`len(x)`, `string(x)`, `int(x)`, `float(x)`, `bool(x)`, `contains(x, y)`, `has(x, k)`, `keys(x)`,
	`entries(x)`, `entries(x)[0].value`, `lower(x)`, `upper(x)`, `sprintf(x, y)`, `min(x, y)`, `max(x, y, z)`,
	`abs(x)`, `floor(x)`, `ceil(x)`, `round(x)`, `trim(x)`, `split(x, y)`, `join(x, y)`, `replace(x, y, z)`,
	`startsWith(x, y)`, `endsWith(x, y)`, `first(x)`, `last(x)`, `sum(x)`, `slice(x, 0, 1)`, `slice(x, y, z)`,
	`sort(x)`, `reverse(x)`, `first(x).a`, `first(x)?.a`, `slice(if(c, x, y), 0, 1)`,
	`x[y]`, `x?[y]`, `x[y]?.a`, `y[x]`, `x.a[k]`, `x?.a?[k]`,
}

type soundCase struct {
	src    string
	schema map[string]*Type
}

// soundCheck runs one expression against a schema and every combination
// of values for it (capped), reporting violations of the soundness rule.
func soundCheck(t *testing.T, sc soundCase, rng *rand.Rand, maxCombos int) {
	t.Helper()
	p, err := Compile(sc.src, WithBuiltins(), WithFunctions(StringFuncs()), WithFunctions(MathFuncs()),
		WithFunctions(CollectionFuncs()), WithFieldTags("json"))
	if err != nil {
		return
	}
	typ, err := p.Check(sc.schema)
	if err != nil {
		return
	}
	soundStats.accepted++
	names := make([]string, 0, len(sc.schema))
	for name := range sc.schema {
		names = append(names, name)
	}
	sort.Strings(names)
	values := map[string][]any{}
	unchecked := false
	for _, name := range names {
		values[name] = valuesFor(sc.schema[name], rng, 2)
		unchecked = unchecked || typeHasAny(sc.schema[name])
	}
	// Walk the cartesian product, sampling when it is large.
	total := 1
	for _, name := range names {
		total *= len(values[name])
	}
	for i := 0; i < min(total, maxCombos); i++ {
		combo := i
		if total > maxCombos {
			combo = rng.Intn(total)
		}
		env := map[string]any{}
		for _, name := range names {
			vs := values[name]
			env[name] = vs[combo%len(vs)]
			combo /= len(vs)
		}
		got, err := runSafely(p, env)
		soundStats.runs++
		if err == nil {
			soundStats.ok++
		}
		if err != nil {
			if !unchecked && typeShapedErr(err) {
				t.Errorf("%s: Check accepted %s but Run failed on %s: %v", sc.src, formatSchema(sc.schema), formatEnv(env), err)
				return
			}
			continue
		}
		if !fits(got, typ) {
			t.Errorf("%s: Check inferred %s, but Run returned %#v on %s (schema %s)", sc.src, typ, got, formatEnv(env), formatSchema(sc.schema))
			return
		}
	}
}

// soundStats counts what the harness exercised, so a change that makes
// Check reject everything shows up in the log rather than as a pass.
var soundStats struct{ accepted, runs, ok int }

func runSafely(p *Program, env map[string]any) (v any, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return p.Run(context.Background(), env)
}

// valueDependent are run-time errors Check cannot see: they depend on the
// value, not its kind, and the spec documents them as Run's to report.
var valueDependent = []string{
	"out of range", "division by zero", "modulo by zero", "integer overflow",
	"cannot parse", "invalid syntax", "NaN", "Inf", "not a whole number",
	"budget", "too deeply", "too large", "sprintf",
	// A number index or slice bound may be fractional.
	"must be integer",
}

// typeShapedErr reports whether err is the kind of failure Check is meant
// to rule out: a wrong-kind operation, a nil receiver or a missing field.
func typeShapedErr(err error) bool {
	if !errors.Is(err, ErrEvaluate) {
		return true // panics and unwrapped errors are always bugs
	}
	msg := err.Error()
	for _, s := range valueDependent {
		if strings.Contains(msg, s) {
			return false
		}
	}
	return true
}

func formatSchema(schema map[string]*Type) string {
	names := make([]string, 0, len(schema))
	for name := range schema {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, len(names))
	for i, name := range names {
		parts[i] = name + ": " + schema[name].String()
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func formatEnv(env map[string]any) string {
	names := make([]string, 0, len(env))
	for name := range env {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, len(names))
	for i, name := range names {
		parts[i] = fmt.Sprintf("%s=%#v", name, env[name])
	}
	return strings.Join(parts, " ")
}

func soundEnvInt(name string, def int) int {
	if s := os.Getenv(name); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			return n
		}
	}
	return def
}

func randomSchema(rng *rand.Rand) map[string]*Type {
	g := schemaGen{rng}
	schema := map[string]*Type{}
	for _, name := range soundVars {
		schema[name] = g.schema(2)
	}
	schema["c"] = Bool()
	if rng.Intn(2) == 0 {
		schema["k"] = String()
	}
	return schema
}

// TestCheckSoundnessCorpus runs each corpus expression against random
// schemas.
func TestCheckSoundnessCorpus(t *testing.T) {
	seed := int64(soundEnvInt("EXPR_SOUNDNESS_SEED", 1))
	schemas := soundEnvInt("EXPR_SOUNDNESS_ITERS", 60)
	rng := rand.New(rand.NewSource(seed))
	for i := 0; i < schemas; i++ {
		schema := randomSchema(rng)
		for _, src := range soundCorpus {
			soundCheck(t, soundCase{src, schema}, rng, 24)
		}
	}
}

// TestCheckSoundnessRandom runs random expressions against random
// schemas.
func TestCheckSoundnessRandom(t *testing.T) {
	seed := int64(soundEnvInt("EXPR_SOUNDNESS_SEED", 1))
	iters := soundEnvInt("EXPR_SOUNDNESS_ITERS", 60) * 150
	rng := rand.New(rand.NewSource(seed))
	g := exprGen{rng}
	soundStats = struct{ accepted, runs, ok int }{}
	for i := 0; i < iters; i++ {
		soundCheck(t, soundCase{g.expr(3, ""), randomSchema(rng)}, rng, 16)
	}
	t.Logf("%d expressions: %d accepted by Check, %d runs, %d succeeded", iters, soundStats.accepted, soundStats.runs, soundStats.ok)
	if soundStats.accepted < iters/20 {
		t.Errorf("only %d of %d random expressions passed Check; the harness is not exercising the checker", soundStats.accepted, iters)
	}
}

// TestCheckSoundnessHarness pins the harness itself: values of a schema
// fit it, and fits rejects values outside it.
func TestCheckSoundnessHarness(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	g := schemaGen{rng}
	for i := 0; i < 500; i++ {
		s := g.schema(3)
		for _, v := range valuesFor(s, rng, 3) {
			if !fits(v, s) {
				t.Fatalf("valuesFor(%s) produced %#v, which does not fit", s, v)
			}
		}
	}
	for _, tc := range []struct {
		v  any
		t  *Type
		ok bool
	}{
		{nil, String(), false},
		{nil, String().OrNil(), true},
		{"c", EnumOf("a", "b"), false},
		{int32(2), &Type{Kind: KindNumber, Enum: []any{int64(2)}}, true},
		{[]string{"a"}, ListOf(String()), true},
		{[]any{1}, ListOf(String()), false},
		{map[string]any{}, ObjectOf(map[string]*Type{"a": Number()}), false},
		{map[string]any{}, &Type{Kind: KindObject, Fields: map[string]*Type{"a": Number()}, Absent: map[string]bool{"a": true}}, true},
		{map[string]any{"z": 1}, ObjectOf(nil), false},
		{map[string]any{"z": 1}, OpenObject(nil), true},
		{"s", Join(Number(), String()), true},
		{true, Join(Number(), String()), false},
	} {
		if got := fits(tc.v, tc.t); got != tc.ok {
			t.Errorf("fits(%#v, %s) = %v, want %v", tc.v, tc.t, got, tc.ok)
		}
	}
}

// TestCheckSoundnessStructFields reads fields of struct values, which Run
// supports for every closed object Check accepts.
func TestCheckSoundnessStructFields(t *testing.T) {
	soundStructs = true
	defer func() { soundStructs = false }()
	rng := rand.New(rand.NewSource(1))
	g := schemaGen{rng}
	for i := 0; i < 200; i++ {
		schema := map[string]*Type{"x": g.schema(2), "y": g.schema(2)}
		for _, src := range []string{`x.a`, `x?.a`, `x.a.b`, `x?.a?.type`, `x.type == y`, `[x.a, y.b]`, `x != nil && x.a`} {
			soundCheck(t, soundCase{src, schema}, rng, 16)
		}
	}
}
