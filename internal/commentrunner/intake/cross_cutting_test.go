package intake

import (
	"context"
	"net/http"
	"testing"

	"github.com/higress-group/issue-spec/internal/commentrunner"
	crstate "github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/commentrunner/testkit"
	"github.com/higress-group/issue-spec/internal/github"
)

func TestRunOnceDeduplicatesCancelAcrossNotificationAndFallback(t *testing.T) {
	comment := testkit.CommandComment(701, 30, "alice", "/cancel ps-1")
	backend := &testkit.RunnerBackend{
		User:        github.User{Login: "bot"},
		Permissions: map[string]string{"alice": "write"},
		Notifications: github.NotificationListResult{
			Notifications: []github.Notification{testkit.Notification(30)},
			Metadata:      testkit.Metadata(http.StatusOK, `"notes-cancel"`, 60),
		},
		IssueComments: map[int]github.IssueCommentsResult{
			30: {Comments: []github.Comment{comment}, Metadata: testkit.Metadata(http.StatusOK, `"thread-cancel"`, 0)},
		},
		RepositoryComments: github.IssueCommentsResult{Comments: []github.Comment{comment}, Metadata: testkit.Metadata(http.StatusOK, `"repo-cancel"`, 0)},
	}
	st := crstate.NewState()
	if err := st.UpsertPublicSession(crstate.PublicSession{
		Repo:            "o/r",
		PublicSessionID: "ps-1",
		IssueNumber:     30,
		AcpxRecordID:    "rec-1",
		Status:          crstate.StatusRunning,
	}); err != nil {
		t.Fatal(err)
	}
	store := testkit.NewMemoryStore(st)

	result, err := RunOnce(context.Background(), testkit.RunnerConfig(), backend, store, Options{
		Clock: testkit.Clock{},
		AuthorizationPolicy: commentrunner.AuthorizationPolicy{
			RunnerLogin:  "bot",
			AllowedUsers: []string{"alice"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || len(result.Cancellations) != 1 || !result.Cancellations[0].Created {
		t.Fatalf("cancel candidate not queued once: cancellations=%+v diagnostics=%+v", result.Cancellations, result.Diagnostics)
	}
	if !hasStatus(result.Commands, CommandStatusCancelQueued) || !hasStatus(result.Commands, CommandStatusDuplicate) {
		t.Fatalf("expected queued and duplicate reports: %+v", result.Commands)
	}
	snapshot := store.Snapshot()
	if len(snapshot.Cancellations) != 1 {
		t.Fatalf("stored cancellations = %d, want 1: %+v", len(snapshot.Cancellations), snapshot.Cancellations)
	}
	cancel := firstCancellation(snapshot.Cancellations)
	if cancel.IdempotencyKey == "" || snapshot.Idempotency.CancelRequests[cancel.IdempotencyKey] != cancel.ID {
		t.Fatalf("cancel idempotency not indexed: cancel=%+v indexes=%+v", cancel, snapshot.Idempotency.CancelRequests)
	}
}
