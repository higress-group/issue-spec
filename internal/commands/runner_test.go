package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/commentrunner"
	"github.com/higress-group/issue-spec/internal/commentrunner/intake"
	"github.com/higress-group/issue-spec/internal/commentrunner/jobs"
	"github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/github"
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

func TestIssueSpecBinaryForRunnerUsesCurrentExecutable(t *testing.T) {
	old := runnerExecutable
	t.Cleanup(func() { runnerExecutable = old })
	runnerExecutable = func() (string, error) {
		return "/tmp/issue-spec-runner-e2e-001/bin/issue-spec", nil
	}

	if got := issueSpecBinaryForRunner(); got != "/tmp/issue-spec-runner-e2e-001/bin/issue-spec" {
		t.Fatalf("issueSpecBinaryForRunner() = %q", got)
	}
}

func TestIssueSpecBinaryForRunnerFallsBackToCommandName(t *testing.T) {
	old := runnerExecutable
	t.Cleanup(func() { runnerExecutable = old })
	runnerExecutable = func() (string, error) {
		return "", errors.New("executable unavailable")
	}

	if got := issueSpecBinaryForRunner(); got != "issue-spec" {
		t.Fatalf("issueSpecBinaryForRunner() = %q", got)
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
	app.runnerDispatch = func(context.Context, commentrunner.Config) (jobs.Result, error) {
		t.Fatal("runner dry-run must not dispatch jobs")
		return jobs.Result{}, nil
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

func TestRunnerPollUsesDefaultStateAndWorkspaceRoot(t *testing.T) {
	clearCommandAuthEnv(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))

	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	var captured commentrunner.Config
	app.runnerPreflight = func(_ context.Context, cfg commentrunner.Config) commentrunner.PreflightReport {
		captured = cfg
		return commentrunner.PreflightReport{OK: true, Config: cfg}
	}
	app.runnerIntake = func(context.Context, commentrunner.Config, intake.Options) (intake.Result, error) {
		return intake.Result{OK: true, DryRun: true}, nil
	}
	app.runnerDispatch = func(context.Context, commentrunner.Config) (jobs.Result, error) {
		t.Fatal("dry-run must not dispatch jobs")
		return jobs.Result{}, nil
	}

	code := app.runRunner(context.Background(), []string{
		"poll",
		"--repo", "o/r",
		"--runner", "issue-spec-bot",
		"--dry-run",
		"--json",
	})
	if code != 0 {
		t.Fatalf("exit code = %d, stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	root := filepath.Join(home, ".issue-spec")
	if captured.StatePath != filepath.Join(root, "runner-state.json") {
		t.Fatalf("StatePath = %q", captured.StatePath)
	}
	if captured.WorkspaceRoot != filepath.Join(root, "workspaces") {
		t.Fatalf("WorkspaceRoot = %q", captured.WorkspaceRoot)
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

func TestRunnerPollDryRunIntakeNotOKReturnsFailure(t *testing.T) {
	clearCommandAuthEnv(t)
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.runnerPreflight = func(_ context.Context, cfg commentrunner.Config) commentrunner.PreflightReport {
		return commentrunner.PreflightReport{OK: true, Config: cfg}
	}
	app.runnerIntake = func(context.Context, commentrunner.Config, intake.Options) (intake.Result, error) {
		return intake.Result{OK: false, Diagnostics: []intake.Diagnostic{{Message: "notification failed"}}}, nil
	}
	app.runnerDispatch = func(context.Context, commentrunner.Config) (jobs.Result, error) {
		t.Fatal("dry-run must not dispatch jobs")
		return jobs.Result{}, nil
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
	if got.OK || got.Intake == nil || !strings.Contains(got.Error, "intake reported failure") {
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
	var callOrder []string
	app.runnerReconcile = func(_ context.Context, cfg commentrunner.Config) (jobs.ReconcileResult, error) {
		callOrder = append(callOrder, "reconcile")
		if cfg.WorkspaceRoot != "/tmp/workspaces" {
			t.Fatalf("config not passed to reconcile: %+v", cfg)
		}
		return jobs.ReconcileResult{Reconciled: 1, Running: 1}, nil
	}
	intakeCalled := false
	app.runnerIntake = func(_ context.Context, cfg commentrunner.Config, opts intake.Options) (intake.Result, error) {
		callOrder = append(callOrder, "intake")
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
		callOrder = append(callOrder, "dispatch")
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
	if strings.Join(callOrder, ",") != "reconcile,intake,dispatch" {
		t.Fatalf("unexpected call order: %v", callOrder)
	}
	var got runnerDryRunResult
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Mode != "run" || got.Reconcile == nil || got.Dispatch == nil || got.Dispatch.Status != state.StatusCompleted {
		t.Fatalf("unexpected run output: %+v", got)
	}
}

func TestRunnerPollStopsBeforeDispatchWhenIntakeNotOK(t *testing.T) {
	clearCommandAuthEnv(t)
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.runnerPreflight = func(_ context.Context, cfg commentrunner.Config) commentrunner.PreflightReport {
		return commentrunner.PreflightReport{OK: true, Config: cfg}
	}
	app.runnerReconcile = func(context.Context, commentrunner.Config) (jobs.ReconcileResult, error) {
		return jobs.ReconcileResult{}, nil
	}
	app.runnerIntake = func(context.Context, commentrunner.Config, intake.Options) (intake.Result, error) {
		return intake.Result{OK: false, Diagnostics: []intake.Diagnostic{{Message: "rate limited"}}}, nil
	}
	app.runnerDispatch = func(context.Context, commentrunner.Config) (jobs.Result, error) {
		t.Fatal("dispatch must not run after intake reported failure")
		return jobs.Result{}, nil
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
	if code != 1 {
		t.Fatalf("exit code = %d, want 1, stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	var got runnerDryRunResult
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.OK || got.Dispatch != nil || got.Intake == nil || !strings.Contains(got.Error, "intake reported failure") {
		t.Fatalf("unexpected run output: %+v", got)
	}
}

func TestRunnerPollWithoutOnceBacksOffAfterIntakeNotOK(t *testing.T) {
	clearCommandAuthEnv(t)
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.runnerPreflight = func(_ context.Context, cfg commentrunner.Config) commentrunner.PreflightReport {
		return commentrunner.PreflightReport{OK: true, Config: cfg}
	}
	var callOrder []string
	reconcileCalls := 0
	app.runnerReconcile = func(context.Context, commentrunner.Config) (jobs.ReconcileResult, error) {
		callOrder = append(callOrder, "reconcile")
		reconcileCalls++
		return jobs.ReconcileResult{}, nil
	}
	retryAfter := 25 * time.Millisecond
	var firstFailureAt time.Time
	intakeCalls := 0
	app.runnerIntake = func(context.Context, commentrunner.Config, intake.Options) (intake.Result, error) {
		callOrder = append(callOrder, "intake")
		intakeCalls++
		switch intakeCalls {
		case 1:
			firstFailureAt = time.Now()
			return intake.Result{
				OK:          false,
				Diagnostics: []intake.Diagnostic{{Source: "notification", Message: "rate limited"}},
				Next:        intake.NextStep{PollAfter: retryAfter, PollAt: firstFailureAt.Add(retryAfter)},
			}, nil
		case 2:
			if waited := time.Since(firstFailureAt); waited < retryAfter {
				t.Fatalf("second intake ran before retry backoff elapsed: waited=%s want>=%s", waited, retryAfter)
			}
			return intake.Result{OK: true, Next: intake.NextStep{PollAfter: 0}}, nil
		default:
			t.Fatalf("unexpected intake call %d", intakeCalls)
			return intake.Result{}, nil
		}
	}
	dispatchCalls := 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	app.runnerDispatch = func(context.Context, commentrunner.Config) (jobs.Result, error) {
		callOrder = append(callOrder, "dispatch")
		dispatchCalls++
		cancel()
		return jobs.Result{Reason: "no ready job"}, nil
	}
	code := app.runRunner(ctx, []string{
		"poll",
		"--repo", "o/r",
		"--runner", "issue-spec-bot",
		"--state", "/tmp/state.json",
		"--workspace-root", "/tmp/workspaces",
		"--json",
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0, stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if reconcileCalls != 2 || intakeCalls != 2 || dispatchCalls != 1 {
		t.Fatalf("loop calls reconcile=%d intake=%d dispatch=%d", reconcileCalls, intakeCalls, dispatchCalls)
	}
	if got := strings.Join(callOrder, ","); got != "reconcile,intake,reconcile,intake,dispatch" {
		t.Fatalf("unexpected call order: %s", got)
	}
	if !strings.Contains(out.String(), "rate limited") || !strings.Contains(out.String(), "intake reported failure") {
		t.Fatalf("failed intake diagnostics missing from output:\n%s", out.String())
	}
}

func TestRunnerPollWithoutOnceLoopsUntilContextCancellation(t *testing.T) {
	clearCommandAuthEnv(t)
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.runnerPreflight = func(_ context.Context, cfg commentrunner.Config) commentrunner.PreflightReport {
		return commentrunner.PreflightReport{OK: true, Config: cfg}
	}
	reconcileCalls := 0
	app.runnerReconcile = func(context.Context, commentrunner.Config) (jobs.ReconcileResult, error) {
		reconcileCalls++
		return jobs.ReconcileResult{}, nil
	}
	intakeCalls := 0
	app.runnerIntake = func(context.Context, commentrunner.Config, intake.Options) (intake.Result, error) {
		intakeCalls++
		return intake.Result{OK: true, Next: intake.NextStep{PollAfter: 0}}, nil
	}
	dispatchCalls := 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	app.runnerDispatch = func(context.Context, commentrunner.Config) (jobs.Result, error) {
		dispatchCalls++
		if dispatchCalls == 2 {
			cancel()
		}
		return jobs.Result{Reason: "no ready job"}, nil
	}
	code := app.runRunner(ctx, []string{
		"poll",
		"--repo", "o/r",
		"--runner", "issue-spec-bot",
		"--state", "/tmp/state.json",
		"--workspace-root", "/tmp/workspaces",
		"--json",
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0, stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if reconcileCalls != 2 || intakeCalls != 2 || dispatchCalls != 2 {
		t.Fatalf("loop calls reconcile=%d intake=%d dispatch=%d", reconcileCalls, intakeCalls, dispatchCalls)
	}
}

func TestRunnerBackendFlagOverridesEnvForEveryPhase(t *testing.T) {
	clearCommandAuthEnv(t)
	t.Setenv(auth.GitHubBackendEnv, "gh")
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.selectGitHubBackend = func(context.Context, string) (auth.GitHubBackendSelection, error) {
		t.Fatal("runner must not use env-only backend selector")
		return auth.GitHubBackendSelection{}, nil
	}
	var selectedModes []auth.GitHubBackendMode
	app.selectRunnerBackend = func(_ context.Context, host string, mode auth.GitHubBackendMode) (auth.GitHubBackendSelection, error) {
		selectedModes = append(selectedModes, mode)
		return auth.GitHubBackendSelection{
			Mode:            mode,
			Name:            auth.GitHubBackendNameREST,
			Kind:            auth.GitHubBackendKindREST,
			Host:            host,
			SelectionSource: "test",
			Token:           auth.Token{Value: "token", Host: host},
		}, nil
	}
	app.newGitHubBackend = func(context.Context, auth.GitHubBackendSelection) (github.Backend, error) {
		return &runnerPhaseBackend{fakeGitHubBackend: fakeGitHubBackend{info: github.BackendInfo{Name: "rest", Kind: "rest", Host: "github.com"}}}, nil
	}
	code := app.runRunner(context.Background(), []string{
		"poll",
		"--repo", "o/r",
		"--runner", "issue-spec-bot",
		"--backend", "rest",
		"--state", t.TempDir() + "/state.json",
		"--workspace-root", t.TempDir(),
		"--once",
		"--unsafe-no-sandbox",
		"--acpx-path", os.Args[0],
		"--json",
	})
	if code != 0 {
		t.Fatalf("exit code = %d, stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if len(selectedModes) < 4 {
		t.Fatalf("selected modes = %v, want preflight/intake/reconcile/dispatch", selectedModes)
	}
	for _, mode := range selectedModes {
		if mode != auth.GitHubBackendModeREST {
			t.Fatalf("selected mode = %s, want flag rest; all modes=%v", mode, selectedModes)
		}
	}
}

type runnerPhaseBackend struct {
	fakeGitHubBackend
}

func (b *runnerPhaseBackend) PollNotifications(context.Context, github.NotificationListOptions) (github.NotificationListResult, error) {
	return github.NotificationListResult{Metadata: github.ResponseMetadata{StatusCode: http.StatusNotModified, NotModified: true}}, nil
}

func (b *runnerPhaseBackend) GetRepositorySubscription(context.Context, string) (github.RepositorySubscriptionResult, error) {
	return github.RepositorySubscriptionResult{Subscription: github.RepositorySubscription{Subscribed: true, Reason: "subscribed"}}, nil
}

func (b *runnerPhaseBackend) GetIssueContext(context.Context, string, int, github.ConditionalRequest) (github.IssueContextResult, error) {
	return github.IssueContextResult{}, nil
}

func (b *runnerPhaseBackend) ListIssueCommentsPage(context.Context, string, int, github.CommentListOptions) (github.IssueCommentsResult, error) {
	return github.IssueCommentsResult{Metadata: github.ResponseMetadata{StatusCode: http.StatusOK}}, nil
}

func (b *runnerPhaseBackend) ListRepositoryIssueCommentsPage(context.Context, string, github.CommentListOptions) (github.IssueCommentsResult, error) {
	return github.IssueCommentsResult{Metadata: github.ResponseMetadata{StatusCode: http.StatusOK}}, nil
}

func (b *runnerPhaseBackend) ListCommentReactionsPage(context.Context, string, int64, github.RunnerPageOptions) (github.CommentReactionsResult, error) {
	return github.CommentReactionsResult{Metadata: github.ResponseMetadata{StatusCode: http.StatusOK}}, nil
}

func (b *runnerPhaseBackend) GetCollaboratorPermission(context.Context, string, string) (github.CollaboratorPermissionResult, error) {
	return github.CollaboratorPermissionResult{Permission: github.CollaboratorPermission{Permission: "write"}, CanWrite: true}, nil
}

func (b *runnerPhaseBackend) CreateRunnerComment(context.Context, string, int, string) (github.RunnerCommentResult, error) {
	return github.RunnerCommentResult{}, nil
}

func (b *runnerPhaseBackend) UpdateRunnerComment(context.Context, string, int64, string) (github.RunnerCommentResult, error) {
	return github.RunnerCommentResult{}, nil
}

func (b *runnerPhaseBackend) AddCommentReaction(context.Context, string, int64, string) (github.RunnerReactionResult, error) {
	return github.RunnerReactionResult{}, nil
}
