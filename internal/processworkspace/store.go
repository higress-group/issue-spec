package processworkspace

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/higress-group/issue-spec/internal/assignment"
)

var (
	ErrRegistryLocked     = errors.New("process workspace registry locked")
	ErrRegistryCorrupt    = errors.New("process workspace registry corrupt")
	ErrLeaseExists        = errors.New("process workspace lease already exists")
	ErrLeaseNotFound      = errors.New("process workspace lease not found")
	ErrGenerationConflict = errors.New("process workspace registry generation conflict")
)

const (
	registryRelativePath = "issue-spec/process-workspaces/registry.json"
	defaultLockWait      = 2 * time.Second
	defaultLockPoll      = 10 * time.Millisecond
)

type Store struct {
	commonDir   string
	path        string
	lockPath    string
	now         func() time.Time
	token       func() (string, error)
	lockWait    time.Duration
	lockPoll    time.Duration
	processGate *processGate
}

type StoreOption func(*Store)

func WithClock(now func() time.Time) StoreOption {
	return func(store *Store) {
		if now != nil {
			store.now = now
		}
	}
}

func WithLockWait(wait time.Duration) StoreOption {
	return func(store *Store) { store.lockWait = wait }
}

func WithLockPoll(interval time.Duration) StoreOption {
	return func(store *Store) {
		if interval > 0 {
			store.lockPoll = interval
		}
	}
}

func withTokenSource(source func() (string, error)) StoreOption {
	return func(store *Store) {
		if source != nil {
			store.token = source
		}
	}
}

type LockInfo struct {
	Held       bool      `json:"held"`
	Token      string    `json:"token"`
	PID        int       `json:"pid"`
	Hostname   string    `json:"hostname,omitempty"`
	AcquiredAt time.Time `json:"acquired_at"`
	Registry   string    `json:"registry"`
}

type LockError struct {
	Path   string
	Holder LockInfo
}

func (e *LockError) Error() string {
	if e.Holder.Token == "" {
		return fmt.Sprintf("%s: %v", e.Path, ErrRegistryLocked)
	}
	return fmt.Sprintf("%s: %v by pid %d since %s", e.Path, ErrRegistryLocked, e.Holder.PID, e.Holder.AcquiredAt.Format(time.RFC3339Nano))
}

func (e *LockError) Is(target error) bool { return target == ErrRegistryLocked }

type CorruptRegistryError struct {
	Path string
	Err  error
}

type GenerationConflictError struct {
	Expected uint64
	Current  uint64
}

func (e *GenerationConflictError) Error() string {
	return fmt.Sprintf("%v: expected %d, current %d", ErrGenerationConflict, e.Expected, e.Current)
}

func (e *GenerationConflictError) Is(target error) bool { return target == ErrGenerationConflict }

func (e *CorruptRegistryError) Error() string {
	return fmt.Sprintf("%s: %v: %v", e.Path, ErrRegistryCorrupt, e.Err)
}

func (e *CorruptRegistryError) Unwrap() error { return e.Err }
func (e *CorruptRegistryError) Is(target error) bool {
	return target == ErrRegistryCorrupt
}

type processGate struct{ ch chan struct{} }

type heldFileLock struct {
	info LockInfo
	file *os.File
}

var registryProcessGates sync.Map

func OpenStore(gitCommonDir string, options ...StoreOption) (*Store, error) {
	if gitCommonDir == "" {
		return nil, errors.New("Git common directory is required")
	}
	commonDir, err := filepath.Abs(gitCommonDir)
	if err != nil {
		return nil, err
	}
	commonDir = filepath.Clean(commonDir)
	path := filepath.Join(commonDir, filepath.FromSlash(registryRelativePath))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create process workspace registry directory: %w", err)
	}
	store := &Store{
		commonDir: commonDir,
		path:      path,
		lockPath:  path + ".lock",
		now:       time.Now,
		token:     randomToken,
		lockWait:  defaultLockWait,
		lockPoll:  defaultLockPoll,
	}
	for _, option := range options {
		if option != nil {
			option(store)
		}
	}
	newGate := &processGate{ch: make(chan struct{}, 1)}
	newGate.ch <- struct{}{}
	actual, _ := registryProcessGates.LoadOrStore(store.path, newGate)
	store.processGate = actual.(*processGate)
	if err := store.ensureStableLockFile(context.Background()); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) CommonDir() string    { return s.commonDir }
func (s *Store) RegistryPath() string { return s.path }
func (s *Store) LockPath() string     { return s.lockPath }

func (s *Store) Load(ctx context.Context) (Registry, error) {
	var result Registry
	err := s.withLock(ctx, func() error {
		registry, err := loadRegistry(s.path)
		if err != nil {
			return err
		}
		result = cloneRegistry(registry)
		return nil
	})
	return result, err
}

func (s *Store) Get(ctx context.Context, workspaceID string) (LocalLease, bool, error) {
	var result LocalLease
	var found bool
	err := s.withLock(ctx, func() error {
		registry, err := loadRegistry(s.path)
		if err != nil {
			return err
		}
		result, found = registry.Leases[workspaceID]
		result = cloneLocalLease(result)
		return nil
	})
	return result, found, err
}

// peek reads an atomically published registry snapshot without acquiring the
// writer lock. It is reserved for pre-mutation deprecation checks where even a
// lock-file ownership update would violate the zero-write contract.
func (s *Store) peek(workspaceID string) (LocalLease, bool, error) {
	if s == nil || s.path == "" {
		return LocalLease{}, false, errors.New("process workspace store is not open")
	}
	registry, err := loadRegistry(s.path)
	if err != nil {
		return LocalLease{}, false, err
	}
	lease, found := registry.Leases[workspaceID]
	return cloneLocalLease(lease), found, nil
}

func (s *Store) List(ctx context.Context) ([]LocalLease, error) {
	registry, err := s.Load(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(registry.Leases))
	for id := range registry.Leases {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := make([]LocalLease, 0, len(ids))
	for _, id := range ids {
		result = append(result, cloneLocalLease(registry.Leases[id]))
	}
	return result, nil
}

func (s *Store) Create(ctx context.Context, lease LocalLease) (LocalLease, error) {
	return s.create(ctx, nil, lease)
}

func (s *Store) CreateAtGeneration(ctx context.Context, expected uint64, lease LocalLease) (LocalLease, error) {
	return s.create(ctx, &expected, lease)
}

func (s *Store) create(ctx context.Context, expected *uint64, lease LocalLease) (LocalLease, error) {
	var result LocalLease
	err := s.withLock(ctx, func() error {
		registry, err := loadRegistry(s.path)
		if err != nil {
			return err
		}
		if err := checkGeneration(registry.Generation, expected); err != nil {
			return err
		}
		id := lease.Portable.WorkspaceID
		if _, exists := registry.Leases[id]; exists {
			return fmt.Errorf("%s: %w", id, ErrLeaseExists)
		}
		now := s.now().UTC()
		if lease.Portable.SchemaVersion == 0 {
			lease.Portable.SchemaVersion = LeaseSchemaVersion
		}
		if lease.Portable.CreatedAt.IsZero() {
			lease.Portable.CreatedAt = now
		}
		lease.Portable.UpdatedAt = now
		lease.LocalRevision = 1
		registry.Leases[id] = cloneLocalLease(lease)
		registry.Generation++
		registry.UpdatedAt = now
		if err := registry.Validate(); err != nil {
			return err
		}
		if err := writeRegistryAtomic(s.path, registry); err != nil {
			return err
		}
		result = cloneLocalLease(lease)
		return nil
	})
	return result, err
}

func (s *Store) Update(ctx context.Context, workspaceID string, mutate func(*LocalLease) error) (LocalLease, error) {
	return s.update(ctx, nil, workspaceID, mutate)
}

func (s *Store) UpdateAtGeneration(ctx context.Context, expected uint64, workspaceID string, mutate func(*LocalLease) error) (LocalLease, error) {
	return s.update(ctx, &expected, workspaceID, mutate)
}

// BindAssignment atomically persists the complete assignment and its portable
// identity before any caller can publish or deliver a packet. A normal retry
// reuses the same binding; advancing generation is an explicit CAS operation.
func (s *Store) BindAssignment(ctx context.Context, workspaceID string, value assignment.Assignment, redispatch bool, expectedAssignmentGeneration *uint64) (LocalLease, error) {
	return s.bindAssignment(ctx, workspaceID, value, redispatch, expectedAssignmentGeneration, nil)
}

// recoverAssignment restores the full local assignment behind an already
// durable remote binding without upgrading that binding's historical shape.
func (s *Store) recoverAssignment(ctx context.Context, workspaceID string, value assignment.Assignment, binding AssignmentBinding) (LocalLease, error) {
	return s.bindAssignment(ctx, workspaceID, value, false, nil, &binding)
}

func (s *Store) bindAssignment(ctx context.Context, workspaceID string, value assignment.Assignment, redispatch bool,
	expectedAssignmentGeneration *uint64, recovery *AssignmentBinding) (LocalLease, error) {
	if value.Role != assignment.RoleImplementation {
		return LocalLease{}, fmt.Errorf("%w: only change-bearing implementation assignments may be persisted", ErrDeprecatedWorkflow)
	}
	bindingValue, err := NewAssignmentBinding(value, 1)
	if err != nil {
		return LocalLease{}, err
	}
	if recovery != nil {
		if err := ValidateAssignmentBindingMatchesAssignment(*recovery, value, recovery.Generation); err != nil {
			return LocalLease{}, fmt.Errorf("assignment recovery binding mismatch: %w", err)
		}
		bindingValue = *cloneAssignmentBinding(recovery)
	}
	var result LocalLease
	err = s.withLock(ctx, func() error {
		registry, err := loadRegistry(s.path)
		if err != nil {
			return err
		}
		original, exists := registry.Leases[workspaceID]
		if !exists {
			return fmt.Errorf("%s: %w", workspaceID, ErrLeaseNotFound)
		}
		if original.Portable.State != StatePrepared {
			return fmt.Errorf("assignment issuance requires prepared lease, current state is %s", original.Portable.State)
		}
		binding := cloneAssignmentBinding(&bindingValue)
		current := original.Portable.Assignment
		if current != nil {
			if recovery != nil && !AssignmentBindingEqual(current, recovery) {
				return errors.New("assignment recovery binding conflicts with the persisted local binding")
			}
			if recovery == nil {
				binding.Generation = current.Generation
			}
			if !redispatch {
				exactRetry := AssignmentBindingEqual(current, binding)
				legacyRetry := current.SelectorAuthority == nil && assignmentBindingCoreEqual(*current, *binding)
				if exactRetry || legacyRetry {
					if original.Assignment == nil {
						original.Assignment = &value
					} else if !sameAssignment(*original.Assignment, value) {
						return errors.New("assignment binding conflict: local assignment differs; inspect and reconcile the lease")
					} else {
						result = cloneLocalLease(original)
						return nil
					}
					if legacyRetry {
						binding = cloneAssignmentBinding(current)
					}
				} else {
					return errors.New("assignment binding conflict: requested assignment differs; use explicit redispatch with the current generation")
				}
			} else {
				if expectedAssignmentGeneration == nil || *expectedAssignmentGeneration != current.Generation {
					return fmt.Errorf("assignment redispatch generation conflict: expected current generation %d", current.Generation)
				}
				if original.AcceptedReceiptID != "" || original.Portable.ResultCommit != "" || original.Portable.IntegrationSHA != "" {
					return errors.New("assignment redispatch is forbidden after receipt acceptance, worker completion, or integration")
				}
				binding.Generation = current.Generation + 1
				if binding.AssignmentID == current.AssignmentID || binding.Digest == current.Digest {
					return errors.New("assignment redispatch must issue a distinct assignment id and digest")
				}
			}
		} else if redispatch {
			return errors.New("assignment redispatch requires an existing assignment binding")
		}
		updated := cloneLocalLease(original)
		updated.Portable.Assignment = binding
		updated.Assignment = &value
		updated.Portable.UpdatedAt = s.now().UTC()
		updated.LocalRevision++
		if err := updated.Validate(); err != nil {
			return err
		}
		registry.Leases[workspaceID] = updated
		registry.Generation++
		registry.UpdatedAt = updated.Portable.UpdatedAt
		if err := registry.Validate(); err != nil {
			return err
		}
		if err := writeRegistryAtomic(s.path, registry); err != nil {
			return err
		}
		result = cloneLocalLease(updated)
		return nil
	})
	return result, err
}

func sameAssignment(left, right assignment.Assignment) bool {
	leftJSON, leftErr := assignment.CanonicalAssignmentJSON(left)
	rightJSON, rightErr := assignment.CanonicalAssignmentJSON(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func (s *Store) update(ctx context.Context, expected *uint64, workspaceID string, mutate func(*LocalLease) error) (LocalLease, error) {
	if mutate == nil {
		return LocalLease{}, errors.New("lease update callback is required")
	}
	var result LocalLease
	err := s.withLock(ctx, func() error {
		registry, err := loadRegistry(s.path)
		if err != nil {
			return err
		}
		if err := checkGeneration(registry.Generation, expected); err != nil {
			return err
		}
		original, exists := registry.Leases[workspaceID]
		if !exists {
			return fmt.Errorf("%s: %w", workspaceID, ErrLeaseNotFound)
		}
		updated := cloneLocalLease(original)
		if err := mutate(&updated); err != nil {
			return err
		}
		if err := validateImmutableLease(original.Portable, updated.Portable); err != nil {
			return err
		}
		if err := validateWorktreePathMutation(original, updated); err != nil {
			return err
		}
		if !CanTransition(original.Portable.State, updated.Portable.State, original.Portable.Mode) {
			return fmt.Errorf("illegal workspace lifecycle transition %s -> %s", original.Portable.State, updated.Portable.State)
		}
		updated.Portable.UpdatedAt = s.now().UTC()
		updated.LocalRevision = original.LocalRevision + 1
		registry.Leases[workspaceID] = updated
		registry.Generation++
		registry.UpdatedAt = updated.Portable.UpdatedAt
		if err := registry.Validate(); err != nil {
			return err
		}
		if err := writeRegistryAtomic(s.path, registry); err != nil {
			return err
		}
		result = cloneLocalLease(updated)
		return nil
	})
	return result, err
}

// Purge removes only a cleaned lease and requires the machine-local owner
// token. Normal cleanup should first persist StateCleaned so reconciliation has
// an observable tombstone.
func (s *Store) Purge(ctx context.Context, workspaceID, ownerToken string) error {
	return s.withLock(ctx, func() error {
		registry, err := loadRegistry(s.path)
		if err != nil {
			return err
		}
		lease, exists := registry.Leases[workspaceID]
		if !exists {
			return nil
		}
		if lease.Portable.State != StateCleaned {
			return errors.New("only a cleaned lease may be purged")
		}
		if ownerToken == "" || ownerToken != lease.Owner.Token {
			return errors.New("lease owner token mismatch")
		}
		delete(registry.Leases, workspaceID)
		registry.Generation++
		registry.UpdatedAt = s.now().UTC()
		if err := registry.Validate(); err != nil {
			return err
		}
		return writeRegistryAtomic(s.path, registry)
	})
}

func (s *Store) InspectLock() (LockInfo, bool, error) {
	releaseProcess, err := s.acquireProcessGate(context.Background())
	if err != nil {
		return LockInfo{}, false, err
	}
	defer releaseProcess()
	file, err := os.OpenFile(s.lockPath, os.O_RDWR, 0)
	if err != nil {
		return LockInfo{}, false, err
	}
	defer file.Close()
	if err := tryFlock(file); err != nil {
		if !lockUnavailable(err) {
			return LockInfo{}, false, err
		}
		info, readErr := readLockInfo(s.lockPath)
		return info, true, readErr
	}
	unlockErr := unlockFile(file)
	if unlockErr != nil {
		return LockInfo{}, false, unlockErr
	}
	return LockInfo{Registry: s.path}, false, nil
}

// RecoverStaleLock is deliberately explicit and compare-by-token. The store
// never guesses that a lock is stale merely from PID or age.
func (s *Store) RecoverStaleLock(ctx context.Context, expectedToken string, staleBefore time.Time) error {
	releaseProcess, err := s.acquireProcessGate(ctx)
	if err != nil {
		return err
	}
	defer releaseProcess()
	file, err := os.OpenFile(s.lockPath, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := tryFlock(file); err != nil {
		if lockUnavailable(err) {
			return &LockError{Path: s.lockPath}
		}
		return err
	}
	defer unlockFile(file)
	info, err := readLockInfoFrom(file)
	if err != nil {
		return err
	}
	if !info.Held {
		return nil
	}
	if expectedToken == "" || info.Token != expectedToken {
		return errors.New("registry lock token mismatch")
	}
	if staleBefore.IsZero() || !info.AcquiredAt.Before(staleBefore) {
		return fmt.Errorf("registry lock is not older than %s", staleBefore.UTC().Format(time.RFC3339Nano))
	}
	return writeLockInfo(file, LockInfo{Registry: s.path})
}

func (s *Store) withLock(ctx context.Context, action func() error) error {
	if s == nil || s.processGate == nil {
		return errors.New("process workspace store is not open")
	}
	if action == nil {
		return errors.New("registry action is required")
	}
	releaseProcess, err := s.acquireProcessGate(ctx)
	if err != nil {
		return err
	}
	defer releaseProcess()
	lock, err := s.acquireFileLock(ctx)
	if err != nil {
		return err
	}
	actionErr := action()
	releaseErr := s.releaseFileLock(lock)
	return errors.Join(actionErr, releaseErr)
}

func (s *Store) acquireProcessGate(ctx context.Context) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.processGate.ch:
		return func() { s.processGate.ch <- struct{}{} }, nil
	}
}

func (s *Store) acquireFileLock(ctx context.Context) (heldFileLock, error) {
	file, err := os.OpenFile(s.lockPath, os.O_RDWR, 0)
	if err != nil {
		return heldFileLock{}, err
	}
	if err := s.waitForFlock(ctx, file); err != nil {
		return heldFileLock{}, errors.Join(err, file.Close())
	}
	token, err := s.token()
	if err != nil {
		_ = unlockFile(file)
		_ = file.Close()
		return heldFileLock{}, err
	}
	hostname, _ := os.Hostname()
	info := LockInfo{Held: true, Token: token, PID: os.Getpid(), Hostname: hostname, AcquiredAt: s.now().UTC(), Registry: s.path}
	if err := writeLockInfo(file, info); err != nil {
		_ = unlockFile(file)
		_ = file.Close()
		return heldFileLock{}, err
	}
	return heldFileLock{info: info, file: file}, nil
}

func (s *Store) releaseFileLock(held heldFileLock) (result error) {
	if held.file == nil {
		return errors.New("registry lock file is missing")
	}
	defer func() {
		result = errors.Join(result, unlockFile(held.file), held.file.Close())
	}()
	current, err := readLockInfoFrom(held.file)
	if err != nil {
		return err
	}
	if !current.Held || current.Token != held.info.Token {
		return errors.New("registry lock changed owner before release")
	}
	return writeLockInfo(held.file, LockInfo{Registry: s.path})
}

func (s *Store) ensureStableLockFile(ctx context.Context) error {
	releaseProcess, err := s.acquireProcessGate(ctx)
	if err != nil {
		return err
	}
	defer releaseProcess()
	file, err := os.OpenFile(s.lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	if err := file.Chmod(0o600); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := s.waitForFlock(ctx, file); err != nil {
		return errors.Join(err, file.Close())
	}
	actionErr := func() error {
		info, err := readLockInfoFrom(file)
		if err == nil {
			if info.Registry != "" && info.Registry != s.path {
				return fmt.Errorf("registry lock targets %q, expected %q", info.Registry, s.path)
			}
			return nil
		}
		if !errors.Is(err, io.EOF) {
			return err
		}
		return writeLockInfo(file, LockInfo{Registry: s.path})
	}()
	return errors.Join(actionErr, unlockFile(file), file.Close())
}

func (s *Store) waitForFlock(ctx context.Context, file *os.File) error {
	if ctx == nil {
		ctx = context.Background()
	}
	deadline := time.Now().Add(s.lockWait)
	for {
		if err := tryFlock(file); err == nil {
			return nil
		} else if !lockUnavailable(err) {
			return err
		}
		if s.lockWait <= 0 || !time.Now().Before(deadline) {
			holder, _ := readLockInfo(s.lockPath)
			return &LockError{Path: s.lockPath, Holder: holder}
		}
		timer := time.NewTimer(s.lockPoll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func writeLockInfo(file *os.File, info LockInfo) error {
	data, err := json.Marshal(info)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := file.Truncate(0); err != nil {
		return err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return err
	}
	return file.Sync()
}

func loadRegistry(path string) (Registry, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return NewRegistry(), nil
	}
	if err != nil {
		return Registry{}, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return Registry{}, &CorruptRegistryError{Path: path, Err: errors.New("empty registry")}
	}
	var registry Registry
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&registry); err != nil {
		return Registry{}, &CorruptRegistryError{Path: path, Err: err}
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return Registry{}, &CorruptRegistryError{Path: path, Err: err}
	}
	if err := registry.Validate(); err != nil {
		return Registry{}, &CorruptRegistryError{Path: path, Err: err}
	}
	return registry, nil
}

func writeRegistryAtomic(path string, registry Registry) error {
	data, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".registry.json.tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		_ = tmp.Close()
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	cleanup = false
	dirFile, err := os.Open(dir)
	if err != nil {
		return err
	}
	syncErr := dirFile.Sync()
	closeErr := dirFile.Close()
	return errors.Join(syncErr, closeErr)
}

func readLockInfo(path string) (LockInfo, error) {
	file, err := os.Open(path)
	if err != nil {
		return LockInfo{}, err
	}
	defer file.Close()
	return readLockInfoFrom(file)
}

func readLockInfoFrom(file *os.File) (LockInfo, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return LockInfo{}, err
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return LockInfo{}, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return LockInfo{}, io.EOF
	}
	var info LockInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return LockInfo{}, err
	}
	return info, nil
}

func validateImmutableLease(before, after PortableLease) error {
	if before.SchemaVersion != after.SchemaVersion || before.WorkspaceID != after.WorkspaceID || before.Repository != after.Repository ||
		before.ProcessID != after.ProcessID || before.ExecutionClass != after.ExecutionClass || before.Mode != after.Mode ||
		before.BaseSHA != after.BaseSHA || before.Branch != after.Branch || before.DetachedRevision != after.DetachedRevision ||
		before.IntegrationOwner != after.IntegrationOwner || before.RuntimeNamespace != after.RuntimeNamespace ||
		!equalStrings(before.WriteOwnership, after.WriteOwnership) || !equalStrings(before.SharedTouchpoints, after.SharedTouchpoints) ||
		before.CreatedAt != after.CreatedAt || !equalAssignmentBinding(before.Assignment, after.Assignment) {
		return errors.New("lease identity, base, ownership, namespace, and creation time are immutable")
	}
	if !equalRuntimeResources(before.RuntimeResources, after.RuntimeResources) {
		return errors.New("lease runtime resources are immutable")
	}
	return nil
}

func equalAssignmentBinding(left, right *AssignmentBinding) bool {
	return AssignmentBindingEqual(left, right)
}

func validateWorktreePathMutation(before, after LocalLease) error {
	if before.WorktreePath == "" {
		return nil
	}
	if after.Portable.State == StateCleaned {
		if after.WorktreePath != "" && after.WorktreePath != before.WorktreePath {
			return errors.New("assigned worktree path cannot be replaced")
		}
		return nil
	}
	if after.WorktreePath != before.WorktreePath {
		return errors.New("assigned worktree path must be retained until the cleaned tombstone")
	}
	return nil
}

func checkGeneration(current uint64, expected *uint64) error {
	if expected != nil && *expected != current {
		return &GenerationConflictError{Expected: *expected, Current: current}
	}
	return nil
}

func equalRuntimeResources(left, right []RuntimeResource) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func cloneRegistry(registry Registry) Registry {
	result := registry
	result.Leases = make(map[string]LocalLease, len(registry.Leases))
	for id, lease := range registry.Leases {
		result.Leases[id] = cloneLocalLease(lease)
	}
	return result
}

func cloneLocalLease(lease LocalLease) LocalLease {
	result := lease
	result.Portable.WriteOwnership = append([]string(nil), lease.Portable.WriteOwnership...)
	result.Portable.SharedTouchpoints = append([]string(nil), lease.Portable.SharedTouchpoints...)
	result.Portable.RuntimeResources = append([]RuntimeResource(nil), lease.Portable.RuntimeResources...)
	result.Portable.Assignment = cloneAssignmentBinding(lease.Portable.Assignment)
	if lease.Assignment != nil {
		value := *lease.Assignment
		result.Assignment = &value
	}
	if lease.Portable.AcceptedReceiptSubmission != nil {
		value := *lease.Portable.AcceptedReceiptSubmission
		result.Portable.AcceptedReceiptSubmission = &value
	}
	return result
}

func randomToken() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}
