package expr

// Tests that the code snippets in docs/guides/*.md actually compile
// and evaluate to the result each guide claims. If this file drifts
// from a guide, fix the guide — these are the canonical expected
// outputs. Pattern mirrors docs_examples_test.go.

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func runGuide(t *testing.T, src string, env any, opts ...Option) any {
	t.Helper()
	if len(opts) == 0 {
		opts = []Option{WithBuiltins()}
	}
	p, err := Compile(src, opts...)
	if err != nil {
		t.Fatalf("compile: %v\nsource:\n%s", err, src)
	}
	v, err := p.Run(t.Context(), env)
	if err != nil {
		t.Fatalf("run: %v\nsource:\n%s", err, src)
	}
	return v
}

// --- registering-functions.md ---------------------------------------------

func TestGuide_RegisteringFunctions_ReturnShapes(t *testing.T) {
	funcs := map[string]any{
		"upper": strings.ToUpper,
		"parseAge": func(s string) (int64, error) {
			var n int64
			if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
				return 0, fmt.Errorf("parseAge: %w", err)
			}
			return n, nil
		},
	}
	got := runGuide(t, `parseAge("36") + 4`, map[string]any{}, WithFunctions(funcs))
	assertDeepEqual(t, got, int64(40))

	got = runGuide(t, `upper("ada")`, map[string]any{}, WithFunctions(funcs))
	assertDeepEqual(t, got, "ADA")
}

func TestGuide_RegisteringFunctions_ContextInjection(t *testing.T) {
	funcs := map[string]any{
		"fetchLen": func(ctx context.Context, prefix string) int64 {
			if ctx == nil {
				t.Fatal("ctx was nil")
			}
			return int64(len(prefix) + 10)
		},
	}
	p, err := Compile(`fetchLen("hello-")`, WithFunctions(funcs))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	v, err := p.Run(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	assertDeepEqual(t, v, int64(16))
}

func TestGuide_RegisteringFunctions_VariadicSprintf(t *testing.T) {
	funcs := map[string]any{"sprintf": fmt.Sprintf}
	env := map[string]any{"name": "ada", "age": int64(36)}
	got := runGuide(t, `sprintf("%s is %d", name, age)`, env, WithFunctions(funcs))
	assertDeepEqual(t, got, "ada is 36")
}

func TestGuide_RegisteringFunctions_NumericRangeCheck(t *testing.T) {
	// int8 narrowing rejects values out of range, as the guide claims.
	funcs := map[string]any{
		"clamp": func(n int8) int8 { return n },
	}
	p, err := Compile(`clamp(999)`, WithFunctions(funcs))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	_, err = p.Run(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected range error, got nil")
	}
	if !errors.Is(err, ErrEvaluate) {
		t.Fatalf("expected ErrEvaluate, got %v", err)
	}
}

// --- designing-an-env.md ---------------------------------------------------

type guideLineItem struct {
	SKU   string
	Price float64
}

type guideOrder struct {
	ID    string
	Items []guideLineItem
	Meta  map[string]any
}

func (o *guideOrder) Subtotal() float64 {
	var s float64
	for _, it := range o.Items {
		s += it.Price
	}
	return s
}

type guideOrderView struct {
	ID       string
	Items    []guideLineItem
	Meta     map[string]any
	subtotal float64
}

func (v guideOrderView) Subtotal() float64 { return v.subtotal }

func TestGuide_DesigningEnv_PointerStruct(t *testing.T) {
	o := &guideOrder{
		ID:    "A-1",
		Items: []guideLineItem{{SKU: "a", Price: 60}, {SKU: "b", Price: 80}},
		Meta:  map[string]any{"source": "web"},
	}
	src := `Subtotal() > 100 && len(Items) >= 2 && !has(Meta, "refunded")`
	got := runGuide(t, src, o)
	assertDeepEqual(t, got, true)
}

func TestGuide_DesigningEnv_ReadOnlyView(t *testing.T) {
	o := &guideOrder{
		Items: []guideLineItem{{Price: 60}, {Price: 80}},
		Meta:  map[string]any{"source": "web"},
	}
	view := guideOrderView{
		ID: o.ID, Items: o.Items, Meta: o.Meta, subtotal: o.Subtotal(),
	}
	src := `Subtotal() > 100 && len(Items) >= 2 && !has(Meta, "refunded")`
	got := runGuide(t, src, view)
	assertDeepEqual(t, got, true)
}

func TestGuide_DesigningEnv_MissingKeyIsError(t *testing.T) {
	p, err := Compile(`user.nickname`, WithBuiltins())
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	_, err = p.Run(context.Background(), map[string]any{
		"user": map[string]any{"id": 42},
	})
	if err == nil {
		t.Fatal("expected missing-key error")
	}
	if !errors.Is(err, ErrEvaluate) {
		t.Fatalf("expected ErrEvaluate, got %v", err)
	}
}

func TestGuide_DesigningEnv_ExplicitNilAllowed(t *testing.T) {
	env := map[string]any{
		"user": map[string]any{
			"id":       42,
			"nickname": nil,
		},
	}
	got := runGuide(t, `user.nickname == nil`, env)
	assertDeepEqual(t, got, true)
}

// --- sandboxing.md ---------------------------------------------------------

func TestGuide_Sandboxing_PolicyEval(t *testing.T) {
	src := `user.role == "admin" || (user.role == "viewer" && resource.public)`
	env := map[string]any{
		"user":     map[string]any{"role": "viewer"},
		"resource": map[string]any{"public": true},
	}
	got := runGuide(t, src, env)
	assertDeepEqual(t, got, true)
}

func TestGuide_Sandboxing_DeadlineInRegisteredFunc(t *testing.T) {
	// A registered function that honors ctx should see the deadline
	// when the caller cancels. The guide claims the caller can match
	// context.DeadlineExceeded via errors.Is.
	funcs := map[string]any{
		"slow": func(ctx context.Context) (bool, error) {
			select {
			case <-ctx.Done():
				return false, ctx.Err()
			case <-time.After(50 * time.Millisecond):
				return true, nil
			}
		},
	}
	p, err := Compile(`slow()`, WithFunctions(funcs))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	_, err = p.Run(ctx, map[string]any{})
	if err == nil {
		t.Fatal("expected deadline error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}
}

func TestGuide_Sandboxing_LanguageDeniesBitwise(t *testing.T) {
	// The guide claims bitwise ops parse but error at eval.
	p, err := Compile(`1 & 2`, WithBuiltins())
	if err != nil {
		return // if it errors at compile time that's also fine
	}
	_, err = p.Run(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected eval error for bitwise op, got nil")
	}
}

// --- templates.md ----------------------------------------------------------

func TestGuide_Templates_CompileOnceRenderMany(t *testing.T) {
	tmpl, err := NewTemplate(
		`Hi ${user.name}! You have ${len(user.tasks)} task(s).`,
		WithBuiltins(),
	)
	if err != nil {
		t.Fatalf("NewTemplate: %v", err)
	}

	users := []map[string]any{
		{"name": "Ada", "tasks": []any{"a", "b"}},
		{"name": "Grace", "tasks": []any{"x"}},
	}
	wants := []string{
		"Hi Ada! You have 2 task(s).",
		"Hi Grace! You have 1 task(s).",
	}
	for i, u := range users {
		out, err := tmpl.Render(context.Background(), map[string]any{"user": u})
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		if out != wants[i] {
			t.Fatalf("render %d: got %q want %q", i, out, wants[i])
		}
	}
}

func TestGuide_Templates_JoinHelper(t *testing.T) {
	opts := []Option{
		WithBuiltins(),
		WithFunctions(map[string]any{
			"join": func(xs []any, sep string) string {
				parts := make([]string, len(xs))
				for i, v := range xs {
					parts[i] = fmt.Sprintf("%v", v)
				}
				return strings.Join(parts, sep)
			},
			"pluralize": func(n int64, singular, plural string) string {
				if n == 1 {
					return singular
				}
				return plural
			},
		}),
	}
	tmpl, err := NewTemplate(
		`${len(items)} ${pluralize(len(items), "item", "items")}: ${join(items, ", ")}.`,
		opts...,
	)
	if err != nil {
		t.Fatalf("NewTemplate: %v", err)
	}
	out, err := tmpl.Render(context.Background(), map[string]any{
		"items": []any{"apple", "pear", "kiwi"},
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	want := "3 items: apple, pear, kiwi."
	if out != want {
		t.Fatalf("got %q want %q", out, want)
	}
}

func TestGuide_Templates_DollarEscape(t *testing.T) {
	tmpl, err := NewTemplate(`Price: $${total}`, WithBuiltins())
	if err != nil {
		t.Fatalf("NewTemplate: %v", err)
	}
	out, err := tmpl.Render(context.Background(), map[string]any{"total": 42})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if out != "Price: ${total}" {
		t.Fatalf("got %q", out)
	}
}

func TestGuide_Templates_NilIsEmpty(t *testing.T) {
	tmpl, err := NewTemplate(`hello${suffix}!`, WithBuiltins())
	if err != nil {
		t.Fatalf("NewTemplate: %v", err)
	}
	out, err := tmpl.Render(context.Background(), map[string]any{"suffix": nil})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if out != "hello!" {
		t.Fatalf("got %q", out)
	}
}

// --- higher-order-patterns.md ----------------------------------------------

func TestGuide_HigherOrder_ValidationBag(t *testing.T) {
	src := `{
        "ok": user.age >= 18 && len(user.email) > 0 && contains(user.email, "@"),
        "errors": map(
            filter(
                [
                    {"ok": user.age >= 18,            "msg": "must be 18 or older"},
                    {"ok": len(user.email) > 0,       "msg": "email is required"},
                    {"ok": contains(user.email, "@"), "msg": "email must contain @"},
                ],
                !it.ok,
            ),
            it.msg,
        ),
    }`
	env := map[string]any{
		"user": map[string]any{"age": 15, "email": "ada"},
	}
	got := runGuide(t, src, env)
	want := map[string]any{
		"ok": false,
		"errors": []any{
			"must be 18 or older",
			"email must contain @",
		},
	}
	assertDeepEqual(t, got, want)
}

func TestGuide_HigherOrder_FilterMapProjection(t *testing.T) {
	env := map[string]any{
		"orders": []any{
			map[string]any{"id": "o-1", "status": "paid"},
			map[string]any{"id": "o-2", "status": "pending"},
			map[string]any{"id": "o-3", "status": "paid"},
		},
	}
	got := runGuide(t, `map(filter(orders, it.status == "paid"), it.id)`, env)
	assertDeepEqual(t, got, []any{"o-1", "o-3"})
}

func TestGuide_HigherOrder_NestedItRebinding(t *testing.T) {
	env := map[string]any{
		"orders": []any{
			map[string]any{
				"status": "paid",
				"items": []any{
					map[string]any{"price": 120},
					map[string]any{"price": 80},
					map[string]any{"price": 150},
				},
			},
			map[string]any{
				"status": "paid",
				"items": []any{
					map[string]any{"price": 10},
				},
			},
		},
	}
	src := `count(orders, it.status == "paid" && count(it.items, it.price >= 100) >= 2)`
	got := runGuide(t, src, env)
	assertDeepEqual(t, got, int64(1))
}

func TestGuide_HigherOrder_EmptyListSemantics(t *testing.T) {
	env := map[string]any{"xs": []any{}}
	cases := []struct {
		src  string
		want any
	}{
		{`any(xs, it > 0)`, false},
		{`all(xs, it > 0)`, true},
		{`count(xs, it > 0)`, int64(0)},
		{`find(xs, it > 0)`, nil},
	}
	for _, c := range cases {
		got := runGuide(t, c.src, env)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %#v want %#v", c.src, got, c.want)
		}
	}
}

func TestGuide_Sandboxing_EvalBudget(t *testing.T) {
	// The guide claims WithEvalBudget fails hostile nesting
	// deterministically and immediately, not at the deadline.
	p, err := Compile(`map(xs, map(xs, map(xs, it)))`, WithBuiltins(), WithEvalBudget(100_000))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	xs := make([]any, 10_000)
	for i := range xs {
		xs[i] = int64(i)
	}
	_, err = p.Run(context.Background(), map[string]any{"xs": xs})
	if !errors.Is(err, ErrEvaluate) {
		t.Fatalf("expected ErrEvaluate, got %v", err)
	}
	if !strings.Contains(err.Error(), "evaluation budget exceeded") {
		t.Fatalf("expected budget error, got %v", err)
	}
}

func TestGuide_Templates_LazyIfGuard(t *testing.T) {
	// templates.md claims `${if(n != 0, total/n, 0)}` is safe when n
	// is zero because only the selected branch evaluates.
	tpl, err := NewTemplate(`${if(n != 0, total/n, 0)}`, WithBuiltins())
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	out, err := tpl.Render(context.Background(), map[string]any{"n": int64(0), "total": int64(10)})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	assertDeepEqual(t, out, "0")
}

func TestGuide_RegisteringFunctions_CompileTimeValidation(t *testing.T) {
	// registering-functions.md claims invalid registrations fail
	// Compile with ErrCompile rather than erroring at call time.
	_, err := Compile(`bad()`, WithFunctions(map[string]any{
		"bad": func() (int, string) { return 1, "x" },
	}))
	if !errors.Is(err, ErrCompile) {
		t.Fatalf("expected ErrCompile, got %v", err)
	}
}

func TestGuide_RegisteringFunctions_HelperGroups(t *testing.T) {
	// The opt-in groups snippet from registering-functions.md.
	p, err := Compile(`join(map(split(trim("  a,b,c  "), ","), upper(it)), "-")`,
		WithBuiltins(),
		WithFunctions(MathFuncs()),
		WithFunctions(StringFuncs()),
		WithFunctions(CollectionFuncs()),
	)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	got, err := p.Run(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	assertDeepEqual(t, got, "A-B-C")
}

func TestGuide_HigherOrder_LazyIf(t *testing.T) {
	// higher-order-patterns.md claims only the selected branch of
	// if(cond, t, f) evaluates.
	env := map[string]any{"xs": []any{int64(1)}}
	got := runGuide(t, `if(len(xs) > 5, xs[5], "small")`, env)
	assertDeepEqual(t, got, "small")
}

// --- higher-order-patterns.md: named bindings ---------------------------------

func TestGuide_HigherOrder_NamedBinding_AllForms(t *testing.T) {
	// higher-order-patterns.md: every iterating form accepts the three-arg shape.
	orders := []any{
		map[string]any{"status": "paid", "n": int64(2)},
		map[string]any{"status": "open", "n": int64(1)},
		map[string]any{"status": "paid", "n": int64(3)},
	}
	env := map[string]any{"orders": orders}

	got := runGuide(t, `map(orders, o, o.n)`, env)
	assertDeepEqual(t, got, []any{int64(2), int64(1), int64(3)})

	got = runGuide(t, `filter(orders, o, o.status == "paid")`, env)
	if len(got.([]any)) != 2 {
		t.Fatalf("filter: expected 2, got %v", got)
	}

	got = runGuide(t, `any(orders, o, o.n > 2)`, env)
	assertDeepEqual(t, got, true)

	got = runGuide(t, `all(orders, o, o.n > 0)`, env)
	assertDeepEqual(t, got, true)

	got = runGuide(t, `count(orders, o, o.status == "paid")`, env)
	assertDeepEqual(t, got, int64(2))
}

func TestGuide_HigherOrder_NamedBinding_NestedScoping(t *testing.T) {
	// higher-order-patterns.md: outer named form + inner two-arg: `r` (review)
	// stays visible inside the inner body because the inner named form doesn't
	// bind `it`.
	reviews := []any{
		map[string]any{"author": "ann", "comments": []any{"a1", "a2"}},
		map[string]any{"author": "bob", "comments": []any{"b1"}},
	}
	env := map[string]any{"reviews": reviews}

	// Outer named (r), inner two-arg: `it` is the comment.
	got := runGuide(t, `map(reviews, r, map(r.comments, r.author + "/" + it))`, env)
	assertDeepEqual(t, got, []any{
		[]any{"ann/a1", "ann/a2"},
		[]any{"bob/b1"},
	})
}

// --- higher-order-patterns.md: flatMap ----------------------------------------

func TestGuide_HigherOrder_FlatMap(t *testing.T) {
	// higher-order-patterns.md: flatMap(users, u, u.orders) flattens orders.
	env := map[string]any{
		"users": []any{
			map[string]any{"orders": []any{int64(1), int64(2)}},
			map[string]any{"orders": []any{int64(3)}},
		},
	}

	got := runGuide(t, `flatMap(users, u, u.orders)`, env)
	assertDeepEqual(t, got, []any{int64(1), int64(2), int64(3)})

	// One-level-deep splice.
	got = runGuide(t, `flatMap([1, [2, 3], 4], it)`, nil, WithBuiltins())
	assertDeepEqual(t, got, []any{int64(1), int64(2), int64(3), int64(4)})

	// nil splices as nothing.
	got = runGuide(t, `flatMap([1, 2, 3], if(it > 1, [it, it], nil))`, nil, WithBuiltins())
	assertDeepEqual(t, got, []any{int64(2), int64(2), int64(3), int64(3)})

	// Strings are not split.
	got = runGuide(t, `flatMap(["ab", "c"], it)`, nil, WithBuiltins())
	assertDeepEqual(t, got, []any{"ab", "c"})
}

// --- higher-order-patterns.md: sortBy -----------------------------------------

func TestGuide_HigherOrder_SortBy(t *testing.T) {
	// higher-order-patterns.md: sortBy evaluates a key expr and returns a stable copy.
	env := map[string]any{
		"orders": []any{
			map[string]any{"status": "paid", "n": int64(2)},
			map[string]any{"status": "open", "n": int64(1)},
			map[string]any{"status": "paid", "n": int64(3)},
		},
	}

	got := runGuide(t, `map(sortBy(orders, o, o.n), o, o.n)`, env)
	assertDeepEqual(t, got, []any{int64(1), int64(2), int64(3)})

	// String keys sort lexicographically.
	got = runGuide(t, `map(sortBy(orders, o, o.status), o, o.status)`, env)
	assertDeepEqual(t, got, []any{"open", "paid", "paid"})

	// Input list is not mutated.
	first := env["orders"].([]any)[0].(map[string]any)
	if first["n"] != int64(2) {
		t.Fatalf("sortBy mutated input: first element changed to %v", first)
	}
}

// --- higher-order-patterns.md: entries ----------------------------------------

func TestGuide_HigherOrder_Entries(t *testing.T) {
	// higher-order-patterns.md: entries makes maps iterable through the forms.
	env := map[string]any{
		"headers": map[string]any{
			"content-type": "application/json",
			"x-request-id": "abc123",
		},
	}

	got := runGuide(t, `map(entries(headers), e, e.key + ": " + e.value)`, env)
	assertDeepEqual(t, got, []any{"content-type: application/json", "x-request-id: abc123"})

	// filter through entries.
	scores := map[string]any{"alice": int64(90), "bob": int64(70), "carol": int64(85)}
	got = runGuide(t, `map(filter(entries(scores), e, e.value > 80), e, e.key)`,
		map[string]any{"scores": scores})
	// keys are sorted by entries, so alice and carol qualify in sorted order.
	assertDeepEqual(t, got, []any{"alice", "carol"})
}

// --- templates.md: JSON composite rendering -----------------------------------

func TestGuide_Templates_JSONCompositeRendering(t *testing.T) {
	// templates.md: maps, slices, arrays, and structs render as compact JSON.
	env := map[string]any{
		"config": map[string]any{"retries": int64(3)},
		"files":  []any{"a.go", "b.go"},
	}

	tmpl, err := NewTemplate("${config} | ${files}", WithBuiltins())
	if err != nil {
		t.Fatalf("NewTemplate: %v", err)
	}
	out, err := tmpl.Render(t.Context(), env)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if out != `{"retries":3} | ["a.go","b.go"]` {
		t.Fatalf("got %q", out)
	}
}

func TestGuide_Templates_CustomDelimiters(t *testing.T) {
	// templates.md: WithTemplateDelimiters swaps the opener/closer.
	env := map[string]any{"service": map[string]any{"name": "api"}}

	tmpl, err := NewTemplate(
		"Deploy ${{ service.name }} via: echo ${HOME}",
		WithTemplateDelimiters("${{", "}}"),
	)
	if err != nil {
		t.Fatalf("NewTemplate: %v", err)
	}
	out, err := tmpl.Render(t.Context(), env)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if out != "Deploy api via: echo ${HOME}" {
		t.Fatalf("got %q", out)
	}
}

func TestGuide_Templates_CustomFormatter(t *testing.T) {
	// templates.md: WithTemplateFormatter runs first for every interpolated value.
	env := map[string]any{"price": 12.5, "label": "total", "empty": nil}

	tmpl, err := NewTemplate("${label}: ${price} (${empty})",
		WithTemplateFormatter(func(v any) (string, bool) {
			switch x := v.(type) {
			case float64:
				return fmt.Sprintf("%.2f", x), true
			case nil:
				return "N/A", true
			}
			return "", false
		}),
	)
	if err != nil {
		t.Fatalf("NewTemplate: %v", err)
	}
	out, err := tmpl.Render(t.Context(), env)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if out != "total: 12.50 (N/A)" {
		t.Fatalf("got %q", out)
	}
}

func TestGuide_Templates_CompileRejectsTemplateOnlyOptions(t *testing.T) {
	// templates.md: template-only options passed to Compile fail at load time.
	_, err := Compile("1+1", WithTemplateDelimiters("${{", "}}"))
	if !errors.Is(err, ErrCompile) {
		t.Fatalf("expected ErrCompile, got %v", err)
	}
	_, err = Compile("1+1", WithTemplateFormatter(func(any) (string, bool) { return "", false }))
	if !errors.Is(err, ErrCompile) {
		t.Fatalf("expected ErrCompile, got %v", err)
	}
}

func TestGuide_Templates_ErrorsReportLineColumn(t *testing.T) {
	// templates.md: runtime errors include line:column in the message.
	src := "line one\nline two\n  ${boom} end"
	tmpl, err := NewTemplate(src)
	if err != nil {
		t.Fatalf("NewTemplate: %v", err)
	}
	_, rerr := tmpl.Render(t.Context(), map[string]any{})
	if rerr == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(rerr.Error(), "at 3:3") {
		t.Fatalf("expected line:col in error, got %v", rerr)
	}
}
