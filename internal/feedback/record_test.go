package feedback

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/github"
)

const (
	feedbackTestRepo        = "example/project"
	feedbackTestIssue       = 42
	feedbackTestPolicyIssue = 17
	feedbackTestReporterID  = 42
)

func TestGitHubPromotionRechecksPolicyAndEvidence(t *testing.T) {
	for _, tc := range []struct {
		name       string
		change     func(*recordTestBackend)
		wantStatus string
		wantCount  int
		wantReason string
		wantWrites int
	}{
		{
			name: "evidence withdrawn",
			change: func(backend *recordTestBackend) {
				backend.comments = backend.comments[1:]
			},
			wantStatus: "below_threshold",
			wantCount:  2,
		},
		{
			name: "published policy Issue edited",
			change: func(backend *recordTestBackend) {
				backend.policyBody = `{"mode":"suggest"}`
			},
			wantStatus: "pending",
			wantCount:  3,
			wantReason: "differs from the published revision",
		},
		{
			name:       "policy and evidence still qualify",
			wantStatus: "applied",
			wantCount:  3,
			wantWrites: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend, event := newRecordTestBackend(t)
			backend.beforePolicyRecheck = tc.change

			result, err := Record(t.Context(), backend, feedbackTestRepo, feedbackTestIssue, event, RecordOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if result.Promotion.Status != tc.wantStatus || result.ConfirmedCount != tc.wantCount ||
				backend.labelWrites != tc.wantWrites || !strings.Contains(result.Promotion.Reason, tc.wantReason) {
				t.Fatalf("result = %+v, label writes = %d", result, backend.labelWrites)
			}
			if result.EventStatus != "recorded" || result.CommentID != 3 || !result.Confirmed ||
				len(backend.comments) != tc.wantCount || backend.comments[len(backend.comments)-1].ID != result.CommentID {
				t.Fatalf("the third event was not preserved: result=%+v comments=%+v", result, backend.comments)
			}
		})
	}
}

func TestAutomaticPromotionPreviewChecksTargetLabel(t *testing.T) {
	for _, tc := range []struct {
		name        string
		selfHosted  bool
		labelExists bool
		wantStatus  string
		wantReason  string
	}{
		{name: "GitHub available", labelExists: true, wantStatus: "would_promote"},
		{name: "GitHub missing", wantStatus: "pending", wantReason: "configured priority label unavailable: repository label not found"},
		{name: "self-hosted available", selfHosted: true, labelExists: true, wantStatus: "would_promote"},
		{name: "self-hosted missing", selfHosted: true, wantStatus: "pending", wantReason: "configured priority label unavailable: repository label not found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend, event := newRecordTestBackend(t)
			backend.targetLabelExists = tc.labelExists

			preview, err := Record(t.Context(), backend, feedbackTestRepo, feedbackTestIssue, event, RecordOptions{
				DryRun: true, SelfHosted: tc.selfHosted,
			})
			if err != nil {
				t.Fatal(err)
			}
			if preview.EventStatus != "would_record" || preview.ConfirmedCount != 3 ||
				preview.Promotion.Status != tc.wantStatus || preview.Promotion.Label != "priority/high" ||
				preview.Promotion.Reason != tc.wantReason {
				t.Fatalf("preview = %+v", preview)
			}
			if len(backend.comments) != 2 || backend.labelWrites != 0 {
				t.Fatalf("preview changed provider state: comments=%d label writes=%d", len(backend.comments), backend.labelWrites)
			}
		})
	}
}

type recordTestBackend struct {
	Backend
	policyBody          string
	policyRef           string
	policyReads         int
	beforePolicyRecheck func(*recordTestBackend)
	comments            []github.Comment
	targetLabelExists   bool
	labelWrites         int
}

func newRecordTestBackend(t *testing.T) (*recordTestBackend, Event) {
	t.Helper()
	backend := &recordTestBackend{
		policyBody:        `{"mode":"auto","trusted_reporter_ids":["42"]}`,
		targetLabelExists: true,
	}
	_, reference, err := parsePolicyIssue(github.Issue{Body: backend.policyBody}, feedbackTestPolicyIssue)
	if err != nil {
		t.Fatal(err)
	}
	backend.policyRef = reference

	var event Event
	for n, name := range []string{"release-0.6.2", "release-0.6.3", "synthetic-third-release"} {
		data, err := os.ReadFile("../../testdata/workflow-feedback/" + name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		event, err = ParseEvent(string(data))
		if err != nil {
			t.Fatal(err)
		}
		if n == 2 {
			break
		}
		body, err := event.commentBody()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := backend.CreateComment(t.Context(), feedbackTestRepo, feedbackTestIssue, body); err != nil {
			t.Fatal(err)
		}
	}
	return backend, event
}

func (b *recordTestBackend) GetRepositoryLabel(_ context.Context, _, name string) (github.Label, error) {
	if name == PolicyLabel {
		b.policyReads++
		if b.policyReads == 2 && b.beforePolicyRecheck != nil {
			b.beforePolicyRecheck(b)
		}
		return github.Label{Name: name, Description: b.policyRef}, nil
	}
	if name == "priority/high" && b.targetLabelExists {
		return github.Label{Name: name}, nil
	}
	return github.Label{}, github.ErrRepositoryLabelNotFound
}

func (b *recordTestBackend) GetIssue(_ context.Context, _ string, number int) (github.Issue, error) {
	issue := github.Issue{Number: number, State: "open"}
	if number == feedbackTestPolicyIssue {
		issue.Body = b.policyBody
	}
	return issue, nil
}

func (b *recordTestBackend) GetUser(context.Context) (github.User, []string, error) {
	return github.User{ID: feedbackTestReporterID, Login: "reporter"}, nil, nil
}

func (b *recordTestBackend) ListIssueComments(context.Context, string, int) ([]github.Comment, error) {
	return b.comments, nil
}

func (b *recordTestBackend) CreateComment(_ context.Context, _ string, _ int, body string) (github.Comment, error) {
	comment := github.Comment{
		ID: int64(len(b.comments) + 1), Body: body, User: &github.User{ID: feedbackTestReporterID, Login: "reporter"},
	}
	b.comments = append(b.comments, comment)
	return comment, nil
}

func (b *recordTestBackend) AddIssueLabels(_ context.Context, _ string, _ int, names []string) ([]github.Label, error) {
	b.labelWrites++
	return []github.Label{{Name: names[0]}}, nil
}
