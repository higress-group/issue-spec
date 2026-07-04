package jobs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/acpx"
	"github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/workspace"
)

func TestReconcileRunningCompletedPatchesStateWritebackAndReleasesLock(t *testing.T) {
	store := newMemoryStore()
	now := time.Date(2026, 7, 3, 13, 0, 0, 0, time.UTC)
	workspaceMeta := testBinding("ws-reconcile-complete").Workspace
	lock := state.SessionLock{OwnerJobID: "job-reconcile", WorkspaceLockToken: "token", WorkspaceLockPath: "/tmp/lock"}
	seedActiveJob(t, store, state.StatusRunning, workspaceMeta, lock)

	writebacks := &fakeWriteback{store: store}
	workspaces := &fakeWorkspaces{binding: testBinding("unused")}
	coordinator := &fakeReconcileCoordinator{reconcileResult: acpx.TurnReconcileResult{
		Status: acpx.ReconcileStatusCompleted,
		Metadata: acpx.Metadata{
			StableRecordID: "rec-reconcile",
			LastTurnID:     "turn-recovered",
		},
		Output: acpx.TurnOutput{
			ReplyText:    "recovered output",
			Summary:      completedSummary(),
			SummaryFound: true,
		},
		Diagnostics: "terminal output recovered",
	}}
	dispatcher := testDispatcher(store, workspaces, &fakeCoordinator{}, writebacks, now)
	dispatcher.Acpx = staticAcpxFactory{coordinator: coordinator}

	result, err := dispatcher.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.Reconciled != 1 || result.Completed != 1 || coordinator.reconcileCalls != 1 {
		t.Fatalf("unexpected reconcile result: %+v calls=%d", result, coordinator.reconcileCalls)
	}
	assertWritebackStatuses(t, writebacks, state.StatusCompleted)
	if !workspaces.released {
		t.Fatal("workspace lock was not released")
	}

	st := loadState(t, store)
	job := st.Jobs["job-reconcile"]
	if job.Status != state.StatusCompleted || job.Restart.RecoveredStatus != state.StatusCompleted || job.Acpx.LastTurnID != "turn-recovered" {
		t.Fatalf("job was not recovered as completed: %+v", job)
	}
	if len(job.CLIDirect) != 1 {
		t.Fatalf("coordinator provenance was not recovered: %+v", job.CLIDirect)
	}
	session, ok := st.GetPublicSession("o/r", "ps-reconcile")
	if !ok || session.Status != state.StatusCompleted || session.Lock.OwnerJobID != "" {
		t.Fatalf("session was not completed and unlocked: %+v ok=%v", session, ok)
	}
}

func TestReconcileDispatchedRefreshFallbackReturnsRunningWithoutRedispatch(t *testing.T) {
	store := newMemoryStore()
	now := time.Date(2026, 7, 3, 13, 30, 0, 0, time.UTC)
	workspaceMeta := testBinding("ws-reconcile-running").Workspace
	seedActiveJob(t, store, state.StatusDispatched, workspaceMeta, state.SessionLock{OwnerJobID: "job-reconcile"})

	writebacks := &fakeWriteback{store: store}
	coordinator := &fakeRefreshCoordinator{metadata: acpx.Metadata{StableRecordID: "rec-reconcile", LastTurnID: "turn-still-active"}}
	dispatcher := testDispatcher(store, &fakeWorkspaces{binding: testBinding("unused")}, &fakeCoordinator{}, writebacks, now)
	dispatcher.Acpx = staticAcpxFactory{coordinator: coordinator}

	result, err := dispatcher.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.Running != 1 || coordinator.refreshCalls != 1 {
		t.Fatalf("unexpected reconcile result: %+v refresh=%d", result, coordinator.refreshCalls)
	}
	assertWritebackStatuses(t, writebacks, state.StatusRunning)
	if coordinator.newCalls != 0 || coordinator.resumeCalls != 0 {
		t.Fatalf("reconcile redispatched prompts: new=%d resume=%d", coordinator.newCalls, coordinator.resumeCalls)
	}
	job := loadState(t, store).Jobs["job-reconcile"]
	if job.Status != state.StatusRunning || job.Acpx.LastTurnID != "turn-still-active" {
		t.Fatalf("job not returned to running: %+v", job)
	}
}

func TestReconcileAmbiguousMarksInterruptedAndDirty(t *testing.T) {
	store := newMemoryStore()
	now := time.Date(2026, 7, 3, 14, 0, 0, 0, time.UTC)
	workspaceMeta := testBinding("ws-reconcile-ambiguous").Workspace
	lock := state.SessionLock{OwnerJobID: "job-reconcile", WorkspaceLockToken: "token", WorkspaceLockPath: "/tmp/lock"}
	seedActiveJob(t, store, state.StatusRunning, workspaceMeta, lock)

	writebacks := &fakeWriteback{store: store}
	workspaces := &fakeWorkspaces{binding: testBinding("unused")}
	coordinator := &fakeReconcileCoordinator{reconcileResult: acpx.TurnReconcileResult{
		Ambiguous:   true,
		Diagnostics: "turn token was not found in acpx history",
	}}
	dispatcher := testDispatcher(store, workspaces, &fakeCoordinator{}, writebacks, now)
	dispatcher.Acpx = staticAcpxFactory{coordinator: coordinator}

	result, err := dispatcher.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.Interrupted != 1 {
		t.Fatalf("unexpected reconcile result: %+v", result)
	}
	assertWritebackStatuses(t, writebacks, state.StatusInterrupted)
	if !workspaces.released {
		t.Fatal("workspace lock was not released")
	}
	st := loadState(t, store)
	job := st.Jobs["job-reconcile"]
	if job.Status != state.StatusInterrupted || !job.Restart.Ambiguous || !job.Workspace.Dirty || !job.Workspace.Uncertain {
		t.Fatalf("ambiguous job not marked interrupted/dirty: %+v", job)
	}
	session, ok := st.GetPublicSession("o/r", "ps-reconcile")
	if !ok || session.Status != state.StatusInterrupted || !session.Workspace.Uncertain {
		t.Fatalf("session not marked interrupted/uncertain: %+v ok=%v", session, ok)
	}
}

func TestReconcileCleansExpiredInactiveWorkspaces(t *testing.T) {
	store := newMemoryStore()
	root := t.TempDir()
	now := time.Date(2026, 7, 4, 10, 0, 0, 0, time.UTC)
	expired := testWorkspacePath(t, root, "ws-expired")
	active := testWorkspacePath(t, root, "ws-active")
	recent := testWorkspacePath(t, root, "ws-recent")
	queuedSession := testWorkspacePath(t, root, "ws-session-queued")
	expiredMeta := state.WorkspaceMetadata{ID: "ws-expired", Path: expired, Repo: "o/r", LastUsedAt: now.Add(-3 * time.Hour)}
	activeMeta := state.WorkspaceMetadata{ID: "ws-active", Path: active, Repo: "o/r", LastUsedAt: now.Add(-3 * time.Hour), CleanupAfter: now.Add(-time.Hour)}
	recentMeta := state.WorkspaceMetadata{ID: "ws-recent", Path: recent, Repo: "o/r", LastUsedAt: now.Add(-30 * time.Minute)}
	queuedSessionMeta := state.WorkspaceMetadata{ID: "ws-session-queued", Path: queuedSession, Repo: "o/r", LastUsedAt: now.Add(-3 * time.Hour), CleanupAfter: now.Add(-time.Hour)}
	seedState(t, store, func(st *state.RunnerState) error {
		for _, workspaceMeta := range []state.WorkspaceMetadata{expiredMeta, activeMeta, recentMeta, queuedSessionMeta} {
			if err := st.UpsertWorkspace(workspaceMeta); err != nil {
				return err
			}
		}
		if err := st.UpsertJob(state.Job{ID: "job-active", Repo: "o/r", Status: state.StatusQueued, Workspace: activeMeta}); err != nil {
			return err
		}
		if err := st.UpsertPublicSession(state.PublicSession{
			Repo:            "o/r",
			PublicSessionID: "ps-queued",
			AcpxRecordID:    "rec-queued",
			Status:          state.StatusCompleted,
			Workspace:       queuedSessionMeta,
		}); err != nil {
			return err
		}
		return st.UpsertJob(state.Job{ID: "job-session-queued", Repo: "o/r", PublicSessionID: "ps-queued", Status: state.StatusQueued})
	})
	writebacks := &fakeWriteback{store: store}
	dispatcher := &Dispatcher{
		Store: store,
		Workspaces: workspace.Manager{
			Root:      root,
			Retention: time.Hour,
			Now:       func() time.Time { return now },
		},
		Sandbox:   &fakeSandbox{},
		Acpx:      fakeAcpxFactory{coordinator: &fakeCoordinator{}},
		Writeback: writebacks,
		Clock:     fixedClock(now),
	}

	result, err := dispatcher.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.Queued != 2 {
		t.Fatalf("queued jobs not preserved: %+v", result)
	}
	actions := map[string]string{}
	for _, cleanup := range result.WorkspaceCleanup {
		actions[cleanup.WorkspaceID] = cleanup.Action + ":" + cleanup.Reason
	}
	want := map[string]string{
		"ws-active":         "kept:active",
		"ws-expired":        "removed:expired",
		"ws-recent":         "kept:within_retention",
		"ws-session-queued": "kept:active",
	}
	for id, action := range want {
		if actions[id] != action {
			t.Fatalf("cleanup action for %s = %q, want %q; all=%#v", id, actions[id], action, actions)
		}
	}
	if _, err := os.Stat(expired); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired workspace still exists or unexpected stat error: %v", err)
	}
	for _, path := range []string{active, recent, queuedSession} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("protected workspace %s missing: %v", path, err)
		}
	}
	st := loadState(t, store)
	if _, ok := st.GetWorkspace("ws-expired"); ok {
		t.Fatalf("removed workspace stayed indexed: %+v", st.Workspaces["ws-expired"])
	}
	for _, id := range []string{"ws-active", "ws-recent", "ws-session-queued"} {
		if _, ok := st.GetWorkspace(id); !ok {
			t.Fatalf("protected workspace %s was removed from state", id)
		}
	}
}

func TestReconcileCleanupUsesDefaultSevenDayRetention(t *testing.T) {
	store := newMemoryStore()
	root := t.TempDir()
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	expired := testWorkspacePath(t, root, "ws-eight-days-old")
	withinRetention := testWorkspacePath(t, root, "ws-six-days-old")
	seedState(t, store, func(st *state.RunnerState) error {
		if err := st.UpsertWorkspace(state.WorkspaceMetadata{
			ID:         "ws-eight-days-old",
			Path:       expired,
			Repo:       "o/r",
			LastUsedAt: now.Add(-8 * 24 * time.Hour),
		}); err != nil {
			return err
		}
		return st.UpsertWorkspace(state.WorkspaceMetadata{
			ID:         "ws-six-days-old",
			Path:       withinRetention,
			Repo:       "o/r",
			LastUsedAt: now.Add(-6 * 24 * time.Hour),
		})
	})
	writebacks := &fakeWriteback{store: store}
	dispatcher := &Dispatcher{
		Store: store,
		Workspaces: workspace.Manager{
			Root:      root,
			Retention: 7 * 24 * time.Hour,
			Now:       func() time.Time { return now },
		},
		Sandbox:   &fakeSandbox{},
		Acpx:      fakeAcpxFactory{coordinator: &fakeCoordinator{}},
		Writeback: writebacks,
		Clock:     fixedClock(now),
	}

	result, err := dispatcher.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	actions := map[string]string{}
	for _, cleanup := range result.WorkspaceCleanup {
		actions[cleanup.WorkspaceID] = cleanup.Action + ":" + cleanup.Reason
	}
	if actions["ws-eight-days-old"] != "removed:expired" {
		t.Fatalf("8-day workspace action = %q, want removed:expired; all=%#v", actions["ws-eight-days-old"], actions)
	}
	if actions["ws-six-days-old"] != "kept:within_retention" {
		t.Fatalf("6-day workspace action = %q, want kept:within_retention; all=%#v", actions["ws-six-days-old"], actions)
	}
	if _, err := os.Stat(expired); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("8-day workspace still exists or unexpected stat error: %v", err)
	}
	if _, err := os.Stat(withinRetention); err != nil {
		t.Fatalf("6-day workspace removed: %v", err)
	}
}

func TestReconcileCleanupFailureIsDiagnosticOnly(t *testing.T) {
	store := newMemoryStore()
	root := t.TempDir()
	outsideRoot := t.TempDir()
	now := time.Date(2026, 7, 4, 11, 0, 0, 0, time.UTC)
	badPath := filepath.Join(outsideRoot, "ws-bad")
	seedState(t, store, func(st *state.RunnerState) error {
		return st.UpsertWorkspace(state.WorkspaceMetadata{
			ID:           "ws-bad",
			Path:         badPath,
			Repo:         "o/r",
			LastUsedAt:   now.Add(-3 * time.Hour),
			CleanupAfter: now.Add(-time.Hour),
		})
	})
	writebacks := &fakeWriteback{store: store}
	dispatcher := &Dispatcher{
		Store: store,
		Workspaces: workspace.Manager{
			Root:      root,
			Retention: time.Hour,
			Now:       func() time.Time { return now },
		},
		Sandbox:   &fakeSandbox{},
		Acpx:      fakeAcpxFactory{coordinator: &fakeCoordinator{}},
		Writeback: writebacks,
		Clock:     fixedClock(now),
	}

	result, err := dispatcher.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("cleanup failure should not fail reconcile: %v", err)
	}
	if len(result.WorkspaceCleanup) != 1 || result.WorkspaceCleanup[0].Action != "rejected" {
		t.Fatalf("cleanup rejection not reported: %+v", result.WorkspaceCleanup)
	}
	if len(result.Diagnostics) == 0 || !strings.Contains(result.Diagnostics[0], "workspace cleanup rejected ws-bad") {
		t.Fatalf("cleanup diagnostic missing: %+v", result.Diagnostics)
	}
	st := loadState(t, store)
	if _, ok := st.GetWorkspace("ws-bad"); !ok {
		t.Fatal("failed cleanup should leave workspace metadata indexed")
	}
}

func TestRunNextCancellationConfirmedCancelsRunningJob(t *testing.T) {
	store := newMemoryStore()
	now := time.Date(2026, 7, 3, 15, 0, 0, 0, time.UTC)
	workspaceMeta := testBinding("ws-cancel-confirmed").Workspace
	lock := state.SessionLock{OwnerJobID: "job-reconcile", WorkspaceLockToken: "token", WorkspaceLockPath: "/tmp/lock"}
	seedActiveJob(t, store, state.StatusRunning, workspaceMeta, lock)
	seedCancellation(t, store, "cancel-1", "cancel-key-1", now)

	writebacks := &fakeWriteback{store: store}
	workspaces := &fakeWorkspaces{binding: testBinding("unused")}
	coordinator := &fakeCancelCoordinator{cancelResult: acpx.CancelResult{Confirmed: true, Diagnostics: "cancelled by acpx"}}
	dispatcher := testDispatcher(store, workspaces, &fakeCoordinator{}, writebacks, now)
	dispatcher.Acpx = staticAcpxFactory{coordinator: coordinator}

	result, err := dispatcher.RunNext(context.Background())
	if err != nil {
		t.Fatalf("RunNext returned error: %v", err)
	}
	if !result.Executed || result.CancellationID != "cancel-1" || result.JobID != "job-reconcile" || result.Status != state.StatusCancelled {
		t.Fatalf("unexpected cancel result: %+v", result)
	}
	assertWritebackStatuses(t, writebacks, state.StatusCancelled)
	if writebacks.requests[0].CancelingUserLogin != "bob" {
		t.Fatalf("canceling user not passed to writeback: %+v", writebacks.requests[0])
	}
	if !workspaces.released {
		t.Fatal("workspace lock was not released")
	}
	st := loadState(t, store)
	job := st.Jobs["job-reconcile"]
	cancel := st.Cancellations["cancel-1"]
	if job.Status != state.StatusCancelled || !job.Workspace.Dirty || !job.Workspace.Uncertain {
		t.Fatalf("job not cancelled/dirty: %+v", job)
	}
	if cancel.Status != state.StatusCancelled || !cancel.DirtyWorkspace || !cancel.WorkspaceUncertain || coordinator.cancelCalls != 1 {
		t.Fatalf("cancellation not confirmed: %+v calls=%d", cancel, coordinator.cancelCalls)
	}
}

func testWorkspacePath(t *testing.T, root, id string) string {
	t.Helper()
	path := filepath.Join(root, id)
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunNextCancellationUnsupportedLeavesJobRunningAndReports(t *testing.T) {
	store := newMemoryStore()
	now := time.Date(2026, 7, 3, 16, 0, 0, 0, time.UTC)
	workspaceMeta := testBinding("ws-cancel-unsupported").Workspace
	seedActiveJob(t, store, state.StatusRunning, workspaceMeta, state.SessionLock{OwnerJobID: "job-reconcile"})
	seedCancellation(t, store, "cancel-1", "cancel-key-1", now)

	writebacks := &fakeWriteback{store: store}
	workspaces := &fakeWorkspaces{binding: testBinding("unused")}
	coordinator := &fakeCancelCoordinator{
		cancelResult: acpx.CancelResult{Unsupported: true, Diagnostics: "cancel command unavailable"},
		cancelErr:    acpx.ErrUnsupportedCancel,
	}
	dispatcher := testDispatcher(store, workspaces, &fakeCoordinator{}, writebacks, now)
	dispatcher.Acpx = staticAcpxFactory{coordinator: coordinator}

	result, err := dispatcher.RunNext(context.Background())
	if err != nil {
		t.Fatalf("RunNext returned error: %v", err)
	}
	if result.Status != state.StatusFailed || result.CancellationID != "cancel-1" {
		t.Fatalf("unexpected unsupported cancel result: %+v", result)
	}
	assertWritebackStatuses(t, writebacks, state.StatusRunning)
	if writebacks.requests[0].Phase != "cancel-unsupported" {
		t.Fatalf("unsupported cancellation not surfaced in writeback: %+v", writebacks.requests[0])
	}
	if workspaces.released {
		t.Fatal("lock should stay held when cancellation is unsupported")
	}
	st := loadState(t, store)
	if st.Jobs["job-reconcile"].Status != state.StatusRunning {
		t.Fatalf("job should remain running: %+v", st.Jobs["job-reconcile"])
	}
	if st.Cancellations["cancel-1"].Status != state.StatusFailed {
		t.Fatalf("cancellation should be failed: %+v", st.Cancellations["cancel-1"])
	}
}

func TestRunNextCancellationUnknownAndTerminalAreSafe(t *testing.T) {
	now := time.Date(2026, 7, 3, 17, 0, 0, 0, time.UTC)
	t.Run("unknown session", func(t *testing.T) {
		store := newMemoryStore()
		seedCancellation(t, store, "cancel-unknown", "cancel-key-unknown", now)
		writebacks := &fakeWriteback{store: store}
		dispatcher := testDispatcher(store, &fakeWorkspaces{}, &fakeCoordinator{}, writebacks, now)

		result, err := dispatcher.RunNext(context.Background())
		if err != nil {
			t.Fatalf("RunNext returned error: %v", err)
		}
		if result.Status != state.StatusRejected || result.Reason != "unknown_session" {
			t.Fatalf("unexpected unknown cancel result: %+v", result)
		}
		if len(writebacks.requests) != 0 {
			t.Fatalf("unknown cancellation should not write back without a target job: %+v", writebacks.requests)
		}
		if got := loadState(t, store).Cancellations["cancel-unknown"].Status; got != state.StatusRejected {
			t.Fatalf("unknown cancellation status = %s", got)
		}
	})
	t.Run("terminal target", func(t *testing.T) {
		store := newMemoryStore()
		workspaceMeta := testBinding("ws-cancel-terminal").Workspace
		seedActiveJob(t, store, state.StatusCompleted, workspaceMeta, state.SessionLock{})
		seedCancellation(t, store, "cancel-terminal", "cancel-key-terminal", now)
		writebacks := &fakeWriteback{store: store}
		dispatcher := testDispatcher(store, &fakeWorkspaces{}, &fakeCoordinator{}, writebacks, now)

		result, err := dispatcher.RunNext(context.Background())
		if err != nil {
			t.Fatalf("RunNext returned error: %v", err)
		}
		if result.Status != state.StatusCancelled || result.Reason != "target_already_terminal" {
			t.Fatalf("unexpected terminal cancel result: %+v", result)
		}
		if len(writebacks.requests) != 0 {
			t.Fatalf("terminal cancellation should not rewrite completed status: %+v", writebacks.requests)
		}
		if got := loadState(t, store).Cancellations["cancel-terminal"].Status; got != state.StatusCancelled {
			t.Fatalf("terminal cancellation status = %s", got)
		}
	})
}

func seedActiveJob(t *testing.T, store *memoryStore, status state.LifecycleStatus, workspaceMeta state.WorkspaceMetadata, lock state.SessionLock) {
	t.Helper()
	seedState(t, store, func(st *state.RunnerState) error {
		if err := st.UpsertWorkspace(workspaceMeta); err != nil {
			return err
		}
		job := state.Job{
			ID:                  "job-reconcile",
			Repo:                "o/r",
			IssueNumber:         30,
			PublicSessionID:     "ps-reconcile",
			AcpxRecordID:        "rec-reconcile",
			CoordinatorKind:     "codex",
			Model:               "gpt-5.5[xhigh]",
			SessionCreatorLogin: "alice",
			TriggeringUserLogin: "alice",
			TriggerCommentID:    404,
			StatusWritebackKey:  "status-reconcile",
			Status:              status,
			CreatedAt:           time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC),
			UpdatedAt:           time.Date(2026, 7, 3, 12, 1, 0, 0, time.UTC),
			Workspace:           workspaceMeta,
			DispatchIntent: state.DispatchIntent{
				RunnerJobID:          "job-reconcile",
				PublicSessionID:      "ps-reconcile",
				AcpxRecordID:         "rec-reconcile",
				TurnSequence:         2,
				TurnCorrelationToken: "turn-token-reconcile",
				WorkspaceLockOwner:   "job-reconcile",
				PersistedAt:          time.Date(2026, 7, 3, 12, 1, 0, 0, time.UTC),
			},
			Acpx: state.AcpxMetadata{StableRecordID: "rec-reconcile", LastTurnID: "turn-before-restart"},
		}
		if err := st.UpsertJob(job); err != nil {
			return err
		}
		session := state.PublicSession{
			Repo:            "o/r",
			PublicSessionID: "ps-reconcile",
			IssueNumber:     30,
			AcpxRecordID:    "rec-reconcile",
			CreatorLogin:    "alice",
			Status:          status,
			Workspace:       workspaceMeta,
			Queue:           state.SessionQueue{AcceptedSequence: 2, PendingJobIDs: []string{"job-reconcile"}},
			Lock:            lock,
			CreatedAt:       time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC),
			LastUsedAt:      time.Date(2026, 7, 3, 12, 1, 0, 0, time.UTC),
			LastJobID:       "job-reconcile",
		}
		return st.UpsertPublicSession(session)
	})
}

func seedCancellation(t *testing.T, store *memoryStore, id, key string, now time.Time) {
	t.Helper()
	seedState(t, store, func(st *state.RunnerState) error {
		return st.UpsertCancellation(state.Cancellation{
			ID:                    id,
			IdempotencyKey:        key,
			Repo:                  "o/r",
			TriggerCommentID:      505,
			CancelingUserLogin:    "bob",
			TargetPublicSessionID: "ps-reconcile",
			Status:                state.StatusQueued,
			CreatedAt:             now,
		})
	})
}

type staticAcpxFactory struct {
	coordinator Coordinator
}

func (f staticAcpxFactory) NewCoordinator(ExecutionEnvironment) (Coordinator, error) {
	return f.coordinator, nil
}

type fakeReconcileCoordinator struct {
	reconcileResult acpx.TurnReconcileResult
	reconcileErr    error
	reconcileCalls  int
}

func (f *fakeReconcileCoordinator) NewSession(context.Context, acpx.NewSessionRequest) (acpx.DispatchResult, error) {
	return acpx.DispatchResult{}, errors.New("unexpected new session")
}

func (f *fakeReconcileCoordinator) Resume(context.Context, acpx.ResumeRequest) (acpx.DispatchResult, error) {
	return acpx.DispatchResult{}, errors.New("unexpected resume")
}

func (f *fakeReconcileCoordinator) ReconcileTurn(context.Context, acpx.TurnReconcileRequest) (acpx.TurnReconcileResult, error) {
	f.reconcileCalls++
	return f.reconcileResult, f.reconcileErr
}

type fakeRefreshCoordinator struct {
	metadata     acpx.Metadata
	refreshErr   error
	refreshCalls int
	newCalls     int
	resumeCalls  int
}

func (f *fakeRefreshCoordinator) NewSession(context.Context, acpx.NewSessionRequest) (acpx.DispatchResult, error) {
	f.newCalls++
	return acpx.DispatchResult{}, errors.New("unexpected new session")
}

func (f *fakeRefreshCoordinator) Resume(context.Context, acpx.ResumeRequest) (acpx.DispatchResult, error) {
	f.resumeCalls++
	return acpx.DispatchResult{}, errors.New("unexpected resume")
}

func (f *fakeRefreshCoordinator) Refresh(context.Context, acpx.SessionRef) (acpx.Metadata, error) {
	f.refreshCalls++
	return f.metadata, f.refreshErr
}

type fakeCancelCoordinator struct {
	cancelResult acpx.CancelResult
	cancelErr    error
	cancelCalls  int
}

func (f *fakeCancelCoordinator) NewSession(context.Context, acpx.NewSessionRequest) (acpx.DispatchResult, error) {
	return acpx.DispatchResult{}, errors.New("unexpected new session")
}

func (f *fakeCancelCoordinator) Resume(context.Context, acpx.ResumeRequest) (acpx.DispatchResult, error) {
	return acpx.DispatchResult{}, errors.New("unexpected resume")
}

func (f *fakeCancelCoordinator) Cancel(context.Context, acpx.SessionRef) (acpx.CancelResult, error) {
	f.cancelCalls++
	return f.cancelResult, f.cancelErr
}
