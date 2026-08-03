package storage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/commentrunner/state"
)

// TestUpgradeAcceptanceFixture models the design's immediate recovery
// acceptance fixture against a sanitized runner root: first apply must
// preserve resumable/protected resources, immediately reclaim proven
// retired-known runtimes, observe unmatched directories, remove only empty
// retired pools, report exact actions, and be idempotent.
func TestUpgradeAcceptanceFixture(t *testing.T) {
	f := newEngineFixture(t)
	root := f.root
	repo := "o/r"

	// Only the to-be-pruned session's workspace and runtime exist for the seed
	// pass; every other fixture directory is created after seeding so the first
	// applied migration sees them fresh.
	wsProven := f.mkWorkspace("ws-proven")
	hashProven := f.runtimeHash(repo, "ps-proven", wsProven)
	runtimeProven := f.mkRuntimeDir(hashProven)
	writeFile(t, filepath.Join(runtimeProven, "gh", "hosts.yml"), 64)

	wsValid := filepath.Join(root, "ws-valid")
	wsMissing := filepath.Join(root, "ws-missing")
	wsLegacyDeleted := filepath.Join(root, "ws-legacy-deleted")
	wsMulti := filepath.Join(root, "ws-multi")
	wsPoolEmpty := filepath.Join(root, "ws-pool-empty")
	wsPoolMissing := filepath.Join(root, "ws-pool-missing")

	st := state.NewState()
	lastUsed := f.now.Add(-72 * time.Hour)
	upsert := func(sid, wsID, wsPath string, status state.LifecycleStatus) {
		session := terminalSession(repo, sid, wsID, wsPath, lastUsed)
		session.Status = status
		if err := st.UpsertPublicSession(session); err != nil {
			t.Fatalf("upsert %s: %v", sid, err)
		}
	}
	upsert("ps-valid", "ws-valid", wsValid, state.StatusCompleted)
	upsert("ps-missing", "ws-missing", wsMissing, state.StatusCompleted)
	upsert("ps-legacy", "ws-legacy-deleted", wsLegacyDeleted, state.StatusCompleted)
	upsert("ps-multi", "ws-multi", wsMulti, state.StatusCompleted)
	upsert("ps-pool-empty", "ws-pool-empty", wsPoolEmpty, state.StatusCompleted)
	upsert("ps-pool-missing", "ws-pool-missing", wsPoolMissing, state.StatusCompleted)
	// Terminal job tombstone: no Workspace/Acpx payload. It must not affect
	// classification either way.
	if err := st.UpsertJob(state.Job{
		ID: "job-tombstone", Repo: repo, PublicSessionID: "ps-missing",
		Status: state.StatusCompleted, CreatedAt: lastUsed, FinishedAt: lastUsed,
	}); err != nil {
		t.Fatalf("upsert tombstone job: %v", err)
	}

	emptyInspector := func(context.Context, string, string) (PoolInspection, error) {
		return PoolInspection{ClonePresent: true, RegistryComplete: true}, nil
	}

	// Pre-seed sidecar ownership proof for the pruned session (case 3): it was
	// recorded managed while retained, then pruned from state and its workspace
	// removed.
	engine := f.newEngine(f.stateLoader(withSession(st, repo, "ps-proven", "ws-proven", wsProven)), emptyInspector)
	engine.PoolRemover = func(_ context.Context, _, poolRoot string) (PoolInspection, bool, error) {
		inspection := PoolInspection{ClonePresent: true, RegistryComplete: true}
		if err := os.RemoveAll(poolRoot); err != nil {
			return inspection, false, err
		}
		return inspection, true, nil
	}
	statePath := filepath.Join(t.TempDir(), "state.json")
	writeRawState(t, statePath, map[string]map[string]any{})
	engine.RawStatePath = statePath
	engine.RequireMigrationBackup = true
	if _, err := engine.Reconcile(context.Background(), ReconcileOptions{Apply: true, OrphanGrace: DefaultOrphanGrace}); err != nil {
		t.Fatalf("seed reconcile: %v", err)
	}
	if err := os.RemoveAll(wsProven); err != nil {
		t.Fatalf("remove proven workspace: %v", err)
	}

	// The remaining fixture directories appear only now.
	wsValid = f.mkWorkspace("ws-valid")
	runtimeValid := f.mkRuntimeDir(f.runtimeHash(repo, "ps-valid", wsValid))
	writeFile(t, filepath.Join(runtimeValid, "home", "keep.txt"), 128)
	runtimeMissing := f.mkRuntimeDir(f.runtimeHash(repo, "ps-missing", wsMissing))
	writeFile(t, filepath.Join(runtimeMissing, "home", "stale.txt"), 256)
	hashUnproven := strings.Repeat("aa", 16)
	runtimeUnproven := f.mkRuntimeDir(hashUnproven)
	writeFile(t, filepath.Join(runtimeUnproven, "xdg", "config"), 32)
	runtimeLegacyDeleted := f.mkRuntimeDir(f.runtimeHash(repo, "ps-legacy", wsLegacyDeleted))
	hashOrphan := strings.Repeat("bb", 16)
	runtimeOrphan := f.mkRuntimeDir(hashOrphan)
	wsMulti = f.mkWorkspace("ws-multi")
	runtimeMultiCurrent := f.mkRuntimeDir(f.runtimeHash(repo, "ps-multi", wsMulti))
	hashMultiLegacy := f.runtimeHash(repo, "ps-multi", filepath.Join(root, "old", "ws-multi"))
	runtimeMultiLegacy := f.mkRuntimeDir(hashMultiLegacy)
	wsPoolEmpty = f.mkWorkspace("ws-pool-empty")
	poolEmpty := f.mkPoolDir(f.poolHash(repo, "ps-pool-empty", wsPoolEmpty))
	poolMissing := f.mkPoolDir(f.poolHash(repo, "ps-pool-missing", wsPoolMissing))

	// First applied migration with the pruned session absent.
	engine.StateLoader = f.stateLoader(st)
	report, err := engine.Reconcile(context.Background(), ReconcileOptions{Apply: true, OrphanGrace: DefaultOrphanGrace})
	if err != nil {
		t.Fatalf("first apply: %v", err)
	}

	assertRemains := func(path string) {
		t.Helper()
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("%s must remain: %v", path, err)
		}
	}
	assertRemoved := func(path string) {
		t.Helper()
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("%s must be removed, err=%v", path, err)
		}
	}

	// Preserved: valid-workspace runtime, both hashes of the multi session
	// (current protected by workspace presence; legacy hash observed as orphan),
	// clone-missing pool, unproven orphan runtimes.
	assertRemains(runtimeValid)
	assertRemains(runtimeUnproven)
	assertRemains(runtimeOrphan)
	assertRemains(runtimeMultiCurrent)
	assertRemains(poolMissing)

	// Immediately reclaimed: retained terminal workspace-missing runtime,
	// sidecar-proven pruned runtime, legacy-deleted workspace runtime,
	// empty retired pool.
	assertRemoved(runtimeMissing)
	assertRemoved(runtimeProven)
	assertRemoved(runtimeLegacyDeleted)
	assertRemoved(poolEmpty)

	// The legacy hash of the retained multi session is observed, not deleted.
	multiLegacy := reportByID(t, report, ResourceID(ResourceKindSessionRuntime, "", "", hashMultiLegacy))
	if multiLegacy.Action != ActionObserved {
		t.Fatalf("multi legacy hash action=%q, want observed", multiLegacy.Action)
	}
	assertRemains(runtimeMultiLegacy)

	// Exact actions by class.
	if got := report.CountByClass(ClassRetiredKnown); got < 4 {
		t.Fatalf("retired_known count=%d, want >=4: %+v", got, report.Resources)
	}
	if report.ReclaimedBytes != 256+64 {
		t.Fatalf("reclaimed=%d, want %d", report.ReclaimedBytes, 256+64)
	}
	// First-migration backup exists.
	if _, err := os.Stat(filepath.Join(root, ".storage", "backups", "state-first.json")); err != nil {
		t.Fatalf("migration backup missing: %v", err)
	}

	// Idempotent: second apply changes nothing.
	second, err := engine.Reconcile(context.Background(), ReconcileOptions{Apply: true, OrphanGrace: DefaultOrphanGrace})
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	for _, r := range second.Resources {
		if r.Action == ActionDeleted || r.Action == ActionFailed {
			t.Fatalf("second apply mutated %q: %+v", r.ID, r)
		}
	}
	assertRemains(runtimeValid)
	assertRemains(runtimeUnproven)
}

func writeFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func withSession(st state.RunnerState, repo, sid, wsID, wsPath string) state.RunnerState {
	session := terminalSession(repo, sid, wsID, wsPath, time.Now().Add(-time.Hour))
	session.Status = state.StatusCompleted
	if err := st.UpsertPublicSession(session); err != nil {
		panic(err)
	}
	return st
}
