package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/workspace"
)

// RuntimeReconcileReport summarizes one job-scratch reconciliation or cache
// eviction pass. Outcome lists record applied mutations only; a dry run
// reports its would-be actions through Diagnostics and never mutates.
type RuntimeReconcileReport struct {
	ScratchRemoved  []string `json:"scratch_removed,omitempty"`
	ScratchKept     []string `json:"scratch_kept,omitempty"`
	ScratchRejected []string `json:"scratch_rejected,omitempty"`
	CacheEvicted    []string `json:"cache_evicted,omitempty"`
	ReclaimedBytes  int64    `json:"reclaimed_bytes,omitempty"`
	Diagnostics     []string `json:"diagnostics,omitempty"`
}

// jobRuntimeActive mirrors buildProtectionView's active job statuses: an
// interrupted job still counts as active because its sandbox may be reaped
// asynchronously, so its scratch is never reclaimed underneath it and its
// scope's runtime home caches are never evicted mid-build.
func jobRuntimeActive(status state.LifecycleStatus) bool {
	switch status {
	case state.StatusQueued, state.StatusDispatched, state.StatusRunning, state.StatusInterrupted:
		return true
	default:
		return false
	}
}

// validateScratchDeletionTarget is the scratch counterpart of
// ValidateDeletionTarget: exact job identity, literal path below
// `.job-scratch`, confined to the canonical root, existing non-symlink
// directory. Missing targets validate so removals stay idempotent.
func validateScratchDeletionTarget(workspaceRoot, target, jobID string) error {
	if !jobScratchIDPattern.MatchString(jobID) {
		return fmt.Errorf("deletion target job id %q is invalid", jobID)
	}
	if strings.TrimSpace(target) == "" {
		return errors.New("deletion target is required")
	}
	clean := filepath.Clean(target)
	canonicalRoot, err := Canonicalize(workspaceRoot)
	if err != nil {
		return err
	}
	if clean == canonicalRoot {
		return fmt.Errorf("deletion target %q is the workspace root", target)
	}
	parent, base := filepath.Dir(clean), filepath.Base(clean)
	if base != jobID {
		return fmt.Errorf("deletion target %q does not match job id %q", target, jobID)
	}
	if parent != filepath.Join(canonicalRoot, JobScratchDirName) {
		return fmt.Errorf("deletion target %q is outside the job scratch directory", target)
	}
	confined, err := workspace.ValidatePathUnderRoot(canonicalRoot, clean)
	if err != nil {
		return err
	}
	if confined != clean {
		return fmt.Errorf("deletion target %q escapes its literal path", target)
	}
	info, err := os.Lstat(clean)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("deletion target %q is a symlink", target)
	}
	if !info.IsDir() {
		return fmt.Errorf("deletion target %q is not a directory", target)
	}
	return nil
}

// ReconcileJobScratch removes scratch of terminal or unknown jobs and keeps
// scratch of active jobs, both for recorded entries and for on-disk leftovers
// a crashed runner never recorded. Foreign names below `.job-scratch` are
// rejected and never deleted. Every deletion revalidates its job against
// freshly loaded runner state immediately before removal (the engine's D8
// deletion-time discipline): a job that turned active or newly known since
// the pass snapshot aborts its deletion. The pass is idempotent: a second
// apply is a no-op.
func (s *Service) ReconcileJobScratch(ctx context.Context, apply bool) (RuntimeReconcileReport, error) {
	_, release, err := EnsureOwner(ctx, s.root)
	if err != nil {
		return RuntimeReconcileReport{}, fmt.Errorf("job scratch reconcile owner: %w", err)
	}
	defer release()
	st, err := s.stateLoader(ctx)
	if err != nil {
		return RuntimeReconcileReport{}, fmt.Errorf("load runner state for job scratch reconciliation: %w", err)
	}
	st.Normalize()
	active := map[string]bool{}
	known := map[string]bool{}
	for _, job := range st.Jobs {
		known[job.ID] = true
		if jobRuntimeActive(job.Status) {
			active[job.ID] = true
		}
	}
	if err := s.store.Reload(); err != nil {
		return RuntimeReconcileReport{}, fmt.Errorf("reload storage sidecar: %w", err)
	}
	report := RuntimeReconcileReport{}
	mutate := apply
	if s.store.Status() == SidecarReportOnly {
		mutate = false
		report.Diagnostics = append(report.Diagnostics, "storage sidecar is report-only: foreign root identity or newer schema; inventory only, no mutations")
	}
	if cause := s.store.LoadCause(); cause != nil {
		report.Diagnostics = append(report.Diagnostics, safeDiagnostic("storage sidecar rebuilt after corruption ("+cause.Err.Error()+")"))
	}
	if !apply {
		report.Diagnostics = append(report.Diagnostics, "dry-run: no mutations performed")
	}

	recorded := map[string]PhysicalResource{}
	for id, resource := range s.store.State().Resources {
		if resource.Kind == ResourceKindJobScratch {
			recorded[id] = resource
		}
	}
	recordedIDs := make([]string, 0, len(recorded))
	for id := range recorded {
		recordedIDs = append(recordedIDs, id)
	}
	sort.Strings(recordedIDs)
	for _, id := range recordedIDs {
		record := recorded[id]
		jobID := record.PhysicalHash
		if active[jobID] {
			report.ScratchKept = append(report.ScratchKept, jobID)
			if mutate && record.CleanupState != CleanupManaged {
				s.healScratchRecord(id, jobID, &report)
			}
			continue
		}
		if _, statErr := os.Lstat(record.Path); errors.Is(statErr, os.ErrNotExist) {
			// Directory already gone: garbage-collect the record.
			if mutate {
				if err := s.store.Update(func(st *StorageState) error {
					delete(st.Resources, id)
					return nil
				}); err != nil {
					report.Diagnostics = append(report.Diagnostics, safeDiagnostic("job scratch "+jobID+" record GC failed: "+err.Error()))
				}
			}
			continue
		}
		if err := validateScratchDeletionTarget(s.root, record.Path, jobID); err != nil {
			report.Diagnostics = append(report.Diagnostics, safeDiagnostic("job scratch "+jobID+" record fails validation: "+err.Error()))
			continue
		}
		if !mutate {
			report.Diagnostics = append(report.Diagnostics, "would remove job scratch "+jobID)
			continue
		}
		abort, revalidateErr := s.revalidateScratchDeletion(ctx, jobID, known[jobID])
		if revalidateErr != nil {
			report.Diagnostics = append(report.Diagnostics, safeDiagnostic("job scratch "+jobID+" deletion skipped: "+revalidateErr.Error()))
			continue
		}
		if abort {
			// The job turned active or newly known after the pass snapshot:
			// keep its scratch and heal the record like the active path.
			report.ScratchKept = append(report.ScratchKept, jobID)
			if record.CleanupState != CleanupManaged {
				s.healScratchRecord(id, jobID, &report)
			}
			continue
		}
		if err := s.removeJobScratch(record, &report); err != nil {
			report.Diagnostics = append(report.Diagnostics, safeDiagnostic("job scratch "+jobID+" removal failed: "+err.Error()))
		}
	}

	// On-disk entries with no sidecar record.
	recordedJobs := map[string]bool{}
	for _, record := range recorded {
		recordedJobs[record.PhysicalHash] = true
	}
	names, err := readDirNames(filepath.Join(s.root, JobScratchDirName))
	if err == nil {
		for _, name := range names {
			if recordedJobs[name] {
				continue
			}
			if !jobScratchIDPattern.MatchString(name) {
				report.ScratchRejected = append(report.ScratchRejected, name)
				report.Diagnostics = append(report.Diagnostics, safeDiagnostic("job scratch entry "+name+" is not a scratch identity; rejected, never deleted"))
				continue
			}
			if active[name] {
				report.ScratchKept = append(report.ScratchKept, name)
				continue
			}
			path := filepath.Join(s.root, JobScratchDirName, name)
			if err := validateScratchDeletionTarget(s.root, path, name); err != nil {
				report.Diagnostics = append(report.Diagnostics, safeDiagnostic("job scratch entry "+name+" fails validation: "+err.Error()))
				continue
			}
			if !mutate {
				report.Diagnostics = append(report.Diagnostics, "would remove unrecorded job scratch "+name)
				continue
			}
			abort, revalidateErr := s.revalidateScratchDeletion(ctx, name, known[name])
			if revalidateErr != nil {
				report.Diagnostics = append(report.Diagnostics, safeDiagnostic("job scratch entry "+name+" deletion skipped: "+revalidateErr.Error()))
				continue
			}
			if abort {
				report.ScratchKept = append(report.ScratchKept, name)
				continue
			}
			measured := measureTreeBytes(path)
			if err := removeOpenedTree(path, nil); err != nil {
				report.Diagnostics = append(report.Diagnostics, safeDiagnostic("job scratch entry "+name+" removal failed: "+err.Error()))
				continue
			}
			report.ScratchRemoved = append(report.ScratchRemoved, name)
			report.ReclaimedBytes += measured
		}
	}
	sort.Strings(report.ScratchRemoved)
	sort.Strings(report.ScratchKept)
	sort.Strings(report.ScratchRejected)
	return report, nil
}

// revalidateScratchDeletion reloads runner state immediately before one
// scratch deletion, mirroring the engine's D8 deletion-time revalidation: a
// job that became active — or became known at all after the pass snapshot
// classified it unknown — aborts its deletion this pass. wasKnown reports
// whether the pass snapshot held the job in state. A reload failure aborts
// the deletion fail-safe: the scratch survives and is retried on a later
// pass.
func (s *Service) revalidateScratchDeletion(ctx context.Context, jobID string, wasKnown bool) (bool, error) {
	fresh, err := s.stateLoader(ctx)
	if err != nil {
		return true, fmt.Errorf("deletion-time state reload: %w", err)
	}
	fresh.Normalize()
	job, knownNow := fresh.Jobs[jobID]
	switch {
	case knownNow && jobRuntimeActive(job.Status):
		return true, nil
	case knownNow && !wasKnown:
		return true, nil
	default:
		return false, nil
	}
}

// revalidateHomeEviction reloads runner state immediately before a home's
// first cache removal, mirroring revalidateScratchDeletion's deletion-time
// discipline: a repo with any job that turned active since the pass snapshot
// aborts the home's eviction this pass. A reload failure aborts the eviction
// fail-safe: the caches survive and are retried on a later pass.
func (s *Service) revalidateHomeEviction(ctx context.Context, repo string) (bool, error) {
	fresh, err := s.stateLoader(ctx)
	if err != nil {
		return true, fmt.Errorf("deletion-time state reload: %w", err)
	}
	fresh.Normalize()
	repo = strings.TrimSpace(repo)
	for _, job := range fresh.Jobs {
		if jobRuntimeActive(job.Status) && strings.TrimSpace(job.Repo) == repo {
			return true, nil
		}
	}
	return false, nil
}

// healScratchRecord returns a live job's scratch record to managed state: a
// crash-interrupted completion raced a still-active job, so the record is
// healed instead of deleting live scratch.
func (s *Service) healScratchRecord(id, jobID string, report *RuntimeReconcileReport) {
	if err := s.store.Update(func(st *StorageState) error {
		current, ok := st.Resources[id]
		if ok {
			current.CleanupState = CleanupManaged
			st.Resources[id] = current
		}
		return nil
	}); err != nil {
		report.Diagnostics = append(report.Diagnostics, safeDiagnostic("job scratch "+jobID+" heal failed: "+err.Error()))
	}
}

// removeJobScratch runs the recorded-entry lifecycle: mark deleting, remove
// the directory capability scoped, then garbage-collect the record.
func (s *Service) removeJobScratch(record PhysicalResource, report *RuntimeReconcileReport) error {
	if err := s.store.Update(func(st *StorageState) error {
		current, ok := st.Resources[record.ID]
		if !ok {
			return nil
		}
		current.CleanupState = CleanupDeleting
		st.Resources[record.ID] = current
		return nil
	}); err != nil {
		return err
	}
	measured := measureTreeBytes(record.Path)
	if err := removeOpenedTree(record.Path, nil); err != nil {
		return err
	}
	if err := s.store.Update(func(st *StorageState) error {
		delete(st.Resources, record.ID)
		return nil
	}); err != nil {
		return err
	}
	report.ScratchRemoved = append(report.ScratchRemoved, record.PhysicalHash)
	report.ReclaimedBytes += measured
	return nil
}

// EvictRuntimeCaches removes only the rebuildable cache subtrees of every
// recorded runtime home, in eviction priority order. Protected identity and
// configuration paths are never deletion targets. A home whose scope has any
// active (queued/dispatched/running/interrupted) job is skipped whole:
// pressured eviction must not break in-flight builds. When every recorded
// home is skipped for that reason the pass reports the deferral so an
// operator (or the pressured-admission path) can tell eviction did run.
// Every home's first cache removal revalidates its repo against freshly
// loaded runner state, mirroring the scratch pass's deletion-time discipline:
// a repo that turned active since the pass snapshot — or a reload failure —
// skips the home fail-safe.
func (s *Service) EvictRuntimeCaches(ctx context.Context, apply bool) (RuntimeReconcileReport, error) {
	_, release, err := EnsureOwner(ctx, s.root)
	if err != nil {
		return RuntimeReconcileReport{}, fmt.Errorf("runtime cache eviction owner: %w", err)
	}
	defer release()
	st, err := s.stateLoader(ctx)
	if err != nil {
		return RuntimeReconcileReport{}, fmt.Errorf("load runner state for runtime cache eviction: %w", err)
	}
	st.Normalize()
	activeRepos := map[string]bool{}
	for _, job := range st.Jobs {
		if jobRuntimeActive(job.Status) {
			activeRepos[strings.TrimSpace(job.Repo)] = true
		}
	}
	if err := s.store.Reload(); err != nil {
		return RuntimeReconcileReport{}, fmt.Errorf("reload storage sidecar: %w", err)
	}
	report := RuntimeReconcileReport{}
	mutate := apply
	if s.store.Status() == SidecarReportOnly {
		mutate = false
		report.Diagnostics = append(report.Diagnostics, "storage sidecar is report-only: foreign root identity or newer schema; inventory only, no mutations")
	}
	homes := map[string]PhysicalResource{}
	for id, resource := range s.store.State().Resources {
		if resource.Kind == ResourceKindRunnerHome {
			homes[id] = resource
		}
	}
	ids := make([]string, 0, len(homes))
	for id := range homes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	deferredActive := 0
	for _, id := range ids {
		home := homes[id]
		if activeRepos[strings.TrimSpace(home.Repo)] {
			deferredActive++
			continue
		}
		if err := s.validateRuntimeHomeRecord(home); err != nil {
			report.Diagnostics = append(report.Diagnostics, safeDiagnostic("runtime home "+home.PhysicalHash+" record fails validation: "+err.Error()))
			continue
		}
		homeDir := RuntimeHomePathsFor(home.Path).Home
		homeRevalidated := false
		for _, cacheDir := range RuntimeCacheDirs(homeDir) {
			info, statErr := os.Lstat(cacheDir)
			if errors.Is(statErr, os.ErrNotExist) {
				continue
			}
			if statErr != nil {
				report.Diagnostics = append(report.Diagnostics, safeDiagnostic("runtime cache "+cacheDir+" lstat failed: "+statErr.Error()))
				continue
			}
			if err := validateCacheEvictionTarget(homeDir, cacheDir, info); err != nil {
				report.Diagnostics = append(report.Diagnostics, safeDiagnostic("runtime cache "+cacheDir+" fails validation: "+err.Error()))
				continue
			}
			measured := measureTreeBytes(cacheDir)
			if !mutate {
				report.Diagnostics = append(report.Diagnostics, fmt.Sprintf("would evict runtime cache %s (%d bytes)", cacheDir, measured))
				continue
			}
			if !homeRevalidated {
				// Deletion-time revalidation, once per home immediately before
				// its first cache removal (mirroring revalidateScratchDeletion):
				// a repo that turned active since the pass snapshot — or a
				// reload failure — skips the home fail-safe.
				homeRevalidated = true
				abort, revalidateErr := s.revalidateHomeEviction(ctx, home.Repo)
				if revalidateErr != nil {
					report.Diagnostics = append(report.Diagnostics, safeDiagnostic("runtime home "+home.PhysicalHash+" cache eviction skipped: "+revalidateErr.Error()))
					break
				}
				if abort {
					report.Diagnostics = append(report.Diagnostics, safeDiagnostic("runtime home "+home.PhysicalHash+" cache eviction skipped: repo "+strings.TrimSpace(home.Repo)+" turned active during the pass"))
					break
				}
			}
			if err := removeOpenedTree(cacheDir, nil); err != nil {
				report.Diagnostics = append(report.Diagnostics, safeDiagnostic("runtime cache "+cacheDir+" removal failed: "+err.Error()))
				continue
			}
			report.CacheEvicted = append(report.CacheEvicted, cacheDir)
			report.ReclaimedBytes += measured
		}
	}
	if deferredActive > 0 && deferredActive == len(ids) {
		report.Diagnostics = append(report.Diagnostics, safeDiagnostic(fmt.Sprintf("runtime cache eviction deferred: all %d recorded runner home(s) have active jobs; sessions are active", deferredActive)))
	}
	sort.Strings(report.CacheEvicted)
	return report, nil
}

// validateRuntimeHomeRecord proves a recorded home still names the canonical
// scoped path below this root before any of its subtrees are touched.
func (s *Service) validateRuntimeHomeRecord(home PhysicalResource) error {
	if !ValidHashName(home.PhysicalHash) {
		return fmt.Errorf("home hash %q is invalid", home.PhysicalHash)
	}
	expected := filepath.Join(s.root, RunnerHomesDirName, home.PhysicalHash)
	if filepath.Clean(home.Path) != expected {
		return fmt.Errorf("home path %q does not match the scoped home %q for this root", home.Path, expected)
	}
	info, err := os.Lstat(home.Path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("home path %q must be a non-symlink directory", home.Path)
	}
	return nil
}

// validateCacheEvictionTarget performs the final identity check on one cache
// directory: it must be one of the exact eviction-eligible paths of this
// home, confined below the literal home path, and a non-symlink directory.
func validateCacheEvictionTarget(home, target string, info os.FileInfo) error {
	clean := filepath.Clean(target)
	eligible := false
	for _, candidate := range RuntimeCacheDirs(home) {
		if candidate == clean {
			eligible = true
			break
		}
	}
	if !eligible {
		return fmt.Errorf("eviction target %q is not an eligible cache directory", target)
	}
	confined, err := workspace.ValidatePathUnderRoot(home, clean)
	if err != nil {
		return err
	}
	if confined != clean {
		return fmt.Errorf("eviction target %q escapes its literal path", target)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("eviction target %q is a symlink", target)
	}
	if !info.IsDir() {
		return fmt.Errorf("eviction target %q is not a directory", target)
	}
	return nil
}
