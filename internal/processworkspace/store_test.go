package processworkspace

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/assignment"
)

func assignmentForLease(lease LocalLease, id, objective string) assignment.Assignment {
	return assignment.Assignment{SchemaVersion: assignment.AssignmentSchemaVersion, ID: id, Role: assignment.RoleImplementation,
		Repository: lease.Portable.Repository, Issue: 297, ProcessID: lease.Portable.ProcessID, BaseRevision: lease.Portable.BaseSHA,
		Scenarios:     []assignment.ScenarioRef{{SpecID: "SPEC-001", Scenario: "worker receives packet"}},
		DesignContext: assignmentDesignContext(),
		Policy:        assignment.Policy{RequireExactRevision: true, MaxResultItems: 64}, ResultSchemaVersion: assignment.ReceiptSchemaVersion,
		Implementation: &assignment.ImplementationPayload{Objective: objective, Branch: lease.Portable.Branch,
			WriteOwnership: append([]string(nil), lease.Portable.WriteOwnership...), SharedTouchpoints: append([]string(nil), lease.Portable.SharedTouchpoints...),
			Commit: assignment.CommitPolicy{RequireSingleCommit: true, RequireDCO: true}}}
}

func TestStoreLoadsAndCleansPreD14RegistryWithoutReissuingAssignment(t *testing.T) {
	now := time.Unix(4400, 0).UTC()
	common := t.TempDir()
	store, err := OpenStore(common, WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	lease := localPreparedLease(t, t.TempDir(), "ws-pre-d14", "PROCESS-005", "legacy", "tree", []string{"internal/processworkspace/**"}, now)
	legacy := assignmentForLease(lease, "ws-pre-d14-assignment-1", "historical assignment")
	legacy.DesignContext = nil
	digest, err := assignment.AssignmentDigestForStorageRead(legacy)
	if err != nil {
		t.Fatal(err)
	}
	lease.Portable.Assignment = &AssignmentBinding{SchemaVersion: legacy.SchemaVersion, AssignmentID: legacy.ID, Digest: digest,
		Role: legacy.Role, BaseRevision: legacy.BaseRevision, Generation: 1}
	lease.Assignment = &legacy
	registry := NewRegistry()
	registry.Generation, registry.UpdatedAt = 1, now
	registry.Leases[lease.Portable.WorkspaceID] = lease
	payload, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.RegistryPath(), append(payload, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	loaded, found, err := store.Get(context.Background(), lease.Portable.WorkspaceID)
	if err != nil || !found || loaded.Assignment == nil || loaded.Assignment.DesignContext != nil {
		t.Fatalf("legacy registry load found=%t lease=%+v err=%v", found, loaded, err)
	}
	if _, err := store.BindAssignment(context.Background(), lease.Portable.WorkspaceID, legacy, false, nil); err == nil || !strings.Contains(err.Error(), "design_context") {
		t.Fatalf("legacy assignment was reissued: %v", err)
	}
	expectedGeneration := uint64(1)
	if _, err := store.BindAssignment(context.Background(), lease.Portable.WorkspaceID, legacy, true, &expectedGeneration); err == nil || !strings.Contains(err.Error(), "design_context") {
		t.Fatalf("legacy assignment was redispatched: %v", err)
	}
	pending, err := store.Update(context.Background(), lease.Portable.WorkspaceID, func(current *LocalLease) error {
		current.Portable.State = StateCleanupPending
		return nil
	})
	if err != nil || pending.Portable.State != StateCleanupPending {
		t.Fatalf("legacy cleanup-pending lease=%+v err=%v", pending, err)
	}
	cleaned, err := store.Update(context.Background(), lease.Portable.WorkspaceID, func(current *LocalLease) error {
		current.Portable.State = StateCleaned
		return nil
	})
	if err != nil || cleaned.Portable.State != StateCleaned {
		t.Fatalf("legacy cleaned lease=%+v err=%v", cleaned, err)
	}
}

func TestStoreAssignmentIssuanceRetryRedispatchAndRecovery(t *testing.T) {
	now := time.Unix(4500, 0).UTC()
	common := t.TempDir()
	store, err := OpenStore(common, WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	lease := localPreparedLease(t, t.TempDir(), "ws-assignment", "PROCESS-006", "worker", "tree", []string{"internal/processworkspace/**"}, now)
	if _, err := store.Create(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	firstValue := assignmentForLease(lease, "ws-assignment-1", "issue packet")
	first, err := store.BindAssignment(context.Background(), lease.Portable.WorkspaceID, firstValue, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.Portable.Assignment == nil || first.Portable.Assignment.Generation != 1 || first.Assignment == nil {
		t.Fatalf("first binding=%+v", first)
	}
	registry, _ := store.Load(context.Background())
	revision, generation := first.LocalRevision, registry.Generation

	reopened, err := OpenStore(common, WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	retry, err := reopened.BindAssignment(context.Background(), lease.Portable.WorkspaceID, firstValue, false, nil)
	if err != nil || retry.LocalRevision != revision {
		t.Fatalf("retry changed binding: lease=%+v err=%v", retry, err)
	}
	registry, _ = reopened.Load(context.Background())
	if registry.Generation != generation {
		t.Fatalf("retry advanced registry generation %d -> %d", generation, registry.Generation)
	}

	conflict := assignmentForLease(lease, "ws-assignment-conflict", "different")
	if _, err := reopened.BindAssignment(context.Background(), lease.Portable.WorkspaceID, conflict, false, nil); err == nil || !strings.Contains(err.Error(), "explicit redispatch") {
		t.Fatalf("normal retry accepted conflict: %v", err)
	}
	wrong := uint64(2)
	if _, err := reopened.BindAssignment(context.Background(), lease.Portable.WorkspaceID, conflict, true, &wrong); err == nil || !strings.Contains(err.Error(), "generation conflict") {
		t.Fatalf("redispatch ignored expected generation: %v", err)
	}
	expected := uint64(1)
	second, err := reopened.BindAssignment(context.Background(), lease.Portable.WorkspaceID, conflict, true, &expected)
	if err != nil || second.Portable.Assignment.Generation != 2 {
		t.Fatalf("redispatch=%+v err=%v", second, err)
	}
	if _, err := reopened.Update(context.Background(), lease.Portable.WorkspaceID, func(current *LocalLease) error {
		current.AcceptedReceiptID = "receipt-accepted"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	third := assignmentForLease(lease, "ws-assignment-3", "third")
	expected = 2
	if _, err := reopened.BindAssignment(context.Background(), lease.Portable.WorkspaceID, third, true, &expected); err == nil || !strings.Contains(err.Error(), "receipt acceptance") {
		t.Fatalf("redispatch allowed after receipt acceptance: %v", err)
	}

	legacy := localPreparedLease(t, t.TempDir(), "ws-remote-only", "PROCESS-007", "review", "tree", []string{"internal/model/**"}, now)
	legacyValue := assignmentForLease(legacy, "ws-remote-only-1", "recover")
	digest, err := assignment.AssignmentDigest(legacyValue)
	if err != nil {
		t.Fatal(err)
	}
	legacy.Portable.Assignment = &AssignmentBinding{SchemaVersion: legacyValue.SchemaVersion, AssignmentID: legacyValue.ID, Digest: digest,
		Role: legacyValue.Role, BaseRevision: legacyValue.BaseRevision, Generation: 1}
	if _, err := reopened.Create(context.Background(), legacy); err != nil {
		t.Fatal(err)
	}
	recovered, err := reopened.BindAssignment(context.Background(), legacy.Portable.WorkspaceID, legacyValue, false, nil)
	if err != nil || recovered.Assignment == nil || recovered.Portable.Assignment.Generation != 1 {
		t.Fatalf("remote-only recovery=%+v err=%v", recovered, err)
	}
}

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
