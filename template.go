package expr

import "github.com/deepnoodle-ai/expr/template"

// Template is a pre-compiled `${...}` string interpolator. It is an
// alias for the implementation in the expr/template subpackage so
// callers can keep using expr.Template without an extra import.
type Template = template.Template

// NewTemplate parses raw and pre-compiles every `${...}` expression
// using the given Compiler. See the expr/template package for the full
// documentation, including brace handling and the `$$` escape.
func NewTemplate(c Compiler, raw string) (*Template, error) {
	return template.New(templateCompiler{c: c}, raw)
}

// templateCompiler adapts the package-level Compiler (which returns
// expr.Script) to template.Compiler (which returns template.Script).
// Both Script interfaces have identical method sets, so the concrete
// script value satisfies both; the adapter only exists to bridge the
// named return type.
type templateCompiler struct{ c Compiler }

func (a templateCompiler) Compile(code string) (template.Script, error) {
	return a.c.Compile(code)
}
