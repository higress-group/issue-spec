package changes

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/higress-group/issue-spec/internal/templates"
)

func TestChangeCardUsesEffectiveAnswerInsteadOfMutableQuestionStatus(t *testing.T) {
	choice := model.ChoiceModel{
		Version: 1, Mode: model.ChoiceModeSingle,
		Options: []model.ChoiceOption{{ID: "accept", Label: "Accept"}},
	}
	questionBody, err := templates.QuestionComment(templates.QuestionOptions{
		ID: "QUESTION-900", Status: "blocked", Blocking: true, Question: "Accept?",
		Assumption: "No.", ChoiceModel: &choice,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := model.SnapshotQuestion(questionBody, "https://example.test/q")
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := model.BuildAnswerPayload(snapshot, []string{"accept"}, "")
	answerBody, _ := templates.AnswerComment(templates.AnswerOptions{ID: "ANSWER-900", Payload: payload})
	now := time.Date(2026, 7, 24, 5, 0, 0, 0, time.UTC)
	repositoryID, issueID := uuid.New(), uuid.New()
	items := []rawArtifact{{
		repositoryID: repositoryID, issueID: issueID, number: 1, title: "Proposal",
		state: "open", updatedAt: now, labels: []string{"issue-spec/proposal"},
		projected: true, kind: StageProposal, changeKey: "choice", version: "1",
	}}
	question := typedArtifact{
		repositoryID: repositoryID, issueID: issueID, typ: "QUESTION", key: "QUESTION-900",
		status: "blocked", body: questionBody, createdAt: now, updatedAt: now,
	}
	answer := typedArtifact{
		repositoryID: repositoryID, issueID: issueID, typ: "ANSWER", key: "ANSWER-900",
		status: "done", body: answerBody, providerID: uuid.NewString(), actor: uuid.NewString(),
		createdAt: now.Add(time.Minute), updatedAt: now.Add(time.Minute), version: 1,
	}
	repository := Repository{ID: repositoryID, Name: "o/r"}
	card := buildCard(uuid.New(), repository, "choice", items, []typedArtifact{question}, map[uuid.UUID][]models.CodeChangeRelationship{})
	if card.Lifecycle != LifecycleBlocked {
		t.Fatalf("card without answer = %+v", card)
	}
	card = buildCard(uuid.New(), repository, "choice", items, []typedArtifact{question, answer}, map[uuid.UUID][]models.CodeChangeRelationship{})
	if card.Lifecycle == LifecycleBlocked {
		t.Fatalf("card with effective answer = %+v", card)
	}
	answer.version = 2
	answer.updatedAt = answer.updatedAt.Add(time.Second)
	card = buildCard(uuid.New(), repository, "choice", items, []typedArtifact{question, answer}, map[uuid.UUID][]models.CodeChangeRelationship{})
	if card.Lifecycle != LifecycleBlocked {
		t.Fatalf("edited answer became change-card authority = %+v", card)
	}
}
