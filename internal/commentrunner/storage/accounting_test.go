package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/commentrunner/state"
)

func TestNormalPollReconcileSkipsRecursiveAccounting(t *testing.T) {
	f := newEngineFixture(t)
	wsPath := f.mkWorkspace("ws-valid")
	hash := f.runtimeHash("o/r", "ps-1", wsPath)
	runtimeDir := f.mkRuntimeDir(hash)
	writeFile(t, filepath.Join(runtimeDir, "home", "payload"), 4096)
	st := state.NewState()
	if err := st.UpsertPublicSession(terminalSession("o/r", "ps-1", "ws-valid", wsPath, f.now.Add(-time.Hour))); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	engine := f.newEngine(f.stateLoader(st), nil)

	// Automatic pass (poll/startup): no measurement of kept resources.
	report, err := engine.Reconcile(context.Background(), ReconcileOptions{Apply: true, OrphanGrace: DefaultOrphanGrace})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got := reportByID(t, report, ResourceID(ResourceKindSessionRuntime, "o/r", "ps-1", hash))
	if got.Bytes != 0 {
		t.Fatalf("normal pass measured kept resource bytes=%d; recursive accounting is operator-only", got.Bytes)
	}

	// Explicit operator run measures every class.
	explicit, err := engine.Reconcile(context.Background(), ReconcileOptions{Apply: false, OrphanGrace: DefaultOrphanGrace, MeasureAll: true})
	if err != nil {
		t.Fatalf("Reconcile explicit: %v", err)
	}
	measured := reportByID(t, explicit, ResourceID(ResourceKindSessionRuntime, "o/r", "ps-1", hash))
	if measured.Bytes != 4096 {
		t.Fatalf("explicit run bytes=%d, want 4096", measured.Bytes)
	}
}

func TestMeasureOnlyDeletionTargetsOnApply(t *testing.T) {
	f := newEngineFixture(t)
	wsGone := filepath.Join(f.root, "ws-gone")
	hash := f.runtimeHash("o/r", "ps-1", wsGone)
	runtimeDir := f.mkRuntimeDir(hash)
	writeFile(t, filepath.Join(runtimeDir, "home", "stale"), 512)
	wsValid := f.mkWorkspace("ws-valid")
	hashValid := f.runtimeHash("o/r", "ps-2", wsValid)
	validDir := f.mkRuntimeDir(hashValid)
	writeFile(t, filepath.Join(validDir, "home", "keep"), 2048)
	st := state.NewState()
	if err := st.UpsertPublicSession(terminalSession("o/r", "ps-1", "ws-gone", wsGone, f.now.Add(-72*time.Hour))); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := st.UpsertPublicSession(terminalSession("o/r", "ps-2", "ws-valid", wsValid, f.now.Add(-time.Hour))); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	engine := f.newEngine(f.stateLoader(st), nil)
	report, err := engine.Reconcile(context.Background(), ReconcileOptions{Apply: true, OrphanGrace: DefaultOrphanGrace})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if report.ReclaimedBytes != 512 {
		t.Fatalf("reclaimed=%d, want 512", report.ReclaimedBytes)
	}
	kept := reportByID(t, report, ResourceID(ResourceKindSessionRuntime, "o/r", "ps-2", hashValid))
	if kept.Bytes != 0 {
		t.Fatalf("kept resource measured on automatic pass: %d", kept.Bytes)
	}
}
