package storage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/higress-group/issue-spec/internal/commentrunner/state"
)

// ErrReportOnly marks a sidecar whose root identity or schema belongs to a
// different root or a newer binary: inventory is read-only.
var ErrReportOnly = errors.New("storage sidecar is report-only")

// ErrCorruptSidecar marks an unreadable sidecar payload that was rebuilt.
var ErrCorruptSidecar = errors.New("storage sidecar corrupt")

// CorruptSidecarError records why a sidecar was rebuilt so reconciliation can
// surface the evidence-losing event instead of resetting it silently.
type CorruptSidecarError struct {
	Path string
	Err  error
}

func (e *CorruptSidecarError) Error() string {
	return fmt.Sprintf("%s: %v: %v", e.Path, ErrCorruptSidecar, e.Err)
}

func (e *CorruptSidecarError) Unwrap() error { return e.Err }
func (e *CorruptSidecarError) Is(target error) bool {
	return target == ErrCorruptSidecar
}

// SidecarStatus describes how much trust this process may place in the loaded
// sidecar for the current reconcile pass.
type SidecarStatus string

const (
	// SidecarReady: loaded and validated against this root.
	SidecarReady SidecarStatus = "ready"
	// SidecarRebuilt: missing or corrupt; a fresh inventory starts here.
	// Ownership proof previously held only by the sidecar is lost, so unmatched
	// directories conservatively restart orphan observation.
	SidecarRebuilt SidecarStatus = "rebuilt"
	// SidecarReportOnly: foreign root identity or newer schema. No writes and no
	// deletions are permitted.
	SidecarReportOnly SidecarStatus = "report_only"
)

// Store is the locked, atomically persisted `.storage/state.json` sidecar.
// Lock discipline: the sidecar flock is acquired only for short
// load/mutate/save sections and is never held while acquiring state or session
// workspace locks.
type Store struct {
	mu          sync.Mutex
	dir         string
	path        string
	lockPath    string
	root        string
	identity    string
	state       StorageState
	status      SidecarStatus
	closed      bool
	now         func() time.Time
	backupError error
	loadCause   *CorruptSidecarError
}

// OpenStore loads the sidecar for the canonical workspace root, creating the
// private `.storage` directory when needed. Missing or corrupt sidecars open
// in rebuilt status; foreign-root or newer-schema sidecars open report-only.
func OpenStore(workspaceRoot string) (*Store, error) {
	root := strings.TrimSpace(workspaceRoot)
	if root == "" {
		return nil, fmt.Errorf("workspace root is required")
	}
	canonical, err := Canonicalize(root)
	if err != nil {
		return nil, err
	}
	identity, err := RootIdentity(canonical)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(canonical, StorageDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("prepare storage directory: %w", err)
	}
	store := &Store{
		dir:      dir,
		path:     filepath.Join(dir, sidecarFileName),
		lockPath: filepath.Join(dir, sidecarLockName),
		root:     canonical,
		identity: identity,
		now:      func() time.Time { return time.Now().UTC() },
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) Status() SidecarStatus { return s.status }

// RootIdentity returns the identity hash bound to this store.
func (s *Store) RootIdentity() string { return s.identity }

// Path returns the sidecar file path.
func (s *Store) Path() string { return s.path }

// Reload re-reads the sidecar under its lock so a long-lived store observes
// writes made by other stores on the same root.
func (s *Store) Reload() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("storage sidecar store is closed")
	}
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	return s.load()
}

// State returns a copy of the currently loaded sidecar state.
func (s *Store) State() StorageState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneStorageState(s.state)
}

// BackupError reports a non-fatal failure to preserve a corrupt sidecar.
func (s *Store) BackupError() error { return s.backupError }

// LoadCause reports why the sidecar was rebuilt from corrupt bytes, if it was.
func (s *Store) LoadCause() *CorruptSidecarError { return s.loadCause }

func (s *Store) load() error {
	s.state = NewStorageState(s.identity)
	s.loadCause = nil
	s.backupError = nil
	data, err := os.ReadFile(s.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		s.status = SidecarRebuilt
		return nil
	case err != nil:
		return fmt.Errorf("read storage sidecar: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return s.rebuildFromCorrupt(fmt.Errorf("empty sidecar"))
	}
	var decoded StorageState
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&decoded); err != nil {
		return s.rebuildFromCorrupt(err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return s.rebuildFromCorrupt(fmt.Errorf("unexpected trailing JSON"))
	}
	if decoded.SchemaVersion > SidecarSchemaVersion {
		s.status = SidecarReportOnly
		s.state = decoded
		return nil
	}
	if decoded.RootIdentity != "" && decoded.RootIdentity != s.identity {
		s.status = SidecarReportOnly
		s.state = decoded
		return nil
	}
	if decoded.Resources == nil {
		decoded.Resources = map[string]PhysicalResource{}
	}
	for id, resource := range decoded.Resources {
		if !resource.Kind.Valid() || !resource.CleanupState.Valid() {
			return s.rebuildFromCorrupt(fmt.Errorf("resource %q has invalid kind or cleanup state", id))
		}
	}
	s.state = decoded
	s.status = SidecarReady
	return nil
}

func (s *Store) rebuildFromCorrupt(cause error) error {
	s.state = NewStorageState(s.identity)
	s.status = SidecarRebuilt
	s.loadCause = &CorruptSidecarError{Path: s.path, Err: cause}
	if _, err := s.backupCorruptCopy(); err != nil {
		s.backupError = err
	}
	return nil
}

func (s *Store) backupCorruptCopy() (string, error) {
	data, err := os.ReadFile(s.path)
	if err != nil || len(bytes.TrimSpace(data)) == 0 {
		return "", err
	}
	backupDir := filepath.Join(s.dir, backupDirName)
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return "", err
	}
	// Stable name: a persistently corrupt sidecar must not grow backups
	// unboundedly across passes.
	backup := filepath.Join(backupDir, "sidecar-corrupt-latest.json")
	if err := state.WriteAtomic(backup, data); err != nil {
		return "", err
	}
	return backup, nil
}

// Update runs one locked read-modify-write cycle on the sidecar. It fails on
// report-only stores so a foreign or newer sidecar is never rewritten.
func (s *Store) Update(mutate func(*StorageState) error) error {
	if mutate == nil {
		return fmt.Errorf("storage sidecar update callback is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("storage sidecar store is closed")
	}
	if s.status == SidecarReportOnly {
		return ErrReportOnly
	}
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	// Re-load inside the lock so interleaved writers never lose updates.
	if err := s.load(); err != nil {
		return err
	}
	if s.status == SidecarReportOnly {
		return ErrReportOnly
	}
	next := cloneStorageState(s.state)
	if err := mutate(&next); err != nil {
		return err
	}
	if next.SchemaVersion != SidecarSchemaVersion {
		return fmt.Errorf("storage sidecar schema version must stay %d", SidecarSchemaVersion)
	}
	if next.RootIdentity != s.identity {
		return fmt.Errorf("storage sidecar root identity must stay bound to this root")
	}
	if next.Resources == nil {
		next.Resources = map[string]PhysicalResource{}
	}
	next.UpdatedAt = s.now()
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := state.WriteAtomic(s.path, data); err != nil {
		return err
	}
	s.state = next
	s.status = SidecarReady
	return nil
}

func (s *Store) lock() (func(), error) {
	file, err := os.OpenFile(s.lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open storage sidecar lock: %w", err)
	}
	if err := flockExclusive(file); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock storage sidecar: %w", err)
	}
	return func() {
		_ = flockUnlock(file)
		_ = file.Close()
	}, nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func cloneStorageState(st StorageState) StorageState {
	clone := st
	clone.Resources = make(map[string]PhysicalResource, len(st.Resources))
	for id, resource := range st.Resources {
		clone.Resources[id] = resource
	}
	return clone
}
