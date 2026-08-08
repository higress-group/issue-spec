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

// RuntimeSchemaVersion is the current `.storage/runtime.json` schema. A newer
// on-disk schema or a foreign root identity permits report-only inventory,
// exactly like the sidecar. The runtime metadata lives in its own file so a
// v1 binary never parses records it cannot classify.
const RuntimeSchemaVersion = 1

const (
	runtimeFileName      = "runtime.json"
	runtimeLockName      = "runtime.json.lock"
	runtimeCorruptBackup = "runtime-corrupt-latest.json"
)

// MigrationState is the ledger lifecycle of one scope's legacy-home import:
// imported -> validated -> retired. Transitions are monotonic.
type MigrationState string

const (
	MigrationImported  MigrationState = "imported"
	MigrationValidated MigrationState = "validated"
	MigrationRetired   MigrationState = "retired"
)

func (s MigrationState) Valid() bool {
	switch s {
	case MigrationImported, MigrationValidated, MigrationRetired:
		return true
	default:
		return false
	}
}

// migrationStateRank orders ledger states; regressions are rejected.
func migrationStateRank(s MigrationState) int {
	switch s {
	case MigrationImported:
		return 1
	case MigrationValidated:
		return 2
	case MigrationRetired:
		return 3
	default:
		return 0
	}
}

// RuntimeHomeRecord pins one scope hash to its prepared runtime home.
type RuntimeHomeRecord struct {
	Hash      string    `json:"hash"`
	Path      string    `json:"path"`
	Hostname  string    `json:"hostname"`
	Realm     string    `json:"realm,omitempty"`
	Repo      string    `json:"repo"`
	Runner    string    `json:"runner"`
	CreatedAt time.Time `json:"created_at,omitempty"`
}

// JobScratchRecord tracks one job's scratch directory through the same
// managed/deleting/removed cleanup lifecycle as sidecar resources.
type JobScratchRecord struct {
	JobID        string       `json:"job_id"`
	Path         string       `json:"path"`
	CreatedAt    time.Time    `json:"created_at,omitempty"`
	CleanupState CleanupState `json:"cleanup_state,omitempty"`
}

// MigrationRecord is the per-scope legacy-home migration ledger entry.
type MigrationRecord struct {
	ScopeHash        string         `json:"scope_hash"`
	State            MigrationState `json:"state"`
	ImportedSessions []string       `json:"imported_sessions,omitempty"`
	ValidatedSession string         `json:"validated_session,omitempty"`
	UpdatedAt        time.Time      `json:"updated_at,omitempty"`
}

// RuntimeState is the `.storage/runtime.json` document.
type RuntimeState struct {
	SchemaVersion int                          `json:"schema_version"`
	RootIdentity  string                       `json:"root_identity"`
	Homes         map[string]RuntimeHomeRecord `json:"homes,omitempty"`
	Scratch       map[string]JobScratchRecord  `json:"scratch,omitempty"`
	Migrations    map[string]MigrationRecord   `json:"migrations,omitempty"`
	UpdatedAt     time.Time                    `json:"updated_at,omitempty"`
}

// NewRuntimeState builds an empty runtime metadata state bound to the root.
func NewRuntimeState(rootIdentity string) RuntimeState {
	return RuntimeState{
		SchemaVersion: RuntimeSchemaVersion,
		RootIdentity:  rootIdentity,
		Homes:         map[string]RuntimeHomeRecord{},
		Scratch:       map[string]JobScratchRecord{},
		Migrations:    map[string]MigrationRecord{},
	}
}

// RuntimeStore is the locked, atomically persisted `.storage/runtime.json`
// metadata store. It mirrors the sidecar Store semantics: the runtime flock is
// a leaf-level lock held only for short load/mutate/save sections, missing or
// corrupt files open rebuilt, and foreign-root or newer-schema files open
// report-only. Corruption causes reuse the sidecar's CorruptSidecarError so
// callers handle one evidence-loss shape.
type RuntimeStore struct {
	mu          sync.Mutex
	dir         string
	path        string
	lockPath    string
	root        string
	identity    string
	state       RuntimeState
	status      SidecarStatus
	closed      bool
	now         func() time.Time
	backupError error
	loadCause   *CorruptSidecarError
}

// OpenRuntimeStore loads the runtime metadata for the canonical workspace
// root, creating the private `.storage` directory when needed.
func OpenRuntimeStore(workspaceRoot string) (*RuntimeStore, error) {
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
	store := &RuntimeStore{
		dir:      dir,
		path:     filepath.Join(dir, runtimeFileName),
		lockPath: filepath.Join(dir, runtimeLockName),
		root:     canonical,
		identity: identity,
		now:      func() time.Time { return time.Now().UTC() },
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

// Status reports how much trust this process may place in the loaded state.
func (s *RuntimeStore) Status() SidecarStatus { return s.status }

// RootIdentity returns the identity hash bound to this store.
func (s *RuntimeStore) RootIdentity() string { return s.identity }

// Path returns the runtime metadata file path.
func (s *RuntimeStore) Path() string { return s.path }

// BackupError reports a non-fatal failure to preserve a corrupt file.
func (s *RuntimeStore) BackupError() error { return s.backupError }

// LoadCause reports why the state was rebuilt from corrupt bytes, if it was.
func (s *RuntimeStore) LoadCause() *CorruptSidecarError { return s.loadCause }

// Reload re-reads the runtime metadata under its lock so a long-lived store
// observes writes made by other stores on the same root.
func (s *RuntimeStore) Reload() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("runtime metadata store is closed")
	}
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	return s.load()
}

// State returns a copy of the currently loaded runtime state.
func (s *RuntimeStore) State() RuntimeState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneRuntimeState(s.state)
}

func (s *RuntimeStore) load() error {
	s.state = NewRuntimeState(s.identity)
	s.loadCause = nil
	s.backupError = nil
	data, err := os.ReadFile(s.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		s.status = SidecarRebuilt
		return nil
	case err != nil:
		return fmt.Errorf("read runtime metadata: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return s.rebuildFromCorrupt(fmt.Errorf("empty runtime metadata"))
	}
	var decoded RuntimeState
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&decoded); err != nil {
		return s.rebuildFromCorrupt(err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return s.rebuildFromCorrupt(fmt.Errorf("unexpected trailing JSON"))
	}
	if decoded.SchemaVersion > RuntimeSchemaVersion {
		s.status = SidecarReportOnly
		s.state = normalizeRuntimeState(decoded)
		return nil
	}
	if decoded.RootIdentity != "" && decoded.RootIdentity != s.identity {
		s.status = SidecarReportOnly
		s.state = normalizeRuntimeState(decoded)
		return nil
	}
	decoded = normalizeRuntimeState(decoded)
	for id, record := range decoded.Scratch {
		if !record.CleanupState.Valid() {
			return s.rebuildFromCorrupt(fmt.Errorf("scratch record %q has invalid cleanup state", id))
		}
	}
	for hash, record := range decoded.Migrations {
		if !record.State.Valid() {
			return s.rebuildFromCorrupt(fmt.Errorf("migration record %q has invalid state", hash))
		}
	}
	s.state = decoded
	s.status = SidecarReady
	return nil
}

func (s *RuntimeStore) rebuildFromCorrupt(cause error) error {
	s.state = NewRuntimeState(s.identity)
	s.status = SidecarRebuilt
	s.loadCause = &CorruptSidecarError{Path: s.path, Err: cause}
	if _, err := s.backupCorruptCopy(); err != nil {
		s.backupError = err
	}
	return nil
}

func (s *RuntimeStore) backupCorruptCopy() (string, error) {
	data, err := os.ReadFile(s.path)
	if err != nil || len(bytes.TrimSpace(data)) == 0 {
		return "", err
	}
	backupDir := filepath.Join(s.dir, backupDirName)
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return "", err
	}
	// Stable name: a persistently corrupt file must not grow backups
	// unboundedly across passes.
	backup := filepath.Join(backupDir, runtimeCorruptBackup)
	if err := state.WriteAtomic(backup, data); err != nil {
		return "", err
	}
	return backup, nil
}

// Update runs one locked read-modify-write cycle on the runtime metadata. It
// fails on report-only stores so a foreign or newer file is never rewritten.
func (s *RuntimeStore) Update(mutate func(*RuntimeState) error) error {
	if mutate == nil {
		return fmt.Errorf("runtime metadata update callback is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("runtime metadata store is closed")
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
	next := cloneRuntimeState(s.state)
	if err := mutate(&next); err != nil {
		return err
	}
	if next.SchemaVersion != RuntimeSchemaVersion {
		return fmt.Errorf("runtime metadata schema version must stay %d", RuntimeSchemaVersion)
	}
	if next.RootIdentity != s.identity {
		return fmt.Errorf("runtime metadata root identity must stay bound to this root")
	}
	next = normalizeRuntimeState(next)
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

func (s *RuntimeStore) lock() (func(), error) {
	file, err := os.OpenFile(s.lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open runtime metadata lock: %w", err)
	}
	if err := flockExclusive(file); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock runtime metadata: %w", err)
	}
	return func() {
		_ = flockUnlock(file)
		_ = file.Close()
	}, nil
}

// Close releases the store handle; the flock itself is never held between
// operations, so close only gates further use.
func (s *RuntimeStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func normalizeRuntimeState(st RuntimeState) RuntimeState {
	if st.Homes == nil {
		st.Homes = map[string]RuntimeHomeRecord{}
	}
	if st.Scratch == nil {
		st.Scratch = map[string]JobScratchRecord{}
	}
	if st.Migrations == nil {
		st.Migrations = map[string]MigrationRecord{}
	}
	return st
}

func cloneRuntimeState(st RuntimeState) RuntimeState {
	clone := st
	clone.Homes = make(map[string]RuntimeHomeRecord, len(st.Homes))
	for hash, record := range st.Homes {
		clone.Homes[hash] = record
	}
	clone.Scratch = make(map[string]JobScratchRecord, len(st.Scratch))
	for id, record := range st.Scratch {
		clone.Scratch[id] = record
	}
	clone.Migrations = make(map[string]MigrationRecord, len(st.Migrations))
	for hash, record := range st.Migrations {
		record.ImportedSessions = append([]string(nil), record.ImportedSessions...)
		clone.Migrations[hash] = record
	}
	return clone
}
