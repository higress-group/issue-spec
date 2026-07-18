package templates

import (
	"strings"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/assignment"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/processworkspace"
)

func TestSpecCommentRendersCanonicalBodyAcceptedByValidator(t *testing.T) {
	body, err := SpecComment(SpecCommentOptions{
		Common: CommonOptions{ID: "SPEC-001", Status: "confirmed", Scope: "canonical SPEC generation"},
		Input: SpecInput{
			Requirement: SpecRequirementInput{
				Title: "canonical SPEC comments",
				Text:  "The CLI MUST render canonical SPEC Markdown from structured fields.",
			},
			Scenarios: []SpecScenarioInput{
				{Title: "structured fields render a canonical SPEC body", When: "a caller provides fields", Then: "the CLI renders a body accepted by comment upsert"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"<!-- issue-spec:type=SPEC id=SPEC-001",
		"Type: SPEC",
		"## Requirement: canonical SPEC comments",
		"### Scenario: structured fields render a canonical SPEC body",
		"- **WHEN** a caller provides fields",
		"- **THEN** the CLI renders a body accepted by comment upsert",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("generated SPEC body missing %q:\n%s", want, body)
		}
	}
	// The generated body must pass the shared model validator without edits.
	if diags := model.ValidateCanonicalBody("SPEC", "SPEC-001", "", body); len(diags) != 0 {
		t.Fatalf("generated SPEC body not canonical: %+v", diags)
	}
	// And it must parse cleanly as a typed comment.
	if tc := model.ParseTypedComment(body); len(tc.Errors) != 0 {
		t.Fatalf("generated SPEC body has parse errors: %v", tc.Errors)
	}
	if strings.Contains(body, IssueSpecProjectURL) {
		t.Fatalf("typed comment should not include issue-spec promotion footer:\n%s", body)
	}
}

func TestSpecCommentRejectsNonNormativeRequirement(t *testing.T) {
	_, err := SpecComment(SpecCommentOptions{
		Common: CommonOptions{ID: "SPEC-001"},
		Input: SpecInput{
			Requirement: SpecRequirementInput{Title: "t", Text: "The CLI should maybe work."},
			Scenarios:   []SpecScenarioInput{{Title: "s", When: "x", Then: "y"}},
		},
	})
	if err == nil {
		t.Fatal("expected non-normative requirement text to be rejected")
	}
}

func TestSpecCommentRequiresScenario(t *testing.T) {
	_, err := SpecComment(SpecCommentOptions{
		Common: CommonOptions{ID: "SPEC-001"},
		Input:  SpecInput{Requirement: SpecRequirementInput{Title: "t", Text: "The CLI MUST work."}},
	})
	if err == nil {
		t.Fatal("expected missing scenarios to be rejected")
	}
}

func TestNonSpecTemplatesProduceParseableTypedBodies(t *testing.T) {
	task, err := TaskComment(TaskCommentOptions{
		Common: CommonOptions{ID: "TASK-001", Status: "ready"},
		Input: TaskInput{Title: "do work", Summary: "s", Checklist: []string{"a", "b"}, Covers: []string{"SPEC-001"}, ExecutionPlanning: TaskExecutionPlanning{
			OwnedAreas:    []string{"internal/x"},
			Coupling:      "low",
			ExecutionMode: "parallel-safe",
			Complexity:    "small",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	proc, err := ProcessComment(ProcessCommentOptions{
		Common: CommonOptions{ID: "PROCESS-001", Status: "ready"},
		Input:  ProcessInput{Title: "impl", Owner: "Worker", ParentTask: "TASK-001", ExecutionClass: model.ProcessExecutionReview, Scope: "x", Dependencies: []string{"PROCESS-000"}, WriteOwnership: []string{"internal/x"}, Covers: []string{"TASK-001"}, Handoff: "state.json contract fixed"},
	})
	if err != nil {
		t.Fatal(err)
	}
	verify, err := VerifyComment(VerifyCommentOptions{
		Common: CommonOptions{ID: "VERIFY-001", Status: "done"},
		Input:  VerifyInput{Title: "final", Summary: "s", Evidence: []string{"go test ./..."}, SpecRefs: []string{"SPEC-001"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"TASK": task, "PROCESS": proc, "VERIFY": verify} {
		tc := model.ParseTypedComment(body)
		if len(tc.Errors) != 0 {
			t.Fatalf("%s generated body has parse errors: %v", name, tc.Errors)
		}
	}
	if !strings.Contains(task, "- [ ] a") {
		t.Fatalf("task checklist missing:\n%s", task)
	}
	if !strings.Contains(proc, "### Write Ownership") {
		t.Fatalf("process missing write ownership:\n%s", proc)
	}
	for _, want := range []string{"### Execution Planning", "- Coupling class: low", "- Recommended execution mode: parallel-safe"} {
		if !strings.Contains(task, want) {
			t.Fatalf("task missing %q:\n%s", want, task)
		}
	}
	for _, want := range []string{"### Parent TASK", "- TASK-001", "### Execution Class", "- review", "### Handoff", "state.json contract fixed"} {
		if !strings.Contains(proc, want) {
			t.Fatalf("process missing %q:\n%s", want, proc)
		}
	}
	// Generated TASK/PROCESS bodies must pass canonical validation without edits.
	if diags := model.ValidateCanonicalBody("TASK", "TASK-001", "", task); len(diags) != 0 {
		t.Fatalf("generated TASK body not canonical: %+v", diags)
	}
	if diags := model.ValidateCanonicalBody("PROCESS", "PROCESS-001", "", proc); len(diags) != 0 {
		t.Fatalf("generated PROCESS body not canonical: %+v", diags)
	}
	for _, body := range []string{task, proc} {
		if strings.Contains(body, IssueSpecProjectURL) {
			t.Fatalf("typed comment should not include issue-spec promotion footer:\n%s", body)
		}
	}
}

func TestTaskAndProcessGeneratorsFillCanonicalDefaults(t *testing.T) {
	// Even with no execution-planning or parent-task input, generated bodies must
	// be canonical (headings present with TBD/N/A defaults).
	task, err := TaskComment(TaskCommentOptions{Common: CommonOptions{ID: "TASK-002"}, Input: TaskInput{Title: "t", Covers: []string{"SPEC-001"}}})
	if err != nil {
		t.Fatal(err)
	}
	if diags := model.ValidateCanonicalBody("TASK", "TASK-002", "", task); len(diags) != 0 {
		t.Fatalf("default TASK body not canonical: %+v", diags)
	}
	if !strings.Contains(task, "- Coupling class: TBD") {
		t.Fatalf("default TASK missing coupling default:\n%s", task)
	}
	proc, err := ProcessComment(ProcessCommentOptions{Common: CommonOptions{ID: "PROCESS-002"}, Input: ProcessInput{Title: "p", Covers: []string{"TASK-002"}}})
	if err != nil {
		t.Fatal(err)
	}
	if diags := model.ValidateCanonicalBody("PROCESS", "PROCESS-002", "", proc); len(diags) != 0 {
		t.Fatalf("default PROCESS body not canonical: %+v", diags)
	}
	if !strings.Contains(proc, "### Handoff\n\nN/A") {
		t.Fatalf("default PROCESS missing handoff default:\n%s", proc)
	}
	if !strings.Contains(proc, "### Execution Class\n\n- change-bearing") {
		t.Fatalf("default PROCESS must use conservative change-bearing class:\n%s", proc)
	}
	if !strings.Contains(proc, "### Workspace Management\n\n- managed") {
		t.Fatalf("default PROCESS must use managed workspace handling:\n%s", proc)
	}
}

func TestProcessGeneratorRejectsUnknownExecutionClass(t *testing.T) {
	_, err := ProcessComment(ProcessCommentOptions{Common: CommonOptions{ID: "PROCESS-003"}, Input: ProcessInput{
		Title: "p", ExecutionClass: model.ProcessExecutionClass("deploy"),
	}})
	if err == nil || !strings.Contains(err.Error(), "unknown PROCESS execution class") {
		t.Fatalf("expected unknown class error, got %v", err)
	}
}

func TestProcessGeneratorRejectsUnknownWorkspaceManagement(t *testing.T) {
	_, err := ProcessComment(ProcessCommentOptions{Common: CommonOptions{ID: "PROCESS-003"}, Input: ProcessInput{
		Title: "p", WorkspaceManagement: model.ProcessWorkspaceManagement("unmanaged"),
	}})
	if err == nil || !strings.Contains(err.Error(), "unknown PROCESS workspace management") {
		t.Fatalf("expected unknown workspace management error, got %v", err)
	}
}

func TestProcessGeneratorRendersIndependentWorkspaceManagement(t *testing.T) {
	body, err := ProcessComment(ProcessCommentOptions{Common: CommonOptions{ID: "PROCESS-003"}, Input: ProcessInput{
		Title: "p", WorkspaceManagement: model.ProcessWorkspaceIndependent,
	}})
	if err != nil || !strings.Contains(body, "### Workspace Management\n\n- independent") {
		t.Fatalf("independent workspace management body=%q err=%v", body, err)
	}
}

func TestProcessGeneratorRendersPortableWorkspace(t *testing.T) {
	now := time.Date(2026, 7, 13, 1, 2, 3, 0, time.UTC)
	workspace := model.ProcessWorkspace{
		SchemaVersion: processworkspace.LeaseSchemaVersion, WorkspaceID: "ws-process-004",
		Repository: "higress-group/issue-spec", ProcessID: "PROCESS-004",
		ExecutionClass: processworkspace.ExecutionChangeBearing, Mode: processworkspace.ModeWritable,
		BaseSHA: "1111111111111111111111111111111111111111", Branch: "codex/process-004",
		WriteOwnership: []string{"internal/model/**"}, RuntimeNamespace: "runtime-process-004",
		State: processworkspace.StatePrepared, CreatedAt: now, UpdatedAt: now,
	}
	body, err := ProcessComment(ProcessCommentOptions{
		Common: CommonOptions{ID: "PROCESS-004", Status: "in-progress"},
		Input: ProcessInput{Title: "metadata", ParentTask: "TASK-002", ExecutionClass: model.ProcessExecutionChangeBearing,
			Workspace: &workspace, Handoff: "N/A"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "### Workspace\n\n```json") {
		t.Fatalf("generated PROCESS missing Workspace section:\n%s", body)
	}
	parsed := model.ParseProcessWorkspace("PROCESS-004", "", body)
	if parsed.Blocking() || parsed.Workspace == nil || parsed.Workspace.WorkspaceID != "ws-process-004" {
		t.Fatalf("generated Workspace = %+v", parsed)
	}
	for _, forbidden := range []string{"worktree_path", "integration_root", "lock_token", "/private/", "/Users/"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("generated PROCESS contains local field %q:\n%s", forbidden, body)
		}
	}
}

func TestProcessGeneratorCarriesBoundCompletionRevisions(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	base, result, integrated := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	workspace := model.ProcessWorkspace{
		SchemaVersion: processworkspace.LeaseSchemaVersion, WorkspaceID: "ws-process-008", Repository: "o/r", ProcessID: "PROCESS-008",
		ExecutionClass: processworkspace.ExecutionChangeBearing, Mode: processworkspace.ModeWritable, BaseSHA: base, Branch: "worker",
		WriteOwnership: []string{"internal/**"}, RuntimeNamespace: "ws-process-008", State: processworkspace.StateIntegrated,
		Assignment: &processworkspace.AssignmentBinding{SchemaVersion: assignment.AssignmentSchemaVersion, AssignmentID: "assignment-008-1",
			Digest: strings.Repeat("d", 64), Role: assignment.RoleImplementation, BaseRevision: base, Generation: 1},
		ResultCommit: result, IntegrationSHA: integrated, CreatedAt: now, UpdatedAt: now,
	}
	body, err := ProcessComment(ProcessCommentOptions{Common: CommonOptions{ID: "PROCESS-008"}, Input: ProcessInput{
		Title: "receipt completion", ParentTask: "TASK-005", ExecutionClass: model.ProcessExecutionChangeBearing,
		WriteOwnership: []string{"internal/**"}, Workspace: &workspace, Handoff: "receipt accepted",
	}})
	if err != nil {
		t.Fatal(err)
	}
	parsed := model.ParseProcessWorkspace("PROCESS-008", "", body)
	if parsed.Workspace == nil || parsed.Workspace.Assignment == nil || parsed.Workspace.ResultCommit != result || parsed.Workspace.IntegrationSHA != integrated {
		t.Fatalf("generated completion carrier=%+v\n%s", parsed, body)
	}
}

func TestProcessGeneratorRejectsWorkspaceIdentityOrClassMismatch(t *testing.T) {
	workspace := model.ProcessWorkspace{ProcessID: "PROCESS-OTHER", ExecutionClass: processworkspace.ExecutionReview}
	_, err := ProcessComment(ProcessCommentOptions{Common: CommonOptions{ID: "PROCESS-005"}, Input: ProcessInput{
		Title: "metadata", ExecutionClass: model.ProcessExecutionChangeBearing, Workspace: &workspace,
	}})
	if err == nil || !strings.Contains(err.Error(), "execution class") {
		t.Fatalf("expected workspace class mismatch, got %v", err)
	}
}

func TestProcessGeneratorRendersMinimalPortableAssignmentInput(t *testing.T) {
	body, err := ProcessComment(ProcessCommentOptions{
		Common: CommonOptions{ID: "PROCESS-005", Status: "ready"},
		Input: ProcessInput{
			Title: "assignment schema", ParentTask: "TASK-003",
			Scope: "pure schemas", Covers: []string{"SPEC-001", "SPEC-002", "SPEC-005"},
			Assignment: &assignment.ProcessInput{
				Objective:     "Define portable role packets",
				RequiredTests: []assignment.TestSelector{{ID: "unit", Command: "go test ./internal/assignment"}},
				CommitPolicy:  &assignment.CommitPolicy{RequireSingleCommit: true, RequireDCO: true},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"### Assignment", `"objective": "Define portable role packets"`, `"required_tests"`, `"require_dco": true`} {
		if !strings.Contains(body, want) {
			t.Fatalf("generated PROCESS missing %q:\n%s", want, body)
		}
	}
	for _, forbidden := range []string{"worktree_path", "owner_token", "closure_policy", "archive_policy"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("generated PROCESS contains delivery/workflow field %q:\n%s", forbidden, body)
		}
	}
	parsed := model.ParseTypedComment(body)
	if len(parsed.Errors) != 0 || parsed.Assignment == nil || parsed.Assignment.Objective != "Define portable role packets" {
		t.Fatalf("parsed PROCESS = %+v", parsed)
	}
}

func TestProcessGeneratorRejectsEmptyAssignmentInput(t *testing.T) {
	_, err := ProcessComment(ProcessCommentOptions{Common: CommonOptions{ID: "PROCESS-005"}, Input: ProcessInput{
		Title: "assignment schema", Assignment: &assignment.ProcessInput{},
	}})
	if err == nil || !strings.Contains(err.Error(), "at least one structured field") {
		t.Fatalf("error = %v", err)
	}
}

func TestReviewCommentDoesNotUseReviewSyncSummaryShape(t *testing.T) {
	body, err := ReviewComment(ReviewCommentOptions{
		Common: CommonOptions{ID: "REVIEW-002", Status: "done"},
		Input:  ReviewInput{Title: "manual review", Summary: "looks good", Findings: []string{"none"}, Verdict: "approve"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "## Review Sync Summary") {
		t.Fatalf("generic REVIEW template must not emit the review sync shape:\n%s", body)
	}
	if !strings.Contains(body, "## Review Summary") {
		t.Fatalf("generic REVIEW template missing its own summary heading:\n%s", body)
	}
	if tc := model.ParseTypedComment(body); len(tc.Errors) != 0 {
		t.Fatalf("review body has parse errors: %v", tc.Errors)
	}
}

func TestVerifyCommentRendersStructuredRevisionBoundEvidenceAdditively(t *testing.T) {
	body, err := VerifyComment(VerifyCommentOptions{Common: CommonOptions{ID: "VERIFY-101", Agent: "Verifier",
		SubjectRevision: "head-abc", Status: "done", Scope: "role-owned verification submission"}, Input: VerifyInput{
		Title: "role-owned receipt", Summary: "Exact revision verified.", SubjectRevision: "head-abc",
		Evidence: []string{"Test unit: passed"}, Tests: []VerifyTestEvidence{{ID: "unit",
			Command: "go test ./internal/gates", Outcome: "passed", Assurance: "self-reported"}},
		Checks: []VerifyCheckEvidence{{Provider: "github", Name: "unit", State: "success",
			SubjectRevision: "head-abc", Source: "github-check-run:42"}}, SpecRefs: []string{"SPEC-005"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Subject Revision: head-abc", "### Revision\n\n`head-abc`", "### Local Tests",
		"go test ./internal/gates", "self-reported", "### Provider Checks", "github-check-run:42", "SPEC-005"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in:\n%s", want, body)
		}
	}
	parsed := model.ParseTypedComment(body)
	if len(parsed.Errors) != 0 || parsed.SubjectRevision != "head-abc" || parsed.Status != "done" {
		t.Fatalf("parsed VERIFY=%+v", parsed)
	}
}
