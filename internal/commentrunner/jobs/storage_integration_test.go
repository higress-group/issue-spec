package jobs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/commentrunner/storage"
	"github.com/higress-group/issue-spec/internal/workspace"
)

type recordedSessionResource struct {
	repo      string
	sid       string
	workspace string
}

type recordedRuntimeHome struct {
	scope storage.RuntimeScope
	paths storage.RuntimeHomePaths
}

type recordedJobScratch struct {
	repo  string
	jobID string
	path  string
}

type recordedScratchCompletion struct {
	repo  string
	jobID string
}

type fakeStorage struct {
	admitErr        error
	recordErr       error
	recordCalls     []recordedSessionResource
	poolErr         error
	poolCalls       []recordedSessionResource
	homeErr         error
	homeCalls       []recordedRuntimeHome
	scratchErr      error
	scratchCalls    []recordedJobScratch
	completeErr     error
	completeCalls   []recordedScratchCompletion
	admitCalls      int
	reconcileCalls  int
	reconcileApply  []bool
	reconcileReport storage.Report
	reconcileErr    error
}

func (f *fakeStorage) AdmitDispatch(context.Context) error {
	f.admitCalls++
	return f.admitErr
}

func (f *fakeStorage) RecordSessionResources(_ context.Context, repo, publicSessionID, workspacePath string) error {
	f.recordCalls = append(f.recordCalls, recordedSessionResource{repo: repo, sid: publicSessionID, workspace: workspacePath})
	return f.recordErr
}

func (f *fakeStorage) RecordSessionProcessPool(_ context.Context, repo, publicSessionID, workspacePath string) error {
	f.poolCalls = append(f.poolCalls, recordedSessionResource{repo: repo, sid: publicSessionID, workspace: workspacePath})
	return f.poolErr
}

func (f *fakeStorage) RecordRuntimeHome(_ context.Context, scope storage.RuntimeScope, paths storage.RuntimeHomePaths) error {
	f.homeCalls = append(f.homeCalls, recordedRuntimeHome{scope: scope, paths: paths})
	return f.homeErr
}

func (f *fakeStorage) RecordJobScratch(_ context.Context, repo, jobID, path string) error {
	f.scratchCalls = append(f.scratchCalls, recordedJobScratch{repo: repo, jobID: jobID, path: path})
	return f.scratchErr
}

func (f *fakeStorage) CompleteJobScratch(_ context.Context, repo, jobID string) error {
	f.completeCalls = append(f.completeCalls, recordedScratchCompletion{repo: repo, jobID: jobID})
	return f.completeErr
}

func (f *fakeStorage) ReconcileStorage(_ context.Context, apply, _ bool) (storage.Report, error) {
	f.reconcileCalls++
	f.reconcileApply = append(f.reconcileApply, apply)
	return f.reconcileReport, f.reconcileErr
}

func TestRunJobStoragePressureDelaysWithoutFailing(t *testing.T) {
	store := newMemoryStore()
	now := time.Date(2026, 7, 3, 13, 0, 0, 0, time.UTC)
	seedQueuedJob(t, store, state.Job{
		ID: "job-1", Repo: "o/r", IssueNumber: 30, CommandName: "new", CommandPrompt: "prompt",
		CommandIdempotencyKey: "cmd-1",
	})
	dispatcher := testDispatcher(store, &fakeWorkspaces{binding: testBinding("ws-1")}, &fakeCoordinator{}, &fakeWriteback{}, now)
	fake := &fakeStorage{admitErr: storage.ErrStoragePressure}
	dispatcher.Storage = fake

	result, err := dispatcher.RunNext(context.Background())
	if err != nil {
		t.Fatalf("RunNext: %v", err)
	}
	if result.Executed {
		t.Fatalf("pressured dispatch must not execute: %+v", result)
	}
	if result.Reason != "storage_pressure" {
		t.Fatalf("reason=%q, want storage_pressure", result.Reason)
	}
	st := loadState(t, store)
	if st.Jobs["job-1"].Status != state.StatusQueued {
		t.Fatalf("pressured job must stay queued, got %q", st.Jobs["job-1"].Status)
	}
	if fake.admitCalls == 0 {
		t.Fatalf("admission was not invoked")
	}
}

func TestRunJobAdmissionErrorDelaysWithoutFailing(t *testing.T) {
	store := newMemoryStore()
	now := time.Date(2026, 7, 3, 13, 0, 0, 0, time.UTC)
	seedQueuedJob(t, store, state.Job{
		ID: "job-1", Repo: "o/r", IssueNumber: 30, CommandName: "new", CommandPrompt: "prompt",
		CommandIdempotencyKey: "cmd-1",
	})
	dispatcher := testDispatcher(store, &fakeWorkspaces{binding: testBinding("ws-1")}, &fakeCoordinator{}, &fakeWriteback{}, now)
	dispatcher.Storage = &fakeStorage{admitErr: errors.New("statfs broken")}

	result, err := dispatcher.RunNext(context.Background())
	if err != nil {
		t.Fatalf("RunNext: %v", err)
	}
	if result.Executed || result.Reason != "storage_admission" {
		t.Fatalf("result=%+v, want non-executed storage_admission", result)
	}
	if st := loadState(t, store); st.Jobs["job-1"].Status != state.StatusQueued {
		t.Fatalf("admission-error job must stay queued, got %q", st.Jobs["job-1"].Status)
	}
}

func TestRunJobRecordsSessionResourcesBeforeSandbox(t *testing.T) {
	store := newMemoryStore()
	now := time.Date(2026, 7, 3, 13, 0, 0, 0, time.UTC)
	seedQueuedJob(t, store, state.Job{
		ID: "job-1", Repo: "o/r", IssueNumber: 30, CommandName: "new", CommandPrompt: "prompt",
		CommandIdempotencyKey: "cmd-1", TriggerCommentID: 42, TriggeringUserLogin: "alice",
		SessionCreatorLogin: "alice",
		FirstObservedComment: state.SeenComment{
			Repo: "o/r", IssueNumber: 30, CommentID: 42, AuthorLogin: "alice",
			HTMLURL: "https://github.com/o/r/issues/30#issuecomment-42",
		},
	})
	coordinator := &fakeCoordinator{newResult: dispatchResult("ps-generated", "rec-new", "turn-new", completedSummary())}
	dispatcher := testDispatcher(store, &fakeWorkspaces{binding: testBinding("ws-1")}, coordinator, &fakeWriteback{}, now)
	fake := &fakeStorage{}
	dispatcher.Storage = fake

	result, err := dispatcher.RunNext(context.Background())
	if err != nil {
		t.Fatalf("RunNext: %v", err)
	}
	if !result.Executed {
		t.Fatalf("dispatch must execute: %+v", result)
	}
	if len(fake.recordCalls) != 1 {
		t.Fatalf("record calls = %d, want 1", len(fake.recordCalls))
	}
	call := fake.recordCalls[0]
	if call.repo != "o/r" || call.sid != "ps-generated" {
		t.Fatalf("record call = %+v", call)
	}
	if !strings.Contains(call.workspace, "ws-1") {
		t.Fatalf("record workspace = %q, want session workspace path", call.workspace)
	}
}

func TestRunJobRecordingFailureBlocksDispatch(t *testing.T) {
	store := newMemoryStore()
	now := time.Date(2026, 7, 3, 13, 0, 0, 0, time.UTC)
	seedQueuedJob(t, store, state.Job{
		ID: "job-1", Repo: "o/r", IssueNumber: 30, CommandName: "new", CommandPrompt: "prompt",
		CommandIdempotencyKey: "cmd-1",
	})
	dispatcher := testDispatcher(store, &fakeWorkspaces{binding: testBinding("ws-1")}, &fakeCoordinator{}, &fakeWriteback{}, now)
	dispatcher.Storage = &fakeStorage{recordErr: errors.New("sidecar write failed")}

	result, err := dispatcher.RunNext(context.Background())
	if err == nil {
		t.Fatalf("recording failure must fail the dispatch: %+v", result)
	}
	st := loadState(t, store)
	if st.Jobs["job-1"].Status != state.StatusFailed {
		t.Fatalf("recording failure must fail the job fail-closed, got %q", st.Jobs["job-1"].Status)
	}
}

func TestReconcileInvokesStorageReconcile(t *testing.T) {
	store := newMemoryStore()
	now := time.Date(2026, 7, 3, 13, 0, 0, 0, time.UTC)
	dispatcher := testDispatcher(store, &fakeWorkspaces{binding: testBinding("ws-1")}, &fakeCoordinator{}, &fakeWriteback{}, now)
	fake := &fakeStorage{reconcileReport: storage.Report{RootIdentity: "root-id"}}
	dispatcher.Storage = fake

	result, err := dispatcher.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if fake.reconcileCalls != 1 || len(fake.reconcileApply) != 1 || !fake.reconcileApply[0] {
		t.Fatalf("storage reconcile calls=%d apply=%v", fake.reconcileCalls, fake.reconcileApply)
	}
	if result.StorageCleanup == nil || result.StorageCleanup.RootIdentity != "root-id" {
		t.Fatalf("reconcile result missing storage report: %+v", result)
	}
}

func TestReconcileStorageFailureIsNonFatal(t *testing.T) {
	store := newMemoryStore()
	now := time.Date(2026, 7, 3, 13, 0, 0, 0, time.UTC)
	dispatcher := testDispatcher(store, &fakeWorkspaces{binding: testBinding("ws-1")}, &fakeCoordinator{}, &fakeWriteback{}, now)
	dispatcher.Storage = &fakeStorage{reconcileErr: errors.New("engine failed")}

	result, err := dispatcher.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("storage failure must not fail job reconcile: %v", err)
	}
	found := false
	for _, d := range result.Diagnostics {
		if strings.Contains(d, "storage") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected storage diagnostic, got %+v", result.Diagnostics)
	}
}

func TestCleanupWorkspacesInvokesStorageReconcile(t *testing.T) {
	store := newMemoryStore()
	now := time.Date(2026, 7, 3, 13, 0, 0, 0, time.UTC)
	dispatcher := testDispatcher(store, &fakeWorkspaces{binding: testBinding("ws-1")}, &fakeCoordinator{}, &fakeWriteback{}, now)
	fake := &fakeStorage{}
	dispatcher.Storage = fake

	if _, err := dispatcher.CleanupWorkspaces(context.Background()); err != nil {
		t.Fatalf("CleanupWorkspaces: %v", err)
	}
	if fake.reconcileCalls != 1 {
		t.Fatalf("async-busy cleanup must invoke storage reconcile, calls=%d", fake.reconcileCalls)
	}
}

func TestLinkedWorktreesKeptProducesOperatorDiagnostic(t *testing.T) {
	store := newMemoryStore()
	now := time.Date(2026, 7, 3, 13, 0, 0, 0, time.UTC)
	workspaces := &fakeWorkspaces{
		binding: testBinding("ws-1"),
		cleanupResults: []workspace.CleanupResult{
			{WorkspaceID: "ws-1", Action: "kept", Reason: "linked_worktrees"},
		},
	}
	dispatcher := testDispatcher(store, workspaces, &fakeCoordinator{}, &fakeWriteback{}, now)
	seedState(t, store, func(st *state.RunnerState) error {
		return st.UpsertWorkspace(state.WorkspaceMetadata{ID: "ws-1", Path: "/tmp/ws-1", Repo: "o/r"})
	})
	result, err := dispatcher.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	found := false
	for _, d := range result.Diagnostics {
		if strings.Contains(d, "linked_worktrees") && strings.Contains(d, "ws-1") {
			found = true
		}
	}
	if !found {
		t.Fatalf("kept linked_worktrees must surface an operator diagnostic, got %+v", result.Diagnostics)
	}
}

func TestDeferredWorkspaceIDsProtectGroupedWorkspaceCleanup(t *testing.T) {
	store := newMemoryStore()
	now := time.Date(2026, 7, 3, 13, 0, 0, 0, time.UTC)
	workspaces := &fakeWorkspaces{binding: testBinding("ws-1")}
	dispatcher := testDispatcher(store, workspaces, &fakeCoordinator{}, &fakeWriteback{}, now)
	dispatcher.Storage = &fakeStorage{reconcileReport: storage.Report{DeferredWorkspaceIDs: []string{"ws-defer"}}}
	seedState(t, store, func(st *state.RunnerState) error {
		return st.UpsertWorkspace(state.WorkspaceMetadata{ID: "ws-defer", Path: "/tmp/ws-defer", Repo: "o/r"})
	})
	if _, err := dispatcher.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(workspaces.cleanupRequests) != 1 {
		t.Fatalf("cleanup requests = %d", len(workspaces.cleanupRequests))
	}
	if !workspaces.cleanupRequests[0].ActiveIDs["ws-defer"] {
		t.Fatalf("deferred workspace must be protected this pass: %+v", workspaces.cleanupRequests[0].ActiveIDs)
	}
}

func TestResumeAfterStorageReconcileKeepsSessionIdentity(t *testing.T) {
	store := newMemoryStore()
	now := time.Date(2026, 7, 3, 13, 0, 0, 0, time.UTC)
	root := t.TempDir()
	wsPath := filepath.Join(root, "ws-resume")
	if err := os.MkdirAll(wsPath, 0o700); err != nil {
		t.Fatal(err)
	}
	runtimeHash, err := storage.SessionRuntimeHash("o/r", "ps-resume", wsPath)
	if err != nil {
		t.Fatal(err)
	}
	runtimeDir := filepath.Join(root, ".sessions", runtimeHash)
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	resumeWorkspace := state.WorkspaceMetadata{ID: "ws-resume", Path: wsPath, Repo: "o/r", CloneURL: "https://github.com/o/r.git", Branch: "issue-spec-ws-resume", RepositoryBinding: testRepositoryBinding()}
	seedState(t, store, func(st *state.RunnerState) error {
		if err := st.UpsertPublicSession(state.PublicSession{
			Repo:              "o/r",
			PublicSessionID:   "ps-resume",
			IssueNumber:       30,
			AcpxRecordID:      "rec-resume",
			CreatorLogin:      "alice",
			Status:            state.StatusCompleted,
			Workspace:         resumeWorkspace,
			RepositoryBinding: testRepositoryBinding(),
			Acpx:              state.AcpxMetadata{StableRecordID: "rec-resume", CWD: wsPath},
			CreatedAt:         now.Add(-2 * time.Hour),
			LastUsedAt:        now.Add(-time.Hour),
		}); err != nil {
			return err
		}
		_, _, err := st.CreateCommandJob(state.Job{
			ID:                    "job-resume-after-reconcile",
			Repo:                  "o/r",
			IssueNumber:           30,
			PublicSessionID:       "ps-resume",
			CoordinatorKind:       "codex",
			SessionCreatorLogin:   "alice",
			TriggeringUserLogin:   "bob",
			TriggerCommentID:      213,
			CommandID:             "cmd-resume-after-reconcile",
			CommandName:           "resume",
			CommandPrompt:         "continue after reconcile",
			CommandIdempotencyKey: "cmd-key-resume-after-reconcile",
			StatusWritebackKey:    "status-resume-after-reconcile",
			Status:                state.StatusQueued,
			CreatedAt:             now,
			FirstObservedComment: state.SeenComment{
				Repo:        "o/r",
				IssueNumber: 30,
				CommentID:   213,
				HTMLURL:     "https://github.com/o/r/issues/30#issuecomment-213",
				AuthorLogin: "bob",
			},
		})
		return err
	})

	svc, err := storage.NewService(storage.ServiceConfig{
		WorkspaceRoot: root,
		StateLoader:   func(ctx context.Context) (state.RunnerState, error) { return store.Load(ctx) },
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	defer svc.Close()

	// Reconcile first: the retained session's runtime must be protected, and
	// phase one records its exact identity in the sidecar.
	report, err := svc.ReconcileStorage(context.Background(), true, false)
	if err != nil {
		t.Fatalf("ReconcileStorage: %v", err)
	}
	runtimeID := storage.ResourceID(storage.ResourceKindSessionRuntime, "o/r", "ps-resume", runtimeHash)
	protected := false
	for _, r := range report.Resources {
		if r.ID == runtimeID && r.Class == storage.ClassProtected && r.Action == storage.ActionKept {
			protected = true
		}
	}
	if !protected {
		t.Fatalf("retained session runtime must be protected by reconcile: %+v", report.Resources)
	}
	before := svc.Store().State().Resources[runtimeID]
	if before.PhysicalHash != runtimeHash || before.CleanupState != storage.CleanupManaged {
		t.Fatalf("sidecar identity before dispatch: %+v", before)
	}

	workspaces := &fakeWorkspaces{binding: workspace.Binding{Workspace: resumeWorkspace, AcpxWorkingDirectory: wsPath, SandboxWorkspacePath: wsPath}}
	coordinator := &fakeCoordinator{resumeResult: dispatchResult("ps-resume", "rec-resume", "turn-resume", completedSummary())}
	dispatcher := testDispatcher(store, workspaces, coordinator, &fakeWriteback{}, now)
	dispatcher.Storage = svc
	result, err := dispatcher.RunNext(context.Background())
	if err != nil || !result.Executed || result.Status != state.StatusCompleted {
		t.Fatalf("resume RunNext result=%+v err=%v", result, err)
	}

	after := svc.Store().State().Resources[runtimeID]
	if after.PhysicalHash != runtimeHash || after.Repo != "o/r" || after.PublicSessionID != "ps-resume" || after.CleanupState != storage.CleanupManaged {
		t.Fatalf("dispatch replaced the runtime identity: before=%+v after=%+v", before, after)
	}
	if !after.FirstObservedAt.Equal(before.FirstObservedAt) {
		t.Fatalf("runtime observation proof reset: before=%s after=%s", before.FirstObservedAt, after.FirstObservedAt)
	}
	if _, err := os.Lstat(runtimeDir); err != nil {
		t.Fatalf("runtime must survive reconcile and resume: %v", err)
	}
	st := loadState(t, store)
	if len(st.PublicSessions) != 1 {
		t.Fatalf("resume must not create a replacement session, sessions=%d", len(st.PublicSessions))
	}
	session, ok := st.GetPublicSession("o/r", "ps-resume")
	if !ok || session.Workspace.Path != wsPath {
		t.Fatalf("session identity changed: %+v", session)
	}
	if st.Jobs["job-resume-after-reconcile"].Status != state.StatusCompleted {
		t.Fatalf("resume job status=%q", st.Jobs["job-resume-after-reconcile"].Status)
	}
}
