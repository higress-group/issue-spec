package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/commentrunner/state"
)

// A dispatcher RecordSessionResources upsert landing between the pass reload
// and phase-one persistence must survive: phase one merges into the fresh
// sidecar instead of replacing it wholesale.
func TestPhaseOnePersistenceMergesConcurrentUpsert(t *testing.T) {
	f := newEngineFixture(t)
	wsPath := filepath.Join(f.root, "ws-gone")
	hash := f.runtimeHash("o/r", "ps-1", wsPath)
	f.mkRuntimeDir(hash)
	st := state.NewState()
	if err := st.UpsertPublicSession(terminalSession("o/r", "ps-1", "ws-gone", wsPath, f.now.Add(-48*time.Hour))); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	engine := f.newEngine(f.stateLoader(st), nil)

	newHash := f.runtimeHash("o/r", "ps-new", filepath.Join(f.root, "ws-new"))
	concurrentID := ResourceID(ResourceKindSessionRuntime, "o/r", "ps-new", newHash)
	engine.prePersistHook = func() {
		other, err := OpenStore(f.root)
		if err != nil {
			t.Fatalf("open concurrent store: %v", err)
		}
		defer other.Close()
		if err := other.Update(func(st *StorageState) error {
			st.Resources[concurrentID] = PhysicalResource{
				ID: concurrentID, Kind: ResourceKindSessionRuntime,
				Path: filepath.Join(f.root, SessionsDirName, newHash),
				Repo: "o/r", PublicSessionID: "ps-new", PhysicalHash: newHash,
				FirstObservedAt: f.now, CleanupState: CleanupManaged,
			}
			return nil
		}); err != nil {
			t.Fatalf("concurrent upsert: %v", err)
		}
	}
	if _, err := engine.Reconcile(context.Background(), ReconcileOptions{Apply: true, OrphanGrace: DefaultOrphanGrace}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got, ok := engine.Store.State().Resources[concurrentID]
	if !ok {
		t.Fatalf("concurrent RecordSessionResources upsert was dropped by phase-one persistence")
	}
	if got.CleanupState != CleanupManaged || got.Repo != "o/r" || got.PublicSessionID != "ps-new" {
		t.Fatalf("concurrent record corrupted by merge: %+v", got)
	}
}

// Sweep deletions keep their compare-and-swap semantics: a vanished record is
// dropped only when no concurrent writer touched it since the reload.
func TestPhaseOneSweepDeleteSkipsConcurrentlyRerecorded(t *testing.T) {
	f := newEngineFixture(t)
	hash := f.runtimeHash("o/r", "ps-1", filepath.Join(f.root, "ws-gone"))
	id := ResourceID(ResourceKindSessionRuntime, "o/r", "ps-1", hash)
	absentPath := filepath.Join(f.root, SessionsDirName, hash)
	engine := f.newEngine(f.stateLoader(state.NewState()), nil)
	prior := PhysicalResource{
		ID: id, Kind: ResourceKindSessionRuntime, Path: absentPath,
		Repo: "o/r", PublicSessionID: "ps-1", PhysicalHash: hash,
		FirstObservedAt: f.now.Add(-72 * time.Hour), CleanupState: CleanupRetiredKnown,
	}
	if err := engine.Store.Update(func(st *StorageState) error {
		st.Resources[id] = prior
		return nil
	}); err != nil {
		t.Fatalf("seed vanished record: %v", err)
	}
	// The path reappears and a concurrent writer re-records ownership after the
	// engine's reload but before phase-one persistence.
	engine.prePersistHook = func() {
		if err := os.MkdirAll(absentPath, 0o700); err != nil {
			t.Fatalf("recreate runtime dir: %v", err)
		}
		other, err := OpenStore(f.root)
		if err != nil {
			t.Fatalf("open concurrent store: %v", err)
		}
		defer other.Close()
		if err := other.Update(func(st *StorageState) error {
			rerecorded := prior
			rerecorded.CleanupState = CleanupManaged
			st.Resources[id] = rerecorded
			return nil
		}); err != nil {
			t.Fatalf("concurrent re-record: %v", err)
		}
	}
	if _, err := engine.Reconcile(context.Background(), ReconcileOptions{Apply: true, OrphanGrace: DefaultOrphanGrace}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got, ok := engine.Store.State().Resources[id]
	if !ok {
		t.Fatalf("concurrently re-recorded resource must survive the sweep delete")
	}
	if got.CleanupState != CleanupManaged {
		t.Fatalf("concurrent re-record state=%q, want managed", got.CleanupState)
	}
	if _, err := os.Lstat(absentPath); err != nil {
		t.Fatalf("re-recorded runtime must remain: %v", err)
	}
}

// Untouched swept records are still garbage-collected by phase one.
func TestPhaseOneSweepDeleteDropsUntouchedRecord(t *testing.T) {
	f := newEngineFixture(t)
	hash := f.runtimeHash("o/r", "ps-1", filepath.Join(f.root, "ws-gone"))
	id := ResourceID(ResourceKindSessionRuntime, "o/r", "ps-1", hash)
	engine := f.newEngine(f.stateLoader(state.NewState()), nil)
	if err := engine.Store.Update(func(st *StorageState) error {
		st.Resources[id] = PhysicalResource{
			ID: id, Kind: ResourceKindSessionRuntime, Path: filepath.Join(f.root, SessionsDirName, hash),
			Repo: "o/r", PublicSessionID: "ps-1", PhysicalHash: hash,
			FirstObservedAt: f.now.Add(-72 * time.Hour), CleanupState: CleanupRetiredKnown,
		}
		return nil
	}); err != nil {
		t.Fatalf("seed vanished record: %v", err)
	}
	if _, err := engine.Reconcile(context.Background(), ReconcileOptions{Apply: true, OrphanGrace: DefaultOrphanGrace}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if _, ok := engine.Store.State().Resources[id]; ok {
		t.Fatalf("untouched vanished record must be swept")
	}
}
