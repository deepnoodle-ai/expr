package expr

import (
	"context"
	"fmt"
	"go/ast"
	"go/token"
	"math"
	"reflect"
	"strconv"
)

// Program is a compiled expression. Programs are immutable and safe for
// concurrent evaluation across goroutines.
type Program struct {
	source   string
	root     ast.Expr
	funcs    map[string]any
	prepared map[string]*preparedFunc

	// callCache maps CallExpr nodes to a pre-resolved preparedFunc for
	// the common case of a bare-identifier call target that resolves to
	// a registered function and is not shadowed by the runtime env.
	// Populated during compile(). Absent entries fall through to the
	// normal runtime resolution path.
	callCache map[*ast.CallExpr]*preparedFunc

	// litCache maps BasicLit nodes to their pre-parsed value so
	// evalLiteral is a single map lookup instead of repeating
	// strconv work on every Run.
	litCache map[*ast.BasicLit]any
}

// compile walks the root AST once to populate the lookup caches used
// during Run and to fold constant subtrees. Pre-resolving what we can
// at Compile time keeps the hot path allocation-free and
// reflection-free for common shapes.
func (p *Program) compile() {
	p.callCache = map[*ast.CallExpr]*preparedFunc{}
	p.litCache = map[*ast.BasicLit]any{}
	p.root = p.prewalk(p.root)
}

// constValue reports the pre-computed value of a folded/literal node,
// if any. It recognizes BasicLit (via litCache) and the three
// keyword-backed idents true/false/nil.
func (p *Program) constValue(node ast.Expr) (any, bool) {
	switch n := node.(type) {
	case *ast.BasicLit:
		if v, ok := p.litCache[n]; ok {
			return v, true
		}
	case *ast.Ident:
		switch n.Name {
		case "true":
			return true, true
		case "false":
			return false, true
		case "nil":
			return nil, true
		}
	}
	return nil, false
}

// wrapConst turns a folded value into an ast.Expr that eval can see
// as a constant. BasicLit-backed kinds populate litCache so eval
// skips the strconv re-parse. Bools and nil use the keyword idents.
func (p *Program) wrapConst(v any) (ast.Expr, bool) {
	switch x := v.(type) {
	case bool:
		if x {
			return &ast.Ident{Name: "true"}, true
		}
		return &ast.Ident{Name: "false"}, true
	case nil:
		return &ast.Ident{Name: "nil"}, true
	case int64:
		lit := &ast.BasicLit{Kind: token.INT, Value: strconv.FormatInt(x, 10)}
		p.litCache[lit] = x
		return lit, true
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil, false
		}
		lit := &ast.BasicLit{Kind: token.FLOAT, Value: strconv.FormatFloat(x, 'g', -1, 64)}
		p.litCache[lit] = x
		return lit, true
	case string:
		lit := &ast.BasicLit{Kind: token.STRING, Value: strconv.Quote(x)}
		p.litCache[lit] = x
		return lit, true
	}
	return nil, false
}

func (p *Program) prewalk(node ast.Expr) ast.Expr {
	switch n := node.(type) {
	case *ast.BasicLit:
		if v, err := evalLiteral(n); err == nil {
			p.litCache[n] = v
		}
		return n
	case *ast.ParenExpr:
		n.X = p.prewalk(n.X)
		// Unwrap parens around constants so constValue sees them.
		if _, ok := p.constValue(n.X); ok {
			return n.X
		}
		return n
	case *ast.UnaryExpr:
		n.X = p.prewalk(n.X)
		if xv, ok := p.constValue(n.X); ok {
			if v, err := applyUnary(n.Op, xv); err == nil {
				if wrapped, ok := p.wrapConst(v); ok {
					return wrapped
				}
			}
		}
		return n
	case *ast.BinaryExpr:
		n.X = p.prewalk(n.X)
		n.Y = p.prewalk(n.Y)
		// Skip logical ops: their short-circuit semantics are visible
		// to side-effects, and the LAND/LOR path in evalBinary is
		// fast enough that folding a pure-literal `true && false`
		// isn't worth a second code path.
		if n.Op != token.LAND && n.Op != token.LOR {
			if xv, ok := p.constValue(n.X); ok {
				if yv, ok2 := p.constValue(n.Y); ok2 {
					if v, err := applyBinary(n.Op, xv, yv); err == nil {
						if wrapped, ok := p.wrapConst(v); ok {
							return wrapped
						}
					}
				}
			}
		}
		return n
	case *ast.SelectorExpr:
		n.X = p.prewalk(n.X)
		return n
	case *ast.IndexExpr:
		n.X = p.prewalk(n.X)
		n.Index = p.prewalk(n.Index)
		return n
	case *ast.CallExpr:
		// Only cache bare-identifier call targets that resolve to a
		// registered, prepared function AND are not higher-order
		// special forms. The form dispatcher needs the raw CallExpr.
		if ident, ok := n.Fun.(*ast.Ident); ok {
			name := ident.Name
			if _, isForm := higherOrderForms[name]; !isForm && name != mapFormName {
				if pf, ok := p.prepared[name]; ok && (pf.native != nil || pf.fv.IsValid()) {
					p.callCache[n] = pf
				}
			}
		}
		for i, a := range n.Args {
			n.Args[i] = p.prewalk(a)
		}
		return n
	case *ast.CompositeLit:
		for i, e := range n.Elts {
			if kv, ok := e.(*ast.KeyValueExpr); ok {
				kv.Key = p.prewalk(kv.Key)
				kv.Value = p.prewalk(kv.Value)
			} else {
				n.Elts[i] = p.prewalk(e)
			}
		}
		return n
	}
	return node
}

// applyUnary is the constant-folding helper for unary ops. It mirrors
// the runtime evalUnary logic but works on already-evaluated values
// so the fold pass never re-enters the main evaluator.
func applyUnary(op token.Token, v any) (any, error) {
	switch op {
	case token.NOT:
		return !isTruthy(v), nil
	case token.SUB:
		if i, ok := toInt64(v); ok {
			return -i, nil
		}
		if f, ok := toFloat64(v); ok {
			return -f, nil
		}
		return nil, fmt.Errorf("%w: cannot negate %T", ErrEvaluate, v)
	case token.ADD:
		if _, ok := toInt64(v); ok {
			return v, nil
		}
		if _, ok := toFloat64(v); ok {
			return v, nil
		}
		return nil, fmt.Errorf("%w: cannot apply unary + to %T", ErrEvaluate, v)
	}
	return nil, fmt.Errorf("%w: unsupported unary operator %v", ErrEvaluate, op)
}

var _ Script = (*Program)(nil)

// Source returns the original expression text.
func (p *Program) Source() string { return p.source }

// Run evaluates the program against env. env may be a map[string]any, a
// struct, or a pointer to a struct — identifier lookups resolve to map
// keys, struct fields, or bound methods (in that order of preference).
// Identifiers not found in env are then looked up against the engine's
// registered functions. The literals true, false, and nil are recognized
// directly. Any unsupported syntax node (slice expressions, type
// assertions, function literals, channel operations, etc.) returns an
// error wrapping ErrEvaluate.
//
// expr checks ctx.Err() at the top of every AST node, so any
// pure-expression evaluation exits within one node of cancellation.
// Registered functions whose first parameter is context.Context receive
// ctx automatically; well-behaved callees can then cancel their own
// work. expr does not forcibly terminate user code that ignores ctx
// — Go provides no mechanism to kill a goroutine, and recovering from a
// blocked callee would leak it. Passing a nil ctx falls back to
// context.Background.
func (p *Program) Run(ctx context.Context, env any) (any, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	return p.eval(ctx, p.root, env, 0)
}

func (p *Program) eval(ctx context.Context, node ast.Expr, env any, depth int) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if depth >= MaxEvalDepth {
		return nil, fmt.Errorf("%w: expression nested too deeply (limit %d)", ErrEvaluate, MaxEvalDepth)
	}
	depth++
	switch n := node.(type) {
	case *ast.BasicLit:
		if v, ok := p.litCache[n]; ok {
			return v, nil
		}
		return evalLiteral(n)
	case *ast.Ident:
		return evalIdent(n, env, p.funcs)
	case *ast.ParenExpr:
		return p.eval(ctx, n.X, env, depth)
	case *ast.UnaryExpr:
		return p.evalUnary(ctx, n, env, depth)
	case *ast.BinaryExpr:
		return p.evalBinary(ctx, n, env, depth)
	case *ast.SelectorExpr:
		return p.evalSelector(ctx, n, env, depth)
	case *ast.IndexExpr:
		return p.evalIndex(ctx, n, env, depth)
	case *ast.CallExpr:
		return p.evalCall(ctx, n, env, depth)
	case *ast.CompositeLit:
		return p.evalCompositeLit(ctx, n, env, depth)
	}
	return nil, fmt.Errorf("%w: unsupported syntax %T", ErrEvaluate, node)
}

// evalCompositeLit evaluates the two composite-literal shapes expr
// accepts: `[]any{...}` and `map[string]any{...}`. These are what the
// jsonlit rewrite produces for bare `[...]` and `{...}` literals, and
// they are also accepted directly in source so users can write them
// without enabling WithJSONLiterals.
//
// Other type forms (fixed-size arrays, typed slices like `[]int{}`,
// maps with non-string keys, struct literals, etc.) are rejected —
// expr is untyped at the value level, so widening the accepted set
// would not change what the evaluator can represent.
func (p *Program) evalCompositeLit(ctx context.Context, n *ast.CompositeLit, env any, depth int) (any, error) {
	switch typ := n.Type.(type) {
	case *ast.ArrayType:
		if typ.Len != nil {
			return nil, fmt.Errorf("%w: fixed-size array literals are not supported", ErrEvaluate)
		}
		if ident, ok := typ.Elt.(*ast.Ident); !ok || ident.Name != "any" {
			return nil, fmt.Errorf("%w: only []any slice literals are supported", ErrEvaluate)
		}
		out := make([]any, len(n.Elts))
		for i, elt := range n.Elts {
			if _, isKV := elt.(*ast.KeyValueExpr); isKV {
				return nil, fmt.Errorf("%w: keyed elements are not supported in []any literals", ErrEvaluate)
			}
			v, err := p.eval(ctx, elt, env, depth)
			if err != nil {
				return nil, err
			}
			out[i] = v
		}
		return out, nil
	case *ast.MapType:
		kIdent, kOk := typ.Key.(*ast.Ident)
		vIdent, vOk := typ.Value.(*ast.Ident)
		if !kOk || !vOk || kIdent.Name != "string" || vIdent.Name != "any" {
			return nil, fmt.Errorf("%w: only map[string]any literals are supported", ErrEvaluate)
		}
		out := make(map[string]any, len(n.Elts))
		for _, elt := range n.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				return nil, fmt.Errorf("%w: map[string]any literal elements must be key:value pairs", ErrEvaluate)
			}
			k, err := p.eval(ctx, kv.Key, env, depth)
			if err != nil {
				return nil, err
			}
			ks, ok := k.(string)
			if !ok {
				return nil, fmt.Errorf("%w: map literal key must be string, got %T", ErrEvaluate, k)
			}
			v, err := p.eval(ctx, kv.Value, env, depth)
			if err != nil {
				return nil, err
			}
			out[ks] = v
		}
		return out, nil
	case nil:
		return nil, fmt.Errorf("%w: untyped composite literals are not supported", ErrEvaluate)
	}
	return nil, fmt.Errorf("%w: unsupported composite literal type %T", ErrEvaluate, n.Type)
}

func evalLiteral(n *ast.BasicLit) (any, error) {
	switch n.Kind {
	case token.INT:
		i, err := strconv.ParseInt(n.Value, 0, 64)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrEvaluate, err)
		}
		return i, nil
	case token.FLOAT:
		f, err := strconv.ParseFloat(n.Value, 64)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrEvaluate, err)
		}
		return f, nil
	case token.STRING:
		s, err := strconv.Unquote(n.Value)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrEvaluate, err)
		}
		return s, nil
	case token.CHAR:
		s, err := strconv.Unquote(n.Value)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrEvaluate, err)
		}
		runes := []rune(s)
		if len(runes) != 1 {
			return nil, fmt.Errorf("%w: invalid char literal %q", ErrEvaluate, n.Value)
		}
		return int64(runes[0]), nil
	}
	return nil, fmt.Errorf("%w: unsupported literal kind %v", ErrEvaluate, n.Kind)
}

func evalIdent(n *ast.Ident, env any, funcs map[string]any) (any, error) {
	switch n.Name {
	case "true":
		return true, nil
	case "false":
		return false, nil
	case "nil":
		return nil, nil
	}
	if v, ok := lookupEnv(env, n.Name); ok {
		return v, nil
	}
	if fn, ok := funcs[n.Name]; ok {
		return fn, nil
	}
	user := displayIdent(n.Name)
	return nil, fmt.Errorf("%w: undefined identifier %q%s",
		ErrEvaluate, user, identHint(env, funcs, user))
}

// lookupEnv resolves a top-level identifier against env. env may be a
// map[string]any, any map with string keys, a struct, a pointer to a
// struct, or an *itEnv wrapping one of the above. For structs, fields
// are preferred over methods when both match. Methods are returned as
// bound function values so they can be invoked by a CallExpr node. An
// itEnv binds `it` and `index` ahead of anything in its parent.
func lookupEnv(env any, name string) (any, bool) {
	if env == nil {
		return nil, false
	}
	if it, ok := env.(*itEnv); ok {
		switch name {
		case "it":
			return it.it, true
		case "index":
			return it.index, true
		}
		return lookupEnv(it.parent, name)
	}
	if m, ok := env.(map[string]any); ok {
		v, ok := m[name]
		return v, ok
	}
	rv := reflect.ValueOf(env)
	// Check methods on the original (possibly pointer) value first so
	// pointer-receiver methods are visible — but prefer struct fields if
	// both exist with the same name.
	orig := rv
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil, false
		}
		rv = rv.Elem()
	}
	switch rv.Kind() {
	case reflect.Struct:
		if fv := rv.FieldByName(name); fv.IsValid() && fv.CanInterface() {
			return fv.Interface(), true
		}
		if mv := orig.MethodByName(name); mv.IsValid() {
			return mv.Interface(), true
		}
	case reflect.Map:
		if rv.Type().Key().Kind() == reflect.String {
			mv := rv.MapIndex(reflect.ValueOf(name))
			if mv.IsValid() {
				return mv.Interface(), true
			}
		}
	}
	return nil, false
}

func (p *Program) evalUnary(ctx context.Context, n *ast.UnaryExpr, env any, depth int) (any, error) {
	v, err := p.eval(ctx, n.X, env, depth)
	if err != nil {
		return nil, err
	}
	switch n.Op {
	case token.NOT:
		return !isTruthy(v), nil
	case token.SUB:
		if i, ok := toInt64(v); ok {
			return -i, nil
		}
		if f, ok := toFloat64(v); ok {
			return -f, nil
		}
		return nil, fmt.Errorf("%w: cannot negate %T", ErrEvaluate, v)
	case token.ADD:
		if _, ok := toInt64(v); ok {
			return v, nil
		}
		if _, ok := toFloat64(v); ok {
			return v, nil
		}
		return nil, fmt.Errorf("%w: cannot apply unary + to %T", ErrEvaluate, v)
	}
	return nil, fmt.Errorf("%w: unsupported unary operator %v", ErrEvaluate, n.Op)
}

func (p *Program) evalBinary(ctx context.Context, n *ast.BinaryExpr, env any, depth int) (any, error) {
	// Short-circuit logical operators: right-hand side is not evaluated
	// when the left-hand side is sufficient to determine the result.
	if n.Op == token.LAND || n.Op == token.LOR {
		lhs, err := p.eval(ctx, n.X, env, depth)
		if err != nil {
			return nil, err
		}
		lt := isTruthy(lhs)
		if n.Op == token.LAND && !lt {
			return false, nil
		}
		if n.Op == token.LOR && lt {
			return true, nil
		}
		rhs, err := p.eval(ctx, n.Y, env, depth)
		if err != nil {
			return nil, err
		}
		return isTruthy(rhs), nil
	}

	lhs, err := p.eval(ctx, n.X, env, depth)
	if err != nil {
		return nil, err
	}
	rhs, err := p.eval(ctx, n.Y, env, depth)
	if err != nil {
		return nil, err
	}
	return applyBinary(n.Op, lhs, rhs)
}

func applyBinary(op token.Token, lhs, rhs any) (any, error) {
	// String concatenation and comparison.
	if ls, lok := lhs.(string); lok {
		if rs, rok := rhs.(string); rok {
			switch op {
			case token.ADD:
				return ls + rs, nil
			case token.EQL:
				return ls == rs, nil
			case token.NEQ:
				return ls != rs, nil
			case token.LSS:
				return ls < rs, nil
			case token.GTR:
				return ls > rs, nil
			case token.LEQ:
				return ls <= rs, nil
			case token.GEQ:
				return ls >= rs, nil
			}
		}
	}

	// Equality across arbitrary comparable types.
	if op == token.EQL || op == token.NEQ {
		if eq, ok := looseEqual(lhs, rhs); ok {
			if op == token.NEQ {
				return !eq, nil
			}
			return eq, nil
		}
	}

	// Numeric operations: stay in int64 when both sides are integral,
	// promote to float64 otherwise.
	li, liOk := toInt64(lhs)
	ri, riOk := toInt64(rhs)
	if liOk && riOk {
		switch op {
		case token.ADD:
			return li + ri, nil
		case token.SUB:
			return li - ri, nil
		case token.MUL:
			return li * ri, nil
		case token.QUO:
			if ri == 0 {
				return nil, fmt.Errorf("%w: division by zero", ErrEvaluate)
			}
			return li / ri, nil
		case token.REM:
			if ri == 0 {
				return nil, fmt.Errorf("%w: modulo by zero", ErrEvaluate)
			}
			return li % ri, nil
		case token.LSS:
			return li < ri, nil
		case token.GTR:
			return li > ri, nil
		case token.LEQ:
			return li <= ri, nil
		case token.GEQ:
			return li >= ri, nil
		}
	}

	lf, lfOk := toFloat64(lhs)
	rf, rfOk := toFloat64(rhs)
	if lfOk && rfOk {
		switch op {
		case token.ADD:
			return lf + rf, nil
		case token.SUB:
			return lf - rf, nil
		case token.MUL:
			return lf * rf, nil
		case token.QUO:
			if rf == 0 {
				return nil, fmt.Errorf("%w: division by zero", ErrEvaluate)
			}
			return lf / rf, nil
		case token.REM:
			if rf == 0 {
				return nil, fmt.Errorf("%w: modulo by zero", ErrEvaluate)
			}
			return math.Mod(lf, rf), nil
		case token.LSS:
			return lf < rf, nil
		case token.GTR:
			return lf > rf, nil
		case token.LEQ:
			return lf <= rf, nil
		case token.GEQ:
			return lf >= rf, nil
		}
	}

	return nil, fmt.Errorf("%w: operator %v not supported for %T and %T", ErrEvaluate, op, lhs, rhs)
}

func (p *Program) evalSelector(ctx context.Context, n *ast.SelectorExpr, env any, depth int) (any, error) {
	// Fast path: an ident-rooted selector chain (a.b.c.d) can be walked
	// entirely through reflect.Value, materializing only the leaf via
	// Interface(). The general path below boxes every intermediate
	// result through any, which copies struct field data — a 10 MiB
	// embedded array gets memcpy'd on each access. The fast path skips
	// plain map[string]any roots because the type-asserted map lookup
	// in selectField is already faster than reflect MapIndex.
	if v, ok, err := evalSelectorChainRV(n, env); ok {
		return v, err
	}
	recv, err := p.eval(ctx, n.X, env, depth)
	if err != nil {
		return nil, err
	}
	return selectField(recv, n.Sel.Name)
}

// evalSelectorChainRV walks an ident-rooted selector chain in reflect
// space. ok is true when the fast path was taken (regardless of error);
// when false the caller must use the general path. The fast path
// declines for non-ident roots, for plain map[string]any envs (the
// general path is faster there), and for itEnv lookups of `it`/`index`
// whose values do not benefit.
func evalSelectorChainRV(n *ast.SelectorExpr, env any) (any, bool, error) {
	if _, isMap := env.(map[string]any); isMap {
		return nil, false, nil
	}
	// Collect chain names leaf-first and find the root.
	var names []string
	cur := ast.Expr(n)
	for {
		sel, isSel := cur.(*ast.SelectorExpr)
		if !isSel {
			break
		}
		names = append(names, sel.Sel.Name)
		cur = sel.X
	}
	ident, ok := cur.(*ast.Ident)
	if !ok {
		return nil, false, nil
	}
	switch ident.Name {
	case "true", "false", "nil":
		return nil, false, nil
	}
	rv, ok := lookupEnvRV(env, ident.Name)
	if !ok {
		return nil, false, nil
	}
	for i := len(names) - 1; i >= 0; i-- {
		next, err := selectFieldRV(rv, displayIdent(names[i]))
		if err != nil {
			return nil, true, err
		}
		rv = next
	}
	if !rv.IsValid() {
		return nil, true, nil
	}
	if !rv.CanInterface() {
		return nil, true, fmt.Errorf("%w: field %q not accessible",
			ErrEvaluate, displayIdent(names[0]))
	}
	return rv.Interface(), true, nil
}

// lookupEnvRV mirrors lookupEnv but returns a reflect.Value so callers
// can chain field accesses without boxing intermediate struct values.
// Map and itEnv lookups still go through any internally because their
// stored values already live behind interfaces.
func lookupEnvRV(env any, name string) (reflect.Value, bool) {
	if env == nil {
		return reflect.Value{}, false
	}
	if it, ok := env.(*itEnv); ok {
		switch name {
		case "it":
			return reflect.ValueOf(it.it), true
		case "index":
			return reflect.ValueOf(it.index), true
		}
		return lookupEnvRV(it.parent, name)
	}
	if m, ok := env.(map[string]any); ok {
		v, ok := m[name]
		if !ok {
			return reflect.Value{}, false
		}
		return reflect.ValueOf(v), true
	}
	rv := reflect.ValueOf(env)
	orig := rv
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return reflect.Value{}, false
		}
		rv = rv.Elem()
	}
	switch rv.Kind() {
	case reflect.Struct:
		if fv := rv.FieldByName(name); fv.IsValid() && fv.CanInterface() {
			return fv, true
		}
		if mv := orig.MethodByName(name); mv.IsValid() {
			return mv, true
		}
	case reflect.Map:
		if rv.Type().Key().Kind() == reflect.String {
			mv := rv.MapIndex(reflect.ValueOf(name))
			if mv.IsValid() {
				return mv, true
			}
		}
	}
	return reflect.Value{}, false
}

// selectFieldRV reads a field/key from rv without boxing the parent
// through any. It mirrors selectField's behavior on structs and
// string-keyed maps, transparently dereffing pointers and unwrapping
// interface values. Errors are produced lazily — the cold path may
// call Interface() to build a "did you mean" hint.
func selectFieldRV(rv reflect.Value, name string) (reflect.Value, error) {
	for rv.Kind() == reflect.Interface && !rv.IsNil() {
		rv = rv.Elem()
	}
	if !rv.IsValid() {
		return reflect.Value{}, fmt.Errorf("%w: cannot access %q on nil", ErrEvaluate, name)
	}
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return reflect.Value{}, fmt.Errorf("%w: cannot access %q on nil pointer", ErrEvaluate, name)
		}
		rv = rv.Elem()
	}
	switch rv.Kind() {
	case reflect.Struct:
		fv := rv.FieldByName(name)
		if !fv.IsValid() || !fv.CanInterface() {
			return reflect.Value{}, fmt.Errorf("%w: field %q not found on %v%s",
				ErrEvaluate, name, rv.Type(), fieldHint(rv.Interface(), name))
		}
		return fv, nil
	case reflect.Map:
		if rv.Type().Key().Kind() != reflect.String {
			return reflect.Value{}, fmt.Errorf("%w: cannot select %q on map with non-string keys", ErrEvaluate, name)
		}
		mv := rv.MapIndex(reflect.ValueOf(name))
		if !mv.IsValid() {
			return reflect.Value{}, fmt.Errorf("%w: key %q not found%s",
				ErrEvaluate, name, fieldHint(rv.Interface(), name))
		}
		return mv, nil
	}
	return reflect.Value{}, fmt.Errorf("%w: cannot select %q on %v", ErrEvaluate, name, rv.Type())
}

func selectField(recv any, name string) (any, error) {
	// Translate rewritten identifiers (currently only `map`) back to
	// their user-visible form so field/key/error lookups match what
	// the user actually typed.
	name = displayIdent(name)
	if recv == nil {
		return nil, fmt.Errorf("%w: cannot access %q on nil", ErrEvaluate, name)
	}
	if m, ok := recv.(map[string]any); ok {
		v, ok := m[name]
		if !ok {
			return nil, fmt.Errorf("%w: key %q not found%s",
				ErrEvaluate, name, fieldHint(recv, name))
		}
		return v, nil
	}
	rv := reflect.ValueOf(recv)
	switch rv.Kind() {
	case reflect.Map:
		if rv.Type().Key().Kind() != reflect.String {
			return nil, fmt.Errorf("%w: cannot select %q on map with non-string keys", ErrEvaluate, name)
		}
		mv := rv.MapIndex(reflect.ValueOf(name))
		if !mv.IsValid() {
			return nil, fmt.Errorf("%w: key %q not found%s",
				ErrEvaluate, name, fieldHint(recv, name))
		}
		return mv.Interface(), nil
	case reflect.Struct:
		fv := rv.FieldByName(name)
		if !fv.IsValid() {
			return nil, fmt.Errorf("%w: field %q not found on %T%s",
				ErrEvaluate, name, recv, fieldHint(recv, name))
		}
		if !fv.CanInterface() {
			// Unexported field: deny access rather than panicking via Interface().
			return nil, fmt.Errorf("%w: field %q not found on %T%s",
				ErrEvaluate, name, recv, fieldHint(recv, name))
		}
		return fv.Interface(), nil
	case reflect.Pointer:
		if rv.IsNil() {
			return nil, fmt.Errorf("%w: cannot access %q on nil pointer", ErrEvaluate, name)
		}
		return selectField(rv.Elem().Interface(), name)
	}
	return nil, fmt.Errorf("%w: cannot select %q on %T", ErrEvaluate, name, recv)
}

func (p *Program) evalIndex(ctx context.Context, n *ast.IndexExpr, env any, depth int) (any, error) {
	// Fast path: filter(xs, p)[k] with a small constant k stops at the
	// (k+1)-th match instead of materializing the full filtered slice.
	if v, ok, err := p.tryFilterIndex(ctx, n, env, depth); ok {
		return v, err
	}
	recv, err := p.eval(ctx, n.X, env, depth)
	if err != nil {
		return nil, err
	}
	idx, err := p.eval(ctx, n.Index, env, depth)
	if err != nil {
		return nil, err
	}
	return indexValue(recv, idx)
}

// tryFilterIndex recognises `filter(xs, predicate)[N]` where N is a
// non-negative integer literal and `filter` is the built-in special
// form (not shadowed by env or funcs). When matched, it iterates xs
// and stops as soon as the N-th matching element is found, mirroring
// the result of the general path without allocating the intermediate
// slice. ok is true when the fast path was taken.
func (p *Program) tryFilterIndex(ctx context.Context, n *ast.IndexExpr, env any, depth int) (any, bool, error) {
	call, ok := n.X.(*ast.CallExpr)
	if !ok {
		return nil, false, nil
	}
	ident, ok := call.Fun.(*ast.Ident)
	if !ok || ident.Name != "filter" || len(call.Args) != 2 {
		return nil, false, nil
	}
	// Respect identifier shadowing: a user-registered or env-bound
	// "filter" goes through the general call path.
	if _, inEnv := lookupEnv(env, "filter"); inEnv {
		return nil, false, nil
	}
	if _, inFuncs := p.funcs["filter"]; inFuncs {
		return nil, false, nil
	}
	lit, ok := n.Index.(*ast.BasicLit)
	if !ok || lit.Kind != token.INT {
		return nil, false, nil
	}
	target, err := strconv.ParseInt(lit.Value, 0, 64)
	if err != nil || target < 0 {
		return nil, false, nil
	}

	// Stream the collection via reflect rather than materializing it
	// through iterItems — for filter(xs, p)[0] over a 1000-element
	// slice, the unstreamed path allocates 1000 any-boxes only to
	// throw 999 of them away.
	coll, err := p.eval(ctx, call.Args[0], env, depth)
	if err != nil {
		return nil, true, err
	}
	if coll == nil {
		return nil, true, fmt.Errorf("%w: index %d out of range", ErrEvaluate, target)
	}
	rv := reflect.ValueOf(coll)
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return nil, true, fmt.Errorf("%w: filter expects a list as its first argument, got %T",
			ErrEvaluate, coll)
	}
	scope := &itEnv{parent: env}
	var seen int64
	for i := 0; i < rv.Len(); i++ {
		item := rv.Index(i).Interface()
		scope.it = item
		scope.index = int64(i)
		v, err := p.eval(ctx, call.Args[1], scope, depth)
		if err != nil {
			return nil, true, err
		}
		if !isTruthy(v) {
			continue
		}
		if seen == target {
			return item, true, nil
		}
		seen++
	}
	return nil, true, fmt.Errorf("%w: index %d out of range", ErrEvaluate, target)
}

func indexValue(recv, idx any) (any, error) {
	if recv == nil {
		return nil, fmt.Errorf("%w: cannot index nil", ErrEvaluate)
	}
	if m, ok := recv.(map[string]any); ok {
		key, ok := idx.(string)
		if !ok {
			return nil, fmt.Errorf("%w: map index must be string, got %T", ErrEvaluate, idx)
		}
		v, ok := m[key]
		if !ok {
			return nil, fmt.Errorf("%w: key %q not found%s",
				ErrEvaluate, key, fieldHint(recv, key))
		}
		return v, nil
	}
	rv := reflect.ValueOf(recv)
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		i, ok := toInt64(idx)
		if !ok {
			return nil, fmt.Errorf("%w: index must be integer, got %T", ErrEvaluate, idx)
		}
		if i < 0 || i >= int64(rv.Len()) {
			return nil, fmt.Errorf("%w: index %d out of range [0, %d)", ErrEvaluate, i, rv.Len())
		}
		return rv.Index(int(i)).Interface(), nil
	case reflect.String:
		i, ok := toInt64(idx)
		if !ok {
			return nil, fmt.Errorf("%w: index must be integer, got %T", ErrEvaluate, idx)
		}
		runes := []rune(rv.String())
		if i < 0 || i >= int64(len(runes)) {
			return nil, fmt.Errorf("%w: index %d out of range [0, %d)", ErrEvaluate, i, len(runes))
		}
		return string(runes[i]), nil
	case reflect.Map:
		keyType := rv.Type().Key()
		if idx == nil {
			// Untyped nil is only a valid key for nilable key kinds; rather
			// than handling every combination via reflect we reject it
			// — reflect.ValueOf(nil).Type() would otherwise panic.
			return nil, fmt.Errorf("%w: cannot use nil as map key %v", ErrEvaluate, keyType)
		}
		kv := reflect.ValueOf(idx)
		if !kv.Type().AssignableTo(keyType) {
			if !kv.Type().ConvertibleTo(keyType) {
				return nil, fmt.Errorf("%w: cannot use %T as map key %v", ErrEvaluate, idx, keyType)
			}
			kv = kv.Convert(keyType)
		}
		mv := rv.MapIndex(kv)
		if !mv.IsValid() {
			return nil, fmt.Errorf("%w: key %v not found", ErrEvaluate, idx)
		}
		return mv.Interface(), nil
	}
	return nil, fmt.Errorf("%w: cannot index %T", ErrEvaluate, recv)
}

func (p *Program) evalCall(ctx context.Context, n *ast.CallExpr, env any, depth int) (any, error) {
	// Fast path: bare-identifier call target that resolved to a
	// registered prepared function at compile time, and is not
	// shadowed by the runtime env. This covers the overwhelming
	// majority of builtin call sites (len, has, contains, ...).
	if pf := p.callCache[n]; pf != nil {
		if _, shadowed := lookupEnv(env, pf.name); !shadowed {
			args := make([]any, len(n.Args))
			for i, a := range n.Args {
				v, err := p.eval(ctx, a, env, depth)
				if err != nil {
					return nil, err
				}
				args[i] = v
			}
			return callPrepared(ctx, pf, args)
		}
	}

	// Higher-order special forms intercept the normal call path when
	// the target is a bare identifier that has not been overridden in
	// the caller's env or funcs. They receive the predicate AST
	// unevaluated so it can be re-run per element with `it`/`index`
	// bound in an itEnv scope. Identifier shadowing follows the same
	// env→funcs order used by resolveCallable, so users can always
	// register their own `map` or `filter` if they prefer.
	//
	// `map` is special: the preprocessing pass rewrote its token to
	// mapFormName so Go's parser would accept it, but shadowing
	// checks still have to use the user-visible name "map" — a user
	// who calls WithFunctions({"map": ...}) expects their function
	// to win, and env entries named "map" should be honored too.
	if ident, isIdent := n.Fun.(*ast.Ident); isIdent {
		if ident.Name == mapFormName {
			if v, ok := lookupEnv(env, "map"); ok {
				return p.callValue(ctx, "map", v, n.Args, env, depth)
			}
			if v, ok := p.funcs["map"]; ok {
				return p.callValue(ctx, "map", v, n.Args, env, depth)
			}
			return formMap(p, ctx, n, env, depth)
		}
		if form, isForm := higherOrderForms[ident.Name]; isForm {
			if _, inEnv := lookupEnv(env, ident.Name); !inEnv {
				if _, inFuncs := p.funcs[ident.Name]; !inFuncs {
					return form(p, ctx, n, env, depth)
				}
			}
		}
	}

	name, fn, err := p.resolveCallable(ctx, n.Fun, env, depth)
	if err != nil {
		return nil, err
	}
	args := make([]any, len(n.Args))
	for i, a := range n.Args {
		v, err := p.eval(ctx, a, env, depth)
		if err != nil {
			return nil, err
		}
		args[i] = v
	}
	return callFunction(ctx, name, fn, args)
}

// callValue evaluates argExprs and invokes an already-resolved
// function value under the given user-visible name. It exists so
// the `map`-rewrite shadowing path in evalCall can call a user
// override without going back through identifier resolution (which
// would look up the internal mapFormName instead of "map").
func (p *Program) callValue(ctx context.Context, name string, fn any, argExprs []ast.Expr, env any, depth int) (any, error) {
	args := make([]any, len(argExprs))
	for i, a := range argExprs {
		v, err := p.eval(ctx, a, env, depth)
		if err != nil {
			return nil, err
		}
		args[i] = v
	}
	return callFunction(ctx, name, fn, args)
}

// resolveCallable finds the function value for a call target. It supports
// bare identifiers (looked up in env, then in the registered builtins) and
// selector expressions (e.g. `state.user.Greet`), which resolve to bound
// methods on structs/pointers or callable values stored inside maps.
func (p *Program) resolveCallable(ctx context.Context, fun ast.Expr, env any, depth int) (string, any, error) {
	switch f := fun.(type) {
	case *ast.Ident:
		if v, ok := lookupEnv(env, f.Name); ok {
			return f.Name, v, nil
		}
		if v, ok := p.funcs[f.Name]; ok {
			return f.Name, v, nil
		}
		return "", nil, fmt.Errorf("%w: unknown function %q%s",
			ErrEvaluate, f.Name, identHint(env, p.funcs, f.Name))
	case *ast.SelectorExpr:
		recv, err := p.eval(ctx, f.X, env, depth)
		if err != nil {
			return "", nil, err
		}
		fn, err := resolveMethod(recv, f.Sel.Name)
		if err != nil {
			return "", nil, err
		}
		return displayIdent(f.Sel.Name), fn, nil
	}
	return "", nil, fmt.Errorf("%w: unsupported call target %T", ErrEvaluate, fun)
}

// resolveMethod looks up a callable attribute `name` on recv. For structs
// and pointers it returns a bound method value; for map[string]any (or any
// map with string keys) it returns the stored value; for map-like types
// with other key kinds it fails. Missing methods return a descriptive error.
func resolveMethod(recv any, name string) (any, error) {
	// Translate rewritten identifiers (currently only `map`) back to
	// their user-visible form so reflect.MethodByName and map lookups
	// match what the user actually typed.
	name = displayIdent(name)
	if recv == nil {
		return nil, fmt.Errorf("%w: cannot call %q on nil", ErrEvaluate, name)
	}
	if m, ok := recv.(map[string]any); ok {
		if v, ok := m[name]; ok {
			return v, nil
		}
		return nil, fmt.Errorf("%w: %q not found%s",
			ErrEvaluate, name, fieldHint(recv, name))
	}
	rv := reflect.ValueOf(recv)
	if rv.Kind() == reflect.Pointer && rv.IsNil() {
		return nil, fmt.Errorf("%w: cannot call %q on nil pointer", ErrEvaluate, name)
	}
	if mv := rv.MethodByName(name); mv.IsValid() {
		return mv.Interface(), nil
	}
	if rv.Kind() == reflect.Pointer {
		rv = rv.Elem()
		if mv := rv.MethodByName(name); mv.IsValid() {
			return mv.Interface(), nil
		}
	}
	switch rv.Kind() {
	case reflect.Struct:
		if fv := rv.FieldByName(name); fv.IsValid() && fv.CanInterface() {
			return fv.Interface(), nil
		}
	case reflect.Map:
		if rv.Type().Key().Kind() == reflect.String {
			mv := rv.MapIndex(reflect.ValueOf(name))
			if mv.IsValid() {
				return mv.Interface(), nil
			}
		}
	}
	return nil, fmt.Errorf("%w: method %q not found on %T%s",
		ErrEvaluate, name, recv, fieldHint(recv, name))
}
