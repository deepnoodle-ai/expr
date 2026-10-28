// Type checking: describe the env's types once, then check each
// expression when it is saved instead of finding its mistakes when it
// runs.
package main

import (
	"errors"
	"fmt"
	"log"

	"github.com/deepnoodle-ai/expr"
)

// env describes what Run will be given: a classified issue, an optional
// review, and a webhook payload whose fields are not all known.
var env = map[string]*expr.Type{
	"issue": expr.ObjectOf(map[string]*expr.Type{
		"kind":   expr.EnumOf("bug", "feature", "question"),
		"title":  expr.String(),
		"labels": expr.ListOf(expr.String()),
		"score":  expr.Number(),
	}),
	"review": expr.ObjectOf(map[string]*expr.Type{
		"verdict": expr.EnumOf("approve", "reject"),
		"body":    expr.String(),
	}).OrNil(),
	"event": expr.OpenObject(map[string]*expr.Type{
		"type":   expr.String(),
		"sender": expr.OpenObject(map[string]*expr.Type{"login": expr.String()}),
	}),
}

func check(src string) {
	p, err := expr.Compile(src, expr.WithBuiltins(), expr.WithFunctions(expr.StringFuncs()))
	if err != nil {
		log.Fatal(err)
	}
	t, err := p.Check(env)
	var te *expr.TypeError
	if errors.As(err, &te) {
		fmt.Printf("%s\n", src)
		for _, problem := range te.Problems {
			fmt.Printf("    %s\n", problem)
		}
		return
	}
	fmt.Printf("%s\n    ok: %s\n", src, t)
}

func main() {
	fmt.Println("== Accepted, with the result type")
	for _, src := range []string{
		`issue.kind == "bug" && issue.score > 0.5`,
		`map(filter(issue.labels, startsWith(it, "area/")), upper(it))`,
		`review?.verdict`,
		`review != nil && review.verdict == "approve"`,
		`try(review.body, "no review")`,
		`event.type == "issues" && event.sender.login != ""`,
		`event.action`, // an open object's unlisted fields are Any
		`{"title": issue.title, "kind": issue.kind}`,
	} {
		check(src)
	}

	fmt.Println("\n== Rejected")
	for _, src := range []string{
		`issue.titel`,
		`issue.kind == "bugfix"`,
		`review.verdict`,
		`issue.score + issue.title`,
		`split(issue.title, 1)`,
		`issue.labels[issue.title]`,
		`len(issue.title || 0)`,
	} {
		check(src)
	}

	fmt.Println("\n== A template checks every ${...}")
	tmpl, err := expr.NewTemplate("${issue.title} (${issue.knd})", expr.WithBuiltins())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(tmpl.Check(env))
}
