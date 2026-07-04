package commentrunner

import (
	"path/filepath"
	"testing"

	"github.com/higress-group/issue-spec/internal/auth"
)

func TestDefaultConfigFromEnvUsesUnifiedIssueSpecHomeDir(t *testing.T) {
	home := t.TempDir()
	setDefaultConfigPathEnv(t, home, filepath.Join(t.TempDir(), "config"), filepath.Join(t.TempDir(), "cache"))

	cfg, err := DefaultConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}

	root := filepath.Join(home, ".issue-spec")
	if cfg.StatePath != filepath.Join(root, "runner-state.json") {
		t.Fatalf("StatePath = %q", cfg.StatePath)
	}
	if cfg.WorkspaceRoot != filepath.Join(root, "workspaces") {
		t.Fatalf("WorkspaceRoot = %q", cfg.WorkspaceRoot)
	}
}

func TestDefaultConfigFromEnvFallsBackToXDGDirsWhenHomeEmpty(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "config")
	cacheDir := filepath.Join(t.TempDir(), "cache")
	setDefaultConfigPathEnv(t, "", configDir, cacheDir)

	cfg, err := DefaultConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}

	if cfg.StatePath != filepath.Join(configDir, "issue-spec", "runner-state.json") {
		t.Fatalf("StatePath = %q", cfg.StatePath)
	}
	if cfg.WorkspaceRoot != filepath.Join(cacheDir, "issue-spec", "runner-workspaces") {
		t.Fatalf("WorkspaceRoot = %q", cfg.WorkspaceRoot)
	}
}

func TestDefaultConfigFromEnvFallsBackToXDGDirsWhenHomeIsRoot(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "config")
	cacheDir := filepath.Join(t.TempDir(), "cache")
	setDefaultConfigPathEnv(t, string(filepath.Separator), configDir, cacheDir)

	cfg, err := DefaultConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}

	if cfg.StatePath != filepath.Join(configDir, "issue-spec", "runner-state.json") {
		t.Fatalf("StatePath = %q", cfg.StatePath)
	}
	if cfg.WorkspaceRoot != filepath.Join(cacheDir, "issue-spec", "runner-workspaces") {
		t.Fatalf("WorkspaceRoot = %q", cfg.WorkspaceRoot)
	}
}

func setDefaultConfigPathEnv(t *testing.T, home, configDir, cacheDir string) {
	t.Helper()
	t.Setenv(auth.GitHubBackendEnv, "")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configDir)
	t.Setenv("XDG_CACHE_HOME", cacheDir)
}
