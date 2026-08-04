package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ErrStoragePressure reports that free space stayed below the configured
// minimum even after a pressured reconciliation pass. Dispatch is delayed,
// never failed, and queued work retries on the existing poll cadence.
var ErrStoragePressure = errors.New("storage free space below configured minimum")

// StatfsFunc returns live free bytes for a path's filesystem.
type StatfsFunc func(path string) (uint64, error)

// ServiceConfig wires the runner-facing storage service: statfs admission,
// dispatch-time resource recording, and shared reconciliation.
type ServiceConfig struct {
	WorkspaceRoot string
	StateLoader   StateLoader
	PoolInspector PoolInspector
	PoolRemover   PoolRemover
	// RawStatePath enables the first-migration backup gate and legacy evidence.
	RawStatePath string
	MinFreeBytes int64
	OrphanGrace  time.Duration
	Statfs       StatfsFunc
	Now          func() time.Time
	AttemptID    func() (string, error)
	// PressureCooldown throttles pressured reconciles (default 30s);
	// PoolInspectBackoff throttles per-pool git inspections (default 15m).
	PressureCooldown   time.Duration
	PoolInspectBackoff time.Duration
}

// Service is the dispatcher/reconcile facade over the storage engine. It is
// shared across the async dispatch and poll-cycle goroutines of one runner.
type Service struct {
	mu            sync.Mutex
	root          string
	store         *Store
	stateLoader   StateLoader
	poolInspector PoolInspector
	poolRemover   PoolRemover
	rawStatePath  string
	minFreeBytes  int64
	orphanGrace   time.Duration
	statfs        StatfsFunc
	now           func() time.Time
	attemptID     func() (string, error)

	evidenceLoaded bool
	evidence       map[string][]string
	// pressured passes cool down so sustained pressure cannot multiply a full
	// reconciliation per queued job per cycle.
	lastPressureReconcile time.Time
	pressureCooldown      time.Duration
	// pool inspections back off per pool so a preserved pool is not re-forked
	// through git every pass.
	poolInspectNext    map[string]time.Time
	poolInspectBackoff time.Duration
}

// NewService opens the sidecar and validates the configuration.
func NewService(cfg ServiceConfig) (*Service, error) {
	if cfg.StateLoader == nil {
		return nil, fmt.Errorf("state loader is required")
	}
	if cfg.MinFreeBytes < 0 {
		return nil, fmt.Errorf("storage min free bytes must not be negative")
	}
	if cfg.OrphanGrace < 0 {
		return nil, fmt.Errorf("storage orphan grace must not be negative")
	}
	root, err := Canonicalize(cfg.WorkspaceRoot)
	if err != nil {
		return nil, err
	}
	store, err := OpenStore(root)
	if err != nil {
		return nil, err
	}
	svc := &Service{
		root:          root,
		store:         store,
		stateLoader:   cfg.StateLoader,
		poolInspector: cfg.PoolInspector,
		poolRemover:   cfg.PoolRemover,
		rawStatePath:  cfg.RawStatePath,
		minFreeBytes:  cfg.MinFreeBytes,
		orphanGrace:   cfg.OrphanGrace,
		statfs:        cfg.Statfs,
		now:           cfg.Now,
		attemptID:     cfg.AttemptID,
	}
	if svc.statfs == nil {
		svc.statfs = statfsFreeBytes
	}
	if svc.now == nil {
		svc.now = func() time.Time { return time.Now().UTC() }
	}
	if svc.pressureCooldown <= 0 {
		svc.pressureCooldown = 30 * time.Second
	}
	if svc.poolInspectBackoff <= 0 {
		svc.poolInspectBackoff = 15 * time.Minute
	}
	svc.poolInspectNext = map[string]time.Time{}
	return svc, nil
}

// Root returns the canonical workspace root this service is bound to.
func (s *Service) Root() string { return s.root }

// Store exposes the sidecar for lifecycle tests and close management.
func (s *Service) Store() *Store { return s.store }

// Close releases the sidecar.
func (s *Service) Close() error { return s.store.Close() }

// AdmitDispatch performs statfs-only minimum-free-space admission before any
// session/workspace lock is acquired. Below threshold it runs one locked safe
// reconciliation and re-reads statfs; still-below delays dispatch with
// ErrStoragePressure. A statfs failure under a configured threshold fails
// closed. No recursive accounting runs on the pass path.
func (s *Service) AdmitDispatch(ctx context.Context) error {
	if s.minFreeBytes <= 0 {
		return nil
	}
	free, err := s.statfs(s.root)
	if err != nil {
		return fmt.Errorf("storage admission statfs: %w", err)
	}
	if free >= uint64(s.minFreeBytes) {
		return nil
	}
	s.mu.Lock()
	cooling := !s.lastPressureReconcile.IsZero() && s.now().Before(s.lastPressureReconcile.Add(s.pressureCooldown))
	s.mu.Unlock()
	if cooling {
		return fmt.Errorf("%w: %d bytes free of %d required; cleanup cooling down", ErrStoragePressure, free, s.minFreeBytes)
	}
	if _, err := s.ReconcileStorage(ctx, true, false); err != nil {
		return fmt.Errorf("storage pressured reconciliation: %w", err)
	}
	s.mu.Lock()
	s.lastPressureReconcile = s.now()
	s.mu.Unlock()
	free, err = s.statfs(s.root)
	if err != nil {
		return fmt.Errorf("storage admission statfs recheck: %w", err)
	}
	if free < uint64(s.minFreeBytes) {
		return fmt.Errorf("%w: %d bytes free of %d required after cleanup", ErrStoragePressure, free, s.minFreeBytes)
	}
	return nil
}

// RecordSessionResources upserts the exact runtime and PROCESS pool physical
// identities for a session before the runtime is exposed to sandbox execution.
// It is fail-closed: an upsert failure means the runtime would be unmanaged.
func (s *Service) RecordSessionResources(_ context.Context, repo, publicSessionID, workspacePath string) error {
	repo = strings.TrimSpace(repo)
	publicSessionID = strings.TrimSpace(publicSessionID)
	workspacePath = strings.TrimSpace(workspacePath)
	if repo == "" || publicSessionID == "" || workspacePath == "" {
		return fmt.Errorf("repo, public session id, and workspace path are required for storage recording")
	}
	runtimeHash, err := SessionRuntimeHash(repo, publicSessionID, workspacePath)
	if err != nil {
		return err
	}
	canonical, err := Canonicalize(workspacePath)
	if err != nil {
		return fmt.Errorf("canonicalize session workspace for storage recording: %w", err)
	}
	poolHash, err := SessionProcessPoolHash(repo, publicSessionID, canonical)
	if err != nil {
		return err
	}
	now := s.now().UTC()
	// Skip the write entirely when both records already match: steady-state
	// touches must not fsync the sidecar per running job per pass.
	desired := func(existing PhysicalResource, id string, kind ResourceKind, hash, path string) PhysicalResource {
		record := existing
		if record.ID == "" {
			record = PhysicalResource{ID: id, Kind: kind, FirstObservedAt: now}
		}
		record.Path = path
		record.Repo = repo
		record.PublicSessionID = publicSessionID
		record.PhysicalHash = hash
		record.CleanupState = CleanupManaged
		record.CleanupAttemptID = ""
		record.LastCleanupError = ""
		return record
	}
	current := s.store.State()
	runtimeID := ResourceID(ResourceKindSessionRuntime, repo, publicSessionID, runtimeHash)
	poolID := ResourceID(ResourceKindSessionProcessPool, repo, publicSessionID, poolHash)
	if current.Resources[runtimeID] == desired(current.Resources[runtimeID], runtimeID, ResourceKindSessionRuntime, runtimeHash, RuntimeRootForHash(s.root, runtimeHash)) &&
		current.Resources[poolID] == desired(current.Resources[poolID], poolID, ResourceKindSessionProcessPool, poolHash, ProcessPoolRootForHash(s.root, poolHash)) {
		return nil
	}
	upsert := func(st *StorageState) error {
		for _, spec := range []struct {
			kind ResourceKind
			hash string
			path string
		}{
			{ResourceKindSessionRuntime, runtimeHash, RuntimeRootForHash(s.root, runtimeHash)},
			{ResourceKindSessionProcessPool, poolHash, ProcessPoolRootForHash(s.root, poolHash)},
		} {
			id := ResourceID(spec.kind, repo, publicSessionID, spec.hash)
			record := st.Resources[id]
			if record.ID == "" {
				record = PhysicalResource{ID: id, Kind: spec.kind, FirstObservedAt: now}
			}
			record.Path = spec.path
			record.Repo = repo
			record.PublicSessionID = publicSessionID
			record.PhysicalHash = spec.hash
			record.CleanupState = CleanupManaged
			record.CleanupAttemptID = ""
			record.LastCleanupError = ""
			st.Resources[id] = record
		}
		return nil
	}
	if err := s.store.Update(upsert); err != nil {
		return fmt.Errorf("record session storage resources: %w", err)
	}
	return nil
}

// ReconcileStorage runs the shared engine under the canonical root owner:
// every destructive storage pass proves ownership first, so an admission
// cleanup inside a standalone dispatch can never bypass the owner lock.
// Operator runs may measure every class; automatic poll passes only measure
// deletion targets.
func (s *Service) ReconcileStorage(ctx context.Context, apply, measureAll bool) (Report, error) {
	_, release, err := EnsureOwner(ctx, s.root)
	if err != nil {
		return Report{}, fmt.Errorf("storage reconcile owner: %w", err)
	}
	defer release()
	evidence := s.loadLegacyEvidence()
	engine, err := NewEngine(EngineConfig{
		WorkspaceRoot:          s.root,
		Store:                  s.store,
		StateLoader:            s.stateLoader,
		PoolInspector:          s.poolInspector,
		PoolRemover:            s.poolRemover,
		LegacyEvidence:         evidence,
		RawStatePath:           s.rawStatePath,
		RequireMigrationBackup: s.rawStatePath != "",
		Now:                    s.now,
		AttemptID:              s.attemptID,
		PoolInspectGate:        s.poolInspectGate,
	})
	if err != nil {
		return Report{}, err
	}
	return engine.Reconcile(ctx, ReconcileOptions{Apply: apply, OrphanGrace: s.orphanGrace, MeasureAll: measureAll})
}

// poolInspectGate throttles per-pool git inspections: each inspection of a
// preserved pool schedules its next allowed attempt one backoff out.
func (s *Service) poolInspectGate(id string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	next, ok := s.poolInspectNext[id]
	if ok && now.Before(next) {
		return false
	}
	s.poolInspectNext[id] = now.Add(s.poolInspectBackoff)
	return true
}

// loadLegacyEvidence caches the immutable first-migration backup parse;
// before the backup exists it retries so a mid-process first migration still
// gains evidence on the following pass. Corrupt evidence never broadens
// deletion.
func (s *Service) loadLegacyEvidence() map[string][]string {
	s.mu.Lock()
	if s.evidenceLoaded {
		defer s.mu.Unlock()
		return s.evidence
	}
	s.mu.Unlock()
	backup := filepath.Join(s.root, StorageDirName, backupDirName, rawStateBackupName)
	if _, err := os.Lstat(backup); err != nil {
		return nil
	}
	evidence, err := LoadLegacyEvidence(backup)
	if err != nil {
		return nil
	}
	s.mu.Lock()
	s.evidence = evidence
	s.evidenceLoaded = true
	s.mu.Unlock()
	return evidence
}
