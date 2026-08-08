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

const (
	scratchJobActive = "job-aaaaaaaaaaaaaaaa"
	scratchJobDone   = "job-bbbbbbbbbbbbbbbb"
	scratchJobOrphan = "job-cccccccccccccccc"
	scratchJobDryRun = "job-dddddddddddddddd"
)

func newRuntimeService(t *testing.T, st state.RunnerState) (*Service, string) {
	t.Helper()
	return newRuntimeServiceWithLoader(t, func(context.Context) (state.RunnerState, error) { return st, nil })
}

func newRuntimeServiceWithLoader(t *testing.T, loader StateLoader) (*Service, string) {
	t.Helper()
	root := testRoot(t)
	svc, err := NewService(ServiceConfig{
		WorkspaceRoot: root,
		StateLoader:   loader,
		OrphanGrace:   DefaultOrphanGrace,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	return svc, root
}

func prepareScratchWithFile(t *testing.T, root, jobID string, size int) JobScratchPaths {
	t.Helper()
	paths, err := PrepareJobScratch(root, jobID)
	if err != nil {
		t.Fatalf("PrepareJobScratch: %v", err)
	}
	writeFile(t, filepath.Join(paths.Tmp, "payload"), size)
	return paths
}

func scratchRecordID(repo, jobID string) string {
	return ResourceID(ResourceKindJobScratch, repo, "", jobID)
}

func scratchRecord(t *testing.T, svc *Service, repo, jobID string) (PhysicalResource, bool) {
	t.Helper()
	record, ok := svc.Store().State().Resources[scratchRecordID(repo, jobID)]
	return record, ok
}

func contains(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func TestReconcileJobScratchKeepsActiveJob(t *testing.T) {
	st := state.NewState()
	st.Jobs[scratchJobActive] = state.Job{ID: scratchJobActive, Repo: "o/r", Status: state.StatusRunning}
	svc, root := newRuntimeService(t, st)
	paths := prepareScratchWithFile(t, root, scratchJobActive, 128)
	if err := svc.RecordJobScratch(context.Background(), "o/r", scratchJobActive, paths.Root); err != nil {
		t.Fatalf("RecordJobScratch: %v", err)
	}
	report, err := svc.ReconcileJobScratch(context.Background(), true)
	if err != nil {
		t.Fatalf("ReconcileJobScratch: %v", err)
	}
	if !contains(report.ScratchKept, scratchJobActive) {
		t.Fatalf("active job scratch must be kept: %+v", report)
	}
	if len(report.ScratchRemoved) != 0 {
		t.Fatalf("active job scratch must not be removed: %+v", report)
	}
	if _, err := os.Lstat(paths.Root); err != nil {
		t.Fatalf("active job scratch dir removed: %v", err)
	}
	record, ok := scratchRecord(t, svc, "o/r", scratchJobActive)
	if !ok || record.CleanupState != CleanupManaged {
		t.Fatalf("record = %+v ok=%v, want managed", record, ok)
	}
	// Interrupted jobs are still active for scratch purposes.
	st.Jobs[scratchJobActive] = state.Job{ID: scratchJobActive, Repo: "o/r", Status: state.StatusInterrupted}
	report, err = svc.ReconcileJobScratch(context.Background(), true)
	if err != nil {
		t.Fatalf("ReconcileJobScratch interrupted: %v", err)
	}
	if !contains(report.ScratchKept, scratchJobActive) {
		t.Fatalf("interrupted job scratch must be kept: %+v", report)
	}
}

func TestReconcileJobScratchRemovesTerminalAndUnknown(t *testing.T) {
	st := state.NewState()
	st.Jobs[scratchJobDone] = state.Job{ID: scratchJobDone, Repo: "o/r", Status: state.StatusCompleted}
	svc, root := newRuntimeService(t, st)
	donePaths := prepareScratchWithFile(t, root, scratchJobDone, 100)
	orphanPaths := prepareScratchWithFile(t, root, scratchJobOrphan, 50)
	for jobID, paths := range map[string]JobScratchPaths{scratchJobDone: donePaths, scratchJobOrphan: orphanPaths} {
		if err := svc.RecordJobScratch(context.Background(), "o/r", jobID, paths.Root); err != nil {
			t.Fatalf("RecordJobScratch %s: %v", jobID, err)
		}
	}
	report, err := svc.ReconcileJobScratch(context.Background(), true)
	if err != nil {
		t.Fatalf("ReconcileJobScratch: %v", err)
	}
	if !contains(report.ScratchRemoved, scratchJobDone) || !contains(report.ScratchRemoved, scratchJobOrphan) {
		t.Fatalf("terminal and unknown job scratch must be removed: %+v", report)
	}
	if report.ReclaimedBytes != 150 {
		t.Fatalf("reclaimed = %d, want 150", report.ReclaimedBytes)
	}
	for _, paths := range []JobScratchPaths{donePaths, orphanPaths} {
		if _, err := os.Lstat(paths.Root); !os.IsNotExist(err) {
			t.Fatalf("scratch dir %q must be gone, err=%v", paths.Root, err)
		}
	}
	for id, resource := range svc.Store().State().Resources {
		if resource.Kind == ResourceKindJobScratch {
			t.Fatalf("scratch records must be garbage-collected: %q = %+v", id, resource)
		}
	}
	// Second apply is a no-op.
	second, err := svc.ReconcileJobScratch(context.Background(), true)
	if err != nil {
		t.Fatalf("second ReconcileJobScratch: %v", err)
	}
	if len(second.ScratchRemoved) != 0 || second.ReclaimedBytes != 0 {
		t.Fatalf("second pass must be a no-op: %+v", second)
	}
}

func TestReconcileJobScratchRemovesUnrecordedOnDiskEntry(t *testing.T) {
	svc, root := newRuntimeService(t, state.NewState())
	// A crashed runner left scratch behind without a sidecar record.
	paths := prepareScratchWithFile(t, root, scratchJobOrphan, 64)
	report, err := svc.ReconcileJobScratch(context.Background(), true)
	if err != nil {
		t.Fatalf("ReconcileJobScratch: %v", err)
	}
	if !contains(report.ScratchRemoved, scratchJobOrphan) {
		t.Fatalf("unrecorded on-disk scratch must be removed: %+v", report)
	}
	if _, err := os.Lstat(paths.Root); !os.IsNotExist(err) {
		t.Fatalf("scratch dir must be gone, err=%v", err)
	}
}

func TestReconcileJobScratchRejectsForeignNames(t *testing.T) {
	svc, root := newRuntimeService(t, state.NewState())
	for _, name := range []string{"random-junk", "job-ZZZZZZZZZZZZZZZZ", "job-1234"} {
		if err := os.MkdirAll(filepath.Join(root, JobScratchDirName, name), 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	report, err := svc.ReconcileJobScratch(context.Background(), true)
	if err != nil {
		t.Fatalf("ReconcileJobScratch: %v", err)
	}
	for _, name := range []string{"random-junk", "job-ZZZZZZZZZZZZZZZZ", "job-1234"} {
		if !contains(report.ScratchRejected, name) {
			t.Fatalf("foreign entry %q must be rejected: %+v", name, report)
		}
		if _, err := os.Lstat(filepath.Join(root, JobScratchDirName, name)); err != nil {
			t.Fatalf("foreign entry %q must never be deleted: %v", name, err)
		}
	}
	if len(report.ScratchRemoved) != 0 {
		t.Fatalf("no scratch may be removed: %+v", report)
	}
}

func TestReconcileJobScratchDryRunDoesNotMutate(t *testing.T) {
	st := state.NewState()
	st.Jobs[scratchJobDryRun] = state.Job{ID: scratchJobDryRun, Repo: "o/r", Status: state.StatusFailed}
	svc, root := newRuntimeService(t, st)
	paths := prepareScratchWithFile(t, root, scratchJobDryRun, 32)
	if err := svc.RecordJobScratch(context.Background(), "o/r", scratchJobDryRun, paths.Root); err != nil {
		t.Fatalf("RecordJobScratch: %v", err)
	}
	report, err := svc.ReconcileJobScratch(context.Background(), false)
	if err != nil {
		t.Fatalf("ReconcileJobScratch dry-run: %v", err)
	}
	if len(report.ScratchRemoved) != 0 || report.ReclaimedBytes != 0 {
		t.Fatalf("dry-run must not report removals: %+v", report)
	}
	if !contains(report.Diagnostics, "would remove job scratch "+scratchJobDryRun) {
		t.Fatalf("dry-run must report the would-delete: %+v", report.Diagnostics)
	}
	if _, err := os.Lstat(paths.Root); err != nil {
		t.Fatalf("dry-run must not delete: %v", err)
	}
	record, ok := scratchRecord(t, svc, "o/r", scratchJobDryRun)
	if !ok || record.CleanupState != CleanupManaged {
		t.Fatalf("dry-run must not mutate records, record = %+v ok=%v", record, ok)
	}
}

// TestReconcileJobScratchRevalidatesBeforeDeletion simulates jobs turning
// active between the pass snapshot and the deletion, using the engine test's
// flip-loader pattern: the snapshot load classifies one recorded terminal job
// and one unrecorded unknown leftover as deletion-eligible, and every
// deletion-time reload sees them running again, so both scratch trees must
// survive.
func TestReconcileJobScratchRevalidatesBeforeDeletion(t *testing.T) {
	before := state.NewState()
	before.Jobs[scratchJobDone] = state.Job{ID: scratchJobDone, Repo: "o/r", Status: state.StatusCompleted}
	after := state.NewState()
	after.Jobs[scratchJobDone] = state.Job{ID: scratchJobDone, Repo: "o/r", Status: state.StatusRunning}
	after.Jobs[scratchJobOrphan] = state.Job{ID: scratchJobOrphan, Repo: "o/r", Status: state.StatusDispatched}
	svc, root := newRuntimeServiceWithLoader(t, flipLoader(before, after))
	donePaths := prepareScratchWithFile(t, root, scratchJobDone, 100)
	orphanPaths := prepareScratchWithFile(t, root, scratchJobOrphan, 50)
	if err := svc.RecordJobScratch(context.Background(), "o/r", scratchJobDone, donePaths.Root); err != nil {
		t.Fatalf("RecordJobScratch: %v", err)
	}
	// Simulate a crash-interrupted completion: the record is mid-lifecycle and
	// must be healed back to managed when the deletion aborts.
	if err := svc.Store().Update(func(st *StorageState) error {
		record := st.Resources[scratchRecordID("o/r", scratchJobDone)]
		record.CleanupState = CleanupDeleting
		st.Resources[scratchRecordID("o/r", scratchJobDone)] = record
		return nil
	}); err != nil {
		t.Fatalf("seed deleting state: %v", err)
	}
	// The orphan scratch is a crash-leftover directory with no sidecar record.
	report, err := svc.ReconcileJobScratch(context.Background(), true)
	if err != nil {
		t.Fatalf("ReconcileJobScratch: %v", err)
	}
	if len(report.ScratchRemoved) != 0 || report.ReclaimedBytes != 0 {
		t.Fatalf("revalidated deletions must be aborted: %+v", report)
	}
	if !contains(report.ScratchKept, scratchJobDone) || !contains(report.ScratchKept, scratchJobOrphan) {
		t.Fatalf("reactivated job scratch must be kept: %+v", report)
	}
	for _, paths := range []JobScratchPaths{donePaths, orphanPaths} {
		if _, err := os.Lstat(paths.Root); err != nil {
			t.Fatalf("scratch dir %q must survive the revalidation abort: %v", paths.Root, err)
		}
	}
	record, ok := scratchRecord(t, svc, "o/r", scratchJobDone)
	if !ok || record.CleanupState != CleanupManaged {
		t.Fatalf("record = %+v ok=%v, want healed to managed after the abort", record, ok)
	}
}

// TestReconcileJobScratchDeletionReloadFailureKeepsScratch proves the
// deletion-time reload fails safe: a state read error aborts every deletion
// with a bounded diagnostic instead of deleting on a stale snapshot.
func TestReconcileJobScratchDeletionReloadFailureKeepsScratch(t *testing.T) {
	calls := 0
	loader := func(context.Context) (state.RunnerState, error) {
		calls++
		if calls == 1 {
			return state.NewState(), nil
		}
		return state.RunnerState{}, errors.New("state store unavailable")
	}
	svc, root := newRuntimeServiceWithLoader(t, loader)
	orphanPaths := prepareScratchWithFile(t, root, scratchJobOrphan, 64)
	report, err := svc.ReconcileJobScratch(context.Background(), true)
	if err != nil {
		t.Fatalf("ReconcileJobScratch: %v", err)
	}
	if len(report.ScratchRemoved) != 0 || report.ReclaimedBytes != 0 {
		t.Fatalf("a reload failure must abort every deletion fail-safe: %+v", report)
	}
	reloadDiag := false
	for _, diagnostic := range report.Diagnostics {
		if strings.Contains(diagnostic, "deletion-time state reload") {
			reloadDiag = true
		}
	}
	if !reloadDiag {
		t.Fatalf("missing deletion-time reload diagnostic: %+v", report.Diagnostics)
	}
	if _, err := os.Lstat(orphanPaths.Root); err != nil {
		t.Fatalf("scratch must survive a deletion-time reload failure: %v", err)
	}
}

func TestCompleteJobScratch(t *testing.T) {
	svc, root := newRuntimeService(t, state.NewState())
	paths := prepareScratchWithFile(t, root, scratchJobDone, 16)
	if err := svc.RecordJobScratch(context.Background(), "o/r", scratchJobDone, paths.Root); err != nil {
		t.Fatalf("RecordJobScratch: %v", err)
	}
	if err := svc.CompleteJobScratch(context.Background(), "o/r", scratchJobDone); err != nil {
		t.Fatalf("CompleteJobScratch: %v", err)
	}
	if _, err := os.Lstat(paths.Root); !os.IsNotExist(err) {
		t.Fatalf("scratch dir must be gone, err=%v", err)
	}
	if _, ok := scratchRecord(t, svc, "o/r", scratchJobDone); ok {
		t.Fatalf("record must be garbage-collected")
	}
	// Idempotent and tolerant of unknown jobs.
	if err := svc.CompleteJobScratch(context.Background(), "o/r", scratchJobDone); err != nil {
		t.Fatalf("second CompleteJobScratch: %v", err)
	}
	if err := svc.CompleteJobScratch(context.Background(), "o/r", scratchJobOrphan); err != nil {
		t.Fatalf("unknown job must be a no-op: %v", err)
	}
	if err := svc.CompleteJobScratch(context.Background(), "o/r", "job-1"); err == nil {
		t.Fatalf("invalid job id must be rejected")
	}
}

func TestRecordJobScratchRejectsForeignPath(t *testing.T) {
	svc, root := newRuntimeService(t, state.NewState())
	if err := svc.RecordJobScratch(context.Background(), "o/r", scratchJobDone, filepath.Join(root, "elsewhere", scratchJobDone)); err == nil {
		t.Fatalf("recording a scratch path outside .job-scratch must fail closed")
	}
	if err := svc.RecordJobScratch(context.Background(), "o/r", "job-1", filepath.Join(root, JobScratchDirName, "job-1")); err == nil {
		t.Fatalf("invalid job id must be rejected")
	}
	if err := svc.RecordJobScratch(context.Background(), "", scratchJobDone, filepath.Join(root, JobScratchDirName, scratchJobDone)); err == nil {
		t.Fatalf("empty repo must be rejected")
	}
}

func TestRecordJobScratchUpsertIdempotent(t *testing.T) {
	svc, root := newRuntimeService(t, state.NewState())
	paths := prepareScratchWithFile(t, root, scratchJobDone, 8)
	if err := svc.RecordJobScratch(context.Background(), "o/r", scratchJobDone, paths.Root); err != nil {
		t.Fatalf("RecordJobScratch: %v", err)
	}
	first, ok := scratchRecord(t, svc, "o/r", scratchJobDone)
	if !ok {
		t.Fatalf("scratch record missing")
	}
	if first.Kind != ResourceKindJobScratch || first.Repo != "o/r" || first.PublicSessionID != "" ||
		first.PhysicalHash != scratchJobDone || first.Path != paths.Root || first.CleanupState != CleanupManaged {
		t.Fatalf("scratch record = %+v", first)
	}
	if first.FirstObservedAt.IsZero() {
		t.Fatalf("scratch record must carry first observation proof")
	}
	// A steady-state re-record preserves the observation proof and the record.
	if err := svc.RecordJobScratch(context.Background(), "o/r", scratchJobDone, paths.Root); err != nil {
		t.Fatalf("re-record: %v", err)
	}
	second, ok := scratchRecord(t, svc, "o/r", scratchJobDone)
	if !ok || second != first {
		t.Fatalf("idempotent re-record changed the record: first=%+v second=%+v", first, second)
	}
}

func TestEvictRuntimeCaches(t *testing.T) {
	svc, root := newRuntimeService(t, state.NewState())
	scope := testScope()
	paths, err := PrepareRuntimeHome(root, scope)
	if err != nil {
		t.Fatalf("PrepareRuntimeHome: %v", err)
	}
	if err := svc.RecordRuntimeHome(context.Background(), scope, paths); err != nil {
		t.Fatalf("RecordRuntimeHome: %v", err)
	}
	writeFile(t, filepath.Join(paths.Home, ".npm", "_npx", "pkg", "i.js"), 10)
	writeFile(t, filepath.Join(paths.Home, ".npm", "registry.tgz"), 20)
	writeFile(t, filepath.Join(paths.Home, ".cache", "go-build", "abc"), 30)
	writeFile(t, filepath.Join(paths.Home, "go", "pkg", "mod", "m.zip"), 40)
	protectedFiles := []string{
		filepath.Join(paths.Home, ".acpx", "sessions", "index.json"),
		filepath.Join(paths.Home, ".qoder", "settings.json"),
		filepath.Join(paths.Home, ".claude.json"),
		filepath.Join(paths.GHConfigDir, "hosts.yml"),
		filepath.Join(paths.CodexHome, "sessions", "s1.json"),
		filepath.Join(paths.Root, "scope.json"),
	}
	for _, path := range protectedFiles[:5] {
		writeFile(t, path, 64)
	}

	dry, err := svc.EvictRuntimeCaches(context.Background(), false)
	if err != nil {
		t.Fatalf("EvictRuntimeCaches dry-run: %v", err)
	}
	if len(dry.CacheEvicted) != 0 || dry.ReclaimedBytes != 0 {
		t.Fatalf("dry-run must not evict: %+v", dry)
	}
	for _, dir := range RuntimeCacheDirs(paths.Home) {
		if _, err := os.Lstat(dir); err != nil {
			t.Fatalf("dry-run removed %q: %v", dir, err)
		}
	}

	report, err := svc.EvictRuntimeCaches(context.Background(), true)
	if err != nil {
		t.Fatalf("EvictRuntimeCaches: %v", err)
	}
	if len(report.CacheEvicted) != 4 {
		t.Fatalf("evicted = %v, want 4 cache dirs", report.CacheEvicted)
	}
	if report.ReclaimedBytes != 100 {
		t.Fatalf("reclaimed = %d, want 100", report.ReclaimedBytes)
	}
	for _, dir := range RuntimeCacheDirs(paths.Home) {
		if _, err := os.Lstat(dir); !os.IsNotExist(err) {
			t.Fatalf("cache dir %q must be gone, err=%v", dir, err)
		}
	}
	for _, path := range protectedFiles {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("protected path %q must survive eviction: %v", path, err)
		}
	}
	// Second apply is a no-op.
	second, err := svc.EvictRuntimeCaches(context.Background(), true)
	if err != nil {
		t.Fatalf("second EvictRuntimeCaches: %v", err)
	}
	if len(second.CacheEvicted) != 0 || second.ReclaimedBytes != 0 {
		t.Fatalf("second eviction must be a no-op: %+v", second)
	}
}

// TestEvictRuntimeCachesRemovesReadOnlyGoModuleCache is the non-root-runner
// regression for the CI failure mode: the Go module cache materializes
// directories 0555 and files 0444, and unlink requires a writable parent, so
// a plain removal fails for the service user with permission denied.
// Eviction must relax the validated tree through the opened root capability
// and still reclaim every byte. As root the removal would succeed either
// way, so the relaxation pass itself is pinned euid-independently by
// TestMakeOpenedTreeWritableRelaxesReadOnlyTree.
func TestEvictRuntimeCachesRemovesReadOnlyGoModuleCache(t *testing.T) {
	svc, root := newRuntimeService(t, state.NewState())
	// If eviction regresses, the read-only fixture must not break t.TempDir
	// removal as a non-root user and mask the primary failure.
	t.Cleanup(func() { relaxTreeForCleanup(t, root) })
	scope := testScope()
	paths, err := PrepareRuntimeHome(root, scope)
	if err != nil {
		t.Fatalf("PrepareRuntimeHome: %v", err)
	}
	if err := svc.RecordRuntimeHome(context.Background(), scope, paths); err != nil {
		t.Fatalf("RecordRuntimeHome: %v", err)
	}
	modCache := filepath.Join(paths.Home, "go", "pkg", "mod")
	writeFile(t, filepath.Join(modCache, "example.com", "dep@v1.0.0", "dep.go"), 48)
	writeFile(t, filepath.Join(modCache, "cache", "download", "example.com", "dep", "@v", "v1.0.0.zip"), 16)
	// Leave the cache exactly the way the go tool does: read-only.
	makeTreeReadOnly(t, modCache)

	report, err := svc.EvictRuntimeCaches(context.Background(), true)
	if err != nil {
		t.Fatalf("EvictRuntimeCaches: %v", err)
	}
	if !contains(report.CacheEvicted, modCache) {
		t.Fatalf("read-only module cache must be evicted: %+v", report)
	}
	if report.ReclaimedBytes != 64 {
		t.Fatalf("reclaimed = %d, want 64", report.ReclaimedBytes)
	}
	if _, err := os.Lstat(modCache); !os.IsNotExist(err) {
		t.Fatalf("read-only module cache must be gone, err=%v", err)
	}
	if _, err := os.Lstat(filepath.Join(paths.Root, "scope.json")); err != nil {
		t.Fatalf("protected scope binding must survive eviction: %v", err)
	}
}

func TestEvictRuntimeCachesNeverFollowsSymlinks(t *testing.T) {
	svc, root := newRuntimeService(t, state.NewState())
	scope := testScope()
	paths, err := PrepareRuntimeHome(root, scope)
	if err != nil {
		t.Fatalf("PrepareRuntimeHome: %v", err)
	}
	if err := svc.RecordRuntimeHome(context.Background(), scope, paths); err != nil {
		t.Fatalf("RecordRuntimeHome: %v", err)
	}
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "payload"), 99)
	if err := os.Symlink(outside, filepath.Join(paths.Home, ".cache")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	report, err := svc.EvictRuntimeCaches(context.Background(), true)
	if err != nil {
		t.Fatalf("EvictRuntimeCaches: %v", err)
	}
	if contains(report.CacheEvicted, filepath.Join(paths.Home, ".cache")) {
		t.Fatalf("a symlinked cache dir must never be evicted: %+v", report)
	}
	if len(report.Diagnostics) == 0 {
		t.Fatalf("symlinked cache dir must produce a diagnostic")
	}
	info, err := os.Lstat(filepath.Join(paths.Home, ".cache"))
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink must remain, info=%v err=%v", info, err)
	}
	if _, err := os.Lstat(filepath.Join(outside, "payload")); err != nil {
		t.Fatalf("symlink target must be untouched: %v", err)
	}
}

func TestEvictRuntimeCachesRejectsForeignHomePath(t *testing.T) {
	svc, root := newRuntimeService(t, state.NewState())
	scope := testScope()
	paths, err := PrepareRuntimeHome(root, scope)
	if err != nil {
		t.Fatalf("PrepareRuntimeHome: %v", err)
	}
	if err := svc.RecordRuntimeHome(context.Background(), scope, paths); err != nil {
		t.Fatalf("RecordRuntimeHome: %v", err)
	}
	// Tamper: repoint the recorded home outside the root.
	hash, err := RuntimeScopeHash(scope)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	id := ResourceID(ResourceKindRunnerHome, scope.Repo, "", hash)
	if err := svc.Store().Update(func(st *StorageState) error {
		record := st.Resources[id]
		record.Path = filepath.Join(root, ".sessions", strings.Repeat("de", 16))
		st.Resources[id] = record
		return nil
	}); err != nil {
		t.Fatalf("tamper update: %v", err)
	}
	report, err := svc.EvictRuntimeCaches(context.Background(), true)
	if err != nil {
		t.Fatalf("EvictRuntimeCaches: %v", err)
	}
	if len(report.CacheEvicted) != 0 {
		t.Fatalf("a home outside the managed root must never be evicted: %+v", report)
	}
	if len(report.Diagnostics) == 0 {
		t.Fatalf("tampered home record must produce a diagnostic")
	}
}

// TestEvictRuntimeCachesSkipsHomesWithActiveJobs proves the in-use guard: a
// home whose repo has any active job keeps its caches (pressured eviction
// must not break in-flight builds), a home with terminal-only jobs is still
// evicted, and when every home is in use the pass reports the deferral.
func TestEvictRuntimeCachesSkipsHomesWithActiveJobs(t *testing.T) {
	current := state.NewState()
	current.Jobs[scratchJobActive] = state.Job{ID: scratchJobActive, Repo: "o/r", Status: state.StatusRunning}
	current.Jobs[scratchJobDone] = state.Job{ID: scratchJobDone, Repo: "o/r2", Status: state.StatusCompleted}
	svc, root := newRuntimeServiceWithLoader(t, func(context.Context) (state.RunnerState, error) { return current, nil })
	busyScope := testScope()
	idleScope := RuntimeScope{Hostname: "host-1", Repo: "o/r2", Runner: "runner-1"}
	homes := map[string]RuntimeHomePaths{}
	for _, scope := range []RuntimeScope{busyScope, idleScope} {
		paths, err := PrepareRuntimeHome(root, scope)
		if err != nil {
			t.Fatalf("PrepareRuntimeHome %s: %v", scope.Repo, err)
		}
		if err := svc.RecordRuntimeHome(context.Background(), scope, paths); err != nil {
			t.Fatalf("RecordRuntimeHome %s: %v", scope.Repo, err)
		}
		writeFile(t, filepath.Join(paths.Home, ".cache", "blob"), 40)
		homes[scope.Repo] = paths
	}

	report, err := svc.EvictRuntimeCaches(context.Background(), true)
	if err != nil {
		t.Fatalf("EvictRuntimeCaches: %v", err)
	}
	busyCache := filepath.Join(homes["o/r"].Home, ".cache")
	idleCache := filepath.Join(homes["o/r2"].Home, ".cache")
	if contains(report.CacheEvicted, busyCache) {
		t.Fatalf("in-use home caches must be preserved: %+v", report)
	}
	if !contains(report.CacheEvicted, idleCache) {
		t.Fatalf("terminal-only home caches must be evicted: %+v", report)
	}
	if _, err := os.Lstat(filepath.Join(busyCache, "blob")); err != nil {
		t.Fatalf("active repo cache must survive eviction: %v", err)
	}
	if _, err := os.Lstat(idleCache); !os.IsNotExist(err) {
		t.Fatalf("idle repo cache must be evicted, err=%v", err)
	}
	for _, diagnostic := range report.Diagnostics {
		if strings.Contains(diagnostic, "sessions are active") {
			t.Fatalf("partial skip must not claim full deferral: %+v", report.Diagnostics)
		}
	}

	// When every recorded home serves an active job the pass defers whole and
	// says so. Recreate the evicted idle cache so both homes have content.
	writeFile(t, filepath.Join(idleCache, "blob"), 40)
	current.Jobs[scratchJobDone] = state.Job{ID: scratchJobDone, Repo: "o/r2", Status: state.StatusRunning}
	deferred, err := svc.EvictRuntimeCaches(context.Background(), true)
	if err != nil {
		t.Fatalf("EvictRuntimeCaches all-active: %v", err)
	}
	if len(deferred.CacheEvicted) != 0 || deferred.ReclaimedBytes != 0 {
		t.Fatalf("all-active eviction must defer every home: %+v", deferred)
	}
	deferralDiag := false
	for _, diagnostic := range deferred.Diagnostics {
		if strings.Contains(diagnostic, "eviction deferred") && strings.Contains(diagnostic, "sessions are active") {
			deferralDiag = true
		}
	}
	if !deferralDiag {
		t.Fatalf("all-active eviction must report the deferral: %+v", deferred.Diagnostics)
	}
	for repo, paths := range homes {
		if _, err := os.Lstat(filepath.Join(paths.Home, ".cache", "blob")); err != nil {
			t.Fatalf("all-active eviction must preserve %s caches: %v", repo, err)
		}
	}
}

// TestEvictRuntimeCachesRefusesIntermediateSymlink redirects one home's go/
// subtree at another scope's cache through an intermediate symlink: the
// eviction target itself lstat's as a real directory, so only the
// confinement revalidation can catch the redirection. Eviction must refuse
// and the foreign tree must survive.
func TestEvictRuntimeCachesRefusesIntermediateSymlink(t *testing.T) {
	svc, root := newRuntimeService(t, state.NewState())
	scope := testScope()
	paths, err := PrepareRuntimeHome(root, scope)
	if err != nil {
		t.Fatalf("PrepareRuntimeHome: %v", err)
	}
	if err := svc.RecordRuntimeHome(context.Background(), scope, paths); err != nil {
		t.Fatalf("RecordRuntimeHome: %v", err)
	}
	// A second scope owns a real module cache below the same root. It is never
	// recorded, so the eviction pass only reaches it through the symlink.
	foreignScope := RuntimeScope{Hostname: "host-1", Repo: "o/r2", Runner: "runner-1"}
	foreignPaths, err := PrepareRuntimeHome(root, foreignScope)
	if err != nil {
		t.Fatalf("PrepareRuntimeHome foreign: %v", err)
	}
	foreignPayload := filepath.Join(foreignPaths.Home, "go", "pkg", "mod", "m.zip")
	writeFile(t, foreignPayload, 40)
	if err := os.Symlink(filepath.Join(foreignPaths.Home, "go"), filepath.Join(paths.Home, "go")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	report, err := svc.EvictRuntimeCaches(context.Background(), true)
	if err != nil {
		t.Fatalf("EvictRuntimeCaches: %v", err)
	}
	target := filepath.Join(paths.Home, "go", "pkg", "mod")
	if contains(report.CacheEvicted, target) {
		t.Fatalf("eviction must refuse an intermediate symlink redirection: %+v", report)
	}
	redirectionDiag := false
	for _, diagnostic := range report.Diagnostics {
		if strings.Contains(diagnostic, target) {
			redirectionDiag = true
		}
	}
	if !redirectionDiag {
		t.Fatalf("redirection must produce a diagnostic naming the target: %+v", report.Diagnostics)
	}
	info, err := os.Lstat(filepath.Join(paths.Home, "go"))
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the intermediate symlink itself must remain, info=%v err=%v", info, err)
	}
	if _, err := os.Lstat(foreignPayload); err != nil {
		t.Fatalf("the foreign scope cache must survive: %v", err)
	}
}
