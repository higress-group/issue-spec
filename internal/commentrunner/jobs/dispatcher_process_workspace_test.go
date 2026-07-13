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
	runnercontext "github.com/higress-group/issue-spec/internal/commentrunner/context"
	"github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/processworkspace"
	"github.com/higress-group/issue-spec/internal/workspace"
)

func TestPrepareProcessWorkspacePersistsExactAssignmentAndBindsOnlyAssignedPath(t *testing.T) {
	now := time.Date(2026, 7, 13, 14, 0, 0, 0, time.UTC)
	store := newMemoryStore()
	job := state.Job{ID: "job-process-008", Repo: "o/r", IssueNumber: 177, ExactProcessID: "PROCESS-008", Status: state.StatusDispatched,
		CreatedAt: now, UpdatedAt: now, Workspace: testBinding("session-integration").Workspace}
	seedState(t, store, func(st *state.RunnerState) error { return st.UpsertJob(job) })
	assigned := t.TempDir()
	association := processLifecycleAssociation("PROCESS-008", "ws-process-008", processworkspace.ExecutionChangeBearing, processworkspace.ModeWritable)
	allocator := &processLifecycleAllocator{allocation: ProcessWorkspaceAllocation{Association: association, Generation: 9,
		Inspection: &processworkspace.Inspection{Registered: true, Present: true, Lease: processworkspace.LocalLease{Portable: processLifecycleLease(association), WorktreePath: assigned}}}}
	workspaces := &processLifecycleWorkspaceProvider{fakeWorkspaces: &fakeWorkspaces{}, allocator: allocator}
	dispatcher := testDispatcher(store, workspaces.fakeWorkspaces, &fakeCoordinator{}, &fakeWriteback{}, now)
	dispatcher.Workspaces = workspaces
	binding := testBinding("session-integration")
	portable := processLifecycleLease(association)
	section, err := model.RenderProcessWorkspaceSection(portable)
	if err != nil {
		t.Fatal(err)
	}
	body, err := model.EnsureTypedBody("PROCESS", "PROCESS-008", "## Process\n\n### Parent TASK\n\n- TASK-004\n\n### Execution Class\n\n- change-bearing\n\n"+section, model.BodyOptions{Status: "in-progress"})
	if err != nil {
		t.Fatal(err)
	}
	artifacts := []model.Artifact{{Issue: 177, CommentID: 8, Comment: model.ParseTypedComment(body)}}

	gotBinding, gotJob, class, err := dispatcher.prepareProcessWorkspace(context.Background(), job, fakeRepoResolverResolution(), binding, artifacts)
	if err != nil {
		t.Fatal(err)
	}
	if class != processworkspace.ExecutionChangeBearing || gotBinding.AcpxWorkingDirectory != assigned || gotBinding.SandboxWorkspacePath != assigned {
		t.Fatalf("binding=%+v class=%s", gotBinding, class)
	}
	if gotBinding.Workspace.Path != binding.Workspace.Path {
		t.Fatalf("session integration root was replaced: %q", gotBinding.Workspace.Path)
	}
	if gotJob.ProcessWorkspace == nil || gotJob.ProcessWorkspace.ProcessID != "PROCESS-008" || gotJob.ProcessWorkspace.AssociationGeneration != 9 {
		t.Fatalf("durable assignment=%+v", gotJob.ProcessWorkspace)
	}
}

func TestPrepareProcessWorkspaceEmptyExactTargetAllocatesNothing(t *testing.T) {
	store := newMemoryStore()
	now := time.Unix(100, 0).UTC()
	job := state.Job{ID: "job-orchestration", Repo: "o/r", Status: state.StatusDispatched, CreatedAt: now, UpdatedAt: now}
	seedState(t, store, func(st *state.RunnerState) error { return st.UpsertJob(job) })
	allocator := &processLifecycleAllocator{}
	workspaces := &processLifecycleWorkspaceProvider{fakeWorkspaces: &fakeWorkspaces{}, allocator: allocator}
	dispatcher := testDispatcher(store, workspaces.fakeWorkspaces, &fakeCoordinator{}, &fakeWriteback{}, now)
	dispatcher.Workspaces = workspaces
	binding := testBinding("session-integration")
	got, _, class, err := dispatcher.prepareProcessWorkspace(context.Background(), job, fakeRepoResolverResolution(), binding, nil)
	if err != nil || class != "" || allocator.allocateCalls != 0 || got.AcpxWorkingDirectory != binding.AcpxWorkingDirectory || got.SandboxWorkspacePath != binding.SandboxWorkspacePath {
		t.Fatalf("binding=%+v class=%s calls=%d err=%v", got, class, allocator.allocateCalls, err)
	}
}

func TestPrepareProcessWorkspaceExactOrchestrationUsesNoCheckout(t *testing.T) {
	store := newMemoryStore()
	now := time.Unix(101, 0).UTC()
	job := state.Job{ID: "job-exact-orchestration", Repo: "o/r", ExactProcessID: "PROCESS-024", Status: state.StatusDispatched, CreatedAt: now, UpdatedAt: now}
	seedState(t, store, func(st *state.RunnerState) error { return st.UpsertJob(job) })
	association := processLifecycleAssociation("PROCESS-024", "ws-process-024", processworkspace.ExecutionOrchestration, processworkspace.ModeNone)
	association.BaseSHA, association.Branch, association.WriteOwnership, association.LocalAssociationRef = "", "", nil, ""
	association.ReservationIdentity = association.ExpectedReservationIdentity()
	allocator := &processLifecycleAllocator{allocation: ProcessWorkspaceAllocation{Association: association, Generation: 10}}
	workspaces := &processLifecycleWorkspaceProvider{fakeWorkspaces: &fakeWorkspaces{}, allocator: allocator}
	dispatcher := testDispatcher(store, workspaces.fakeWorkspaces, &fakeCoordinator{}, &fakeWriteback{}, now)
	dispatcher.Workspaces = workspaces
	binding := testBinding("session-integration")
	body, err := model.EnsureTypedBody("PROCESS", "PROCESS-024", "## Process\n\n### Parent TASK\n\n- TASK-004\n\n### Execution Class\n\n- orchestration", model.BodyOptions{Status: "in-progress"})
	if err != nil {
		t.Fatal(err)
	}
	got, persisted, class, err := dispatcher.prepareProcessWorkspace(context.Background(), job, fakeRepoResolverResolution(), binding,
		[]model.Artifact{{Issue: 177, CommentID: 24, Comment: model.ParseTypedComment(body)}})
	if err != nil {
		t.Fatal(err)
	}
	if class != processworkspace.ExecutionOrchestration || allocator.allocateCalls != 1 || got.AcpxWorkingDirectory == "" || got.AcpxWorkingDirectory == binding.Workspace.Path || persisted.ProcessWorkspace == nil {
		t.Fatalf("binding=%+v class=%s calls=%d assignment=%+v", got, class, allocator.allocateCalls, persisted.ProcessWorkspace)
	}
}

func TestPrepareProcessWorkspaceRealFileStoreAndGitWorktree(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 13, 15, 0, 0, 0, time.UTC)
	repo := filepath.Join(t.TempDir(), "integration")
	gitRun(t, "", "init", repo)
	gitRun(t, repo, "config", "user.name", "Runner Test")
	gitRun(t, repo, "config", "user.email", "runner@example.test")
	gitRun(t, repo, "commit", "--allow-empty", "-m", "base")
	base := filepath.Clean(strings.TrimSpace(gitRun(t, repo, "rev-parse", "HEAD")))
	fileStore, err := state.OpenFileStore(filepath.Join(t.TempDir(), "runner-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer fileStore.Close()
	adapter, err := state.NewProcessWorkspaceStoreAdapter(fileStore)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewProcessWorkspaceRuntime(&fakeWorkspaces{}, adapter, filepath.Join(t.TempDir(), "managed"), nil)
	if err != nil {
		t.Fatal(err)
	}
	job := state.Job{ID: "job-real-p008", Repo: "o/r", IssueNumber: 177, ExactProcessID: "PROCESS-008", Status: state.StatusDispatched, CreatedAt: now, UpdatedAt: now,
		Workspace: state.WorkspaceMetadata{ID: "session-real", Path: repo, Repo: "o/r", CheckoutSHA: base, RepositoryBinding: testRepositoryBinding()}}
	if err := fileStore.Update(ctx, func(st *state.RunnerState) error { return st.UpsertJob(job) }); err != nil {
		t.Fatal(err)
	}
	dispatcher := &Dispatcher{Store: fileStore, Workspaces: runtime, Clock: fixedClock(now)}
	portable := processworkspace.PortableLease{SchemaVersion: processworkspace.LeaseSchemaVersion, WorkspaceID: "ws-real-p008", Repository: "o/r", ProcessID: "PROCESS-008",
		ExecutionClass: processworkspace.ExecutionChangeBearing, Mode: processworkspace.ModeWritable, BaseSHA: base, Branch: "process/real-p008",
		WriteOwnership: []string{"internal/commentrunner/jobs/**"}, RuntimeNamespace: "runtime-real-p008", State: processworkspace.StatePrepared, CreatedAt: now, UpdatedAt: now}
	section, err := model.RenderProcessWorkspaceSection(portable)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := model.EnsureTypedBody("PROCESS", "PROCESS-008", "## Process\n\n### Parent TASK\n\n- TASK-004\n\n### Execution Class\n\n- change-bearing\n\n"+section, model.BodyOptions{Status: "in-progress"})
	binding := workspaceBindingForRealRepo(job.Workspace)
	got, persisted, _, err := dispatcher.prepareProcessWorkspace(ctx, job, fakeRepoResolverResolution(), binding, []model.Artifact{{Issue: 177, CommentID: 8, Comment: model.ParseTypedComment(body)}})
	if err != nil {
		t.Fatal(err)
	}
	if persisted.ProcessWorkspace == nil || got.AcpxWorkingDirectory == repo || got.AcpxWorkingDirectory == "" {
		t.Fatalf("binding=%+v assignment=%+v", got, persisted.ProcessWorkspace)
	}
	if output := gitRun(t, repo, "worktree", "list", "--porcelain"); !strings.Contains(output, got.AcpxWorkingDirectory) {
		t.Fatalf("assigned worktree missing from git registry: %s", output)
	}
	if err := os.WriteFile(filepath.Join(got.AcpxWorkingDirectory, "dirty.txt"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := dispatcher.reconcileProcessWorkspace(ctx, persisted); err == nil {
		t.Fatal("restart accepted dirty assigned PROCESS worktree")
	}
	associations, err := adapter.LoadProcessWorkspaces(ctx)
	if err != nil {
		t.Fatal(err)
	}
	marked, ok := associations.Get(persisted.ProcessWorkspace.WorkspaceID)
	if !ok || !marked.NeedsReconcile {
		t.Fatalf("dirty restart did not mark association for reconcile: %+v", marked)
	}
}

func TestSnapshotProcessWorkspacePostconditionRejectsEveryMutation(t *testing.T) {
	tests := map[string]func(*ProcessWorkspaceAllocation){
		"dirty":                func(a *ProcessWorkspaceAllocation) { a.Inspection.Dirty = true },
		"head drift":           func(a *ProcessWorkspaceAllocation) { a.Inspection.Head = strings.Repeat("b", 40) },
		"registration missing": func(a *ProcessWorkspaceAllocation) { a.Inspection.Registered = false },
		"worktree missing":     func(a *ProcessWorkspaceAllocation) { a.Inspection.Present = false },
		"inspection problems": func(a *ProcessWorkspaceAllocation) {
			a.Inspection.Problems = []string{"detached snapshot changed"}
		},
		"needs reconcile": func(a *ProcessWorkspaceAllocation) { a.Association.NeedsReconcile = true },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			now := time.Date(2026, 7, 13, 16, 0, 0, 0, time.UTC)
			job, allocation := snapshotPostconditionFixture(processworkspace.ExecutionReview, now)
			mutate(&allocation)
			store := newMemoryStore()
			seedState(t, store, func(st *state.RunnerState) error { return st.UpsertJob(job) })
			allocator := &processLifecycleAllocator{allocation: allocation}
			workspaces := &processLifecycleWorkspaceProvider{fakeWorkspaces: &fakeWorkspaces{}, allocator: allocator}
			dispatcher := testDispatcher(store, workspaces.fakeWorkspaces, &fakeCoordinator{}, &fakeWriteback{}, now)
			dispatcher.Workspaces = workspaces
			if err := dispatcher.validateSnapshotProcessWorkspacePostcondition(context.Background(), job, processworkspace.ExecutionReview); err == nil {
				t.Fatal("mutated snapshot passed completion postcondition")
			}
		})
	}
}

func TestSnapshotProcessWorkspacePostconditionAcceptsCleanExactSnapshot(t *testing.T) {
	now := time.Date(2026, 7, 13, 16, 15, 0, 0, time.UTC)
	job, allocation := snapshotPostconditionFixture(processworkspace.ExecutionVerification, now)
	store := newMemoryStore()
	seedState(t, store, func(st *state.RunnerState) error { return st.UpsertJob(job) })
	allocator := &processLifecycleAllocator{allocation: allocation}
	workspaces := &processLifecycleWorkspaceProvider{fakeWorkspaces: &fakeWorkspaces{}, allocator: allocator}
	dispatcher := testDispatcher(store, workspaces.fakeWorkspaces, &fakeCoordinator{}, &fakeWriteback{}, now)
	dispatcher.Workspaces = workspaces
	if err := dispatcher.validateSnapshotProcessWorkspacePostcondition(context.Background(), job, processworkspace.ExecutionVerification); err != nil {
		t.Fatalf("clean exact verification snapshot was rejected: %v", err)
	}
}

func TestSnapshotProcessWorkspacePostconditionSkipsNonSnapshotClasses(t *testing.T) {
	dispatcher := &Dispatcher{}
	for _, class := range []processworkspace.ExecutionClass{
		processworkspace.ExecutionChangeBearing,
		processworkspace.ExecutionOrchestration,
		processworkspace.ExecutionExternal,
	} {
		if err := dispatcher.validateSnapshotProcessWorkspacePostcondition(context.Background(), state.Job{}, class); err != nil {
			t.Fatalf("class %s was incorrectly revalidated: %v", class, err)
		}
	}
}

func TestUnsafeSnapshotMutationFailsNormalCompletionAndPreservesCleanupEvidence(t *testing.T) {
	now := time.Date(2026, 7, 13, 16, 30, 0, 0, time.UTC)
	job, allocation := snapshotPostconditionFixture(processworkspace.ExecutionReview, now)
	allocation.Inspection.Dirty = true
	store := newMemoryStore()
	seedState(t, store, func(st *state.RunnerState) error { return st.UpsertJob(job) })
	allocator := &processLifecycleAllocator{allocation: allocation, cleanupErr: errors.New("cleanup temporarily unavailable")}
	workspaces := &processLifecycleWorkspaceProvider{fakeWorkspaces: &fakeWorkspaces{}, allocator: allocator}
	writebacks := &fakeWriteback{}
	dispatcher := testDispatcher(store, workspaces.fakeWorkspaces, &fakeCoordinator{}, writebacks, now)
	dispatcher.Workspaces = workspaces

	err := dispatcher.complete(context.Background(), job.ID, runnercontext.CommandResume, "", state.PublicSession{}, job.Workspace,
		acpx.DispatchResult{}, processworkspace.ExecutionReview, state.StatusCompleted)
	if err == nil {
		t.Fatal("unsafe dirty review snapshot completed")
	}
	failed := loadState(t, store).Jobs[job.ID]
	if failed.Status != state.StatusFailed || !failed.Sandbox.UnsafeNoSandbox || failed.ProcessWorkspace == nil ||
		!failed.ProcessWorkspace.CleanupRequired || failed.ProcessWorkspace.CleanupState != state.ProcessWorkspaceAssignmentCleanupPending ||
		failed.ProcessWorkspace.LastError != "cleanup_failed" ||
		allocator.cleanupCalls != 1 || !containsStringContaining(failed.Diagnostics, "process-workspace-postcondition") {
		t.Fatalf("failed snapshot evidence job=%+v cleanupCalls=%d", failed, allocator.cleanupCalls)
	}
	assertWritebackStatuses(t, writebacks, state.StatusFailed)
	allocator.cleanupErr = nil
	if _, err := dispatcher.cleanupTerminalProcessWorkspace(context.Background(), failed); err != nil {
		t.Fatal(err)
	}
	retried := loadState(t, store).Jobs[job.ID].ProcessWorkspace
	if retried == nil || retried.CleanupRequired || retried.CleanupState != state.ProcessWorkspaceAssignmentCleanupConfirmed || retried.LastError != "" || allocator.cleanupCalls != 2 {
		t.Fatalf("cleanup retry evidence=%+v calls=%d", retried, allocator.cleanupCalls)
	}
}

func TestUnsafeSnapshotHeadDriftFailsRecoveredCompletion(t *testing.T) {
	now := time.Date(2026, 7, 13, 17, 0, 0, 0, time.UTC)
	job, allocation := snapshotPostconditionFixture(processworkspace.ExecutionVerification, now)
	allocation.Inspection.Head = strings.Repeat("c", 40)
	store := newMemoryStore()
	seedState(t, store, func(st *state.RunnerState) error { return st.UpsertJob(job) })
	allocator := &processLifecycleAllocator{allocation: allocation}
	workspaces := &processLifecycleWorkspaceProvider{fakeWorkspaces: &fakeWorkspaces{}, allocator: allocator}
	writebacks := &fakeWriteback{}
	dispatcher := testDispatcher(store, workspaces.fakeWorkspaces, &fakeCoordinator{}, writebacks, now)
	dispatcher.Workspaces = workspaces

	result, err := dispatcher.recoveredTerminal(context.Background(), job, state.StatusRunning, state.StatusCompleted, acpx.TurnReconcileResult{
		Status: acpx.ReconcileStatusCompleted,
		Output: acpx.TurnOutput{Summary: completedSummary(), SummaryFound: true},
	}, processworkspace.ExecutionVerification, "")
	if err != nil {
		t.Fatal(err)
	}
	failed := loadState(t, store).Jobs[job.ID]
	if result.Status != state.StatusFailed || result.Action != string(state.StatusFailed) || failed.Status != state.StatusFailed ||
		failed.Restart.RecoveredStatus != state.StatusFailed || !failed.Sandbox.UnsafeNoSandbox ||
		failed.ProcessWorkspace == nil || failed.ProcessWorkspace.CleanupRequired ||
		failed.ProcessWorkspace.CleanupState != state.ProcessWorkspaceAssignmentCleanupConfirmed || allocator.cleanupCalls != 1 ||
		!containsStringContaining(failed.Diagnostics, "process-workspace-postcondition") {
		t.Fatalf("recovered snapshot result=%+v job=%+v cleanupCalls=%d", result, failed, allocator.cleanupCalls)
	}
	assertWritebackStatuses(t, writebacks, state.StatusFailed)
}

func TestUnsafeSnapshotAssignmentDriftFailsNormalCompletionAndCleansCurrentReservation(t *testing.T) {
	now := time.Date(2026, 7, 13, 17, 30, 0, 0, time.UTC)
	job, allocation := snapshotPostconditionFixture(processworkspace.ExecutionReview, now)
	replacement := driftedSnapshotAssignment(*job.ProcessWorkspace)
	baseStore := newMemoryStore()
	seedState(t, baseStore, func(st *state.RunnerState) error { return st.UpsertJob(job) })
	driftStore := &assignmentDriftStore{memoryStore: baseStore, jobID: job.ID, replacement: replacement}
	allocator := &processLifecycleAllocator{allocation: allocation}
	workspaces := &processLifecycleWorkspaceProvider{fakeWorkspaces: &fakeWorkspaces{}, allocator: allocator}
	writebacks := &fakeWriteback{}
	dispatcher := testDispatcher(baseStore, workspaces.fakeWorkspaces, &fakeCoordinator{}, writebacks, now)
	dispatcher.Store = driftStore
	dispatcher.Workspaces = workspaces

	err := dispatcher.complete(context.Background(), job.ID, runnercontext.CommandResume, "", state.PublicSession{}, job.Workspace,
		acpx.DispatchResult{}, processworkspace.ExecutionReview, state.StatusCompleted)
	if !errors.Is(err, errSnapshotProcessWorkspaceAssignmentChanged) {
		t.Fatalf("normal assignment drift error=%v", err)
	}
	failed := loadState(t, baseStore).Jobs[job.ID]
	if failed.Status != state.StatusFailed || !sameProcessWorkspaceAssignmentIdentity(failed.ProcessWorkspace, replacement) ||
		allocator.cleanupWorkspaceID != replacement.WorkspaceID || allocator.cleanupReservationID != replacement.ReservationID ||
		!containsStringContaining(failed.Diagnostics, "assignment changed after validation") {
		t.Fatalf("normal drift job=%+v cleanup=%s/%s", failed, allocator.cleanupWorkspaceID, allocator.cleanupReservationID)
	}
	assertWritebackStatuses(t, writebacks, state.StatusFailed)
}

func TestUnsafeSnapshotAssignmentDriftFailsRecoveredCompletionAndCleansCurrentReservation(t *testing.T) {
	now := time.Date(2026, 7, 13, 18, 0, 0, 0, time.UTC)
	job, allocation := snapshotPostconditionFixture(processworkspace.ExecutionVerification, now)
	replacement := driftedSnapshotAssignment(*job.ProcessWorkspace)
	baseStore := newMemoryStore()
	seedState(t, baseStore, func(st *state.RunnerState) error { return st.UpsertJob(job) })
	driftStore := &assignmentDriftStore{memoryStore: baseStore, jobID: job.ID, replacement: replacement}
	allocator := &processLifecycleAllocator{allocation: allocation}
	workspaces := &processLifecycleWorkspaceProvider{fakeWorkspaces: &fakeWorkspaces{}, allocator: allocator}
	writebacks := &fakeWriteback{}
	dispatcher := testDispatcher(baseStore, workspaces.fakeWorkspaces, &fakeCoordinator{}, writebacks, now)
	dispatcher.Store = driftStore
	dispatcher.Workspaces = workspaces

	result, err := dispatcher.recoveredTerminal(context.Background(), job, state.StatusRunning, state.StatusCompleted, acpx.TurnReconcileResult{
		Status: acpx.ReconcileStatusCompleted,
		Output: acpx.TurnOutput{Summary: completedSummary(), SummaryFound: true},
	}, processworkspace.ExecutionVerification, "")
	if err != nil {
		t.Fatal(err)
	}
	failed := loadState(t, baseStore).Jobs[job.ID]
	if result.Status != state.StatusFailed || failed.Status != state.StatusFailed || failed.Restart.RecoveredStatus != state.StatusFailed ||
		!sameProcessWorkspaceAssignmentIdentity(failed.ProcessWorkspace, replacement) ||
		allocator.cleanupWorkspaceID != replacement.WorkspaceID || allocator.cleanupReservationID != replacement.ReservationID ||
		!containsStringContaining(failed.Diagnostics, "assignment changed after validation") {
		t.Fatalf("recovered drift result=%+v job=%+v cleanup=%s/%s", result, failed, allocator.cleanupWorkspaceID, allocator.cleanupReservationID)
	}
	assertWritebackStatuses(t, writebacks, state.StatusFailed)
}

func snapshotPostconditionFixture(class processworkspace.ExecutionClass, now time.Time) (state.Job, ProcessWorkspaceAllocation) {
	association := processLifecycleAssociation("PROCESS-027", "ws-process-027", class, processworkspace.ModeSnapshot)
	association.BaseSHA = allocationTestSHA
	association.Branch = ""
	association.ReservationIdentity = association.ExpectedReservationIdentity()
	lease := processLifecycleLease(association)
	lease.Branch = ""
	lease.DetachedRevision = allocationTestSHA
	inspection := &processworkspace.Inspection{Registered: true, Present: true, Head: allocationTestSHA,
		Lease: processworkspace.LocalLease{Portable: lease, WorktreePath: filepath.Join(os.TempDir(), "snapshot-process-027")}}
	assignment := state.ProcessWorkspaceAssignment{ProcessID: association.ProcessID, WorkspaceID: association.WorkspaceID,
		ReservationID: association.ReservationID, AssociationGeneration: 11, ReservationIdentity: association.ReservationIdentity}
	job := state.Job{ID: "job-process-027", Repo: "o/r", IssueNumber: 177, ExactProcessID: association.ProcessID,
		Status: state.StatusRunning, CreatedAt: now, UpdatedAt: now, Workspace: testBinding("session-process-027").Workspace,
		Sandbox: state.SandboxMetadata{UnsafeNoSandbox: true, SandboxProvider: "none", FSBoundary: "disabled"}, ProcessWorkspace: &assignment}
	return job, ProcessWorkspaceAllocation{Association: association, Generation: 11, Inspection: inspection}
}

func driftedSnapshotAssignment(original state.ProcessWorkspaceAssignment) state.ProcessWorkspaceAssignment {
	original.WorkspaceID = "ws-process-027-drift"
	original.ReservationID = "reservation:p027-drift"
	original.ReservationIdentity = "identity:" + strings.Repeat("d", 32)
	original.AssociationGeneration++
	return original
}

func sameProcessWorkspaceAssignmentIdentity(actual *state.ProcessWorkspaceAssignment, expected state.ProcessWorkspaceAssignment) bool {
	return actual != nil && actual.ProcessID == expected.ProcessID && actual.WorkspaceID == expected.WorkspaceID &&
		actual.ReservationID == expected.ReservationID && actual.AssociationGeneration == expected.AssociationGeneration &&
		actual.ReservationIdentity == expected.ReservationIdentity
}

type assignmentDriftStore struct {
	*memoryStore
	jobID       string
	replacement state.ProcessWorkspaceAssignment
	drifted     bool
}

func (s *assignmentDriftStore) Update(ctx context.Context, mutate func(*state.RunnerState) error) error {
	if !s.drifted {
		if err := s.memoryStore.Update(ctx, func(st *state.RunnerState) error {
			job := st.Jobs[s.jobID]
			job.ProcessWorkspace = &s.replacement
			return st.UpsertJob(job)
		}); err != nil {
			return err
		}
		s.drifted = true
	}
	return s.memoryStore.Update(ctx, mutate)
}

func workspaceBindingForRealRepo(meta state.WorkspaceMetadata) workspace.Binding {
	return workspace.Binding{Workspace: meta, AcpxWorkingDirectory: meta.Path, SandboxWorkspacePath: meta.Path}
}

type processLifecycleWorkspaceProvider struct {
	*fakeWorkspaces
	allocator *processLifecycleAllocator
}

func (p *processLifecycleWorkspaceProvider) ProcessWorkspaceAllocator(context.Context, ProcessWorkspaceAllocatorRequest) (Allocator, error) {
	return p.allocator, nil
}

type processLifecycleAllocator struct {
	Allocator
	allocation           ProcessWorkspaceAllocation
	allocateCalls        int
	cleanupCalls         int
	cleanupErr           error
	cleanupWorkspaceID   string
	cleanupReservationID string
}

func (a *processLifecycleAllocator) Allocate(context.Context, ProcessWorkspaceAllocationRequest) (ProcessWorkspaceAllocation, error) {
	a.allocateCalls++
	return a.allocation, nil
}

func (a *processLifecycleAllocator) Reconcile(context.Context, string) (ProcessWorkspaceAllocation, error) {
	return a.allocation, nil
}

func (a *processLifecycleAllocator) AllowProcessWorkspaceCleanup(context.Context, state.ProcessWorkspaceAssignment, time.Time) (bool, error) {
	return true, nil
}

func (a *processLifecycleAllocator) CleanupAndRelease(_ context.Context, workspaceID, reservationID string) (state.ProcessWorkspaceAssociation, error) {
	a.cleanupCalls++
	a.cleanupWorkspaceID = workspaceID
	a.cleanupReservationID = reservationID
	released := a.allocation.Association
	released.Lifecycle = state.ProcessWorkspaceReleased
	return released, a.cleanupErr
}

func processLifecycleAssociation(processID, workspaceID string, class processworkspace.ExecutionClass, mode processworkspace.WorkspaceMode) state.ProcessWorkspaceAssociation {
	a := state.ProcessWorkspaceAssociation{SchemaVersion: state.ProcessWorkspaceAssociationSchemaVersion, ReservationID: "reservation:p008", Lifecycle: state.ProcessWorkspacePrepared,
		WorkspaceID: workspaceID, Repository: "o/r", Provider: state.ProcessWorkspaceProviderIdentity{ProviderKey: "github", ServerInstance: "public", Host: "github.com"},
		ProcessID: processID, BaseSHA: allocationTestSHA, Branch: "process/process-008", ExecutionClass: class, Mode: mode,
		WriteOwnership: []string{"internal/commentrunner/jobs/**"}, LocalAssociationRef: "lease:" + workspaceID, RuntimeNamespace: "runtime-p008"}
	a.ReservationIdentity = a.ExpectedReservationIdentity()
	return a
}

func processLifecycleLease(association state.ProcessWorkspaceAssociation) processworkspace.PortableLease {
	now := time.Unix(100, 0).UTC()
	return processworkspace.PortableLease{SchemaVersion: processworkspace.LeaseSchemaVersion, WorkspaceID: association.WorkspaceID, Repository: association.Repository,
		ProcessID: association.ProcessID, ExecutionClass: association.ExecutionClass, Mode: association.Mode, BaseSHA: association.BaseSHA, Branch: association.Branch,
		WriteOwnership: append([]string(nil), association.WriteOwnership...), RuntimeNamespace: association.RuntimeNamespace,
		State: processworkspace.StatePrepared, CreatedAt: now, UpdatedAt: now}
}

func fakeRepoResolverResolution() RepositoryInfo {
	return RepositoryInfo{Repo: "o/r", CloneURL: "https://github.com/o/r.git", DefaultBranch: "main", Ref: "main", Binding: testRepositoryBinding()}
}

var _ WorkspaceManager = (*processLifecycleWorkspaceProvider)(nil)
