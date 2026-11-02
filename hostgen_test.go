package expr

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// hostGen draws bounded random Go types, values and function signatures,
// to test evaluation against host data shapes no hand-written table
// lists. Everything is derived from one seed, so a failure reproduces
// from the seed the test prints.
type hostGen struct {
	r *rand.Rand
}

func newHostGen(seed uint64) *hostGen {
	return &hostGen{r: rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))}
}

func (g *hostGen) n(k int) int { return g.r.IntN(k) }

var (
	leafTypes = []reflect.Type{
		reflect.TypeFor[int](), reflect.TypeFor[int8](), reflect.TypeFor[uint16](), reflect.TypeFor[uint64](),
		reflect.TypeFor[int64](), reflect.TypeFor[float32](), reflect.TypeFor[float64](), reflect.TypeFor[string](),
		reflect.TypeFor[bool](), reflect.TypeFor[mxID](), reflect.TypeFor[mxName](), reflect.TypeFor[mxFlag](),
		reflect.TypeFor[any](), reflect.TypeFor[error](),
	}
	mapKeyTypes = []reflect.Type{
		reflect.TypeFor[string](), reflect.TypeFor[string](), reflect.TypeFor[int](), reflect.TypeFor[mxName](),
		reflect.TypeFor[mxID](), reflect.TypeFor[[2]int](), reflect.TypeFor[any](), reflect.TypeFor[bool](),
	}
	fieldNames = []string{"A", "B", "C", "Type", "Map"}
)

// typ returns a random type at most depth levels deep.
func (g *hostGen) typ(depth int) reflect.Type {
	if depth <= 0 || g.n(3) == 0 {
		return leafTypes[g.n(len(leafTypes))]
	}
	switch g.n(6) {
	case 0:
		return reflect.SliceOf(g.typ(depth - 1))
	case 1:
		return reflect.ArrayOf(g.n(4), g.typ(depth-1))
	case 2:
		return reflect.MapOf(mapKeyTypes[g.n(len(mapKeyTypes))], g.typ(depth-1))
	case 3:
		return reflect.PointerTo(g.typ(depth - 1))
	default:
		nf := 1 + g.n(3)
		fields := make([]reflect.StructField, nf)
		for i := range fields {
			fields[i] = reflect.StructField{Name: fieldNames[i], Type: g.typ(depth - 1)}
			if g.n(3) == 0 {
				fields[i].Tag = reflect.StructTag(`json:"` + strings.ToLower(fieldNames[i]) + `"`)
			}
		}
		// An unexported field, which expr must never expose.
		if g.n(4) == 0 {
			fields = append(fields, reflect.StructField{Name: "hidden", PkgPath: "expr", Type: reflect.TypeFor[int]()})
		}
		return reflect.StructOf(fields)
	}
}

// value returns a random value of type t, at most depth levels deep.
// Nilable kinds are sometimes typed nils; interfaces hold values of
// random types, sometimes interfaces again.
func (g *hostGen) value(t reflect.Type, depth int) reflect.Value {
	v := reflect.New(t).Elem()
	switch t.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		vals := []int64{0, 1, -1, 7, math.MaxInt8, math.MaxInt64, math.MinInt64, 1<<53 + 1}
		x := vals[g.n(len(vals))]
		if v.OverflowInt(x) {
			x = int64(g.n(100))
		}
		v.SetInt(x)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		vals := []uint64{0, 1, 7, math.MaxUint16, math.MaxUint64}
		x := vals[g.n(len(vals))]
		if v.OverflowUint(x) {
			x = uint64(g.n(100))
		}
		v.SetUint(x)
	case reflect.Float32, reflect.Float64:
		vals := []float64{0, 1, -1.5, 2.5, 1e300}
		x := vals[g.n(len(vals))]
		if v.OverflowFloat(x) {
			x = 1.5
		}
		v.SetFloat(x)
	case reflect.String:
		vals := []string{"", "a", "A", "héllo", "type", "missing"}
		v.SetString(vals[g.n(len(vals))])
	case reflect.Bool:
		v.SetBool(g.n(2) == 0)
	case reflect.Interface:
		if depth <= 0 || g.n(4) == 0 {
			return v // nil interface
		}
		if t == reflect.TypeFor[error]() {
			errs := []error{mxErr{"e"}, &mxPtrErr{"p"}, (*mxPtrErr)(nil)}
			v.Set(reflect.ValueOf(errs[g.n(len(errs))]))
			return v
		}
		inner := g.typ(depth - 1)
		v.Set(g.value(inner, depth-1))
	case reflect.Pointer:
		if depth <= 0 || g.n(4) == 0 {
			return v // typed nil pointer
		}
		p := reflect.New(t.Elem())
		p.Elem().Set(g.value(t.Elem(), depth-1))
		v.Set(p)
	case reflect.Slice:
		if g.n(5) == 0 {
			return v // typed nil slice
		}
		n := g.n(4)
		s := reflect.MakeSlice(t, n, n)
		for i := 0; i < n; i++ {
			s.Index(i).Set(g.value(t.Elem(), depth-1))
		}
		v.Set(s)
	case reflect.Array:
		for i := 0; i < t.Len(); i++ {
			v.Index(i).Set(g.value(t.Elem(), depth-1))
		}
	case reflect.Map:
		if g.n(5) == 0 {
			return v // typed nil map
		}
		m := reflect.MakeMap(t)
		for i, n := 0, g.n(4); i < n; i++ {
			k := g.value(t.Key(), 1)
			if !k.Comparable() {
				continue // an interface key holding a slice
			}
			m.SetMapIndex(k, g.value(t.Elem(), depth-1))
		}
		v.Set(m)
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			if t.Field(i).IsExported() {
				v.Field(i).Set(g.value(t.Field(i).Type, depth-1))
			}
		}
	}
	return v
}

// pathStep is one hop of a generated access path, with what the hop is
// known to produce.
type pathStep struct {
	src string // ".A", "[0]", `["a"]`
	opt string // the same hop with ?. or ?[
}

// accessOutcome is what reading a path is known to give, from walking
// the value with reflect.
type accessOutcome int

const (
	accessValue   accessOutcome = iota // a value: want
	accessMissing                      // a missing field or key, or an index out of range
	accessNilRecv                      // a nil receiver along the way
	accessUnknown                      // the walk stopped at something it does not model
)

// path walks v for up to hops steps, choosing existing and missing
// fields, keys and indexes, and returns the steps with the outcome a
// strict read of them must have.
func (g *hostGen) path(v reflect.Value, hops int) ([]pathStep, accessOutcome, any) {
	var steps []pathStep
	for h := 0; h < hops; h++ {
		u, _ := unwrapReceiver(v)
		if !u.IsValid() {
			return append(steps, pathStep{".A", "?.A"}), accessNilRecv, nil
		}
		switch u.Kind() {
		case reflect.Struct:
			t := u.Type()
			if g.n(5) == 0 {
				return append(steps, pathStep{".Zz", "?.Zz"}), accessMissing, nil
			}
			i := g.n(t.NumField())
			f := t.Field(i)
			if !f.IsExported() {
				return append(steps, pathStep{"." + f.Name, "?." + f.Name}), accessMissing, nil
			}
			name := displayFieldName(f.Name)
			steps = append(steps, pathStep{"." + name, "?." + name})
			v = u.Field(i)
		case reflect.Map:
			if u.Type().Key().Kind() != reflect.String {
				return steps, accessUnknown, nil
			}
			keys := u.MapKeys()
			if len(keys) == 0 || g.n(4) == 0 {
				return append(steps, pathStep{`["zz"]`, `?["zz"]`}), accessMissing, nil
			}
			k := keys[g.n(len(keys))]
			q := strconv.Quote(k.String())
			steps = append(steps, pathStep{"[" + q + "]", "?[" + q + "]"})
			v = u.MapIndex(k)
		case reflect.Slice, reflect.Array:
			i := g.n(u.Len() + 1) // sometimes one past the end
			steps = append(steps, pathStep{"[" + strconv.Itoa(i) + "]", "?[" + strconv.Itoa(i) + "]"})
			if i >= u.Len() {
				return steps, accessMissing, nil
			}
			v = u.Index(i)
		default:
			return steps, accessValue, boxValue(v)
		}
	}
	return steps, accessValue, boxValue(v)
}

// displayFieldName writes a field name as expr source: keyword names
// like `Type` and `Map` are not keywords (Go keywords are lower case),
// so names pass through.
func displayFieldName(name string) string { return name }

func joinSteps(steps []pathStep, optionalFrom int) string {
	var b strings.Builder
	b.WriteString("V")
	for i, s := range steps {
		if i >= optionalFrom {
			b.WriteString(s.opt)
		} else {
			b.WriteString(s.src)
		}
	}
	return b.String()
}

// checkHostValue runs the value matrix expressions and generated access
// paths over one generated value, through every path, and checks the
// read oracle and the strict/optional relations.
func checkHostValue(t *testing.T, label string, g *hostGen, v any) {
	t.Helper()
	got := map[string]mxOutcome{}
	for _, src := range mxExprs {
		got[src] = mxEval(t, label, mxCase{src: src}, v)
	}
	checkMatrixRelations(t, namedValue{label, v}, got)

	for i := 0; i < 6; i++ {
		steps, outcome, want := g.path(reflect.ValueOf(v), 1+g.n(4))
		if len(steps) == 0 || outcome == accessUnknown {
			continue
		}
		strictSrc := joinSteps(steps, len(steps))
		optSrc := joinSteps(steps, 0)
		strict := mxEval(t, label, mxCase{src: strictSrc}, v)
		opt := mxEval(t, label, mxCase{src: optSrc}, v)
		switch outcome {
		case accessValue:
			if !sameOutcome(strict, mxOutcome{value: want}) {
				t.Errorf("%s: %s = %v, want %#v", label, strictSrc, strict, want)
			}
			if !sameOutcome(opt, mxOutcome{value: want}) {
				t.Errorf("%s: %s = %v, want %#v", label, optSrc, opt, want)
			}
		case accessMissing, accessNilRecv:
			if strict.category != "evaluate" {
				t.Errorf("%s: %s = %v, want ErrEvaluate (missing or nil)", label, strictSrc, strict)
			}
			if !sameOutcome(opt, mxOutcome{}) {
				t.Errorf("%s: %s = %v, want nil", label, optSrc, opt)
			}
		}
		// try of a strict read is the read, or the default.
		tried := mxEval(t, label, mxCase{src: "try(" + strictSrc + `, "d")`}, v)
		wantTry := strict
		if strict.category != "" {
			wantTry = mxOutcome{value: "d"}
		}
		if !sameOutcome(tried, wantTry) {
			t.Errorf("%s: try(%s, \"d\") = %v, want %v", label, strictSrc, tried, wantTry)
		}
	}
}

// hostSeeds is how many generated values TestHostValuesRandom checks;
// EXPR_HOSTGEN_SEEDS raises it for a longer search.
func hostSeeds() int {
	if s, err := strconv.Atoi(os.Getenv("EXPR_HOSTGEN_SEEDS")); err == nil && s > 0 {
		return s
	}
	if testing.Short() {
		return 50
	}
	return 400
}

// hostRegressionSeeds are seeds that once found a bug, kept beyond the
// default range so they always run.
var hostRegressionSeeds = []uint64{
	90,   // [0]map[any]int8 compared with ==: panicked
	2623, // map[any]uint16 with keys 1 and uint16(1): nondeterministic string(V)
}

func TestHostValuesRandom(t *testing.T) {
	seeds := hostRegressionSeeds
	for seed := uint64(1); seed <= uint64(hostSeeds()); seed++ {
		seeds = append(seeds, seed)
	}
	for _, seed := range seeds {
		g := newHostGen(seed)
		typ := g.typ(4)
		v := g.value(typ, 5)
		label := fmt.Sprintf("seed %d (%v)", seed, typ)
		checkHostValue(t, label, g, boxValue(v))
		if t.Failed() {
			t.Fatalf("first failing seed: %d; rerun with EXPR_HOSTGEN_SEEDS or add it to hostRegressionSeeds", seed)
		}
	}
}

// FuzzHostValues explores generated host values from fuzzed seeds:
// go test -fuzz=FuzzHostValues.
func FuzzHostValues(f *testing.F) {
	for _, s := range []uint64{1, 2, 3, 42} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, seed uint64) {
		g := newHostGen(seed)
		typ := g.typ(4)
		checkHostValue(t, fmt.Sprintf("seed %d (%v)", seed, typ), g, boxValue(g.value(typ, 5)))
	})
}

// funcSig draws a function type: parameters of generated types
// (sometimes a leading context.Context, sometimes variadic) and one of
// the result shapes expr supports or rejects.
func (g *hostGen) funcSig() reflect.Type {
	var in []reflect.Type
	if g.n(4) == 0 {
		in = append(in, ctxType)
	}
	for i, n := 0, g.n(3); i < n; i++ {
		in = append(in, g.typ(2))
	}
	variadic := false
	if g.n(4) == 0 {
		in = append(in, reflect.SliceOf(g.typ(1)))
		variadic = true
	}
	var out []reflect.Type
	switch g.n(6) {
	case 0:
	case 1, 2:
		out = []reflect.Type{g.typ(2)}
	case 3:
		out = []reflect.Type{g.typ(1), reflect.TypeFor[error]()}
	case 4:
		out = []reflect.Type{g.typ(1), reflect.TypeFor[mxErr]()}
	default:
		out = []reflect.Type{g.typ(1), g.typ(1)} // unsupported unless the second is an error
	}
	return reflect.FuncOf(in, out, variadic)
}

type hostCall struct {
	ran  int
	args []any
}

// makeHostFunc builds a function of type ft that records its arguments
// and returns generated results, failing on every third call shape.
func (g *hostGen) makeHostFunc(ft reflect.Type, rec *hostCall, results []reflect.Value) any {
	return reflect.MakeFunc(ft, func(in []reflect.Value) []reflect.Value {
		rec.ran++
		for i, a := range in {
			switch {
			case a.Type() == ctxType:
			case ft.IsVariadic() && i == len(in)-1:
				// MakeFunc passes the variadic arguments as one slice.
				for j := 0; j < a.Len(); j++ {
					rec.args = append(rec.args, boxValue(a.Index(j)))
				}
			default:
				rec.args = append(rec.args, boxValue(a))
			}
		}
		return results
	}).Interface()
}

func TestHostFuncsRandom(t *testing.T) {
	n := hostSeeds()
	for seed := uint64(1); seed <= uint64(n); seed++ {
		g := newHostGen(seed)
		ft := g.funcSig()
		results := make([]reflect.Value, ft.NumOut())
		for i := range results {
			results[i] = g.value(ft.Out(i), 2)
		}
		// Arguments: values of the parameter types (which must reach the
		// body unchanged), of other types, or nil.
		var args []any
		var exact []bool
		params := ft.NumIn()
		for i := 0; i < params; i++ {
			pt := ft.In(i)
			if pt == ctxType {
				continue
			}
			if ft.IsVariadic() && i == params-1 {
				pt = pt.Elem()
			}
			switch g.n(4) {
			case 0:
				args = append(args, nil)
				exact = append(exact, false)
			case 1:
				args = append(args, boxValue(g.value(g.typ(2), 2)))
				exact = append(exact, false)
			default:
				args = append(args, boxValue(g.value(pt, 2)))
				exact = append(exact, true)
			}
		}
		if ft.IsVariadic() && g.n(2) == 0 && len(args) > 0 {
			args = args[:len(args)-1] // no variadic arguments
			exact = exact[:len(exact)-1]
		}
		names := make([]string, len(args))
		env := map[string]any{}
		for i, a := range args {
			names[i] = fmt.Sprintf("A%d", i)
			env[names[i]] = a
		}
		src := "F(" + strings.Join(names, ", ") + ")"
		label := fmt.Sprintf("seed %d: %v called as %s with %#v", seed, ft, src, args)

		outcomes := map[string]callOutcome{}
		var recs []*hostCall
		for _, how := range []string{"registered", "registered/noopt", "env"} {
			rec := &hostCall{}
			recs = append(recs, rec)
			fn := g.makeHostFunc(ft, rec, results)
			outcomes[how] = runHostCall(fn, how, src, env)
			outcomes[how] = callOutcome{outcomes[how].mxOutcome, rec.ran}
		}
		reg := outcomes["registered"]
		for how, o := range outcomes {
			if o.panicked != nil {
				t.Errorf("%s via %s panicked: %v", label, how, o.panicked)
			}
			if o.category == "UNWRAPPED" {
				t.Errorf("%s via %s: unwrapped error %s", label, how, o.msg)
			}
			if how == "registered" || reg.category == "compile" {
				continue
			}
			if !sameOutcome(reg.mxOutcome, o.mxOutcome) || reg.called != o.called {
				t.Errorf("%s:\n  registered: %v\n  %s: %v", label, reg, how, o)
			}
		}
		if reg.category == "compile" {
			if o := outcomes["env"]; o.category != "evaluate" || o.called != 0 {
				t.Errorf("%s: registration rejects the signature, so env call must fail unrun: %v", label, o)
			}
			continue
		}
		// An argument whose type is the parameter type reaches the body
		// unchanged.
		if rec := recs[0]; rec.ran == 1 {
			for i, a := range args {
				if exact[i] && i < len(rec.args) && !reflect.DeepEqual(rec.args[i], a) && !hasNaN(a) {
					t.Errorf("%s: argument %d reached the body as %#v, want %#v", label, i, rec.args[i], a)
				}
			}
		}
		if t.Failed() {
			t.Fatalf("first failing seed: %d", seed)
		}
	}
}

func hasNaN(v any) bool {
	f, ok := v.(float64)
	return ok && math.IsNaN(f)
}

func runHostCall(fn any, how, src string, argEnv map[string]any) (out callOutcome) {
	defer func() {
		if r := recover(); r != nil {
			out = callOutcome{mxOutcome{panicked: r}, 0}
		}
	}()
	env := map[string]any{}
	for k, v := range argEnv {
		env[k] = v
	}
	var opts []Option
	switch how {
	case "registered", "registered/noopt":
		opts = append(opts, WithFunctions(map[string]any{"F": fn}))
		if how == "registered/noopt" {
			opts = append(opts, withoutOptimizations())
		}
	case "env":
		env["F"] = fn
	}
	p, err := Compile(src, opts...)
	if err != nil {
		return callOutcome{mxOutcome{category: errCategory(err), msg: err.Error()}, 0}
	}
	v, err := p.Run(context.Background(), env)
	if err != nil {
		return callOutcome{mxOutcome{category: errCategory(err), msg: err.Error()}, 0}
	}
	return callOutcome{mxOutcome{value: v}, 0}
}
