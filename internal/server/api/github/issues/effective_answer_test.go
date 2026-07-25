package issues

import (
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/higress-group/issue-spec/internal/server/store"
	"github.com/higress-group/issue-spec/internal/templates"
)

func questionSnapshotFixture() model.QuestionSnapshot {
	return model.QuestionSnapshot{
		ID:                "QUESTION-007",
		Question:          "Which release posture should we use?",
		Blocking:          true,
		DefaultAssumption: "Use Safe.",
		IssueURL:          "https://issues.example/acme/workflow/issues/41",
		SourceURL:         "https://issues.example/acme/workflow/issues/41#issuecomment-20",
		ChoiceModel: model.ChoiceModel{
			Version: model.ChoiceModelVersion,
			Mode:    model.ChoiceModeSingle,
			Options: []model.ChoiceOption{
				{ID: "safe", Label: "Safe"},
				{ID: "fast", Label: "Fast"},
			},
			AllowCustom: true,
		},
	}
}

func answerBodyFixture(t *testing.T, answerID string, optionIDs []string, custom string) string {
	t.Helper()
	payload, err := model.BuildAnswerPayload(questionSnapshotFixture(), optionIDs, custom)
	if err != nil {
		t.Fatalf("BuildAnswerPayload: %v", err)
	}
	body, err := templates.AnswerComment(templates.AnswerOptions{
		ID: answerID, Agent: "alice", Scope: "QUESTION-007", Payload: payload,
	})
	if err != nil {
		t.Fatalf("AnswerComment: %v", err)
	}
	return body
}

func answerObservationFixture(commentID int64, actor, body string, createdAt time.Time, representationVersion int64) store.TypedAnswerObservation {
	return store.TypedAnswerObservation{
		CompatibilityID: commentID, ActorLogin: actor, Body: body,
		RepresentationVersion: representationVersion, CreatedAt: createdAt, UpdatedAt: createdAt,
	}
}

func TestEffectiveAnswerViewSkipsEditedAndDuplicateAnswers(t *testing.T) {
	resource := models.RepositoryResource{Owner: "acme", Name: "workflow"}
	base := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	observations := []store.TypedAnswerObservation{
		answerObservationFixture(101, "alice", answerBodyFixture(t, "ANSWER-101", []string{"safe"}, ""), base, 1),
		// Edited ANSWERs are never workflow authority.
		answerObservationFixture(102, "bob", answerBodyFixture(t, "ANSWER-102", []string{"fast"}, ""), base.Add(time.Minute), 2),
		// A reused typed identity is not authority either.
		answerObservationFixture(103, "carol", answerBodyFixture(t, "ANSWER-101", []string{"fast"}, ""), base.Add(2*time.Minute), 1),
	}

	view := effectiveAnswerView(observations, "QUESTION-007", "https://web.example", resource, 41)
	if view == nil {
		t.Fatal("expected an effective answer view")
	}
	if view.ID != "ANSWER-101" || view.CommentID != 101 || view.Actor != "alice" {
		t.Fatalf("unexpected effective answer identity: %+v", view)
	}
	if !view.CreatedAt.Equal(base) {
		t.Fatalf("unexpected created_at: %v", view.CreatedAt)
	}
	if len(view.Selection.Options) != 1 || view.Selection.Options[0].ID != "safe" ||
		view.Selection.Options[0].Label != "Safe" || view.Selection.Custom != "" {
		t.Fatalf("unexpected selection: %+v", view.Selection)
	}
	if view.SourceURL != "https://web.example/acme/workflow/issues/41#issuecomment-101" {
		t.Fatalf("unexpected source URL: %q", view.SourceURL)
	}
}

func TestEffectiveAnswerViewLatestValidAnswerWins(t *testing.T) {
	resource := models.RepositoryResource{Owner: "acme", Name: "workflow"}
	base := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	observations := []store.TypedAnswerObservation{
		answerObservationFixture(101, "alice", answerBodyFixture(t, "ANSWER-101", []string{"safe"}, ""), base, 1),
		answerObservationFixture(102, "bob", answerBodyFixture(t, "ANSWER-102", nil, "Staged rollout."), base.Add(time.Minute), 1),
	}

	view := effectiveAnswerView(observations, "QUESTION-007", "https://web.example", resource, 41)
	if view == nil {
		t.Fatal("expected an effective answer view")
	}
	if view.ID != "ANSWER-102" || view.CommentID != 102 || view.Actor != "bob" {
		t.Fatalf("unexpected effective answer identity: %+v", view)
	}
	if len(view.Selection.Options) != 0 || view.Selection.Custom != "Staged rollout." {
		t.Fatalf("unexpected selection: %+v", view.Selection)
	}
}

func TestEffectiveAnswerViewAbsentOrDegradedCases(t *testing.T) {
	resource := models.RepositoryResource{Owner: "acme", Name: "workflow"}
	base := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	valid := answerObservationFixture(101, "alice", answerBodyFixture(t, "ANSWER-101", []string{"safe"}, ""), base, 1)
	edited := answerObservationFixture(102, "bob", answerBodyFixture(t, "ANSWER-102", []string{"fast"}, ""), base.Add(time.Minute), 2)

	if view := effectiveAnswerView(nil, "QUESTION-007", "https://web.example", resource, 41); view != nil {
		t.Fatalf("expected nil without observations, got %+v", view)
	}
	if view := effectiveAnswerView([]store.TypedAnswerObservation{edited}, "QUESTION-007", "https://web.example", resource, 41); view != nil {
		t.Fatalf("expected nil with only an edited ANSWER, got %+v", view)
	}
	if view := effectiveAnswerView([]store.TypedAnswerObservation{valid}, "QUESTION-008", "https://web.example", resource, 41); view != nil {
		t.Fatalf("expected nil for another QUESTION, got %+v", view)
	}
	view := effectiveAnswerView([]store.TypedAnswerObservation{valid}, "QUESTION-007", "not a origin", resource, 41)
	if view == nil {
		t.Fatal("expected an effective answer view")
	}
	if view.SourceURL != "" {
		t.Fatalf("expected empty source URL for an invalid Web origin, got %q", view.SourceURL)
	}
}
