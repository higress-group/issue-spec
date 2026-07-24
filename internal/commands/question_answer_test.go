package commands

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/gates"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/templates"
	"github.com/higress-group/issue-spec/internal/workflow"
)

func commandChoiceQuestion(t *testing.T) string {
	t.Helper()
	choice := model.ChoiceModel{
		Version: 1, Mode: model.ChoiceModeSingle, AllowCustom: true,
		Options: []model.ChoiceOption{
			{ID: "keep", Label: "Keep", Description: "Keep behavior"},
			{ID: "change", Label: "Change", Description: "Change behavior"},
		},
	}
	body, err := templates.QuestionComment(templates.QuestionOptions{
		ID: "QUESTION-701", Agent: "Coordinator", Status: "blocked", Blocking: true,
		Question: "Which behavior?", Assumption: "Keep.", ChoiceModel: &choice,
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestQuestionAnswerAppendsCanonicalSnapshotsAndNeverUpdates(t *testing.T) {
	questionBody := commandChoiceQuestion(t)
	question := github.Comment{ID: 70, HTMLURL: "https://example.test/issues/7#issuecomment-70", Body: questionBody}
	var created []string
	backend := fakeGitHubBackend{
		info: github.BackendInfo{Name: "fake", Kind: "test", Host: "github.com"},
		listIssueComments: func(context.Context, string, int) ([]github.Comment, error) {
			return []github.Comment{question}, nil
		},
		createComment: func(_ context.Context, _ string, _ int, body string) (github.Comment, error) {
			created = append(created, body)
			return github.Comment{ID: int64(70 + len(created)), HTMLURL: "https://example.test/answer"}, nil
		},
		updateComment: func(context.Context, string, int64, string) (github.Comment, error) {
			t.Fatal("ANSWER submission attempted an update")
			return github.Comment{}, nil
		},
	}
	app, out := transitionApp(backend)
	for _, id := range []string{"ANSWER-701", "ANSWER-702"} {
		if code := app.runQuestionAnswer(t.Context(), []string{
			"--repo", "o/r", "--issue", "7", "--id", id, "--question-id", "QUESTION-701", "--select", "keep",
		}); code != 0 {
			t.Fatalf("%s code=%d out=%s", id, code, out.String())
		}
	}
	if len(created) != 2 || created[0] == "" || created[1] == "" {
		t.Fatalf("created ANSWER bodies = %d", len(created))
	}
	for i, body := range created {
		payload, err := model.ParseAnswerPayload(body)
		if err != nil {
			t.Fatalf("answer[%d]: %v\n%s", i, err, body)
		}
		if payload.Question.Question != "Which behavior?" || payload.Question.SourceURL != question.HTMLURL ||
			len(payload.Selection.Options) != 1 || payload.Selection.Options[0].ID != "keep" {
			t.Fatalf("answer[%d] payload = %+v", i, payload)
		}
	}
}

func TestQuestionChoicePathsRejectRewriteAndGenericUpsert(t *testing.T) {
	questionBody := commandChoiceQuestion(t)
	backend := fakeGitHubBackend{
		info: github.BackendInfo{Name: "fake", Kind: "test", Host: "github.com"},
		listIssueComments: func(context.Context, string, int) ([]github.Comment, error) {
			return []github.Comment{{ID: 70, HTMLURL: "https://example.test/q", Body: questionBody}}, nil
		},
		updateComment: func(context.Context, string, int64, string) (github.Comment, error) {
			t.Fatal("choice QUESTION resolve attempted a rewrite")
			return github.Comment{}, nil
		},
	}
	app, _, errOut := transitionAppWithError(backend)
	if code := app.runQuestionResolve(t.Context(), []string{
		"--repo", "o/r", "--issue", "7", "--id", "QUESTION-701", "--resolution", "Keep",
	}); code != 2 || !strings.Contains(errOut.String(), "append-only") {
		t.Fatalf("resolve code=%d err=%s", code, errOut.String())
	}

	snapshot, err := model.SnapshotQuestion(questionBody, "https://example.test/q")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := model.BuildAnswerPayload(snapshot, []string{"keep"}, "")
	if err != nil {
		t.Fatal(err)
	}
	answerBody, err := templates.AnswerComment(templates.AnswerOptions{ID: "ANSWER-703", Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	path := writeTempInput(t, answerBody)
	app, _, errOut = transitionAppWithError(backend)
	if code := app.runCommentUpsert(t.Context(), []string{
		"--repo", "o/r", "--issue", "7", "--type", "ANSWER", "--id", "ANSWER-703", "--body-file", path,
	}); code != 2 || !strings.Contains(errOut.String(), "cannot be upserted") {
		t.Fatalf("upsert code=%d err=%s", code, errOut.String())
	}
}

func TestCommentActiveAnswerSelectionUsesEffectiveProviderOrder(t *testing.T) {
	questionBody := commandChoiceQuestion(t)
	snapshot, err := model.SnapshotQuestion(questionBody, "https://example.test/q")
	if err != nil {
		t.Fatal(err)
	}
	firstPayload, _ := model.BuildAnswerPayload(snapshot, []string{"keep"}, "")
	secondPayload, _ := model.BuildAnswerPayload(snapshot, []string{"change"}, "")
	first, _ := templates.AnswerComment(templates.AnswerOptions{ID: "ANSWER-710", Payload: firstPayload})
	second, _ := templates.AnswerComment(templates.AnswerOptions{ID: "ANSWER-711", Payload: secondPayload})
	now := time.Date(2026, 7, 24, 2, 0, 0, 0, time.UTC)
	backend := fakeGitHubBackend{
		info: github.BackendInfo{Name: "fake", Kind: "test", Host: "github.com"},
		listIssueComments: func(context.Context, string, int) ([]github.Comment, error) {
			return []github.Comment{
				{ID: 70, HTMLURL: "https://example.test/q", Body: questionBody, User: &github.User{Login: "coord"}, CreatedAt: now, UpdatedAt: now},
				{ID: 71, HTMLURL: "https://example.test/a1", Body: first, User: &github.User{Login: "alice"}, CreatedAt: now, UpdatedAt: now},
				{ID: 72, HTMLURL: "https://example.test/a2", Body: second, User: &github.User{Login: "bob"}, CreatedAt: now.Add(time.Minute), UpdatedAt: now.Add(time.Minute)},
			}, nil
		},
	}
	app, out := transitionApp(backend)
	if code := app.runCommentList(t.Context(), []string{
		"--repo", "o/r", "--issue", "7", "--type", "ANSWER", "--active-only",
	}); code != 0 {
		t.Fatalf("code=%d out=%s", code, out.String())
	}
	if strings.Contains(out.String(), "ANSWER-710") || !strings.Contains(out.String(), "ANSWER-711") {
		t.Fatalf("active ANSWER output = %s", out.String())
	}
}

func TestStatusCountsAndGateUseTheSameEffectiveAnswer(t *testing.T) {
	questionBody := commandChoiceQuestion(t)
	snapshot, err := model.SnapshotQuestion(questionBody, "https://example.test/q")
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := model.BuildAnswerPayload(snapshot, []string{"keep"}, "")
	answerBody, _ := templates.AnswerComment(templates.AnswerOptions{ID: "ANSWER-720", Payload: payload})
	now := time.Date(2026, 7, 24, 4, 0, 0, 0, time.UTC)
	answers := model.ResolveEffectiveAnswers([]model.AnswerObservation{{
		ProviderID: "72", Actor: "alice", CreatedAt: now, UpdatedAt: now, Body: answerBody,
	}})
	specBody, err := model.EnsureTypedBody("SPEC", "SPEC-001",
		"## Requirement: status\n\nStatus MUST agree with gates.\n\n### Scenario: answer\n\n- **WHEN** an answer exists\n- **THEN** the question is satisfied",
		model.BodyOptions{Status: "confirmed"})
	if err != nil {
		t.Fatal(err)
	}
	artifacts := []model.Artifact{
		{Issue: 1, CommentID: 1, URL: "https://example.test/spec", Comment: model.ParseTypedComment(specBody)},
		{Issue: 1, CommentID: 2, URL: "https://example.test/q", Comment: model.ParseTypedComment(questionBody)},
	}
	without := summarizeStatusForGate("o/r", 1, 0, 0, gates.TargetProposal, artifacts, workflow.Plan{}, statusGateCollection{})
	if without.BlockingQuestions != 1 || without.Gate.Ready {
		t.Fatalf("status without answer = %+v", without)
	}
	with := summarizeStatusForGate("o/r", 1, 0, 0, gates.TargetProposal, artifacts, workflow.Plan{},
		statusGateCollection{Answers: answers})
	if with.BlockingQuestions != 0 || !with.Gate.Ready {
		t.Fatalf("status with answer = %+v", with)
	}
}
