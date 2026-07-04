package commentrunner

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	if cfg.WorkspaceRetention.Duration != 7*24*time.Hour {
		t.Fatalf("WorkspaceRetention = %s, want 168h", cfg.WorkspaceRetention.Duration)
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

func TestDefaultRunnerScopePathsUsesSingleRepoScope(t *testing.T) {
	home := t.TempDir()
	setDefaultConfigPathEnv(t, home, filepath.Join(t.TempDir(), "config"), filepath.Join(t.TempDir(), "cache"))

	statePath, workspaceRoot, err := DefaultRunnerScopePaths(Config{
		Hostname:       "https://GitHub.com/",
		Repositories:   []string{"Owner/Repo"},
		RunnerIdentity: "Issue-Spec-Bot",
	})
	if err != nil {
		t.Fatal(err)
	}

	root := filepath.Join(home, ".issue-spec", "runners", "github.com", "owner", "repo", "issue-spec-bot")
	if statePath != filepath.Join(root, "state.json") {
		t.Fatalf("statePath = %q", statePath)
	}
	if workspaceRoot != filepath.Join(root, "workspaces") {
		t.Fatalf("workspaceRoot = %q", workspaceRoot)
	}
}

func TestDefaultRunnerScopePathsIsolatesReposAndRunners(t *testing.T) {
	home := t.TempDir()
	setDefaultConfigPathEnv(t, home, filepath.Join(t.TempDir(), "config"), filepath.Join(t.TempDir(), "cache"))

	stateA, workspaceA, err := DefaultRunnerScopePaths(Config{Repositories: []string{"o/one"}, RunnerIdentity: "bot"})
	if err != nil {
		t.Fatal(err)
	}
	stateB, workspaceB, err := DefaultRunnerScopePaths(Config{Repositories: []string{"o/two"}, RunnerIdentity: "bot"})
	if err != nil {
		t.Fatal(err)
	}
	stateC, workspaceC, err := DefaultRunnerScopePaths(Config{Repositories: []string{"o/one"}, RunnerIdentity: "other-bot"})
	if err != nil {
		t.Fatal(err)
	}

	if stateA == stateB || workspaceA == workspaceB {
		t.Fatalf("different repos share paths: stateA=%q stateB=%q workspaceA=%q workspaceB=%q", stateA, stateB, workspaceA, workspaceB)
	}
	if stateA == stateC || workspaceA == workspaceC {
		t.Fatalf("different runners share paths: stateA=%q stateC=%q workspaceA=%q workspaceC=%q", stateA, stateC, workspaceA, workspaceC)
	}
}

func TestDefaultRunnerScopePathsUsesStableMultiRepoScope(t *testing.T) {
	home := t.TempDir()
	setDefaultConfigPathEnv(t, home, filepath.Join(t.TempDir(), "config"), filepath.Join(t.TempDir(), "cache"))

	stateA, workspaceA, err := DefaultRunnerScopePaths(Config{Repositories: []string{"b/two", "a/one"}, RunnerIdentity: "bot"})
	if err != nil {
		t.Fatal(err)
	}
	stateB, workspaceB, err := DefaultRunnerScopePaths(Config{Repositories: []string{"a/one", "b/two"}, RunnerIdentity: "bot"})
	if err != nil {
		t.Fatal(err)
	}

	if stateA != stateB || workspaceA != workspaceB {
		t.Fatalf("multi-repo scope is not stable: stateA=%q stateB=%q workspaceA=%q workspaceB=%q", stateA, stateB, workspaceA, workspaceB)
	}
	multiRoot := filepath.Join(home, ".issue-spec", "runners", "github.com", "multi")
	if !strings.HasPrefix(stateA, multiRoot+string(filepath.Separator)) {
		t.Fatalf("statePath = %q, want under %q", stateA, multiRoot)
	}
	if !strings.HasPrefix(workspaceA, multiRoot+string(filepath.Separator)) {
		t.Fatalf("workspaceRoot = %q, want under %q", workspaceA, multiRoot)
	}
}

func TestApplyDefaultRunnerScopePathsRespectsExplicitPaths(t *testing.T) {
	home := t.TempDir()
	setDefaultConfigPathEnv(t, home, filepath.Join(t.TempDir(), "config"), filepath.Join(t.TempDir(), "cache"))

	cfg := Config{
		Repositories:   []string{"o/r"},
		RunnerIdentity: "bot",
		StatePath:      "/custom/state.json",
		WorkspaceRoot:  "/custom/workspaces",
	}
	got, err := ApplyDefaultRunnerScopePaths(cfg, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if got.StatePath != "/custom/state.json" {
		t.Fatalf("StatePath = %q", got.StatePath)
	}
	if got.WorkspaceRoot != "/custom/workspaces" {
		t.Fatalf("WorkspaceRoot = %q", got.WorkspaceRoot)
	}

	got, err = ApplyDefaultRunnerScopePaths(cfg, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.StatePath != "/custom/state.json" {
		t.Fatalf("StatePath with explicit state = %q", got.StatePath)
	}
	if got.WorkspaceRoot == "/custom/workspaces" || !strings.Contains(got.WorkspaceRoot, filepath.Join("github.com", "o", "r", "bot")) {
		t.Fatalf("WorkspaceRoot with default workspace = %q", got.WorkspaceRoot)
	}

	got, err = ApplyDefaultRunnerScopePaths(cfg, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if got.StatePath == "/custom/state.json" || !strings.Contains(got.StatePath, filepath.Join("github.com", "o", "r", "bot")) {
		t.Fatalf("StatePath with default state = %q", got.StatePath)
	}
	if got.WorkspaceRoot != "/custom/workspaces" {
		t.Fatalf("WorkspaceRoot with explicit workspace = %q", got.WorkspaceRoot)
	}
}

func TestConfigNormalizedTrimsSplitsAndDeduplicatesAllowedUsers(t *testing.T) {
	cfg := Config{AllowedUsers: []string{" alice ", "bob,charlie", "ALICE", "bob", "  "}}.Normalized()

	if got := strings.Join(cfg.AllowedUsers, ","); got != "alice,bob,charlie" {
		t.Fatalf("AllowedUsers = %q, want alice,bob,charlie", got)
	}
}

func TestConfigValidateRequiresNotificationTokenEnvWithIdentity(t *testing.T) {
	cfg := Config{
		Repositories:         []string{"o/r"},
		RunnerIdentity:       "bot",
		NotificationIdentity: "notify-bot",
		StatePath:            filepath.Join(t.TempDir(), "state.json"),
		WorkspaceRoot:        t.TempDir(),
		PollInterval:         NewDuration(time.Minute),
		FallbackInterval:     NewDuration(time.Hour),
		WorkspaceRetention:   NewDuration(24 * time.Hour),
		MaxConcurrentJobs:    1,
		Agent:                DefaultAgentConfig(),
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "--notification-token-env is required") {
		t.Fatalf("Validate error = %v", err)
	}
}

func setDefaultConfigPathEnv(t *testing.T, home, configDir, cacheDir string) {
	t.Helper()
	t.Setenv(auth.GitHubBackendEnv, "")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configDir)
	t.Setenv("XDG_CACHE_HOME", cacheDir)
}
