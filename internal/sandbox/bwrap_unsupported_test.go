//go:build !linux

package sandbox

import (
	"context"
	"errors"
	"testing"
)

func TestUnsupportedPlatformDefaultSandboxFails(t *testing.T) {
	meta, err := Preflight(context.Background(), Config{}, Dependencies{})
	if !errors.Is(err, ErrSandboxUnsupported) {
		t.Fatalf("Preflight error = %v, want unsupported", err)
	}
	if meta.PlatformSupported {
		t.Fatalf("PlatformSupported = true, want false: %+v", meta)
	}
	if meta.SandboxProvider != ProviderBubblewrap || meta.FSBoundary != FSBoundaryWorkspace {
		t.Fatalf("unexpected metadata: %+v", meta)
	}
}

func TestUnsupportedPlatformUnsafeModeSucceeds(t *testing.T) {
	meta, err := Preflight(context.Background(), Config{UnsafeNoSandbox: true}, Dependencies{})
	if err != nil {
		t.Fatalf("Preflight unsafe returned error: %v", err)
	}
	if meta.SandboxProvider != ProviderNone || meta.FSBoundary != FSBoundaryDisabled || !meta.UnsafeNoSandbox {
		t.Fatalf("unexpected unsafe metadata: %+v", meta)
	}
}
