package expr

import (
	"errors"
	"fmt"
	"go/ast"
	"go/token"
	"reflect"
	"sort"
	"strings"
)

// ErrType wraps every error [Program.Check] returns.
var ErrType = errors.New("expr: type error")

// TypeError lists every problem Check found.
type TypeError struct {
	Problems []string
}

func (e *TypeError) Error() string {
	return "expr: type error: " + strings.Join(e.Problems, "; ")
}

func (e *TypeError) Unwrap() error { return ErrType }

// FuncType computes a function's result type from its argument types, for
// [WithFuncType]. Return an error to report a misuse.
type FuncType func(args []*Type) (*Type, error)

// CheckOption configures [Program.Check].
type CheckOption func(*checkConfig)

type checkConfig struct {
	funcTypes map[string]FuncType
}

// WithFuncType declares the result type of a function registered with
// [WithFunctions]. Without it, calls to registered functions produce Any.
func WithFuncType(name string, fn FuncType) CheckOption {
	return func(c *checkConfig) {
		if c.funcTypes == nil {
			c.funcTypes = map[string]FuncType{}
		}
		c.funcTypes[name] = fn
	}
}

// Check type-checks the expression against the types of its environment
// and returns the type of its result.
//
// It reports every problem it finds, as a *[TypeError]: names that are not
// in env, fields a closed object does not have, operations on the wrong
// kind of value, comparisons with a value outside an enum, and selecting
// from a value that may be nil without `?.`. Anything typed [Any] is
// unchecked, so a partially typed environment checks what it can.
//
// Check is static. It does not evaluate anything and cannot see values,
// so a successful check does not guarantee Run succeeds (a list index can
// still be out of range), and env must describe what Run will be given.
func (p *Program) Check(env map[string]*Type, opts ...CheckOption) (*Type, error) {
	c := &checker{p: p, env: env}
	for _, o := range opts {
		o(&c.cfg)
	}
	t := c.check(p.root, nil)
	if len(c.problems) > 0 {
		return t, &TypeError{Problems: c.problems}
	}
	return t, nil
}

// Check type-checks every expression in the template. A template always
// renders a string.
func (t *Template) Check(env map[string]*Type, opts ...CheckOption) error {
	var problems []string
	for _, seg := range t.Segments() {
		if seg.Program == nil {
			continue
		}
		if _, err := seg.Program.Check(env, opts...); err != nil {
			var te *TypeError
			if errors.As(err, &te) {
				problems = append(problems, te.Problems...)
			}
		}
	}
	if len(problems) > 0 {
		return &TypeError{Problems: problems}
	}
	return nil
}

type checker struct {
	p        *Program
	env      map[string]*Type
	cfg      checkConfig
	problems []string
	// lenient is positive inside try's first argument, where a nil
	// receiver is caught at run time and so is not a problem.
	lenient int
	// narrowed counts, by source text, expressions known not to be nil
	// here: the right side of `x != nil && ...`, the then-branch of
	// `if(x != nil, ...)`.
	narrowed map[string]int
}

// narrowings returns the expressions that cond being truthy (or falsy)
// proves are not nil.
func narrowings(cond ast.Expr, truthy bool) []string {
	switch n := cond.(type) {
	case *ast.ParenExpr:
		return narrowings(n.X, truthy)
	case *ast.UnaryExpr:
		if n.Op == token.NOT {
			return narrowings(n.X, !truthy)
		}
	case *ast.BinaryExpr:
		switch {
		case n.Op == token.LAND && truthy, n.Op == token.LOR && !truthy:
			return append(narrowings(n.X, truthy), narrowings(n.Y, truthy)...)
		case n.Op == token.NEQ && truthy, n.Op == token.EQL && !truthy:
			if isNilIdent(n.Y) {
				return []string{exprDisplayString(n.X)}
			}
			if isNilIdent(n.X) {
				return []string{exprDisplayString(n.Y)}
			}
		}
	case *ast.Ident, *ast.SelectorExpr, *ast.IndexExpr, *ast.CallExpr:
		if truthy {
			return []string{exprDisplayString(cond)}
		}
	}
	return nil
}

func isNilIdent(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "nil"
}

// narrow checks node with the given expressions known not to be nil.
func (c *checker) narrow(names []string, node ast.Expr, sc *scope) *Type {
	if c.narrowed == nil {
		c.narrowed = map[string]int{}
	}
	for _, n := range names {
		c.narrowed[n]++
	}
	t := c.check(node, sc)
	for _, n := range names {
		c.narrowed[n]--
	}
	return t
}

// applyNarrowing drops Optional from a value narrowed by its context.
func (c *checker) applyNarrowing(node ast.Expr, t *Type) *Type {
	if len(c.narrowed) == 0 || t == nil || !t.Optional {
		return t
	}
	if c.narrowed[exprDisplayString(node)] > 0 {
		return t.NonNil()
	}
	return t
}

// scope holds the names a higher-order form binds.
type scope struct {
	names  map[string]*Type
	parent *scope
}

func (s *scope) lookup(name string) (*Type, bool) {
	for ; s != nil; s = s.parent {
		if t, ok := s.names[name]; ok {
			return t, true
		}
	}
	return nil, false
}

func (c *checker) report(node ast.Expr, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if src := exprDisplayString(node); src != "" {
		msg = "`" + src + "`: " + msg
	}
	c.problems = append(c.problems, msg)
}

// oneKind reports a union where an operation needs a single kind.
func (c *checker) oneKind(node ast.Expr, t *Type) bool {
	if t.IsUnion() {
		c.report(node, "may be %s; the operation needs one kind", t)
		return false
	}
	return true
}

// nilCheck reports a value that may be nil where nil is not allowed.
func (c *checker) nilCheck(node ast.Expr, t *Type) {
	if t != nil && t.Optional && c.lenient == 0 {
		c.report(node, "may be nil here; use ?. or try(...)")
	}
}

func (c *checker) check(node ast.Expr, sc *scope) *Type {
	switch node.(type) {
	case *ast.Ident, *ast.SelectorExpr, *ast.IndexExpr, *ast.CallExpr:
		return c.applyNarrowing(node, c.checkNode(node, sc))
	}
	return c.checkNode(node, sc)
}

func (c *checker) checkNode(node ast.Expr, sc *scope) *Type {
	switch n := node.(type) {
	case *ast.Ident:
		return c.ident(n, sc)
	case *ast.BasicLit:
		return c.literal(n)
	case *ast.ParenExpr:
		return c.check(n.X, sc)
	case *ast.UnaryExpr:
		x := c.check(n.X, sc)
		if n.Op == token.NOT {
			return Bool()
		}
		c.nilCheck(n.X, x)
		c.oneKind(n.X, x)
		if !x.isAny() && x.Kind != KindNumber {
			c.report(n, "%s is not a number", x)
		}
		return Number()
	case *ast.BinaryExpr:
		return c.binary(n, sc)
	case *ast.SelectorExpr:
		return c.field(n, n.X, c.check(n.X, sc), n.Sel.Name, false)
	case *ast.IndexExpr:
		return c.index(n, n.X, c.check(n.X, sc), n.Index, c.check(n.Index, sc), false)
	case *ast.CallExpr:
		return c.call(n, sc)
	case *ast.CompositeLit:
		return c.composite(n, sc)
	}
	return Any
}

func (c *checker) ident(n *ast.Ident, sc *scope) *Type {
	switch n.Name {
	case "true", "false":
		return &Type{Kind: KindBool, Enum: []any{n.Name == "true"}}
	case "nil":
		return Null()
	}
	if t, ok := sc.lookup(n.Name); ok {
		return t
	}
	if t, ok := c.env[n.Name]; ok {
		if t == nil {
			return Any
		}
		return t
	}
	if _, ok := c.p.funcs[displayIdent(n.Name)]; ok {
		return Any
	}
	names := make([]string, 0, len(c.env))
	for k := range c.env {
		names = append(names, k)
	}
	c.report(n, "undefined name %q%s", displayIdent(n.Name), formatHint(n.Name, names))
	return Any
}

func (c *checker) literal(n *ast.BasicLit) *Type {
	v, ok := c.p.litCache[n]
	if !ok {
		var err error
		if v, err = evalLiteral(n); err != nil {
			return Any
		}
	}
	switch v.(type) {
	case string:
		return &Type{Kind: KindString, Enum: []any{v}}
	case int64, float64, int:
		return &Type{Kind: KindNumber, Enum: []any{v}}
	}
	return Any
}

// plain drops literal values from a type: `"a" + x` is a string, not "a".
func plain(t *Type) *Type {
	if t == nil || len(t.Enum) == 0 {
		return t
	}
	c := *t
	c.Enum = nil
	return &c
}

func (c *checker) binary(n *ast.BinaryExpr, sc *scope) *Type {
	switch n.Op {
	case token.LAND:
		// `a && b` is a when a is falsy, else b.
		l := c.check(n.X, sc)
		r := c.narrow(narrowings(n.X, true), n.Y, sc)
		return Join(l, r)
	case token.LOR:
		// `a || b` is a when a is truthy (so never nil), else b.
		l := c.check(n.X, sc)
		r := c.narrow(narrowings(n.X, false), n.Y, sc)
		return Join(l.NonNil(), r)
	}
	l, r := c.check(n.X, sc), c.check(n.Y, sc)
	switch n.Op {
	case token.EQL, token.NEQ:
		c.comparable(n, l, r)
		return Bool()
	case token.LSS, token.GTR, token.LEQ, token.GEQ:
		c.nilCheck(n.X, l)
		c.nilCheck(n.Y, r)
		if !c.oneKind(n.X, l) || !c.oneKind(n.Y, r) {
			return Bool()
		}
		if !l.isAny() && !r.isAny() && (l.Kind != r.Kind || (l.Kind != KindNumber && l.Kind != KindString)) {
			c.report(n, "cannot order %s and %s", l, r)
		}
		return Bool()
	case token.ADD:
		c.nilCheck(n.X, l)
		c.nilCheck(n.Y, r)
		if !c.oneKind(n.X, l) || !c.oneKind(n.Y, r) {
			return Any
		}
		switch {
		case l.isAny() && r.isAny():
			return Any
		case l.isAny():
			return plain(r.NonNil())
		case r.isAny():
			return plain(l.NonNil())
		case l.Kind == r.Kind && (l.Kind == KindNumber || l.Kind == KindString):
			return plain(l.NonNil())
		}
		c.report(n, "cannot add %s and %s", l, r)
		return Any
	default: // - * / %
		c.nilCheck(n.X, l)
		c.nilCheck(n.Y, r)
		if !c.oneKind(n.X, l) || !c.oneKind(n.Y, r) {
			return Number()
		}
		for _, t := range []*Type{l, r} {
			if !t.isAny() && t.Kind != KindNumber {
				c.report(n, "%s is not a number", t)
				break
			}
		}
		return Number()
	}
}

// comparable reports == and != comparisons that can never be true.
func (c *checker) comparable(n *ast.BinaryExpr, l, r *Type) {
	if l.isAny() || r.isAny() || l.Kind == KindNull || r.Kind == KindNull {
		return
	}
	if l.Kind == KindList || l.Kind == KindObject || r.Kind == KindList || r.Kind == KindObject {
		c.report(n, "cannot compare %s and %s with %s", l.NonNil(), r.NonNil(), n.Op)
		return
	}
	if l.Kind != r.Kind {
		c.report(n, "comparing %s with %s is never true", l.NonNil(), r.NonNil())
		return
	}
	for _, pair := range [][2]*Type{{l, r}, {r, l}} {
		set, lit := pair[0], pair[1]
		if len(set.Enum) > 0 && len(lit.Enum) == 1 && !set.hasEnumValue(lit.Enum[0]) {
			c.report(n, "the value is one of %s, never %s", set.NonNil(), formatEnumValue(lit.Enum[0]))
			return
		}
	}
}

// field types recv.name. optional is set for `?.`.
func (c *checker) field(n, recvNode ast.Expr, recv *Type, name string, optional bool) *Type {
	switch {
	case recv.IsUnion():
		c.oneKind(recvNode, recv)
		return Any
	case recv.isAny():
		return Any
	case recv.Kind == KindNull:
		if !optional && c.lenient == 0 {
			c.report(n, "the value is always nil")
		}
		return Null()
	}
	if !optional {
		c.nilCheck(recvNode, recv)
	}
	if recv.Kind != KindObject {
		c.report(n, "cannot read field %q of %s", name, recv)
		return Any
	}
	f, ok := recv.Fields[name]
	switch {
	case ok:
		f = orAny(f) // a nil field type is unknown, like a nil env entry
	case recv.Open:
		// An unlisted field may be absent, and selecting a missing key
		// fails at run time; only ?. reads it safely.
		f = recv.Elem
		if f == nil {
			f = Any
		}
		if f.Kind != KindAny {
			if !optional && c.lenient == 0 {
				c.report(n, "field %q may be absent; use ?.", name)
			}
			f = f.OrNil()
		}
	default:
		names := make([]string, 0, len(recv.Fields))
		for k := range recv.Fields {
			names = append(names, k)
		}
		sort.Strings(names)
		c.report(n, "no field %q in %s%s", name, recv.NonNil(), formatHint(name, names))
		return Any
	}
	if optional && recv.Optional {
		return f.OrNil()
	}
	return f
}

func (c *checker) index(n, recvNode ast.Expr, recv *Type, idxNode ast.Expr, idx *Type, optional bool) *Type {
	if recv.IsUnion() {
		c.oneKind(recvNode, recv)
		return Any
	}
	if recv.isAny() {
		return Any
	}
	if recv.Kind == KindNull {
		if !optional && c.lenient == 0 {
			c.report(n, "the value is always nil")
		}
		return Null()
	}
	if !optional {
		c.nilCheck(recvNode, recv)
	}
	var out *Type
	switch recv.Kind {
	case KindList:
		if !idx.isAny() && idx.Kind != KindNumber {
			c.report(n, "list index must be a number, not %s", idx)
		}
		out = recv.Elem
	case KindString:
		out = String()
	case KindObject:
		if idx.Kind == KindString && len(idx.Enum) == 1 {
			name, _ := idx.Enum[0].(string)
			return c.field(n, recvNode, recv, name, optional)
		}
		out = Any
		if recv.Open && recv.Elem != nil {
			out = recv.Elem
		}
	default:
		c.report(n, "cannot index %s", recv)
		return Any
	}
	out = orAny(out)
	if optional {
		return out.OrNil()
	}
	return out
}

var iteratingForms = map[string]bool{
	mapFormName: true, "filter": true, "flatMap": true, "any": true,
	"all": true, "find": true, "count": true, "sortBy": true,
}

func (c *checker) call(n *ast.CallExpr, sc *scope) *Type {
	ident, _ := n.Fun.(*ast.Ident)
	if ident == nil {
		c.check(n.Fun, sc)
		for _, a := range n.Args {
			c.check(a, sc)
		}
		return Any
	}
	name := ident.Name
	switch {
	case name == trySelectFormName && len(n.Args) == 2:
		recv := c.check(n.Args[0], sc)
		lit, _ := n.Args[1].(*ast.BasicLit)
		if lit == nil {
			return Any
		}
		field, ok := literalString(c.literal(lit))
		if !ok {
			return Any
		}
		return c.field(n, n.Args[0], recv, field, true).OrNil()
	case name == tryIndexFormName && len(n.Args) == 2:
		recv := c.check(n.Args[0], sc)
		return c.index(n, n.Args[0], recv, n.Args[1], c.check(n.Args[1], sc), true).OrNil()
	case iteratingForms[name]:
		return c.iterating(n, displayIdent(name), sc)
	case name == "try":
		if len(n.Args) != 2 {
			c.report(n, "try expects 2 arguments (value, default), got %d", len(n.Args))
			return Any
		}
		c.lenient++
		a := c.check(n.Args[0], sc)
		c.lenient--
		return Join(a, c.check(n.Args[1], sc))
	case name == ifFuncName:
		if len(n.Args) != 3 {
			c.report(n, "if expects 3 arguments (cond, then, else), got %d", len(n.Args))
			return Any
		}
		c.check(n.Args[0], sc)
		then := c.narrow(narrowings(n.Args[0], true), n.Args[1], sc)
		return Join(then, c.narrow(narrowings(n.Args[0], false), n.Args[2], sc))
	}

	args := make([]*Type, len(n.Args))
	for i, a := range n.Args {
		args[i] = c.check(a, sc)
	}
	display := displayIdent(name)
	fn := c.cfg.funcTypes[display]
	if fn == nil {
		if registered, ok := c.p.funcs[display]; ok && isBuiltin(display, registered) {
			fn = builtinTypes[display]
		}
	}
	if fn == nil {
		if _, registered := c.p.funcs[display]; registered {
			return Any
		}
		if _, inEnv := c.env[display]; inEnv {
			return Any
		}
		if _, bound := sc.lookup(display); bound {
			return Any
		}
		c.report(n, "undefined function %q", display)
		return Any
	}
	if nilIntolerant[display] && fn != nil && c.cfg.funcTypes[display] == nil {
		for i, a := range args {
			c.nilCheck(n.Args[i], a)
		}
	}
	t, err := fn(args)
	if err != nil {
		c.report(n, "%v", err)
		return Any
	}
	return t
}

// builtinFuncs maps each builtin name to its function's code pointer, so
// Check uses a builtin's type only when the registered function is the
// builtin, not a host function that replaced it.
var builtinFuncs = func() map[string]uintptr {
	out := map[string]uintptr{}
	for _, group := range []map[string]any{Builtins(), StringFuncs(), MathFuncs(), CollectionFuncs()} {
		for name, fn := range group {
			out[name] = reflect.ValueOf(fn).Pointer()
		}
	}
	return out
}()

func isBuiltin(name string, fn any) bool {
	ptr, ok := builtinFuncs[name]
	if !ok {
		return false
	}
	rv := reflect.ValueOf(fn)
	return rv.Kind() == reflect.Func && rv.Pointer() == ptr
}

// nilIntolerant are the builtins that fail on a nil argument. The rest
// (len, string, int, first, keys, sum, ...) treat nil as empty.
var nilIntolerant = map[string]bool{
	"abs": true, "ceil": true, "floor": true, "round": true, "min": true, "max": true,
	"upper": true, "lower": true, "trim": true, "replace": true, "split": true,
	"startsWith": true, "endsWith": true, "slice": true,
}

func (c *checker) iterating(n *ast.CallExpr, form string, sc *scope) *Type {
	coll, bind, body, err := splitFormArgs(form, n)
	if err != nil {
		c.report(n, "%s", strings.TrimPrefix(err.Error(), ErrEvaluate.Error()+": "))
		return Any
	}
	ct := c.check(coll, sc)
	elem := Any
	switch {
	case ct.isAny(), ct.Kind == KindNull:
	case ct.Kind == KindList:
		elem = ct.Elem
		if elem == nil {
			elem = Any
		}
	default:
		c.report(n, "%s expects a list, got %s", form, ct)
	}
	if bind == "" {
		bind = "it"
	}
	inner := &scope{names: map[string]*Type{bind: elem, "index": Number()}, parent: sc}
	// The binding shadows any outer name, so nil checks on that name do
	// not reach into the body.
	hidden := map[string]int{}
	for k, v := range c.narrowed {
		for _, name := range []string{bind, "index"} {
			if k == name || strings.HasPrefix(k, name+".") || strings.HasPrefix(k, name+"?") || strings.HasPrefix(k, name+"[") {
				hidden[k] = v
				delete(c.narrowed, k)
			}
		}
	}
	bt := c.check(body, inner)
	for k, v := range hidden {
		c.narrowed[k] = v
	}
	switch form {
	case "map":
		return ListOf(bt)
	case "filter", "sortBy":
		return ListOf(elem)
	case "flatMap":
		if bt.Kind == KindList {
			return ListOf(bt.Elem)
		}
		return ListOf(Any)
	case "any", "all":
		return Bool()
	case "find":
		return elem.OrNil()
	case "count":
		return Number()
	}
	return Any
}

func (c *checker) composite(n *ast.CompositeLit, sc *scope) *Type {
	switch n.Type.(type) {
	case *ast.ArrayType:
		var elem *Type
		for _, e := range n.Elts {
			elem = Join(elem, c.check(e, sc))
		}
		if elem == nil {
			elem = Any
		}
		return ListOf(elem)
	case *ast.MapType:
		out := ObjectOf(map[string]*Type{})
		for _, e := range n.Elts {
			kv, ok := e.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			kt := c.check(kv.Key, sc)
			vt := c.check(kv.Value, sc)
			if key, ok := literalString(kt); ok {
				out.Fields[key] = vt
			} else {
				out.Open = true
			}
		}
		return out
	}
	return Any
}

func literalString(t *Type) (string, bool) {
	if t.Kind != KindString || len(t.Enum) != 1 {
		return "", false
	}
	s, ok := t.Enum[0].(string)
	return s, ok
}

// builtinTypes types the functions in Builtins, StringFuncs, MathFuncs and
// CollectionFuncs.
var builtinTypes = map[string]FuncType{
	"len": func(a []*Type) (*Type, error) {
		if err := wantArgs("len", a, 1); err != nil {
			return nil, err
		}
		if t := a[0]; !t.isAny() && t.Kind != KindNull && t.Kind != KindList && t.Kind != KindString && t.Kind != KindObject {
			return nil, fmt.Errorf("len of %s", t)
		}
		return Number(), nil
	},
	"string": returns(String()), "sprintf": returns(String()), "join": returns(String()),
	"int": returns(Number()), "float": returns(Number()), "abs": returns(Number()),
	"ceil": returns(Number()), "floor": returns(Number()), "round": returns(Number()),
	"sum": returns(Number()), "min": returns(Number()), "max": returns(Number()),
	"bool": returns(Bool()), "has": returns(Bool()), "contains": returns(Bool()),
	"upper": stringFunc("upper", String()), "lower": stringFunc("lower", String()),
	"trim": stringFunc("trim", String()), "replace": stringFunc("replace", String()),
	"startsWith": stringFunc("startsWith", Bool()), "endsWith": stringFunc("endsWith", Bool()),
	"split": stringFunc("split", ListOf(String())),
	"keys":  returns(ListOf(String())),
	"first": elemFunc, "last": elemFunc,
	"slice": sameFunc, "sort": sameFunc, "reverse": sameFunc,
	"entries": func(a []*Type) (*Type, error) {
		value := Any
		if len(a) == 1 && a[0].Kind == KindObject {
			var v *Type
			for _, f := range a[0].Fields {
				v = Join(v, f)
			}
			if a[0].Open {
				v = Any
			}
			if v != nil {
				value = v
			}
		}
		return ListOf(ObjectOf(map[string]*Type{"key": String(), "value": value})), nil
	},
}

func returns(t *Type) FuncType {
	return func([]*Type) (*Type, error) { return t, nil }
}

func stringFunc(name string, result *Type) FuncType {
	return func(a []*Type) (*Type, error) {
		if len(a) > 0 && !a[0].isAny() && a[0].Kind != KindString {
			return nil, fmt.Errorf("%s expects a string, got %s", name, a[0])
		}
		return result, nil
	}
}

func elemFunc(a []*Type) (*Type, error) {
	if len(a) == 1 && a[0] != nil && a[0].Kind == KindList && a[0].Elem != nil {
		return a[0].Elem.OrNil(), nil
	}
	return Any, nil
}

func sameFunc(a []*Type) (*Type, error) {
	if len(a) > 0 && a[0] != nil && (a[0].Kind == KindList || a[0].Kind == KindString) {
		return plain(a[0].NonNil()), nil
	}
	return Any, nil
}

func wantArgs(name string, a []*Type, n int) error {
	if len(a) != n {
		return fmt.Errorf("%s expects %d argument(s), got %d", name, n, len(a))
	}
	return nil
}
