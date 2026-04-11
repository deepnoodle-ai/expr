package expr

import "context"

// evalExpr compiles and runs code in one step. Exists only so the test
// suite can stay terse; the public API exposes only Compile+Run.
func evalExpr(ctx context.Context, code string, env any, opts ...Option) (any, error) {
	p, err := Compile(code, opts...)
	if err != nil {
		return nil, err
	}
	return p.Run(ctx, env)
}
