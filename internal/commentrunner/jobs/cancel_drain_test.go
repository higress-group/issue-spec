package jobs

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/acpx"
	"github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/workspace"
)

// TestDrainCancellationsCancelsBlockedDispatchWithoutConflictingTerminal is the
// TASK-001 regression test for the out-of-band cancellation race: a job whose
// acpx dispatch is blocked in-flight must still be cancellable, and the
// cancellation must win over the eventual (now-stale) dispatch completion so no
// conflicting terminal writeback is produced.
func TestDrainCancellationsCancelsBlockedDispatchWithoutConflictingTerminal(t *testing.T) {
	tests := []struct {
		name        string
		dispatch    acpx.DispatchResult
		dispatchErr error
	}{
		{name: "stale success", dispatch: dispatchResult("ps-blocked", "rec-blocked", "turn-blocked", completedSummary())},
		{name: "stale sandbox failure", dispatchErr: errors.New("set-mode error=sandbox config invalid: credential capability source must be a private regular file")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newMemoryStore()
			now := time.Date(2026, 7, 5, 10, 0, 0, 0, time.UTC)
			seedQueuedJob(t, store, state.Job{
				ID:                    "job-blocked",
				Repo:                  "o/r",
				IssueNumber:           30,
				CoordinatorKind:       "codex",
				Model:                 "gpt-5.5[xhigh]",
				SessionCreatorLogin:   "alice",
				TriggeringUserLogin:   "alice",
				TriggerCommentID:      601,
				CommandID:             "cmd-blocked",
				CommandName:           "new",
				CommandPrompt:         "start blocked work",
				CommandIdempotencyKey: "cmd-key-blocked",
				StatusWritebackKey:    "status-blocked",
				Status:                state.StatusQueued,
				CreatedAt:             now,
			})

			workspaces := &fakeWorkspaces{binding: testBinding("ws-blocked")}
			writebacks := &fakeWriteback{store: store}
			coordinator := &blockingCancelCoordinator{
				started:      make(chan struct{}, 1),
				release:      make(chan struct{}),
				newResult:    tt.dispatch,
				newErr:       tt.dispatchErr,
				cancelResult: acpx.CancelResult{Confirmed: true, Diagnostics: "cancelled mid-dispatch"},
			}
			dispatcher := testDispatcher(store, workspaces, &fakeCoordinator{}, writebacks, now)
			dispatcher.Acpx = staticAcpxFactory{coordinator: coordinator}
			dispatcher.PublicSessionID = func() (string, error) { return "ps-blocked", nil }

			type runOutcome struct {
				result Result
				err    error
			}
			done := make(chan runOutcome, 1)
			go func() {
				result, err := dispatcher.RunNext(context.Background())
				done <- runOutcome{result: result, err: err}
			}()

			// Wait until the job has been marked running and its acpx dispatch is blocked.
			select {
			case <-coordinator.started:
			case <-time.After(time.Second):
				t.Fatal("timed out waiting for blocked dispatch to start")
			}

			// The job is now Running with an in-flight dispatch; queue a cancellation for it.
			seedBlockedCancellation(t, store, "cancel-blocked", "cancel-key-blocked", "ps-blocked", now)

			drain, err := dispatcher.DrainCancellations(context.Background(), 0)
			if err != nil {
				t.Fatalf("DrainCancellations returned error: %v", err)
			}
			if !drain.Executed || drain.CancellationID != "cancel-blocked" || drain.Status != state.StatusCancelled {
				t.Fatalf("unexpected drain result: %+v", drain)
			}
			if coordinator.cancelCalls != 1 {
				t.Fatalf("acpx cancel calls = %d, want 1", coordinator.cancelCalls)
			}

			// Release the stale dispatch. Success and the live credential-removal
			// failure must both collapse into the already-durable cancellation,
			// never a dispatcher error that terminates runner serve.
			close(coordinator.release)
			var outcome runOutcome
			select {
			case outcome = <-done:
			case <-time.After(time.Second):
				t.Fatal("timed out waiting for blocked dispatch goroutine to finish")
			}
			if outcome.err != nil || outcome.result.Status != state.StatusCancelled || outcome.result.Reason != "cancelled_during_dispatch" {
				t.Fatalf("stale dispatch outcome = %+v err=%v, want benign cancellation", outcome.result, outcome.err)
			}

			st := loadState(t, store)
			if job := st.Jobs["job-blocked"]; job.Status != state.StatusCancelled || job.AcpxRecordID != "" {
				t.Fatalf("cancelled job accepted stale dispatch metadata: %+v", job)
			}
			if cancel := st.Cancellations["cancel-blocked"]; cancel.Status != state.StatusCancelled {
				t.Fatalf("cancellation status = %s, want cancelled", cancel.Status)
			}
			session, ok := st.GetPublicSession("o/r", "ps-blocked")
			if !ok || session.Status != state.StatusCancelled || session.AcpxRecordID != "" || len(session.Queue.PendingJobIDs) != 0 || session.Lock.OwnerJobID != "" {
				t.Fatalf("cancelled no-record session retained running state: %+v ok=%v", session, ok)
			}
			if !workspaces.released {
				t.Fatal("workspace lock was not released")
			}
			// Only the running and cancelled writebacks must be present; the stale
			// dispatch completion must NOT emit a conflicting completed writeback.
			assertWritebackStatuses(t, writebacks, state.StatusRunning, state.StatusCancelled)
			for _, req := range writebacks.requests {
				if req.Status == state.StatusCompleted || req.Status == state.StatusFailed {
					t.Fatalf("stale dispatch produced a conflicting terminal writeback: %+v", req)
				}
			}
		})
	}
}

// TestDrainCancellationsDoesNotDispatchQueuedJobs is the TASK-001 unit test:
// DrainCancellations processes only queued cancellations and never falls through
// to job dispatch, so an unrelated queued job is left untouched.
func TestDrainCancellationsDoesNotDispatchQueuedJobs(t *testing.T) {
	store := newMemoryStore()
	now := time.Date(2026, 7, 5, 11, 0, 0, 0, time.UTC)
	workspaceMeta := testBinding("ws-cancel-drain").Workspace
	seedActiveJob(t, store, state.StatusRunning, workspaceMeta, state.SessionLock{OwnerJobID: "job-reconcile"})
	seedCancellation(t, store, "cancel-1", "cancel-key-1", now)
	seedQueuedJob(t, store, state.Job{
		ID:                    "job-untouched",
		Repo:                  "o/r",
		IssueNumber:           30,
		CoordinatorKind:       "codex",
		Model:                 "gpt-5.5[xhigh]",
		SessionCreatorLogin:   "carol",
		TriggeringUserLogin:   "carol",
		TriggerCommentID:      611,
		CommandID:             "cmd-untouched",
		CommandName:           "new",
		CommandPrompt:         "should not dispatch",
		CommandIdempotencyKey: "cmd-key-untouched",
		StatusWritebackKey:    "status-untouched",
		Status:                state.StatusQueued,
		CreatedAt:             now,
	})

	writebacks := &fakeWriteback{store: store}
	workspaces := &fakeWorkspaces{binding: testBinding("unused")}
	coordinator := &fakeCancelCoordinator{cancelResult: acpx.CancelResult{Confirmed: true}}
	dispatcher := testDispatcher(store, workspaces, &fakeCoordinator{}, writebacks, now)
	dispatcher.Acpx = staticAcpxFactory{coordinator: coordinator}

	result, err := dispatcher.DrainCancellations(context.Background(), 0)
	if err != nil {
		t.Fatalf("DrainCancellations returned error: %v", err)
	}
	if !result.Executed || result.CancellationID != "cancel-1" || result.Status != state.StatusCancelled {
		t.Fatalf("unexpected drain result: %+v", result)
	}
	if coordinator.cancelCalls != 1 {
		t.Fatalf("acpx cancel calls = %d, want 1", coordinator.cancelCalls)
	}
	st := loadState(t, store)
	if got := st.Jobs["job-untouched"].Status; got != state.StatusQueued {
		t.Fatalf("queued job status = %s, want queued (drain must not dispatch jobs)", got)
	}
	if got := st.Cancellations["cancel-1"].Status; got != state.StatusCancelled {
		t.Fatalf("cancellation status = %s, want cancelled", got)
	}
}

func TestRunJobsReadyLeavesCancellationForDedicatedDrain(t *testing.T) {
	store := newMemoryStore()
	now := time.Date(2026, 7, 5, 11, 30, 0, 0, time.UTC)
	seedCancellation(t, store, "cancel-dedicated", "cancel-key-dedicated", now)
	dispatcher := testDispatcher(store, &fakeWorkspaces{}, &fakeCoordinator{}, &fakeWriteback{}, now)
	result, err := dispatcher.RunJobsReady(t.Context(), 1)
	if err != nil || result.Executed || result.Reason != ErrNoReadyJob.Error() {
		t.Fatalf("job-only dispatch=%+v err=%v", result, err)
	}
	if got := loadState(t, store).Cancellations["cancel-dedicated"].Status; got != state.StatusQueued {
		t.Fatalf("job-only dispatch consumed cancellation: %s", got)
	}
}

// TestDrainCancellationsCancelsRunningJobWithoutRecordID is the TASK-002 unit
// test: an in-flight /new job that has a public session id but no persisted acpx
// record id must still be cancellable, since acpx cancel targets the session by
// name.
func TestDrainCancellationsCancelsRunningJobWithoutRecordID(t *testing.T) {
	store := newMemoryStore()
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	workspaceMeta := testBinding("ws-norec").Workspace
	manager := workspace.Manager{Root: t.TempDir(), Retention: time.Hour, Now: func() time.Time { return now }, TokenFunc: func() (string, error) { return "physical-lock-token", nil }}
	lock, err := manager.AcquireLock(context.Background(), workspace.LockRequest{Repo: "o/r", PublicSessionID: "ps-norec", JobID: "job-norec", WorkspaceID: workspaceMeta.ID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.ReleaseLock(lock) })
	seedState(t, store, func(st *state.RunnerState) error {
		if err := st.UpsertWorkspace(workspaceMeta); err != nil {
			return err
		}
		job := state.Job{
			ID:                  "job-norec",
			Repo:                "o/r",
			IssueNumber:         30,
			PublicSessionID:     "ps-norec",
			CoordinatorKind:     "codex",
			Model:               "gpt-5.5[xhigh]",
			SessionCreatorLogin: "alice",
			TriggeringUserLogin: "alice",
			TriggerCommentID:    620,
			StatusWritebackKey:  "status-norec",
			Status:              state.StatusRunning,
			CreatedAt:           now,
			UpdatedAt:           now,
			Workspace:           workspaceMeta,
			RepositoryBinding:   testRepositoryBinding(),
			DispatchIntent: state.DispatchIntent{
				RunnerJobID:     "job-norec",
				PublicSessionID: "ps-norec",
			},
		}
		if err := st.UpsertJob(job); err != nil {
			return err
		}
		return st.UpsertPublicSession(state.PublicSession{
			Repo: "o/r", PublicSessionID: "ps-norec", IssueNumber: 30, CreatorLogin: "alice",
			Status: state.StatusRunning, Workspace: workspaceMeta, RepositoryBinding: testRepositoryBinding(),
			Queue: state.SessionQueue{AcceptedSequence: 1, PendingJobIDs: []string{"job-norec"}}, Lock: lock,
			CreatedAt: now, LastUsedAt: now, LastJobID: "job-norec",
		})
	})
	seedBlockedCancellation(t, store, "cancel-norec", "cancel-key-norec", "ps-norec", now)

	writebacks := &fakeWriteback{store: store}
	workspaces := &fakeWorkspaces{binding: testBinding("unused")}
	coordinator := &fakeCancelCoordinator{cancelResult: acpx.CancelResult{Confirmed: true, Diagnostics: "cancelled by acpx"}}
	dispatcher := testDispatcher(store, workspaces, &fakeCoordinator{}, writebacks, now)
	dispatcher.Workspaces = manager
	dispatcher.Acpx = staticAcpxFactory{coordinator: coordinator}

	result, err := dispatcher.DrainCancellations(context.Background(), 0)
	if err != nil {
		t.Fatalf("DrainCancellations returned error: %v", err)
	}
	if !result.Executed || result.JobID != "job-norec" || result.Status != state.StatusCancelled {
		t.Fatalf("unexpected cancel result for record-id-less job: %+v", result)
	}
	if coordinator.cancelCalls != 1 {
		t.Fatalf("acpx cancel calls = %d, want 1", coordinator.cancelCalls)
	}
	st := loadState(t, store)
	if got := st.Jobs["job-norec"].Status; got != state.StatusCancelled {
		t.Fatalf("record-id-less job status = %s, want cancelled", got)
	}
	if got := st.Cancellations["cancel-norec"].Status; got != state.StatusCancelled {
		t.Fatalf("cancellation status = %s, want cancelled", got)
	}
	session, ok := st.GetPublicSession("o/r", "ps-norec")
	if !ok || session.Status != state.StatusCancelled || session.AcpxRecordID != "" || len(session.Queue.PendingJobIDs) != 0 || session.Lock.OwnerJobID != "" {
		t.Fatalf("record-id-less public session not terminalized atomically: %+v ok=%v", session, ok)
	}
	if _, err := os.Stat(lock.WorkspaceLockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("physical workspace lock still present after cancellation: %v", err)
	}
	reacquired, err := manager.AcquireLock(context.Background(), workspace.LockRequest{Repo: "o/r", PublicSessionID: "ps-norec", JobID: "job-after-cancel", WorkspaceID: workspaceMeta.ID})
	if err != nil {
		t.Fatalf("physical workspace lock was not reusable after cancellation: %v", err)
	}
	if err := manager.ReleaseLock(reacquired); err != nil {
		t.Fatal(err)
	}
	assertWritebackStatuses(t, writebacks, state.StatusCancelled)
}

// TestReconcileRejectsJobMissingRecordID is the TASK-002 guard test: cancellation
// relaxed the session-ref requirement, but reconciliation must still refuse to
// reconcile a job that lacks a stable acpx record id (it interrupts instead).
func TestReconcileRejectsJobMissingRecordID(t *testing.T) {
	store := newMemoryStore()
	now := time.Date(2026, 7, 5, 13, 0, 0, 0, time.UTC)
	workspaceMeta := testBinding("ws-reconcile-norec").Workspace
	seedState(t, store, func(st *state.RunnerState) error {
		if err := st.UpsertWorkspace(workspaceMeta); err != nil {
			return err
		}
		return st.UpsertJob(state.Job{
			ID:                  "job-reconcile-norec",
			Repo:                "o/r",
			IssueNumber:         30,
			PublicSessionID:     "ps-reconcile-norec",
			CoordinatorKind:     "codex",
			Model:               "gpt-5.5[xhigh]",
			SessionCreatorLogin: "alice",
			TriggeringUserLogin: "alice",
			TriggerCommentID:    630,
			StatusWritebackKey:  "status-reconcile-norec",
			Status:              state.StatusRunning,
			CreatedAt:           now,
			UpdatedAt:           now,
			Workspace:           workspaceMeta,
			DispatchIntent: state.DispatchIntent{
				RunnerJobID:     "job-reconcile-norec",
				PublicSessionID: "ps-reconcile-norec",
			},
		})
	})

	writebacks := &fakeWriteback{store: store}
	workspaces := &fakeWorkspaces{binding: testBinding("unused")}
	coordinator := &fakeCancelCoordinator{}
	dispatcher := testDispatcher(store, workspaces, &fakeCoordinator{}, writebacks, now)
	dispatcher.Acpx = staticAcpxFactory{coordinator: coordinator}

	result, err := dispatcher.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if result.Interrupted != 1 {
		t.Fatalf("unexpected reconcile result: %+v", result)
	}
	if coordinator.cancelCalls != 0 {
		t.Fatalf("reconcile should not reach the coordinator for a record-id-less job: calls=%d", coordinator.cancelCalls)
	}
	foundDiagnostic := false
	for _, diagnostic := range result.Diagnostics {
		if strings.Contains(diagnostic, "missing stable acpx record id") {
			foundDiagnostic = true
			break
		}
	}
	if !foundDiagnostic {
		t.Fatalf("reconcile diagnostics missing record-id rejection: %+v", result.Diagnostics)
	}
	if got := loadState(t, store).Jobs["job-reconcile-norec"].Status; got != state.StatusInterrupted {
		t.Fatalf("record-id-less job status = %s, want interrupted", got)
	}
}

func seedBlockedCancellation(t *testing.T, store *memoryStore, id, key, targetPublicSessionID string, now time.Time) {
	t.Helper()
	seedState(t, store, func(st *state.RunnerState) error {
		return st.UpsertCancellation(state.Cancellation{
			ID:                    id,
			IdempotencyKey:        key,
			Repo:                  "o/r",
			IssueNumber:           30,
			TriggerCommentID:      707,
			CancelingUserLogin:    "bob",
			TargetPublicSessionID: targetPublicSessionID,
			Status:                state.StatusQueued,
			CreatedAt:             now,
		})
	})
}

// blockingCancelCoordinator dispatches a new session that blocks until released,
// while still supporting an out-of-band cancel. It is used to exercise the
// blocked-dispatch cancellation race.
type blockingCancelCoordinator struct {
	started      chan struct{}
	release      chan struct{}
	newResult    acpx.DispatchResult
	newErr       error
	cancelResult acpx.CancelResult
	cancelErr    error
	cancelCalls  int
}

func (c *blockingCancelCoordinator) NewSession(_ context.Context, _ acpx.NewSessionRequest) (acpx.DispatchResult, error) {
	select {
	case c.started <- struct{}{}:
	default:
	}
	<-c.release
	return c.newResult, c.newErr
}

func (c *blockingCancelCoordinator) Resume(context.Context, acpx.ResumeRequest) (acpx.DispatchResult, error) {
	return acpx.DispatchResult{}, errors.New("unexpected resume")
}

func (c *blockingCancelCoordinator) Cancel(context.Context, acpx.SessionRef) (acpx.CancelResult, error) {
	c.cancelCalls++
	return c.cancelResult, c.cancelErr
}
