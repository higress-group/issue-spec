package sandbox

import (
	"context"
	"errors"
	"runtime"
	"testing"
)

// TestSandboxCapabilityMatrix exercises bubblewrap capability discovery through
// the injected Dependencies.LookPath and Runner seams without ever executing a
// real bwrap binary. It runs on every platform: on Linux it asserts the
// supported / unavailable / fallback+error branches, and on non-Linux it asserts
// the unsupported-platform contract (and that discovery is never even attempted).
func TestSandboxCapabilityMatrix(t *testing.T) {
	capableRunner := func() Runner {
		return runnerFunc(func(_ context.Context, command Command) (Result, error) {
			switch {
			case len(command.Args) == 1 && command.Args[0] == "--version":
				return Result{Stdout: []byte("bubblewrap 0.8.0\n")}, nil
			case len(command.Args) == 1 && command.Args[0] == "--help":
				return Result{Stdout: []byte("usage: bwrap --perms OCTAL ...\n")}, nil
			default: // smoke test
				return Result{}, nil
			}
		})
	}
	oldVersionRunner := func() Runner {
		return runnerFunc(func(_ context.Context, command Command) (Result, error) {
			switch {
			case len(command.Args) == 1 && command.Args[0] == "--version":
				return Result{Stdout: []byte("bubblewrap 0.4.0\n")}, nil
			case len(command.Args) == 1 && command.Args[0] == "--help":
				return Result{Stdout: []byte("usage: --perms\n")}, nil
			default:
				return Result{}, nil
			}
		})
	}
	noPermsRunner := func() Runner {
		return runnerFunc(func(_ context.Context, command Command) (Result, error) {
			switch {
			case len(command.Args) == 1 && command.Args[0] == "--version":
				return Result{Stdout: []byte("bubblewrap 0.8.0\n")}, nil
			case len(command.Args) == 1 && command.Args[0] == "--help":
				return Result{Stdout: []byte("usage: bwrap ...\n")}, nil
			default:
				return Result{}, nil
			}
		})
	}

	tests := []struct {
		name string
		// lookPath simulates PATH discovery; nil means "not found".
		found  bool
		runner func() Runner
		// linuxWant is the error the Linux contract must return (nil == success).
		linuxWantSentinel error
		linuxWantSuccess  bool
	}{
		{
			name:             "supported: bwrap found and capable",
			found:            true,
			runner:           capableRunner,
			linuxWantSuccess: true,
		},
		{
			name:              "unavailable: bwrap not on PATH",
			found:             false,
			runner:            capableRunner,
			linuxWantSentinel: ErrBubblewrapUnavailable,
		},
		{
			name:              "fallback: bwrap too old",
			found:             true,
			runner:            oldVersionRunner,
			linuxWantSentinel: ErrBubblewrapUnsupported,
		},
		{
			name:              "error: bwrap lacks --perms support",
			found:             true,
			runner:            noPermsRunner,
			linuxWantSentinel: ErrBubblewrapUnsupported,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lookPathCalled := false
			lookPath := func(string) (string, error) {
				lookPathCalled = true
				if tt.found {
					return "/usr/bin/bwrap", nil
				}
				return "", errors.New("executable file not found in $PATH")
			}
			deps := Dependencies{LookPath: lookPath, Runner: tt.runner()}
			meta, err := Preflight(context.Background(), Config{HostEnv: []string{"PATH=/usr/bin"}}, deps)

			if runtime.GOOS != "linux" {
				// Unsupported platform: bubblewrap execution is Linux-only, so
				// discovery must not even be attempted and the platform is
				// reported unsupported with an explicit reason.
				if !errors.Is(err, ErrSandboxUnsupported) {
					t.Fatalf("non-Linux Preflight error = %v, want ErrSandboxUnsupported", err)
				}
				if meta.PlatformSupported {
					t.Fatalf("non-Linux PlatformSupported = true, want false: %+v", meta)
				}
				if lookPathCalled {
					t.Fatalf("non-Linux platform must not probe bubblewrap capability")
				}
				return
			}

			if !meta.PlatformSupported {
				t.Fatalf("Linux PlatformSupported = false, want true: %+v", meta)
			}
			if tt.linuxWantSuccess {
				if err != nil {
					t.Fatalf("supported case error = %v, want nil", err)
				}
				if !meta.BwrapPermsSupported || !meta.BwrapSmokeTest || meta.BwrapVersion == "" {
					t.Fatalf("supported metadata incomplete: %+v", meta)
				}
				if meta.SandboxProvider != ProviderBubblewrap || meta.FSBoundary != FSBoundaryWorkspace {
					t.Fatalf("supported metadata mismatch: %+v", meta)
				}
				return
			}
			if !errors.Is(err, ErrSandboxPreflightFailed) {
				t.Fatalf("failure case error = %v, want ErrSandboxPreflightFailed", err)
			}
			if !errors.Is(err, tt.linuxWantSentinel) {
				t.Fatalf("failure case error = %v, want %v", err, tt.linuxWantSentinel)
			}
		})
	}
}
