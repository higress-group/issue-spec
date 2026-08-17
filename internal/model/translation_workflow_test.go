package model

import (
	"strings"
	"testing"
	"time"
)

func TestVerifyTraceabilityIgnoresDuplicatedSectionsInTranslatedSuffix(t *testing.T) {
	specBody, _ := EnsureTypedBody("SPEC", "SPEC-001", "## Requirement: X\n\nX MUST work.\n\n### Scenario: ok\n\n- **WHEN** x\n- **THEN** y", BodyOptions{})
	taskBody, _ := EnsureTypedBody("TASK", "TASK-001", "## Task: work\n\n### Execution Planning\n\n- Coupling class: low\n\n### Covers\n\n- SPEC-001", BodyOptions{})
	taskBody, _, _ = AddRelatedCommentLink(taskBody, "https://github.com/o/r/issues/1#issuecomment-1")
	// The bot copy duplicates the marker, header, and every planning section.
	translated := suffixedBody(taskBody, taskBody)

	report := VerifyTraceability([]Artifact{
		{URL: "https://github.com/o/r/issues/1#issuecomment-1", Comment: ParseTypedComment(specBody)},
		{URL: "https://github.com/o/r/issues/2#issuecomment-2", Comment: ParseTypedComment(translated)},
	})
	if !report.OK {
		t.Fatalf("translated TASK must verify like the canonical body: %v", report.Errors)
	}
	for _, err := range report.Errors {
		if strings.Contains(err, "duplicate") {
			t.Fatalf("duplicated suffix section leaked into traceability: %v", err)
		}
	}
}

func TestTranslatedAnswerDigestStableButEditedEvidenceStillRejects(t *testing.T) {
	questionBody := testQuestionBody(t, "QUESTION-001", "Choose one.", testChoiceModel(ChoiceModeSingle, true))
	snapshot, err := SnapshotQuestion(questionBody, "https://example.test/issues/1#issuecomment-10")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := BuildAnswerPayload(snapshot, []string{"safe"}, "")
	if err != nil {
		t.Fatal(err)
	}
	answerBody := testAnswerBody(t, "ANSWER-001", payload)
	translated := suffixedBody(answerBody, answerBody)

	if RepresentationDigest(answerBody) != RepresentationDigest(translated) {
		t.Fatal("bot translation after sealing must keep the ANSWER digest stable")
	}

	created := time.Date(2026, 8, 16, 10, 0, 0, 0, time.UTC)
	edited := created.Add(time.Minute)
	resolution := ResolveEffectiveAnswers([]AnswerObservation{{
		ProviderID: "71", Actor: "human", CreatedAt: created, UpdatedAt: edited, Body: translated,
		URL: "https://example.test/issues/1#issuecomment-71"}})
	if len(resolution.Effective) != 0 {
		t.Fatalf("edited ANSWER became authority: %+v", resolution.Effective)
	}
	if len(resolution.Diagnostics) != 1 || !strings.Contains(resolution.Diagnostics[0].Message, "edited ANSWER") {
		t.Fatalf("diagnostics=%+v", resolution.Diagnostics)
	}

	unedited := ResolveEffectiveAnswers([]AnswerObservation{{
		ProviderID: "71", Actor: "human", CreatedAt: created, UpdatedAt: created, Body: translated,
		URL: "https://example.test/issues/1#issuecomment-71"}})
	effective, ok := unedited.Effective["QUESTION-001"]
	if !ok || effective.BodyDigest != RepresentationDigest(answerBody) {
		t.Fatalf("effective=%+v ok=%v", effective, ok)
	}
}
