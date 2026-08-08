package storage

import (
	"context"
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
	root := testRoot(t)
	svc, err := NewService(ServiceConfig{
		WorkspaceRoot: root,
		StateLoader:   func(context.Context) (state.RunnerState, error) { return st, nil },
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
