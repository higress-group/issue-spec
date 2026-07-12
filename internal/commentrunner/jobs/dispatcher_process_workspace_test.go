package jobs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	if err != nil || class != processworkspace.ExecutionOrchestration || allocator.allocateCalls != 0 || got.AcpxWorkingDirectory == "" || got.AcpxWorkingDirectory == binding.Workspace.Path {
		t.Fatalf("binding=%+v class=%s calls=%d err=%v", got, class, allocator.allocateCalls, err)
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
	allocation    ProcessWorkspaceAllocation
	allocateCalls int
}

func (a *processLifecycleAllocator) Allocate(context.Context, ProcessWorkspaceAllocationRequest) (ProcessWorkspaceAllocation, error) {
	a.allocateCalls++
	return a.allocation, nil
}

func (a *processLifecycleAllocator) Reconcile(context.Context, string) (ProcessWorkspaceAllocation, error) {
	return a.allocation, nil
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
