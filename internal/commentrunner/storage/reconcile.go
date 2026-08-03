package storage

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/workspace"
)

// Class is the reconciliation classification of one physical resource.
type Class string

const (
	ClassProtected      Class = "protected"
	ClassRetiredKnown   Class = "retired_known"
	ClassOrphanObserved Class = "orphan_observed"
	ClassRejected       Class = "rejected"
)

// Action is what reconciliation did (or would do) about one resource.
type Action string

const (
	ActionKept        Action = "kept"
	ActionDeleted     Action = "deleted"
	ActionWouldDelete Action = "would_delete"
	ActionObserved    Action = "observed"
	ActionPreserved   Action = "preserved"
	ActionFailed      Action = "failed"
	ActionRejected    Action = "rejected"
)

// ResourceReport is the private, bounded per-resource outcome. It carries
// identity, class, action, safe reason, attempt id, and measured bytes; it
// never contains runtime contents, credentials, or environment values.
type ResourceReport struct {
	ID              string       `json:"id"`
	Kind            ResourceKind `json:"kind"`
	Class           Class        `json:"class"`
	Action          Action       `json:"action"`
	Reason          string       `json:"reason,omitempty"`
	Hash            string       `json:"hash,omitempty"`
	Repo            string       `json:"repo,omitempty"`
	PublicSessionID string       `json:"public_session_id,omitempty"`
	Bytes           int64        `json:"bytes,omitempty"`
	AttemptID       string       `json:"attempt_id,omitempty"`
}

// Report summarizes one reconciliation pass.
type Report struct {
	RootIdentity         string           `json:"root_identity"`
	DryRun               bool             `json:"dry_run"`
	ReportOnly           bool             `json:"report_only,omitempty"`
	SidecarStatus        SidecarStatus    `json:"sidecar_status"`
	Resources            []ResourceReport `json:"resources,omitempty"`
	Diagnostics          []string         `json:"diagnostics,omitempty"`
	ReclaimedBytes       int64            `json:"reclaimed_bytes,omitempty"`
	DeferredWorkspaceIDs []string         `json:"deferred_workspace_ids,omitempty"`
}

// CountByClass tallies resources per classification.
func (r Report) CountByClass(class Class) int {
	count := 0
	for _, resource := range r.Resources {
		if resource.Class == class {
			count++
		}
	}
	return count
}

// BytesByClass sums measured bytes per classification.
func (r Report) BytesByClass(class Class) int64 {
	var total int64
	for _, resource := range r.Resources {
		if resource.Class == class {
			total += resource.Bytes
		}
	}
	return total
}

// StateLoader reloads current runner state (post-compaction) on demand.
type StateLoader func(context.Context) (state.RunnerState, error)

// PoolInspection is the process-workspace safety proof for one session pool.
type PoolInspection struct {
	ClonePresent        bool     `json:"clone_present"`
	RegistryComplete    bool     `json:"registry_complete"`
	ActiveLeases        int      `json:"active_leases"`
	OwnershipMarkers    int      `json:"ownership_markers"`
	RegisteredWorktrees int      `json:"registered_worktrees"`
	FilesystemEntries   int      `json:"filesystem_entries"`
	Problems            []string `json:"problems,omitempty"`
}

// ProvenEmpty is true only when inspection proves the pool has no active or
// owned worktree evidence and the owning clone is present.
func (p PoolInspection) ProvenEmpty() bool {
	return p.ClonePresent && p.RegistryComplete && p.ActiveLeases == 0 &&
		p.OwnershipMarkers == 0 && p.RegisteredWorktrees == 0 && p.FilesystemEntries == 0 &&
		len(p.Problems) == 0
}

// PoolInspector inspects one session pool without bypassing process-workspace
// lifecycle ownership.
type PoolInspector func(ctx context.Context, integrationRoot, poolRoot string) (PoolInspection, error)

// PoolRemover proves and removes one empty PROCESS pool while holding the
// processworkspace integration lifecycle lock. The returned inspection
// explains a conservative non-removal.
type PoolRemover func(ctx context.Context, integrationRoot, poolRoot string) (PoolInspection, bool, error)

// EngineConfig wires the shared reconciliation engine used by startup,
// periodic, async-busy, pressured, and explicit operator reconciliation.
type EngineConfig struct {
	WorkspaceRoot string
	Store         *Store
	StateLoader   StateLoader
	PoolInspector PoolInspector
	PoolRemover   PoolRemover
	// LegacyEvidence supplies raw pre-Normalize Acpx.CWD candidates per session
	// key, captured from a state backup made before any current-binary save.
	LegacyEvidence map[string][]string
	// RawStatePath is the live runner state file used for the first-migration
	// raw backup. RequireMigrationBackup gates the first applied migration's
	// deletions on a successful backup (D7/D12); later passes are unaffected.
	RawStatePath           string
	RequireMigrationBackup bool
	Now                    func() time.Time
	AttemptID              func() (string, error)
	// PoolInspectGate optionally throttles per-pool git inspections; nil
	// inspects every eligible pool every pass.
	PoolInspectGate func(resourceID string, now time.Time) bool
}

// Engine reconciles physical storage resources against current state, the
// sidecar, and the filesystem. It never acquires session workspace locks and
// never writes control-plane state.
type Engine struct {
	WorkspaceRoot          string
	Store                  *Store
	StateLoader            StateLoader
	PoolInspector          PoolInspector
	PoolRemover            PoolRemover
	LegacyEvidence         map[string][]string
	RawStatePath           string
	RequireMigrationBackup bool
	now                    func() time.Time
	attemptID              func() (string, error)
	poolInspectGate        func(resourceID string, now time.Time) bool
	ownStore               bool
	// prePersistHook is a test-only seam invoked immediately before phase-one
	// persistence so concurrent writer interleavings are deterministic.
	prePersistHook func()
	// preRemoveHook is a test-only seam invoked after final validation and
	// immediately before the capability-scoped remover.
	preRemoveHook func()
}

// ReconcileOptions controls one pass.
type ReconcileOptions struct {
	Apply       bool
	OrphanGrace time.Duration
	// MeasureAll measures every inventoried resource (explicit operator runs).
	// Normal poll reconciliation only measures deletion targets.
	MeasureAll bool
}

var engineLocks = struct {
	sync.Mutex
	byRoot map[string]*sync.Mutex
}{byRoot: map[string]*sync.Mutex{}}

// NewEngine validates and canonicalizes the configuration.
func NewEngine(cfg EngineConfig) (*Engine, error) {
	if strings.TrimSpace(cfg.WorkspaceRoot) == "" {
		return nil, fmt.Errorf("workspace root is required")
	}
	root, err := Canonicalize(cfg.WorkspaceRoot)
	if err != nil {
		return nil, err
	}
	if cfg.StateLoader == nil {
		return nil, fmt.Errorf("state loader is required")
	}
	engine := &Engine{
		WorkspaceRoot:          root,
		Store:                  cfg.Store,
		StateLoader:            cfg.StateLoader,
		PoolInspector:          cfg.PoolInspector,
		PoolRemover:            cfg.PoolRemover,
		LegacyEvidence:         cfg.LegacyEvidence,
		RawStatePath:           cfg.RawStatePath,
		RequireMigrationBackup: cfg.RequireMigrationBackup,
		now:                    cfg.Now,
		attemptID:              cfg.AttemptID,
		poolInspectGate:        cfg.PoolInspectGate,
	}
	if engine.now == nil {
		engine.now = func() time.Time { return time.Now().UTC() }
	}
	if engine.attemptID == nil {
		engine.attemptID = randomAttemptID
	}
	if err := engine.openStore(); err != nil {
		return nil, err
	}
	return engine, nil
}

func randomAttemptID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "attempt-" + hex.EncodeToString(b), nil
}

func (e *Engine) openStore() error {
	if e.Store != nil {
		return nil
	}
	store, err := OpenStore(e.WorkspaceRoot)
	if err != nil {
		return err
	}
	e.Store = store
	e.ownStore = true
	return nil
}

// Close releases an engine-owned sidecar store.
func (e *Engine) Close() error {
	if e.ownStore && e.Store != nil {
		return e.Store.Close()
	}
	return nil
}

// Reconcile runs one classification and optional deletion pass. The engine
// mutex serializes in-process passes per root; the caller holds the owner
// lock and the state flock.
func (e *Engine) Reconcile(ctx context.Context, opts ReconcileOptions) (Report, error) {
	if opts.OrphanGrace < 0 {
		return Report{}, fmt.Errorf("orphan grace must not be negative")
	}
	engineLocks.Lock()
	mu := engineLocks.byRoot[e.WorkspaceRoot]
	if mu == nil {
		mu = &sync.Mutex{}
		engineLocks.byRoot[e.WorkspaceRoot] = mu
	}
	engineLocks.Unlock()
	mu.Lock()
	defer mu.Unlock()

	if err := e.openStore(); err != nil {
		return Report{}, err
	}
	if err := e.Store.Reload(); err != nil {
		return Report{}, fmt.Errorf("reload storage sidecar: %w", err)
	}
	report := Report{
		RootIdentity:  e.Store.RootIdentity(),
		DryRun:        !opts.Apply,
		SidecarStatus: e.Store.Status(),
	}
	reportOnly := e.Store.Status() == SidecarReportOnly
	if reportOnly {
		report.ReportOnly = true
		report.Diagnostics = append(report.Diagnostics, "storage sidecar is report-only: foreign root identity or newer schema; inventory only, no mutations")
	}
	if cause := e.Store.LoadCause(); cause != nil {
		report.Diagnostics = append(report.Diagnostics, safeDiagnostic("storage sidecar rebuilt after corruption ("+cause.Err.Error()+"); ownership and orphan observation proof restarted"))
	}
	if backupErr := e.Store.BackupError(); backupErr != nil {
		report.Diagnostics = append(report.Diagnostics, safeDiagnostic("corrupt sidecar backup failed: "+backupErr.Error()))
	}
	st, err := e.StateLoader(ctx)
	if err != nil {
		return report, fmt.Errorf("load runner state for storage reconciliation: %w", err)
	}
	st.Normalize()
	view := buildProtectionView(st)
	now := e.now().UTC()

	updated := map[string]PhysicalResource{}
	sidecar := e.Store.State()
	for id, resource := range sidecar.Resources {
		updated[id] = resource
	}
	swept := map[string]PhysicalResource{}
	report = e.sweepStaleEntries(report, updated, swept, opts.Apply && !reportOnly)

	entries, rejected := e.inventory()
	report.Resources = append(report.Resources, rejected...)

	pending := map[string]inventoryEntry{}
	for _, entry := range entries {
		result, record := e.classifyEntry(view, sidecar, entry, now, opts)
		updated[record.ID] = record
		pending[record.ID] = entry
		report.Resources = append(report.Resources, result)
	}

	// Persist phase-1 classification before any deletion so every
	// transaction compare-and-swap observes the current records. Merge into
	// the fresh sidecar: concurrent RecordSessionResources upserts landing
	// between the reload and this write must survive, and swept records are
	// deleted only when no concurrent writer touched them.
	if opts.Apply && !reportOnly {
		if e.prePersistHook != nil {
			e.prePersistHook()
		}
		if err := e.Store.Update(func(st *StorageState) error {
			for id, prior := range swept {
				if current, ok := st.Resources[id]; ok && current == prior {
					delete(st.Resources, id)
				}
			}
			for id, resource := range updated {
				st.Resources[id] = resource
			}
			return nil
		}); err != nil {
			return report, fmt.Errorf("persist storage sidecar: %w", err)
		}
	}

	// First applied migration: the first destructive pass on this root
	// requires a successful raw state and prior sidecar backup, whenever it
	// occurs. Backup failure blocks only this pass's deletions.
	backupBlocked := false
	if opts.Apply && !reportOnly {
		hasDeletions := false
		for _, resource := range report.Resources {
			if e.intendsDeletion(resource, opts) {
				hasDeletions = true
				break
			}
		}
		if hasDeletions && e.migrationBackupNeeded() {
			if err := e.migrationBackup(); err != nil {
				backupBlocked = true
				report.Diagnostics = append(report.Diagnostics, safeDiagnostic("first applied migration backup failed: "+err.Error()+"; skipping deletions"))
			}
		}
	}

	// Phase 2: deletion transactions. Each transaction re-locks the resource,
	// reloads state, and re-validates every condition before mutating.
	if opts.Apply && !reportOnly {
		for i := range report.Resources {
			result := &report.Resources[i]
			if !e.intendsDeletion(*result, opts) {
				continue
			}
			if backupBlocked {
				result.Action = ActionKept
				result.Reason = "migration backup unavailable"
				continue
			}
			entry, ok := pending[result.ID]
			if !ok {
				continue
			}
			if entry.kind == ResourceKindSessionProcessPool && e.poolInspectGate != nil && !e.poolInspectGate(result.ID, now) {
				result.Action = ActionPreserved
				result.Reason = "pool inspection backoff; preserved this pass"
				continue
			}
			e.deleteResource(ctx, result, entry, opts, &report)
		}
	} else {
		for i := range report.Resources {
			if e.intendsDeletion(report.Resources[i], opts) {
				report.Resources[i].Action = ActionWouldDelete
			}
		}
	}

	sort.Slice(report.Resources, func(i, j int) bool { return report.Resources[i].ID < report.Resources[j].ID })
	return report, nil
}

// intendsDeletion reports whether a classified resource is deletion-eligible
// this pass: retired-known resources always; orphan-observed runtimes after
// the grace window. Orphan pools are never force-abandoned.
func (e *Engine) intendsDeletion(result ResourceReport, opts ReconcileOptions) bool {
	switch result.Class {
	case ClassRetiredKnown:
		return true
	case ClassOrphanObserved:
		return result.Kind == ResourceKindSessionRuntime && (result.Action == ActionDeleted || result.Action == ActionWouldDelete)
	default:
		return false
	}
}

// sweepStaleEntries finalizes interrupted deletions and drops records whose
// paths vanished outside reconciliation. Applied sweeps record the dropped
// entries so phase-one persistence can delete them compare-and-swap style.
func (e *Engine) sweepStaleEntries(report Report, updated, swept map[string]PhysicalResource, apply bool) Report {
	for id, resource := range updated {
		_, statErr := os.Lstat(resource.Path)
		if statErr == nil {
			continue
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			report.Diagnostics = append(report.Diagnostics, safeDiagnostic("storage inventory lstat failed for "+resource.ID))
			continue
		}
		switch resource.CleanupState {
		case CleanupDeleting:
			report.Resources = append(report.Resources, ResourceReport{
				ID: id, Kind: resource.Kind, Class: classFromCleanup(resource.CleanupState),
				Action: ActionDeleted, Reason: "interrupted deletion completed; path absent",
				Hash: resource.PhysicalHash, Repo: resource.Repo, PublicSessionID: resource.PublicSessionID,
				AttemptID: resource.CleanupAttemptID,
			})
		case CleanupRemoved:
			// Confirmed gone: garbage-collect the tombstone.
		default:
			report.Diagnostics = append(report.Diagnostics, safeDiagnostic("storage resource "+id+" vanished outside reconciliation; dropping record"))
		}
		if apply {
			swept[id] = resource
			delete(updated, id)
		}
	}
	return report
}

type inventoryEntry struct {
	kind ResourceKind
	hash string
	path string
}

func (e *Engine) inventory() ([]inventoryEntry, []ResourceReport) {
	var entries []inventoryEntry
	var rejected []ResourceReport
	for _, spec := range []struct {
		dir  string
		kind ResourceKind
	}{
		{SessionsDirName, ResourceKindSessionRuntime},
		{ProcessPoolsDirName, ResourceKindSessionProcessPool},
	} {
		base := filepath.Join(e.WorkspaceRoot, spec.dir)
		names, err := readDirNames(base)
		if err != nil {
			continue
		}
		for _, name := range names {
			path, err := ValidateInventoryEntry(e.WorkspaceRoot, spec.dir, name)
			if err != nil {
				rejected = append(rejected, ResourceReport{
					ID: ResourceID(spec.kind, "", "", name), Kind: spec.kind, Class: ClassRejected,
					Action: ActionRejected, Reason: boundReason(err.Error()), Hash: name,
				})
				continue
			}
			entries = append(entries, inventoryEntry{kind: spec.kind, hash: name, path: path})
		}
	}
	return entries, rejected
}

func readDirNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names, nil
}

// classification is the engine's per-entry decision before any deletion.
type classification struct {
	class  Class
	reason string
	repo   string
	sid    string
	owned  bool
	wsID   string
	wsPath string
}

func (e *Engine) classifyEntry(view *protectionView, sidecar StorageState, entry inventoryEntry, now time.Time, opts ReconcileOptions) (ResourceReport, PhysicalResource) {
	decision := e.decide(view, sidecar, entry)
	id := ResourceID(entry.kind, decision.repo, decision.sid, entry.hash)
	if !decision.owned {
		id = ResourceID(entry.kind, "", "", entry.hash)
	}
	prior := sidecar.Resources[id]
	report := ResourceReport{
		ID:              id,
		Kind:            entry.kind,
		Class:           decision.class,
		Hash:            entry.hash,
		Repo:            decision.repo,
		PublicSessionID: decision.sid,
	}
	record := PhysicalResource{
		ID:              id,
		Kind:            entry.kind,
		Path:            entry.path,
		PhysicalHash:    entry.hash,
		FirstObservedAt: prior.FirstObservedAt,
	}
	if record.FirstObservedAt.IsZero() {
		record.FirstObservedAt = now
	}
	if decision.owned {
		record.Repo = decision.repo
		record.PublicSessionID = decision.sid
		record.WorkspaceID = decision.wsID
	}

	switch decision.class {
	case ClassProtected:
		report.Action = ActionKept
		report.Reason = decision.reason
		record.CleanupState = CleanupManaged
	case ClassRetiredKnown:
		report.Action = ActionKept
		report.Reason = decision.reason
		record.CleanupState = CleanupRetiredKnown
	case ClassOrphanObserved:
		record.CleanupState = CleanupOrphanObserved
		observedAt := record.FirstObservedAt
		graceElapsed := !prior.FirstObservedAt.IsZero() && !now.Before(observedAt.Add(opts.OrphanGrace))
		switch {
		case graceElapsed && entry.kind == ResourceKindSessionRuntime:
			report.Action = ActionWouldDelete
			report.Reason = "orphan grace elapsed"
		case graceElapsed:
			report.Action = ActionPreserved
			report.Reason = "orphan grace elapsed but pool ownership is unproven; preserved for operator remediation"
		default:
			report.Action = ActionObserved
			report.Reason = "unmatched resource under orphan observation"
		}
	}
	if opts.MeasureAll {
		report.Bytes = measureTreeBytes(entry.path)
	}
	return report, record
}

// decide maps one inventory entry to its classification using only current
// state, prior sidecar ownership, raw legacy evidence, and lstat checks.
func (e *Engine) decide(view *protectionView, sidecar StorageState, entry inventoryEntry) classification {
	mapped, uncertain := e.mapEntry(view, entry)
	if uncertain != "" {
		return classification{class: ClassProtected, reason: uncertain}
	}
	if mapped != nil {
		base := classification{repo: mapped.Repo, sid: mapped.PublicSessionID, owned: true, wsID: mapped.Workspace.ID, wsPath: strings.TrimSpace(mapped.Workspace.Path)}
		if blocker := view.sessionProtected(mapped); blocker != "" {
			base.class, base.reason = ClassProtected, blocker
			return base
		}
		if base.wsPath == "" {
			base.class, base.reason = ClassProtected, "workspace identity cannot be established"
			return base
		}
		_, statErr := os.Lstat(base.wsPath)
		switch {
		case statErr == nil:
			if entry.kind == ResourceKindSessionRuntime {
				base.class, base.reason = ClassProtected, "session workspace present"
			} else {
				// A pool carries no /resume anchor: with the session terminal
				// and every active reference absent, inspection decides.
				base.class, base.reason = ClassRetiredKnown, "retained terminal session pool; owning clone present"
			}
			return base
		case errors.Is(statErr, os.ErrNotExist):
			base.class = ClassRetiredKnown
			if entry.kind == ResourceKindSessionRuntime {
				base.reason = "retained terminal session workspace confirmed missing; no active references"
			} else {
				base.reason = "retained terminal session pool; owning clone missing"
			}
			return base
		default:
			base.class, base.reason = ClassProtected, "session workspace lstat uncertain: "+boundReason(statErr.Error())
			return base
		}
	}

	// Unmatched by current sessions: prior sidecar ownership is proof.
	for _, prior := range sidecar.Resources {
		if prior.Kind != entry.kind || prior.PhysicalHash != entry.hash || !prior.Owned() {
			continue
		}
		base := classification{repo: prior.Repo, sid: prior.PublicSessionID, owned: true, wsID: prior.WorkspaceID}
		if blocker := view.refsProtected(prior.Repo, prior.PublicSessionID); blocker != "" {
			base.class, base.reason = ClassProtected, blocker
			return base
		}
		if _, stillRetained := view.sessions[sessionKeyOf(prior.Repo, prior.PublicSessionID)]; stillRetained {
			base.class, base.reason = ClassProtected, "owning session retained with changed identity"
			return base
		}
		base.class, base.reason = ClassRetiredKnown, "prior sidecar ownership proof; session pruned"
		return base
	}
	// Raw pre-Normalize legacy CWD evidence.
	if proof := e.matchLegacyEvidence(view, entry); proof != nil {
		return *proof
	}
	return classification{class: ClassOrphanObserved, reason: "no retained session and no ownership proof"}
}

// mapEntry finds the unique retained session whose current Workspace.Path
// reproduces this entry's complete physical hash. Any ambiguity protects.
func (e *Engine) mapEntry(view *protectionView, entry inventoryEntry) (*state.PublicSession, string) {
	index := view.runtimeByHash
	if entry.kind == ResourceKindSessionProcessPool {
		index = view.poolByHash
	}
	matches := index[entry.hash]
	if len(matches) > 1 {
		return nil, "multiple retained sessions map to one physical hash"
	}
	if len(matches) == 1 {
		return &view.sessionList[matches[0]], ""
	}
	return nil, ""
}

// matchLegacyEvidence proves ownership from raw pre-Normalize Acpx.CWD
// candidates: the candidate must reproduce the complete current hash. Evidence
// never broadens protection of a mapped current session; it only proves
// ownership for sessions absent from current state.
func (e *Engine) matchLegacyEvidence(view *protectionView, entry inventoryEntry) *classification {
	for key, candidates := range e.LegacyEvidence {
		repo, sid := splitSessionKey(key)
		if repo == "" || sid == "" {
			continue
		}
		if _, retained := view.sessions[key]; retained {
			continue
		}
		for _, candidate := range candidates {
			candidate = strings.TrimSpace(candidate)
			if candidate == "" {
				continue
			}
			var hash string
			var err error
			if entry.kind == ResourceKindSessionRuntime {
				hash, err = SessionRuntimeHash(repo, sid, candidate)
			} else {
				canonical, canonErr := Canonicalize(candidate)
				if canonErr != nil {
					continue
				}
				hash, err = SessionProcessPoolHash(repo, sid, canonical)
			}
			if err != nil || hash != entry.hash {
				continue
			}
			if blocker := view.refsProtected(repo, sid); blocker != "" {
				return &classification{class: ClassProtected, reason: blocker, repo: repo, sid: sid, owned: true}
			}
			return &classification{class: ClassRetiredKnown, reason: "raw pre-normalize ownership proof; session pruned", repo: repo, sid: sid, owned: true, wsPath: candidate}
		}
	}
	return nil
}

// migrationBackupNeeded reports whether the first destructive pass still
// lacks its backup. With no raw state file there is nothing to preserve.
func (e *Engine) migrationBackupNeeded() bool {
	if !e.RequireMigrationBackup {
		return false
	}
	backup := filepath.Join(e.WorkspaceRoot, StorageDirName, backupDirName, rawStateBackupName)
	if _, err := os.Lstat(backup); err == nil {
		return false
	}
	if strings.TrimSpace(e.RawStatePath) == "" {
		return true
	}
	if _, err := os.Lstat(e.RawStatePath); err != nil {
		return false
	}
	return true
}

// migrationBackup performs the D7 step-2 backups: the raw pre-Normalize state
// and the prior sidecar, both atomic and idempotent.
func (e *Engine) migrationBackup() error {
	if strings.TrimSpace(e.RawStatePath) == "" {
		return errors.New("raw state path unavailable for migration backup")
	}
	if _, err := EnsureRawStateBackup(e.WorkspaceRoot, e.RawStatePath); err != nil {
		return err
	}
	data, err := os.ReadFile(e.Store.Path())
	if err != nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	backupDir := filepath.Join(e.WorkspaceRoot, StorageDirName, backupDirName)
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return err
	}
	target := filepath.Join(backupDir, sidecarBackupPrefix+".json")
	if _, err := os.Lstat(target); err == nil {
		return nil
	}
	return state.WriteAtomic(target, data)
}

func classFromCleanup(cleanup CleanupState) Class {
	switch cleanup {
	case CleanupRetiredKnown, CleanupEligible, CleanupDeleting, CleanupRemoved:
		return ClassRetiredKnown
	case CleanupOrphanObserved:
		return ClassOrphanObserved
	default:
		return ClassProtected
	}
}

// deleteResource runs the recoverable deletion transaction for one eligible
// resource: mark eligible, try-lock, reload state and re-validate, persist
// deleteResource runs the recoverable deletion transaction for one eligible
// resource: mark eligible, try-lock, reload state and re-validate, persist
// deleting with fsync, remove, then mark removed. Any failed re-validation
// aborts and protects the resource.
func (e *Engine) deleteResource(ctx context.Context, result *ResourceReport, entry inventoryEntry, opts ReconcileOptions, report *Report) {
	record, ok := e.Store.State().Resources[result.ID]
	if !ok {
		result.Action = ActionKept
		result.Reason = "resource record vanished before deletion"
		return
	}
	saveRecord := func() {
		if err := e.Store.Update(func(st *StorageState) error {
			st.Resources[record.ID] = record
			return nil
		}); err != nil {
			report.Diagnostics = append(report.Diagnostics, safeDiagnostic("storage sidecar persist for "+record.ID+": "+err.Error()))
		}
	}
	fail := func(reason string) {
		result.Action = ActionFailed
		result.Reason = boundReason(reason)
		record.LastCleanupError = boundReason(reason)
		saveRecord()
		report.Diagnostics = append(report.Diagnostics, safeDiagnostic("storage deletion "+result.ID+": "+reason))
		// A hard failure defers the owning workspace: for runtimes it is the
		// session clone; for pools it holds the remediation evidence.
		if record.WorkspaceID != "" {
			report.DeferredWorkspaceIDs = append(report.DeferredWorkspaceIDs, record.WorkspaceID)
		}
	}
	protect := func(restore CleanupState, reason string) {
		result.Action = ActionKept
		result.Class = ClassProtected
		result.Reason = boundReason(reason)
		record.CleanupState = restore
		record.LastCleanupError = ""
		saveRecord()
	}

	attemptID, err := e.attemptID()
	if err != nil {
		fail("allocate cleanup attempt: " + err.Error())
		return
	}
	result.AttemptID = attemptID

	if err := e.Store.Update(func(st *StorageState) error {
		current, ok := st.Resources[record.ID]
		if !ok {
			return fmt.Errorf("resource record vanished")
		}
		switch current.CleanupState {
		case CleanupRetiredKnown, CleanupOrphanObserved, CleanupEligible, CleanupDeleting:
		default:
			return fmt.Errorf("resource cleanup state %q is not eligible", current.CleanupState)
		}
		current.CleanupState = CleanupEligible
		current.CleanupAttemptID = attemptID
		st.Resources[record.ID] = current
		record = current
		return nil
	}); err != nil {
		protect(cleanupForClass(result.Class), "eligibility lost: "+err.Error())
		return
	}

	unlock, lockErr := e.tryResourceLock(record.ID)
	if lockErr != nil {
		// Resource lock failure protects the resource.
		record.CleanupState = cleanupForClass(result.Class)
		record.LastCleanupError = "resource lock unavailable"
		saveRecord()
		result.Action = ActionKept
		result.Class = ClassProtected
		result.Reason = "resource lock unavailable; protected"
		return
	}
	defer unlock()

	// Deletion-time revalidation: any state, mapping, or activity change
	// aborts the transaction and protects the resource (D8).
	fresh, err := e.StateLoader(ctx)
	if err != nil {
		fail("deletion-time state reload: " + err.Error())
		return
	}
	fresh.Normalize()
	freshView := buildProtectionView(fresh)
	revalidated := e.decide(freshView, e.Store.State(), entry)
	if revalidated.class != result.Class {
		protect(cleanupForClass(revalidated.class), "classification changed during deletion: "+revalidated.reason)
		return
	}
	if result.Class == ClassOrphanObserved {
		observedAt := record.FirstObservedAt
		if observedAt.IsZero() || e.now().UTC().Before(observedAt.Add(opts.OrphanGrace)) {
			protect(CleanupOrphanObserved, "orphan grace revalidation failed")
			return
		}
	}
	record.CleanupState = CleanupDeleting
	record.CleanupAttemptID = attemptID
	if err := e.Store.Update(func(st *StorageState) error {
		st.Resources[record.ID] = record
		return nil
	}); err != nil {
		fail("persist deleting state: " + err.Error())
		return
	}

	if err := ValidateDeletionTarget(e.WorkspaceRoot, entry.path, entry.hash); err != nil {
		fail("final identity check: " + err.Error())
		return
	}
	var measured int64
	if _, statErr := os.Lstat(entry.path); statErr == nil {
		measured = measureTreeBytes(entry.path)
	}
	if entry.kind == ResourceKindSessionProcessPool {
		if !e.removePoolSafely(ctx, revalidated, entry, result, report) {
			record.CleanupState = cleanupForClass(result.Class)
			saveRecord()
			return
		}
	} else if err := RemoveManagedTree(e.WorkspaceRoot, entry.path, entry.hash, e.preRemoveHook); err != nil {
		fail("remove: " + err.Error())
		return
	}
	if _, err := os.Lstat(entry.path); err == nil {
		fail("removal incomplete: path still exists")
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		fail("post-removal identity check: " + err.Error())
		return
	}

	record.CleanupState = CleanupRemoved
	record.LastCleanupError = ""
	saveRecord()
	result.Action = ActionDeleted
	result.Bytes = measured
	report.ReclaimedBytes += measured
}

// removePoolSafely proves and removes a retired pool within processworkspace's
// integration lifecycle lock. Snapshot-only inspection is never sufficient
// for deletion.
func (e *Engine) removePoolSafely(ctx context.Context, decision classification, entry inventoryEntry, result *ResourceReport, report *Report) bool {
	clone := decision.wsPath
	if clone == "" && decision.wsID != "" {
		candidate := filepath.Join(e.WorkspaceRoot, decision.wsID)
		if validated, err := workspace.ValidatePathUnderRoot(e.WorkspaceRoot, candidate); err == nil {
			clone = validated
		}
	}
	if clone == "" {
		result.Action = ActionPreserved
		result.Reason = "owning clone identity unknown; preserved for operator remediation"
		report.Diagnostics = append(report.Diagnostics, safeDiagnostic("PROCESS pool "+result.ID+" preserved: owning clone identity unknown; inspect manually"))
		return false
	}
	if info, err := os.Lstat(clone); err != nil || !info.IsDir() {
		result.Action = ActionPreserved
		result.Reason = "owning clone missing; preserved for operator remediation"
		report.Diagnostics = append(report.Diagnostics, safeDiagnostic("PROCESS pool "+result.ID+" preserved: owning clone missing; recover or remove manually"))
		return false
	}
	if e.PoolRemover == nil {
		result.Action = ActionPreserved
		result.Reason = "atomic process pool remover unavailable; preserved"
		report.Diagnostics = append(report.Diagnostics, safeDiagnostic("PROCESS pool "+result.ID+" preserved: atomic remover unavailable"))
		return false
	}
	inspection, removed, err := e.PoolRemover(ctx, clone, entry.path)
	if err != nil {
		result.Action = ActionPreserved
		result.Reason = "atomic pool removal failed: " + boundReason(err.Error())
		report.Diagnostics = append(report.Diagnostics, safeDiagnostic("PROCESS pool "+result.ID+" preserved: atomic removal failed; inspect manually"))
		return false
	}
	if !removed {
		result.Action = ActionPreserved
		result.Reason = poolPreserveReason(inspection)
		report.Diagnostics = append(report.Diagnostics, safeDiagnostic("PROCESS pool "+result.ID+" preserved: "+result.Reason+"; run issue-spec workflow workspace reconcile for the owning PROCESS"))
		return false
	}
	return true
}

func poolPreserveReason(inspection PoolInspection) string {
	var reasons []string
	if !inspection.ClonePresent {
		reasons = append(reasons, "clone-missing")
	}
	if !inspection.RegistryComplete {
		reasons = append(reasons, "registry-incomplete")
	}
	if inspection.ActiveLeases > 0 {
		reasons = append(reasons, "active-leases")
	}
	if inspection.OwnershipMarkers > 0 {
		reasons = append(reasons, "ownership-markers")
	}
	if inspection.RegisteredWorktrees > 0 {
		reasons = append(reasons, "registered-worktrees")
	}
	if inspection.FilesystemEntries > 0 {
		reasons = append(reasons, "non-empty")
	}
	if len(inspection.Problems) > 0 {
		reasons = append(reasons, "inspection-problems")
	}
	if len(reasons) == 0 {
		return "uncertain pool state"
	}
	return strings.Join(reasons, ",")
}

func cleanupForClass(class Class) CleanupState {
	switch class {
	case ClassOrphanObserved:
		return CleanupOrphanObserved
	case ClassRetiredKnown:
		return CleanupRetiredKnown
	default:
		return CleanupManaged
	}
}

// tryResourceLock takes the per-resource try-lock. Failure protects the
// resource; locks live under `.storage/locks/` and are never deletion targets.
func (e *Engine) tryResourceLock(id string) (func(), error) {
	dir := filepath.Join(e.WorkspaceRoot, StorageDirName, resourceLockDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	sum := sha256Hex(id)
	path := filepath.Join(dir, sum+".lock")
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := flockTryExclusive(file); err != nil {
		_ = file.Close()
		return nil, err
	}
	return func() {
		_ = flockUnlock(file)
		_ = file.Close()
	}, nil
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// protectionView is the conservative PublicSession-centered active-reference
// scan derived from freshly loaded state. Physical hashes are indexed once per
// pass so mapping stays O(sessions + entries) instead of re-canonicalizing
// every session workspace for every inventory entry.
type protectionView struct {
	sessions      map[string]state.PublicSession
	sessionList   []state.PublicSession
	refs          map[string]string
	runtimeByHash map[string][]int
	poolByHash    map[string][]int
}

func buildProtectionView(st state.RunnerState) *protectionView {
	view := &protectionView{
		sessions:      map[string]state.PublicSession{},
		refs:          map[string]string{},
		runtimeByHash: map[string][]int{},
		poolByHash:    map[string][]int{},
	}
	for _, session := range st.PublicSessions {
		key := sessionKeyOf(session.Repo, session.PublicSessionID)
		view.sessions[key] = session
		view.sessionList = append(view.sessionList, session)
	}
	sort.Slice(view.sessionList, func(i, j int) bool {
		return sessionKeyOf(view.sessionList[i].Repo, view.sessionList[i].PublicSessionID) < sessionKeyOf(view.sessionList[j].Repo, view.sessionList[j].PublicSessionID)
	})
	for i := range view.sessionList {
		session := &view.sessionList[i]
		wsPath := strings.TrimSpace(session.Workspace.Path)
		if wsPath == "" {
			continue
		}
		if hash, err := SessionRuntimeHash(session.Repo, session.PublicSessionID, wsPath); err == nil {
			view.runtimeByHash[hash] = append(view.runtimeByHash[hash], i)
		}
		canonical, err := Canonicalize(wsPath)
		if err != nil {
			continue
		}
		if hash, err := SessionProcessPoolHash(session.Repo, session.PublicSessionID, canonical); err == nil {
			view.poolByHash[hash] = append(view.poolByHash[hash], i)
		}
	}
	addRef := func(repo, sid, reason string) {
		key := sessionKeyOf(repo, sid)
		if key == "" {
			return
		}
		if _, ok := view.refs[key]; !ok {
			view.refs[key] = reason
		}
	}
	for _, job := range st.Jobs {
		switch job.Status {
		case state.StatusQueued, state.StatusDispatched, state.StatusRunning, state.StatusInterrupted:
			addRef(job.Repo, job.PublicSessionID, "active job "+job.ID)
			addRef(job.Repo, job.DispatchIntent.PublicSessionID, "active job "+job.ID)
		}
	}
	for _, cancel := range st.Cancellations {
		if !cancel.Status.Terminal() {
			addRef(cancel.Repo, cancel.TargetPublicSessionID, "non-terminal cancellation "+cancel.ID)
		}
	}
	return view
}

// sessionProtected reports the first protection condition for a mapped
// session, or "" when the session is provably inactive.
func (v *protectionView) sessionProtected(session *state.PublicSession) string {
	if !session.Status.Terminal() {
		return "session is non-terminal"
	}
	if blocker := v.refsProtected(session.Repo, session.PublicSessionID); blocker != "" {
		return blocker
	}
	if strings.TrimSpace(session.Lock.OwnerJobID) != "" {
		return "session lock owner present"
	}
	if len(session.Queue.PendingJobIDs) > 0 {
		return "session queue non-empty"
	}
	return ""
}

func (v *protectionView) refsProtected(repo, sid string) string {
	if reason, ok := v.refs[sessionKeyOf(repo, sid)]; ok {
		return "active reference: " + reason
	}
	return ""
}

func sessionKeyOf(repo, sid string) string {
	repo = strings.TrimSpace(repo)
	sid = strings.TrimSpace(sid)
	if repo == "" || sid == "" {
		return ""
	}
	return state.PublicSessionKey(repo, sid)
}

func splitSessionKey(key string) (string, string) {
	repo, sid, ok := state.SplitPublicSessionKey(key)
	if !ok {
		return "", ""
	}
	return repo, sid
}

func measureTreeBytes(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(_ string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr == nil && info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return total
}

func safeDiagnostic(value string) string { return truncateBounded(value, 512) }

func boundReason(value string) string { return truncateBounded(value, 256) }

func truncateBounded(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "...(truncated)"
}
