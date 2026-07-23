package gates

import (
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/templates"
)

func gateQuestionAndAnswer(t *testing.T) (model.Artifact, model.AnswerResolution) {
	t.Helper()
	choice := model.ChoiceModel{
		Version: 1, Mode: model.ChoiceModeSingle,
		Options: []model.ChoiceOption{{ID: "ship", Label: "Ship"}},
	}
	questionBody, err := templates.QuestionComment(templates.QuestionOptions{
		ID: "QUESTION-800", Status: "blocked", Blocking: true, Question: "Ship it?",
		Assumption: "Do not ship.", ChoiceModel: &choice,
	})
	if err != nil {
		t.Fatal(err)
	}
	question := model.Artifact{Issue: 1, CommentID: 80, URL: "https://example.test/q",
		Comment: model.ParseTypedComment(questionBody)}
	snapshot, err := model.SnapshotQuestion(questionBody, question.URL)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := model.BuildAnswerPayload(snapshot, []string{"ship"}, "")
	if err != nil {
		t.Fatal(err)
	}
	answerBody, err := templates.AnswerComment(templates.AnswerOptions{ID: "ANSWER-800", Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 24, 3, 0, 0, 0, time.UTC)
	answers := model.ResolveEffectiveAnswers([]model.AnswerObservation{{
		ProviderID: "81", Actor: "reviewer", CreatedAt: now, UpdatedAt: now, Body: answerBody,
	}})
	return question, answers
}

func TestEveryStageGateUsesEffectiveAnswerAuthority(t *testing.T) {
	question, answers := gateQuestionAndAnswer(t)
	spec := artifact(t, "SPEC", "SPEC-001", "confirmed", specLogical)

	blocked := evaluate(t, Snapshot{Target: TargetProposal, Mode: ModeForecast, Artifacts: []model.Artifact{spec, question}})
	if blocked.Ready || !containsCode(blocked, CodeQuestionBlocked) {
		t.Fatalf("proposal gate without ANSWER = %+v", blocked.Diagnostics)
	}
	ready := evaluate(t, Snapshot{
		Target: TargetProposal, Mode: ModeForecast, Artifacts: []model.Artifact{spec, question}, Answers: answers,
	})
	if !ready.Ready || containsCode(ready, CodeQuestionBlocked) {
		t.Fatalf("proposal gate with ANSWER = %+v", ready.Diagnostics)
	}

	final := minimalFinalSnapshot(t, ModeAuthoritative)
	final.Artifacts = append(final.Artifacts, question)
	blocked = evaluate(t, final)
	if blocked.Ready || !containsCode(blocked, CodeQuestionBlocked) {
		t.Fatalf("final gate without ANSWER = %+v", blocked.Diagnostics)
	}
	final.Answers = answers
	ready = evaluate(t, final)
	if !ready.Ready || containsCode(ready, CodeQuestionBlocked) {
		t.Fatalf("final gate with ANSWER = %+v", ready.Diagnostics)
	}
}
