package delivery

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/events/outbox"
)

func TestNotificationPolicyKeepsAutomationOutOfHumanOnlyFilter(t *testing.T) {
	base := notificationPolicy{IssueActions: []string{"opened"}, IssueKinds: []string{"ordinary"}, ActorClasses: []string{"human"}}
	envelope := outbox.Envelope{EventType: "issue.created", Notification: &outbox.NotificationFacts{
		IssueKind: "ordinary", ActorClass: "automation",
	}}
	if matched, reason := matchesNotification(envelope, base); matched || reason != "actor_class_filtered" {
		t.Fatalf("human-only automation match=%v reason=%q", matched, reason)
	}
	base.ActorClasses = []string{"automation"}
	if matched, reason := matchesNotification(envelope, base); !matched || reason != "" {
		t.Fatalf("automation filter match=%v reason=%q", matched, reason)
	}
	envelope.Notification.ActorClass = ""
	if matched, reason := matchesNotification(envelope, base); matched || reason != "actor_class_filtered" {
		t.Fatalf("unknown actor provenance match=%v reason=%q", matched, reason)
	}
}

func TestRenderGitHubUsesCanonicalRepositoryIssueAndCommentURLs(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	commentID := uuid.New()
	envelope := outbox.Envelope{EventType: "issue_comment.created", Action: "created",
		Issue:   outbox.IssueIdentity{StableID: uuid.New(), Number: 21},
		Comment: &outbox.CommentRevision{StableID: commentID, NumericID: 42},
		Notification: &outbox.NotificationFacts{
			Organization: outbox.NotificationOrganization{ID: uuid.New(), Login: "ingress", DisplayName: "Ingress"},
			Repository:   outbox.NotificationRepository{ID: uuid.New(), Name: "istio", FullName: "ingress/istio"},
			Sender:       outbox.NotificationUser{ID: uuid.New(), Login: "alice"},
			Issue: outbox.NotificationIssue{ID: uuid.New(), Number: 21, State: "open", Title: "Canonical URLs",
				Author: outbox.NotificationUser{ID: uuid.New(), Login: "alice"}, CreatedAt: now, UpdatedAt: now},
			Comment: &outbox.NotificationComment{ID: commentID, NumericID: 42, Body: "body",
				Author: outbox.NotificationUser{ID: uuid.New(), Login: "alice"}, CreatedAt: now, UpdatedAt: now},
		},
	}
	body, eventName, err := renderGitHub(envelope, "https://api.issue.test", "https://issues.test")
	if err != nil {
		t.Fatal(err)
	}
	if eventName != "issue_comment" {
		t.Fatalf("event name = %q", eventName)
	}
	var payload struct {
		Issue struct {
			URL     string `json:"url"`
			HTMLURL string `json:"html_url"`
		} `json:"issue"`
		Comment struct {
			URL      string `json:"url"`
			HTMLURL  string `json:"html_url"`
			IssueURL string `json:"issue_url"`
		} `json:"comment"`
		Repository struct {
			URL       string `json:"url"`
			HTMLURL   string `json:"html_url"`
			IssuesURL string `json:"issues_url"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	checks := map[string][2]string{
		"repository API":    {payload.Repository.URL, "https://api.issue.test/repos/ingress/istio"},
		"repository Web":    {payload.Repository.HTMLURL, "https://issues.test/ingress/istio"},
		"issues template":   {payload.Repository.IssuesURL, "https://api.issue.test/repos/ingress/istio/issues{/number}"},
		"issue API":         {payload.Issue.URL, "https://api.issue.test/repos/ingress/istio/issues/21"},
		"issue Web":         {payload.Issue.HTMLURL, "https://issues.test/ingress/istio/issues/21"},
		"comment API":       {payload.Comment.URL, "https://api.issue.test/repos/ingress/istio/issues/comments/42"},
		"comment Web":       {payload.Comment.HTMLURL, "https://issues.test/ingress/istio/issues/21#issuecomment-42"},
		"comment issue API": {payload.Comment.IssueURL, "https://api.issue.test/repos/ingress/istio/issues/21"},
	}
	for name, check := range checks {
		if check[0] != check[1] {
			t.Errorf("%s = %q, want %q", name, check[0], check[1])
		}
	}
}
