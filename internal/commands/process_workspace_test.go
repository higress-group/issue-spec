package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/gates"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/processworkspace"
)

func TestWorkspaceIntegrateSuccessAndIdempotentRetry(t *testing.T) {
	repo, root, backend, base := completedWorkspaceForIntegration(t, "integrate-owner", "internal/commands/integrated.txt")
	args := workspaceIntegrateArgs(repo, root, "integrate-owner", base)
	app, out, errOut := transitionAppWithError(backend)
	if code := app.runWorkspaceIntegrate(t.Context(), args); code != 0 {
		t.Fatalf("integrate code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	result := decodeWorkspaceResult(t, out)
	if result.State != processworkspace.StateIntegrated || result.IntegrationSHA == "" || result.IntegrationSHA != workspaceGitOutput(t, repo, "rev-parse", "HEAD") {
		t.Fatalf("integration result=%+v", result)
	}
	remote := model.ParseProcessWorkspace("PROCESS-004", "", backend.body)
	if remote.Workspace == nil || remote.Workspace.State != processworkspace.StateIntegrated || remote.Workspace.IntegrationSHA != result.IntegrationSHA {
		t.Fatalf("remote workspace=%+v", remote)
	}
	writes := backend.writes
	app, out, errOut = transitionAppWithError(backend)
	if code := app.runWorkspaceIntegrate(t.Context(), args); code != 0 {
		t.Fatalf("retry code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	retry := decodeWorkspaceResult(t, out)
	if retry.Action != "integrated-already" || retry.IntegrationSHA != result.IntegrationSHA || backend.writes != writes {
		t.Fatalf("retry=%+v writes=%d->%d", retry, writes, backend.writes)
	}
}

func TestWorkspaceIntegrateConflictLeavesRemoteWorkerComplete(t *testing.T) {
	repo, root, backend, _ := completedWorkspaceForIntegration(t, "conflict-owner", "internal/commands/conflict.txt")
	if err := os.MkdirAll(filepath.Join(repo, "internal", "commands"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "internal", "commands", "conflict.txt"), []byte("coordinator\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspaceGit(t, repo, "add", "internal/commands/conflict.txt")
	workspaceGit(t, repo, "commit", "-s", "-m", "coordinator conflict")
	expected := workspaceGitOutput(t, repo, "rev-parse", "HEAD")
	writes := backend.writes
	app, out, _ := transitionAppWithError(backend)
	if code := app.runWorkspaceIntegrate(t.Context(), workspaceIntegrateArgs(repo, root, "conflict-owner", expected)); code != 1 {
		t.Fatalf("conflict code=%d out=%s", code, out.String())
	}
	result := decodeWorkspaceResult(t, out)
	if result.Code != "integration_failed" || result.State != processworkspace.StateConflicted || backend.writes != writes || workspaceGitOutput(t, repo, "rev-parse", "HEAD") != expected {
		t.Fatalf("conflict result=%+v writes=%d->%d", result, writes, backend.writes)
	}
	remote := model.ParseProcessWorkspace("PROCESS-004", "", backend.body)
	if remote.Workspace == nil || remote.Workspace.State != processworkspace.StateWorkerComplete {
		t.Fatalf("conflict changed remote workspace=%+v", remote)
	}
}

func TestWorkspaceIntegrateRemoteFailureIsReconciledByRetry(t *testing.T) {
	repo, root, backend, base := completedWorkspaceForIntegration(t, "partial-owner", "internal/commands/partial.txt")
	backend.updateErr = errWorkspaceRemoteUnavailable
	args := workspaceIntegrateArgs(repo, root, "partial-owner", base)
	app, out, _ := transitionAppWithError(backend)
	if code := app.runWorkspaceIntegrate(t.Context(), args); code != 1 {
		t.Fatalf("partial code=%d out=%s", code, out.String())
	}
	partial := decodeWorkspaceResult(t, out)
	if partial.Code != "remote_workspace_update_failed" || partial.State != processworkspace.StateIntegrated || !partial.ReconcileRequired {
		t.Fatalf("partial=%+v", partial)
	}
	backend.updateErr = nil
	app, out, errOut := transitionAppWithError(backend)
	if code := app.runWorkspaceIntegrate(t.Context(), args); code != 0 {
		t.Fatalf("retry code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	retried := decodeWorkspaceResult(t, out)
	if retried.Action != "integrated-already" || retried.IntegrationSHA != partial.IntegrationSHA {
		t.Fatalf("retried=%+v", retried)
	}
}

func TestWorkspaceIntegrateCASFailurePrecedesLocalMutation(t *testing.T) {
	repo, root, backend, base := completedWorkspaceForIntegration(t, "cas-owner", "internal/commands/cas.txt")
	args := append(workspaceIntegrateArgs(repo, root, "cas-owner", base), "--expected-version", "999")
	app, out, _ := transitionAppWithError(backend)
	if code := app.runWorkspaceIntegrate(t.Context(), args); code != 1 {
		t.Fatalf("CAS code=%d out=%s", code, out.String())
	}
	result := decodeWorkspaceResult(t, out)
	if result.Code != "remote_precondition_failed" || workspaceGitOutput(t, repo, "rev-parse", "HEAD") != base {
		t.Fatalf("CAS result=%+v", result)
	}
	manager := openWorkspaceManager(t, repo, root)
	lease, found, err := manager.Store.Get(t.Context(), "ws-process-004")
	if err != nil || !found || lease.Portable.State != processworkspace.StateWorkerComplete {
		t.Fatalf("CAS mutated lease=%+v found=%v err=%v", lease, found, err)
	}
}

func TestWorkspaceIntegrateRejectsWrongWorkspaceOverrideBeforeMutation(t *testing.T) {
	repo, root, backend, base := completedWorkspaceForIntegration(t, "override-owner", "internal/commands/override.txt")
	args := append(workspaceIntegrateArgs(repo, root, "override-owner", base), "--workspace-id", "ws-other")
	app, out, _ := transitionAppWithError(backend)
	if code := app.runWorkspaceIntegrate(t.Context(), args); code != 1 {
		t.Fatalf("override code=%d out=%s", code, out.String())
	}
	result := decodeWorkspaceResult(t, out)
	if result.Code != "reservation_identity_mismatch" || workspaceGitOutput(t, repo, "rev-parse", "HEAD") != base {
		t.Fatalf("override result=%+v", result)
	}
}

func TestWorkspaceIntegrateRejectsRemoteLocalIdentityMismatchBeforeMutation(t *testing.T) {
	repo, root, backend, base := completedWorkspaceForIntegration(t, "identity-owner", "internal/commands/identity.txt")
	parsed := model.ParseProcessWorkspace("PROCESS-004", "", backend.body)
	if parsed.Workspace == nil {
		t.Fatal("remote Workspace missing")
	}
	original, err := model.RenderProcessWorkspaceSection(*parsed.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	changed := *parsed.Workspace
	changed.RuntimeNamespace = "different-namespace"
	replacement, err := model.RenderProcessWorkspaceSection(changed)
	if err != nil {
		t.Fatal(err)
	}
	backend.body = strings.Replace(backend.body, original, replacement, 1)
	app, out, _ := transitionAppWithError(backend)
	if code := app.runWorkspaceIntegrate(t.Context(), workspaceIntegrateArgs(repo, root, "identity-owner", base)); code != 1 {
		t.Fatalf("identity code=%d out=%s", code, out.String())
	}
	result := decodeWorkspaceResult(t, out)
	if result.Code != "reservation_identity_mismatch" || workspaceGitOutput(t, repo, "rev-parse", "HEAD") != base {
		t.Fatalf("identity result=%+v", result)
	}
	manager := openWorkspaceManager(t, repo, root)
	lease, found, err := manager.Store.Get(t.Context(), "ws-process-004")
	if err != nil || !found || lease.Portable.State != processworkspace.StateWorkerComplete {
		t.Fatalf("identity mismatch mutated lease=%+v found=%v err=%v", lease, found, err)
	}
}

func TestWorkspaceIntegrateRejectsImmutableAndLifecycleDriftBeforeMutation(t *testing.T) {
	tests := map[string]func(*processworkspace.PortableLease){
		"created at": func(workspace *processworkspace.PortableLease) {
			workspace.CreatedAt = workspace.CreatedAt.Add(time.Minute)
			workspace.UpdatedAt = workspace.CreatedAt
		},
		"retention": func(workspace *processworkspace.PortableLease) {
			workspace.RetentionExpiresAt = workspace.CreatedAt.Add(time.Hour)
		},
		"cleanup pending": func(workspace *processworkspace.PortableLease) {
			workspace.State = processworkspace.StateCleanupPending
		},
		"cleaned": func(workspace *processworkspace.PortableLease) {
			workspace.State = processworkspace.StateCleaned
		},
		"conflicted": func(workspace *processworkspace.PortableLease) {
			workspace.State = processworkspace.StateConflicted
		},
		"integrated sha mismatch": func(workspace *processworkspace.PortableLease) {
			workspace.State = processworkspace.StateIntegrated
			workspace.IntegrationSHA = strings.Repeat("e", 40)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			repo, root, backend, base := completedWorkspaceForIntegration(t, "drift-owner", "internal/commands/drift.txt")
			replaceRemoteWorkspace(t, backend, mutate)
			app, out, _ := transitionAppWithError(backend)
			if code := app.runWorkspaceIntegrate(t.Context(), workspaceIntegrateArgs(repo, root, "drift-owner", base)); code != 1 {
				t.Fatalf("drift code=%d out=%s", code, out.String())
			}
			result := decodeWorkspaceResult(t, out)
			if result.Code != "reservation_identity_mismatch" || workspaceGitOutput(t, repo, "rev-parse", "HEAD") != base {
				t.Fatalf("drift result=%+v", result)
			}
			manager := openWorkspaceManager(t, repo, root)
			lease, found, err := manager.Store.Get(t.Context(), "ws-process-004")
			if err != nil || !found || lease.Portable.State != processworkspace.StateWorkerComplete {
				t.Fatalf("drift mutated lease=%+v found=%v err=%v", lease, found, err)
			}
		})
	}
}

func TestWorkspaceIntegrateNonAtomicRequiresDigestAndConfirmsWrite(t *testing.T) {
	repo, root, strict, base := completedWorkspaceForIntegration(t, "nonatomic-owner", "internal/commands/nonatomic.txt")
	current := strict.body
	writes := 0
	plain := transitionFake(current)
	plain.listIssueComments = func(context.Context, string, int) ([]github.Comment, error) {
		return []github.Comment{{ID: 77, Body: current}}, nil
	}
	plain.updateComment = func(_ context.Context, _ string, _ int64, updated string) (github.Comment, error) {
		writes++
		current = updated
		return github.Comment{ID: 77, Body: updated}, nil
	}
	args := workspaceIntegrateArgs(repo, root, "nonatomic-owner", base)
	app, out, _ := transitionAppWithError(plain)
	if code := app.runWorkspaceIntegrate(t.Context(), args); code != 1 {
		t.Fatalf("non-atomic without digest code=%d out=%s", code, out.String())
	}
	if workspaceGitOutput(t, repo, "rev-parse", "HEAD") != base {
		t.Fatal("non-atomic precondition failure mutated integration HEAD")
	}
	args = append(args, "--allow-nonatomic", "--expected-digest", bodyDigest(current))
	app, out, errOut := transitionAppWithError(plain)
	if code := app.runWorkspaceIntegrate(t.Context(), args); code != 0 {
		t.Fatalf("non-atomic integrate code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	result := decodeWorkspaceResult(t, out)
	if result.Remote.Atomic || writes != 1 || result.State != processworkspace.StateIntegrated {
		t.Fatalf("non-atomic result=%+v writes=%d", result, writes)
	}
}

func TestFinalVerifyWiresExactSnapshotWorkspaceEvidence(t *testing.T) {
	spec := typedArtifact(t, 1, "SPEC", "SPEC-001", "confirmed", "## Requirement: X\n\nX MUST work.\n\n### Scenario: ok\n\n- **WHEN** x\n- **THEN** y")
	spec.URL = "https://github.com/o/r/issues/1#issuecomment-1"
	task := typedArtifact(t, 2, "TASK", "TASK-001", "done", canonicalTaskContent)
	task.URL = "https://github.com/o/r/issues/2#issuecomment-2"
	process := typedArtifact(t, 3, "PROCESS", "PROCESS-001", "done", "## Process: verify\n\n### Parent TASK\n\n- TASK-001\n\n### Execution Class\n\n- verification\n\n### Handoff\n\nN/A")
	process.URL = "https://github.com/o/r/issues/3#issuecomment-3"
	verify := typedArtifact(t, 3, "VERIFY", "VERIFY-001", "done", "## Verification Summary: final\n\nTests passed for PROCESS-001.\n\n### Covered SPECs\n\n- SPEC-001")
	linkArtifacts(t, &spec, &task)
	linkArtifacts(t, &task, &process)
	body, _, err := model.AddPRLink(process.Comment.Body, "https://github.com/o/r/pull/7")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0).UTC()
	workspace := processworkspace.PortableLease{SchemaVersion: processworkspace.LeaseSchemaVersion, WorkspaceID: "ws-process-001", Repository: "o/r", ProcessID: "PROCESS-001",
		ExecutionClass: processworkspace.ExecutionVerification, Mode: processworkspace.ModeSnapshot, BaseSHA: strings.Repeat("a", 40), DetachedRevision: strings.Repeat("a", 40),
		RuntimeNamespace: "ws-process-001", State: processworkspace.StatePrepared, CreatedAt: now, UpdatedAt: now}
	transition, err := model.ApplyTypedTransition(body, model.TransitionRequest{ExpectedType: "PROCESS", ExpectedID: "PROCESS-001", Workspace: &workspace})
	if err != nil {
		t.Fatal(err)
	}
	process.Comment = model.ParseTypedComment(transition.Body)
	artifacts := []model.Artifact{spec, task, process, verify}
	stale, err := buildFinalVerifyReport(artifacts, "https://github.com/o/r/issues/1", finalVerifyOptions{
		PR: 7, PRURL: "https://github.com/o/r/pull/7", ExpectedRevision: strings.Repeat("b", 40), RationaleRequired: true,
	})
	if err != nil || stale.OK || !containsErrorText(stale.Errors, "not bound to the expected") {
		t.Fatalf("stale report OK=%v errors=%v err=%v", stale.OK, stale.Errors, err)
	}
	fresh, err := buildFinalVerifyReport(artifacts, "https://github.com/o/r/issues/1", finalVerifyOptions{
		PR: 7, PRURL: "https://github.com/o/r/pull/7", ExpectedRevision: strings.Repeat("a", 40), RationaleRequired: true,
		CarrierRevisions: map[string]gates.CarrierRevisionFact{"PROCESS-001": {Known: true, Revision: strings.Repeat("a", 40), Trusted: true, Source: "verify-comment"}},
	})
	if err != nil || !fresh.OK {
		t.Fatalf("fresh report OK=%v errors=%v err=%v", fresh.OK, fresh.Errors, err)
	}
}

var errWorkspaceRemoteUnavailable = &workspaceTestError{"remote unavailable"}

type workspaceTestError struct{ message string }

func (e *workspaceTestError) Error() string { return e.message }

func completedWorkspaceForIntegration(t *testing.T, owner, resultPath string) (string, string, *workspaceCASBackend, string) {
	t.Helper()
	repo, base := workspaceGitRepository(t)
	backend := newWorkspaceCASBackend(workspaceProcessBody(t, model.ProcessExecutionChangeBearing))
	root := filepath.Join(t.TempDir(), "managed")
	prepare := append(workspaceBaseArgs(repo, root, owner), "--base", base, "--json")
	app, out, errOut := transitionAppWithError(backend)
	if code := app.runWorkflowWorkspace(t.Context(), append([]string{"prepare"}, prepare...)); code != 0 {
		t.Fatalf("prepare code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	prepared := decodeWorkspaceResult(t, out)
	path := filepath.Join(prepared.WorktreePath, filepath.FromSlash(resultPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("worker\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspaceGit(t, prepared.WorktreePath, "add", "--", resultPath)
	workspaceGit(t, prepared.WorktreePath, "commit", "-s", "-m", "worker integration result")
	resultCommit := workspaceGitOutput(t, prepared.WorktreePath, "rev-parse", "HEAD")
	complete := append([]string{"complete"}, workspaceBaseArgs(repo, root, owner)...)
	complete = append(complete, "--result-commit", resultCommit, "--json")
	app, out, errOut = transitionAppWithError(backend)
	if code := app.runWorkflowWorkspace(t.Context(), complete); code != 0 {
		t.Fatalf("complete code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	return repo, root, backend, base
}

func workspaceIntegrateArgs(repo, root, owner, expected string) []string {
	args := workspaceBaseArgs(repo, root, owner)
	return append(args, "--expected-head", strings.TrimSpace(expected), "--json")
}

func replaceRemoteWorkspace(t *testing.T, backend *workspaceCASBackend, mutate func(*processworkspace.PortableLease)) {
	t.Helper()
	parsed := model.ParseProcessWorkspace("PROCESS-004", "", backend.body)
	if parsed.Workspace == nil {
		t.Fatal("remote Workspace missing")
	}
	original, err := model.RenderProcessWorkspaceSection(*parsed.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	changed := processworkspace.PortableLease(*parsed.Workspace)
	mutate(&changed)
	replacement, err := model.RenderProcessWorkspaceSection(changed)
	if err != nil {
		t.Fatal(err)
	}
	backend.body = strings.Replace(backend.body, original, replacement, 1)
}

func containsErrorText(values []string, text string) bool {
	for _, value := range values {
		if strings.Contains(value, text) {
			return true
		}
	}
	return false
}
