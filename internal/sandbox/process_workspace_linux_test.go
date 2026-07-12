//go:build linux

package sandbox

import "testing"

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
