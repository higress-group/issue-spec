package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/processworkspace"
)

const allocationTestSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestSelectTrustedProcessWorkspaceExactMissingAndAmbiguous(t *testing.T) {
	body, err := model.EnsureTypedBody("PROCESS", "PROCESS-017", "## Process\n\n### Parent TASK\n\n- TASK-004\n\n### Execution Class\n\n- change-bearing", model.BodyOptions{Status: "in-progress"})
	if err != nil {
		t.Fatal(err)
	}
	artifact := model.Artifact{Issue: 177, CommentID: 17, Comment: model.ParseTypedComment(body)}
	selection, err := SelectTrustedProcessWorkspace([]model.Artifact{artifact}, "PROCESS-017")
	if err != nil || selection == nil || selection.ExecutionClass != processworkspace.ExecutionChangeBearing {
		t.Fatalf("selection=%+v err=%v", selection, err)
	}
	if selection, err := SelectTrustedProcessWorkspace([]model.Artifact{artifact}, ""); err != nil || selection != nil {
		t.Fatalf("/new orchestration selection=%+v err=%v", selection, err)
	}
	if _, err := SelectTrustedProcessWorkspace([]model.Artifact{artifact}, "PROCESS-404"); err == nil {
		t.Fatal("missing exact PROCESS mapping accepted")
	}
	if _, err := SelectTrustedProcessWorkspace([]model.Artifact{artifact, artifact}, "PROCESS-017"); err == nil {
		t.Fatal("ambiguous exact PROCESS mapping accepted")
	}
}

func TestMarkTerminalWorkspaceCleanupRequired(t *testing.T) {
	job := state.Job{ID: "job-terminal", ProcessWorkspace: &state.ProcessWorkspaceAssignment{
		ProcessID: "PROCESS-017", WorkspaceID: "ws-process-017", ReservationID: "reservation:exact",
		AssociationGeneration: 1, ReservationIdentity: "identity:0123456789abcdef0123456789abcdef",
	}}
	MarkTerminalWorkspaceCleanupRequired(&job)
	if !job.ProcessWorkspace.CleanupRequired || job.ProcessWorkspace.CleanupState != state.ProcessWorkspaceAssignmentCleanupRequired || job.ProcessWorkspace.LastError != "" {
		t.Fatalf("terminal cleanup intent=%+v", job.ProcessWorkspace)
	}
}

func TestExternalWorkspaceWithoutConfiguredAdapterFailsBeforeReservation(t *testing.T) {
	for _, mode := range []processworkspace.WorkspaceMode{processworkspace.ModeNone, processworkspace.ModeWritable} {
		store := newMemoryProcessWorkspaceStore()
		allocator := testAllocator(store, &fakeProcessWorkspaceManager{})
		allocator.NoCheckout = runtimeNoCheckout{}
		request := orchestrationAllocationRequest("ws-external-"+string(mode), "PROCESS-EXT")
		request.ExecutionClass = processworkspace.ExecutionExternal
		request.ExternalMode = mode
		if mode == processworkspace.ModeWritable {
			request.BaseSHA, request.Branch, request.WriteOwnership = allocationTestSHA, "process/external", []string{"internal/**"}
		}
		if _, err := allocator.Allocate(context.Background(), request); !errors.Is(err, ErrExternalWorkspaceUnsupported) {
			t.Fatalf("external %s allocation error=%v", mode, err)
		}
		if len(store.value.ByWorkspace) != 0 {
			t.Fatalf("unsupported external %s adapter persisted reservation: %+v", mode, store.value.ByWorkspace)
		}
	}
}

func TestWorkspaceModeForExecutionClass(t *testing.T) {
	tests := []struct {
		class     processworkspace.ExecutionClass
		ext, want processworkspace.WorkspaceMode
	}{
		{processworkspace.ExecutionChangeBearing, "", processworkspace.ModeWritable},
		{processworkspace.ExecutionReview, "", processworkspace.ModeSnapshot},
		{processworkspace.ExecutionVerification, "", processworkspace.ModeSnapshot},
		{processworkspace.ExecutionOrchestration, "", processworkspace.ModeNone},
		{processworkspace.ExecutionExternal, processworkspace.ModeWritable, processworkspace.ModeWritable},
		{processworkspace.ExecutionExternal, processworkspace.ModeNone, processworkspace.ModeNone},
	}
	for _, test := range tests {
		got, err := workspaceModeForClass(test.class, test.ext)
		if err != nil || got != test.want {
			t.Fatalf("class=%s got=%s err=%v", test.class, got, err)
		}
	}
	if _, err := workspaceModeForClass(processworkspace.ExecutionExternal, "bad"); err == nil {
		t.Fatal("unknown external mode accepted")
	}
}

func TestManagerAllocatorIdempotentAllocationAndRestartInspect(t *testing.T) {
	store := newMemoryProcessWorkspaceStore()
	manager := &fakeProcessWorkspaceManager{}
	allocator := testAllocator(store, manager)
	request := writableAllocationRequest("ws-1", "PROCESS-001")
	first, err := allocator.Allocate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := allocator.Allocate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.Association, second.Association) || first.Association.Lifecycle != state.ProcessWorkspacePrepared || manager.prepareCalls != 1 {
		t.Fatalf("allocation not idempotent: first=%+v second=%+v prepares=%d", first, second, manager.prepareCalls)
	}
	encoded, _ := json.Marshal(store.value)
	var restored state.ProcessWorkspaceAssociations
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	restartedStore := &memoryProcessWorkspaceStore{value: restored}
	restarted := testAllocator(restartedStore, manager)
	inspected, err := restarted.Reconcile(context.Background(), "ws-1")
	if err != nil || inspected.Inspection == nil || inspected.Association.Lifecycle != state.ProcessWorkspacePrepared {
		t.Fatalf("restart=%+v err=%v", inspected, err)
	}
}

func TestManagerAllocatorValidatesStrictOwnershipBeforeReservation(t *testing.T) {
	for _, ownership := range [][]string{{"internal/?.go"}, {"internal/[ab].go"}} {
		store := newMemoryProcessWorkspaceStore()
		request := writableAllocationRequest("ws-invalid", "PROCESS-001")
		request.WriteOwnership = ownership
		_, err := testAllocator(store, &fakeProcessWorkspaceManager{}).Allocate(context.Background(), request)
		if err == nil {
			t.Fatalf("ownership %v accepted", ownership)
		}
		if len(store.value.ByWorkspace) != 0 {
			t.Fatalf("ownership failure reserved state: %+v", store.value)
		}
	}
}

func TestManagerAllocatorPrepareFailureCompensatesExactReservation(t *testing.T) {
	store := newMemoryProcessWorkspaceStore()
	manager := &fakeProcessWorkspaceManager{prepareErr: errors.New("prepare failed")}
	request := writableAllocationRequest("ws-fail", "PROCESS-001")
	request.RuntimeResources = []processworkspace.RuntimeResource{{Kind: "database", Name: "shared", Exclusive: true}}
	allocation, err := testAllocator(store, manager).Allocate(context.Background(), request)
	if err == nil || allocation.Association.Lifecycle != state.ProcessWorkspaceAllocating || !allocation.Association.NeedsReconcile {
		t.Fatalf("allocation=%+v err=%v", allocation, err)
	}
	second := writableAllocationRequest("ws-next", "PROCESS-002")
	second.RuntimeResources = append([]processworkspace.RuntimeResource(nil), request.RuntimeResources...)
	manager.prepareErr = nil
	if _, err := testAllocator(store, manager).Allocate(context.Background(), second); err == nil {
		t.Fatal("post-publication prepare failure released exclusive resource")
	}
	if _, err := testAllocator(store, manager).BeginRelease(context.Background(), allocation.Association.WorkspaceID, allocation.Association.ReservationID); err != nil {
		t.Fatal(err)
	}
	if _, err := testAllocator(store, manager).ReleaseAfterCleanup(context.Background(), allocation.Association.WorkspaceID, allocation.Association.ReservationID); err != nil {
		t.Fatal(err)
	}
	if _, err := testAllocator(store, manager).Allocate(context.Background(), second); err != nil {
		t.Fatalf("confirmed cleanup did not release resource: %v", err)
	}
}

func TestManagerAllocatorRestartMissingLeaseFailsAndReleasesResource(t *testing.T) {
	store := newMemoryProcessWorkspaceStore()
	manager := &fakeProcessWorkspaceManager{}
	request := writableAllocationRequest("ws-missing", "PROCESS-001")
	request.RuntimeResources = []processworkspace.RuntimeResource{{Kind: "database", Name: "restart", Exclusive: true}}
	allocation, err := testAllocator(store, manager).Allocate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	delete(manager.prepared, "ws-missing")
	manager.missing = true
	manager.mu.Unlock()
	reconciled, err := testAllocator(store, manager).Reconcile(context.Background(), "ws-missing")
	if !errors.Is(err, ErrProcessWorkspaceLeaseMissing) || !reconciled.Association.NeedsReconcile {
		t.Fatalf("reconciled=%+v err=%v", reconciled, err)
	}
	if reconciled.Association.ReservationID != allocation.Association.ReservationID {
		t.Fatal("missing lease compensation changed reservation identity")
	}
	next := writableAllocationRequest("ws-after-restart", "PROCESS-002")
	next.RuntimeResources = append([]processworkspace.RuntimeResource(nil), request.RuntimeResources...)
	manager.mu.Lock()
	manager.missing = false
	manager.mu.Unlock()
	if _, err := testAllocator(store, manager).Allocate(context.Background(), next); err == nil {
		t.Fatal("missing lease released resource before cleanup confirmation")
	}
}

func TestManagerAllocatorRestartReplaysPreparedLeasePublication(t *testing.T) {
	store := newMemoryProcessWorkspaceStore()
	manager := &fakeProcessWorkspaceManager{}
	allocation, err := testAllocator(store, manager).Allocate(context.Background(), writableAllocationRequest("ws-replay", "PROCESS-001"))
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	crashWindow := store.value.ByWorkspace[allocation.Association.WorkspaceID]
	crashWindow.Lifecycle = state.ProcessWorkspaceAllocating
	store.value.ByWorkspace[allocation.Association.WorkspaceID] = crashWindow
	store.mu.Unlock()
	replayed, err := testAllocator(store, manager).Reconcile(context.Background(), allocation.Association.WorkspaceID)
	if err != nil || replayed.Association.Lifecycle != state.ProcessWorkspacePrepared || replayed.Inspection == nil {
		t.Fatalf("prepared lease publication was not replayed: %+v err=%v", replayed, err)
	}
}

func TestManagerAllocatorReadinessRejectsDirtyProblemsAndConflictedLease(t *testing.T) {
	mutations := map[string]func(*processworkspace.Inspection){
		"dirty":      func(i *processworkspace.Inspection) { i.Dirty = true },
		"problems":   func(i *processworkspace.Inspection) { i.Problems = []string{"marker mismatch"} },
		"conflicted": func(i *processworkspace.Inspection) { i.Lease.Portable.State = processworkspace.StateConflicted },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			store := newMemoryProcessWorkspaceStore()
			manager := &fakeProcessWorkspaceManager{}
			allocation, err := testAllocator(store, manager).Allocate(context.Background(), writableAllocationRequest("ws-not-ready-"+name, "PROCESS-001"))
			if err != nil {
				t.Fatal(err)
			}
			store.mu.Lock()
			association := store.value.ByWorkspace[allocation.Association.WorkspaceID]
			association.Lifecycle = state.ProcessWorkspaceAllocating
			store.value.ByWorkspace[association.WorkspaceID] = association
			store.mu.Unlock()
			manager.mu.Lock()
			inspection := manager.prepared[association.WorkspaceID]
			mutate(&inspection)
			manager.prepared[association.WorkspaceID] = inspection
			manager.mu.Unlock()
			reconciled, err := testAllocator(store, manager).Reconcile(context.Background(), association.WorkspaceID)
			if !errors.Is(err, ErrProcessWorkspaceNotReady) || reconciled.Association.Lifecycle != state.ProcessWorkspaceAllocating || !reconciled.Association.NeedsReconcile {
				t.Fatalf("reconciled=%+v err=%v", reconciled, err)
			}
		})
	}
}

func TestManagerAllocatorTerminalRetryUsesNewTokenAndRejectsOldCleanupCAS(t *testing.T) {
	store := newMemoryProcessWorkspaceStore()
	allocator := testAllocator(store, &fakeProcessWorkspaceManager{})
	request := writableAllocationRequest("ws-aba", "PROCESS-001")
	first, err := allocator.Allocate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	activeRetry, err := allocator.Allocate(context.Background(), request)
	if err != nil || activeRetry.Association.ReservationID != first.Association.ReservationID {
		t.Fatalf("active retry token changed: first=%s retry=%s err=%v", first.Association.ReservationID, activeRetry.Association.ReservationID, err)
	}
	if _, err := allocator.BeginRelease(context.Background(), first.Association.WorkspaceID, first.Association.ReservationID); err != nil {
		t.Fatal(err)
	}
	if _, err := allocator.ReleaseAfterCleanup(context.Background(), first.Association.WorkspaceID, first.Association.ReservationID); err != nil {
		t.Fatal(err)
	}
	second, err := allocator.Allocate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if second.Association.ReservationID == first.Association.ReservationID {
		t.Fatal("terminal retry reused reservation token")
	}
	if _, err := allocator.BeginRelease(context.Background(), second.Association.WorkspaceID, first.Association.ReservationID); err == nil {
		t.Fatal("old cleanup token matched new allocation")
	}
}

func TestManagerAllocatorModeNoneRequiresAdapterReadiness(t *testing.T) {
	store := newMemoryProcessWorkspaceStore()
	allocator := testAllocator(store, &fakeProcessWorkspaceManager{})
	allocator.NoCheckout = notReadyNoCheckout{}
	request := orchestrationAllocationRequest("ws-adapter-wait", "PROCESS-001")
	request.RuntimeResources = []processworkspace.RuntimeResource{{Kind: "port", Name: "adapter", Exclusive: true}}
	allocation, err := allocator.Allocate(context.Background(), request)
	if !errors.Is(err, ErrProcessWorkspaceNotReady) || allocation.Association.Lifecycle != state.ProcessWorkspaceAllocating || !allocation.Association.NeedsReconcile {
		t.Fatalf("allocation=%+v err=%v", allocation, err)
	}
}

func TestManagerAllocatorReleaseFreesExclusiveResource(t *testing.T) {
	store := newMemoryProcessWorkspaceStore()
	allocator := testAllocator(store, &fakeProcessWorkspaceManager{})
	first := writableAllocationRequest("ws-a", "PROCESS-001")
	first.RuntimeResources = []processworkspace.RuntimeResource{{Kind: "port", Name: "http", Exclusive: true}}
	allocation, err := allocator.Allocate(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := allocator.BeginRelease(context.Background(), allocation.Association.WorkspaceID, allocation.Association.ReservationID); err != nil {
		t.Fatal(err)
	}
	if _, err := allocator.ReleaseAfterCleanup(context.Background(), allocation.Association.WorkspaceID, allocation.Association.ReservationID); err != nil {
		t.Fatal(err)
	}
	second := writableAllocationRequest("ws-b", "PROCESS-002")
	second.RuntimeResources = append([]processworkspace.RuntimeResource(nil), first.RuntimeResources...)
	if _, err := allocator.Allocate(context.Background(), second); err != nil {
		t.Fatalf("released resource remained exclusive: %v", err)
	}
}

func TestManagerAllocatorCleanupFailureRetainsExclusiveReservationForRetry(t *testing.T) {
	store := newMemoryProcessWorkspaceStore()
	manager := &fakeProcessWorkspaceManager{cleanupErr: errors.New("cleanup unavailable")}
	allocator := testAllocator(store, manager)
	request := writableAllocationRequest("ws-cleanup", "PROCESS-001")
	request.RuntimeResources = []processworkspace.RuntimeResource{{Kind: "port", Name: "http", Exclusive: true}}
	allocation, err := allocator.Allocate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if held, err := allocator.CleanupAndRelease(context.Background(), allocation.Association.WorkspaceID, allocation.Association.ReservationID); err == nil || held.Lifecycle != state.ProcessWorkspaceCleanupPending {
		t.Fatalf("cleanup failure released reservation: held=%+v err=%v", held, err)
	}
	blocked := writableAllocationRequest("ws-blocked", "PROCESS-002")
	blocked.RuntimeResources = append([]processworkspace.RuntimeResource(nil), request.RuntimeResources...)
	if _, err := allocator.Allocate(context.Background(), blocked); err == nil {
		t.Fatal("cleanup failure freed exclusive resource")
	}
	manager.cleanupErr = nil
	released, err := allocator.CleanupAndRelease(context.Background(), allocation.Association.WorkspaceID, allocation.Association.ReservationID)
	if err != nil || released.Lifecycle != state.ProcessWorkspaceReleased {
		t.Fatalf("cleanup retry=%+v err=%v", released, err)
	}
	if _, err := allocator.Allocate(context.Background(), blocked); err != nil {
		t.Fatalf("confirmed cleanup did not free resource: %v", err)
	}
}

func TestManagerAllocatorNoCheckoutCleanupRequiresProviderConfirmation(t *testing.T) {
	store := newMemoryProcessWorkspaceStore()
	allocator := testAllocator(store, &fakeProcessWorkspaceManager{})
	request := orchestrationAllocationRequest("ws-none-cleanup", "PROCESS-001")
	allocation, err := allocator.Allocate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	allocator.NoCheckout = notReadyNoCheckout{}
	held, err := allocator.CleanupAndRelease(context.Background(), allocation.Association.WorkspaceID, allocation.Association.ReservationID)
	if err == nil || held.Lifecycle != state.ProcessWorkspaceCleanupPending {
		t.Fatalf("unconfirmed adapter cleanup=%+v err=%v", held, err)
	}
	allocator.NoCheckout = readyNoCheckout{}
	if _, err := allocator.CleanupAndRelease(context.Background(), allocation.Association.WorkspaceID, allocation.Association.ReservationID); err != nil {
		t.Fatal(err)
	}
}

func TestManagerAllocatorProviderIdentityNamespacesAndRejectsUnsafeValues(t *testing.T) {
	provider := state.ProcessWorkspaceProviderIdentity{ProviderKey: "github", ServerInstance: "public", Host: "github.com"}
	other := state.ProcessWorkspaceProviderIdentity{ProviderKey: "aone", ServerInstance: "corp", Host: "code.alibaba-inc.com"}
	if runtimeNamespace("o/r", provider, "PROCESS-001", "ws") == runtimeNamespace("o/r", other, "PROCESS-001", "ws") {
		t.Fatal("provider identity omitted from namespace")
	}
	for _, mutate := range []func(*ProcessWorkspaceAllocationRequest){
		func(r *ProcessWorkspaceAllocationRequest) { r.Repository = "https://user:token@example/o/r" },
		func(r *ProcessWorkspaceAllocationRequest) { r.Repository = "file:///tmp/repo" },
		func(r *ProcessWorkspaceAllocationRequest) { r.ProviderHost = "user@github.com" },
	} {
		store := newMemoryProcessWorkspaceStore()
		request := writableAllocationRequest("ws-unsafe", "PROCESS-001")
		mutate(&request)
		if _, err := testAllocator(store, &fakeProcessWorkspaceManager{}).Allocate(context.Background(), request); err == nil {
			t.Fatal("unsafe provider/repository accepted")
		}
		if len(store.value.ByWorkspace) != 0 {
			t.Fatal("unsafe identity reached reservation")
		}
	}
}

func TestManagerAllocatorModeNoneResourceIsSerializedAndReleased(t *testing.T) {
	store := newMemoryProcessWorkspaceStore()
	allocator := testAllocator(store, &fakeProcessWorkspaceManager{})
	request := orchestrationAllocationRequest("ws-none-a", "PROCESS-001")
	request.RuntimeResources = []processworkspace.RuntimeResource{{Kind: "database", Name: "external", Exclusive: true}}
	first, err := allocator.Allocate(context.Background(), request)
	if err != nil || first.Inspection != nil || first.Association.Lifecycle != state.ProcessWorkspacePrepared {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second := orchestrationAllocationRequest("ws-none-b", "PROCESS-002")
	second.RuntimeResources = append([]processworkspace.RuntimeResource(nil), request.RuntimeResources...)
	if _, err := allocator.Allocate(context.Background(), second); err == nil {
		t.Fatal("mode-none exclusive resource was not serialized")
	}
	if _, err := allocator.BeginRelease(context.Background(), first.Association.WorkspaceID, first.Association.ReservationID); err != nil {
		t.Fatal(err)
	}
	if _, err := allocator.ReleaseAfterCleanup(context.Background(), first.Association.WorkspaceID, first.Association.ReservationID); err != nil {
		t.Fatal(err)
	}
	if _, err := allocator.Allocate(context.Background(), second); err != nil {
		t.Fatalf("mode-none release did not free resource: %v", err)
	}
}

func TestManagerAllocatorExclusiveReservationIsSerialized(t *testing.T) {
	store := newMemoryProcessWorkspaceStore()
	allocator := testAllocator(store, &fakeProcessWorkspaceManager{})
	requests := []ProcessWorkspaceAllocationRequest{writableAllocationRequest("ws-a", "PROCESS-001"), writableAllocationRequest("ws-b", "PROCESS-002")}
	for i := range requests {
		requests[i].RuntimeResources = []processworkspace.RuntimeResource{{Kind: "database", Name: "shared", Exclusive: true}}
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, request := range requests {
		wg.Add(1)
		go func(r ProcessWorkspaceAllocationRequest) {
			defer wg.Done()
			_, err := allocator.Allocate(context.Background(), r)
			errs <- err
		}(request)
	}
	wg.Wait()
	close(errs)
	success := 0
	for err := range errs {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("exclusive successes=%d", success)
	}
}

func TestProcessWorkspaceRuntimeUsesRealFileStoreAndSessionIntegrationRoot(t *testing.T) {
	ctx := context.Background()
	repo := filepath.Join(t.TempDir(), "session-clone")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "init")
	gitRun(t, repo, "config", "user.name", "Runner Test")
	gitRun(t, repo, "config", "user.email", "runner@example.test")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", "README.md")
	gitRun(t, repo, "commit", "-m", "base")
	base := strings.TrimSpace(gitRun(t, repo, "rev-parse", "HEAD"))
	statePath := filepath.Join(t.TempDir(), "runner-state.json")
	fileStore, err := state.OpenFileStore(statePath)
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
	allocator, err := runtime.ProcessWorkspaceAllocator(ctx, ProcessWorkspaceAllocatorRequest{IntegrationRoot: repo})
	if err != nil {
		t.Fatal(err)
	}
	allocation, err := allocator.Allocate(ctx, ProcessWorkspaceAllocationRequest{
		Repository: "o/r", ProviderKey: "github", ServerInstance: "public", ProviderHost: "github.com",
		ProcessID: "PROCESS-017", WorkspaceID: "ws-process-017", ExecutionClass: processworkspace.ExecutionChangeBearing,
		BaseSHA: base, Branch: "process/process-017", WriteOwnership: []string{"internal/**"},
		Owner: processworkspace.LeaseOwner{CoordinatorID: "runner", Token: "owner-secret", AcquiredAt: time.Now().UTC()},
	})
	canonicalRepo, _ := filepath.EvalSymlinks(repo)
	if err != nil || allocation.Inspection == nil || allocation.Inspection.Lease.IntegrationRoot != canonicalRepo ||
		strings.HasPrefix(allocation.Inspection.Lease.WorktreePath, canonicalRepo+string(os.PathSeparator)) {
		t.Fatalf("allocation=%+v err=%v", allocation, err)
	}
	if _, err := allocator.CleanupAndRelease(ctx, allocation.Association.WorkspaceID, allocation.Association.ReservationID); err != nil {
		t.Fatal(err)
	}
	loaded, err := adapter.LoadProcessWorkspaces(ctx)
	if err != nil || loaded.ByWorkspace[allocation.Association.WorkspaceID].Lifecycle != state.ProcessWorkspaceReleased {
		t.Fatalf("durable release=%+v err=%v", loaded, err)
	}
}

func TestRuntimeNoCheckoutKeysAdaptersByCompleteProviderIdentity(t *testing.T) {
	leftIdentity := state.ProcessWorkspaceProviderIdentity{ProviderKey: "code.example", ServerInstance: "server-a", Host: "code.example"}
	rightIdentity := state.ProcessWorkspaceProviderIdentity{ProviderKey: "code.example", ServerInstance: "server-b", Host: "code.example"}
	leftKey, err := ProcessWorkspaceAdapterKey(leftIdentity)
	if err != nil {
		t.Fatal(err)
	}
	rightKey, err := ProcessWorkspaceAdapterKey(rightIdentity)
	if err != nil {
		t.Fatal(err)
	}
	if leftKey == rightKey {
		t.Fatal("distinct provider instances produced a colliding adapter key")
	}
	left, right := &recordingNoCheckout{name: "left"}, &recordingNoCheckout{name: "right"}
	runtime := runtimeNoCheckout{external: map[string]NoCheckoutLifecycle{leftKey: left, rightKey: right}}
	association := state.ProcessWorkspaceAssociation{ExecutionClass: processworkspace.ExecutionExternal, Provider: rightIdentity}
	ready, err := runtime.Ready(t.Context(), association)
	if err != nil || !ready || left.readyCalls != 0 || right.readyCalls != 1 {
		t.Fatalf("ready=%t err=%v left=%d right=%d", ready, err, left.readyCalls, right.readyCalls)
	}
	association.Provider.Host = "other.example"
	if _, err := runtime.Ready(t.Context(), association); !errors.Is(err, ErrExternalWorkspaceUnsupported) {
		t.Fatalf("missing exact identity error=%v", err)
	}
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}

type memoryProcessWorkspaceStore struct {
	mu           sync.Mutex
	value        state.ProcessWorkspaceAssociations
	tokenCounter int
}

func newMemoryProcessWorkspaceStore() *memoryProcessWorkspaceStore {
	return &memoryProcessWorkspaceStore{value: state.NewProcessWorkspaceAssociations()}
}
func (s *memoryProcessWorkspaceStore) LoadProcessWorkspaces(context.Context) (state.ProcessWorkspaceAssociations, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, _ := json.Marshal(s.value)
	var out state.ProcessWorkspaceAssociations
	return out, json.Unmarshal(data, &out)
}
func (s *memoryProcessWorkspaceStore) ReserveProcessWorkspace(_ context.Context, association state.ProcessWorkspaceAssociation) (state.ProcessWorkspaceAssociation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.value.Reserve(association)
}
func (s *memoryProcessWorkspaceStore) TransitionProcessWorkspace(_ context.Context, id, reservation string, from, to state.ProcessWorkspaceLifecycle) (state.ProcessWorkspaceAssociation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.value.Transition(id, reservation, from, to)
}
func (s *memoryProcessWorkspaceStore) MarkProcessWorkspaceFailure(_ context.Context, id, reservation, code string) (state.ProcessWorkspaceAssociation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.value.MarkFailure(id, reservation, code)
}
func (s *memoryProcessWorkspaceStore) BeginReleaseProcessWorkspace(_ context.Context, id, reservation string) (state.ProcessWorkspaceAssociation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	association, ok := s.value.Get(id)
	if !ok || association.ReservationID != reservation {
		return state.ProcessWorkspaceAssociation{}, errors.New("process workspace reservation CAS mismatch")
	}
	if association.Lifecycle == state.ProcessWorkspaceCleanupPending {
		return association, nil
	}
	return s.value.Transition(id, reservation, association.Lifecycle, state.ProcessWorkspaceCleanupPending)
}
func (s *memoryProcessWorkspaceStore) ConfirmProcessWorkspaceReleased(_ context.Context, id, reservation string) (state.ProcessWorkspaceAssociation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.value.ConfirmReleased(id, reservation)
}
func (s *memoryProcessWorkspaceStore) DeleteProcessWorkspace(_ context.Context, id, reservation string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.value.Delete(id, reservation)
}

type fakeProcessWorkspaceManager struct {
	mu                         sync.Mutex
	prepareCalls, inspectCalls int
	prepared                   map[string]processworkspace.Inspection
	prepareErr                 error
	cleanupErr                 error
	missing                    bool
}

func (m *fakeProcessWorkspaceManager) Prepare(_ context.Context, request processworkspace.PrepareRequest) (processworkspace.Inspection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prepareCalls++
	if m.prepared == nil {
		m.prepared = map[string]processworkspace.Inspection{}
	}
	request.Lease.Portable.State = processworkspace.StatePrepared
	inspection := processworkspace.Inspection{Lease: request.Lease, Registered: true, Present: true, Head: request.Lease.Portable.BaseSHA}
	m.prepared[request.Lease.Portable.WorkspaceID] = inspection
	return inspection, m.prepareErr
}
func (m *fakeProcessWorkspaceManager) Inspect(_ context.Context, id string) (processworkspace.Inspection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inspectCalls++
	if m.missing {
		return processworkspace.Inspection{}, processworkspace.ErrLeaseNotFound
	}
	return m.prepared[id], nil
}
func (m *fakeProcessWorkspaceManager) Cleanup(_ context.Context, id, ownerToken string) (processworkspace.Inspection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	inspection := m.prepared[id]
	if ownerToken == "" || ownerToken != inspection.Lease.Owner.Token {
		return inspection, errors.New("owner mismatch")
	}
	if m.cleanupErr != nil {
		return inspection, m.cleanupErr
	}
	inspection.Lease.Portable.State = processworkspace.StateCleaned
	m.prepared[id] = inspection
	return inspection, nil
}

func testAllocator(store ProcessWorkspaceStateStore, manager ProcessWorkspaceManager) *ManagerAllocator {
	allocator := &ManagerAllocator{State: store, Manager: manager, NoCheckout: readyNoCheckout{}, Now: func() time.Time { return time.Unix(100, 0).UTC() }}
	if memory, ok := store.(*memoryProcessWorkspaceStore); ok {
		allocator.ReservationToken = func() (string, error) {
			memory.mu.Lock()
			defer memory.mu.Unlock()
			memory.tokenCounter++
			return fmt.Sprintf("reservation:test-%d", memory.tokenCounter), nil
		}
	}
	return allocator
}

type readyNoCheckout struct{}

func (readyNoCheckout) Ready(context.Context, state.ProcessWorkspaceAssociation) (bool, error) {
	return true, nil
}
func (readyNoCheckout) Cleanup(context.Context, state.ProcessWorkspaceAssociation) (bool, error) {
	return true, nil
}

type notReadyNoCheckout struct{}

func (notReadyNoCheckout) Ready(context.Context, state.ProcessWorkspaceAssociation) (bool, error) {
	return false, nil
}
func (notReadyNoCheckout) Cleanup(context.Context, state.ProcessWorkspaceAssociation) (bool, error) {
	return false, nil
}

type recordingNoCheckout struct {
	name         string
	readyCalls   int
	cleanupCalls int
}

func (r *recordingNoCheckout) Ready(context.Context, state.ProcessWorkspaceAssociation) (bool, error) {
	r.readyCalls++
	return true, nil
}
func (r *recordingNoCheckout) Cleanup(context.Context, state.ProcessWorkspaceAssociation) (bool, error) {
	r.cleanupCalls++
	return true, nil
}
func writableAllocationRequest(workspaceID, processID string) ProcessWorkspaceAllocationRequest {
	return ProcessWorkspaceAllocationRequest{Repository: "o/r", ProviderKey: "github", ServerInstance: "public", ProviderHost: "github.com", WorkspaceID: workspaceID, ProcessID: processID, ExecutionClass: processworkspace.ExecutionChangeBearing, BaseSHA: allocationTestSHA, Branch: "process/" + processID, WriteOwnership: []string{"internal/**"}, Owner: processworkspace.LeaseOwner{CoordinatorID: "runner", Token: "local-secret", AcquiredAt: time.Unix(100, 0)}}
}
func orchestrationAllocationRequest(workspaceID, processID string) ProcessWorkspaceAllocationRequest {
	return ProcessWorkspaceAllocationRequest{Repository: "o/r", ProviderKey: "aone", ServerInstance: "corp", ProviderHost: "code.alibaba-inc.com", WorkspaceID: workspaceID, ProcessID: processID, ExecutionClass: processworkspace.ExecutionOrchestration}
}
