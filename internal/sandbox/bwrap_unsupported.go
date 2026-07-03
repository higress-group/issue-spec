//go:build !linux

package sandbox

import "context"

type Bubblewrap struct {
	Path   string
	Runner Runner
}

func (b Bubblewrap) Preflight(context.Context, PreflightOptions) error { return ErrUnsupported }
func (b Bubblewrap) Run(context.Context, CommandSpec) (Result, error)  { return Result{}, ErrUnsupported }

