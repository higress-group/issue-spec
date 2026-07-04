package templates

import (
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/model"
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
		Input:  TaskInput{Title: "do work", Summary: "s", Checklist: []string{"a", "b"}, Covers: []string{"SPEC-001"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	proc, err := ProcessComment(ProcessCommentOptions{
		Common: CommonOptions{ID: "PROCESS-001", Status: "ready"},
		Input:  ProcessInput{Title: "impl", Owner: "Worker", Scope: "x", Dependencies: []string{"PROCESS-000"}, WriteOwnership: []string{"internal/x"}, Covers: []string{"TASK-001"}},
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
