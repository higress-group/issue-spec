package commands

import (
	"testing"

	"github.com/higress-group/issue-spec/internal/model"
)

func TestSummarizeStatusBlocksOnBlockedQuestion(t *testing.T) {
	specBody, err := model.EnsureTypedBody("SPEC", "SPEC-001", "## Requirement: X\n\nX MUST work.\n\n### Scenario: ok\n\n- **WHEN** x\n- **THEN** y", model.BodyOptions{Status: "confirmed"})
	if err != nil {
		t.Fatal(err)
	}
	questionBody, err := model.EnsureTypedBody("QUESTION", "QUESTION-001", "## Question\n\nDecide X.", model.BodyOptions{Status: "blocked"})
	if err != nil {
		t.Fatal(err)
	}
	summary := summarizeStatus("o/r", 1, 0, 0, []model.Artifact{
		{Issue: 1, URL: "https://github.com/o/r/issues/1#issuecomment-1", Comment: model.ParseTypedComment(specBody)},
		{Issue: 1, URL: "https://github.com/o/r/issues/1#issuecomment-2", Comment: model.ParseTypedComment(questionBody)},
	})
	if summary.OK {
		t.Fatal("blocked QUESTION should make status non-OK")
	}
	if summary.BlockingQuestions != 1 {
		t.Fatalf("blocking questions = %d", summary.BlockingQuestions)
	}
}

func TestSummarizeStatusReportsSessionMetadataDiagnosticsWithoutBlocking(t *testing.T) {
	specBody, err := model.EnsureTypedBody("SPEC", "SPEC-001", "## Requirement: X\n\nX MUST work.\n\n### Scenario: ok\n\n- **WHEN** x\n- **THEN** y", model.BodyOptions{Status: "confirmed"})
	if err != nil {
		t.Fatal(err)
	}
	summary := summarizeStatus("o/r", 1, 0, 0, []model.Artifact{
		{Issue: 1, URL: "https://github.com/o/r/issues/1#issuecomment-1", Comment: model.ParseTypedComment(specBody)},
	})
	if !summary.OK {
		t.Fatalf("metadata diagnostics should not block status: %+v", summary.NextGates)
	}
	if len(summary.Diagnostics) != 1 || summary.Diagnostics[0].Code != "missing_session_metadata" {
		t.Fatalf("unexpected diagnostics: %+v", summary.Diagnostics)
	}
}
