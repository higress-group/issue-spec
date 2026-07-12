package jobs

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/acpx"
	resolver "github.com/higress-group/issue-spec/internal/commentrunner/repository"
	"github.com/higress-group/issue-spec/internal/commentrunner/state"
)

func TestDispatcherResumeRejectsBindingDriftBeforeWorkspace(t *testing.T) {
	store := newMemoryStore()
	now := time.Date(2026, 7, 11, 5, 0, 0, 0, time.UTC)
	old := testRepositoryBinding()
	seedResumeRepositoryJob(t, store, now, old, "job-drift", "ps-drift")
	changed := old
	changed.BindingID = "mapping-v2"
	changed.Version = 2
	changed.CloneURL = "https://code.example/o/r-v2.git"
	workspaces := &fakeWorkspaces{binding: testBinding("ws-drift")}
	dispatcher := testDispatcher(store, workspaces, &fakeCoordinator{}, &fakeWriteback{}, now)
	dispatcher.Repositories = fixedRepositoryResolver{info: repositoryInfo(changed)}

	result, err := dispatcher.RunNext(context.Background())
	if err == nil || resolver.DiagnosticCode(err) != resolver.DiagnosticBindingDrift || result.Status != state.StatusFailed {
		t.Fatalf("RunNext drift result=%+v err=%v", result, err)
	}
	if workspaces.resolveResumeCalled || workspaces.prepareNewCalled {
		t.Fatal("workspace was touched before drift rejection")
	}
	job := loadState(t, store).Jobs["job-drift"]
	if !containsStringContaining(job.Diagnostics, resolver.DiagnosticBindingDrift) {
		t.Fatalf("drift diagnostic missing: %+v", job.Diagnostics)
	}
}

func TestDispatcherResumeRejectsLegacyStateBeforeResolverOrWorkspace(t *testing.T) {
	store := newMemoryStore()
	now := time.Date(2026, 7, 11, 5, 10, 0, 0, time.UTC)
	seedResumeRepositoryJob(t, store, now, state.RepositoryBindingSnapshot{}, "job-legacy", "ps-legacy")
	repositories := &countingRepositoryResolver{info: repositoryInfo(testRepositoryBinding())}
	workspaces := &fakeWorkspaces{binding: testBinding("ws-legacy")}
	dispatcher := testDispatcher(store, workspaces, &fakeCoordinator{}, &fakeWriteback{}, now)
	dispatcher.Repositories = repositories

	result, err := dispatcher.RunNext(context.Background())
	if err == nil || resolver.DiagnosticCode(err) != resolver.DiagnosticLegacyState || result.Status != state.StatusFailed {
		t.Fatalf("RunNext legacy result=%+v err=%v", result, err)
	}
	if repositories.calls != 0 || workspaces.resolveResumeCalled || workspaces.prepareNewCalled {
		t.Fatalf("legacy rejection had side effects: resolver=%d prepare=%t resume=%t", repositories.calls, workspaces.prepareNewCalled, workspaces.resolveResumeCalled)
	}
}

func TestDispatcherResumeRejectsWorkspaceManagerSnapshotSubstitutionBeforeSandbox(t *testing.T) {
	store := newMemoryStore()
	now := time.Date(2026, 7, 11, 5, 15, 0, 0, time.UTC)
	pinned := testRepositoryBinding()
	seedResumeRepositoryJob(t, store, now, pinned, "job-workspace-drift", "ps-workspace-drift")
	replaced := testBinding("ws-workspace-drift")
	replaced.Workspace.RepositoryBinding = pinned
	replaced.Workspace.RepositoryBinding.BindingID = "substituted-binding"
	workspaces := &fakeWorkspaces{binding: replaced}
	dispatcher := testDispatcher(store, workspaces, &fakeCoordinator{}, &fakeWriteback{}, now)
	dispatcher.Repositories = fixedRepositoryResolver{info: repositoryInfo(pinned)}
	sandbox := &fakeSandbox{}
	dispatcher.Sandbox = sandbox

	result, err := dispatcher.RunNext(context.Background())
	if err == nil || resolver.DiagnosticCode(err) != resolver.DiagnosticBindingDrift || result.Status != state.StatusFailed {
		t.Fatalf("RunNext workspace substitution result=%+v err=%v", result, err)
	}
	if !workspaces.resolveResumeCalled || len(sandbox.requests) != 0 {
		t.Fatalf("workspace substitution was not stopped before sandbox: resume=%t sandbox=%d", workspaces.resolveResumeCalled, len(sandbox.requests))
	}
}

func TestDispatcherNewFailsClosedWithoutBinding(t *testing.T) {
	store := newMemoryStore()
	now := time.Date(2026, 7, 11, 5, 20, 0, 0, time.UTC)
	seedQueuedJob(t, store, state.Job{ID: "job-no-binding", Repo: "o/r", IssueNumber: 1,
		CommandName: "new", CommandPrompt: "work", CommandIdempotencyKey: "no-binding", Status: state.StatusQueued})
	workspaces := &fakeWorkspaces{binding: testBinding("ws-unused")}
	dispatcher := testDispatcher(store, workspaces, &fakeCoordinator{}, &fakeWriteback{}, now)
	dispatcher.Repositories = fixedRepositoryResolver{err: resolver.NoBindingError()}
	dispatcher.PublicSessionID = func() (string, error) { return "ps-no-binding", nil }

	result, err := dispatcher.RunNext(context.Background())
	if err == nil || !errors.Is(err, resolver.ErrNoBinding) || result.Status != state.StatusFailed {
		t.Fatalf("RunNext no binding result=%+v err=%v", result, err)
	}
	if workspaces.prepareNewCalled || workspaces.resolveResumeCalled {
		t.Fatal("workspace was touched without a binding")
	}
}

func TestDispatcherPinsAllRepositorySnapshotsBeforeCoordinatorDispatch(t *testing.T) {
	store := newMemoryStore()
	now := time.Date(2026, 7, 11, 5, 25, 0, 0, time.UTC)
	seedQueuedJob(t, store, state.Job{ID: "job-pre-dispatch-pin", Repo: "o/r", IssueNumber: 1,
		CommandName: "new", CommandPrompt: "work", CommandIdempotencyKey: "pre-dispatch-pin",
		SessionCreatorLogin: "alice", TriggeringUserLogin: "alice", TriggerCommentID: 101,
		Status: state.StatusQueued, CreatedAt: now, FirstObservedComment: state.SeenComment{
			Repo: "o/r", IssueNumber: 1, CommentID: 101, AuthorLogin: "alice",
			FirstObservedUpdatedAt: now, FirstObservedBodyHash: "sha256:pin",
		}})
	pinned := testRepositoryBinding()
	coordinator := &fakeCoordinator{}
	coordinator.onNew = func(_ context.Context, _ acpx.NewSessionRequest) (acpx.DispatchResult, error) {
		st := loadState(t, store)
		job := st.Jobs["job-pre-dispatch-pin"]
		session, ok := st.GetPublicSession("o/r", "ps-pre-dispatch-pin")
		workspaceMeta, workspaceOK := st.GetWorkspace(job.Workspace.ID)
		if !ok || !workspaceOK || !job.RepositoryBinding.Equal(pinned) ||
			!job.DispatchIntent.RepositoryBinding.Equal(pinned) || !session.RepositoryBinding.Equal(pinned) ||
			!workspaceMeta.RepositoryBinding.Equal(pinned) {
			t.Fatalf("repository snapshot not pinned before coordinator: job=%+v intent=%+v session=%+v workspace=%+v",
				job.RepositoryBinding, job.DispatchIntent.RepositoryBinding, session.RepositoryBinding, workspaceMeta.RepositoryBinding)
		}
		return dispatchResult("ps-pre-dispatch-pin", "rec-pre-dispatch-pin", "turn-pin", completedSummary()), nil
	}
	workspaces := &fakeWorkspaces{binding: testBinding("ws-pre-dispatch-pin")}
	dispatcher := testDispatcher(store, workspaces, coordinator, &fakeWriteback{}, now)
	dispatcher.Repositories = fixedRepositoryResolver{info: repositoryInfo(pinned)}
	dispatcher.PublicSessionID = func() (string, error) { return "ps-pre-dispatch-pin", nil }
	if result, err := dispatcher.RunNext(context.Background()); err != nil || result.Status != state.StatusCompleted {
		t.Fatalf("RunNext result=%+v err=%v", result, err)
	}
}

func TestStaticRepositoryResolverDoesNotDeriveFromHostnameOrSlug(t *testing.T) {
	_, err := (StaticRepositoryResolver{Hostname: "github.com"}).ResolveRepository(t.Context(), "acme/widgets")
	if !errors.Is(err, resolver.ErrNoBinding) {
		t.Fatalf("StaticRepositoryResolver derived a source: %v", err)
	}
}

func TestConcurrentJobBindingPinCannotSplitSnapshot(t *testing.T) {
	store := newMemoryStore()
	seedQueuedJob(t, store, state.Job{ID: "job-pin", Repo: "o/r", CommandName: "new",
		CommandPrompt: "work", CommandIdempotencyKey: "pin", Status: state.StatusQueued})
	dispatcher := &Dispatcher{Store: store}
	first := testRepositoryBinding()
	second := first
	second.BindingID = "other-binding"
	second.Version = 2
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for _, binding := range []state.RepositoryBindingSnapshot{first, second} {
		binding := binding
		wait.Add(1)
		go func() {
			defer wait.Done()
			results <- dispatcher.pinJobRepositoryBinding(context.Background(), "job-pin", binding)
		}()
	}
	wait.Wait()
	close(results)
	var success, drift int
	for err := range results {
		if err == nil {
			success++
		} else if resolver.DiagnosticCode(err) == resolver.DiagnosticBindingDrift {
			drift++
		} else {
			t.Fatalf("unexpected pin error: %v", err)
		}
	}
	if success != 1 || drift != 1 || !loadState(t, store).Jobs["job-pin"].RepositoryBinding.Complete() {
		t.Fatalf("concurrent pin success=%d drift=%d state=%+v", success, drift, loadState(t, store).Jobs["job-pin"].RepositoryBinding)
	}
}

func seedResumeRepositoryJob(t *testing.T, store *memoryStore, now time.Time, binding state.RepositoryBindingSnapshot, jobID, publicID string) {
	t.Helper()
	workspace := state.WorkspaceMetadata{ID: "ws-" + publicID, Path: "/tmp/ws-" + publicID, Repo: "o/r",
		CloneURL: firstNonEmpty(binding.CloneURL, "https://legacy.example/o/r.git"), Branch: "issue-spec-" + publicID,
		Ref: "main", RepositoryBinding: binding}
	seedState(t, store, func(st *state.RunnerState) error {
		if err := st.UpsertWorkspace(workspace); err != nil {
			return err
		}
		if err := st.UpsertPublicSession(state.PublicSession{Repo: "o/r", PublicSessionID: publicID,
			IssueNumber: 1, AcpxRecordID: "rec-" + publicID, CreatorLogin: "alice", Status: state.StatusCompleted,
			Workspace: workspace, RepositoryBinding: binding, CreatedAt: now.Add(-time.Hour)}); err != nil {
			return err
		}
		_, _, err := st.CreateCommandJob(state.Job{ID: jobID, Repo: "o/r", IssueNumber: 1, PublicSessionID: publicID,
			CommandName: "resume", CommandPrompt: "continue", CommandIdempotencyKey: jobID, Status: state.StatusQueued,
			CreatedAt: now})
		return err
	})
}

func repositoryInfo(binding state.RepositoryBindingSnapshot) RepositoryInfo {
	return RepositoryInfo{Repo: binding.IssueRepositoryKey, CloneURL: binding.CloneURL,
		DefaultBranch: binding.DefaultBranch, Ref: binding.DefaultBranch, Binding: binding}
}

type fixedRepositoryResolver struct {
	info RepositoryInfo
	err  error
}

func (r fixedRepositoryResolver) ResolveRepository(context.Context, string) (RepositoryInfo, error) {
	return r.info, r.err
}

type countingRepositoryResolver struct {
	info  RepositoryInfo
	err   error
	calls int
}

func (r *countingRepositoryResolver) ResolveRepository(context.Context, string) (RepositoryInfo, error) {
	r.calls++
	return r.info, r.err
}

func containsStringContaining(values []string, want string) bool {
	for _, value := range values {
		if strings.Contains(value, want) {
			return true
		}
	}
	return false
}
