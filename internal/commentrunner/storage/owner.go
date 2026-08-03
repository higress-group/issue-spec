package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ErrOwnerLocked reports that another logical runner already owns this
// canonical workspace root. The focused remediation is to stop the old runner
// before starting a new one.
var ErrOwnerLocked = errors.New("workspace root already has a storage owner; stop the old runner before starting a new one")

// OwnerLock is the process-lifetime flock on `<root>/.storage/owner.lock`.
// Exactly one logical owner may hold it for a canonical root; it is the top of
// the storage lock order (owner -> state flock -> session workspace lock ->
// sidecar lock -> resource try-lock).
type OwnerLock struct {
	root string
	path string
	file *os.File
	mu   sync.Mutex
}

var ownerRegistry = struct {
	sync.Mutex
	byRoot map[string]*OwnerLock
}{byRoot: map[string]*OwnerLock{}}

// AcquireOwner takes fail-fast ownership of the canonical workspace root. It
// conflicts with any other holder, including another logical owner inside this
// process, so poll/serve/maintenance overlap is detected in tests as well as
// across processes.
func AcquireOwner(workspaceRoot string) (*OwnerLock, error) {
	canonical, err := Canonicalize(workspaceRoot)
	if err != nil {
		return nil, err
	}
	identity, err := RootIdentity(canonical)
	if err != nil {
		return nil, err
	}
	ownerRegistry.Lock()
	if ownerRegistry.byRoot[canonical] != nil {
		ownerRegistry.Unlock()
		return nil, &OwnerConflictError{Path: filepath.Join(canonical, StorageDirName, ownerLockName)}
	}
	ownerRegistry.Unlock()

	dir := filepath.Join(canonical, StorageDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("prepare storage directory: %w", err)
	}
	path := filepath.Join(dir, ownerLockName)
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open storage owner lock: %w", err)
	}
	if err := flockTryExclusive(file); err != nil {
		_ = file.Close()
		if lockWouldBlock(err) {
			return nil, &OwnerConflictError{Path: path}
		}
		return nil, fmt.Errorf("lock storage owner: %w", err)
	}
	lock := &OwnerLock{root: canonical, path: path, file: file}
	ownerRegistry.Lock()
	if ownerRegistry.byRoot[canonical] != nil {
		ownerRegistry.Unlock()
		_ = flockUnlock(file)
		_ = file.Close()
		return nil, &OwnerConflictError{Path: path}
	}
	ownerRegistry.byRoot[canonical] = lock
	ownerRegistry.Unlock()
	if err := writeOwnerMetadata(file, identity); err != nil {
		_ = lock.Release()
		return nil, err
	}
	return lock, nil
}

// OwnerConflictError identifies the lock path that blocked ownership.
type OwnerConflictError struct {
	Path string
}

func (e *OwnerConflictError) Error() string {
	return fmt.Sprintf("%s: %v", e.Path, ErrOwnerLocked)
}

func (e *OwnerConflictError) Is(target error) bool { return target == ErrOwnerLocked }

func (l *OwnerLock) Root() string { return l.root }

// Release drops ownership. It is idempotent and safe to defer.
func (l *OwnerLock) Release() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	ownerRegistry.Lock()
	if ownerRegistry.byRoot[l.root] == l {
		delete(ownerRegistry.byRoot, l.root)
	}
	ownerRegistry.Unlock()
	err := flockUnlock(l.file)
	closeErr := l.file.Close()
	l.file = nil
	if err != nil {
		return err
	}
	return closeErr
}

func writeOwnerMetadata(file *os.File, identity string) error {
	if err := file.Truncate(0); err != nil {
		return err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(file, "pid=%d\ncreated_at=%s\nroot_identity=%s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339Nano), identity); err != nil {
		return err
	}
	return file.Sync()
}

type ownerContextKey struct{}

type serviceContextKey struct{}

// WithService attaches a shared storage service so every entry point of one
// logical run reuses a single sidecar handle instead of reopening per call.
func WithService(ctx context.Context, service *Service) context.Context {
	if service == nil {
		return ctx
	}
	return context.WithValue(ctx, serviceContextKey{}, service)
}

// ServiceFromContext returns the shared service when it is bound to the same
// canonical root.
func ServiceFromContext(ctx context.Context, workspaceRoot string) (*Service, bool) {
	if ctx == nil {
		return nil, false
	}
	service, ok := ctx.Value(serviceContextKey{}).(*Service)
	if !ok || service == nil {
		return nil, false
	}
	canonical, err := Canonicalize(workspaceRoot)
	if err != nil || service.Root() != canonical {
		return nil, false
	}
	return service, true
}

// WithOwner attaches an acquired owner lock so nested destructive entry points
// share the one logical owner instead of re-acquiring.
func WithOwner(ctx context.Context, owner *OwnerLock) context.Context {
	if owner == nil {
		return ctx
	}
	return context.WithValue(ctx, ownerContextKey{}, owner)
}

// OwnerFromContext returns the shared owner, if any.
func OwnerFromContext(ctx context.Context) (*OwnerLock, bool) {
	if ctx == nil {
		return nil, false
	}
	owner, ok := ctx.Value(ownerContextKey{}).(*OwnerLock)
	return owner, ok && owner != nil
}

// EnsureOwner returns the context-carried owner with a no-op release, or
// acquires a fresh fail-fast owner with a real release. Every destructive
// entry point routes through this so the owner lock covers poll sync/async,
// serve, startup/full/async-busy cleanup, maintenance, and one-shot paths
// without double-locking inside one logical run.
func EnsureOwner(ctx context.Context, workspaceRoot string) (*OwnerLock, func(), error) {
	if owner, ok := OwnerFromContext(ctx); ok {
		if sameRoot(owner.Root(), workspaceRoot) {
			return owner, func() {}, nil
		}
	}
	owner, err := AcquireOwner(workspaceRoot)
	if err != nil {
		return nil, nil, err
	}
	return owner, func() { _ = owner.Release() }, nil
}

func sameRoot(left, right string) bool {
	canonicalRight, err := Canonicalize(right)
	if err != nil {
		return false
	}
	return left == canonicalRight
}
