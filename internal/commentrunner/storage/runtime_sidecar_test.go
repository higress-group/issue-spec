package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/commentrunner/state"
)

func TestRuntimeResourceKindsValid(t *testing.T) {
	for _, kind := range []ResourceKind{
		ResourceKindSessionRuntime, ResourceKindSessionProcessPool,
		ResourceKindRunnerHome, ResourceKindJobScratch,
	} {
		if !kind.Valid() {
			t.Fatalf("kind %q must be valid", kind)
		}
	}
	if ResourceKind("runtime_home").Valid() || ResourceKind("").Valid() {
		t.Fatalf("unknown kinds must stay invalid")
	}
}

func TestRecordRuntimeHomeUpsertIdempotent(t *testing.T) {
	svc, root := newRuntimeService(t, state.NewState())
	scope := testScope()
	paths, err := PrepareRuntimeHome(root, scope)
	if err != nil {
		t.Fatalf("PrepareRuntimeHome: %v", err)
	}
	if err := svc.RecordRuntimeHome(context.Background(), scope, paths); err != nil {
		t.Fatalf("RecordRuntimeHome: %v", err)
	}
	hash, err := RuntimeScopeHash(scope)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	id := ResourceID(ResourceKindRunnerHome, scope.Repo, "", hash)
	first, ok := svc.Store().State().Resources[id]
	if !ok {
		t.Fatalf("runtime home record missing")
	}
	if first.Kind != ResourceKindRunnerHome || first.Repo != scope.Repo || first.PublicSessionID != "" ||
		first.PhysicalHash != hash || first.Path != paths.Root || first.CleanupState != CleanupManaged {
		t.Fatalf("runtime home record = %+v", first)
	}
	if first.FirstObservedAt.IsZero() {
		t.Fatalf("runtime home record must carry first observation proof")
	}
	// A steady-state re-record preserves the observation proof and the record.
	if err := svc.RecordRuntimeHome(context.Background(), scope, paths); err != nil {
		t.Fatalf("re-record: %v", err)
	}
	second, ok := svc.Store().State().Resources[id]
	if !ok || second != first {
		t.Fatalf("idempotent re-record changed the record: first=%+v second=%+v", first, second)
	}
	// A foreign home path fails closed.
	if err := svc.RecordRuntimeHome(context.Background(), scope, RuntimeHomePaths{Root: filepath.Join(root, "elsewhere")}); err == nil {
		t.Fatalf("recording a home outside .runner-home must fail closed")
	}
}

// TestEngineSweepDropsStaleJobScratchRecord: a job_scratch record whose
// directory vanished outside reconciliation is dropped with a diagnostic by
// the generic engine pass, while sibling records stay intact.
func TestEngineSweepDropsStaleJobScratchRecord(t *testing.T) {
	svc, root := newRuntimeService(t, state.NewState())
	stalePaths := prepareScratchWithFile(t, root, scratchJobDone, 16)
	livePaths := prepareScratchWithFile(t, root, scratchJobActive, 16)
	if err := svc.RecordJobScratch(context.Background(), "o/r", scratchJobDone, stalePaths.Root); err != nil {
		t.Fatalf("RecordJobScratch stale: %v", err)
	}
	if err := svc.RecordJobScratch(context.Background(), "o/r", scratchJobActive, livePaths.Root); err != nil {
		t.Fatalf("RecordJobScratch live: %v", err)
	}
	if err := os.RemoveAll(stalePaths.Root); err != nil {
		t.Fatalf("remove stale scratch: %v", err)
	}
	report, err := svc.ReconcileStorage(context.Background(), true, false)
	if err != nil {
		t.Fatalf("ReconcileStorage: %v", err)
	}
	if _, ok := scratchRecord(t, svc, "o/r", scratchJobDone); ok {
		t.Fatalf("stale scratch record must be swept")
	}
	found := false
	for _, diagnostic := range report.Diagnostics {
		if strings.Contains(diagnostic, scratchJobDone) && strings.Contains(diagnostic, "vanished") {
			found = true
		}
	}
	if !found {
		t.Fatalf("sweep must diagnose the vanished record: %+v", report.Diagnostics)
	}
	live, ok := scratchRecord(t, svc, "o/r", scratchJobActive)
	if !ok || live.CleanupState != CleanupManaged {
		t.Fatalf("live scratch record must survive the sweep: %+v ok=%v", live, ok)
	}
	if _, err := os.Lstat(livePaths.Root); err != nil {
		t.Fatalf("live scratch dir must survive the sweep: %v", err)
	}
}

// TestRunnerHomeSurvivesApplyReconcile is the forward-looking engine contract:
// the engine never inventories `.runner-home`/`.job-scratch`, so a runner_home
// record and its tree survive an apply reconcile while a legacy `.sessions`
// orphan is still reclaimed.
func TestRunnerHomeSurvivesApplyReconcile(t *testing.T) {
	svc, root := newRuntimeService(t, state.NewState())
	scope := testScope()
	paths, err := PrepareRuntimeHome(root, scope)
	if err != nil {
		t.Fatalf("PrepareRuntimeHome: %v", err)
	}
	if err := svc.RecordRuntimeHome(context.Background(), scope, paths); err != nil {
		t.Fatalf("RecordRuntimeHome: %v", err)
	}
	scratch := prepareScratchWithFile(t, root, scratchJobActive, 64)
	if err := svc.RecordJobScratch(context.Background(), "o/r", scratchJobActive, scratch.Root); err != nil {
		t.Fatalf("RecordJobScratch: %v", err)
	}
	writeFile(t, filepath.Join(paths.Home, ".acpx", "sessions", "index.json"), 128)
	// Plus one legacy orphan the engine must still reclaim.
	orphanHash := strings.Repeat("ef", 16)
	orphanDir := filepath.Join(root, SessionsDirName, orphanHash)
	writeFile(t, filepath.Join(orphanDir, "home", "stale"), 32)

	orphanID := ResourceID(ResourceKindSessionRuntime, "", "", orphanHash)
	var report Report
	// Two passes: the first observes the orphan, the second reclaims it past
	// a zero grace window.
	for pass := 0; pass < 2; pass++ {
		report, err = svc.ReconcileStorage(context.Background(), true, false)
		if err != nil {
			t.Fatalf("ReconcileStorage pass %d: %v", pass, err)
		}
		svc.orphanGrace = 0
	}
	got := reportByID(t, report, orphanID)
	if got.Action != ActionDeleted {
		t.Fatalf("engine must still delete the legacy orphan, got %+v", got)
	}
	if _, err := os.Lstat(orphanDir); !os.IsNotExist(err) {
		t.Fatalf("legacy orphan must be gone, err=%v", err)
	}
	// The engine neither inventories nor schedules the shared-layout resources.
	for _, resource := range report.Resources {
		if resource.Kind == ResourceKindRunnerHome || resource.Kind == ResourceKindJobScratch {
			t.Fatalf("engine must never classify shared-layout resources: %+v", resource)
		}
		if strings.Contains(resource.ID, RunnerHomesDirName) || strings.Contains(resource.ID, JobScratchDirName) {
			t.Fatalf("engine must never inventory runner-home resources: %+v", resource)
		}
	}
	hash, err := RuntimeScopeHash(scope)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	homeRecord, ok := svc.Store().State().Resources[ResourceID(ResourceKindRunnerHome, scope.Repo, "", hash)]
	if !ok || homeRecord.CleanupState != CleanupManaged {
		t.Fatalf("runtime home record must survive reconcile: %+v ok=%v", homeRecord, ok)
	}
	if _, ok := scratchRecord(t, svc, "o/r", scratchJobActive); !ok {
		t.Fatalf("job scratch record must survive reconcile")
	}
	if _, err := os.Lstat(filepath.Join(paths.Home, ".acpx", "sessions", "index.json")); err != nil {
		t.Fatalf("shared runtime home damaged by the engine: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(scratch.Root, "tmp", "payload")); err != nil {
		t.Fatalf("job scratch damaged by the engine: %v", err)
	}
}

// TestAdmitDispatchPressuredReclaimsScratchAndCaches: the pressured pass also
// reclaims stale job scratch and rebuildable home caches once the root runs
// the shared layout.
func TestAdmitDispatchPressuredReclaimsScratchAndCaches(t *testing.T) {
	svc, root := newRuntimeService(t, state.NewState())
	svc.minFreeBytes = 1024
	scope := testScope()
	paths, err := PrepareRuntimeHome(root, scope)
	if err != nil {
		t.Fatalf("PrepareRuntimeHome: %v", err)
	}
	if err := svc.RecordRuntimeHome(context.Background(), scope, paths); err != nil {
		t.Fatalf("RecordRuntimeHome: %v", err)
	}
	writeFile(t, filepath.Join(paths.Home, ".cache", "go-build", "abc"), 256)
	scratch := prepareScratchWithFile(t, root, scratchJobDone, 128)
	if err := svc.RecordJobScratch(context.Background(), "o/r", scratchJobDone, scratch.Root); err != nil {
		t.Fatalf("RecordJobScratch: %v", err)
	}
	calls := 0
	svc.statfs = func(string) (uint64, error) {
		calls++
		if calls == 1 {
			return 512, nil
		}
		return 4096, nil
	}
	if err := svc.AdmitDispatch(context.Background()); err != nil {
		t.Fatalf("AdmitDispatch after pressured reclaim: %v", err)
	}
	if calls != 2 {
		t.Fatalf("statfs calls=%d, want 2 (pressure check + post-cleanup recheck)", calls)
	}
	if _, err := os.Lstat(filepath.Join(paths.Home, ".cache")); !os.IsNotExist(err) {
		t.Fatalf("pressured admission must evict rebuildable caches, err=%v", err)
	}
	if _, err := os.Lstat(scratch.Root); !os.IsNotExist(err) {
		t.Fatalf("pressured admission must remove stale job scratch, err=%v", err)
	}
	if _, ok := scratchRecord(t, svc, "o/r", scratchJobDone); ok {
		t.Fatalf("stale scratch record must be gone after pressured reclaim")
	}
	// Protected home content survives.
	if _, err := os.Lstat(filepath.Join(paths.Root, runtimeScopeFileName)); err != nil {
		t.Fatalf("scope binding must survive pressured reclaim: %v", err)
	}
}

// TestAdmitDispatchPressuredSkipsSharedCleanupWithoutHomes: without any
// runner_home record the pressured pass leaves the shared-layout directories
// alone, so a root that never adopted the layout sees no new cleanup behavior.
func TestAdmitDispatchPressuredSkipsSharedCleanupWithoutHomes(t *testing.T) {
	svc, root := newRuntimeService(t, state.NewState())
	svc.minFreeBytes = 1024
	scratch := prepareScratchWithFile(t, root, scratchJobDone, 128)
	if err := svc.RecordJobScratch(context.Background(), "o/r", scratchJobDone, scratch.Root); err != nil {
		t.Fatalf("RecordJobScratch: %v", err)
	}
	calls := 0
	svc.statfs = func(string) (uint64, error) {
		calls++
		if calls == 1 {
			return 512, nil
		}
		return 4096, nil
	}
	if err := svc.AdmitDispatch(context.Background()); err != nil {
		t.Fatalf("AdmitDispatch: %v", err)
	}
	if _, err := os.Lstat(scratch.Root); err != nil {
		t.Fatalf("scratch must be untouched without a runner home record: %v", err)
	}
}

// TestAdmitDispatchPressuredCleanupFailureIsBestEffort: a failing shared
// cleanup never fails admission by itself; it surfaces only as a bounded
// diagnostic while pressure persists.
func TestAdmitDispatchPressuredCleanupFailureIsBestEffort(t *testing.T) {
	svc, root := newRuntimeService(t, state.NewState())
	svc.minFreeBytes = 1024
	scope := testScope()
	paths, err := PrepareRuntimeHome(root, scope)
	if err != nil {
		t.Fatalf("PrepareRuntimeHome: %v", err)
	}
	if err := svc.RecordRuntimeHome(context.Background(), scope, paths); err != nil {
		t.Fatalf("RecordRuntimeHome: %v", err)
	}
	// Break only the scratch reconciliation's state reload: the pressured
	// engine pass loads state first and must succeed.
	loaderCalls := 0
	svc.stateLoader = func(context.Context) (state.RunnerState, error) {
		loaderCalls++
		if loaderCalls > 1 {
			return state.RunnerState{}, errors.New("state unavailable")
		}
		return state.NewState(), nil
	}
	// First statfs below threshold, then recovered: admission passes despite
	// the best-effort cleanup failure.
	calls := 0
	svc.statfs = func(string) (uint64, error) {
		calls++
		if calls == 1 {
			return 512, nil
		}
		return 4096, nil
	}
	if err := svc.AdmitDispatch(context.Background()); err != nil {
		t.Fatalf("best-effort cleanup failure must not fail admission: %v", err)
	}
}
