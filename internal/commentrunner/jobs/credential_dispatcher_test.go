package jobs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	clientauth "github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/commentrunner/credentials"
	"github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/server/models"
)

func TestDispatcherCredentialOrderRotationSandboxAndTerminalRevoke(t *testing.T) {
	var exchangeCalls, remoteRevokes, providerRevokes, providerJobRevokes atomic.Int32
	var providerRevokeDeadline, providerJobRevokeDeadline atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer parent-secret" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		if r.Method == http.MethodPost {
			exchangeCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": uuid.New(), "token": "iss_dgt_aabbccdd_child", "expires_at": time.Now().Add(time.Minute)})
			return
		}
		if r.Method == http.MethodDelete {
			remoteRevokes.Add(1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	now := time.Date(2026, 7, 11, 1, 0, 0, 0, time.UTC)
	store := newMemoryStore()
	seedQueuedJob(t, store, state.Job{ID: "job-credential", Repo: "o/r", IssueNumber: 30, CoordinatorKind: "codex",
		SessionCreatorLogin: "alice", TriggeringUserLogin: "alice", TriggerCommentID: 501, CommandID: "cmd-credential",
		CommandName: "new", CommandPrompt: "credential lifecycle", CommandIdempotencyKey: "cmd-key-credential",
		StatusWritebackKey: "status-credential", Status: state.StatusQueued, CreatedAt: now,
		FirstObservedComment: state.SeenComment{Repo: "o/r", IssueNumber: 30, CommentID: 501,
			HTMLURL: "https://github.com/o/r/issues/30#issuecomment-501", AuthorLogin: "alice",
			FirstObservedUpdatedAt: now, FirstObservedBodyHash: "sha256:credential", StatusWritebackIdempotencyKey: "status-credential"}})
	workspaces := &fakeWorkspaces{binding: testBinding("ws-credential")}
	sandboxer := &fakeSandbox{}
	dispatcher := testDispatcher(store, workspaces,
		&fakeCoordinator{newResult: dispatchResult("ps-credential", "rec-credential", "turn-credential", completedSummary())}, &fakeWriteback{}, now)
	dispatcher.Sandbox = sandboxer
	dispatcher.PublicSessionID = func() (string, error) { return "ps-credential", nil }
	repoScope := models.RepoScope{OrgID: uuid.New(), RepoID: uuid.New()}
	profile := clientauth.Profile{Name: "runner", Kind: clientauth.ProfileKindHosted, APIURL: server.URL + "/api/v3",
		NativeAPIURL: server.URL + "/api/v1", WebURL: server.URL, ServerInstanceID: "instance-a"}
	dispatcher.CredentialBroker = &credentials.Broker{Profile: profile, Audience: "instance-a", Subject: "runner-child",
		ParentToken: "parent-secret", Materializer: credentials.Materializer{Root: t.TempDir()}, TTL: time.Minute,
		GitProvider: credentials.OperatorGitProvider{ProviderKey: "github", Host: "github.com", ExternalRepositoryID: "o/r",
			AcquireLease: func(context.Context, credentials.GitRequest) (credentials.GitProviderLease, error) {
				return credentials.GitProviderLease{Credential: credentials.GitSecret{Username: "runner", Password: "git-secret"},
					ExpiresAt: time.Now().Add(time.Minute), Revoke: func(ctx context.Context) error {
						providerRevokes.Add(1)
						_, ok := ctx.Deadline()
						providerRevokeDeadline.Store(ok)
						return nil
					}}, nil
			}, RevokeJobLease: func(ctx context.Context, _ string) error {
				providerJobRevokes.Add(1)
				_, ok := ctx.Deadline()
				providerJobRevokeDeadline.Store(ok)
				return nil
			}},
	}
	dispatcher.CredentialScopes = map[string]models.RepoScope{"o/r": repoScope}
	preGate := &recordingEvidencePreGate{}
	dispatcher.EvidencePreGate = preGate

	result, err := dispatcher.RunNext(context.Background())
	if err != nil || result.Status != state.StatusCompleted {
		t.Fatalf("RunNext = %+v, %v", result, err)
	}
	if exchangeCalls.Load() != 1 || remoteRevokes.Load() != 1 || providerRevokes.Load() != 2 || providerJobRevokes.Load() != 1 {
		t.Fatalf("exchange=%d remote_revoke=%d provider_revoke=%d provider_job_revoke=%d", exchangeCalls.Load(), remoteRevokes.Load(), providerRevokes.Load(), providerJobRevokes.Load())
	}
	if !providerRevokeDeadline.Load() || !providerJobRevokeDeadline.Load() {
		t.Fatalf("cleanup deadlines individual=%t job=%t", providerRevokeDeadline.Load(), providerJobRevokeDeadline.Load())
	}
	if preGate.calls != 1 || !preGate.credentialAvailable || preGate.request.Repo != "o/r" ||
		preGate.request.IssueNumber != 30 || preGate.request.WorkflowRoot != workspaces.binding.Workspace.Path {
		t.Fatalf("evidence pre-gate request=%+v calls=%d credential_available=%t", preGate.request, preGate.calls, preGate.credentialAvailable)
	}
	if workspaces.lastNewRequest.Credentials == nil || len(sandboxer.requests) != 1 || len(sandboxer.requests[0].FileCapabilities) != 4 || sandboxer.requests[0].ChildProfile == nil {
		t.Fatalf("workspace request=%+v sandbox request=%+v", workspaces.lastNewRequest, sandboxer.requests)
	}
	if sandboxer.requests[0].ExtraEnv["GIT_CONFIG_NOSYSTEM"] != "1" || sandboxer.requests[0].ExtraEnv["GIT_CONFIG_VALUE_0"] != "false" {
		t.Fatalf("child git hardening env = %+v", sandboxer.requests[0].ExtraEnv)
	}
	for _, capability := range sandboxer.requests[0].FileCapabilities {
		if _, err := os.Stat(capability.Source); !os.IsNotExist(err) {
			t.Fatalf("credential capability remained after terminal revoke: %s err=%v", capability.Source, err)
		}
		if strings.Contains(capability.Source, "iss_dgt") || strings.Contains(capability.Destination, "iss_dgt") {
			t.Fatalf("secret entered capability metadata: %+v", capability)
		}
	}
	persisted, err := json.Marshal(loadState(t, store))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(persisted), "iss_dgt") || strings.Contains(string(persisted), "git-secret") || strings.Contains(string(persisted), "parent-secret") {
		t.Fatalf("credential leaked into runner state: %s", persisted)
	}
	cleanup := loadState(t, store).Jobs["job-credential"].CredentialCleanup
	if cleanup.Status != state.CredentialCleanupComplete || cleanup.Attempt != 1 || cleanup.CompletedAt.IsZero() {
		t.Fatalf("terminal credential cleanup was not durably confirmed: %+v", cleanup)
	}
}
