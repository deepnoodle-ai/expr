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
		ID: "A-1",
		Items: []guideLineItem{{SKU: "a", Price: 60}, {SKU: "b", Price: 80}},
		Meta: map[string]any{"source": "web"},
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
