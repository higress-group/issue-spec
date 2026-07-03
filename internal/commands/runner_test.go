package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/commentrunner"
	"github.com/higress-group/issue-spec/internal/commentrunner/intake"
	"github.com/higress-group/issue-spec/internal/commentrunner/jobs"
	"github.com/higress-group/issue-spec/internal/commentrunner/state"
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
	app.runnerIntake = func(_ context.Context, cfg commentrunner.Config, opts intake.Options) (intake.Result, error) {
		if !opts.DryRun {
			t.Fatal("runner dry-run must call intake with dry-run enabled")
		}
		if cfg.RunnerIdentity != "issue-spec-bot" {
			t.Fatalf("runner config not passed to intake: %+v", cfg)
		}
		return intake.Result{
			OK:     true,
			DryRun: true,
			Next:   intake.NextStep{PollAt: time.Date(2026, 7, 3, 12, 1, 0, 0, time.UTC)},
		}, nil
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
	if !got.OK || got.Mode != "dry-run" || !got.Once || got.Config.RunnerIdentity != "issue-spec-bot" || got.Intake == nil {
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

func TestRunnerPollDryRunIntakeErrorReturnsFailure(t *testing.T) {
	clearCommandAuthEnv(t)
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.runnerPreflight = func(_ context.Context, cfg commentrunner.Config) commentrunner.PreflightReport {
		return commentrunner.PreflightReport{OK: true, Config: cfg}
	}
	app.runnerIntake = func(context.Context, commentrunner.Config, intake.Options) (intake.Result, error) {
		return intake.Result{}, errors.New("intake failed")
	}
	code := app.runRunner(context.Background(), []string{
		"poll",
		"--repo", "o/r",
		"--runner", "issue-spec-bot",
		"--state", "/tmp/state.json",
		"--workspace-root", "/tmp/workspaces",
		"--dry-run",
		"--json",
	})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1, stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	var got runnerDryRunResult
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.OK || !strings.Contains(got.Error, "intake failed") {
		t.Fatalf("unexpected dry-run result: %+v", got)
	}
}

func TestRunnerPollWithoutDryRunRunsIntakeAndOneDispatch(t *testing.T) {
	clearCommandAuthEnv(t)
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.runnerPreflight = func(_ context.Context, cfg commentrunner.Config) commentrunner.PreflightReport {
		return commentrunner.PreflightReport{OK: true, Config: cfg}
	}
	intakeCalled := false
	app.runnerIntake = func(_ context.Context, cfg commentrunner.Config, opts intake.Options) (intake.Result, error) {
		intakeCalled = true
		if opts.DryRun {
			t.Fatal("non-dry-run poll must persist intake")
		}
		if cfg.RunnerIdentity != "issue-spec-bot" {
			t.Fatalf("config not passed to intake: %+v", cfg)
		}
		return intake.Result{OK: true}, nil
	}
	dispatchCalled := false
	app.runnerDispatch = func(_ context.Context, cfg commentrunner.Config) (jobs.Result, error) {
		dispatchCalled = true
		if cfg.WorkspaceRoot != "/tmp/workspaces" {
			t.Fatalf("config not passed to dispatch: %+v", cfg)
		}
		return jobs.Result{Executed: true, JobID: "job-1", Status: state.StatusCompleted}, nil
	}
	code := app.runRunner(context.Background(), []string{
		"poll",
		"--repo", "o/r",
		"--runner", "issue-spec-bot",
		"--state", "/tmp/state.json",
		"--workspace-root", "/tmp/workspaces",
		"--once",
		"--json",
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0, stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if !intakeCalled || !dispatchCalled {
		t.Fatalf("expected intake and dispatch to run: intake=%v dispatch=%v", intakeCalled, dispatchCalled)
	}
	var got runnerDryRunResult
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Mode != "run" || got.Dispatch == nil || got.Dispatch.Status != state.StatusCompleted {
		t.Fatalf("unexpected run output: %+v", got)
	}
}
