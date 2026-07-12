package jobs

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/capability"
	"github.com/higress-group/issue-spec/internal/commentrunner/credentials"
	"github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/server/models"
)

type capabilityPreflightFunc func(context.Context, credentials.PreflightRequest) capability.Report

func (f capabilityPreflightFunc) Probe(ctx context.Context, request credentials.PreflightRequest) capability.Report {
	return f(ctx, request)
}

type countingCredentialBroker struct{ acquires atomic.Int32 }

func (b *countingCredentialBroker) Acquire(context.Context, credentials.AcquireRequest) (*credentials.Lease, error) {
	b.acquires.Add(1)
	return nil, nil
}
func (*countingCredentialBroker) RevokeJob(context.Context, models.RepoScope, string) error {
	return nil
}

func TestStrictCapabilityFailurePrecedesLeaseWorkspaceAndWorker(t *testing.T) {
	store := newMemoryStore()
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	seedQueuedJob(t, store, state.Job{ID: "job-strict-preflight", Repo: "o/r", IssueNumber: 30,
		CoordinatorKind: "codex", SessionCreatorLogin: "alice", TriggeringUserLogin: "alice", TriggerCommentID: 701,
		CommandID: "cmd-strict", CommandName: "new", CommandPrompt: "review the PR", CommandIdempotencyKey: "cmd-key-strict",
		StatusWritebackKey: "status-strict", Status: state.StatusQueued, CreatedAt: now,
		FirstObservedComment: state.SeenComment{Repo: "o/r", IssueNumber: 30, CommentID: 701,
			HTMLURL: "https://github.com/o/r/issues/30#issuecomment-701", AuthorLogin: "alice",
			FirstObservedUpdatedAt: now, FirstObservedBodyHash: "sha256:strict"}})
	workspaces := &fakeWorkspaces{binding: testBinding("ws-must-not-exist")}
	coordinator := &fakeCoordinator{}
	dispatcher := testDispatcher(store, workspaces, coordinator, &fakeWriteback{}, now)
	broker := &countingCredentialBroker{}
	dispatcher.CredentialBroker = broker
	dispatcher.CredentialScopes = map[string]models.RepoScope{"o/r": {OrgID: uuid.New(), RepoID: uuid.New()}}
	dispatcher.CapabilityHost = "github.com"
	dispatcher.RequiredOperations = []capability.Operation{capability.OperationIssueRead, capability.OperationPullRequestReviewWrite}
	dispatcher.CapabilityPreflight = capabilityPreflightFunc(func(_ context.Context, request credentials.PreflightRequest) capability.Report {
		report := capability.Report{Host: request.Request.Host, Repository: request.Request.Repository, Backend: "operator-issuer",
			Credential: capability.CredentialSummary{SourceClass: "delegated"}, Network: capability.NetworkSummary{Status: "reachable"},
			Operations: []capability.OperationResult{
				{Operation: capability.OperationIssueRead, Decision: capability.DecisionAllowed, Detail: "/private/token"},
				{Operation: capability.OperationPullRequestReviewWrite, Decision: capability.DecisionUnknown,
					Code: capability.FailureOperationNotProvable, Detail: "raw-provider-secret"},
			}}
		report.Finish()
		return report
	})

	result, err := dispatcher.RunNext(t.Context())
	if err == nil || result.Status != state.StatusFailed || !strings.Contains(err.Error(), "pr.review.write=unknown/operation_not_provable") {
		t.Fatalf("RunNext result=%+v err=%v", result, err)
	}
	if broker.acquires.Load() != 0 || workspaces.prepareNewCalled || workspaces.resolveResumeCalled ||
		len(coordinator.newPrompts) != 0 || len(coordinator.resumePrompts) != 0 {
		t.Fatalf("preflight was late: acquire=%d workspace=%+v coordinator=%+v", broker.acquires.Load(), workspaces, coordinator)
	}
	job := loadState(t, store).Jobs["job-strict-preflight"]
	if job.CapabilityPreflight == nil || job.CapabilityPreflight.OK || job.CapabilityPreflight.Operations[1].Detail != "" {
		t.Fatalf("persisted preflight = %+v", job.CapabilityPreflight)
	}
	encoded, encodeErr := json.Marshal(job)
	if encodeErr != nil || strings.Contains(string(encoded), "/private/token") || strings.Contains(string(encoded), "raw-provider-secret") {
		t.Fatalf("secret-bearing provider diagnostics persisted: %s err=%v", encoded, encodeErr)
	}
}

func TestStrictCapabilityMissingIssuerFailsClosedBeforeWorkspace(t *testing.T) {
	store := newMemoryStore()
	now := time.Now().UTC()
	seedQueuedJob(t, store, state.Job{ID: "job-missing-issuer", Repo: "o/r", IssueNumber: 30,
		CoordinatorKind: "codex", TriggeringUserLogin: "alice", TriggerCommentID: 702, CommandID: "cmd-missing",
		CommandName: "new", CommandPrompt: "implement", CommandIdempotencyKey: "cmd-key-missing", Status: state.StatusQueued,
		CreatedAt: now, FirstObservedComment: state.SeenComment{Repo: "o/r", IssueNumber: 30, CommentID: 702,
			AuthorLogin: "alice", FirstObservedUpdatedAt: now, FirstObservedBodyHash: "sha256:missing"}})
	workspaces := &fakeWorkspaces{binding: testBinding("ws-missing")}
	dispatcher := testDispatcher(store, workspaces, &fakeCoordinator{}, &fakeWriteback{}, now)
	dispatcher.CapabilityHost = "github.com"
	dispatcher.RequiredOperations = []capability.Operation{capability.OperationIssueRead}
	dispatcher.CredentialScopes = map[string]models.RepoScope{"o/r": {OrgID: uuid.New(), RepoID: uuid.New()}}
	if _, err := dispatcher.RunNext(t.Context()); err == nil || workspaces.prepareNewCalled {
		t.Fatalf("missing issuer did not fail before workspace: err=%v workspace=%+v", err, workspaces)
	}
}
