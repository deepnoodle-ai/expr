package expr

import (
	"context"
)

// Script is a compiled program that can be run against an environment.
// *Program implements Script directly, so any host that accepts a Script
// can also accept a *Program returned from Engine.Compile.
type Script interface {
	Run(ctx context.Context, env any) (any, error)
}

// Compiler turns source code into a Script. Engine satisfies this surface
// via Engine.Compiler. It exists so generic helpers like Template can
// accept any backend that compiles strings into runnable scripts.
type Compiler interface {
	Compile(code string) (Script, error)
}
