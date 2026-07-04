package commentrunner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/github"
)

func TestPreflightReportsMissingBwrapAndAcpx(t *testing.T) {
	cfg := testPreflightConfig(t)
	report := RunPreflight(context.Background(), cfg, PreflightDependencies{
		SelectBackend: func(context.Context, string) (auth.GitHubBackendSelection, error) {
			return auth.GitHubBackendSelection{
				Mode:            auth.GitHubBackendModeGH,
				Name:            auth.GitHubBackendNameGH,
				Kind:            auth.GitHubBackendKindCLI,
				Host:            "github.com",
				SelectionSource: "test",
			}, nil
		},
		OpenBackend: watchedPreflightBackend,
		LookPath: func(name string) (string, error) {
			if name == "gh" {
				return "/test/bin/gh", nil
			}
			return "", errors.New("missing")
		},
		RunCommand: func(context.Context, string, ...string) ([]byte, error) {
			t.Fatal("bwrap --help should not run when bwrap is missing")
			return nil, nil
		},
	})

	if report.OK {
		t.Fatalf("preflight unexpectedly OK: %+v", report)
	}
	bwrap := findCheck(t, report, "bwrap")
	if bwrap.Status != CheckError || !strings.Contains(bwrap.Detail, "bwrap not found") || !strings.Contains(bwrap.Hint, "Install or upgrade bubblewrap") {
		t.Fatalf("unexpected bwrap check: %+v", bwrap)
	}
	acpx := findCheck(t, report, "acpx")
	if acpx.Status != CheckError || acpx.Hint != acpxInstallHint {
		t.Fatalf("unexpected acpx check: %+v", acpx)
	}
}

func TestPreflightUnsafeNoSandboxSkipsBwrapAndMarksBoundaryDisabled(t *testing.T) {
	cfg := testPreflightConfig(t)
	cfg.UnsafeNoSandbox = true
	report := RunPreflight(context.Background(), cfg, PreflightDependencies{
		SelectBackend: func(context.Context, string) (auth.GitHubBackendSelection, error) {
			return auth.GitHubBackendSelection{
				Mode:            auth.GitHubBackendModeGH,
				Name:            auth.GitHubBackendNameGH,
				Kind:            auth.GitHubBackendKindCLI,
				Host:            "github.com",
				SelectionSource: "test",
			}, nil
		},
		OpenBackend: watchedPreflightBackend,
		LookPath: func(name string) (string, error) {
			switch name {
			case "gh":
				return "/test/bin/gh", nil
			case "acpx", "npm", "npx":
				return "/test/bin/" + name, nil
			case "bwrap":
				t.Fatal("bwrap lookup should be skipped in unsafe mode")
			}
			return "", errors.New("missing")
		},
	})

	if !report.OK {
		t.Fatalf("preflight should tolerate unsafe warning/skips: %+v", report)
	}
	unsafe := findCheck(t, report, "unsafe-no-sandbox")
	if unsafe.Status != CheckWarning || !strings.Contains(unsafe.Detail, "fs_boundary=disabled") {
		t.Fatalf("unexpected unsafe check: %+v", unsafe)
	}
	bwrap := findCheck(t, report, "bwrap")
	if bwrap.Status != CheckSkipped {
		t.Fatalf("bwrap not skipped in unsafe mode: %+v", bwrap)
	}
}

func TestPreflightFailsCodexACPToolchainWhenNpxMissing(t *testing.T) {
	cfg := testPreflightConfig(t)
	cfg.UnsafeNoSandbox = true
	report := RunPreflight(context.Background(), cfg, PreflightDependencies{
		SelectBackend: func(context.Context, string) (auth.GitHubBackendSelection, error) {
			return auth.GitHubBackendSelection{
				Mode:            auth.GitHubBackendModeGH,
				Name:            auth.GitHubBackendNameGH,
				Kind:            auth.GitHubBackendKindCLI,
				Host:            "github.com",
				SelectionSource: "test",
			}, nil
		},
		OpenBackend: watchedPreflightBackend,
		LookPath: func(name string) (string, error) {
			switch name {
			case "gh", "acpx", "npm":
				return "/test/bin/" + name, nil
			case "bwrap":
				t.Fatal("bwrap lookup should be skipped in unsafe mode")
			}
			return "", errors.New("missing")
		},
	})

	if report.OK {
		t.Fatalf("preflight unexpectedly OK without npx: %+v", report)
	}
	check := findCheck(t, report, "codex-acp")
	if check.Status != CheckError || !strings.Contains(check.Detail, "npx -y "+codexACPPackage) || !strings.Contains(check.Detail, "npx not found") || !strings.Contains(check.Hint, "acpx") || !strings.Contains(check.Hint, codexACPPackage) {
		t.Fatalf("unexpected codex-acp check: %+v", check)
	}
}

func TestPreflightFailsWhenRepositoryWatchCannotBeConfirmed(t *testing.T) {
	cfg := testPreflightConfig(t)
	cfg.UnsafeNoSandbox = true
	report := RunPreflight(context.Background(), cfg, PreflightDependencies{
		SelectBackend: func(context.Context, string) (auth.GitHubBackendSelection, error) {
			return auth.GitHubBackendSelection{
				Mode:            auth.GitHubBackendModeREST,
				Name:            auth.GitHubBackendNameREST,
				Kind:            auth.GitHubBackendKindREST,
				Host:            "github.com",
				SelectionSource: "test",
			}, nil
		},
		OpenBackend: func(context.Context, auth.GitHubBackendSelection) (PreflightRunnerBackend, error) {
			return fakePreflightBackend{subscription: github.RepositorySubscription{Subscribed: false}}, nil
		},
		LookPath: func(name string) (string, error) {
			switch name {
			case "acpx", "npm", "npx":
				return "/test/bin/" + name, nil
			}
			return "", errors.New("missing")
		},
	})

	if report.OK {
		t.Fatalf("preflight unexpectedly OK: %+v", report)
	}
	watch := findCheck(t, report, "repository-watch:o/r")
	if watch.Status != CheckError || !strings.Contains(watch.Detail, "not subscribed") {
		t.Fatalf("unexpected repository watch check: %+v", watch)
	}
}

func TestPreflightUsesNotificationBackendForRepositoryWatchWhenConfigured(t *testing.T) {
	cfg := testPreflightConfig(t)
	cfg.UnsafeNoSandbox = true
	cfg.GitHubBackend = auth.GitHubBackendModeREST
	cfg.NotificationIdentity = "notify-bot"
	cfg.NotificationTokenEnv = "BOT_TOKEN"
	report := RunPreflight(context.Background(), cfg, PreflightDependencies{
		SelectBackend: func(context.Context, string) (auth.GitHubBackendSelection, error) {
			return auth.GitHubBackendSelection{
				Mode:            auth.GitHubBackendModeREST,
				Name:            auth.GitHubBackendNameREST,
				Kind:            auth.GitHubBackendKindREST,
				Host:            "github.com",
				SelectionSource: "test",
			}, nil
		},
		OpenBackend: func(context.Context, auth.GitHubBackendSelection) (PreflightRunnerBackend, error) {
			return fakePreflightBackend{subscription: github.RepositorySubscription{Subscribed: false}}, nil
		},
		OpenNotificationBackend: func(context.Context, Config) (PreflightNotificationBackend, error) {
			return fakeNotificationPreflightBackend{
				fakePreflightBackend: fakePreflightBackend{subscription: github.RepositorySubscription{Subscribed: true, Reason: "subscribed"}},
				user:                 github.User{Login: "notify-bot"},
			}, nil
		},
		LookPath: func(name string) (string, error) {
			switch name {
			case "acpx", "npm", "npx":
				return "/test/bin/" + name, nil
			}
			return "", errors.New("missing")
		},
	})

	if !report.OK {
		t.Fatalf("preflight should accept watched notification backend: %+v", report)
	}
	if check := findCheck(t, report, "notification-backend"); check.Status != CheckOK {
		t.Fatalf("unexpected notification backend check: %+v", check)
	}
	if check := findCheck(t, report, "notification-identity"); check.Status != CheckOK || !strings.Contains(check.Detail, "notify-bot") {
		t.Fatalf("unexpected notification identity check: %+v", check)
	}
	if check := findCheck(t, report, "notification-watch:o/r"); check.Status != CheckOK {
		t.Fatalf("unexpected notification watch check: %+v", check)
	}
	if hasCheck(report, "repository-watch:o/r") {
		t.Fatalf("main repository watch should not be required when notification backend is configured: %+v", report.Checks)
	}
}

func TestPreflightNamesNotificationWatchWhenNotificationBackendUnavailable(t *testing.T) {
	cfg := testPreflightConfig(t)
	cfg.UnsafeNoSandbox = true
	cfg.NotificationIdentity = "notify-bot"
	cfg.NotificationTokenEnv = "BOT_TOKEN"
	report := RunPreflight(context.Background(), cfg, PreflightDependencies{
		SelectBackend: func(context.Context, string) (auth.GitHubBackendSelection, error) {
			return auth.GitHubBackendSelection{
				Mode:            auth.GitHubBackendModeREST,
				Name:            auth.GitHubBackendNameREST,
				Kind:            auth.GitHubBackendKindREST,
				Host:            "github.com",
				SelectionSource: "test",
			}, nil
		},
		OpenBackend: watchedPreflightBackend,
		OpenNotificationBackend: func(context.Context, Config) (PreflightNotificationBackend, error) {
			return nil, errors.New("bot token missing")
		},
		LookPath: func(name string) (string, error) {
			switch name {
			case "acpx", "npm", "npx":
				return "/test/bin/" + name, nil
			}
			return "", errors.New("missing")
		},
	})

	if report.OK {
		t.Fatalf("preflight unexpectedly OK without notification backend: %+v", report)
	}
	if check := findCheck(t, report, "notification-backend"); check.Status != CheckError || !strings.Contains(check.Detail, "bot token missing") {
		t.Fatalf("unexpected notification backend check: %+v", check)
	}
	if check := findCheck(t, report, "notification-watch:o/r"); check.Status != CheckError || !strings.Contains(check.Detail, "bot token missing") {
		t.Fatalf("unexpected notification watch check: %+v", check)
	}
	if hasCheck(report, "repository-watch:o/r") {
		t.Fatalf("notification backend failure should still use notification watch prefix: %+v", report.Checks)
	}
}

func TestPreflightSkipsNotificationBackendWhenNotConfigured(t *testing.T) {
	cfg := testPreflightConfig(t)
	cfg.UnsafeNoSandbox = true
	cfg.GitHubBackend = auth.GitHubBackendModeREST
	report := RunPreflight(context.Background(), cfg, PreflightDependencies{
		SelectBackend: func(context.Context, string) (auth.GitHubBackendSelection, error) {
			return auth.GitHubBackendSelection{
				Mode:            auth.GitHubBackendModeREST,
				Name:            auth.GitHubBackendNameREST,
				Kind:            auth.GitHubBackendKindREST,
				Host:            "github.com",
				SelectionSource: "test",
			}, nil
		},
		OpenBackend: watchedPreflightBackend,
		OpenNotificationBackend: func(context.Context, Config) (PreflightNotificationBackend, error) {
			t.Fatal("notification backend should not be opened without notification token env")
			return nil, nil
		},
		LookPath: func(name string) (string, error) {
			switch name {
			case "acpx", "npm", "npx":
				return "/test/bin/" + name, nil
			}
			return "", errors.New("missing")
		},
	})

	if !report.OK {
		t.Fatalf("preflight should pass with main runner notification polling: %+v", report)
	}
	if check := findCheck(t, report, "notification-backend"); check.Status != CheckSkipped {
		t.Fatalf("unexpected notification backend check: %+v", check)
	}
	if check := findCheck(t, report, "repository-watch:o/r"); check.Status != CheckOK {
		t.Fatalf("unexpected repository watch check: %+v", check)
	}
}

func TestDefaultPreflightNotificationBackendFailsClosedWhenTokenEmpty(t *testing.T) {
	t.Setenv("BOT_TOKEN", " ")
	backend, err := defaultPreflightNotificationBackend(context.Background(), Config{NotificationTokenEnv: "BOT_TOKEN"})
	if err == nil || !strings.Contains(err.Error(), "BOT_TOKEN is empty") {
		t.Fatalf("defaultPreflightNotificationBackend error = %v, backend=%T", err, backend)
	}
}

func TestDefaultPreflightNotificationBackendFailsClosedWhenTokenUnset(t *testing.T) {
	unsetEnvForTest(t, "BOT_TOKEN")
	backend, err := defaultPreflightNotificationBackend(context.Background(), Config{NotificationTokenEnv: "BOT_TOKEN"})
	if err == nil || !strings.Contains(err.Error(), "BOT_TOKEN is unset") {
		t.Fatalf("defaultPreflightNotificationBackend error = %v, backend=%T", err, backend)
	}
}

func unsetEnvForTest(t *testing.T, name string) {
	t.Helper()
	oldValue, hadValue := os.LookupEnv(name)
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if hadValue {
			_ = os.Setenv(name, oldValue)
			return
		}
		_ = os.Unsetenv(name)
	})
}

func TestPreflightRejectsNotificationIdentityMismatch(t *testing.T) {
	cfg := testPreflightConfig(t)
	cfg.UnsafeNoSandbox = true
	cfg.GitHubBackend = auth.GitHubBackendModeREST
	cfg.NotificationIdentity = "notify-bot"
	cfg.NotificationTokenEnv = "BOT_TOKEN"
	report := RunPreflight(context.Background(), cfg, PreflightDependencies{
		SelectBackend: func(context.Context, string) (auth.GitHubBackendSelection, error) {
			return auth.GitHubBackendSelection{
				Mode:            auth.GitHubBackendModeREST,
				Name:            auth.GitHubBackendNameREST,
				Kind:            auth.GitHubBackendKindREST,
				Host:            "github.com",
				SelectionSource: "test",
			}, nil
		},
		OpenBackend: watchedPreflightBackend,
		OpenNotificationBackend: func(context.Context, Config) (PreflightNotificationBackend, error) {
			return fakeNotificationPreflightBackend{
				fakePreflightBackend: fakePreflightBackend{subscription: github.RepositorySubscription{Subscribed: true}},
				user:                 github.User{Login: "other-bot"},
			}, nil
		},
		LookPath: func(name string) (string, error) {
			switch name {
			case "acpx", "npm", "npx":
				return "/test/bin/" + name, nil
			}
			return "", errors.New("missing")
		},
	})

	if report.OK {
		t.Fatalf("preflight unexpectedly OK with mismatched notification token: %+v", report)
	}
	check := findCheck(t, report, "notification-identity")
	if check.Status != CheckError || !strings.Contains(check.Detail, "other-bot") || !strings.Contains(check.Detail, "notify-bot") {
		t.Fatalf("unexpected notification identity check: %+v", check)
	}
}

func TestPreflightFailsCodexAuthBeforeRunnerStarts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, "missing-codex"))
	cfg := testPreflightConfigWithoutAuth()
	cfg.UnsafeNoSandbox = true

	report := RunPreflight(context.Background(), cfg, passingPreflightDependencies(t))
	if report.OK {
		t.Fatalf("preflight unexpectedly OK without codex auth: %+v", report)
	}
	codex := findCheck(t, report, "codex-auth")
	if codex.Status != CheckError || !strings.Contains(codex.Detail, "Codex auth unavailable") || !strings.Contains(codex.Hint, "CODEX_HOME") {
		t.Fatalf("unexpected codex auth check: %+v", codex)
	}
	claude := findCheck(t, report, "claude-auth")
	if claude.Status != CheckSkipped || !strings.Contains(claude.Detail, "configured agent is codex") {
		t.Fatalf("claude auth should be skipped for codex agent: %+v", claude)
	}
}

func TestPreflightDistinguishesClaudeAuthFromCodexAuth(t *testing.T) {
	home := t.TempDir()
	codexHome := filepath.Join(home, "codex")
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "auth.json"), []byte(`{"token":"codex"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", codexHome)
	cfg := testPreflightConfigWithoutAuth()
	cfg.Agent.Kind = AgentClaude
	cfg.UnsafeNoSandbox = true

	report := RunPreflight(context.Background(), cfg, passingPreflightDependencies(t))
	if report.OK {
		t.Fatalf("preflight unexpectedly OK with only codex auth for claude agent: %+v", report)
	}
	codex := findCheck(t, report, "codex-auth")
	if codex.Status != CheckSkipped || !strings.Contains(codex.Detail, "configured agent is claude") {
		t.Fatalf("codex auth should be skipped for claude agent: %+v", codex)
	}
	claude := findCheck(t, report, "claude-auth")
	if claude.Status != CheckError || !strings.Contains(claude.Detail, "Claude Code auth unavailable") || !strings.Contains(claude.Hint, "claude login") {
		t.Fatalf("unexpected claude auth check: %+v", claude)
	}
}

func TestPreflightAcceptsClaudeCredentialsForClaudeAgent(t *testing.T) {
	home := t.TempDir()
	claudeHome := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeHome, ".credentials.json"), []byte(`{"token":"claude"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, "missing-codex"))
	cfg := testPreflightConfigWithoutAuth()
	cfg.Agent.Kind = AgentClaude
	cfg.UnsafeNoSandbox = true

	report := RunPreflight(context.Background(), cfg, passingPreflightDependencies(t))
	if !report.OK {
		t.Fatalf("preflight should accept claude credentials for claude agent: %+v", report)
	}
	claude := findCheck(t, report, "claude-auth")
	if claude.Status != CheckOK || !strings.Contains(claude.Detail, ".credentials.json") {
		t.Fatalf("unexpected claude auth check: %+v", claude)
	}
}

func testPreflightConfig(t *testing.T) Config {
	t.Helper()
	home := t.TempDir()
	codexHome := filepath.Join(home, "codex")
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "auth.json"), []byte(`{"token":"codex"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", codexHome)
	return testPreflightConfigWithoutAuth()
}

func testPreflightConfigWithoutAuth() Config {
	return Config{
		Hostname:            "github.com",
		Repositories:        []string{"o/r"},
		RunnerIdentity:      "issue-spec-bot",
		GitHubBackend:       auth.GitHubBackendModeGH,
		StatePath:           "state.json",
		PollInterval:        NewDuration(time.Minute),
		FallbackInterval:    NewDuration(5 * time.Minute),
		MaxConcurrentJobs:   1,
		AcpxPath:            "acpx",
		Agent:               DefaultAgentConfig(),
		WorkspaceRoot:       "workspaces",
		WorkspaceRetention:  NewDuration(time.Hour),
		CancellationEnabled: true,
	}
}

func passingPreflightDependencies(t *testing.T) PreflightDependencies {
	t.Helper()
	return PreflightDependencies{
		SelectBackend: func(context.Context, string) (auth.GitHubBackendSelection, error) {
			return auth.GitHubBackendSelection{
				Mode:            auth.GitHubBackendModeGH,
				Name:            auth.GitHubBackendNameGH,
				Kind:            auth.GitHubBackendKindCLI,
				Host:            "github.com",
				SelectionSource: "test",
			}, nil
		},
		OpenBackend: watchedPreflightBackend,
		LookPath: func(name string) (string, error) {
			switch name {
			case "gh", "acpx", "npm", "npx":
				return "/test/bin/" + name, nil
			case "bwrap":
				t.Fatal("bwrap lookup should be skipped in unsafe mode")
			}
			return "", errors.New("missing")
		},
	}
}

func findCheck(t *testing.T, report PreflightReport, name string) PreflightCheck {
	t.Helper()
	for _, check := range report.Checks {
		if check.Name == name {
			return check
		}
	}
	t.Fatalf("missing preflight check %q in %+v", name, report.Checks)
	return PreflightCheck{}
}

func hasCheck(report PreflightReport, name string) bool {
	for _, check := range report.Checks {
		if check.Name == name {
			return true
		}
	}
	return false
}

func watchedPreflightBackend(context.Context, auth.GitHubBackendSelection) (PreflightRunnerBackend, error) {
	return fakePreflightBackend{subscription: github.RepositorySubscription{Subscribed: true, Reason: "subscribed"}}, nil
}

type fakePreflightBackend struct {
	subscription github.RepositorySubscription
	err          error
}

func (f fakePreflightBackend) BackendInfo() github.BackendInfo {
	return github.BackendInfo{Name: "fake", Host: "github.com"}
}

func (f fakePreflightBackend) GetRepositorySubscription(context.Context, string) (github.RepositorySubscriptionResult, error) {
	return github.RepositorySubscriptionResult{Subscription: f.subscription}, f.err
}

type fakeNotificationPreflightBackend struct {
	fakePreflightBackend
	user github.User
}

func (f fakeNotificationPreflightBackend) GetUser(context.Context) (github.User, []string, error) {
	return f.user, nil, nil
}
