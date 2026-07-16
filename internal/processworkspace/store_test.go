package processworkspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStorePersistsLifecycleAtomicallyUnderGitCommonDir(t *testing.T) {
	commonDir := filepath.Join(t.TempDir(), ".git")
	now := time.Unix(5000, 0).UTC()
	store, err := OpenStore(commonDir, WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(commonDir, filepath.FromSlash(registryRelativePath))
	if store.RegistryPath() != wantPath || store.CommonDir() != filepath.Clean(commonDir) {
		t.Fatalf("store paths common=%q registry=%q", store.CommonDir(), store.RegistryPath())
	}
	lockBefore, err := os.Stat(store.LockPath())
	if err != nil {
		t.Fatal(err)
	}
	lease := LocalLease{
		Portable:        portableLease("ws-1", "PROCESS-001", "branch-1", []string{"internal/processworkspace/**"}, now),
		IntegrationRoot: filepath.Join(t.TempDir(), "integration"),
		Owner:           LeaseOwner{CoordinatorID: "coordinator", AgentSession: "session", Token: "owner-token", AcquiredAt: now},
	}
	created, err := store.Create(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	if created.LocalRevision != 1 || created.Portable.State != StatePreparing {
		t.Fatalf("created=%+v", created)
	}
	now = now.Add(time.Minute)
	worktree := filepath.Join(t.TempDir(), "worker")
	prepared, err := store.Update(context.Background(), "ws-1", func(lease *LocalLease) error {
		lease.WorktreePath = worktree
		lease.Portable.State = StatePrepared
		lease.Observation = WorktreeObservation{Registered: true, HeadSHA: baseSHA, Branch: "branch-1", InspectedAt: now}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.LocalRevision != 2 || prepared.WorktreePath != worktree || !prepared.Observation.Registered {
		t.Fatalf("prepared=%+v", prepared)
	}
	now = now.Add(time.Minute)
	workerComplete, err := store.Update(context.Background(), "ws-1", func(lease *LocalLease) error {
		lease.Portable.ResultCommit = resultSHA
		lease.Portable.State = StateWorkerComplete
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if workerComplete.Portable.IntegrationSHA != "" {
		t.Fatalf("worker completion prematurely integrated: %+v", workerComplete.Portable)
	}
	now = now.Add(time.Minute)
	_, err = store.Update(context.Background(), "ws-1", func(lease *LocalLease) error {
		lease.Portable.State = StateIntegrating
		lease.Integration = IntegrationState{ExpectedHead: baseSHA, StartedAt: now}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	integrated, err := store.Update(context.Background(), "ws-1", func(lease *LocalLease) error {
		lease.Portable.State = StateIntegrated
		lease.Portable.IntegrationSHA = integrationSHA
		lease.Integration.ObservedHead = integrationSHA
		lease.Integration.CompletedAt = now
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if integrated.Portable.IntegrationSHA != integrationSHA || integrated.LocalRevision != 5 {
		t.Fatalf("integrated=%+v", integrated)
	}
	loaded, found, err := store.Get(context.Background(), "ws-1")
	if err != nil || !found || loaded.Portable.State != StateIntegrated {
		t.Fatalf("loaded=%+v found=%v err=%v", loaded, found, err)
	}
	info, err := os.Stat(store.RegistryPath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("registry mode=%o", info.Mode().Perm())
	}
	lockAfter, err := os.Stat(store.LockPath())
	if err != nil || !os.SameFile(lockBefore, lockAfter) {
		t.Fatalf("registry lock inode changed: err=%v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(store.RegistryPath()))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".registry.json.tmp-") {
			t.Fatalf("atomic write left temp file %s", entry.Name())
		}
	}
}

func TestStoreSerializesConcurrentUpdatesInProcess(t *testing.T) {
	now := time.Unix(6000, 0).UTC()
	store, err := OpenStore(t.TempDir(), WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	lease := localPreparedLease(t, t.TempDir(), "ws-1", "PROCESS-001", "branch", "worker", []string{"internal/**"}, now)
	if _, err := store.Create(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	const updates = 32
	var wg sync.WaitGroup
	errorsSeen := make(chan error, updates)
	for index := 0; index < updates; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.Update(context.Background(), "ws-1", func(lease *LocalLease) error {
				lease.Observation.InspectedAt = now
				return nil
			})
			errorsSeen <- err
		}()
	}
	wg.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatal(err)
		}
	}
	loaded, found, err := store.Get(context.Background(), "ws-1")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if loaded.LocalRevision != updates+1 {
		t.Fatalf("local revision=%d want=%d", loaded.LocalRevision, updates+1)
	}
}

func TestStoreFileLockAndExplicitStaleRecovery(t *testing.T) {
	now := time.Unix(7000, 0).UTC()
	store, err := OpenStore(t.TempDir(), WithClock(func() time.Time { return now }), WithLockWait(0), withTokenSource(func() (string, error) { return "normal-token", nil }))
	if err != nil {
		t.Fatal(err)
	}
	held, err := store.acquireFileLock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	observed, found, err := store.InspectLock()
	if err != nil || !found || observed.Token != held.info.Token {
		t.Fatalf("observed=%+v found=%v err=%v", observed, found, err)
	}
	if _, err := store.Load(context.Background()); !errors.Is(err, ErrRegistryLocked) {
		t.Fatalf("expected OS file lock contention, got %v", err)
	}
	if err := store.RecoverStaleLock(context.Background(), held.info.Token, now); !errors.Is(err, ErrRegistryLocked) {
		t.Fatalf("live OS lock was recovered: %v", err)
	}
	if err := store.releaseFileLock(held); err != nil {
		t.Fatal(err)
	}

	stale := LockInfo{Held: true, Token: "stale-token", PID: 99, Hostname: "old-host", AcquiredAt: now.Add(-time.Hour), Registry: store.RegistryPath()}
	file, err := os.OpenFile(store.LockPath(), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeLockInfo(file, stale); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.RecoverStaleLock(context.Background(), "wrong-token", now); err == nil {
		t.Fatal("stale recovery ignored token mismatch")
	}
	if err := store.RecoverStaleLock(context.Background(), stale.Token, now.Add(-2*time.Hour)); err == nil {
		t.Fatal("stale recovery ignored cutoff")
	}
	if err := store.RecoverStaleLock(context.Background(), stale.Token, now); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.InspectLock(); err != nil || found {
		t.Fatalf("lock remained found=%v err=%v", found, err)
	}
	if _, err := store.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestStoreReopenGenerationCASAndTwoStoreCompetition(t *testing.T) {
	now := time.Unix(7500, 0).UTC()
	commonDir := t.TempDir()
	first, err := OpenStore(commonDir, WithClock(func() time.Time { return now }), WithLockWait(0))
	if err != nil {
		t.Fatal(err)
	}
	second, err := OpenStore(commonDir, WithClock(func() time.Time { return now }), WithLockWait(0))
	if err != nil {
		t.Fatal(err)
	}
	lease := localPreparedLease(t, t.TempDir(), "ws-1", "PROCESS-001", "branch", "worker", []string{"internal/**"}, now)
	if _, err := first.CreateAtGeneration(context.Background(), 0, lease); err != nil {
		t.Fatal(err)
	}
	snapshot, err := second.Load(context.Background())
	if err != nil || snapshot.Generation != 1 {
		t.Fatalf("generation=%d err=%v", snapshot.Generation, err)
	}
	if _, err := first.UpdateAtGeneration(context.Background(), snapshot.Generation, "ws-1", func(lease *LocalLease) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := second.UpdateAtGeneration(context.Background(), snapshot.Generation, "ws-1", func(lease *LocalLease) error { return nil }); !errors.Is(err, ErrGenerationConflict) {
		t.Fatalf("stale generation err=%v", err)
	}
	held, err := first.acquireFileLock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	lockBefore, err := os.Stat(first.LockPath())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(commonDir, WithLockWait(0)); !errors.Is(err, ErrRegistryLocked) {
		t.Fatalf("OpenStore modified metadata without acquiring the stable lock: %v", err)
	}
	lockAfter, err := os.Stat(first.LockPath())
	if err != nil || !os.SameFile(lockBefore, lockAfter) {
		t.Fatalf("stable lock inode changed: err=%v", err)
	}
	if _, err := second.Load(context.Background()); !errors.Is(err, ErrRegistryLocked) {
		t.Fatalf("second store ignored held file lock: %v", err)
	}
	if err := first.releaseFileLock(held); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(commonDir)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := reopened.Load(context.Background())
	if err != nil || registry.Generation != 2 || len(registry.Leases) != 1 {
		t.Fatalf("reopened=%+v err=%v", registry, err)
	}
}

func TestReleaseFileLockAlwaysUnlocksAndClosesOnMetadataErrors(t *testing.T) {
	store, err := OpenStore(t.TempDir(), WithLockWait(0))
	if err != nil {
		t.Fatal(err)
	}
	for _, corrupt := range []func(*os.File) error{
		func(file *os.File) error {
			if err := file.Truncate(0); err != nil {
				return err
			}
			if _, err := file.Seek(0, 0); err != nil {
				return err
			}
			_, err := file.WriteString("{broken\n")
			return err
		},
		func(file *os.File) error {
			return writeLockInfo(file, LockInfo{Held: true, Token: "other-owner", Registry: store.RegistryPath()})
		},
	} {
		held, err := store.acquireFileLock(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if err := corrupt(held.file); err != nil {
			t.Fatal(err)
		}
		if err := store.releaseFileLock(held); err == nil {
			t.Fatal("release accepted corrupt or mismatched lock metadata")
		}
		if _, err := store.Load(context.Background()); err != nil {
			t.Fatalf("release error leaked the OS lock: %v", err)
		}
	}
}

func TestReservationOnlyLeaseCanBeCleanedWithoutWorktreePath(t *testing.T) {
	now := time.Unix(7800, 0).UTC()
	store, err := OpenStore(t.TempDir(), WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	lease := LocalLease{
		Portable:        portableLease("ws-reserved", "PROCESS-001", "branch-reserved", []string{"internal/**"}, now),
		IntegrationRoot: filepath.Clean(t.TempDir()),
		Owner:           LeaseOwner{CoordinatorID: "coordinator", Token: "owner-token", AcquiredAt: now},
	}
	if _, err := store.Create(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	for _, state := range []LifecycleState{StateCleanupPending, StateCleaned} {
		now = now.Add(time.Minute)
		cleaned, err := store.Update(context.Background(), lease.Portable.WorkspaceID, func(current *LocalLease) error {
			current.Portable.State = state
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if cleaned.WorktreePath != "" || cleaned.Portable.State != state {
			t.Fatalf("reservation cleanup=%+v", cleaned)
		}
	}
}

func TestAssignedWorktreePathIsRetainedUntilCleanedTombstone(t *testing.T) {
	now := time.Unix(7900, 0).UTC()
	store, err := OpenStore(t.TempDir(), WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	lease := localPreparedLease(t, t.TempDir(), "ws-assigned", "PROCESS-001", "branch-assigned", "worker", []string{"internal/**"}, now)
	lease.Observation = WorktreeObservation{Registered: true, HeadSHA: baseSHA, Branch: lease.Portable.Branch, InspectedAt: now}
	if _, err := store.Create(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(context.Background(), lease.Portable.WorkspaceID, func(current *LocalLease) error {
		current.Portable.State = StateCleanupPending
		current.WorktreePath = ""
		return nil
	}); err == nil {
		t.Fatal("prepared registered lease cleared its assigned path before cleaned tombstone")
	}
	loaded, found, err := store.Get(context.Background(), lease.Portable.WorkspaceID)
	if err != nil || !found || loaded.Portable.State != StatePrepared || loaded.WorktreePath != lease.WorktreePath {
		t.Fatalf("failed update changed lease: loaded=%+v found=%v err=%v", loaded, found, err)
	}
}

func TestStoreUpdateFailureAndCorruptRegistryAreFailClosed(t *testing.T) {
	now := time.Unix(8000, 0).UTC()
	store, err := OpenStore(t.TempDir(), WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	lease := localPreparedLease(t, t.TempDir(), "ws-1", "PROCESS-001", "branch", "worker", []string{"internal/**"}, now)
	if _, err := store.Create(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("stop update")
	if _, err := store.Update(context.Background(), "ws-1", func(lease *LocalLease) error {
		lease.Portable.State = StateWorkerComplete
		lease.Portable.ResultCommit = resultSHA
		return wantErr
	}); !errors.Is(err, wantErr) {
		t.Fatalf("err=%v", err)
	}
	loaded, _, err := store.Get(context.Background(), "ws-1")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Portable.State != StatePrepared || loaded.LocalRevision != 1 {
		t.Fatalf("failed callback persisted mutation: %+v", loaded)
	}
	for _, data := range [][]byte{nil, []byte("{broken"), []byte(`{"schema_version":99,"leases":{}}`)} {
		if err := os.WriteFile(store.RegistryPath(), data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Load(context.Background()); !errors.Is(err, ErrRegistryCorrupt) {
			t.Fatalf("expected corrupt registry error for %q, got %v", data, err)
		}
	}
}

func TestStoreProcessGateHonorsContext(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	release, err := store.acquireProcessGate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := store.Load(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("process gate ignored context: %v", err)
	}
}

func TestStoreRejectsCollisionsAndPurgesOnlyOwnedCleanedLease(t *testing.T) {
	now := time.Unix(9000, 0).UTC()
	store, err := OpenStore(t.TempDir(), WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	first := localPreparedLease(t, root, "ws-1", "PROCESS-001", "branch-1", "worker-1", []string{"internal/a/**"}, now)
	if _, err := store.Create(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := localPreparedLease(t, root, "ws-2", "PROCESS-002", "branch-2", "worker-2", []string{"internal/a/file.go"}, now)
	if _, err := store.Create(context.Background(), second); err == nil {
		t.Fatal("store accepted active ownership collision")
	}
	if _, err := store.Create(context.Background(), first); !errors.Is(err, ErrLeaseExists) {
		t.Fatalf("duplicate create err=%v", err)
	}
	if err := store.Purge(context.Background(), "ws-1", first.Owner.Token); err == nil {
		t.Fatal("purged active lease")
	}
	for _, state := range []LifecycleState{StateCleanupPending, StateCleaned} {
		now = now.Add(time.Minute)
		if _, err := store.Update(context.Background(), "ws-1", func(lease *LocalLease) error {
			lease.Portable.State = state
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Purge(context.Background(), "ws-1", "wrong"); err == nil {
		t.Fatal("purged cleaned lease with wrong owner")
	}
	if err := store.Purge(context.Background(), "ws-1", first.Owner.Token); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Get(context.Background(), "ws-1"); err != nil || found {
		t.Fatalf("purged lease found=%v err=%v", found, err)
	}
}
