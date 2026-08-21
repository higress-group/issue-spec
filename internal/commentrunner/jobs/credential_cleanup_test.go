package jobs

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	clientauth "github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/commentrunner/credentials"
	"github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/server/models"
)

func TestTerminalCredentialCleanupRetriesAllLegsAfterRestart(t *testing.T) {
	var remoteAttempts atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.Header.Get("Authorization") != "Bearer parent-token" {
			http.Error(w, "invalid", http.StatusBadRequest)
			return
		}
		if remoteAttempts.Add(1) == 1 {
			http.Error(w, "retry", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer remote.Close()

	provider := &retryCleanupGitProvider{}
	credentialRoot := filepath.Join(t.TempDir(), "credentials")
	materializer := credentials.Materializer{Root: credentialRoot}
	fileLease, err := materializer.WriteIssueToken("job-cleanup", "iss_dgt_aabbccdd_cleanup")
	if err != nil {
		t.Fatal(err)
	}
	blockedRoot := credentialRoot + ".blocked"
	if err := os.Rename(credentialRoot, blockedRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(blockedRoot, credentialRoot); err != nil {
		t.Fatal(err)
	}

	repoScope := models.RepoScope{OrgID: uuid.New(), RepoID: uuid.New()}
	profile := clientauth.Profile{Name: "cleanup", Kind: clientauth.ProfileKindHosted, Hostname: "127.0.0.1",
		APIURL: remote.URL + "/api/v3", NativeAPIURL: remote.URL + "/api/v1", WebURL: remote.URL,
		ServerInstanceID: "cleanup-instance"}
	broker := &credentials.Broker{Profile: profile, Audience: profile.ServerInstanceID, Subject: "runner",
		ParentToken: "parent-token", HTTPClient: remote.Client(), Materializer: materializer, GitProvider: provider, TTL: time.Minute}

	statePath := filepath.Join(t.TempDir(), "runner-state.json")
	store, err := state.OpenFileStore(statePath)
	if err != nil {
		t.Fatal(err)
	}
	requestedAt := time.Now().UTC().Add(-time.Minute)
	if err := store.Update(t.Context(), func(st *state.RunnerState) error {
		return st.UpsertJob(state.Job{ID: "job-cleanup", Repo: "o/r", Status: state.StatusCompleted,
			CreatedAt: requestedAt, FinishedAt: requestedAt, RepositoryBinding: testRepositoryBinding(),
			CredentialCleanup: state.CredentialCleanup{Status: state.CredentialCleanupPending, RequestedAt: requestedAt}})
	}); err != nil {
		t.Fatal(err)
	}
	dispatcher := cleanupTestDispatcher(store, broker, repoScope)
	first, err := dispatcher.Reconcile(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	firstState, err := store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	firstCleanup := firstState.Jobs["job-cleanup"].CredentialCleanup
	if first.CredentialCleanupPending != 1 || !firstCleanup.Pending() || firstCleanup.Attempt != 1 ||
		firstCleanup.LastError == "" || firstCleanup.NextAttemptAt.IsZero() {
		t.Fatalf("first reconciliation=%+v cleanup=%+v", first, firstCleanup)
	}
	if remoteAttempts.Load() != 1 || provider.attempts.Load() != 1 {
		t.Fatalf("first remote=%d provider=%d", remoteAttempts.Load(), provider.attempts.Load())
	}
	if _, err := os.Stat(fileLease.HostPath); err != nil {
		t.Fatalf("first failed local cleanup did not preserve retry target: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(credentialRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(blockedRoot, credentialRoot); err != nil {
		t.Fatal(err)
	}

	restarted, err := state.OpenFileStore(statePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	dispatcher = cleanupTestDispatcher(restarted, broker, repoScope)
	second, err := dispatcher.Reconcile(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	secondState, err := restarted.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	secondCleanup := secondState.Jobs["job-cleanup"].CredentialCleanup
	if second.CredentialCleanupComplete != 1 || secondCleanup.Status != state.CredentialCleanupComplete ||
		secondCleanup.Attempt != 2 || secondCleanup.LastError != "" || secondCleanup.CompletedAt.IsZero() {
		t.Fatalf("second reconciliation=%+v cleanup=%+v", second, secondCleanup)
	}
	if remoteAttempts.Load() != 2 || provider.attempts.Load() != 2 {
		t.Fatalf("final remote=%d provider=%d", remoteAttempts.Load(), provider.attempts.Load())
	}
	if _, err := os.Stat(fileLease.HostPath); !os.IsNotExist(err) {
		t.Fatalf("local credential survived successful retry: %v", err)
	}
}

func TestLiveDispatcherRetriesPendingTerminalCredentialCleanup(t *testing.T) {
	store := newMemoryStore()
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	if err := store.Update(t.Context(), func(st *state.RunnerState) error {
		return st.UpsertJob(state.Job{ID: "job-live-cleanup", Repo: "o/r", Status: state.StatusCompleted,
			CreatedAt: now.Add(-time.Hour), FinishedAt: now.Add(-time.Minute),
			CredentialCleanup: state.CredentialCleanup{Status: state.CredentialCleanupPending, RequestedAt: now.Add(-time.Minute)}})
	}); err != nil {
		t.Fatal(err)
	}
	broker := &sequenceCleanupBroker{}
	dispatcher := testDispatcher(store, &fakeWorkspaces{}, &fakeCoordinator{}, &fakeWriteback{}, now)
	dispatcher.CredentialBroker = broker
	dispatcher.CredentialScopes = map[string]models.RepoScope{"o/r": {OrgID: uuid.New(), RepoID: uuid.New()}}
	first, err := dispatcher.RunReady(t.Context(), 1)
	if err != nil || first.Reason != "credential_cleanup_pending" || first.Error == "" {
		t.Fatalf("first live retry=%+v err=%v", first, err)
	}
	pending := loadState(t, store).Jobs["job-live-cleanup"].CredentialCleanup
	if !pending.Pending() || pending.Attempt != 1 {
		t.Fatalf("pending cleanup=%+v", pending)
	}
	dispatcher.Clock = fixedClock(pending.NextAttemptAt.Add(time.Second))
	second, err := dispatcher.RunReady(t.Context(), 1)
	if err != nil || second.Reason != "credential_cleanup_complete" || second.Error != "" {
		t.Fatalf("second live retry=%+v err=%v", second, err)
	}
	completed := loadState(t, store).Jobs["job-live-cleanup"].CredentialCleanup
	if completed.Status != state.CredentialCleanupComplete || completed.Attempt != 2 || broker.attempts.Load() != 2 {
		t.Fatalf("completed cleanup=%+v attempts=%d", completed, broker.attempts.Load())
	}
}

func TestRestartCleanupUsesPersistedScopeAfterRepositoryPermissionRemoval(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "runner-state.json")
	store, err := state.OpenFileStore(statePath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 11, 13, 0, 0, 0, time.UTC)
	scope := models.RepoScope{OrgID: uuid.New(), RepoID: uuid.New()}
	if err := store.Update(t.Context(), func(st *state.RunnerState) error {
		persisted := scope
		return st.UpsertJob(state.Job{ID: "job-scope-cleanup", Repo: "o/r", Status: state.StatusInterrupted,
			CreatedAt: now.Add(-time.Hour), FinishedAt: now.Add(-time.Minute),
			CredentialCleanup: state.CredentialCleanup{Status: state.CredentialCleanupPending,
				RepositoryScope: &persisted, RequestedAt: now.Add(-time.Minute)}})
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := state.OpenFileStore(statePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	reloaded, err := restarted.Load(t.Context())
	if err != nil || reloaded.Jobs["job-scope-cleanup"].CredentialCleanup.RepositoryScope == nil ||
		*reloaded.Jobs["job-scope-cleanup"].CredentialCleanup.RepositoryScope != scope {
		t.Fatalf("persisted cleanup scope did not survive restart: state=%+v err=%v",
			reloaded.Jobs["job-scope-cleanup"].CredentialCleanup, err)
	}
	broker := &recordingCleanupBroker{}
	repositories := &countingRepositoryResolver{err: errors.New("repository permission removed")}
	dispatcher := cleanupTestDispatcher(restarted, broker, models.RepoScope{})
	dispatcher.Clock = fixedClock(now)
	dispatcher.Repositories = repositories
	result, err := dispatcher.Reconcile(t.Context())
	if err != nil || result.CredentialCleanupComplete != 1 {
		t.Fatalf("reconcile=%+v err=%v", result, err)
	}
	current, err := restarted.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	cleanup, retained := current.Jobs["job-scope-cleanup"]
	if retained && cleanup.CredentialCleanup.Status != state.CredentialCleanupComplete {
		t.Fatalf("retained cleanup did not complete: %+v", cleanup.CredentialCleanup)
	}
	if broker.scope != scope || broker.jobID != "job-scope-cleanup" || repositories.calls != 0 {
		t.Fatalf("broker=%+v resolver_calls=%d", broker, repositories.calls)
	}
}

func TestUpgradeCleanupUsesExplicitStaticScopeAfterBindingRemoval(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "runner-state.json")
	store, err := state.OpenFileStore(statePath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if err := store.Update(t.Context(), func(st *state.RunnerState) error {
		return st.UpsertJob(state.Job{ID: "job-legacy-scope-cleanup", Repo: "Owner/Repo", Status: state.StatusInterrupted,
			CreatedAt: now.Add(-time.Hour), FinishedAt: now.Add(-time.Minute),
			CredentialCleanup: state.CredentialCleanup{Status: state.CredentialCleanupPending,
				RequestedAt: now.Add(-time.Minute)}})
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(`"repository_scope"`)) {
		t.Fatalf("base-format cleanup fixture unexpectedly contains repository_scope: %s", raw)
	}
	restarted, err := state.OpenFileStore(statePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	scope := models.RepoScope{OrgID: uuid.New(), RepoID: uuid.New()}
	broker := &recordingCleanupBroker{}
	repositories := &countingRepositoryResolver{err: errors.New("active source binding removed")}
	dispatcher := cleanupTestDispatcher(restarted, broker, models.RepoScope{})
	dispatcher.Clock = fixedClock(now)
	dispatcher.Repositories = repositories
	dispatcher.CredentialScopes = map[string]models.RepoScope{"owner/repo": scope}
	result, err := dispatcher.Reconcile(t.Context())
	if err != nil || result.CredentialCleanupComplete != 1 {
		t.Fatalf("reconcile=%+v err=%v", result, err)
	}
	if broker.scope != scope || broker.jobID != "job-legacy-scope-cleanup" || repositories.calls != 0 {
		t.Fatalf("broker=%+v resolver_calls=%d", broker, repositories.calls)
	}
}

func cleanupTestDispatcher(store state.StateStore, broker CredentialBroker, scope models.RepoScope) *Dispatcher {
	return &Dispatcher{Store: store, Workspaces: &fakeWorkspaces{}, Sandbox: &fakeSandbox{},
		Acpx: fakeAcpxFactory{coordinator: &fakeCoordinator{}}, Writeback: &fakeWriteback{},
		CredentialBroker: broker, CredentialScopes: map[string]models.RepoScope{"o/r": scope}}
}

type retryCleanupGitProvider struct{ attempts atomic.Int32 }

func (*retryCleanupGitProvider) Acquire(context.Context, credentials.GitRequest) (credentials.GitProviderLease, error) {
	return credentials.GitProviderLease{}, errors.New("unexpected acquire")
}

func (p *retryCleanupGitProvider) RevokeJob(context.Context, string) error {
	if p.attempts.Add(1) == 1 {
		return errors.New("provider revoke retry")
	}
	return nil
}

type sequenceCleanupBroker struct{ attempts atomic.Int32 }

func (*sequenceCleanupBroker) Acquire(context.Context, credentials.AcquireRequest) (*credentials.Lease, error) {
	return nil, errors.New("unexpected acquire")
}

func (b *sequenceCleanupBroker) RevokeJob(context.Context, models.RepoScope, string) error {
	if b.attempts.Add(1) == 1 {
		return errors.New("retry cleanup")
	}
	return nil
}

type recordingCleanupBroker struct {
	scope models.RepoScope
	jobID string
}

func (*recordingCleanupBroker) Acquire(context.Context, credentials.AcquireRequest) (*credentials.Lease, error) {
	return nil, errors.New("unexpected acquire")
}

func (b *recordingCleanupBroker) RevokeJob(_ context.Context, scope models.RepoScope, jobID string) error {
	b.scope, b.jobID = scope, jobID
	return nil
}
