package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/auth"
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
	for _, id := range []string{"ANSWER-7001", "ANSWER-7002"} {
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

func TestQuestionAnswerRequiresIssueScopedNewIDUnlessLegacyBypassIsExplicit(t *testing.T) {
	questionBody := commandChoiceQuestion(t)
	question := github.Comment{ID: 70, HTMLURL: "https://example.test/issues/7#issuecomment-70", Body: questionBody}
	creates := 0
	backend := fakeGitHubBackend{
		info: github.BackendInfo{Name: "fake", Kind: "test", Host: "github.com"},
		listIssueComments: func(context.Context, string, int) ([]github.Comment, error) {
			return []github.Comment{question}, nil
		},
		createComment: func(_ context.Context, _ string, _ int, body string) (github.Comment, error) {
			creates++
			return github.Comment{ID: 71, HTMLURL: "https://example.test/answer", Body: body}, nil
		},
	}
	app, out, errOut := transitionAppWithError(backend)
	args := []string{"--repo", "o/r", "--issue", "7", "--id", "ANSWER-701", "--question-id", "QUESTION-701", "--select", "keep"}
	if code := app.runQuestionAnswer(t.Context(), args); code != 2 || creates != 0 || !strings.Contains(errOut.String(), "expected ANSWER-7<NNN>") {
		t.Fatalf("without bypass code=%d creates=%d stdout=%q stderr=%q", code, creates, out.String(), errOut.String())
	}
	out.Reset()
	errOut.Reset()
	args = append(args, "--allow-legacy-id")
	if code := app.runQuestionAnswer(t.Context(), args); code != 0 || creates != 1 {
		t.Fatalf("with bypass code=%d creates=%d stdout=%q stderr=%q", code, creates, out.String(), errOut.String())
	}
}

type fakeNativeQuestionAnswerProvider struct {
	question    github.NativeQuestionAuthority
	answerID    string
	getRepo     string
	getIssue    int
	getID       string
	createRepo  string
	createIssue int
	intent      github.NativeAnswerIntent
	createErr   error
}

func (f *fakeNativeQuestionAnswerProvider) GetNativeQuestion(_ context.Context, repo string, issue int,
	questionID string) (github.NativeQuestionAuthority, error) {
	f.getRepo, f.getIssue, f.getID = repo, issue, questionID
	return f.question, nil
}

func (f *fakeNativeQuestionAnswerProvider) CreateNativeAnswer(_ context.Context, repo string, issue int,
	intent github.NativeAnswerIntent) (github.NativeAnswerResult, error) {
	f.createRepo, f.createIssue, f.intent = repo, issue, intent
	if f.createErr != nil {
		return github.NativeAnswerResult{}, f.createErr
	}
	payload, err := model.BuildAnswerPayload(f.question.Question, intent.OptionIDs, intent.Custom)
	if err != nil {
		return github.NativeAnswerResult{}, err
	}
	body, err := templates.AnswerComment(templates.AnswerOptions{ID: f.answerID, Payload: payload})
	if err != nil {
		return github.NativeAnswerResult{}, err
	}
	return github.NativeAnswerResult{
		Comment: github.Comment{
			ID: 81, HTMLURL: "https://issues.test/o/r/issues/7#issuecomment-81",
			URL: "https://issues.test/api/v3/repos/o/r/issues/comments/81", Body: body,
		},
		Question: f.question.Question, QuestionRepresentationVersion: f.question.RepresentationVersion,
		QuestionBodyDigest: f.question.BodyDigest,
	}, nil
}

func TestQuestionAnswerUsesNativeHostedAuthorityForSelectedAndCustomIntent(t *testing.T) {
	tests := []struct {
		name       string
		answerID   string
		args       []string
		wantOption string
		wantCustom string
	}{
		{
			name: "selected option", answerID: "ANSWER-7099",
			args: []string{"--select", "keep"}, wantOption: "keep",
		},
		{
			name: "custom answer", answerID: "ANSWER-7100",
			args:       []string{"--custom", "custom markdown <b>do-not-leak</b>"},
			wantCustom: "custom markdown <b>do-not-leak</b>",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(auth.ConfigDirEnv, t.TempDir())
			t.Setenv("ISSUE_SPEC_TOKEN", "native-question-token")
			profile := auth.Profile{
				Name: "native-question", Kind: auth.ProfileKindHosted, Hostname: "issues.test",
				APIURL: "https://issues.test/api/v3", NativeAPIURL: "https://issues.test/api/v1",
				WebURL: "https://issues.test", ServerInstanceID: "native-question-instance",
			}
			if err := auth.SaveProfile(profile, false); err != nil {
				t.Fatal(err)
			}
			snapshot := model.QuestionSnapshot{
				ID: "QUESTION-701", Question: "Which behavior?", Blocking: true,
				DefaultAssumption: "Keep.", IssueURL: "https://issues.test/o/r/issues/7",
				SourceURL: "https://issues.test/o/r/issues/7#issuecomment-70",
				ChoiceModel: model.ChoiceModel{
					Version: model.ChoiceModelVersion, Mode: model.ChoiceModeSingle, AllowCustom: true,
					Options: []model.ChoiceOption{{ID: "keep", Label: "Keep"}},
				},
			}
			provider := &fakeNativeQuestionAnswerProvider{
				question: github.NativeQuestionAuthority{
					Question: snapshot, RepresentationVersion: 3,
					BodyDigest: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
				},
				answerID: test.answerID,
			}
			var out, errOut bytes.Buffer
			app := newApp(strings.NewReader(""), &out, &errOut)
			app.profileName = profile.Name
			app.selectGitHubBackend = func(context.Context, string) (auth.GitHubBackendSelection, error) {
				t.Fatal("self-hosted question answer probed gh")
				return auth.GitHubBackendSelection{}, nil
			}
			app.newGitHubBackend = func(context.Context, auth.GitHubBackendSelection) (github.Backend, error) {
				t.Fatal("self-hosted question answer constructed the compatibility backend")
				return nil, nil
			}
			app.newNativeQuestionAnswerProvider = func(got auth.Profile, token string) (github.NativeQuestionAnswerOperations, error) {
				if got != profile || token != "native-question-token" {
					t.Fatalf("profile=%+v token=%q", got, token)
				}
				return provider, nil
			}
			args := []string{
				"--repo", "o/r", "--issue", "7", "--id", "ANSWER-7001",
				"--question-id", "QUESTION-701", "--json",
			}
			args = append(args, test.args...)
			if code := app.runQuestionAnswer(t.Context(), args); code != 0 || errOut.Len() != 0 {
				t.Fatalf("code=%d stderr=%s", code, errOut.String())
			}
			if provider.getRepo != "o/r" || provider.getIssue != 7 || provider.getID != "QUESTION-701" ||
				provider.createRepo != "o/r" || provider.createIssue != 7 ||
				provider.intent.QuestionID != "QUESTION-701" ||
				provider.intent.QuestionDigest != provider.question.BodyDigest ||
				provider.intent.Custom != test.wantCustom {
				t.Fatalf("provider=%+v", provider)
			}
			if test.wantOption == "" {
				if len(provider.intent.OptionIDs) != 0 {
					t.Fatalf("option ids=%v", provider.intent.OptionIDs)
				}
			} else if len(provider.intent.OptionIDs) != 1 || provider.intent.OptionIDs[0] != test.wantOption {
				t.Fatalf("option ids=%v", provider.intent.OptionIDs)
			}
			var result map[string]any
			if err := json.Unmarshal(out.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result["id"] != test.answerID || result["requested_id"] != "ANSWER-7001" ||
				result["question_id"] != "QUESTION-701" || result["comment_id"] != float64(81) {
				t.Fatalf("result=%#v", result)
			}
			output := out.String()
			for _, forbidden := range []string{
				"native-question-token", test.wantCustom, test.wantOption, snapshot.Question,
			} {
				if forbidden != "" && strings.Contains(output, forbidden) {
					t.Fatalf("output exposed %q: %s", forbidden, output)
				}
			}
		})
	}
}

func TestQuestionAnswerNativeErrorsDoNotExposeIntentOrToken(t *testing.T) {
	t.Setenv(auth.ConfigDirEnv, t.TempDir())
	t.Setenv("ISSUE_SPEC_TOKEN", "native-question-token")
	profile := auth.Profile{
		Name: "native-question-error", Kind: auth.ProfileKindHosted, Hostname: "issues.test",
		APIURL: "https://issues.test/api/v3", NativeAPIURL: "https://issues.test/api/v1",
		WebURL: "https://issues.test", ServerInstanceID: "native-question-error-instance",
	}
	if err := auth.SaveProfile(profile, false); err != nil {
		t.Fatal(err)
	}
	snapshot := model.QuestionSnapshot{
		ID: "QUESTION-701", Question: "Which behavior?", IssueURL: "https://issues.test/o/r/issues/7",
		SourceURL: "https://issues.test/o/r/issues/7#issuecomment-70",
		ChoiceModel: model.ChoiceModel{
			Version: model.ChoiceModelVersion, Mode: model.ChoiceModeSingle, AllowCustom: true,
			Options: []model.ChoiceOption{{ID: "keep-secret", Label: "Keep"}},
		},
	}
	provider := &fakeNativeQuestionAnswerProvider{
		question: github.NativeQuestionAuthority{
			Question: snapshot, RepresentationVersion: 3,
			BodyDigest: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
		},
		createErr: errors.New("native-question-token keep-secret leaked-custom"),
	}
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.profileName = profile.Name
	app.newNativeQuestionAnswerProvider = func(auth.Profile, string) (github.NativeQuestionAnswerOperations, error) {
		return provider, nil
	}
	code := app.runQuestionAnswer(t.Context(), []string{
		"--repo", "o/r", "--issue", "7", "--id", "ANSWER-7001", "--question-id", "QUESTION-701",
		"--custom", "leaked-custom", "--json",
	})
	if code != 1 || out.Len() != 0 || !strings.Contains(errOut.String(), "native request failed") {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, out.String(), errOut.String())
	}
	for _, forbidden := range []string{"native-question-token", "keep-secret", "leaked-custom"} {
		if strings.Contains(errOut.String(), forbidden) {
			t.Fatalf("stderr exposed %q: %s", forbidden, errOut.String())
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
