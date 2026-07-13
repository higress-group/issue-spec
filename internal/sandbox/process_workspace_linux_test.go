//go:build linux

package sandbox

import (
	"os"
	"path/filepath"
	"strings"
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
	siblingPool := filepath.Join(runnerRoot, ".process-workspaces", "other-session-hash")
	for _, dir := range []string{workspace, pool, siblingPool} {
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
	assertArgSequenceMissing(t, command.Args, "--bind", siblingPool, siblingPool)
	assertNoWritableSourceCovers(t, siblingPool, command.Args, mounts)
	assertMount(t, mounts, Mount{Source: pool, Destination: pool, Mode: "rw"})
	assertArgSequence(t, command.Args, "--chdir", workspace)
}

func assertNoWritableSourceCovers(t *testing.T, target string, args []string, mounts []Mount) {
	t.Helper()
	var sources []string
	for index := 0; index+2 < len(args); index++ {
		if args[index] == "--bind" {
			sources = append(sources, args[index+1])
			index += 2
		}
	}
	for _, mount := range mounts {
		if mount.Mode == "rw" && mount.Source != "" {
			sources = append(sources, mount.Source)
		}
	}
	for _, source := range sources {
		rel, err := filepath.Rel(filepath.Clean(source), filepath.Clean(target))
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			t.Fatalf("writable source %q covers sibling session pool %q; all sources=%v", source, target, sources)
		}
	}
}
