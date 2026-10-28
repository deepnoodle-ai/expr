package expr

import (
	"errors"
	"fmt"
	"go/ast"
	"go/scanner"
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
		if !c.oneKind(n.X, x) {
			return Number()
		}
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
	name := displayIdent(n.Name)
	if t, ok := sc.lookup(name); ok {
		return t
	}
	if t, ok := c.env[name]; ok {
		return orAny(t)
	}
	if _, ok := c.p.funcs[name]; ok {
		return Any
	}
	names := make([]string, 0, len(c.env))
	for k := range c.env {
		names = append(names, k)
	}
	for s := sc; s != nil; s = s.parent {
		for k := range s.names {
			names = append(names, k)
		}
	}
	c.report(n, "undefined name %q%s", name, formatHint(name, names))
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

// comparable reports == and != comparisons that fail at run time (a list
// or object compared with anything but nil) or can never be true.
func (c *checker) comparable(n *ast.BinaryExpr, l, r *Type) {
	if l.isAny() || r.isAny() || l.Kind == KindNull || r.Kind == KindNull {
		return
	}
	lk, rk := variants(l), variants(r)
	for _, t := range append(lk, rk...) {
		if t.Kind == KindList || t.Kind == KindObject {
			c.report(n, "cannot compare %s and %s with %s", l.NonNil(), r.NonNil(), n.Op)
			return
		}
	}
	shared := false
	for _, a := range lk {
		for _, b := range rk {
			shared = shared || a.Kind == b.Kind
		}
	}
	if !shared {
		c.report(n, "comparing %s with %s is never true", l.NonNil(), r.NonNil())
		return
	}
	if l.IsUnion() || r.IsUnion() {
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

// variants returns the types a union may have, or t alone.
func variants(t *Type) []*Type {
	if t.IsUnion() {
		return t.Variants
	}
	return []*Type{t}
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
		// fails at run time; only ?. reads it safely, as nil.
		f = orAny(recv.Elem)
		switch {
		case f.isAny():
		case optional:
			f = f.OrNil()
		case c.lenient == 0:
			c.report(n, "field %q may be absent; use ?.", name)
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
	// A nil index fails even after ?[.
	if idx.Kind == KindNull {
		if c.lenient == 0 {
			c.report(idxNode, "the index is always nil")
		}
		return Any
	}
	c.nilCheck(idxNode, idx)
	if !c.oneKind(idxNode, idx) {
		return Any
	}
	var out *Type
	switch recv.Kind {
	case KindList, KindString:
		what := "list"
		out = recv.Elem
		if recv.Kind == KindString {
			what, out = "string", String()
		}
		if !idx.isAny() && idx.Kind != KindNumber {
			c.report(n, "%s index must be a number, not %s", what, describe(idx))
		}
	case KindObject:
		if !idx.isAny() && idx.Kind != KindString {
			c.report(n, "object key must be a string, not %s", describe(idx))
			return Any
		}
		if len(idx.Enum) > 0 {
			// Each key the index may be. A nil receiver is reported above.
			r := recv
			if !optional {
				r = recv.NonNil()
			}
			var t *Type
			for _, v := range idx.Enum {
				name, _ := v.(string)
				t = Join(t, c.field(n, recvNode, r, name, optional))
			}
			return t
		}
		if idx.isAny() || (recv.Open && recv.Elem.isAny()) {
			return Any
		}
		// Any string: the key may be absent, which fails unless read
		// with ?[, as nil.
		if !optional && c.lenient == 0 {
			c.report(n, "the key may be absent; use ?[")
		}
		for _, f := range recv.Fields {
			out = Join(out, orAny(f))
		}
		if recv.Open {
			out = Join(out, recv.Elem)
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
		c.args(n, sc)
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
	}

	// Resolve the name as Run does: the environment, then registered
	// functions, then the special forms.
	display := displayIdent(name)
	_, bound := sc.lookup(display)
	if _, inEnv := c.env[display]; bound || inEnv {
		c.args(n, sc)
		return Any
	}
	if registered, ok := c.p.funcs[display]; ok {
		args := c.args(n, sc)
		if fn := c.cfg.funcTypes[display]; fn != nil {
			t, err := fn(args)
			if err != nil {
				c.report(n, "%v", err)
				return Any
			}
			return t
		}
		if sig, ok := builtinSigs[display]; ok && isBuiltin(display, registered) {
			return c.builtin(n, display, sig, args)
		}
		return Any
	}
	switch {
	case iteratingForms[name]:
		return c.iterating(n, display, sc)
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
	c.args(n, sc)
	names := make([]string, 0, len(c.p.funcs)+len(specialForms))
	for k := range c.p.funcs {
		names = append(names, k)
	}
	names = append(names, specialForms...)
	c.report(n, "undefined function %q%s", display, formatHint(display, names))
	return Any
}

// specialForms are the user-visible names of the forms call handles
// itself.
var specialForms = []string{"map", "filter", "flatMap", "any", "all", "find", "count", "sortBy", "try", "if"}

func (c *checker) args(n *ast.CallExpr, sc *scope) []*Type {
	args := make([]*Type, len(n.Args))
	for i, a := range n.Args {
		args[i] = c.check(a, sc)
	}
	return args
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
		if mentions(k, bind) || mentions(k, "index") {
			hidden[k] = v
			delete(c.narrowed, k)
		}
	}
	bt := c.check(body, inner)
	if form == "sortBy" {
		// Keys must all be numbers or all strings, and not nil.
		if bt.IsUnion() {
			c.report(body, "sortBy: keys may be %s; they must all be one kind", bt)
		} else {
			c.argument(body, "sortBy", 2, param{kinds: numberOrString}, bt)
		}
	}
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

// mentions reports whether the source text src refers to the name as an
// identifier, not as a field (`x.name`) or inside a string.
func mentions(src, name string) bool {
	fs := token.NewFileSet()
	file := fs.AddFile("", fs.Base(), len(src))
	var s scanner.Scanner
	s.Init(file, []byte(src), nil, 0)
	prev := token.ILLEGAL
	for {
		_, tok, lit := s.Scan()
		if tok == token.EOF {
			return false
		}
		if tok == token.IDENT && lit == name && prev != token.PERIOD {
			return true
		}
		prev = tok
	}
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
				continue
			}
			out.Open = true
			switch {
			case kt.Kind == KindNull:
				c.report(kv.Key, "an object key is always nil")
			case !c.oneKind(kv.Key, kt):
			case !kt.isAny() && kt.Kind != KindString:
				c.report(kv.Key, "an object key must be a string, not %s", describe(kt))
			default:
				c.nilCheck(kv.Key, kt)
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

// kinds is a set of kinds, for the arguments a builtin accepts. The zero
// set accepts any kind.
type kinds uint8

func kindSet(ks ...Kind) kinds {
	var s kinds
	for _, k := range ks {
		s |= 1 << k
	}
	return s
}

func (s kinds) has(k Kind) bool { return s == 0 || s&(1<<k) != 0 }

func (s kinds) String() string {
	var names []string
	for k := KindNull; k <= KindObject; k++ {
		if s&(1<<k) != 0 {
			names = append(names, kindNames[k])
		}
	}
	return strings.Join(names, " or ")
}

var kindNames = map[Kind]string{
	KindNull: "nil", KindBool: "bool", KindNumber: "number", KindString: "string",
	KindList: "a list", KindObject: "an object",
}

// describe renders t for a message about its kind, without literal values
// or nil.
func describe(t *Type) string { return plain(t.NonNil()).String() }

var (
	stringKind     = kindSet(KindString)
	numberKind     = kindSet(KindNumber)
	listKind       = kindSet(KindList)
	objectKind     = kindSet(KindObject)
	listOrString   = kindSet(KindList, KindString)
	numberOrString = kindSet(KindNumber, KindString)
	sizedKinds     = kindSet(KindList, KindString, KindObject)
)

// param is one argument of a builtin.
type param struct {
	kinds kinds
	// nilOK is set when the builtin treats nil as empty or zero.
	nilOK bool
	// elems, if set, are the kinds a listKind argument's elements must all
	// have. They must be one kind and not nil.
	elems kinds
}

// sig is a builtin's signature, which Check enforces as the builtin does
// at run time.
type sig struct {
	params []param
	// variadic repeats the last param zero or more times.
	variadic bool
	// check, if set, reports rules that span arguments.
	check  func(args []*Type) error
	result func(args []*Type) *Type
}

func fixed(t *Type) func([]*Type) *Type { return func([]*Type) *Type { return t } }

// builtinSigs types the functions in Builtins, StringFuncs, MathFuncs and
// CollectionFuncs.
var builtinSigs = map[string]sig{
	"len":      {params: []param{{kinds: sizedKinds, nilOK: true}}, result: fixed(Number())},
	"string":   {params: []param{{nilOK: true}}, result: fixed(String())},
	"int":      {params: []param{{kinds: numberOrString, nilOK: true}}, result: fixed(Number())},
	"float":    {params: []param{{kinds: numberOrString, nilOK: true}}, result: fixed(Number())},
	"bool":     {params: []param{{nilOK: true}}, result: fixed(Bool())},
	"contains": {params: []param{{kinds: sizedKinds, nilOK: true}, {nilOK: true}}, check: containsNeedle, result: fixed(Bool())},
	"has":      {params: []param{{kinds: objectKind, nilOK: true}, {kinds: stringKind}}, result: fixed(Bool())},
	"keys":     {params: []param{{kinds: objectKind, nilOK: true}}, result: fixed(ListOf(String()))},
	"entries":  {params: []param{{kinds: objectKind, nilOK: true}}, result: entriesType},
	"lower":    {params: []param{{kinds: stringKind}}, result: fixed(String())},
	"upper":    {params: []param{{kinds: stringKind}}, result: fixed(String())},
	"sprintf":  {params: []param{{kinds: stringKind}, {nilOK: true}}, variadic: true, result: fixed(String())},

	"min":   {params: []param{{kinds: numberKind}, {kinds: numberKind}}, variadic: true, result: fixed(Number())},
	"max":   {params: []param{{kinds: numberKind}, {kinds: numberKind}}, variadic: true, result: fixed(Number())},
	"abs":   {params: []param{{kinds: numberKind}}, result: fixed(Number())},
	"floor": {params: []param{{kinds: numberKind}}, result: fixed(Number())},
	"ceil":  {params: []param{{kinds: numberKind}}, result: fixed(Number())},
	"round": {params: []param{{kinds: numberKind}}, result: fixed(Number())},

	"trim":       {params: []param{{kinds: stringKind}}, result: fixed(String())},
	"split":      {params: []param{{kinds: stringKind}, {kinds: stringKind}}, result: fixed(ListOf(String()))},
	"join":       {params: []param{{kinds: listKind, nilOK: true, elems: stringKind}, {kinds: stringKind}}, result: fixed(String())},
	"replace":    {params: []param{{kinds: stringKind}, {kinds: stringKind}, {kinds: stringKind}}, result: fixed(String())},
	"startsWith": {params: []param{{kinds: stringKind}, {kinds: stringKind}}, result: fixed(Bool())},
	"endsWith":   {params: []param{{kinds: stringKind}, {kinds: stringKind}}, result: fixed(Bool())},

	"first":   {params: []param{{kinds: listKind, nilOK: true}}, result: elemOrNil},
	"last":    {params: []param{{kinds: listKind, nilOK: true}}, result: elemOrNil},
	"sum":     {params: []param{{kinds: listKind, nilOK: true, elems: numberKind}}, result: fixed(Number())},
	"slice":   {params: []param{{kinds: listOrString, nilOK: true}, {kinds: numberKind}, {kinds: numberKind}}, result: sliceType},
	"sort":    {params: []param{{kinds: listKind, nilOK: true, elems: numberOrString}}, result: listType},
	"reverse": {params: []param{{kinds: listKind, nilOK: true}}, result: listType},
}

// builtin checks a call to a builtin against its signature.
func (c *checker) builtin(n *ast.CallExpr, name string, s sig, args []*Type) *Type {
	switch {
	case s.variadic && len(args) < len(s.params)-1:
		c.report(n, "%s expects at least %d argument(s), got %d", name, len(s.params)-1, len(args))
		return s.result(nil)
	case !s.variadic && len(args) != len(s.params):
		c.report(n, "%s expects %d argument(s), got %d", name, len(s.params), len(args))
		return s.result(nil)
	}
	for i, a := range args {
		p := s.params[min(i, len(s.params)-1)]
		c.argument(n.Args[i], name, i+1, p, a)
	}
	if s.check != nil {
		if err := s.check(args); err != nil {
			c.report(n, "%s: %v", name, err)
		}
	}
	return s.result(args)
}

func (c *checker) argument(node ast.Expr, name string, pos int, p param, t *Type) {
	if t.isAny() {
		return
	}
	if t.Kind == KindNull {
		if !p.nilOK && c.lenient == 0 {
			c.report(node, "%s: argument %d is always nil", name, pos)
		}
		return
	}
	if !p.nilOK {
		c.nilCheck(node, t)
	}
	for _, v := range variants(t) {
		if !p.kinds.has(v.Kind) {
			if t.IsUnion() {
				c.report(node, "%s: argument %d may be %s, and must be %s", name, pos, describe(v), p.kinds)
			} else {
				c.report(node, "%s: argument %d must be %s, not %s", name, pos, p.kinds, describe(t))
			}
			return
		}
	}
	if p.elems == 0 || t.Kind != KindList || t.Elem.isAny() {
		return
	}
	switch e := t.Elem; {
	case e.Kind == KindNull || e.Optional:
		c.report(node, "%s: argument %d has elements that may be nil", name, pos)
	case e.IsUnion():
		c.report(node, "%s: argument %d has elements that may be %s, and must all be one of %s", name, pos, e, p.elems)
	case !p.elems.has(e.Kind):
		c.report(node, "%s: argument %d must have %s elements, not %s", name, pos, p.elems, describe(e))
	}
}

// containsNeedle checks contains: a string haystack and an object (by key)
// need a string needle.
func containsNeedle(a []*Type) error {
	if len(a) != 2 || a[1].isAny() {
		return nil
	}
	for _, h := range variants(a[0]) {
		if h.Kind != KindString && h.Kind != KindObject {
			continue
		}
		if a[1].Kind != KindString || a[1].Optional {
			return fmt.Errorf("looking in %s needs a string, not %s", kindNames[h.Kind], a[1].String())
		}
	}
	return nil
}

func entriesType(a []*Type) *Type {
	value := Any
	if len(a) == 1 && a[0].Kind == KindObject && !(a[0].Open && a[0].Elem.isAny()) {
		var v *Type
		for _, f := range a[0].Fields {
			v = Join(v, orAny(f))
		}
		if a[0].Open {
			v = Join(v, a[0].Elem)
		}
		value = orAny(v)
	}
	return ListOf(ObjectOf(map[string]*Type{"key": String(), "value": value}))
}

func elemOrNil(a []*Type) *Type {
	if len(a) == 1 && a[0].Kind == KindList {
		return orAny(a[0].Elem).OrNil()
	}
	return Any
}

// listType is the result of sort and reverse: a new list, empty for nil.
func listType(a []*Type) *Type {
	if len(a) == 1 && a[0].Kind == KindList {
		return ListOf(orAny(a[0].Elem))
	}
	return ListOf(Any)
}

// sliceType is a list for a list or nil, and a string for a string.
func sliceType(a []*Type) *Type {
	if len(a) == 0 || a[0].isAny() {
		return Any
	}
	if a[0].Kind == KindString {
		if a[0].Optional {
			return Join(String(), ListOf(Any))
		}
		return String()
	}
	return listType(a[:1])
}
