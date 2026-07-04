package jobs

import (
	"context"
	"sort"
	"testing"

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
			30: {Number: 30, HTMLURL: "https://github.com/o/r/issues/30", URL: "https://api.github.com/repos/o/r/issues/30", Title: "implement", State: "open", Body: "Design Issue: https://github.com/o/r/issues/25"},
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
