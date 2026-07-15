package commands

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/gates"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/processworkspace"
)

const (
	canonicalTaskContent    = "## Task: work\n\n### Implementation Checklist\n\n- [x] 1. work\n\n### Execution Planning\n\n- Owned modules / write areas:\n  - internal/x\n- Coupling class: low\n- Recommended execution mode: coordinator-owned\n\n### Covers\n\n- SPEC-001"
	canonicalProcessContent = "## Process: impl\n\n### Owner\n\n- Worker\n\n### Parent TASK\n\n- TASK-001\n\n### Write Ownership\n\n- internal/x\n\n### Dependencies\n\n- N/A\n\n### Covers\n\n- TASK-001\n\n### Handoff\n\nN/A"
	canonicalVerifyContent  = "## Verification Summary: final\n\nTests, review, and traceability confirmed.\n\n### Evidence\n\n- go test ./...\n\n### Covered SPECs\n\n- SPEC-001"
)

func TestBuildFinalVerifyReportRequiresDoneTasksAndCoverage(t *testing.T) {
	spec := typedArtifact(t, 1, "SPEC", "SPEC-001", "confirmed", "## Requirement: X\n\nX MUST work.\n\n### Scenario: ok\n\n- **WHEN** x\n- **THEN** y")
	task := typedArtifact(t, 2, "TASK", "TASK-001", "ready", canonicalTaskContent)
	verify := typedArtifact(t, 3, "VERIFY", "VERIFY-001", "done", canonicalVerifyContent)
	report, err := buildFinalVerifyReport([]model.Artifact{spec, task, verify}, "https://github.com/o/r/issues/1", finalVerifyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if report.OK {
		t.Fatal("ready TASK should fail final verify")
	}
	if !report.SpecCoverage["SPEC-001"] {
		t.Fatalf("expected SPEC-001 coverage: %+v", report.SpecCoverage)
	}
}

func TestFinalVerifyUsesAuthoritativePullRequestAncestry(t *testing.T) {
	ancestor := strings.Repeat("a", 40)
	head := strings.Repeat("b", 40)
	unrelated := strings.Repeat("c", 40)

	ancestorArtifacts := finalVerifyChangeBearingArtifacts(t, ancestor)
	ancestorReport, err := buildFinalVerifyReport(ancestorArtifacts, "https://github.com/o/r/issues/1", finalVerifyOptions{
		PR: 7, PRURL: "https://github.com/o/r/pull/7", ExpectedRevision: head,
		PRCommits: []github.PullRequestCommit{{SHA: strings.Repeat("0", 40)}, {SHA: ancestor}, {SHA: head}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if finalReportHasGateCode(ancestorReport, gates.CodeProcessWorkspaceRevisionStale) {
		t.Fatalf("authoritative multi-commit PR ancestor was rejected: %+v", ancestorReport.Gate.Diagnostics)
	}

	headReport, err := buildFinalVerifyReport(finalVerifyChangeBearingArtifacts(t, head), "https://github.com/o/r/issues/1", finalVerifyOptions{
		PR: 7, PRURL: "https://github.com/o/r/pull/7", ExpectedRevision: head,
	})
	if err != nil {
		t.Fatal(err)
	}
	if finalReportHasGateCode(headReport, gates.CodeProcessWorkspaceRevisionStale) {
		t.Fatalf("exact PR head was rejected: %+v", headReport.Gate.Diagnostics)
	}

	for name, commits := range map[string][]github.PullRequestCommit{
		"unrelated":                       {{SHA: unrelated}},
		"integration present head absent": {{SHA: ancestor}},
		"collection failed":               nil,
	} {
		t.Run(name, func(t *testing.T) {
			report, buildErr := buildFinalVerifyReport(ancestorArtifacts, "https://github.com/o/r/issues/1", finalVerifyOptions{
				PR: 7, PRURL: "https://github.com/o/r/pull/7", ExpectedRevision: head, PRCommits: commits,
			})
			if buildErr != nil {
				t.Fatal(buildErr)
			}
			if !finalReportHasGateCode(report, gates.CodeProcessWorkspaceRevisionStale) {
				t.Fatalf("non-authoritative ancestry was accepted: %+v", report.Gate.Diagnostics)
			}
		})
	}
}

func TestRunVerifyRejectsUnstablePullRequestIdentity(t *testing.T) {
	t.Setenv(auth.ConfigDirEnv, t.TempDir())
	t.Setenv(auth.ProfileEnv, auth.DefaultProfileName)
	t.Setenv(auth.GitHubBackendAPIURLEnv, "")
	initialHead := strings.Repeat("a", 40)
	advancedHead := strings.Repeat("b", 40)
	valid := pullRequestAtHead(7, initialHead)
	advanced := pullRequestAtHead(7, advancedHead)
	missingNumber := valid
	missingNumber.Number = 0
	wrongRepo := valid
	wrongRepo.HTMLURL = "https://github.com/o/other/pull/7"
	for name, pulls := range map[string][]github.PullRequest{
		"head advanced":  {valid, advanced},
		"missing number": {missingNumber, valid},
		"wrong repo URL": {wrongRepo, wrongRepo},
	} {
		t.Run(name, func(t *testing.T) {
			backend := &sequencedPullRequestCommitBackend{
				fakeGitHubBackend: fakeGitHubBackend{
					info: github.BackendInfo{Name: "rest", Kind: "rest", Host: "github.com"},
					getIssue: func(_ context.Context, _ string, issue int) (github.Issue, error) {
						return github.Issue{Number: issue, HTMLURL: "https://github.com/o/r/issues/1"}, nil
					},
					listIssueComments: func(context.Context, string, int) ([]github.Comment, error) { return nil, nil },
				},
				pulls: pulls, commits: []github.PullRequestCommit{{SHA: initialHead}},
			}
			var out, errOut bytes.Buffer
			app := newApp(strings.NewReader(""), &out, &errOut)
			app.selectGitHubBackend = ghSelection
			app.newGitHubBackend = func(context.Context, auth.GitHubBackendSelection) (github.Backend, error) { return backend, nil }
			code := app.runVerify(t.Context(), []string{"--repo", "o/r", "--proposal", "1", "--design", "2", "--implement", "3", "--pr", "7", "--json"})
			if code != 1 || !strings.Contains(errOut.String(), "pull request changed while collecting gate facts") {
				t.Fatalf("verify code=%d out=%q err=%q", code, out.String(), errOut.String())
			}
		})
	}
}

func finalVerifyChangeBearingArtifacts(t *testing.T, integrationSHA string) []model.Artifact {
	t.Helper()
	spec := typedArtifact(t, 1, "SPEC", "SPEC-001", "confirmed", "## Requirement: X\n\nX MUST work.\n\n### Scenario: ok\n\n- **WHEN** x\n- **THEN** y")
	task := typedArtifact(t, 2, "TASK", "TASK-001", "done", canonicalTaskContent)
	process := typedArtifact(t, 3, "PROCESS", "PROCESS-001", "done", "## Process: impl\n\n### Parent TASK\n\n- TASK-001\n\n### Execution Class\n\n- change-bearing\n\n### Covers\n\n- SPEC-001\n\n### Handoff\n\ncomplete")
	verify := typedArtifact(t, 4, "VERIFY", "VERIFY-001", "done", canonicalVerifyContent+"\n\nTest evidence covers PROCESS-001.")
	linkArtifacts(t, &spec, &task)
	linkArtifacts(t, &task, &process)
	body, _, err := model.AddPRLink(process.Comment.Body, "https://github.com/o/r/pull/7")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0).UTC()
	workspace := processworkspace.PortableLease{SchemaVersion: processworkspace.LeaseSchemaVersion, WorkspaceID: "ws-process-001", Repository: "o/r",
		ProcessID: "PROCESS-001", ExecutionClass: processworkspace.ExecutionChangeBearing, Mode: processworkspace.ModeWritable,
		BaseSHA: strings.Repeat("0", 40), Branch: "codex/process-001", ResultCommit: strings.Repeat("1", 40), IntegrationSHA: integrationSHA,
		WriteOwnership: []string{"internal/x"}, RuntimeNamespace: "ws-process-001", State: processworkspace.StateIntegrated, CreatedAt: now, UpdatedAt: now}
	transition, err := model.ApplyTypedTransition(body, model.TransitionRequest{ExpectedType: "PROCESS", ExpectedID: "PROCESS-001", Workspace: &workspace})
	if err != nil {
		t.Fatal(err)
	}
	process.Comment = model.ParseTypedComment(transition.Body)
	return []model.Artifact{spec, task, process, verify}
}

func finalReportHasGateCode(report finalVerifyReport, code string) bool {
	for _, diagnostic := range report.Gate.Diagnostics {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}

func TestBuildFinalVerifyReportReportsSessionDiagnosticsWithoutErrors(t *testing.T) {
	spec := typedArtifact(t, 1, "SPEC", "SPEC-001", "confirmed", "## Requirement: X\n\nX MUST work.\n\n### Scenario: ok\n\n- **WHEN** x\n- **THEN** y")
	task := typedArtifact(t, 2, "TASK", "TASK-001", "done", canonicalTaskContent)
	process := typedArtifact(t, 3, "PROCESS", "PROCESS-001", "done", canonicalProcessContent)
	verify := typedArtifact(t, 3, "VERIFY", "VERIFY-001", "done", canonicalVerifyContent)
	linkArtifacts(t, &spec, &task)
	linkArtifacts(t, &task, &process)
	report, err := buildFinalVerifyReport([]model.Artifact{spec, task, process, verify}, "https://github.com/o/r/issues/1", finalVerifyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !report.OK {
		t.Fatalf("metadata diagnostics should not fail verify: %+v", report.Errors)
	}
	if len(report.Diagnostics) != 4 {
		t.Fatalf("diagnostics = %+v", report.Diagnostics)
	}
}

func TestBuildFinalVerifyReportChecksDurableSpec(t *testing.T) {
	spec := typedArtifact(t, 1, "SPEC", "SPEC-001", "confirmed", "## Requirement: X\n\nX MUST work.\n\n### Scenario: ok\n\n- **WHEN** x\n- **THEN** y")
	spec.URL = "https://github.com/o/r/issues/1#issuecomment-1"
	task := typedArtifact(t, 2, "TASK", "TASK-001", "done", canonicalTaskContent)
	task.URL = "https://github.com/o/r/issues/2#issuecomment-2"
	process := typedArtifact(t, 3, "PROCESS", "PROCESS-001", "done", canonicalProcessContent)
	process.URL = "https://github.com/o/r/issues/3#issuecomment-3"
	review := typedArtifact(t, 3, "REVIEW", "REVIEW-001", "done", "## Review\n\nnone")
	verify := typedArtifact(t, 3, "VERIFY", "VERIFY-001", "done", canonicalVerifyContent)
	linkArtifacts(t, &spec, &task)
	linkArtifacts(t, &task, &process)
	specPath := filepath.Join(t.TempDir(), "spec.md")
	if err := os.WriteFile(specPath, []byte(`# issue-spec-cli

## Purpose

Purpose.

Proposal Issues:
- https://github.com/o/r/issues/1

## Requirements

### Requirement: X

X MUST work.

Source SPEC comment: https://github.com/o/r/issues/1#issuecomment-1
`), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := buildFinalVerifyReport([]model.Artifact{spec, task, process, review, verify}, "https://github.com/o/r/issues/1", finalVerifyOptions{DurableSpecPath: specPath})
	if err != nil {
		t.Fatal(err)
	}
	if !report.OK {
		t.Fatalf("expected final verify OK: %+v", report.Errors)
	}
}

func TestBuildFinalVerifyReportChecksRationaleCoverageWhenPRProvided(t *testing.T) {
	spec := typedArtifact(t, 1, "SPEC", "SPEC-001", "confirmed", "## Requirement: X\n\nX MUST work.\n\n### Scenario: ok\n\n- **WHEN** x\n- **THEN** y")
	spec.URL = "https://github.com/o/r/issues/1#issuecomment-1"
	task := typedArtifact(t, 2, "TASK", "TASK-001", "done", canonicalTaskContent)
	task.URL = "https://github.com/o/r/issues/2#issuecomment-2"
	process := typedArtifact(t, 3, "PROCESS", "PROCESS-001", "done", canonicalProcessContent)
	process.URL = "https://github.com/o/r/issues/3#issuecomment-3"
	review := typedArtifact(t, 3, "REVIEW", "REVIEW-001", "done", "## Review\n\nnone")
	verify := typedArtifact(t, 3, "VERIFY", "VERIFY-001", "done", canonicalVerifyContent)
	linkArtifacts(t, &spec, &task)
	linkArtifacts(t, &task, &process)
	report, err := buildFinalVerifyReport([]model.Artifact{spec, task, process, review, verify}, "https://github.com/o/r/issues/1", finalVerifyOptions{
		PR:                7,
		PRURL:             "https://github.com/o/r/pull/7",
		RationaleRequired: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.OK {
		t.Fatal("missing rationale should fail when PR is supplied")
	}
	body, err := model.RenderRationaleBody("Worker Agent A", "PROCESS-001", "SPEC-001", spec.URL, "Explain why.", "internal/foo.go", 12)
	if err != nil {
		t.Fatal(err)
	}
	processWithPR := process
	processBody, changed, err := model.AddPRLink(processWithPR.Comment.Body, "https://github.com/o/r/pull/7")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected PR link to change process body")
	}
	processWithPR.Comment = model.ParseTypedComment(processBody)
	report, err = buildFinalVerifyReport([]model.Artifact{spec, task, process, review, verify}, "https://github.com/o/r/issues/1", finalVerifyOptions{
		PR:                7,
		PRURL:             "https://github.com/o/r/pull/7",
		RationaleRequired: true,
		RationaleComments: []github.PullRequestReviewComment{{Body: body, Path: "internal/foo.go", Line: 12}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.OK {
		t.Fatal("missing PROCESS PR link should fail even when rationale exists")
	}
	report, err = buildFinalVerifyReport([]model.Artifact{spec, task, processWithPR, review, verify}, "https://github.com/o/r/issues/1", finalVerifyOptions{
		PR:                7,
		PRURL:             "https://github.com/o/r/pull/7",
		RationaleRequired: true,
		RationaleComments: []github.PullRequestReviewComment{{Body: body, Path: "internal/foo.go", Line: 12}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !report.OK {
		t.Fatalf("expected rationale coverage OK: %+v", report.Errors)
	}
}

func TestBuildFinalVerifyReportUsesVerificationCarrierInsteadOfRationale(t *testing.T) {
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
	process.Comment = model.ParseTypedComment(body)
	report, err := buildFinalVerifyReport([]model.Artifact{spec, task, process, verify}, "https://github.com/o/r/issues/1", finalVerifyOptions{
		PR: 7, PRURL: "https://github.com/o/r/pull/7", RationaleRequired: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !report.OK || len(report.ProcessEvidence) != 1 || report.ProcessEvidence[0].ExecutionClass != model.ProcessExecutionVerification {
		t.Fatalf("verification carrier should pass without arbitrary rationale: errors=%v evidence=%+v", report.Errors, report.ProcessEvidence)
	}
}

func TestBuildFinalVerifyReportBlocksOpenP0P1Findings(t *testing.T) {
	spec := typedArtifact(t, 1, "SPEC", "SPEC-001", "confirmed", "## Requirement: X\n\nX MUST work.\n\n### Scenario: ok\n\n- **WHEN** x\n- **THEN** y")
	spec.URL = "https://github.com/o/r/issues/1#issuecomment-1"
	task := typedArtifact(t, 2, "TASK", "TASK-001", "done", canonicalTaskContent)
	task.URL = "https://github.com/o/r/issues/2#issuecomment-2"
	process := typedArtifact(t, 3, "PROCESS", "PROCESS-001", "done", canonicalProcessContent)
	process.URL = "https://github.com/o/r/issues/3#issuecomment-3"
	review := typedArtifact(t, 3, "REVIEW", "REVIEW-001", "done", "## Review\n\nnone")
	verify := typedArtifact(t, 3, "VERIFY", "VERIFY-001", "done", canonicalVerifyContent)
	linkArtifacts(t, &spec, &task)
	linkArtifacts(t, &task, &process)
	processBody, changed, err := model.AddPRLink(process.Comment.Body, "https://github.com/o/r/pull/7")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected PR link to change process body")
	}
	process.Comment = model.ParseTypedComment(processBody)
	rationale, err := model.RenderRationaleBody("Worker Agent A", "PROCESS-001", "SPEC-001", spec.URL, "Explain why.", "internal/foo.go", 12)
	if err != nil {
		t.Fatal(err)
	}
	finding, err := model.RenderFindingBody("Review", "FINDING-001", "P1", "PROCESS-001", "SPEC-001", spec.URL, "Fix this before merge.", "open", "internal/foo.go", 12)
	if err != nil {
		t.Fatal(err)
	}
	report, err := buildFinalVerifyReport([]model.Artifact{spec, task, process, review, verify}, "https://github.com/o/r/issues/1", finalVerifyOptions{
		PR:                7,
		PRURL:             "https://github.com/o/r/pull/7",
		RationaleRequired: true,
		RationaleComments: []github.PullRequestReviewComment{
			{ID: 1, Body: rationale, Path: "internal/foo.go", Line: 12},
			{ID: 2, Body: finding, Path: "internal/foo.go", Line: 12},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.OK {
		t.Fatal("open P1 finding should fail final verify")
	}
	if len(report.ReviewFindingBlockers) != 1 {
		t.Fatalf("expected one review finding blocker: %+v", report.ReviewFindingBlockers)
	}
	reply, err := model.RenderFindingReplyBody("Review", "FINDING-001", "PROCESS-001", "resolved", "Re-checked; fix satisfies the finding.")
	if err != nil {
		t.Fatal(err)
	}
	report, err = buildFinalVerifyReport([]model.Artifact{spec, task, process, review, verify}, "https://github.com/o/r/issues/1", finalVerifyOptions{
		PR:                7,
		PRURL:             "https://github.com/o/r/pull/7",
		RationaleRequired: true,
		RationaleComments: []github.PullRequestReviewComment{
			{ID: 1, Body: rationale, Path: "internal/foo.go", Line: 12},
			{ID: 2, Body: finding, Path: "internal/foo.go", Line: 12},
			{ID: 3, InReplyToID: 2, Body: reply},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !report.OK {
		t.Fatalf("resolved P1 finding should pass final verify: %+v", report.Errors)
	}
}

func TestBuildFinalVerifyReportBlocksFailedAndPendingChecks(t *testing.T) {
	spec := typedArtifact(t, 1, "SPEC", "SPEC-001", "confirmed", "## Requirement: X\n\nX MUST work.\n\n### Scenario: ok\n\n- **WHEN** x\n- **THEN** y")
	spec.URL = "https://github.com/o/r/issues/1#issuecomment-1"
	task := typedArtifact(t, 2, "TASK", "TASK-001", "done", canonicalTaskContent)
	task.URL = "https://github.com/o/r/issues/2#issuecomment-2"
	process := typedArtifact(t, 3, "PROCESS", "PROCESS-001", "done", canonicalProcessContent)
	process.URL = "https://github.com/o/r/issues/3#issuecomment-3"
	review := typedArtifact(t, 3, "REVIEW", "REVIEW-001", "done", "## Review\n\nnone")
	verify := typedArtifact(t, 3, "VERIFY", "VERIFY-001", "done", canonicalVerifyContent)
	linkArtifacts(t, &spec, &task)
	linkArtifacts(t, &task, &process)
	processBody, changed, err := model.AddPRLink(process.Comment.Body, "https://github.com/o/r/pull/7")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected PR link to change process body")
	}
	process.Comment = model.ParseTypedComment(processBody)
	rationale, err := model.RenderRationaleBody("Worker Agent A", "PROCESS-001", "SPEC-001", spec.URL, "Explain why.", "internal/foo.go", 12)
	if err != nil {
		t.Fatal(err)
	}
	report, err := buildFinalVerifyReport([]model.Artifact{spec, task, process, review, verify}, "https://github.com/o/r/issues/1", finalVerifyOptions{
		PR:                7,
		PRURL:             "https://github.com/o/r/pull/7",
		RationaleRequired: true,
		RationaleComments: []github.PullRequestReviewComment{{ID: 1, Body: rationale, Path: "internal/foo.go", Line: 12}},
		PRStatus: github.CombinedStatus{Statuses: []github.Status{
			{Context: "ci/test", State: "failure"},
		}},
		PRCheckRuns: []github.CheckRun{
			{Name: "build", Status: "queued"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.OK {
		t.Fatal("failed and pending checks should fail final verify")
	}
	if len(report.FailedChecks) != 1 || len(report.PendingChecks) != 1 {
		t.Fatalf("unexpected check blockers: failed=%+v pending=%+v", report.FailedChecks, report.PendingChecks)
	}
}

func TestBuildFinalVerifyReportRequiresSerialHandoff(t *testing.T) {
	// PROCESS-002 depends on PROCESS-001, so PROCESS-001 is a serial-chain
	// predecessor that must record ### Handoff evidence when done.
	buildReport := func(handoff string) finalVerifyReport {
		t.Helper()
		spec := typedArtifact(t, 1, "SPEC", "SPEC-001", "confirmed", "## Requirement: X\n\nX MUST work.\n\n### Scenario: ok\n\n- **WHEN** x\n- **THEN** y")
		spec.URL = "https://github.com/o/r/issues/1#issuecomment-1"
		task := typedArtifact(t, 2, "TASK", "TASK-001", "done", canonicalTaskContent)
		task.URL = "https://github.com/o/r/issues/2#issuecomment-2"
		p1 := typedArtifact(t, 3, "PROCESS", "PROCESS-001", "done", "## Process: p1\n\n### Owner\n\n- Worker\n\n### Parent TASK\n\n- TASK-001\n\n### Dependencies\n\n- N/A\n\n### Covers\n\n- TASK-001\n\n### Handoff\n\n"+handoff)
		p1.URL = "https://github.com/o/r/issues/3#issuecomment-31"
		p2 := typedArtifact(t, 3, "PROCESS", "PROCESS-002", "done", "## Process: p2\n\n### Owner\n\n- Worker\n\n### Parent TASK\n\n- TASK-001\n\n### Dependencies\n\n- PROCESS-001\n\n### Covers\n\n- TASK-001\n\n### Handoff\n\nN/A")
		p2.URL = "https://github.com/o/r/issues/3#issuecomment-32"
		verify := typedArtifact(t, 3, "VERIFY", "VERIFY-001", "done", canonicalVerifyContent)
		linkArtifacts(t, &spec, &task)
		linkArtifacts(t, &task, &p1)
		linkArtifacts(t, &task, &p2)
		report, err := buildFinalVerifyReport([]model.Artifact{spec, task, p1, p2, verify}, "https://github.com/o/r/issues/1", finalVerifyOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return report
	}

	failReport := buildReport("N/A")
	if failReport.OK {
		t.Fatal("serial-chain predecessor without handoff must fail final verify")
	}
	foundHandoff := false
	for _, e := range failReport.Errors {
		if strings.Contains(e, "PROCESS-001") && strings.Contains(e, "Handoff") {
			foundHandoff = true
		}
	}
	if !foundHandoff {
		t.Fatalf("expected serial handoff error for PROCESS-001: %v", failReport.Errors)
	}

	passReport := buildReport("state.json contract fixed; successor may parse it")
	if !passReport.OK {
		t.Fatalf("recorded handoff evidence should pass final verify: %v", passReport.Errors)
	}
}

func TestBuildFinalVerifyReportRequiresVerifyTestEvidence(t *testing.T) {
	spec := typedArtifact(t, 1, "SPEC", "SPEC-001", "confirmed", "## Requirement: X\n\nX MUST work.\n\n### Scenario: ok\n\n- **WHEN** x\n- **THEN** y")
	// VERIFY references SPEC-001 coverage but no test evidence.
	verify := typedArtifact(t, 3, "VERIFY", "VERIFY-001", "done", "## Verification Summary: final\n\n### Covered SPECs\n\n- SPEC-001")
	report, err := buildFinalVerifyReport([]model.Artifact{spec, verify}, "https://github.com/o/r/issues/1", finalVerifyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if report.OK {
		t.Fatal("VERIFY without test evidence must fail final verify")
	}
	if !strings.Contains(strings.Join(report.Errors, "\n"), "test evidence") {
		t.Fatalf("expected test-evidence error: %v", report.Errors)
	}
}

func TestBuildFinalVerifyReportTestEvidenceIgnoresSubstringMatch(t *testing.T) {
	spec := typedArtifact(t, 1, "SPEC", "SPEC-001", "confirmed", "## Requirement: X\n\nX MUST work.\n\n### Scenario: ok\n\n- **WHEN** x\n- **THEN** y")
	// "latest" contains the substring "test" but is not test evidence.
	verify := typedArtifact(t, 3, "VERIFY", "VERIFY-001", "done", "## Verification Summary: final\n\nRan the latest greatest review.\n\n### Covered SPECs\n\n- SPEC-001")
	report, err := buildFinalVerifyReport([]model.Artifact{spec, verify}, "https://github.com/o/r/issues/1", finalVerifyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if report.OK {
		t.Fatal("VERIFY whose only \"test\" is a substring of another word must fail final verify")
	}
	if !strings.Contains(strings.Join(report.Errors, "\n"), "test evidence") {
		t.Fatalf("expected test-evidence error: %v", report.Errors)
	}
}

func linkArtifacts(t *testing.T, from, to *model.Artifact) {
	t.Helper()
	fromBody, changed, err := model.AddRelatedCommentLink(from.Comment.Body, to.URL)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatalf("expected %s -> %s link to change body", from.Comment.ID, to.Comment.ID)
	}
	toBody, changed, err := model.AddRelatedCommentLink(to.Comment.Body, from.URL)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatalf("expected %s -> %s link to change body", to.Comment.ID, from.Comment.ID)
	}
	from.Comment = model.ParseTypedComment(fromBody)
	to.Comment = model.ParseTypedComment(toBody)
}

func typedArtifact(t *testing.T, issue int, typ, id, status, content string) model.Artifact {
	t.Helper()
	body, err := model.EnsureTypedBody(typ, id, content, model.BodyOptions{Status: status})
	if err != nil {
		t.Fatal(err)
	}
	return model.Artifact{
		Issue:   issue,
		URL:     "https://github.com/o/r/issues/1#issuecomment-" + id,
		Comment: model.ParseTypedComment(body),
	}
}
