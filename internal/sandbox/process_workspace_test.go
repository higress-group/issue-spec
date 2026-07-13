package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

func TestUnsafeNoSandboxKeepsProcessWorkspaceEnvButReportsNoFSIsolation(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, "session")
	pool := filepath.Join(root, "pool")
	for _, dir := range []string{workspace, pool, filepath.Join(root, "home"), filepath.Join(root, "gh"), filepath.Join(root, "xdg")} {
		if err := mkdirPrivate(dir); err != nil {
			t.Fatal(err)
		}
	}
	prepared, err := Prepare(context.Background(), Config{
		UnsafeNoSandbox: true, WorkspacePath: workspace, WritableBinds: []string{pool},
		TempHome: filepath.Join(root, "home"), TempGHConfigDir: filepath.Join(root, "gh"), TempXDGConfigHome: filepath.Join(root, "xdg"),
		ExtraEnv: map[string]string{"ISSUE_SPEC_PROCESS_WORKSPACE_ROOT": pool},
	}, Command{Binary: "/usr/bin/true", Dir: workspace}, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	if got := envMapFromEntries(prepared.Command.Env)["ISSUE_SPEC_PROCESS_WORKSPACE_ROOT"]; got != pool {
		t.Fatalf("trusted PROCESS env = %q, want %q", got, pool)
	}
	if prepared.Metadata.FSBoundary != FSBoundaryDisabled || len(prepared.Metadata.Mounts) != 0 ||
		!strings.Contains(strings.Join(prepared.Metadata.Diagnostics, "\n"), "filesystem access is not constrained") {
		t.Fatalf("unsafe metadata does not clearly report absent FS isolation: %+v", prepared.Metadata)
	}
}

func TestWritableBindsRejectSymlinkOverlapAndDefaultSystemPath(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "session")
	pool := filepath.Join(root, "pool")
	for _, dir := range []string{workspace, pool} {
		if err := mkdirPrivate(dir); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(root, "pool-link")
	if err := os.Symlink(pool, link); err == nil {
		if _, err := validatedWritableBinds(Config{WorkspacePath: workspace, WritableBinds: []string{link}}); err == nil {
			t.Fatal("expected symlink writable bind to be rejected")
		}
	}
	if _, err := validatedWritableBinds(Config{WorkspacePath: workspace, WritableBinds: []string{root}}); err == nil {
		t.Fatal("expected ancestor overlap with workspace to be rejected")
	}
	if _, err := validatedWritableBinds(Config{WorkspacePath: workspace, WritableBinds: []string{"/usr"}}); err == nil {
		t.Fatal("expected overlap with default system read-only bind to be rejected")
	}
}

func TestWritableBindsCanonicalizeSymlinkWorkspaceBeforeOverlapCheck(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	realWorkspace := filepath.Join(root, "real-session")
	poolInsideWorkspace := filepath.Join(realWorkspace, "pool")
	alias := filepath.Join(root, "session-alias")
	if err := os.MkdirAll(poolInsideWorkspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realWorkspace, alias); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, err := validatedWritableBinds(Config{WorkspacePath: alias, WritableBinds: []string{poolInsideWorkspace}}); err == nil {
		t.Fatal("expected pool inside symlinked workspace to be rejected")
	}
}

func mkdirPrivate(path string) error { return os.MkdirAll(path, 0o700) }
