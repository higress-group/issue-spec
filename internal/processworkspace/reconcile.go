package processworkspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func (m *Manager) Reconcile(ctx context.Context, workspaceID string) (Inspection, error) {
	return m.withIntegrationLock(ctx, func() (Inspection, error) {
		return m.reconcileLocked(ctx, workspaceID)
	})
}

func (m *Manager) reconcileLocked(ctx context.Context, workspaceID string) (Inspection, error) {
	lease, found, err := m.Store.Get(ctx, workspaceID)
	if err != nil {
		return Inspection{}, err
	}
	if !found {
		return Inspection{}, fmt.Errorf("%s: %w", workspaceID, ErrLeaseNotFound)
	}
	switch lease.Portable.State {
	case StatePreparing:
		path, err := m.workspacePath(workspaceID)
		if err != nil {
			return Inspection{}, err
		}
		return m.reconcilePreparing(ctx, lease, path)
	case StateCleanupPending:
		return m.cleanupPending(ctx, lease)
	default:
		inspection, err := m.inspectLease(ctx, lease)
		if err != nil {
			return inspection, err
		}
		if lease.Portable.State == StateCleaned {
			if inspection.Registered || inspection.Present || len(inspection.Problems) > 0 {
				return inspection, fmt.Errorf("%w: cleaned lease still has Git or filesystem state", ErrWorkspaceConflict)
			}
			return inspection, nil
		}
		if !inspection.Registered || !inspection.Present {
			inspection.Problems = append(inspection.Problems, "active lease worktree is missing or unregistered")
			return m.persistReconcileConflict(ctx, inspection)
		}
		if len(inspection.Problems) > 0 {
			return m.persistReconcileConflict(ctx, inspection)
		}
		if inspection.Dirty {
			return inspection, fmt.Errorf("%w: %s", ErrWorkspaceDirty, lease.WorktreePath)
		}
		return inspection, nil
	}
}

func (m *Manager) Cleanup(ctx context.Context, workspaceID, ownerToken string) (Inspection, error) {
	return m.withIntegrationLock(ctx, func() (Inspection, error) {
		return m.cleanupLocked(ctx, workspaceID, ownerToken)
	})
}

func (m *Manager) cleanupLocked(ctx context.Context, workspaceID, ownerToken string) (Inspection, error) {
	lease, found, err := m.Store.Get(ctx, workspaceID)
	if err != nil {
		return Inspection{}, err
	}
	if !found {
		return Inspection{Lease: lease}, nil
	}
	if ownerToken == "" || ownerToken != lease.Owner.Token {
		return Inspection{}, errors.New("lease owner token mismatch")
	}
	if lease.Portable.State == StateCleaned {
		return Inspection{Lease: lease}, nil
	}
	if lease.Portable.State != StateCleanupPending {
		lease, err = m.Store.Update(ctx, workspaceID, func(current *LocalLease) error {
			current.Portable.State = StateCleanupPending
			return nil
		})
		if err != nil {
			return Inspection{}, err
		}
	}
	return m.cleanupPending(ctx, lease)
}

func (m *Manager) cleanupPending(ctx context.Context, lease LocalLease) (Inspection, error) {
	worktreePath := lease.WorktreePath
	if worktreePath == "" {
		var err error
		worktreePath, err = m.workspacePath(lease.Portable.WorkspaceID)
		if err != nil {
			return Inspection{Lease: lease}, err
		}
	}
	inspection, err := m.inspectLeaseAt(ctx, lease, worktreePath)
	if err != nil {
		return inspection, err
	}
	if inspection.Dirty {
		return inspection, fmt.Errorf("%w: %s", ErrWorkspaceDirty, worktreePath)
	}
	if len(inspection.Problems) > 0 {
		return inspection, fmt.Errorf("%w: %s", ErrWorkspaceConflict, strings.Join(inspection.Problems, "; "))
	}
	if inspection.Registered {
		if _, err := m.git(ctx, "remove owned process worktree", m.IntegrationRoot, "worktree", "remove", "--", worktreePath); err != nil {
			return inspection, err
		}
		inspection, err = m.inspectLeaseAt(ctx, lease, worktreePath)
		if err != nil {
			return inspection, err
		}
		if inspection.Registered || inspection.Present {
			return inspection, fmt.Errorf("%w: cleanup left worktree registered or present", ErrWorkspaceConflict)
		}
	} else if inspection.Present {
		return inspection, fmt.Errorf("%w: path exists but is not an owned registered worktree", ErrWorkspaceConflict)
	}
	if lease.Portable.Mode == ModeWritable {
		if err := m.removeWorkspaceMarker(ctx, lease); err != nil {
			return inspection, err
		}
	}
	return m.markCleaned(ctx, lease)
}

func (m *Manager) removeWorkspaceMarker(ctx context.Context, lease LocalLease) error {
	markerRef := workspaceMarkerRef(lease)
	markerSHA, exists, err := m.resolveOptionalRef(ctx, markerRef)
	if err != nil {
		return err
	}
	markers, err := m.workspaceMarkerRefs(ctx, lease.Portable.WorkspaceID)
	if err != nil {
		return err
	}
	if !exists {
		if len(markers) > 0 {
			return fmt.Errorf("%w: workspace has an unowned branch marker", ErrWorkspaceConflict)
		}
		return nil
	}
	if len(markers) != 1 || markers[0] != markerRef || !strings.EqualFold(markerSHA, lease.Portable.BaseSHA) {
		return fmt.Errorf("%w: workspace branch marker does not match the lease", ErrWorkspaceConflict)
	}
	_, err = m.git(ctx, "delete owned process branch marker", m.IntegrationRoot, "update-ref", "-d", markerRef, lease.Portable.BaseSHA)
	return err
}

func (m *Manager) markCleaned(ctx context.Context, lease LocalLease) (Inspection, error) {
	updated, err := m.Store.Update(ctx, lease.Portable.WorkspaceID, func(current *LocalLease) error {
		current.Portable.State = StateCleaned
		current.WorktreePath = ""
		current.Observation = WorktreeObservation{}
		return nil
	})
	return Inspection{Lease: updated}, err
}

func (m *Manager) inspectLease(ctx context.Context, lease LocalLease) (Inspection, error) {
	path := lease.WorktreePath
	if path == "" {
		path, _ = m.workspacePath(lease.Portable.WorkspaceID)
	}
	return m.inspectLeaseAt(ctx, lease, path)
}

func (m *Manager) inspectLeaseAt(ctx context.Context, lease LocalLease, worktreePath string) (Inspection, error) {
	inspection := Inspection{Lease: lease}
	path, err := m.validateWorkspacePath(worktreePath)
	if err != nil {
		return inspection, err
	}
	info, statErr := os.Lstat(path)
	if statErr == nil {
		inspection.Present = true
		if info.Mode()&os.ModeSymlink != 0 {
			inspection.Problems = append(inspection.Problems, "worktree path is a symlink")
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return inspection, statErr
	}
	worktrees, err := m.listWorktrees(ctx)
	if err != nil {
		return inspection, err
	}
	for _, candidate := range worktrees {
		candidatePath, canonicalErr := existingCanonicalDir(candidate.Path)
		if canonicalErr != nil {
			candidatePath = filepath.Clean(candidate.Path)
		}
		if candidatePath != path {
			continue
		}
		inspection.Registered = true
		inspection.Head = candidate.Head
		inspection.Branch = candidate.Branch
		if candidate.Bare || path == m.IntegrationRoot {
			inspection.Problems = append(inspection.Problems, "lease resolves to integration or bare worktree")
		}
		break
	}
	if !inspection.Registered {
		return inspection, nil
	}
	common, err := m.resolveCommonDir(ctx, path)
	if err != nil {
		inspection.Problems = append(inspection.Problems, "linked worktree common-dir cannot be resolved")
		return inspection, nil
	}
	if common != m.CommonDir {
		inspection.Problems = append(inspection.Problems, "linked worktree uses a different Git common-dir")
	}
	status, err := m.gitOutput(ctx, "inspect process worktree status", path, "status", "--porcelain=v1", "--untracked-files=normal")
	if err != nil {
		return inspection, err
	}
	inspection.Dirty = status != ""
	if lease.Portable.Mode == ModeSnapshot {
		if !candidateDetached(worktrees, path) || !strings.EqualFold(inspection.Head, lease.Portable.DetachedRevision) {
			inspection.Problems = append(inspection.Problems, "snapshot is not detached at the exact reserved revision")
		}
	} else if lease.Portable.Mode == ModeWritable {
		fullRef, _ := fullBranchRef(lease.Portable.Branch)
		if inspection.Branch != fullRef {
			inspection.Problems = append(inspection.Problems, "writable worktree branch differs from reserved branch")
		}
		markerRef := workspaceMarkerRef(lease)
		markerSHA, markerExists, markerErr := m.resolveOptionalRef(ctx, markerRef)
		if markerErr != nil {
			return inspection, markerErr
		}
		markers, markerErr := m.workspaceMarkerRefs(ctx, lease.Portable.WorkspaceID)
		if markerErr != nil {
			return inspection, markerErr
		}
		if !markerExists || len(markers) != 1 || markers[0] != markerRef || !strings.EqualFold(markerSHA, lease.Portable.BaseSHA) {
			inspection.Problems = append(inspection.Problems, "writable worktree lacks its exact lease ownership marker")
		}
		if expected := expectedWritableHead(lease.Portable); expected != "" && !strings.EqualFold(inspection.Head, expected) {
			inspection.Problems = append(inspection.Problems, fmt.Sprintf("writable worktree HEAD %s differs from expected %s for state %s", inspection.Head, expected, lease.Portable.State))
		}
	}
	return inspection, nil
}

func expectedWritableHead(lease PortableLease) string {
	switch lease.State {
	case StatePreparing, StatePrepared:
		return lease.BaseSHA
	case StateWorkerComplete, StateIntegrating, StateIntegrated:
		return lease.ResultCommit
	case StateCleanupPending, StateConflicted:
		if lease.ResultCommit != "" {
			return lease.ResultCommit
		}
		return lease.BaseSHA
	default:
		return ""
	}
}

func (m *Manager) persistReconcileConflict(ctx context.Context, inspection Inspection) (Inspection, error) {
	message := strings.Join(inspection.Problems, "; ")
	if inspection.Lease.Portable.State == StateConflicted || inspection.Lease.Portable.State == StateCleaned {
		return inspection, fmt.Errorf("%w: %s", ErrWorkspaceConflict, message)
	}
	updated, err := m.Store.Update(ctx, inspection.Lease.Portable.WorkspaceID, func(current *LocalLease) error {
		current.Portable.State = StateConflicted
		return nil
	})
	if err != nil {
		return inspection, errors.Join(fmt.Errorf("%w: %s", ErrWorkspaceConflict, message), err)
	}
	inspection.Lease = updated
	return inspection, fmt.Errorf("%w: %s", ErrWorkspaceConflict, message)
}

func (m *Manager) listWorktrees(ctx context.Context) ([]gitWorktree, error) {
	result, err := m.git(ctx, "list Git worktrees", m.IntegrationRoot, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	return parseWorktreePorcelain(result.Stdout)
}

func (m *Manager) validateWorkspacePath(candidate string) (string, error) {
	if candidate == "" || !filepath.IsAbs(candidate) {
		return "", fmt.Errorf("%w: worktree path must be absolute", ErrUnsafeWorkspace)
	}
	clean := filepath.Clean(candidate)
	if !pathWithin(m.WorkspaceRoot, clean) {
		return "", fmt.Errorf("%w: worktree path escapes managed root", ErrUnsafeWorkspace)
	}
	parent := filepath.Dir(clean)
	realParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", fmt.Errorf("%w: resolve worktree parent: %v", ErrUnsafeWorkspace, err)
	}
	if !pathWithin(m.WorkspaceRoot, realParent) && filepath.Clean(realParent) != m.WorkspaceRoot {
		return "", fmt.Errorf("%w: symlinked parent escapes managed root", ErrUnsafeWorkspace)
	}
	return clean, nil
}

func candidateDetached(worktrees []gitWorktree, path string) bool {
	for _, worktree := range worktrees {
		if filepath.Clean(worktree.Path) == filepath.Clean(path) {
			return worktree.Detached
		}
	}
	return false
}
