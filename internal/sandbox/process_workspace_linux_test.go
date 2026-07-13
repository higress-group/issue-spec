//go:build linux

package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProcessWorkspaceSnapshotUsesReadOnlyBind(t *testing.T) {
	cfg := Config{WorkspacePath: "/tmp/process-review", WorkspaceReadOnly: true, TempHome: "/tmp/home", TempGHConfigDir: "/tmp/gh", TempXDGConfigHome: "/tmp/xdg", TempCodexHome: "/tmp/codex", SystemReadOnlyBinds: []string{"/usr"}}
	command, _, err := buildBwrapCommand(cfg, Command{Binary: "/usr/bin/true", Dir: cfg.WorkspacePath}, nil, "/usr/bin/bwrap")
	if err != nil {
		t.Fatal(err)
	}
	assertArgSequence(t, command.Args, "--ro-bind", cfg.WorkspacePath, "/workspace")
	assertArgSequence(t, command.Args, "--ro-bind", cfg.WorkspacePath, cfg.WorkspacePath)
	assertArgSequenceMissing(t, command.Args, "--bind", cfg.WorkspacePath, "/workspace")
}

func TestProcessWorkspacePoolUsesExactWritableBindWithoutRunnerRoot(t *testing.T) {
	runnerRoot := t.TempDir()
	workspace := filepath.Join(runnerRoot, "session")
	pool := filepath.Join(runnerRoot, ".process-workspaces", "session-hash")
	for _, dir := range []string{workspace, pool} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cfg := Config{WorkspacePath: workspace, WritableBinds: []string{pool}, TempHome: "/tmp/home", TempGHConfigDir: "/tmp/gh", TempXDGConfigHome: "/tmp/xdg", TempCodexHome: "/tmp/codex", SystemReadOnlyBinds: []string{"/usr"}}
	command, mounts, err := buildBwrapCommand(cfg, Command{Binary: "/usr/bin/true", Dir: workspace}, nil, "/usr/bin/bwrap")
	if err != nil {
		t.Fatal(err)
	}
	assertArgSequence(t, command.Args, "--bind", pool, pool)
	assertArgSequenceMissing(t, command.Args, "--bind", runnerRoot, runnerRoot)
	assertMount(t, mounts, Mount{Source: pool, Destination: pool, Mode: "rw"})
	assertArgSequence(t, command.Args, "--chdir", workspace)
}
