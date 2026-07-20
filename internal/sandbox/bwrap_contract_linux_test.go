//go:build linux

package sandbox

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestLinuxRealBubblewrapContract is the designated non-skippable real-bwrap
// execution contract. It provisions a minimal sandbox config and runs a trivial
// command through a real bubblewrap binary end to end.
//
// On developer hosts without a working bubblewrap it SKIPs with an explicit
// reason. The provisioned Linux CI job installs bubblewrap and then asserts this
// test actually PASSed (a SKIP or FAIL is treated as a job failure), so the
// contract cannot silently go green across platforms.
func TestLinuxRealBubblewrapContract(t *testing.T) {
	bwrapPath, err := exec.LookPath("bwrap")
	if err != nil {
		t.Skip("real-bubblewrap contract: bwrap is not installed; the provisioned Linux CI job must install it")
	}

	root := t.TempDir()
	dirs := map[string]string{}
	for _, name := range []string{"workspace", "home", "gh", "xdg", "codex"} {
		dirs[name] = filepath.Join(root, name)
		if mkErr := os.Mkdir(dirs[name], 0o700); mkErr != nil {
			t.Fatal(mkErr)
		}
	}

	systemBinds := []string{}
	for _, candidate := range []string{"/usr", "/bin", "/lib", "/lib64", "/etc"} {
		if _, statErr := os.Stat(candidate); statErr == nil {
			systemBinds = append(systemBinds, candidate)
		}
	}

	cfg := Config{
		BwrapPath:           bwrapPath,
		WorkspacePath:       dirs["workspace"],
		TempHome:            dirs["home"],
		TempGHConfigDir:     dirs["gh"],
		TempXDGConfigHome:   dirs["xdg"],
		TempCodexHome:       dirs["codex"],
		HostEnv:             []string{"PATH=/usr/bin:/bin"},
		SystemReadOnlyBinds: systemBinds,
	}

	prepared, err := Prepare(context.Background(), cfg,
		Command{Binary: "/usr/bin/env", Args: []string{"true"}, Dir: dirs["workspace"]},
		Dependencies{})
	if err != nil {
		// bwrap is installed but the environment cannot run it (e.g. restricted
		// unprivileged user namespaces). Skip on dev hosts; CI must have fixed
		// this and will fail because a SKIP is not a PASS.
		if errors.Is(err, ErrSandboxPreflightFailed) ||
			errors.Is(err, ErrBubblewrapUnavailable) ||
			errors.Is(err, ErrBubblewrapUnsupported) {
			t.Skipf("real-bubblewrap contract: bwrap present but unusable in this environment: %v", err)
		}
		t.Fatalf("real-bubblewrap contract preflight/prepare failed: %v", err)
	}

	if prepared.Command.Binary != bwrapPath {
		t.Fatalf("prepared command did not use bwrap: %q", prepared.Command.Binary)
	}
	if !prepared.Metadata.PlatformSupported || !prepared.Metadata.BwrapSmokeTest {
		t.Fatalf("real-bubblewrap contract metadata incomplete: %+v", prepared.Metadata)
	}

	result, err := (ExecRunner{}).Run(context.Background(), prepared.Command)
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("real bubblewrap execution failed: %s", commandOutputSummary(result, err))
	}
	t.Logf("REAL-BWRAP-CONTRACT ok bwrap=%s", bwrapPath)
}
