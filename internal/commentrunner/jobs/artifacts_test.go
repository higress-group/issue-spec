package jobs

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	runnercontext "github.com/higress-group/issue-spec/internal/commentrunner/context"
	"github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
)

func TestIssueSpecArtifactProviderCollectsLinkedIssueContextAndTypedDAG(t *testing.T) {
	specBody, err := model.EnsureTypedBody("SPEC", "SPEC-018", "## Requirement\n\nRunner MUST pass selected backend through all phases.", model.BodyOptions{Status: "confirmed"})
	if err != nil {
		t.Fatal(err)
	}
	taskBody, err := model.EnsureTypedBody("TASK", "TASK-018", "## Task\n\nImplement runner fixes.", model.BodyOptions{
		Status: "ready",
		Links:  map[string][]string{"Related Comments": {"https://github.com/o/r/issues/24#issuecomment-2401"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	processBody, err := model.EnsureTypedBody("PROCESS", "NATIVE-018", "## Scope\n\nFix runner backend and artifacts.", model.BodyOptions{
		Status: "ready",
		Links:  map[string][]string{"Related Comments": {"https://github.com/o/r/issues/25#issuecomment-2501"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	backend := &artifactBackend{
		issues: map[int]github.Issue{
			24: {Number: 24, HTMLURL: "https://github.com/o/r/issues/24", URL: "https://api.github.com/repos/o/r/issues/24", Title: "proposal", State: "open", Body: "Proposal body"},
			25: {Number: 25, HTMLURL: "https://github.com/o/r/issues/25", URL: "https://api.github.com/repos/o/r/issues/25", Title: "design", State: "open", Body: "Proposal Issue: #24"},
			30: {Number: 30, HTMLURL: "https://github.com/o/r/issues/30", URL: "https://api.github.com/repos/o/r/issues/30", Title: "implement", State: "open", Body: "Design Issue: https://github.com/o/r/issues/25\n\n```html-preview id=hostile version=1 title=\"#998\"\nHOSTILE_CONTEXT #999\n```\n"},
		},
		comments: map[int][]github.Comment{
			24: {{ID: 2401, HTMLURL: "https://github.com/o/r/issues/24#issuecomment-2401", URL: "https://api.github.com/repos/o/r/issues/comments/2401", IssueNumber: 24, Body: specBody}},
			25: {{ID: 2501, HTMLURL: "https://github.com/o/r/issues/25#issuecomment-2501", URL: "https://api.github.com/repos/o/r/issues/comments/2501", IssueNumber: 25, Body: taskBody}},
			30: {{ID: 3001, HTMLURL: "https://github.com/o/r/issues/30#issuecomment-3001", URL: "https://api.github.com/repos/o/r/issues/comments/3001", IssueNumber: 30, Body: processBody}},
		},
	}
	provider := &IssueSpecArtifactProvider{GitHub: backend}

	artifacts, err := provider.ArtifactsForJob(context.Background(), state.Job{Repo: "o/r", IssueNumber: 30})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, artifact := range artifacts {
		ids = append(ids, artifact.Comment.ID)
	}
	sort.Strings(ids)
	want := []string{"ISSUE-024", "ISSUE-025", "ISSUE-030", "NATIVE-018", "SPEC-018", "TASK-018"}
	if !stringSlicesEqual(ids, want) {
		t.Fatalf("artifact ids = %v, want %v", ids, want)
	}
	if len(backend.issueContextCalls) != 3 {
		t.Fatalf("issue context calls = %v", backend.issueContextCalls)
	}
	for _, artifact := range artifacts {
		if artifact.Comment.ID == "ISSUE-030" {
			if strings.Contains(artifact.Comment.Body, "HOSTILE_CONTEXT") ||
				!strings.Contains(artifact.Comment.Body, `"id":"hostile"`) {
				t.Fatalf("issue context preview was not folded explicitly: %q", artifact.Comment.Body)
			}
		}
	}
}

func TestIssueSpecArtifactProviderSelectsOnlyLatestValidEffectiveAnswer(t *testing.T) {
	questionURL := "https://github.com/o/r/issues/30#issuecomment-3001"
	questionBody := choiceQuestionBody(t, "QUESTION-001")
	snapshot, err := model.SnapshotQuestion(questionBody, questionURL)
	if err != nil {
		t.Fatal(err)
	}
	older := answerBody(t, "ANSWER-001", snapshot, "old")
	latest := answerBody(t, "ANSWER-002", snapshot, "new")
	edited := answerBody(t, "ANSWER-003", snapshot, "old")
	malformed, err := model.EnsureTypedBody("ANSWER", "ANSWER-004", "## Answer\n\n```json\n{not-json}\n```\n",
		model.BodyOptions{Status: "done", Scope: "QUESTION-001"})
	if err != nil {
		t.Fatal(err)
	}
	wrongQuestion := snapshot
	wrongQuestion.SourceURL = "https://github.com/o/r/issues/30#issuecomment-3999"
	wrongSource := answerBody(t, "ANSWER-005", wrongQuestion, "old")
	wrongQuestionScope := answerBodyWithScope(t, "ANSWER-006", snapshot, "old", "QUESTION-999")
	unrelatedQuestion := snapshot
	unrelatedQuestion.ID = "QUESTION-999"
	unrelatedQuestion.IssueURL = "https://github.com/o/r/issues/998"
	unrelatedQuestion.SourceURL = "https://github.com/o/r/issues/998#issuecomment-9981"
	unrelated := answerBody(t, "ANSWER-007", unrelatedQuestion, "old")
	specBody, err := model.EnsureTypedBody("SPEC", "SPEC-001", "## Requirement\n\nKeep non-ANSWER artifacts.",
		model.BodyOptions{Status: "confirmed"})
	if err != nil {
		t.Fatal(err)
	}

	base := time.Date(2026, time.July, 24, 9, 0, 0, 0, time.UTC)
	actor := &github.User{Login: "reviewer"}
	backend := &artifactBackend{
		issues: map[int]github.Issue{
			30: {Number: 30, HTMLURL: "https://github.com/o/r/issues/30", URL: "https://api.github.com/repos/o/r/issues/30", Title: "implement", State: "open"},
		},
		comments: map[int][]github.Comment{
			30: {
				{ID: 3001, HTMLURL: questionURL, URL: "https://api.github.com/repos/o/r/issues/comments/3001", IssueNumber: 30, Body: questionBody},
				{ID: 3002, HTMLURL: "https://github.com/o/r/issues/30#issuecomment-3002", URL: "https://api.github.com/repos/o/r/issues/comments/3002", IssueNumber: 30, Body: specBody},
				{ID: 3010, HTMLURL: "https://github.com/o/r/issues/30#issuecomment-3010", IssueNumber: 30, Body: older, User: actor, CreatedAt: base, UpdatedAt: base},
				{ID: 3011, HTMLURL: "https://github.com/o/r/issues/30#issuecomment-3011", IssueNumber: 30, Body: latest, User: actor, CreatedAt: base.Add(time.Minute), UpdatedAt: base.Add(time.Minute)},
				{ID: 3012, HTMLURL: "https://github.com/o/r/issues/30#issuecomment-3012", IssueNumber: 30, Body: edited, User: actor, CreatedAt: base.Add(2 * time.Minute), UpdatedAt: base.Add(3 * time.Minute)},
				{ID: 3013, HTMLURL: "https://github.com/o/r/issues/30#issuecomment-3013", IssueNumber: 30, Body: malformed, User: actor, CreatedAt: base.Add(4 * time.Minute), UpdatedAt: base.Add(4 * time.Minute)},
				{ID: 3014, HTMLURL: "https://github.com/o/r/issues/30#issuecomment-3014", IssueNumber: 30, Body: wrongSource, User: actor, CreatedAt: base.Add(5 * time.Minute), UpdatedAt: base.Add(5 * time.Minute)},
				{ID: 3015, HTMLURL: "https://github.com/o/r/issues/30#issuecomment-3015", IssueNumber: 30, Body: wrongQuestionScope, User: actor, CreatedAt: base.Add(6 * time.Minute), UpdatedAt: base.Add(6 * time.Minute)},
				{ID: 3016, HTMLURL: "https://github.com/o/r/issues/30#issuecomment-3016", IssueNumber: 30, Body: unrelated, User: actor, CreatedAt: base.Add(7 * time.Minute), UpdatedAt: base.Add(7 * time.Minute)},
			},
		},
	}

	artifacts, err := (&IssueSpecArtifactProvider{GitHub: backend}).ArtifactsForJob(
		context.Background(), state.Job{Repo: "o/r", IssueNumber: 30},
	)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, artifact := range artifacts {
		ids = append(ids, artifact.Comment.ID)
	}
	sort.Strings(ids)
	want := []string{"ANSWER-002", "ISSUE-030", "QUESTION-001", "SPEC-001"}
	if !stringSlicesEqual(ids, want) {
		t.Fatalf("artifact ids = %v, want %v", ids, want)
	}
	if !stringSlicesEqual(intStrings(backend.issueContextCalls), []string{"30"}) {
		t.Fatalf("ANSWER history expanded issue graph: calls=%v", backend.issueContextCalls)
	}
	if got := len(backend.comments[30]); got != 9 {
		t.Fatalf("provider ANSWER history changed: comments=%d", got)
	}
}

func TestIssueSpecArtifactProviderKeepsSameQuestionIDIndependentAcrossLinkedIssues(t *testing.T) {
	question30URL := "https://github.com/o/r/issues/30#issuecomment-3001"
	question31URL := "https://github.com/o/r/issues/31#issuecomment-3101"
	question30 := choiceQuestionBody(t, "QUESTION-001")
	question31 := choiceQuestionBody(t, "QUESTION-001")
	snapshot30, err := model.SnapshotQuestion(question30, question30URL)
	if err != nil {
		t.Fatal(err)
	}
	snapshot31, err := model.SnapshotQuestion(question31, question31URL)
	if err != nil {
		t.Fatal(err)
	}
	answer30 := answerBody(t, "ANSWER-001", snapshot30, "old")
	answer31 := answerBody(t, "ANSWER-002", snapshot31, "new")
	base := time.Date(2026, time.July, 24, 10, 0, 0, 0, time.UTC)
	actor := &github.User{Login: "reviewer"}
	backend := &artifactBackend{
		issues: map[int]github.Issue{
			30: {
				Number: 30, HTMLURL: "https://github.com/o/r/issues/30",
				URL: "https://api.github.com/repos/o/r/issues/30", Title: "first source", State: "open",
				Body: "Related source: #31",
			},
			31: {
				Number: 31, HTMLURL: "https://github.com/o/r/issues/31",
				URL: "https://api.github.com/repos/o/r/issues/31", Title: "second source", State: "open",
			},
		},
		comments: map[int][]github.Comment{
			30: {
				{ID: 3001, HTMLURL: question30URL, IssueNumber: 30, Body: question30},
				{ID: 3010, HTMLURL: "https://github.com/o/r/issues/30#issuecomment-3010", IssueNumber: 30, Body: answer30, User: actor, CreatedAt: base, UpdatedAt: base},
			},
			31: {
				{ID: 3101, HTMLURL: question31URL, IssueNumber: 31, Body: question31},
				{ID: 3110, HTMLURL: "https://github.com/o/r/issues/31#issuecomment-3110", IssueNumber: 31, Body: answer31, User: actor, CreatedAt: base.Add(time.Minute), UpdatedAt: base.Add(time.Minute)},
			},
		},
	}

	artifacts, err := (&IssueSpecArtifactProvider{GitHub: backend}).ArtifactsForJob(
		context.Background(), state.Job{Repo: "o/r", IssueNumber: 30},
	)
	if err != nil {
		t.Fatal(err)
	}
	answerByIssue := map[int]string{}
	questionCountByIssue := map[int]int{}
	for _, artifact := range artifacts {
		switch artifact.Comment.Type {
		case "ANSWER":
			answerByIssue[artifact.Issue] = artifact.Comment.ID
		case "QUESTION":
			questionCountByIssue[artifact.Issue]++
		}
	}
	if answerByIssue[30] != "ANSWER-001" || answerByIssue[31] != "ANSWER-002" {
		t.Fatalf("effective answers by source issue = %v", answerByIssue)
	}
	if questionCountByIssue[30] != 1 || questionCountByIssue[31] != 1 {
		t.Fatalf("QUESTION context by source issue = %v", questionCountByIssue)
	}

	command := runnercontext.CommandCandidate{
		Authorized:       true,
		Verb:             runnercontext.CommandNew,
		Repo:             "o/r",
		Issue:            30,
		TriggerCommentID: 3999,
		Commenter:        "reviewer",
		Prompt:           "continue the linked issue workflow",
	}
	for _, test := range []struct {
		name          string
		command       runnercontext.CommandCandidate
		referenceOnly bool
	}{
		{name: "new", command: command},
		{name: "resume", command: func() runnercontext.CommandCandidate {
			resume := command
			resume.Verb = runnercontext.CommandResume
			resume.PublicSessionID = "public-resume"
			return resume
		}(), referenceOnly: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			bundle, err := runnercontext.BuildBundle(runnercontext.BuildOptions{
				Command:                test.command,
				Artifacts:              artifacts,
				ReferenceOnlyArtifacts: test.referenceOnly,
			})
			if err != nil {
				t.Fatal(err)
			}
			var decisions []runnercontext.BundleArtifact
			for _, artifact := range bundle.Artifacts {
				if artifact.Type == "QUESTION" || artifact.Type == "ANSWER" {
					decisions = append(decisions, artifact)
				}
			}
			want := []struct {
				issue        int
				artifactType string
				id           string
			}{
				{issue: 30, artifactType: "QUESTION", id: "QUESTION-001"},
				{issue: 30, artifactType: "ANSWER", id: "ANSWER-001"},
				{issue: 31, artifactType: "QUESTION", id: "QUESTION-001"},
				{issue: 31, artifactType: "ANSWER", id: "ANSWER-002"},
			}
			if len(decisions) != len(want) {
				t.Fatalf("provider-to-bundle decisions = %+v", decisions)
			}
			for index, expected := range want {
				got := decisions[index]
				if got.Issue != expected.issue || got.Type != expected.artifactType || got.ID != expected.id {
					t.Fatalf("decision[%d] = %+v, want issue=%d type=%s id=%s",
						index, got, expected.issue, expected.artifactType, expected.id)
				}
				if test.referenceOnly && (!got.ReferenceOnly || got.Content != "") {
					t.Fatalf("resume decision[%d] inlined content: %+v", index, got)
				}
			}
			if !test.referenceOnly &&
				(!strings.Contains(decisions[1].Content, `"id":"old"`) ||
					!strings.Contains(decisions[3].Content, `"id":"new"`)) {
				t.Fatalf("provider-to-bundle answers crossed source issues: %+v", decisions)
			}
		})
	}
}

func choiceQuestionBody(t *testing.T, id string) string {
	t.Helper()
	choice := model.ChoiceModel{
		Version: model.ChoiceModelVersion,
		Mode:    model.ChoiceModeSingle,
		Options: []model.ChoiceOption{
			{ID: "old", Label: "Older choice"},
			{ID: "new", Label: "Newer choice"},
		},
	}
	raw, err := model.CanonicalJSON(choice)
	if err != nil {
		t.Fatal(err)
	}
	body, err := model.EnsureTypedBody("QUESTION", id, "## Question\n\nChoose one.\n\n## Blocking\n\ntrue\n\n## Default Assumption\n\nOlder choice\n\n## Choice Model\n\n```json\n"+raw+"\n```\n",
		model.BodyOptions{Status: "blocked"})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func answerBody(t *testing.T, id string, snapshot model.QuestionSnapshot, optionID string) string {
	t.Helper()
	return answerBodyWithScope(t, id, snapshot, optionID, snapshot.ID)
}

func answerBodyWithScope(t *testing.T, id string, snapshot model.QuestionSnapshot, optionID, scope string) string {
	t.Helper()
	payload, err := model.BuildAnswerPayload(snapshot, []string{optionID}, "")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := model.CanonicalJSON(payload)
	if err != nil {
		t.Fatal(err)
	}
	body, err := model.EnsureTypedBody("ANSWER", id, "## Answer\n\n```json\n"+raw+"\n```\n",
		model.BodyOptions{Status: "done", Scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func intStrings(values []int) []string {
	result := make([]string, len(values))
	for i, value := range values {
		result[i] = strconv.Itoa(value)
	}
	return result
}

type artifactBackend struct {
	issues            map[int]github.Issue
	comments          map[int][]github.Comment
	issueContextCalls []int
}

func (b *artifactBackend) PollNotifications(context.Context, github.NotificationListOptions) (github.NotificationListResult, error) {
	return github.NotificationListResult{}, nil
}

func (b *artifactBackend) GetRepositorySubscription(context.Context, string) (github.RepositorySubscriptionResult, error) {
	return github.RepositorySubscriptionResult{}, nil
}

func (b *artifactBackend) GetIssueContext(_ context.Context, _ string, issue int, _ github.ConditionalRequest) (github.IssueContextResult, error) {
	b.issueContextCalls = append(b.issueContextCalls, issue)
	return github.IssueContextResult{Issue: b.issues[issue]}, nil
}

func (b *artifactBackend) ListIssueCommentsPage(_ context.Context, _ string, issue int, _ github.CommentListOptions) (github.IssueCommentsResult, error) {
	return github.IssueCommentsResult{Comments: b.comments[issue]}, nil
}

func (b *artifactBackend) ListRepositoryIssueCommentsPage(context.Context, string, github.CommentListOptions) (github.IssueCommentsResult, error) {
	return github.IssueCommentsResult{}, nil
}

func (b *artifactBackend) ListCommentReactionsPage(context.Context, string, int64, github.RunnerPageOptions) (github.CommentReactionsResult, error) {
	return github.CommentReactionsResult{}, nil
}

func (b *artifactBackend) GetCollaboratorPermission(context.Context, string, string) (github.CollaboratorPermissionResult, error) {
	return github.CollaboratorPermissionResult{}, nil
}

func (b *artifactBackend) CreateRunnerComment(context.Context, string, int, string) (github.RunnerCommentResult, error) {
	return github.RunnerCommentResult{}, nil
}

func (b *artifactBackend) UpdateRunnerComment(context.Context, string, int64, string) (github.RunnerCommentResult, error) {
	return github.RunnerCommentResult{}, nil
}

func (b *artifactBackend) AddCommentReaction(context.Context, string, int64, string) (github.RunnerReactionResult, error) {
	return github.RunnerReactionResult{}, nil
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
