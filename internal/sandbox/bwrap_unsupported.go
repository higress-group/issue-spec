//go:build !linux

package sandbox

import (
	"context"
	"fmt"
	"runtime"
)

func preflightBwrap(ctx context.Context, cfg Config, envMeta EnvMetadata, deps Dependencies) (Metadata, error) {
	_ = ctx
	_ = cfg
	_ = deps
	meta := Metadata{
		SandboxEnabled:    true,
		SandboxProvider:   ProviderBubblewrap,
		FSBoundary:        FSBoundaryWorkspace,
		Platform:          runtime.GOOS,
		PlatformSupported: false,
		Env:               envMeta,
		Diagnostics:       []string{"default bubblewrap sandbox execution is supported on Linux only", installOrUnsafeHint()},
	}
	return meta, fmt.Errorf("%w: %s", ErrSandboxUnsupported, installOrUnsafeHint())
}

func buildBwrapCommand(cfg Config, target Command, env []string, bwrapPath string) (Command, []Mount, error) {
	_ = cfg
	_ = target
	_ = env
	_ = bwrapPath
	return Command{}, nil, fmt.Errorf("%w: default bubblewrap sandbox execution is supported on Linux only", ErrSandboxUnsupported)
}
