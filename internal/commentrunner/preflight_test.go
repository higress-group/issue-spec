package commentrunner

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/auth"
)

func TestPreflightReportsMissingBwrapAndAcpx(t *testing.T) {
	cfg := testPreflightConfig()
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
	cfg := testPreflightConfig()
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
		LookPath: func(name string) (string, error) {
			switch name {
			case "gh":
				return "/test/bin/gh", nil
			case "acpx":
				return "/test/bin/acpx", nil
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

func testPreflightConfig() Config {
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
