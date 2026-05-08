package expr

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// identHint returns a short, user-friendly hint to append to an
// "undefined identifier" error. When there is a single close match
// among the in-scope names the hint reads `(did you mean "foo"?)`.
// When there is no close match but the candidate set is small enough
// to list, the hint reads `(available: a, b, c)`. When neither
// condition holds the hint is empty, so the original error message is
// unchanged.
//
// Higher-order forms get a tailored hint instead. If the unresolved
// name is itself a form (`count`, `filter`, `try`, ...), the hint
// reports it as a special form and shows the call signature so the
// user knows it must be invoked as `count(xs, predicate)` rather
// than referenced as a value.
func identHint(env any, funcs map[string]any, name string, fieldTags *structTagConfig) string {
	if hint, ok := specialFormHint(name); ok {
		return hint
	}
	return formatHint(name, availableIdents(env, funcs, fieldTags))
}

// specialFormHint returns the "is a special form" message when name
// matches one of the higher-order forms exactly. The signature shown
// matches the form's actual arity so users see the correct shape
// (`try(value, default)` vs `count(xs, predicate)`).
func specialFormHint(name string) (string, bool) {
	for _, f := range userForms {
		if f.name == name {
			return fmt.Sprintf(" (%q is a special form, did you mean to call %s?)", name, f.callHint), true
		}
	}
	return "", false
}

// fieldHint is identHint's counterpart for selector and index
// lookups: it reports the names reachable on a specific receiver
// value (struct fields/methods or the keys of a string-keyed map).
func fieldHint(recv any, name string, fieldTags *structTagConfig) string {
	return formatHint(name, availableFields(recv, fieldTags))
}

func formatHint(name string, candidates []string) string {
	if len(candidates) == 0 {
		return ""
	}
	candidates = dedup(candidates)
	if closest, ok := closestName(name, candidates); ok {
		return fmt.Sprintf(" (did you mean %q?)", closest)
	}
	const maxList = 8
	if len(candidates) > maxList {
		return ""
	}
	sort.Strings(candidates)
	return fmt.Sprintf(" (available: %s)", strings.Join(candidates, ", "))
}

// closestName picks the nearest candidate within an edit-distance
// threshold proportional to the input length. A case-insensitive
// match beats any edit-distance winner, so `Name` vs `name` always
// suggests the correct casing.
func closestName(name string, candidates []string) (string, bool) {
	lname := strings.ToLower(name)
	for _, c := range candidates {
		if strings.ToLower(c) == lname && c != name {
			return c, true
		}
	}
	// Threshold of 2 catches the usual single-character typos AND
	// transpositions like "Nmae" → "Name" (which cost 2 edits under
	// plain Levenshtein). It grows with longer names so suggestions
	// still fire on things like "usernmae" → "username".
	threshold := len(name) / 3
	if threshold < 2 {
		threshold = 2
	}
	if threshold > 3 {
		threshold = 3
	}
	best := -1
	var bestName string
	for _, c := range candidates {
		// Skip exact matches: a hint of `did you mean "count"?` is
		// noise when the user already typed that name. Exact-match
		// candidates only reach this loop for higher-order forms
		// (whose names appear in the candidate set even though the
		// evaluator could not resolve them as identifiers).
		// specialFormHint handles those cases separately.
		if c == name {
			continue
		}
		d := levenshtein(name, c)
		if d <= threshold && (best == -1 || d < best) {
			best = d
			bestName = c
		}
	}
	return bestName, best != -1
}

// levenshtein computes edit distance between two strings using the
// standard two-row dynamic-programming table. Identifiers in expr
// are ASCII in practice, so byte-level comparison is sufficient and
// faster than decoding runes.
func levenshtein(a, b string) int {
	if a == b {
		return 0
	}
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := 0; j <= len(b); j++ {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			ins := cur[j-1] + 1
			del := prev[j] + 1
			sub := prev[j-1] + cost
			cur[j] = min3(ins, del, sub)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

func min3(a, b, c int) int {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}

// availableIdents gathers every name that a top-level identifier
// could legitimately resolve to: env keys/fields/methods plus every
// registered function. Used only to build error hints, so allocation
// cost only matters on the error path.
func availableIdents(env any, funcs map[string]any, fieldTags *structTagConfig) []string {
	names := availableFields(env, fieldTags)
	for k := range funcs {
		names = append(names, k)
	}
	// Include the higher-order special forms under their user-visible
	// names so a typo of `map` or `filter` suggests the right thing.
	for _, f := range userForms {
		names = append(names, f.name)
	}
	return names
}

// availableFields gathers every selector-reachable name on recv.
// Mirrors the lookup rules in selectField / resolveMethod so the hint
// never points at something the evaluator cannot actually resolve.
// For nested itEnv scopes, the caller's parent env is walked too, so
// a typo inside a `map(...)` predicate can still suggest an outer
// binding.
func availableFields(recv any, fieldTags *structTagConfig) []string {
	if recv == nil {
		return nil
	}
	if m, ok := recv.(map[string]any); ok {
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		return out
	}
	if it, ok := recv.(*itEnv); ok {
		out := []string{"it", "index"}
		return append(out, availableFields(it.parent, fieldTags)...)
	}
	rv := reflect.ValueOf(recv)
	orig := rv
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil
		}
		rv = rv.Elem()
	}
	var names []string
	switch rv.Kind() {
	case reflect.Struct:
		if fieldTags != nil {
			names = append(names, availableTaggedFields(recv, fieldTags)...)
			break
		}
		t := rv.Type()
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if f.IsExported() {
				names = append(names, f.Name)
			}
		}
		ot := orig.Type()
		for i := 0; i < ot.NumMethod(); i++ {
			names = append(names, ot.Method(i).Name)
		}
	case reflect.Map:
		if rv.Type().Key().Kind() == reflect.String {
			for _, k := range rv.MapKeys() {
				names = append(names, k.String())
			}
		}
	}
	return names
}

func dedup(names []string) []string {
	if len(names) < 2 {
		return names
	}
	seen := make(map[string]struct{}, len(names))
	out := make([]string, 0, len(names))
	for _, n := range names {
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	return out
}
