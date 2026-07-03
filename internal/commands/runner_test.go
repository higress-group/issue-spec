package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/commentrunner"
)

func TestRootUsageDocumentsRunnerCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Execute([]string{"--help"}, strings.NewReader(""), &out, &errOut)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "issue-spec runner poll") || !strings.Contains(out.String(), "issue-spec runner preflight") {
		t.Fatalf("runner usage missing from help:\n%s", out.String())
	}
}

func TestRunnerPollDryRunJSONUsesTrustedConfigAndPreflight(t *testing.T) {
	clearCommandAuthEnv(t)
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	var captured commentrunner.Config
	app.runnerPreflight = func(_ context.Context, cfg commentrunner.Config) commentrunner.PreflightReport {
		captured = cfg
		return commentrunner.PreflightReport{
			OK:     true,
			Config: cfg,
			Checks: []commentrunner.PreflightCheck{{
				Name:   "acpx",
				Status: commentrunner.CheckOK,
				Detail: "/test/bin/acpx",
			}},
		}
	}

	code := app.runRunner(context.Background(), []string{
		"poll",
		"--repo", "o/r,other/repo",
		"--runner", "issue-spec-bot",
		"--backend", "gh",
		"--state", "/tmp/state.json",
		"--workspace-root", "/tmp/workspaces",
		"--once",
		"--dry-run",
		"--unsafe-no-sandbox",
		"--json",
	})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%q stdout=%q", code, errOut.String(), out.String())
	}
	if captured.GitHubBackend != auth.GitHubBackendModeGH || !captured.UnsafeNoSandbox || len(captured.Repositories) != 2 {
		t.Fatalf("runner config not passed to preflight: %+v", captured)
	}
	var got runnerDryRunResult
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.OK || got.Mode != "dry-run" || !got.Once || got.Config.RunnerIdentity != "issue-spec-bot" {
		t.Fatalf("unexpected dry-run output: %+v", got)
	}
	if len(got.Actions) == 0 || !strings.Contains(strings.Join(got.Actions, "\n"), "skip GitHub writes") {
		t.Fatalf("dry-run output missing planned actions: %+v", got.Actions)
	}
}

func TestRunnerRejectsInvalidConfig(t *testing.T) {
	clearCommandAuthEnv(t)
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	code := app.runRunner(context.Background(), []string{
		"poll",
		"--repo", "not-a-repo",
		"--runner", "issue-spec-bot",
		"--dry-run",
	})
	if code != 2 {
		t.Fatalf("exit code = %d, want 2, stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
}

func TestRunnerPollWithoutDryRunDoesNotDispatchOrPreflight(t *testing.T) {
	clearCommandAuthEnv(t)
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.runnerPreflight = func(context.Context, commentrunner.Config) commentrunner.PreflightReport {
		t.Fatal("preflight should not run when poll would dispatch")
		return commentrunner.PreflightReport{}
	}
	code := app.runRunner(context.Background(), []string{
		"poll",
		"--repo", "o/r",
		"--runner", "issue-spec-bot",
		"--state", "/tmp/state.json",
		"--workspace-root", "/tmp/workspaces",
		"--once",
	})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1, stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if !strings.Contains(errOut.String(), "not implemented") {
		t.Fatalf("expected not implemented guard, stderr=%q", errOut.String())
	}
}
