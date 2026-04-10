package expr

import (
	"errors"
)

// ErrNoCompiler is returned by NoopCompiler.Compile when a host program
// asks a placeholder compiler to evaluate code without a real engine
// wired in.
var ErrNoCompiler = errors.New("expr: no compiler configured")

// NoopCompiler is a Compiler that always returns ErrNoCompiler. It is
// useful as a sentinel default for hosts that accept a Compiler field
// and want a non-nil zero value.
type NoopCompiler struct{}

func (NoopCompiler) Compile(code string) (Script, error) {
	return nil, ErrNoCompiler
}
