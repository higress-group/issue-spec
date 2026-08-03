package processworkspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/higress-group/issue-spec/internal/commentrunner/storage"
)

// InspectPool proves whether one session PROCESS pool root is safe to remove.
// It never mutates the pool, the registry, or Git state. Destructive callers
// must use RemoveEmptyPool, which holds the integration lifecycle lock across
// the proof and removal.
func InspectPool(ctx context.Context, integrationRoot, poolRoot string, options ManagerOptions) (storage.PoolInspection, error) {
	manager, absPool, inspection, err := openPoolManager(ctx, integrationRoot, poolRoot, options)
	if err != nil || manager == nil {
		return inspection, err
	}
	return inspectPoolLocked(ctx, manager, absPool)
}

// RemoveEmptyPool proves and removes a pool while holding the exact integration
// lock shared by Prepare/Reconcile/Cleanup. The empty proof therefore cannot be
// invalidated by a concurrently published PROCESS lease or worktree.
func RemoveEmptyPool(ctx context.Context, integrationRoot, poolRoot string, options ManagerOptions) (storage.PoolInspection, bool, error) {
	manager, absPool, inspection, err := openPoolManager(ctx, integrationRoot, poolRoot, options)
	if err != nil || manager == nil {
		return inspection, false, err
	}
	var lockedInspection storage.PoolInspection
	removed := false
	_, err = manager.withIntegrationLock(ctx, func() (Inspection, error) {
		var inspectErr error
		lockedInspection, inspectErr = inspectPoolLocked(ctx, manager, absPool)
		if inspectErr != nil || !lockedInspection.ProvenEmpty() {
			return Inspection{}, inspectErr
		}
		hash := filepath.Base(absPool)
		if err := storage.RemoveManagedTree(filepath.Dir(filepath.Dir(absPool)), absPool, hash, nil); err != nil {
			return Inspection{}, err
		}
		removed = true
		return Inspection{}, nil
	})
	if err != nil {
		return lockedInspection, false, err
	}
	return lockedInspection, removed, nil
}

func openPoolManager(ctx context.Context, integrationRoot, poolRoot string, options ManagerOptions) (*Manager, string, storage.PoolInspection, error) {
	var inspection storage.PoolInspection
	integrationRoot = strings.TrimSpace(integrationRoot)
	poolRoot = strings.TrimSpace(poolRoot)
	if integrationRoot == "" || poolRoot == "" {
		return nil, "", inspection, fmt.Errorf("integration root and pool root are required")
	}
	absPool, err := filepath.Abs(poolRoot)
	if err != nil {
		return nil, "", inspection, err
	}
	absPool = filepath.Clean(absPool)
	info, statErr := os.Stat(integrationRoot)
	if statErr != nil || !info.IsDir() {
		return nil, absPool, inspection, nil
	}
	manager, err := OpenManager(ctx, integrationRoot, absPool, options)
	if err != nil {
		inspection.Problems = append(inspection.Problems, "process workspace manager unavailable")
		return nil, absPool, inspection, nil
	}
	inspection.ClonePresent = true
	return manager, absPool, inspection, nil
}

func inspectPoolLocked(ctx context.Context, manager *Manager, absPool string) (storage.PoolInspection, error) {
	inspection := storage.PoolInspection{ClonePresent: true}
	if entries, readErr := os.ReadDir(absPool); readErr == nil {
		inspection.FilesystemEntries = len(entries)
	} else if !errors.Is(readErr, os.ErrNotExist) {
		inspection.Problems = append(inspection.Problems, "pool directory unreadable")
	}
	leases, complete := inspectPoolLeases(manager.CommonDir)
	inspection.RegistryComplete = complete
	foreign := map[string]bool{}
	for _, lease := range leases {
		if leaseWorktreeUnderPool(lease, absPool) {
			if lease.Portable.State != StateCleaned {
				inspection.ActiveLeases++
			}
			continue
		}
		foreign[lease.Portable.WorkspaceID] = true
	}
	markers, err := manager.listOwnershipMarkers(ctx)
	if err != nil {
		inspection.Problems = append(inspection.Problems, "ownership marker listing failed")
	} else {
		for _, marker := range markers {
			if !foreign[ownershipMarkerWorkspaceID(marker)] {
				inspection.OwnershipMarkers++
			}
		}
	}
	worktrees, err := manager.registeredWorktreePaths(ctx)
	if err != nil {
		inspection.Problems = append(inspection.Problems, "worktree listing failed")
	} else {
		for _, worktree := range worktrees {
			if pathWithin(absPool, worktree) {
				inspection.RegisteredWorktrees++
			}
		}
	}
	status, err := manager.gitOutput(ctx, "inspect integration status", manager.IntegrationRoot, "status", "--porcelain=v1", "--untracked-files=normal")
	if err != nil {
		inspection.Problems = append(inspection.Problems, "integration status inspection failed")
	} else if strings.TrimSpace(status) != "" {
		inspection.Problems = append(inspection.Problems, "integration root dirty")
	}
	return inspection, nil
}

// inspectPoolLeases reads the registry without creating it: a missing registry
// is complete with zero leases; a corrupt one is incomplete.
func inspectPoolLeases(commonDir string) ([]LocalLease, bool) {
	path := filepath.Join(commonDir, filepath.FromSlash(registryRelativePath))
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil, true
	}
	registry, err := loadRegistry(path)
	if err != nil {
		return nil, false
	}
	leases := make([]LocalLease, 0, len(registry.Leases))
	for _, lease := range registry.Leases {
		leases = append(leases, lease)
	}
	return leases, true
}

// leaseWorktreeUnderPool reports whether the lease's worktree lives inside the
// inspected pool. A lease without a recorded path cannot prove otherwise and
// is conservatively treated as belonging to this pool.
func leaseWorktreeUnderPool(lease LocalLease, absPool string) bool {
	path := strings.TrimSpace(lease.WorktreePath)
	if path == "" {
		return lease.Portable.State != StateCleaned
	}
	if canonical, err := filepath.EvalSymlinks(path); err == nil {
		path = filepath.Clean(canonical)
	} else {
		path = filepath.Clean(path)
	}
	return pathWithin(absPool, path)
}

func ownershipMarkerWorkspaceID(ref string) string {
	rest := strings.TrimPrefix(ref, "refs/issue-spec/process-workspaces/")
	id, _, _ := strings.Cut(rest, "/")
	return id
}

// listOwnershipMarkers enumerates every process-workspace ownership marker ref
// in the owning clone.
func (m *Manager) listOwnershipMarkers(ctx context.Context) ([]string, error) {
	output, err := m.gitOutput(ctx, "list all process workspace ownership markers", m.IntegrationRoot, "for-each-ref", "--format=%(refname)", "refs/issue-spec/process-workspaces/")
	if err != nil || strings.TrimSpace(output) == "" {
		return nil, err
	}
	return strings.Split(strings.TrimSpace(output), "\n"), nil
}

// registeredWorktreePaths lists canonical paths from `git worktree list`.
func (m *Manager) registeredWorktreePaths(ctx context.Context) ([]string, error) {
	output, err := m.gitOutput(ctx, "list registered worktrees", m.IntegrationRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, line := range strings.Split(output, "\n") {
		if !strings.HasPrefix(line, "worktree ") {
			continue
		}
		candidate := strings.TrimSpace(strings.TrimPrefix(line, "worktree "))
		if evaluated, err := filepath.EvalSymlinks(candidate); err == nil {
			candidate = filepath.Clean(evaluated)
		} else {
			candidate = filepath.Clean(candidate)
		}
		paths = append(paths, candidate)
	}
	return paths, nil
}
