package storage

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/commentrunner/state"
)

func newServiceFixture(t *testing.T, minFree int64, statfs StatfsFunc) (*Service, string) {
	t.Helper()
	root := testRoot(t)
	svc, err := NewService(ServiceConfig{
		WorkspaceRoot: root,
		StateLoader:   func(context.Context) (state.RunnerState, error) { return state.NewState(), nil },
		MinFreeBytes:  minFree,
		OrphanGrace:   DefaultOrphanGrace,
		Statfs:        statfs,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc, root
}

func TestAdmissionDisabledWhenZero(t *testing.T) {
	svc, _ := newServiceFixture(t, 0, func(string) (uint64, error) {
		return 0, errors.New("statfs must not be called when disabled")
	})
	if err := svc.AdmitDispatch(context.Background()); err != nil {
		t.Fatalf("AdmitDispatch with disabled threshold: %v", err)
	}
}

func TestAdmissionPassesAboveThreshold(t *testing.T) {
	calls := 0
	svc, _ := newServiceFixture(t, 1024, func(string) (uint64, error) {
		calls++
		return 2048, nil
	})
	if err := svc.AdmitDispatch(context.Background()); err != nil {
		t.Fatalf("AdmitDispatch: %v", err)
	}
	if calls != 1 {
		t.Fatalf("statfs calls=%d, want exactly 1 (no recursive scan)", calls)
	}
}

func TestAdmissionFailsClosedOnStatfsError(t *testing.T) {
	svc, _ := newServiceFixture(t, 1024, func(string) (uint64, error) {
		return 0, errors.New("statfs broken")
	})
	if err := svc.AdmitDispatch(context.Background()); err == nil {
		t.Fatalf("statfs failure under configured threshold must fail closed")
	}
}

func TestAdmissionRecoversAfterPressuredCleanup(t *testing.T) {
	root := testRoot(t)
	svc, err := NewService(ServiceConfig{
		WorkspaceRoot: root,
		StateLoader:   func(context.Context) (state.RunnerState, error) { return state.NewState(), nil },
		MinFreeBytes:  1024,
		OrphanGrace:   0,
		Statfs: func(string) (uint64, error) {
			return 512, nil
		},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	// Orphan past grace gets reclaimed during the pressured pass; the fake
	// statfs then reports recovery on the re-read.
	hash := strings.Repeat("de", 16)
	runtimeDir := filepath.Join(root, ".sessions", hash)
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	engine, err := NewEngine(EngineConfig{WorkspaceRoot: root, StateLoader: func(context.Context) (state.RunnerState, error) {
		return state.NewState(), nil
	}})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	if _, err := engine.Reconcile(context.Background(), ReconcileOptions{Apply: true, OrphanGrace: 0}); err != nil {
		t.Fatalf("seed orphan: %v", err)
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
		t.Fatalf("AdmitDispatch after recovery: %v", err)
	}
	if calls != 2 {
		t.Fatalf("statfs calls=%d, want 2 (pressure check + post-cleanup recheck)", calls)
	}
	if _, err := os.Lstat(runtimeDir); !os.IsNotExist(err) {
		t.Fatalf("pressured cleanup must reclaim grace-expired orphan, err=%v", err)
	}
}

func TestAdmissionStillLowAfterCleanup(t *testing.T) {
	svc, _ := newServiceFixture(t, 4096, func(string) (uint64, error) {
		return 512, nil
	})
	err := svc.AdmitDispatch(context.Background())
	if !errors.Is(err, ErrStoragePressure) {
		t.Fatalf("err=%v, want ErrStoragePressure", err)
	}
}

func TestRecordSessionResourcesUpsertsRuntimeAndPool(t *testing.T) {
	svc, root := newServiceFixture(t, 0, nil)
	wsPath := filepath.Join(root, "ws-1")
	if err := os.MkdirAll(wsPath, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := svc.RecordSessionResources(context.Background(), "o/r", "ps-1", wsPath); err != nil {
		t.Fatalf("RecordSessionResources: %v", err)
	}
	runtimeHash, err := SessionRuntimeHash("o/r", "ps-1", wsPath)
	if err != nil {
		t.Fatalf("runtime hash: %v", err)
	}
	poolHash, err := SessionProcessPoolHash("o/r", "ps-1", wsPath)
	if err != nil {
		t.Fatalf("pool hash: %v", err)
	}
	st := svc.Store().State()
	runtimeEntry, ok := st.Resources[ResourceID(ResourceKindSessionRuntime, "o/r", "ps-1", runtimeHash)]
	if !ok || runtimeEntry.CleanupState != CleanupManaged || !runtimeEntry.Owned() {
		t.Fatalf("runtime entry = %+v ok=%v", runtimeEntry, ok)
	}
	poolEntry, ok := st.Resources[ResourceID(ResourceKindSessionProcessPool, "o/r", "ps-1", poolHash)]
	if !ok || poolEntry.CleanupState != CleanupManaged || !poolEntry.Owned() {
		t.Fatalf("pool entry = %+v ok=%v", poolEntry, ok)
	}
	// Idempotent touch.
	if err := svc.RecordSessionResources(context.Background(), "o/r", "ps-1", wsPath); err != nil {
		t.Fatalf("touch: %v", err)
	}
}

func TestRecordSessionResourcesFailsOnReportOnlyStore(t *testing.T) {
	svc, root := newServiceFixture(t, 0, nil)
	if err := svc.Store().Update(func(st *StorageState) error {
		st.Resources["session_runtime:o/r:ps-x:"+strings.Repeat("ab", 16)] = PhysicalResource{ID: "x"}
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Foreign identity flips the store report-only.
	sidecarPath := filepath.Join(root, ".storage", "state.json")
	data, err := os.ReadFile(sidecarPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	decoded["root_identity"] = strings.Repeat("ff", 32)
	raw, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if err := os.WriteFile(sidecarPath, raw, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	svc2, err := NewService(ServiceConfig{
		WorkspaceRoot: root,
		StateLoader:   func(context.Context) (state.RunnerState, error) { return state.NewState(), nil },
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	wsPath := filepath.Join(root, "ws-1")
	if err := os.MkdirAll(wsPath, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := svc2.RecordSessionResources(context.Background(), "o/r", "ps-1", wsPath); err == nil {
		t.Fatalf("recording must fail closed on a report-only sidecar")
	}
}
