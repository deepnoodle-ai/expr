package expr

// Tests that every example in docs/guides/examples.md actually compiles and
// evaluates to the result the documentation claims. If this file drifts
// from the doc, fix the doc — these are the canonical expected outputs.

import (
	"reflect"
	"testing"
)

func runDocExample(t *testing.T, src string, env any, opts ...Option) any {
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

func assertDeepEqual(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("result mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

// Example 1: Shaping a response object.
func TestDocsExample1_ResponseShaping(t *testing.T) {
	src := `{
        "id":         user.id,
        "name":       upper(user.name),
        "is_adult":   user.age >= 18,
        "roles":      filter(user.roles, it != "internal"),
        "role_count": len(filter(user.roles, it != "internal")),
        "primary":    find(user.roles, it != "internal"),
    }`
	env := map[string]any{
		"user": map[string]any{
			"id":    42,
			"name":  "ada",
			"age":   36,
			"roles": []any{"internal", "admin", "editor"},
		},
	}
	got := runDocExample(t, src, env)
	want := map[string]any{
		"id":         42,
		"name":       "ADA",
		"is_adult":   true,
		"roles":      []any{"admin", "editor"},
		"role_count": 2,
		"primary":    "admin",
	}
	assertDeepEqual(t, got, want)
}

// Example 2: Nested higher-order forms.
func TestDocsExample2_NestedHigherOrder(t *testing.T) {
	src := `{
        "has_whale_order": any(
            orders,
            it.status == "paid" &&
                count(it.items, it.price >= 100) >= 2,
        ),
        "paid_ids":     map(filter(orders, it.status == "paid"), it.id),
        "total_orders": len(orders),
    }`
	env := map[string]any{
		"orders": []any{
			map[string]any{
				"id": "o-1", "status": "paid",
				"items": []any{
					map[string]any{"sku": "A", "price": 120},
					map[string]any{"sku": "B", "price": 80},
					map[string]any{"sku": "C", "price": 150},
				},
			},
			map[string]any{
				"id": "o-2", "status": "pending",
				"items": []any{
					map[string]any{"sku": "D", "price": 999},
				},
			},
		},
	}
	got := runDocExample(t, src, env)
	want := map[string]any{
		"has_whale_order": true,
		"paid_ids":        []any{"o-1"},
		"total_orders":    2,
	}
	assertDeepEqual(t, got, want)
}

// Example 3: Validation error bag.
func TestDocsExample3_ValidationErrorBag(t *testing.T) {
	src := `{
        "ok": user.age >= 18 &&
            len(user.email) > 0 &&
            contains(user.email, "@"),
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
		"user": map[string]any{
			"age":   15,
			"email": "ada",
		},
	}
	got := runDocExample(t, src, env)
	want := map[string]any{
		"ok": false,
		"errors": []any{
			"must be 18 or older",
			"email must contain @",
		},
	}
	assertDeepEqual(t, got, want)
}

// Example 4: Summarizing a stream of events.
func TestDocsExample4_EventSummary(t *testing.T) {
	src := `{
        "total":     len(events),
        "clicks":    count(events, it.kind == "click"),
        "views":     count(events, it.kind == "view"),
        "purchases": count(events, it.kind == "purchase"),
        "has_sale":  any(events, it.kind == "purchase"),
        "top_user":  find(events, it.kind == "purchase")?.user,
    }`
	env := map[string]any{
		"events": []any{
			map[string]any{"kind": "view", "user": "ada"},
			map[string]any{"kind": "click", "user": "ada"},
			map[string]any{"kind": "view", "user": "grace"},
			map[string]any{"kind": "purchase", "user": "grace"},
		},
	}
	got := runDocExample(t, src, env)
	want := map[string]any{
		"total":     4,
		"clicks":    int64(1),
		"views":     int64(2),
		"purchases": int64(1),
		"has_sale":  true,
		"top_user":  "grace",
	}
	assertDeepEqual(t, got, want)
}

// Example 5: Template-driven messages.
func TestDocsExample5_Template(t *testing.T) {
	src := "Hi ${user.name}! You have ${len(filter(user.tasks, !it.done))} task(s) left."
	env := map[string]any{
		"user": map[string]any{
			"name": "Grace",
			"tasks": []any{
				map[string]any{"title": "write spec", "due": "Mon", "done": true},
				map[string]any{"title": "review PR", "due": "Tue", "done": false},
				map[string]any{"title": "ship v1", "due": "Fri", "done": false},
			},
		},
	}
	tmpl, err := NewTemplate(src, WithBuiltins())
	if err != nil {
		t.Fatalf("NewTemplate: %v", err)
	}
	out, err := tmpl.Render(t.Context(), env)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	want := "Hi Grace! You have 2 task(s) left."
	if out != want {
		t.Fatalf("template mismatch\n got: %q\nwant: %q", out, want)
	}
}

// Example 6: Mixed struct + map env.
type docOrderItem struct {
	SKU   string
	Price float64
}

type docOrder struct {
	Items []docOrderItem
	Meta  map[string]any
}

func (o *docOrder) Subtotal() float64 {
	var s float64
	for _, it := range o.Items {
		s += it.Price
	}
	return s
}

func TestDocsExample6_StructEnv(t *testing.T) {
	src := `Order.Subtotal() > 100 &&
        len(Order.Items) >= 2 &&
        any(Order.Items, it.Price >= 50) &&
        !contains(keys(Order.Meta), "refunded")`

	env := map[string]any{
		"Order": &docOrder{
			Items: []docOrderItem{
				{SKU: "A", Price: 60},
				{SKU: "B", Price: 80},
			},
			Meta: map[string]any{"source": "web"},
		},
	}
	got := runDocExample(t, src, env)
	assertDeepEqual(t, got, true)
}

type docEnvironmentOutput struct {
	Environment docEnvironment `json:"environment"`
}

type docEnvironment struct {
	Status string `json:"status"`
}

func TestDocsExample6_StructTags(t *testing.T) {
	src := `result.environment.status == "ready"`
	env := map[string]any{
		"result": docEnvironmentOutput{
			Environment: docEnvironment{Status: "ready"},
		},
	}
	got := runDocExample(t, src, env, WithStructTags("json"))
	assertDeepEqual(t, got, true)
}

// Example 7: Role-based access control.
func TestDocsExample7_RBAC(t *testing.T) {
	src := `{
        "allow": user.role == "admin" ||
            user.role == "editor" &&
                contains(["draft", "review", "published"], resource.state) ||
            user.role == "viewer" &&
                resource.state == "published" &&
                !resource.private ||
            resource.owner == user.id &&
                resource.state != "archived",
        "role":  user.role,
        "state": resource.state,
    }`
	env := map[string]any{
		"user": map[string]any{"id": "u-7", "role": "viewer"},
		"resource": map[string]any{
			"state":   "published",
			"private": false,
			"owner":   "u-1",
		},
	}
	got := runDocExample(t, src, env)
	want := map[string]any{
		"allow": true,
		"role":  "viewer",
		"state": "published",
	}
	assertDeepEqual(t, got, want)
}

// Example 8: Extracting + sorting via a registered function.
func TestDocsExample8_RegisteredSort(t *testing.T) {
	src := `take(
        sortBy(
            filter(users, it.active),
            "age",
        ),
        3,
    )`
	sortBy := func(xs []any, key string) []any {
		out := append([]any{}, xs...)
		// stable insertion sort by numeric key
		for i := 1; i < len(out); i++ {
			for j := i; j > 0; j-- {
				a := out[j-1].(map[string]any)[key]
				b := out[j].(map[string]any)[key]
				if toFloat(a) > toFloat(b) {
					out[j-1], out[j] = out[j], out[j-1]
				} else {
					break
				}
			}
		}
		return out
	}
	take := func(xs []any, n int) []any {
		if n > len(xs) {
			n = len(xs)
		}
		return xs[:n]
	}
	env := map[string]any{
		"users": []any{
			map[string]any{"name": "Ada", "age": 36, "active": true},
			map[string]any{"name": "Alan", "age": 41, "active": false},
			map[string]any{"name": "Grace", "age": 29, "active": true},
			map[string]any{"name": "Linus", "age": 54, "active": true},
			map[string]any{"name": "Mira", "age": 22, "active": true},
		},
	}
	got := runDocExample(t, src, env,
		WithBuiltins(),
		WithFunctions(map[string]any{"sortBy": sortBy, "take": take}),
	)
	want := []any{
		map[string]any{"name": "Mira", "age": 22, "active": true},
		map[string]any{"name": "Grace", "age": 29, "active": true},
		map[string]any{"name": "Ada", "age": 36, "active": true},
	}
	assertDeepEqual(t, got, want)
}

func toFloat(v any) float64 {
	switch x := v.(type) {
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case float64:
		return x
	}
	return 0
}

// Example 9: Chunky webhook predicate.
func TestDocsExample9_WebhookPredicate(t *testing.T) {
	src := `{
        "accept": request.method == "POST" &&
            contains(["application/json", "application/ld+json"], request.headers["content-type"]) &&
            len(request.body.items) > 0 &&
            all(request.body.items, it.qty > 0 && it.price >= 0) &&
            (has(request.body, "coupon") && upper(request.body.coupon) == "LAUNCH25" || request.body.total >= 100),
        "method":       request.method,
        "item_count":   len(request.body.items),
        "all_positive": all(request.body.items, it.qty > 0 && it.price >= 0),
    }`
	env := map[string]any{
		"request": map[string]any{
			"method":  "POST",
			"headers": map[string]any{"content-type": "application/json"},
			"body": map[string]any{
				"items": []any{
					map[string]any{"sku": "A", "qty": 2, "price": 40},
					map[string]any{"sku": "B", "qty": 1, "price": 25},
				},
				"coupon": "launch25",
				"total":  105,
			},
		},
	}
	got := runDocExample(t, src, env)
	want := map[string]any{
		"accept":       true,
		"method":       "POST",
		"item_count":   2,
		"all_positive": true,
	}
	assertDeepEqual(t, got, want)
}
