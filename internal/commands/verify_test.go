package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
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

func TestBuildFinalVerifyReportReportsSessionDiagnosticsWithoutErrors(t *testing.T) {
	spec := typedArtifact(t, 1, "SPEC", "SPEC-001", "confirmed", "## Requirement: X\n\nX MUST work.\n\n### Scenario: ok\n\n- **WHEN** x\n- **THEN** y")
	verify := typedArtifact(t, 3, "VERIFY", "VERIFY-001", "done", canonicalVerifyContent)
	report, err := buildFinalVerifyReport([]model.Artifact{spec, verify}, "https://github.com/o/r/issues/1", finalVerifyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !report.OK {
		t.Fatalf("metadata diagnostics should not fail verify: %+v", report.Errors)
	}
	if len(report.Diagnostics) != 2 {
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
		RationaleComments: []github.PullRequestReviewComment{{Body: body}},
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
		RationaleComments: []github.PullRequestReviewComment{{Body: body}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !report.OK {
		t.Fatalf("expected rationale coverage OK: %+v", report.Errors)
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
			{ID: 1, Body: rationale},
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
			{ID: 1, Body: rationale},
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
		RationaleComments: []github.PullRequestReviewComment{{ID: 1, Body: rationale}},
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
