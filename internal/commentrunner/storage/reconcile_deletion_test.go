package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/commentrunner/state"
)

// flipLoader returns st1 on the first load and st2 on every later load,
// simulating a control-plane change between classification and the
// deletion-time reload.
func flipLoader(first, later state.RunnerState) StateLoader {
	calls := 0
	return func(context.Context) (state.RunnerState, error) {
		calls++
		if calls == 1 {
			return first, nil
		}
		return later, nil
	}
}

func TestDeletionAbortsWhenStateChangesBeforeRemoval(t *testing.T) {
	f := newEngineFixture(t)
	wsPath := filepath.Join(f.root, "ws-gone")
	hash := f.runtimeHash("o/r", "ps-1", wsPath)
	f.mkRuntimeDir(hash)
	before := state.NewState()
	if err := before.UpsertPublicSession(terminalSession("o/r", "ps-1", "ws-gone", wsPath, f.now.Add(-48*time.Hour))); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	after := state.NewState()
	if err := after.UpsertPublicSession(terminalSession("o/r", "ps-1", "ws-gone", wsPath, f.now.Add(-48*time.Hour))); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// A new running job referencing the session appears before removal.
	if err := after.UpsertJob(state.Job{ID: "j-late", Repo: "o/r", PublicSessionID: "ps-1", Status: state.StatusRunning}); err != nil {
		t.Fatalf("upsert job: %v", err)
	}
	engine := f.newEngine(flipLoader(before, after), nil)
	report, err := engine.Reconcile(context.Background(), ReconcileOptions{Apply: true, OrphanGrace: DefaultOrphanGrace})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got := reportByID(t, report, ResourceID(ResourceKindSessionRuntime, "o/r", "ps-1", hash))
	if got.Action != ActionKept {
		t.Fatalf("action=%q, want kept after deletion-time revalidation abort", got.Action)
	}
	if _, err := os.Lstat(filepath.Join(f.root, ".sessions", hash)); err != nil {
		t.Fatalf("aborted deletion must keep the runtime: %v", err)
	}
	entry := engine.Store.State().Resources[got.ID]
	if entry.CleanupState == CleanupDeleting || entry.CleanupState == CleanupEligible {
		t.Fatalf("aborted deletion must not remain %q", entry.CleanupState)
	}
}

func TestDeletionRestoresRevalidatedClassAfterClassificationChange(t *testing.T) {
	f := newEngineFixture(t)
	wsPath := filepath.Join(f.root, "ws-late")
	hash := f.runtimeHash("o/r", "ps-late", wsPath)
	runtimeDir := f.mkRuntimeDir(hash)
	id := ResourceID(ResourceKindSessionRuntime, "", "", hash)
	// Pass start: no retained session, so the runtime is an orphan whose grace
	// elapsed long ago (prior sidecar observation proof).
	before := state.NewState()
	// Deletion-time reload: the owning session is retained again, terminal with
	// its workspace confirmed missing, so the class flips to retired_known.
	later := state.NewState()
	if err := later.UpsertPublicSession(terminalSession("o/r", "ps-late", "ws-late", wsPath, f.now.Add(-48*time.Hour))); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	engine := f.newEngine(flipLoader(before, later), nil)
	if err := engine.Store.Update(func(st *StorageState) error {
		st.Resources[id] = PhysicalResource{
			ID: id, Kind: ResourceKindSessionRuntime, Path: runtimeDir, PhysicalHash: hash,
			FirstObservedAt: f.now.Add(-30 * 24 * time.Hour), CleanupState: CleanupOrphanObserved,
		}
		return nil
	}); err != nil {
		t.Fatalf("seed orphan: %v", err)
	}
	report, err := engine.Reconcile(context.Background(), ReconcileOptions{Apply: true, OrphanGrace: DefaultOrphanGrace})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got := reportByID(t, report, id)
	if got.Action != ActionKept {
		t.Fatalf("action=%q, want kept after classification change", got.Action)
	}
	if _, err := os.Lstat(runtimeDir); err != nil {
		t.Fatalf("aborted deletion must keep the runtime: %v", err)
	}
	entry := engine.Store.State().Resources[id]
	if entry.CleanupState != CleanupRetiredKnown {
		t.Fatalf("restore state=%q, want class-derived %q after revalidation flip", entry.CleanupState, CleanupRetiredKnown)
	}
}

func TestDeletionDoesNotRemoveReplacementAfterFinalValidation(t *testing.T) {
	f := newEngineFixture(t)
	wsPath := filepath.Join(f.root, "ws-gone")
	hash := f.runtimeHash("o/r", "ps-race", wsPath)
	runtimeDir := f.mkRuntimeDir(hash)
	originalDir := runtimeDir + ".original"
	sentinel := filepath.Join(runtimeDir, "sentinel")
	st := state.NewState()
	if err := st.UpsertPublicSession(terminalSession("o/r", "ps-race", "ws-gone", wsPath, f.now.Add(-48*time.Hour))); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	engine := f.newEngine(f.stateLoader(st), nil)
	engine.preRemoveHook = func() {
		if err := os.Rename(runtimeDir, originalDir); err != nil {
			t.Fatalf("rename classified runtime: %v", err)
		}
		if err := os.Mkdir(runtimeDir, 0o700); err != nil {
			t.Fatalf("create replacement runtime: %v", err)
		}
		if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
			t.Fatalf("write replacement sentinel: %v", err)
		}
	}
	report, err := engine.Reconcile(context.Background(), ReconcileOptions{Apply: true, OrphanGrace: DefaultOrphanGrace})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got := reportByID(t, report, ResourceID(ResourceKindSessionRuntime, "o/r", "ps-race", hash))
	if got.Action == ActionDeleted {
		t.Fatalf("replacement must not be reported deleted")
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "keep" {
		t.Fatalf("replacement sentinel was removed or changed: data=%q err=%v", data, err)
	}
	if _, err := os.Stat(originalDir); err != nil {
		t.Fatalf("classified original must remain after identity replacement: %v", err)
	}
}

func TestCrashedDeletingEntryCompletesWhenPathAbsent(t *testing.T) {
	f := newEngineFixture(t)
	hash := strings.Repeat("ab", 16)
	engine := f.newEngine(f.stateLoader(state.NewState()), nil)
	id := ResourceID(ResourceKindSessionRuntime, "o/r", "ps-1", hash)
	// Simulate a crash between RemoveAll and the final sidecar write.
	if err := engine.Store.Update(func(st *StorageState) error {
		st.Resources[id] = PhysicalResource{
			ID: id, Kind: ResourceKindSessionRuntime, Path: filepath.Join(f.root, ".sessions", hash),
			Repo: "o/r", PublicSessionID: "ps-1", PhysicalHash: hash,
			CleanupState: CleanupDeleting, CleanupAttemptID: "attempt-crash",
		}
		return nil
	}); err != nil {
		t.Fatalf("seed deleting: %v", err)
	}
	report, err := engine.Reconcile(context.Background(), ReconcileOptions{Apply: true, OrphanGrace: DefaultOrphanGrace})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got := reportByID(t, report, id)
	if got.Action != ActionDeleted || got.AttemptID != "attempt-crash" {
		t.Fatalf("action=%q attempt=%q, want deleted with original attempt", got.Action, got.AttemptID)
	}
	if _, ok := engine.Store.State().Resources[id]; ok {
		t.Fatalf("completed deleting entry must be swept")
	}
}

func TestCrashedDeletingEntryRetriesWhenPathRemains(t *testing.T) {
	f := newEngineFixture(t)
	wsPath := filepath.Join(f.root, "ws-gone")
	hash := f.runtimeHash("o/r", "ps-1", wsPath)
	runtimeDir := f.mkRuntimeDir(hash)
	if err := os.WriteFile(filepath.Join(runtimeDir, "partial"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write partial: %v", err)
	}
	st := state.NewState()
	if err := st.UpsertPublicSession(terminalSession("o/r", "ps-1", "ws-gone", wsPath, f.now.Add(-48*time.Hour))); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	engine := f.newEngine(f.stateLoader(st), nil)
	id := ResourceID(ResourceKindSessionRuntime, "o/r", "ps-1", hash)
	// Simulate a crash mid-RemoveAll: deleting persisted, partial tree remains.
	if err := engine.Store.Update(func(st *StorageState) error {
		st.Resources[id] = PhysicalResource{
			ID: id, Kind: ResourceKindSessionRuntime, Path: runtimeDir,
			Repo: "o/r", PublicSessionID: "ps-1", WorkspaceID: "ws-gone", PhysicalHash: hash,
			CleanupState: CleanupDeleting, CleanupAttemptID: "attempt-crash",
		}
		return nil
	}); err != nil {
		t.Fatalf("seed deleting: %v", err)
	}
	report, err := engine.Reconcile(context.Background(), ReconcileOptions{Apply: true, OrphanGrace: DefaultOrphanGrace})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got := reportByID(t, report, id)
	if got.Action != ActionDeleted {
		t.Fatalf("action=%q, want retried deletion completed", got.Action)
	}
	if _, err := os.Lstat(runtimeDir); !os.IsNotExist(err) {
		t.Fatalf("partial tree must be removed, err=%v", err)
	}
	if got.Bytes != 1 {
		t.Fatalf("reclaimed bytes=%d, want 1", got.Bytes)
	}
}

func TestResourceLockConflictProtectsResource(t *testing.T) {
	f := newEngineFixture(t)
	wsPath := filepath.Join(f.root, "ws-gone")
	hashLocked := f.runtimeHash("o/r", "ps-1", wsPath)
	hashFree := strings.Repeat("ef", 16)
	f.mkRuntimeDir(hashLocked)
	f.mkRuntimeDir(hashFree)
	st := state.NewState()
	if err := st.UpsertPublicSession(terminalSession("o/r", "ps-1", "ws-gone", wsPath, f.now.Add(-48*time.Hour))); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	engine := f.newEngine(f.stateLoader(st), nil)
	lockedID := ResourceID(ResourceKindSessionRuntime, "o/r", "ps-1", hashLocked)
	unlock, err := engine.tryResourceLock(lockedID)
	if err != nil {
		t.Fatalf("pre-lock resource: %v", err)
	}
	defer unlock()
	report, err := engine.Reconcile(context.Background(), ReconcileOptions{Apply: true, OrphanGrace: DefaultOrphanGrace})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got := reportByID(t, report, lockedID)
	if got.Action != ActionKept {
		t.Fatalf("locked resource action=%q, want kept", got.Action)
	}
	if _, err := os.Lstat(filepath.Join(f.root, ".sessions", hashLocked)); err != nil {
		t.Fatalf("locked runtime must remain: %v", err)
	}
	// The independent orphan resource was observed normally: failures isolate.
	orphan := reportByID(t, report, ResourceID(ResourceKindSessionRuntime, "", "", hashFree))
	if orphan.Action != ActionObserved {
		t.Fatalf("independent resource action=%q, want observed", orphan.Action)
	}
}

func TestMultipleRuntimeHashesForOneSession(t *testing.T) {
	f := newEngineFixture(t)
	wsPath := f.mkWorkspace("ws-active")
	legacyCWD := filepath.Join(f.root, "legacy-spelling", "ws-active")
	currentHash := f.runtimeHash("o/r", "ps-1", wsPath)
	legacyHash := f.runtimeHash("o/r", "ps-1", legacyCWD)
	f.mkRuntimeDir(currentHash)
	f.mkRuntimeDir(legacyHash)
	st := state.NewState()
	session := terminalSession("o/r", "ps-1", "ws-active", wsPath, f.now.Add(-time.Hour))
	session.Status = state.StatusRunning
	if err := st.UpsertPublicSession(session); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	engine := f.newEngine(f.stateLoader(st), nil)
	engine.LegacyEvidence = map[string][]string{state.PublicSessionKey("o/r", "ps-1"): {legacyCWD}}
	report, err := engine.Reconcile(context.Background(), ReconcileOptions{Apply: true, OrphanGrace: DefaultOrphanGrace})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	current := reportByID(t, report, ResourceID(ResourceKindSessionRuntime, "o/r", "ps-1", currentHash))
	if current.Class != ClassProtected || current.Action != ActionKept {
		t.Fatalf("current hash class=%q action=%q, want protected/kept", current.Class, current.Action)
	}
	// The legacy hash is not mapped by current state; raw evidence never
	// broadens protection of a mapped session, so it is orphaned, not deleted.
	legacy := reportByID(t, report, ResourceID(ResourceKindSessionRuntime, "", "", legacyHash))
	if legacy.Class != ClassOrphanObserved || legacy.Action != ActionObserved {
		t.Fatalf("legacy hash class=%q action=%q, want orphan_observed/observed", legacy.Class, legacy.Action)
	}
}

func TestRawLegacyEvidenceProvesOwnershipForPrunedSession(t *testing.T) {
	f := newEngineFixture(t)
	legacyCWD := filepath.Join(f.root, "ws-legacy")
	hash := f.runtimeHash("o/r", "ps-old", legacyCWD)
	f.mkRuntimeDir(hash)
	engine := f.newEngine(f.stateLoader(state.NewState()), nil)
	engine.LegacyEvidence = map[string][]string{state.PublicSessionKey("o/r", "ps-old"): {legacyCWD}}
	report, err := engine.Reconcile(context.Background(), ReconcileOptions{Apply: true, OrphanGrace: DefaultOrphanGrace})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got := reportByID(t, report, ResourceID(ResourceKindSessionRuntime, "o/r", "ps-old", hash))
	if got.Class != ClassRetiredKnown || got.Action != ActionDeleted {
		t.Fatalf("class=%q action=%q, want retired_known/deleted from raw proof", got.Class, got.Action)
	}
	if _, err := os.Lstat(filepath.Join(f.root, ".sessions", hash)); !os.IsNotExist(err) {
		t.Fatalf("raw-proven runtime must be removed, err=%v", err)
	}
}

func TestRawEvidenceWithMismatchedHashDoesNotProve(t *testing.T) {
	f := newEngineFixture(t)
	hash := strings.Repeat("ab", 16)
	f.mkRuntimeDir(hash)
	engine := f.newEngine(f.stateLoader(state.NewState()), nil)
	// Evidence for a different path cannot reproduce this hash.
	engine.LegacyEvidence = map[string][]string{state.PublicSessionKey("o/r", "ps-old"): {filepath.Join(f.root, "ws-other")}}
	report, err := engine.Reconcile(context.Background(), ReconcileOptions{Apply: true, OrphanGrace: DefaultOrphanGrace})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got := reportByID(t, report, ResourceID(ResourceKindSessionRuntime, "", "", hash))
	if got.Class != ClassOrphanObserved {
		t.Fatalf("class=%q, want orphan_observed", got.Class)
	}
}

func TestSidecarLossRestartsOrphanObservation(t *testing.T) {
	f := newEngineFixture(t)
	hash := strings.Repeat("de", 16)
	f.mkRuntimeDir(hash)
	engine := f.newEngine(f.stateLoader(state.NewState()), nil)
	if _, err := engine.Reconcile(context.Background(), ReconcileOptions{Apply: true, OrphanGrace: time.Hour}); err != nil {
		t.Fatalf("Reconcile first: %v", err)
	}
	// Sidecar lost: ownership and observation proof is gone.
	if err := os.Remove(filepath.Join(f.root, ".storage", "state.json")); err != nil {
		t.Fatalf("remove sidecar: %v", err)
	}
	f.now = f.now.Add(2 * time.Hour)
	engine2 := f.newEngine(f.stateLoader(state.NewState()), nil)
	report, err := engine2.Reconcile(context.Background(), ReconcileOptions{Apply: true, OrphanGrace: time.Hour})
	if err != nil {
		t.Fatalf("Reconcile second: %v", err)
	}
	if report.SidecarStatus != SidecarRebuilt {
		t.Fatalf("sidecar status=%q, want rebuilt", report.SidecarStatus)
	}
	got := reportByID(t, report, ResourceID(ResourceKindSessionRuntime, "", "", hash))
	if got.Action != ActionObserved {
		t.Fatalf("action=%q, want observation restart after sidecar loss", got.Action)
	}
	if _, err := os.Lstat(filepath.Join(f.root, ".sessions", hash)); err != nil {
		t.Fatalf("restarted orphan must remain: %v", err)
	}
}

func TestStateUnavailableBlocksDestructiveCleanup(t *testing.T) {
	f := newEngineFixture(t)
	wsPath := filepath.Join(f.root, "ws-gone")
	hash := f.runtimeHash("o/r", "ps-1", wsPath)
	f.mkRuntimeDir(hash)
	engine := f.newEngine(func(context.Context) (state.RunnerState, error) {
		return state.RunnerState{}, errors.New("state store unavailable")
	}, nil)
	_, err := engine.Reconcile(context.Background(), ReconcileOptions{Apply: true, OrphanGrace: DefaultOrphanGrace})
	if err == nil {
		t.Fatalf("reconcile must fail when state is unavailable")
	}
	if _, statErr := os.Lstat(filepath.Join(f.root, ".sessions", hash)); statErr != nil {
		t.Fatalf("no destructive cleanup may run without state: %v", statErr)
	}
}

func TestPoolProvenEmptyIsDeleted(t *testing.T) {
	f := newEngineFixture(t)
	wsPath := f.mkWorkspace("ws-pool")
	hash := f.poolHash("o/r", "ps-1", wsPath)
	poolDir := f.mkPoolDir(hash)
	st := state.NewState()
	if err := st.UpsertPublicSession(terminalSession("o/r", "ps-1", "ws-pool", wsPath, f.now.Add(-48*time.Hour))); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	inspector := func(_ context.Context, integrationRoot, poolRoot string) (PoolInspection, error) {
		if integrationRoot != wsPath {
			t.Fatalf("integration root=%q, want %q", integrationRoot, wsPath)
		}
		return PoolInspection{ClonePresent: true, RegistryComplete: true}, nil
	}
	engine := f.newEngine(f.stateLoader(st), inspector)
	engine.PoolRemover = func(context.Context, string, string) (PoolInspection, bool, error) {
		return PoolInspection{ClonePresent: true, RegistryComplete: true}, true, os.RemoveAll(poolDir)
	}
	report, err := engine.Reconcile(context.Background(), ReconcileOptions{Apply: true, OrphanGrace: DefaultOrphanGrace})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got := reportByID(t, report, ResourceID(ResourceKindSessionProcessPool, "o/r", "ps-1", hash))
	if got.Class != ClassRetiredKnown || got.Action != ActionDeleted {
		t.Fatalf("class=%q action=%q reason=%q, want retired_known/deleted", got.Class, got.Action, got.Reason)
	}
	if _, err := os.Lstat(poolDir); !os.IsNotExist(err) {
		t.Fatalf("proven-empty pool must be removed, err=%v", err)
	}
}

func TestPoolDeletionRequiresAtomicPoolRemover(t *testing.T) {
	f := newEngineFixture(t)
	wsPath := f.mkWorkspace("ws-pool")
	hash := f.poolHash("o/r", "ps-pool", wsPath)
	poolDir := f.mkPoolDir(hash)
	st := state.NewState()
	if err := st.UpsertPublicSession(terminalSession("o/r", "ps-pool", "ws-pool", wsPath, f.now.Add(-48*time.Hour))); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	engine := f.newEngine(f.stateLoader(st), func(context.Context, string, string) (PoolInspection, error) {
		return PoolInspection{ClonePresent: true, RegistryComplete: true}, nil
	})
	report, err := engine.Reconcile(context.Background(), ReconcileOptions{Apply: true, OrphanGrace: DefaultOrphanGrace})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got := reportByID(t, report, ResourceID(ResourceKindSessionProcessPool, "o/r", "ps-pool", hash))
	if got.Action != ActionPreserved {
		t.Fatalf("pool action=%q, want preserved without atomic remover", got.Action)
	}
	if _, err := os.Stat(poolDir); err != nil {
		t.Fatalf("pool must remain without atomic remover: %v", err)
	}
}

func TestPoolHardFailureDefersOwningWorkspace(t *testing.T) {
	f := newEngineFixture(t)
	wsPath := f.mkWorkspace("ws-pool")
	hash := f.poolHash("o/r", "ps-1", wsPath)
	poolDir := f.mkPoolDir(hash)
	st := state.NewState()
	if err := st.UpsertPublicSession(terminalSession("o/r", "ps-1", "ws-pool", wsPath, f.now.Add(-48*time.Hour))); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// The classification load succeeds; the deletion-time reload fails hard.
	calls := 0
	loader := func(context.Context) (state.RunnerState, error) {
		calls++
		if calls == 1 {
			return st, nil
		}
		return state.RunnerState{}, errors.New("state store unavailable")
	}
	inspector := func(context.Context, string, string) (PoolInspection, error) {
		return PoolInspection{ClonePresent: true, RegistryComplete: true}, nil
	}
	engine := f.newEngine(loader, inspector)
	report, err := engine.Reconcile(context.Background(), ReconcileOptions{Apply: true, OrphanGrace: DefaultOrphanGrace})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got := reportByID(t, report, ResourceID(ResourceKindSessionProcessPool, "o/r", "ps-1", hash))
	if got.Action != ActionFailed {
		t.Fatalf("action=%q reason=%q, want failed after hard reload error", got.Action, got.Reason)
	}
	if _, err := os.Lstat(poolDir); err != nil {
		t.Fatalf("failed pool deletion must keep the pool: %v", err)
	}
	found := false
	for _, id := range report.DeferredWorkspaceIDs {
		if id == "ws-pool" {
			found = true
		}
	}
	if !found {
		t.Fatalf("pool hard failure must defer owning workspace for evidence, deferred=%v", report.DeferredWorkspaceIDs)
	}
}

func TestPoolCloneMissingIsPreservedWithRemediation(t *testing.T) {
	f := newEngineFixture(t)
	wsPath := filepath.Join(f.root, "ws-gone")
	hash := f.poolHash("o/r", "ps-1", wsPath)
	poolDir := f.mkPoolDir(hash)
	st := state.NewState()
	if err := st.UpsertPublicSession(terminalSession("o/r", "ps-1", "ws-gone", wsPath, f.now.Add(-48*time.Hour))); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	engine := f.newEngine(f.stateLoader(st), nil)
	report, err := engine.Reconcile(context.Background(), ReconcileOptions{Apply: true, OrphanGrace: DefaultOrphanGrace})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got := reportByID(t, report, ResourceID(ResourceKindSessionProcessPool, "o/r", "ps-1", hash))
	if got.Action != ActionPreserved {
		t.Fatalf("action=%q, want preserved for clone-missing pool", got.Action)
	}
	if _, err := os.Lstat(poolDir); err != nil {
		t.Fatalf("clone-missing pool must remain: %v", err)
	}
	foundRemediation := false
	for _, d := range report.Diagnostics {
		if strings.Contains(d, "pool") && strings.Contains(d, "clone") {
			foundRemediation = true
		}
	}
	if !foundRemediation {
		t.Fatalf("expected operator remediation diagnostic, got %+v", report.Diagnostics)
	}
}

func TestPoolNonEmptyIsPreservedWithRemediation(t *testing.T) {
	f := newEngineFixture(t)
	wsPath := f.mkWorkspace("ws-pool")
	hash := f.poolHash("o/r", "ps-1", wsPath)
	poolDir := f.mkPoolDir(hash)
	st := state.NewState()
	if err := st.UpsertPublicSession(terminalSession("o/r", "ps-1", "ws-pool", wsPath, f.now.Add(-48*time.Hour))); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	inspector := func(_ context.Context, _, _ string) (PoolInspection, error) {
		return PoolInspection{ClonePresent: true, RegistryComplete: true, ActiveLeases: 1}, nil
	}
	engine := f.newEngine(f.stateLoader(st), inspector)
	report, err := engine.Reconcile(context.Background(), ReconcileOptions{Apply: true, OrphanGrace: DefaultOrphanGrace})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got := reportByID(t, report, ResourceID(ResourceKindSessionProcessPool, "o/r", "ps-1", hash))
	if got.Action != ActionPreserved {
		t.Fatalf("action=%q, want preserved for active-lease pool", got.Action)
	}
	if _, err := os.Lstat(poolDir); err != nil {
		t.Fatalf("uncertain pool must remain: %v", err)
	}
}

func TestPoolWithActiveSessionIsProtected(t *testing.T) {
	f := newEngineFixture(t)
	wsPath := f.mkWorkspace("ws-pool")
	hash := f.poolHash("o/r", "ps-1", wsPath)
	f.mkPoolDir(hash)
	st := state.NewState()
	session := terminalSession("o/r", "ps-1", "ws-pool", wsPath, f.now.Add(-time.Hour))
	session.Status = state.StatusRunning
	if err := st.UpsertPublicSession(session); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	engine := f.newEngine(f.stateLoader(st), nil)
	report, err := engine.Reconcile(context.Background(), ReconcileOptions{Apply: true, OrphanGrace: DefaultOrphanGrace})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got := reportByID(t, report, ResourceID(ResourceKindSessionProcessPool, "o/r", "ps-1", hash))
	if got.Class != ClassProtected || got.Action != ActionKept {
		t.Fatalf("class=%q action=%q, want protected/kept", got.Class, got.Action)
	}
}

func TestOrphanPoolNeverForceAbandoned(t *testing.T) {
	f := newEngineFixture(t)
	hash := strings.Repeat("be", 16)
	poolDir := f.mkPoolDir(hash)
	engine := f.newEngine(f.stateLoader(state.NewState()), nil)
	if _, err := engine.Reconcile(context.Background(), ReconcileOptions{Apply: true, OrphanGrace: time.Hour}); err != nil {
		t.Fatalf("Reconcile observe: %v", err)
	}
	f.now = f.now.Add(2 * time.Hour)
	report, err := engine.Reconcile(context.Background(), ReconcileOptions{Apply: true, OrphanGrace: time.Hour})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got := reportByID(t, report, ResourceID(ResourceKindSessionProcessPool, "", "", hash))
	if got.Class != ClassOrphanObserved || got.Action != ActionPreserved {
		t.Fatalf("class=%q action=%q, want orphan_observed/preserved", got.Class, got.Action)
	}
	if _, err := os.Lstat(poolDir); err != nil {
		t.Fatalf("orphan pool must never be force-abandoned: %v", err)
	}
}
