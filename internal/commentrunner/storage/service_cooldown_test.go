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

func TestAdmissionCooldownSkipsRepeatedReconcile(t *testing.T) {
	root := testRoot(t)
	svc, err := NewService(ServiceConfig{
		WorkspaceRoot: root,
		StateLoader:   func(context.Context) (state.RunnerState, error) { return state.NewState(), nil },
		MinFreeBytes:  4096,
		Statfs:        func(string) (uint64, error) { return 512, nil },
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	svc.poolInspectBackoff = time.Minute
	// First pressured admission reconciles once (nothing to reclaim) then fails.
	err = svc.AdmitDispatch(context.Background())
	if !errors.Is(err, ErrStoragePressure) {
		t.Fatalf("first admission err=%v", err)
	}
	// Second admission inside the cooldown must not reconcile again.
	if err := svc.AdmitDispatch(context.Background()); !errors.Is(err, ErrStoragePressure) {
		t.Fatalf("cooldown admission err=%v", err)
	}
	// The cooldown is observable: no second reconcile ran (no resource locks
	// or sidecar updates beyond the first pass). Verify via the sidecar mtime.
	info1, err := os.Stat(filepath.Join(root, ".storage", "state.json"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("stat sidecar: %v", err)
	}
	if err := svc.AdmitDispatch(context.Background()); !errors.Is(err, ErrStoragePressure) {
		t.Fatalf("cooldown admission err=%v", err)
	}
	info2, _ := os.Stat(filepath.Join(root, ".storage", "state.json"))
	if (info1 == nil) != (info2 == nil) || (info1 != nil && !info1.ModTime().Equal(info2.ModTime())) {
		t.Fatalf("cooldown pass rewrote the sidecar")
	}
}

func TestRecordSessionResourcesSkipsUnchangedWrite(t *testing.T) {
	svc, root := newServiceFixture(t, 0, nil)
	wsPath := filepath.Join(root, "ws-1")
	if err := os.MkdirAll(wsPath, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := svc.RecordSessionResources(context.Background(), "o/r", "ps-1", wsPath); err != nil {
		t.Fatalf("record: %v", err)
	}
	sidecarPath := filepath.Join(root, ".storage", "state.json")
	before, err := os.Stat(sidecarPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	if err := svc.RecordSessionResources(context.Background(), "o/r", "ps-1", wsPath); err != nil {
		t.Fatalf("touch: %v", err)
	}
	after, err := os.Stat(sidecarPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("unchanged touch rewrote the sidecar")
	}
}

func TestPoolInspectionBackoffPreservesWithoutInspection(t *testing.T) {
	f := newEngineFixture(t)
	wsPath := f.mkWorkspace("ws-pool")
	hash := f.poolHash("o/r", "ps-1", wsPath)
	poolDir := f.mkPoolDir(hash)
	st := state.NewState()
	if err := st.UpsertPublicSession(terminalSession("o/r", "ps-1", "ws-pool", wsPath, f.now.Add(-72*time.Hour))); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	inspections := 0
	inspector := func(context.Context, string, string) (PoolInspection, error) {
		inspections++
		return PoolInspection{ClonePresent: true, RegistryComplete: true, ActiveLeases: 1}, nil
	}
	engine := f.newEngine(f.stateLoader(st), inspector)
	allowed := map[string]bool{}
	engine.poolInspectGate = func(id string, _ time.Time) bool {
		if allowed[id] {
			return true
		}
		return false
	}
	report, err := engine.Reconcile(context.Background(), ReconcileOptions{Apply: true, OrphanGrace: DefaultOrphanGrace})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if inspections != 0 {
		t.Fatalf("gate blocked pass must not inspect, got %d", inspections)
	}
	got := reportByID(t, report, ResourceID(ResourceKindSessionProcessPool, "o/r", "ps-1", hash))
	if got.Action != ActionPreserved || !strings.Contains(got.Reason, "backoff") {
		t.Fatalf("action=%q reason=%q, want preserved/backoff", got.Action, got.Reason)
	}
	if _, err := os.Lstat(poolDir); err != nil {
		t.Fatalf("pool must remain: %v", err)
	}
}

func TestCorruptSidecarSurfacesDiagnostic(t *testing.T) {
	f := newEngineFixture(t)
	engine := f.newEngine(f.stateLoader(state.NewState()), nil)
	if err := engine.Store.Update(func(st *StorageState) error {
		st.Resources["session_runtime:o/r:ps-9:"+strings.Repeat("ab", 16)] = PhysicalResource{ID: "x", Kind: ResourceKindSessionRuntime}
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(f.root, ".storage", "state.json"), []byte("{bad"), 0o600); err != nil {
		t.Fatalf("corrupt: %v", err)
	}
	report, err := engine.Reconcile(context.Background(), ReconcileOptions{Apply: true, OrphanGrace: DefaultOrphanGrace})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	found := false
	for _, d := range report.Diagnostics {
		if strings.Contains(d, "corrupt") {
			found = true
		}
	}
	if !found {
		t.Fatalf("corrupt rebuild must be diagnosed, got %+v", report.Diagnostics)
	}
}
