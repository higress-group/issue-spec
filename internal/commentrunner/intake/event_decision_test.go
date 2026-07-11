package intake

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/commentrunner"
	"github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/github"
)

func TestEventDecisionAppliesJobLocallyAndIsIdempotent(t *testing.T) {
	now := time.Now().UTC()
	comment := eventComment(71, 12, "alice", "/new verify event path", now)
	decision, err := DecideAuthoritativeComment(t.Context(), allowedEventBackend(), eventConfig(),
		commentrunner.AuthorizationPolicy{RunnerLogin: "runner", AllowedUsers: []string{"alice"}}, state.NewState(), comment, now)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Outcome != state.DeliveryOutcomeJob || decision.Job.TriggerCommentID != 71 || decision.Job.Repo != "owner/repo" {
		t.Fatalf("decision=%+v", decision)
	}
	current := state.NewState()
	if err := decision.Apply(&current); err != nil {
		t.Fatal(err)
	}
	if err := decision.Apply(&current); err != nil {
		t.Fatalf("duplicate apply: %v", err)
	}
	if len(current.Jobs) != 1 {
		t.Fatalf("jobs=%+v", current.Jobs)
	}
	if err := decision.ValidateLink(current, "owner/repo", 12, 71); err != nil {
		t.Fatal(err)
	}
}

func TestEventDecisionRejectsBeforeRemoteWriteAndPersistsSyntheticRecord(t *testing.T) {
	now := time.Now().UTC()
	backend := allowedEventBackend()
	backend.permission = "read"
	decision, err := DecideAuthoritativeComment(t.Context(), backend, eventConfig(),
		commentrunner.AuthorizationPolicy{RunnerLogin: "runner", AllowedUsers: []string{"alice"}}, state.NewState(),
		eventComment(72, 12, "alice", "/new denied", now), now)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Outcome != state.DeliveryOutcomeRejected || decision.Rejection.IdempotencyKey == "" {
		t.Fatalf("decision=%+v", decision)
	}
	current := state.NewState()
	if err := decision.Apply(&current); err != nil {
		t.Fatal(err)
	}
	if len(current.StatusWritebacks) != 1 || len(current.Jobs) != 0 {
		t.Fatalf("rejection state=%+v", current)
	}
	request, ok := decision.RejectionWritebackRequest()
	if !ok || request.Job.TriggerCommentID != 72 || request.Phase != "command-unauthorized" {
		t.Fatalf("writeback request=%+v ok=%v", request, ok)
	}
}

func TestEventDecisionFailsClosedOnWrongExistingLink(t *testing.T) {
	now := time.Now().UTC()
	decision, err := DecideAuthoritativeComment(t.Context(), allowedEventBackend(), eventConfig(),
		commentrunner.AuthorizationPolicy{RunnerLogin: "runner", AllowedUsers: []string{"alice"}}, state.NewState(),
		eventComment(73, 12, "alice", "/new one", now), now)
	if err != nil {
		t.Fatal(err)
	}
	current := state.NewState()
	wrong := decision.Job
	wrong.IssueNumber = 99
	if err := current.UpsertJob(wrong); err != nil {
		t.Fatal(err)
	}
	if err := decision.Apply(&current); err == nil || !strings.Contains(err.Error(), "linkage") {
		t.Fatalf("wrong-link apply error=%v", err)
	}
	if err := decision.ValidateLink(current, "owner/repo", 12, 73); err == nil {
		t.Fatal("wrong linked job validated")
	}
}

func TestEventDecisionIgnoredHasNoStateMutation(t *testing.T) {
	now := time.Now().UTC()
	decision, err := DecideAuthoritativeComment(t.Context(), allowedEventBackend(), eventConfig(),
		commentrunner.AuthorizationPolicy{RunnerLogin: "runner"}, state.NewState(),
		eventComment(74, 12, "alice", "ordinary discussion", now), now)
	if err != nil || decision.Outcome != state.DeliveryOutcomeIgnored {
		t.Fatalf("decision=%+v err=%v", decision, err)
	}
	current := state.NewState()
	if err := decision.Apply(&current); err != nil || len(current.Jobs) != 0 || len(current.StatusWritebacks) != 0 {
		t.Fatalf("ignored mutation state=%+v err=%v", current, err)
	}
}

func eventConfig() commentrunner.Config {
	return commentrunner.Config{Hostname: "issues.test", Repositories: []string{"owner/repo"}, RunnerIdentity: "runner",
		CancellationEnabled: true, Agent: commentrunner.AgentConfig{Kind: commentrunner.AgentCodex}}
}

func eventComment(id int64, issue int, login, body string, updated time.Time) github.Comment {
	return github.Comment{ID: id, URL: "https://issues.test/repos/owner/repo/issues/comments/71",
		HTMLURL: "https://issues.test/owner/repo/issues/12#issuecomment-71", IssueNumber: issue, Body: body,
		User: &github.User{Login: login}, CreatedAt: updated.Add(-time.Minute), UpdatedAt: updated}
}

type eventPermissionBackend struct {
	runner     string
	permission string
}

func allowedEventBackend() *eventPermissionBackend {
	return &eventPermissionBackend{runner: "runner", permission: "write"}
}

func (b *eventPermissionBackend) GetUser(context.Context) (github.User, []string, error) {
	return github.User{Login: b.runner}, nil, nil
}

func (b *eventPermissionBackend) GetCollaboratorPermission(context.Context, string, string) (github.CollaboratorPermissionResult, error) {
	return github.CollaboratorPermissionResult{Permission: github.CollaboratorPermission{Permission: b.permission},
		CanWrite: b.permission == "write"}, nil
}
