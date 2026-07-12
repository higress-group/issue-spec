package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestUnsafeNoSandboxIsExplicitAndReportsDisabledReadOnlyBoundary(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "detached-review")
	for _, dir := range []string{workspace, filepath.Join(root, "home"), filepath.Join(root, "gh"), filepath.Join(root, "xdg"), filepath.Join(root, "codex")} {
		if err := mkdirPrivate(dir); err != nil {
			t.Fatal(err)
		}
	}
	prepared, err := Prepare(context.Background(), Config{UnsafeNoSandbox: true, WorkspaceReadOnly: true, WorkspacePath: workspace,
		TempHome: filepath.Join(root, "home"), TempGHConfigDir: filepath.Join(root, "gh"), TempXDGConfigHome: filepath.Join(root, "xdg"), TempCodexHome: filepath.Join(root, "codex")},
		Command{Binary: "/usr/bin/true", Dir: workspace}, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Metadata.SandboxProvider != ProviderNone || prepared.Metadata.FSBoundary != FSBoundaryDisabled || !prepared.Metadata.UnsafeNoSandbox || prepared.Command.Dir != workspace {
		t.Fatalf("explicit unsafe metadata=%+v command=%+v", prepared.Metadata, prepared.Command)
	}
}

func mkdirPrivate(path string) error { return os.MkdirAll(path, 0o700) }
