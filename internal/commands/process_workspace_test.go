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

func TestWorkspaceManagedLifecycleRejectsProcessChangedToIndependent(t *testing.T) {
	t.Run("reconcile", func(t *testing.T) {
		repo, root, backend, _ := completedWorkspaceForIntegration(t, "independent-reconcile", "internal/commands/independent-reconcile.txt")
		backend.body = strings.Replace(backend.body, "### Workspace Management\n\n- managed", "### Workspace Management\n\n- independent", 1)
		writes := backend.writes
		app, out, _ := transitionAppWithError(backend)
		args := append([]string{"reconcile"}, workspaceBaseArgs(repo, root, "independent-reconcile")...)
		args = append(args, "--json")
		if code := app.runWorkflowWorkspace(t.Context(), args); code != 1 {
			t.Fatalf("reconcile code=%d out=%s", code, out.String())
		}
		result := decodeWorkspaceResult(t, out)
		if result.Code != "workspace_management_independent" || backend.writes != writes {
			t.Fatalf("reconcile result=%+v writes=%d->%d", result, writes, backend.writes)
		}
	})

	t.Run("integrate", func(t *testing.T) {
		repo, root, backend, base := completedWorkspaceForIntegration(t, "independent-integrate", "internal/commands/independent-integrate.txt")
		backend.body = strings.Replace(backend.body, "### Workspace Management\n\n- managed", "### Workspace Management\n\n- independent", 1)
		writes := backend.writes
		app, out, _ := transitionAppWithError(backend)
		if code := app.runWorkspaceIntegrate(t.Context(), workspaceIntegrateArgs(repo, root, "independent-integrate", base)); code != 1 {
			t.Fatalf("integrate code=%d out=%s", code, out.String())
		}
		result := decodeWorkspaceResult(t, out)
		if result.Code != "workspace_management_independent" || backend.writes != writes || workspaceGitOutput(t, repo, "rev-parse", "HEAD") != base {
			t.Fatalf("integrate result=%+v writes=%d->%d", result, writes, backend.writes)
		}
	})
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
	task.URL = "https://github.com/o/r/issues/3#issuecomment-2"
	process := typedArtifact(t, 3, "PROCESS", "PROCESS-001", "done", "## Process: verify\n\n### Parent TASK\n\n- TASK-001\n\n### Execution Class\n\n- verification\n\n### Covers\n\n- SPEC-001\n\n### Handoff\n\nN/A")
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
	canonicalizeVerificationFixture(t, &verify, process, spec)
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

func TestFinalVerifyBindsZeroFindingReviewToExactSubjectRevision(t *testing.T) {
	const (
		prURL = "https://github.com/o/r/pull/7"
		head  = "0123456789abcdef0123456789abcdef01234567"
		stale = "89abcdef0123456789abcdef0123456789abcdef"
	)
	build := func(subjectRevision string) finalVerifyReport {
		t.Helper()
		spec := typedArtifact(t, 1, "SPEC", "SPEC-001", "confirmed", "## Requirement: X\n\nX MUST work.\n\n### Scenario: ok\n\n- **WHEN** x\n- **THEN** y")
		spec.URL = "https://github.com/o/r/issues/1#issuecomment-1"
		task := typedArtifact(t, 2, "TASK", "TASK-001", "done", canonicalTaskContent)
		task.URL = "https://github.com/o/r/issues/3#issuecomment-2"
		process := typedArtifact(t, 3, "PROCESS", "PROCESS-001", "done", canonicalReviewProcess)
		process.URL = "https://github.com/o/r/issues/3#issuecomment-3"
		reviewBody, err := model.EnsureTypedBody("REVIEW", "REVIEW-001", "## Review Sync Summary\n\nReviewed PROCESS-001 and SPEC-001 with zero findings.", model.BodyOptions{
			Agent: "Reviewer Agent", Status: "done", SubjectRevision: subjectRevision, Links: map[string][]string{"PR": {prURL}},
		})
		if err != nil {
			t.Fatal(err)
		}
		review := model.Artifact{Issue: 3, URL: "https://github.com/o/r/issues/3#issuecomment-4", Comment: model.ParseTypedComment(reviewBody)}
		verify := typedArtifact(t, 3, "VERIFY", "VERIFY-001", "done", canonicalVerifyContent)
		linkArtifacts(t, &spec, &task)
		linkArtifacts(t, &task, &process)
		linkArtifacts(t, &process, &review)
		linkArtifacts(t, &spec, &review)
		processBody, _, err := model.AddPRLink(process.Comment.Body, prURL)
		if err != nil {
			t.Fatal(err)
		}
		now := time.Unix(100, 0).UTC()
		workspace := processworkspace.PortableLease{SchemaVersion: processworkspace.LeaseSchemaVersion, WorkspaceID: "ws-process-001", Repository: "o/r", ProcessID: "PROCESS-001",
			ExecutionClass: processworkspace.ExecutionReview, Mode: processworkspace.ModeSnapshot, BaseSHA: head, DetachedRevision: head,
			RuntimeNamespace: "ws-process-001", State: processworkspace.StateCleaned, CreatedAt: now, UpdatedAt: now}
		transition, err := model.ApplyTypedTransition(processBody, model.TransitionRequest{ExpectedType: "PROCESS", ExpectedID: "PROCESS-001", Workspace: &workspace})
		if err != nil {
			t.Fatal(err)
		}
		process.Comment = model.ParseTypedComment(transition.Body)
		canonicalizeReviewFixture(t, &review, []model.Artifact{process}, spec)

		report, err := buildFinalVerifyReport([]model.Artifact{spec, task, process, review, verify}, "https://github.com/o/r/issues/1", finalVerifyOptions{
			PR: 7, PRURL: prURL, ExpectedRevision: head, RationaleRequired: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		return report
	}
	t.Run("matching head", func(t *testing.T) {
		report := build(head)
		if !report.OK || finalReportHasGateCode(report, gates.CodeProcessWorkspaceRevisionUnknown) || finalReportHasGateCode(report, gates.CodeProcessWorkspaceRevisionStale) {
			t.Fatalf("exact zero-finding review did not pass final verify: errors=%v diagnostics=%+v evidence=%+v", report.Errors, report.Gate.Diagnostics, report.ProcessEvidence)
		}
	})
	t.Run("stale head", func(t *testing.T) {
		report := build(stale)
		if report.OK || !finalReportHasGateCode(report, gates.CodeProcessCarrierMissing) {
			t.Fatalf("stale zero-finding review passed final verify: errors=%v diagnostics=%+v evidence=%+v", report.Errors, report.Gate.Diagnostics, report.ProcessEvidence)
		}
	})
	t.Run("missing subject revision", func(t *testing.T) {
		report := build("")
		if report.OK || !finalReportHasGateCode(report, gates.CodeProcessCarrierMissing) {
			t.Fatalf("revisionless zero-finding review passed final verify: errors=%v diagnostics=%+v evidence=%+v", report.Errors, report.Gate.Diagnostics, report.ProcessEvidence)
		}
	})
}

func TestFinalVerifyRequiresExactCurrentReviewForEverySpec(t *testing.T) {
	const (
		prURL = "https://github.com/o/r/pull/7"
		head  = "0123456789abcdef0123456789abcdef01234567"
		stale = "89abcdef0123456789abcdef0123456789abcdef"
	)
	build := func(secondRevision string) finalVerifyReport {
		t.Helper()
		spec1 := typedArtifact(t, 1, "SPEC", "SPEC-001", "confirmed", "## Requirement: X\n\nX MUST work.\n\n### Scenario: ok\n\n- **WHEN** x\n- **THEN** y")
		spec1.URL = "https://github.com/o/r/issues/1#issuecomment-1"
		spec2 := typedArtifact(t, 1, "SPEC", "SPEC-002", "confirmed", "## Requirement: Z\n\nZ MUST work.\n\n### Scenario: ok\n\n- **WHEN** z\n- **THEN** it works")
		spec2.URL = "https://github.com/o/r/issues/1#issuecomment-2"
		task := typedArtifact(t, 2, "TASK", "TASK-001", "done", strings.Replace(canonicalTaskContent, "- SPEC-001", "- SPEC-001\n- SPEC-002", 1))
		task.URL = "https://github.com/o/r/issues/3#issuecomment-3"
		process := typedArtifact(t, 3, "PROCESS", "PROCESS-001", "done", canonicalReviewProcess)
		process.URL = "https://github.com/o/r/issues/3#issuecomment-4"
		process.Comment = model.ParseTypedComment(strings.Replace(process.Comment.Body, "- SPEC-001", "- SPEC-001\n- SPEC-002", 1))
		verify := typedArtifact(t, 3, "VERIFY", "VERIFY-001", "done", strings.Replace(canonicalVerifyContent, "- SPEC-001", "- SPEC-001\n- SPEC-002", 1))

		makeReview := func(id, specID, revision, url string) model.Artifact {
			body, err := model.EnsureTypedBody("REVIEW", id, "## Review Sync Summary\n\nReviewed PROCESS-001 and "+specID+" with zero findings.", model.BodyOptions{
				Agent: "Independent Reviewer", Status: "done", SubjectRevision: revision, Links: map[string][]string{"PR": {prURL}},
			})
			if err != nil {
				t.Fatal(err)
			}
			return model.Artifact{Issue: 3, URL: url, Comment: model.ParseTypedComment(body)}
		}
		review1 := makeReview("REVIEW-001", "SPEC-001", head, "https://github.com/o/r/issues/3#issuecomment-5")
		review2 := makeReview("REVIEW-002", "SPEC-002", secondRevision, "https://github.com/o/r/issues/3#issuecomment-6")

		linkArtifacts(t, &spec1, &task)
		linkArtifacts(t, &spec2, &task)
		linkArtifacts(t, &task, &process)
		linkArtifacts(t, &process, &review1)
		linkArtifacts(t, &process, &review2)
		linkArtifacts(t, &spec1, &review1)
		linkArtifacts(t, &spec2, &review2)
		processBody, _, err := model.AddPRLink(process.Comment.Body, prURL)
		if err != nil {
			t.Fatal(err)
		}
		now := time.Unix(100, 0).UTC()
		workspace := processworkspace.PortableLease{SchemaVersion: processworkspace.LeaseSchemaVersion, WorkspaceID: "ws-process-001", Repository: "o/r", ProcessID: "PROCESS-001",
			ExecutionClass: processworkspace.ExecutionReview, Mode: processworkspace.ModeSnapshot, BaseSHA: head, DetachedRevision: head,
			RuntimeNamespace: "ws-process-001", State: processworkspace.StateCleaned, CreatedAt: now, UpdatedAt: now}
		transition, err := model.ApplyTypedTransition(processBody, model.TransitionRequest{ExpectedType: "PROCESS", ExpectedID: "PROCESS-001", Workspace: &workspace})
		if err != nil {
			t.Fatal(err)
		}
		process.Comment = model.ParseTypedComment(transition.Body)
		canonicalizeReviewFixture(t, &review1, []model.Artifact{process}, spec1)
		canonicalizeReviewFixture(t, &review2, []model.Artifact{process}, spec2)

		report, err := buildFinalVerifyReport([]model.Artifact{spec1, spec2, task, process, review1, review2, verify}, "https://github.com/o/r/issues/1", finalVerifyOptions{
			PR: 7, PRURL: prURL, ExpectedRevision: head, RationaleRequired: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		return report
	}

	t.Run("one current and one stale", func(t *testing.T) {
		report := build(stale)
		if report.OK || !finalReportHasGateCode(report, gates.CodeProcessCarrierMissing) ||
			!finalReportHasGateCode(report, gates.CodeProcessWorkspaceRevisionStale) {
			t.Fatalf("current review rescued stale SPEC at final/workspace gate: errors=%v diagnostics=%+v evidence=%+v", report.Errors, report.Gate.Diagnostics, report.ProcessEvidence)
		}
	})
	t.Run("every SPEC current", func(t *testing.T) {
		report := build(head)
		if !report.OK || finalReportHasGateCode(report, gates.CodeProcessCarrierMissing) ||
			finalReportHasGateCode(report, gates.CodeProcessWorkspaceRevisionStale) {
			t.Fatalf("one current review per SPEC should pass: errors=%v diagnostics=%+v evidence=%+v", report.Errors, report.Gate.Diagnostics, report.ProcessEvidence)
		}
	})
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
