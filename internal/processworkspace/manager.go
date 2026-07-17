package processworkspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var (
	ErrWorkspaceConflict = errors.New("process workspace conflicts with Git or registry truth")
	ErrWorkspaceDirty    = errors.New("process workspace is dirty")
	ErrUnsafeWorkspace   = errors.New("unsafe process workspace path")
	ErrIntegrationLocked = errors.New("process workspace integration coordination locked")
)

type Manager struct {
	IntegrationRoot string
	WorkspaceRoot   string
	CommonDir       string
	GitBinary       string
	Runner          GitRunner
	Store           *Store
	Now             func() time.Time
	// IntegrationLockPath is a stable, never-unlinked advisory coordination
	// point. Future integration mutators must share this lock around Git
	// validation/mutation and lease-state publication.
	IntegrationLockPath string
	integrationGate     *processGate
	integrationLockWait time.Duration
	integrationLockPoll time.Duration
}

type ManagerOptions struct {
	GitBinary    string
	Runner       GitRunner
	Now          func() time.Time
	StoreOptions []StoreOption
}

type PrepareRequest struct {
	Lease              LocalLease
	ExpectedGeneration *uint64
}

type Inspection struct {
	Lease      LocalLease `json:"lease"`
	Registered bool       `json:"registered"`
	Present    bool       `json:"present"`
	Dirty      bool       `json:"dirty"`
	Head       string     `json:"head,omitempty"`
	Branch     string     `json:"branch,omitempty"`
	Problems   []string   `json:"problems,omitempty"`
}

func OpenManager(ctx context.Context, integrationRoot, workspaceRoot string, options ManagerOptions) (*Manager, error) {
	runner := options.Runner
	if runner == nil {
		runner = ExecGitRunner{}
	}
	root, err := existingCanonicalDir(integrationRoot)
	if err != nil {
		return nil, fmt.Errorf("integration root: %w", err)
	}
	manager := &Manager{IntegrationRoot: root, GitBinary: options.GitBinary, Runner: runner, Now: options.Now}
	if manager.Now == nil {
		manager.Now = time.Now
	}
	top, err := manager.gitOutput(ctx, "resolve integration top-level", root, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, err
	}
	top, err = existingCanonicalDir(top)
	if err != nil || top != root {
		return nil, fmt.Errorf("integration root %q is not the Git top-level %q", root, top)
	}
	common, err := manager.resolveCommonDir(ctx, root)
	if err != nil {
		return nil, err
	}
	manager.CommonDir = common
	if workspaceRoot == "" {
		workspaceRoot = filepath.Join(filepath.Dir(root), ".issue-spec-worktrees")
	}
	absWorkspaceRoot, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return nil, err
	}
	if filepath.Clean(absWorkspaceRoot) == root || pathWithin(root, absWorkspaceRoot) {
		return nil, fmt.Errorf("%w: workspace root must not be inside the integration checkout", ErrUnsafeWorkspace)
	}
	workspaceRoot, err = prepareCanonicalRoot(workspaceRoot)
	if err != nil {
		return nil, err
	}
	if workspaceRoot == root || pathWithin(root, workspaceRoot) {
		return nil, fmt.Errorf("%w: workspace root must not be inside the integration checkout", ErrUnsafeWorkspace)
	}
	manager.WorkspaceRoot = workspaceRoot
	store, err := OpenStore(common, options.StoreOptions...)
	if err != nil {
		return nil, err
	}
	manager.Store = store
	manager.IntegrationLockPath = filepath.Join(filepath.Dir(store.RegistryPath()), "integration.lock")
	manager.integrationLockWait = store.lockWait
	manager.integrationLockPoll = store.lockPoll
	newGate := &processGate{ch: make(chan struct{}, 1)}
	newGate.ch <- struct{}{}
	actual, _ := registryProcessGates.LoadOrStore(manager.IntegrationLockPath, newGate)
	manager.integrationGate = actual.(*processGate)
	if err := manager.ensureStableIntegrationLock(ctx); err != nil {
		return nil, err
	}
	return manager, nil
}

func (m *Manager) Prepare(ctx context.Context, request PrepareRequest) (Inspection, error) {
	if m == nil || m.Store == nil {
		return Inspection{}, errors.New("process workspace manager is not open")
	}
	lease := request.Lease
	lease.IntegrationRoot = m.IntegrationRoot
	lease.WorktreePath = ""
	lease.Portable.State = StatePreparing
	if err := lease.Validate(); err != nil {
		return Inspection{}, err
	}
	if err := m.validateIntegration(ctx, lease.Portable.BaseSHA, lease.Portable.Mode == ModeWritable); err != nil {
		return Inspection{}, err
	}
	if err := m.validateBaseWriteOwnership(ctx, lease.Portable); err != nil {
		return Inspection{}, err
	}
	path, err := m.workspacePath(lease.Portable.WorkspaceID)
	if err != nil {
		return Inspection{}, err
	}
	lease.WorktreePath = ""
	var created LocalLease
	if request.ExpectedGeneration != nil {
		created, err = m.Store.CreateAtGeneration(ctx, *request.ExpectedGeneration, lease)
	} else {
		created, err = m.Store.Create(ctx, lease)
	}
	if errors.Is(err, ErrLeaseExists) {
		existing, found, getErr := m.Store.Get(ctx, lease.Portable.WorkspaceID)
		if getErr != nil || !found {
			return Inspection{}, errors.Join(err, getErr)
		}
		if !sameReservation(existing, lease) {
			return Inspection{}, fmt.Errorf("%w: workspace id %s belongs to a different reservation", ErrWorkspaceConflict, lease.Portable.WorkspaceID)
		}
		created = existing
	} else if err != nil {
		return Inspection{}, err
	}
	return m.withIntegrationLock(ctx, func() (Inspection, error) {
		return m.reconcilePreparing(ctx, created, path)
	})
}

func (m *Manager) Inspect(ctx context.Context, workspaceID string) (Inspection, error) {
	lease, found, err := m.Store.Get(ctx, workspaceID)
	if err != nil {
		return Inspection{}, err
	}
	if !found {
		return Inspection{}, fmt.Errorf("%s: %w", workspaceID, ErrLeaseNotFound)
	}
	return m.inspectLease(ctx, lease)
}

func (m *Manager) reconcilePreparing(ctx context.Context, lease LocalLease, worktreePath string) (Inspection, error) {
	inspection, err := m.inspectLeaseAt(ctx, lease, worktreePath)
	if err != nil {
		return inspection, err
	}
	if inspection.Registered {
		if len(inspection.Problems) > 0 {
			return inspection, fmt.Errorf("%w: %s", ErrWorkspaceConflict, strings.Join(inspection.Problems, "; "))
		}
		if inspection.Dirty {
			return inspection, fmt.Errorf("%w: %s", ErrWorkspaceDirty, worktreePath)
		}
		if err := m.validateIntegration(ctx, lease.Portable.BaseSHA, lease.Portable.Mode == ModeWritable); err != nil {
			return inspection, err
		}
		return m.markPreparedAndRevalidate(ctx, lease, worktreePath, inspection)
	}
	if inspection.Present {
		return inspection, fmt.Errorf("%w: unregistered path exists at %s", ErrWorkspaceConflict, worktreePath)
	}
	if err := m.addWorktree(ctx, lease, worktreePath); err != nil {
		return inspection, err
	}
	inspection, err = m.inspectLeaseAt(ctx, lease, worktreePath)
	if err != nil {
		return inspection, err
	}
	if !inspection.Registered || !inspection.Present || inspection.Dirty || len(inspection.Problems) > 0 {
		return inspection, fmt.Errorf("%w: created worktree did not match reservation: %s", ErrWorkspaceConflict, strings.Join(inspection.Problems, "; "))
	}
	if err := m.validateIntegration(ctx, lease.Portable.BaseSHA, lease.Portable.Mode == ModeWritable); err != nil {
		return inspection, err
	}
	return m.markPreparedAndRevalidate(ctx, lease, worktreePath, inspection)
}

func (m *Manager) markPreparedAndRevalidate(ctx context.Context, lease LocalLease, worktreePath string, inspection Inspection) (Inspection, error) {
	prepared, err := m.markPrepared(ctx, lease.Portable.WorkspaceID, worktreePath, inspection)
	if err != nil {
		return inspection, err
	}
	if validationErr := m.validateIntegration(ctx, lease.Portable.BaseSHA, lease.Portable.Mode == ModeWritable); validationErr != nil {
		conflictErr := fmt.Errorf("%w: integration changed immediately after workspace publication: %v", ErrWorkspaceConflict, validationErr)
		updated, updateErr := m.Store.Update(ctx, lease.Portable.WorkspaceID, func(current *LocalLease) error {
			current.Portable.State = StateConflicted
			return nil
		})
		if updateErr == nil {
			prepared.Lease = updated
		}
		return prepared, errors.Join(conflictErr, updateErr)
	}
	return prepared, nil
}

func (m *Manager) addWorktree(ctx context.Context, lease LocalLease, worktreePath string) error {
	if lease.Portable.Mode == ModeWritable {
		fullRef, err := fullBranchRef(lease.Portable.Branch)
		if err != nil {
			return err
		}
		if _, err := m.git(ctx, "validate process branch", m.IntegrationRoot, "check-ref-format", fullRef); err != nil {
			return err
		}
		branch := strings.TrimPrefix(fullRef, "refs/heads/")
		markerRef := workspaceMarkerRef(lease)
		markerSHA, markerExists, err := m.resolveOptionalRef(ctx, markerRef)
		if err != nil {
			return err
		}
		branchSHA, branchExists, err := m.resolveOptionalRef(ctx, fullRef)
		if err != nil {
			return err
		}
		markers, err := m.workspaceMarkerRefs(ctx, lease.Portable.WorkspaceID)
		if err != nil {
			return err
		}
		if markerExists {
			if len(markers) != 1 || markers[0] != markerRef || !branchExists ||
				!strings.EqualFold(markerSHA, lease.Portable.BaseSHA) || !strings.EqualFold(branchSHA, lease.Portable.BaseSHA) {
				return fmt.Errorf("%w: branch %s does not match its lease ownership marker", ErrWorkspaceConflict, fullRef)
			}
		} else {
			if branchExists || len(markers) > 0 {
				return fmt.Errorf("%w: branch %s or workspace marker already exists without matching lease ownership", ErrWorkspaceConflict, fullRef)
			}
			transaction := fmt.Sprintf("start\ncreate %s %s\ncreate %s %s\nprepare\ncommit\n", fullRef, lease.Portable.BaseSHA, markerRef, lease.Portable.BaseSHA)
			if _, reserveErr := m.gitInput(ctx, "reserve process branch and ownership marker", m.IntegrationRoot, []byte(transaction), "update-ref", "--stdin"); reserveErr != nil {
				owned, inspectErr := m.branchReservationMatches(ctx, lease, fullRef, markerRef)
				if inspectErr != nil {
					return errors.Join(reserveErr, inspectErr)
				}
				if !owned {
					return reserveErr
				}
			}
		}
		if _, err = m.git(ctx, "attach reserved process branch", m.IntegrationRoot, "worktree", "add", "--", worktreePath, branch); err != nil {
			inspection, inspectErr := m.inspectLeaseAt(ctx, lease, worktreePath)
			if inspectErr == nil && inspection.Registered && inspection.Present && !inspection.Dirty && len(inspection.Problems) == 0 {
				return nil
			}
			return errors.Join(err, inspectErr)
		}
		return nil
	}
	if lease.Portable.Mode == ModeSnapshot {
		_, err := m.git(ctx, "create detached process snapshot", m.IntegrationRoot, "worktree", "add", "--detach", "--", worktreePath, lease.Portable.DetachedRevision)
		return err
	}
	return errors.New("workspace prepare requires writable or snapshot mode")
}

func (m *Manager) branchReservationMatches(ctx context.Context, lease LocalLease, fullRef, markerRef string) (bool, error) {
	markerSHA, markerExists, err := m.resolveOptionalRef(ctx, markerRef)
	if err != nil {
		return false, err
	}
	branchSHA, branchExists, err := m.resolveOptionalRef(ctx, fullRef)
	if err != nil {
		return false, err
	}
	markers, err := m.workspaceMarkerRefs(ctx, lease.Portable.WorkspaceID)
	if err != nil {
		return false, err
	}
	return markerExists && branchExists && len(markers) == 1 && markers[0] == markerRef &&
		strings.EqualFold(markerSHA, lease.Portable.BaseSHA) && strings.EqualFold(branchSHA, lease.Portable.BaseSHA), nil
}

func (m *Manager) markPrepared(ctx context.Context, workspaceID, worktreePath string, inspection Inspection) (Inspection, error) {
	now := m.Now().UTC()
	updated, err := m.Store.Update(ctx, workspaceID, func(current *LocalLease) error {
		current.WorktreePath = worktreePath
		current.Portable.State = StatePrepared
		current.Observation = WorktreeObservation{Registered: true, HeadSHA: inspection.Head, Branch: inspection.Branch, Dirty: false, InspectedAt: now}
		return nil
	})
	if err != nil {
		return inspection, err
	}
	inspection.Lease = updated
	return inspection, nil
}

func (m *Manager) validateIntegration(ctx context.Context, baseSHA string, requireHead bool) error {
	common, err := m.resolveCommonDir(ctx, m.IntegrationRoot)
	if err != nil {
		return err
	}
	if common != m.CommonDir {
		return fmt.Errorf("%w: integration common dir changed", ErrWorkspaceConflict)
	}
	status, err := m.gitOutput(ctx, "inspect integration status", m.IntegrationRoot, "status", "--porcelain=v1", "--untracked-files=normal")
	if err != nil {
		return err
	}
	if status != "" {
		return fmt.Errorf("%w: integration checkout is dirty", ErrWorkspaceDirty)
	}
	resolved, err := m.gitOutput(ctx, "resolve base revision", m.IntegrationRoot, "rev-parse", "--verify", baseSHA+"^{commit}")
	if err != nil {
		return err
	}
	if !strings.EqualFold(resolved, baseSHA) {
		return fmt.Errorf("%w: base revision resolved to %s", ErrWorkspaceConflict, resolved)
	}
	if requireHead {
		head, err := m.gitOutput(ctx, "inspect integration HEAD", m.IntegrationRoot, "rev-parse", "HEAD")
		if err != nil {
			return err
		}
		if !strings.EqualFold(head, baseSHA) {
			return fmt.Errorf("%w: integration HEAD %s differs from reserved base %s", ErrWorkspaceConflict, head, baseSHA)
		}
	}
	return nil
}

func (m *Manager) workspacePath(workspaceID string) (string, error) {
	if !safeID.MatchString(workspaceID) {
		return "", fmt.Errorf("%w: invalid workspace id %q", ErrUnsafeWorkspace, workspaceID)
	}
	path := filepath.Join(m.WorkspaceRoot, workspaceID)
	if !pathWithin(m.WorkspaceRoot, path) {
		return "", fmt.Errorf("%w: path escapes workspace root", ErrUnsafeWorkspace)
	}
	return filepath.Clean(path), nil
}

func (m *Manager) resolveCommonDir(ctx context.Context, directory string) (string, error) {
	value, err := m.gitOutput(ctx, "resolve Git common directory", directory, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(value) {
		value = filepath.Join(directory, value)
	}
	return existingCanonicalDir(value)
}

func (m *Manager) git(ctx context.Context, operation, dir string, args ...string) (GitResult, error) {
	return m.gitInput(ctx, operation, dir, nil, args...)
}

func (m *Manager) gitInput(ctx context.Context, operation, dir string, input []byte, args ...string) (GitResult, error) {
	result, err := m.Runner.Run(ctx, GitCommand{Binary: m.GitBinary, Dir: dir, Args: append([]string(nil), args...), Stdin: append([]byte(nil), input...)})
	if err != nil {
		return result, &GitError{Operation: operation, Args: append([]string(nil), args...), Stderr: string(result.Stderr), Err: err}
	}
	return result, nil
}

func (m *Manager) resolveOptionalRef(ctx context.Context, ref string) (string, bool, error) {
	output, err := m.gitOutput(ctx, "inspect process workspace ref", m.IntegrationRoot, "for-each-ref", "--format=%(objectname)", ref)
	if err != nil {
		return "", false, err
	}
	if output == "" {
		return "", false, nil
	}
	if strings.Contains(output, "\n") {
		return "", false, fmt.Errorf("%w: ref lookup for %s was ambiguous", ErrWorkspaceConflict, ref)
	}
	return output, true, nil
}

func (m *Manager) workspaceMarkerRefs(ctx context.Context, workspaceID string) ([]string, error) {
	prefix := "refs/issue-spec/process-workspaces/" + workspaceID + "/"
	output, err := m.gitOutput(ctx, "list process workspace ownership markers", m.IntegrationRoot, "for-each-ref", "--format=%(refname)", prefix)
	if err != nil || output == "" {
		return nil, err
	}
	return strings.Split(output, "\n"), nil
}

func workspaceMarkerRef(lease LocalLease) string {
	identity := struct {
		Portable PortableLease `json:"portable"`
		Token    string        `json:"token"`
	}{Portable: lease.Portable, Token: lease.Owner.Token}
	identity.Portable.State = ""
	identity.Portable.ResultCommit = ""
	identity.Portable.IntegrationSHA = ""
	identity.Portable.CreatedAt = time.Time{}
	identity.Portable.UpdatedAt = time.Time{}
	identity.Portable.RetentionExpiresAt = time.Time{}
	payload, _ := json.Marshal(identity)
	digest := sha256.Sum256(payload)
	return "refs/issue-spec/process-workspaces/" + lease.Portable.WorkspaceID + "/" + hex.EncodeToString(digest[:])
}

func (m *Manager) gitOutput(ctx context.Context, operation, dir string, args ...string) (string, error) {
	result, err := m.git(ctx, operation, dir, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(result.Stdout)), nil
}

func existingCanonicalDir(value string) (string, error) {
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", real)
	}
	return filepath.Clean(real), nil
}

func prepareCanonicalRoot(value string) (string, error) {
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return "", err
	}
	return existingCanonicalDir(abs)
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

type heldIntegrationLock struct {
	file        *os.File
	releaseGate func()
}

// withIntegrationLock is the package-wide ordering boundary for operations
// that inspect or publish integration-dependent state. P005 integration writes
// must use this same boundary rather than introducing another lock order.
func (m *Manager) withIntegrationLock(ctx context.Context, action func() (Inspection, error)) (result Inspection, resultErr error) {
	if m == nil || m.integrationGate == nil || action == nil {
		return Inspection{}, errors.New("process workspace integration coordinator is not open")
	}
	held, err := m.acquireIntegrationLock(ctx)
	if err != nil {
		return Inspection{}, err
	}
	result, actionErr := action()
	releaseErr := m.releaseIntegrationLock(held)
	return result, errors.Join(actionErr, releaseErr)
}

func (m *Manager) ensureStableIntegrationLock(ctx context.Context) error {
	releaseGate, err := m.acquireIntegrationGate(ctx)
	if err != nil {
		return err
	}
	defer releaseGate()
	file, err := os.OpenFile(m.IntegrationLockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	if err := file.Chmod(0o600); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := m.waitForIntegrationFlock(ctx, file); err != nil {
		return errors.Join(err, file.Close())
	}
	return errors.Join(unlockFile(file), file.Close())
}

func (m *Manager) acquireIntegrationLock(ctx context.Context) (heldIntegrationLock, error) {
	releaseGate, err := m.acquireIntegrationGate(ctx)
	if err != nil {
		return heldIntegrationLock{}, err
	}
	file, err := os.OpenFile(m.IntegrationLockPath, os.O_RDWR, 0)
	if err != nil {
		releaseGate()
		return heldIntegrationLock{}, err
	}
	if err := m.waitForIntegrationFlock(ctx, file); err != nil {
		releaseGate()
		return heldIntegrationLock{}, errors.Join(err, file.Close())
	}
	return heldIntegrationLock{file: file, releaseGate: releaseGate}, nil
}

func (m *Manager) releaseIntegrationLock(held heldIntegrationLock) (result error) {
	if held.releaseGate != nil {
		defer held.releaseGate()
	}
	if held.file == nil {
		return errors.New("integration lock file is missing")
	}
	return errors.Join(unlockFile(held.file), held.file.Close())
}

func (m *Manager) acquireIntegrationGate(ctx context.Context) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-m.integrationGate.ch:
		return func() { m.integrationGate.ch <- struct{}{} }, nil
	}
}

func (m *Manager) waitForIntegrationFlock(ctx context.Context, file *os.File) error {
	if ctx == nil {
		ctx = context.Background()
	}
	deadline := time.Now().Add(m.integrationLockWait)
	for {
		if err := tryFlock(file); err == nil {
			return nil
		} else if !lockUnavailable(err) {
			return err
		}
		if m.integrationLockWait <= 0 || !time.Now().Before(deadline) {
			return fmt.Errorf("%s: %w", m.IntegrationLockPath, ErrIntegrationLocked)
		}
		timer := time.NewTimer(m.integrationLockPoll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func sameReservation(left, right LocalLease) bool {
	return left.Portable.WorkspaceID == right.Portable.WorkspaceID && left.Portable.Repository == right.Portable.Repository &&
		left.Portable.ProcessID == right.Portable.ProcessID && left.Portable.ExecutionClass == right.Portable.ExecutionClass &&
		left.Portable.Mode == right.Portable.Mode && left.Portable.BaseSHA == right.Portable.BaseSHA && left.Portable.Branch == right.Portable.Branch &&
		left.Portable.DetachedRevision == right.Portable.DetachedRevision && equalStrings(left.Portable.WriteOwnership, right.Portable.WriteOwnership) &&
		equalStrings(left.Portable.SharedTouchpoints, right.Portable.SharedTouchpoints) &&
		left.Portable.IntegrationOwner == right.Portable.IntegrationOwner && left.Portable.RuntimeNamespace == right.Portable.RuntimeNamespace &&
		equalRuntimeResources(left.Portable.RuntimeResources, right.Portable.RuntimeResources) && left.IntegrationRoot == right.IntegrationRoot &&
		left.Owner.Token == right.Owner.Token
}
