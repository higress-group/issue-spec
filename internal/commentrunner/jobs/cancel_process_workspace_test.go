package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/processworkspace"
)

func TestManagerAllocatorCancellationCleanupUsesIntegratedEvidenceOrRetention(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_000, 0).UTC()
	tests := []struct {
		name    string
		mutate  func(*processworkspace.Inspection)
		allowed bool
	}{
		{name: "prepared change is retained"},
		{name: "integrated evidence permits cleanup", allowed: true, mutate: func(inspection *processworkspace.Inspection) {
			inspection.Lease.Portable.State = processworkspace.StateIntegrated
			inspection.Lease.Portable.ResultCommit = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
			inspection.Lease.Portable.IntegrationSHA = "cccccccccccccccccccccccccccccccccccccccc"
			inspection.Head = inspection.Lease.Portable.ResultCommit
		}},
		{name: "locally cleaned integrated lease permits release retry", allowed: true, mutate: func(inspection *processworkspace.Inspection) {
			inspection.Lease.Portable.State = processworkspace.StateCleaned
			inspection.Lease.Portable.ResultCommit = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
			inspection.Lease.Portable.IntegrationSHA = "cccccccccccccccccccccccccccccccccccccccc"
			inspection.Registered = false
			inspection.Present = false
		}},
		{name: "elapsed retention permits cleanup", allowed: true, mutate: func(inspection *processworkspace.Inspection) {
			inspection.Lease.Portable.RetentionExpiresAt = now.Add(-time.Second)
		}},
		{name: "future retention retains workspace", mutate: func(inspection *processworkspace.Inspection) {
			inspection.Lease.Portable.RetentionExpiresAt = now.Add(time.Hour)
		}},
	}
	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newMemoryProcessWorkspaceStore()
			manager := &fakeProcessWorkspaceManager{}
			allocator := testAllocator(store, manager)
			request := writableAllocationRequest("ws-cancel-policy-"+string(rune('a'+index)), "PROCESS-022")
			allocation, err := allocator.Allocate(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			manager.mu.Lock()
			inspection := manager.prepared[request.WorkspaceID]
			if tt.mutate != nil {
				tt.mutate(&inspection)
			}
			manager.prepared[request.WorkspaceID] = inspection
			manager.mu.Unlock()
			assignment := state.ProcessWorkspaceAssignment{
				ProcessID: allocation.Association.ProcessID, WorkspaceID: allocation.Association.WorkspaceID,
				ReservationID: allocation.Association.ReservationID, ReservationIdentity: allocation.Association.ReservationIdentity,
				AssociationGeneration: allocation.Generation,
			}
			allowed, err := allocator.AllowProcessWorkspaceCleanup(ctx, assignment, now)
			if err != nil || allowed != tt.allowed {
				t.Fatalf("allowed=%v err=%v, want %v", allowed, err, tt.allowed)
			}
			if allowed {
				released, err := allocator.CleanupAndRelease(ctx, assignment.WorkspaceID, assignment.ReservationID)
				if err != nil || released.Lifecycle != state.ProcessWorkspaceReleased {
					t.Fatalf("released=%+v err=%v", released, err)
				}
			}
		})
	}
}

func TestCancellationProcessWorkspaceCleanupPolicy(t *testing.T) {
	now := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name         string
		allowed      bool
		cleanupErr   error
		wantErr      error
		wantCalls    int
		wantRequired bool
		wantState    state.ProcessWorkspaceCleanupState
	}{
		{name: "change-bearing not integrated is retained", wantErr: errProcessWorkspaceCleanupDeferred, wantRequired: true, wantState: state.ProcessWorkspaceAssignmentCleanupPending},
		{name: "integrated or elapsed retention policy permits release", allowed: true, wantCalls: 1, wantState: state.ProcessWorkspaceAssignmentCleanupConfirmed},
		{name: "cleanup failure preserves retryable intent", allowed: true, cleanupErr: errors.New("cleanup unavailable"), wantCalls: 1, wantRequired: true, wantState: state.ProcessWorkspaceAssignmentCleanupPending},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newMemoryStore()
			assignment := cancellationTestAssignment()
			seedCancellationCleanupJob(t, store, assignment, now)
			allocator := &policyProcessAllocator{allowed: tt.allowed, cleanupErr: tt.cleanupErr}
			dispatcher := testDispatcher(store, &fakeWorkspaces{}, &fakeCoordinator{}, &fakeWriteback{}, now)
			dispatcher.Workspaces = &cancelWorkspaceProvider{fakeWorkspaces: &fakeWorkspaces{}, allocator: allocator}
			job := loadState(t, store).Jobs["job-cleanup-policy"]

			err := dispatcher.cleanupAssignedProcessWorkspace(context.Background(), job)
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("cleanup error = %v, want %v", err, tt.wantErr)
			}
			if tt.cleanupErr != nil && !errors.Is(err, tt.cleanupErr) {
				t.Fatalf("cleanup error = %v, want %v", err, tt.cleanupErr)
			}
			if tt.wantErr == nil && tt.cleanupErr == nil && err != nil {
				t.Fatalf("cleanup error = %v", err)
			}
			if allocator.cleanupCalls != tt.wantCalls {
				t.Fatalf("CleanupAndRelease calls = %d, want %d", allocator.cleanupCalls, tt.wantCalls)
			}
			if err := dispatcher.recordJobWorkspaceCleanupResult(context.Background(), job.ID, err); err != nil {
				t.Fatal(err)
			}
			got := loadState(t, store).Jobs[job.ID].ProcessWorkspace
			if got == nil || got.CleanupRequired != tt.wantRequired || got.CleanupState != tt.wantState {
				t.Fatalf("durable cleanup result = %+v", got)
			}
		})
	}
}

func TestRepeatedCancellationCleanupIsIdempotentAfterConfirmation(t *testing.T) {
	now := time.Date(2026, 7, 13, 11, 0, 0, 0, time.UTC)
	store := newMemoryStore()
	seedCancellationCleanupJob(t, store, cancellationTestAssignment(), now)
	allocator := &policyProcessAllocator{allowed: true}
	dispatcher := testDispatcher(store, &fakeWorkspaces{}, &fakeCoordinator{}, &fakeWriteback{}, now)
	dispatcher.Workspaces = &cancelWorkspaceProvider{fakeWorkspaces: &fakeWorkspaces{}, allocator: allocator}

	job := loadState(t, store).Jobs["job-cleanup-policy"]
	if err := dispatcher.recordJobWorkspaceCleanupResult(context.Background(), job.ID, dispatcher.cleanupAssignedProcessWorkspace(context.Background(), job)); err != nil {
		t.Fatal(err)
	}
	confirmed := loadState(t, store).Jobs[job.ID]
	if err := dispatcher.cleanupAssignedProcessWorkspace(context.Background(), confirmed); err != nil {
		t.Fatalf("repeated cleanup = %v", err)
	}
	if allocator.cleanupCalls != 1 || allocator.policyCalls != 1 {
		t.Fatalf("repeated cancellation re-entered cleanup: policy=%d cleanup=%d", allocator.policyCalls, allocator.cleanupCalls)
	}
}

func TestCancelQueuedJobRetainsUnintegratedProcessWorkspace(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	store := newMemoryStore()
	seedActiveJob(t, store, state.StatusQueued, testBinding("ws-queued-cancel").Workspace, state.SessionLock{})
	seedState(t, store, func(st *state.RunnerState) error {
		job := st.Jobs["job-reconcile"]
		assignment := cancellationTestAssignment()
		assignment.CleanupRequired = false
		assignment.CleanupState = ""
		job.ProcessWorkspace = &assignment
		return st.UpsertJob(job)
	})
	seedCancellation(t, store, "cancel-queued-policy", "cancel-queued-key", now)
	allocator := &policyProcessAllocator{allowed: false}
	writebacks := &fakeWriteback{store: store}
	dispatcher := testDispatcher(store, &fakeWorkspaces{}, &fakeCoordinator{}, writebacks, now)
	dispatcher.Workspaces = &cancelWorkspaceProvider{fakeWorkspaces: &fakeWorkspaces{}, allocator: allocator}
	st := loadState(t, store)

	if err := dispatcher.cancelQueuedJob(context.Background(), st.Cancellations["cancel-queued-policy"], st.Jobs["job-reconcile"]); err != nil {
		t.Fatal(err)
	}
	job := loadState(t, store).Jobs["job-reconcile"]
	if job.Status != state.StatusCancelled || job.ProcessWorkspace == nil || !job.ProcessWorkspace.CleanupRequired ||
		job.ProcessWorkspace.CleanupState != state.ProcessWorkspaceAssignmentCleanupPending || allocator.cleanupCalls != 0 {
		t.Fatalf("queued cancellation cleanup state=%+v cleanupCalls=%d", job, allocator.cleanupCalls)
	}
}

func TestTerminalCancellationCleanupPropagatesDurableMarkAndRecordFailures(t *testing.T) {
	now := time.Date(2026, 7, 13, 13, 0, 0, 0, time.UTC)
	paths := []struct {
		name string
		run  func(context.Context, *Dispatcher, state.Cancellation, state.Job) error
	}{
		{name: "repeated terminal cancellation", run: func(ctx context.Context, dispatcher *Dispatcher, cancel state.Cancellation, _ state.Job) error {
			_, err := dispatcher.cancel(ctx, cancel)
			return err
		}},
		{name: "already terminal target", run: func(ctx context.Context, dispatcher *Dispatcher, cancel state.Cancellation, job state.Job) error {
			_, err := dispatcher.cancelAlreadyTerminal(ctx, cancel, job)
			return err
		}},
	}
	for _, path := range paths {
		for _, failure := range []struct {
			name       string
			failAt     int
			wantCalls  int
			wantIntent bool
		}{
			{name: "mark failure blocks physical cleanup", failAt: 1},
			{name: "record failure is returned after idempotent release", failAt: 2, wantCalls: 1, wantIntent: true},
		} {
			t.Run(path.name+"/"+failure.name, func(t *testing.T) {
				store := newMemoryStore()
				assignment := cancellationTestAssignment()
				assignment.CleanupRequired = false
				assignment.CleanupState = ""
				seedCancellationCleanupJob(t, store, assignment, now)
				allocator := &policyProcessAllocator{allowed: true}
				dispatcher := testDispatcher(store, &fakeWorkspaces{}, &fakeCoordinator{}, &fakeWriteback{}, now)
				dispatcher.Workspaces = &cancelWorkspaceProvider{fakeWorkspaces: &fakeWorkspaces{}, allocator: allocator}
				injected := errors.New("injected durable update failure")
				dispatcher.Store = &failingCancelUpdateStore{Store: store, failAt: failure.failAt, err: injected}
				job := loadState(t, store).Jobs["job-cleanup-policy"]
				cancel := state.Cancellation{ID: "cancel-terminal-policy", Status: state.StatusCancelled, TargetJobID: job.ID}

				err := path.run(context.Background(), dispatcher, cancel, job)
				if !errors.Is(err, injected) {
					t.Fatalf("terminal cleanup error = %v, want injected update failure", err)
				}
				if allocator.cleanupCalls != failure.wantCalls {
					t.Fatalf("CleanupAndRelease calls = %d, want %d", allocator.cleanupCalls, failure.wantCalls)
				}
				got := loadState(t, store).Jobs[job.ID].ProcessWorkspace
				if got == nil || got.CleanupRequired != failure.wantIntent ||
					(failure.wantIntent && got.CleanupState != state.ProcessWorkspaceAssignmentCleanupRequired) {
					t.Fatalf("durable intent after failure = %+v, wantIntent=%v", got, failure.wantIntent)
				}
			})
		}
	}
}

// Keep the pre-existing cancellation fixture on the guarded contract. Its
// scenarios exercise confirmed cancellation, a failed cleanup, and retry.
func (a *recordingProcessAllocator) AllowProcessWorkspaceCleanup(context.Context, state.ProcessWorkspaceAssignment, time.Time) (bool, error) {
	return true, nil
}

type policyProcessAllocator struct {
	Allocator
	allowed                   bool
	policyErr, cleanupErr     error
	policyCalls, cleanupCalls int
}

type failingCancelUpdateStore struct {
	Store
	failAt int
	calls  int
	err    error
}

func (s *failingCancelUpdateStore) Update(ctx context.Context, mutate func(*state.RunnerState) error) error {
	s.calls++
	if s.calls == s.failAt {
		return s.err
	}
	return s.Store.Update(ctx, mutate)
}

func (a *policyProcessAllocator) AllowProcessWorkspaceCleanup(context.Context, state.ProcessWorkspaceAssignment, time.Time) (bool, error) {
	a.policyCalls++
	return a.allowed, a.policyErr
}

func (a *policyProcessAllocator) CleanupAndRelease(_ context.Context, workspaceID, reservationID string) (state.ProcessWorkspaceAssociation, error) {
	a.cleanupCalls++
	return state.ProcessWorkspaceAssociation{WorkspaceID: workspaceID, ReservationID: reservationID}, a.cleanupErr
}

func cancellationTestAssignment() state.ProcessWorkspaceAssignment {
	return state.ProcessWorkspaceAssignment{
		ProcessID:             "PROCESS-022",
		WorkspaceID:           "ws-process-022",
		ReservationID:         "reservation:process-022",
		AssociationGeneration: 1,
		ReservationIdentity:   "identity:0123456789abcdef0123456789abcdef",
		CleanupRequired:       true,
		CleanupState:          state.ProcessWorkspaceAssignmentCleanupRequired,
	}
}

func seedCancellationCleanupJob(t *testing.T, store *memoryStore, assignment state.ProcessWorkspaceAssignment, now time.Time) {
	t.Helper()
	seedState(t, store, func(st *state.RunnerState) error {
		return st.UpsertJob(state.Job{
			ID: "job-cleanup-policy", Repo: "o/r", IssueNumber: 177,
			Status: state.StatusCancelled, StatusWritebackKey: "status-cleanup-policy",
			CreatedAt: now, UpdatedAt: now, FinishedAt: now,
			Workspace:        testBinding("ws-cleanup-policy").Workspace,
			ProcessWorkspace: &assignment,
		})
	})
}
