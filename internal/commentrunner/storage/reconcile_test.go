package storage

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/commentrunner/state"
)

type engineFixture struct {
	t    *testing.T
	root string
	now  time.Time
}

func newEngineFixture(t *testing.T) *engineFixture {
	t.Helper()
	return &engineFixture{t: t, root: testRoot(t), now: time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)}
}

func (f *engineFixture) runtimeHash(repo, sid, wsPath string) string {
	f.t.Helper()
	hash, err := SessionRuntimeHash(repo, sid, wsPath)
	if err != nil {
		f.t.Fatalf("runtime hash: %v", err)
	}
	return hash
}

func (f *engineFixture) poolHash(repo, sid, wsPath string) string {
	f.t.Helper()
	canonical, err := Canonicalize(wsPath)
	if err != nil {
		f.t.Fatalf("canonicalize: %v", err)
	}
	hash, err := SessionProcessPoolHash(repo, sid, canonical)
	if err != nil {
		f.t.Fatalf("pool hash: %v", err)
	}
	return hash
}

func (f *engineFixture) mkWorkspace(id string) string {
	f.t.Helper()
	path := filepath.Join(f.root, id)
	if err := os.MkdirAll(path, 0o700); err != nil {
		f.t.Fatalf("mkdir workspace: %v", err)
	}
	return path
}

func (f *engineFixture) mkRuntimeDir(hash string) string {
	f.t.Helper()
	path := filepath.Join(f.root, ".sessions", hash)
	if err := os.MkdirAll(path, 0o700); err != nil {
		f.t.Fatalf("mkdir runtime: %v", err)
	}
	return path
}

func (f *engineFixture) mkPoolDir(hash string) string {
	f.t.Helper()
	path := filepath.Join(f.root, ".process-workspaces", hash)
	if err := os.MkdirAll(path, 0o700); err != nil {
		f.t.Fatalf("mkdir pool: %v", err)
	}
	return path
}

func (f *engineFixture) stateLoader(st state.RunnerState) StateLoader {
	return func(context.Context) (state.RunnerState, error) {
		return st, nil
	}
}

func (f *engineFixture) newEngine(loader StateLoader, inspector PoolInspector) *Engine {
	f.t.Helper()
	engine, err := NewEngine(EngineConfig{
		WorkspaceRoot: f.root,
		StateLoader:   loader,
		PoolInspector: inspector,
		Now:           func() time.Time { return f.now },
		AttemptID:     sequentialAttemptIDs(),
	})
	if err != nil {
		f.t.Fatalf("NewEngine: %v", err)
	}
	return engine
}

func sequentialAttemptIDs() func() (string, error) {
	n := 0
	return func() (string, error) {
		n++
		return "attempt-" + strings.Repeat("0", 3) + string(rune('0'+n)), nil
	}
}

func terminalSession(repo, sid, wsID, wsPath string, lastUsed time.Time) state.PublicSession {
	return state.PublicSession{
		Repo:            repo,
		PublicSessionID: sid,
		AcpxRecordID:    "rec-" + sid,
		Status:          state.StatusCompleted,
		Workspace: state.WorkspaceMetadata{
			ID:   wsID,
			Path: wsPath,
			Repo: repo,
		},
		CreatedAt:  lastUsed.Add(-time.Hour),
		LastUsedAt: lastUsed,
	}
}

func reportByID(t *testing.T, report Report, id string) ResourceReport {
	t.Helper()
	for _, r := range report.Resources {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("resource %q missing from report: %+v", id, report.Resources)
	return ResourceReport{}
}

func TestClassifyProtectedForActiveSession(t *testing.T) {
	f := newEngineFixture(t)
	wsPath := f.mkWorkspace("ws-active")
	hash := f.runtimeHash("o/r", "ps-1", wsPath)
	f.mkRuntimeDir(hash)
	st := state.NewState()
	session := terminalSession("o/r", "ps-1", "ws-active", wsPath, f.now.Add(-time.Hour))
	session.Status = state.StatusRunning
	if err := st.UpsertPublicSession(session); err != nil {
		t.Fatalf("upsert session: %v", err)
	}

	engine := f.newEngine(f.stateLoader(st), nil)
	report, err := engine.Reconcile(context.Background(), ReconcileOptions{Apply: true, OrphanGrace: DefaultOrphanGrace})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	id := ResourceID(ResourceKindSessionRuntime, "o/r", "ps-1", hash)
	got := reportByID(t, report, id)
	if got.Class != ClassProtected || got.Action != ActionKept {
		t.Fatalf("class=%q action=%q, want protected/kept", got.Class, got.Action)
	}
	if _, err := os.Stat(filepath.Join(f.root, ".sessions", hash)); err != nil {
		t.Fatalf("protected runtime must remain: %v", err)
	}
	// Sidecar records the managed ownership evidence.
	entry, ok := engine.Store.State().Resources[id]
	if !ok || entry.CleanupState != CleanupManaged || !entry.Owned() {
		t.Fatalf("sidecar entry = %+v ok=%v, want managed owned", entry, ok)
	}
}

func TestClassifyProtectedWhenTerminalWorkspacePresent(t *testing.T) {
	f := newEngineFixture(t)
	wsPath := f.mkWorkspace("ws-present")
	hash := f.runtimeHash("o/r", "ps-1", wsPath)
	f.mkRuntimeDir(hash)
	st := state.NewState()
	if err := st.UpsertPublicSession(terminalSession("o/r", "ps-1", "ws-present", wsPath, f.now.Add(-time.Hour))); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	engine := f.newEngine(f.stateLoader(st), nil)
	report, err := engine.Reconcile(context.Background(), ReconcileOptions{Apply: true, OrphanGrace: DefaultOrphanGrace})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got := reportByID(t, report, ResourceID(ResourceKindSessionRuntime, "o/r", "ps-1", hash))
	if got.Class != ClassProtected {
		t.Fatalf("class=%q, want protected (valid workspace anchors /resume)", got.Class)
	}
}

func TestRetireTerminalSessionWithMissingWorkspace(t *testing.T) {
	f := newEngineFixture(t)
	wsPath := filepath.Join(f.root, "ws-gone")
	hash := f.runtimeHash("o/r", "ps-1", wsPath)
	f.mkRuntimeDir(hash)
	st := state.NewState()
	if err := st.UpsertPublicSession(terminalSession("o/r", "ps-1", "ws-gone", wsPath, f.now.Add(-48*time.Hour))); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	engine := f.newEngine(f.stateLoader(st), nil)
	report, err := engine.Reconcile(context.Background(), ReconcileOptions{Apply: true, OrphanGrace: DefaultOrphanGrace})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	id := ResourceID(ResourceKindSessionRuntime, "o/r", "ps-1", hash)
	got := reportByID(t, report, id)
	if got.Class != ClassRetiredKnown || got.Action != ActionDeleted {
		t.Fatalf("class=%q action=%q, want retired_known/deleted", got.Class, got.Action)
	}
	if _, err := os.Lstat(filepath.Join(f.root, ".sessions", hash)); !os.IsNotExist(err) {
		t.Fatalf("retired runtime must be removed, lstat err=%v", err)
	}
	entry, ok := engine.Store.State().Resources[id]
	if !ok || entry.CleanupState != CleanupRemoved {
		t.Fatalf("sidecar entry = %+v ok=%v, want removed tombstone", entry, ok)
	}
	// The PublicSession itself is untouched: no retirement fields in RunnerState.
	if _, ok := st.GetPublicSession("o/r", "ps-1"); !ok {
		t.Fatalf("runner state session must be preserved for TTL pruning")
	}
}

func TestRetirementBlockedByActiveReferences(t *testing.T) {
	cases := map[string]func(st *state.RunnerState, repo, sid string){
		"queued job": func(st *state.RunnerState, repo, sid string) {
			_ = st.UpsertJob(state.Job{ID: "j-1", Repo: repo, PublicSessionID: sid, Status: state.StatusQueued})
		},
		"dispatched job via intent": func(st *state.RunnerState, repo, sid string) {
			_ = st.UpsertJob(state.Job{ID: "j-2", Repo: repo, Status: state.StatusDispatched, DispatchIntent: state.DispatchIntent{PublicSessionID: sid}})
		},
		"running job": func(st *state.RunnerState, repo, sid string) {
			_ = st.UpsertJob(state.Job{ID: "j-3", Repo: repo, PublicSessionID: sid, Status: state.StatusRunning})
		},
		"interrupted job": func(st *state.RunnerState, repo, sid string) {
			_ = st.UpsertJob(state.Job{ID: "j-4", Repo: repo, PublicSessionID: sid, Status: state.StatusInterrupted})
		},
		"pending cancellation": func(st *state.RunnerState, repo, sid string) {
			_ = st.UpsertCancellation(state.Cancellation{ID: "c-1", IdempotencyKey: "k-1", Repo: repo, TargetPublicSessionID: sid, Status: state.StatusQueued})
		},
		"session lock": func(st *state.RunnerState, repo, sid string) {
			session, _ := st.GetPublicSession(repo, sid)
			session.Lock = state.SessionLock{OwnerJobID: "j-9"}
			_ = st.UpsertPublicSession(session)
		},
		"session queue": func(st *state.RunnerState, repo, sid string) {
			session, _ := st.GetPublicSession(repo, sid)
			session.Queue.PendingJobIDs = []string{"j-10"}
			_ = st.UpsertPublicSession(session)
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := newEngineFixture(t)
			wsPath := filepath.Join(f.root, "ws-gone")
			hash := f.runtimeHash("o/r", "ps-1", wsPath)
			f.mkRuntimeDir(hash)
			st := state.NewState()
			if err := st.UpsertPublicSession(terminalSession("o/r", "ps-1", "ws-gone", wsPath, f.now.Add(-48*time.Hour))); err != nil {
				t.Fatalf("upsert: %v", err)
			}
			mutate(&st, "o/r", "ps-1")
			engine := f.newEngine(f.stateLoader(st), nil)
			report, err := engine.Reconcile(context.Background(), ReconcileOptions{Apply: true, OrphanGrace: DefaultOrphanGrace})
			if err != nil {
				t.Fatalf("Reconcile: %v", err)
			}
			got := reportByID(t, report, ResourceID(ResourceKindSessionRuntime, "o/r", "ps-1", hash))
			if got.Class != ClassProtected || got.Action != ActionKept {
				t.Fatalf("class=%q action=%q, want protected/kept", got.Class, got.Action)
			}
			if _, err := os.Lstat(filepath.Join(f.root, ".sessions", hash)); err != nil {
				t.Fatalf("protected runtime must remain: %v", err)
			}
		})
	}
}

func TestUnmatchedRuntimeBecomesOrphanObserved(t *testing.T) {
	f := newEngineFixture(t)
	hash := strings.Repeat("de", 16)
	f.mkRuntimeDir(hash)
	st := state.NewState()
	engine := f.newEngine(f.stateLoader(st), nil)
	report, err := engine.Reconcile(context.Background(), ReconcileOptions{Apply: true, OrphanGrace: DefaultOrphanGrace})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	id := ResourceID(ResourceKindSessionRuntime, "", "", hash)
	got := reportByID(t, report, id)
	if got.Class != ClassOrphanObserved || got.Action != ActionObserved {
		t.Fatalf("class=%q action=%q, want orphan_observed/observed", got.Class, got.Action)
	}
	entry, ok := engine.Store.State().Resources[id]
	if !ok || entry.CleanupState != CleanupOrphanObserved || !entry.FirstObservedAt.Equal(f.now) {
		t.Fatalf("sidecar entry = %+v ok=%v, want orphan_observed at now", entry, ok)
	}
	// Within grace: never deleted even on apply.
	if _, err := os.Lstat(filepath.Join(f.root, ".sessions", hash)); err != nil {
		t.Fatalf("orphan within grace must remain: %v", err)
	}
}

func TestOrphanEligibleAfterGrace(t *testing.T) {
	f := newEngineFixture(t)
	hash := strings.Repeat("de", 16)
	f.mkRuntimeDir(hash)
	st := state.NewState()
	engine := f.newEngine(f.stateLoader(st), nil)
	// First pass observes.
	if _, err := engine.Reconcile(context.Background(), ReconcileOptions{Apply: true, OrphanGrace: DefaultOrphanGrace}); err != nil {
		t.Fatalf("Reconcile observe: %v", err)
	}
	// Advance past the grace window: eligible and deleted on apply.
	f.now = f.now.Add(DefaultOrphanGrace + time.Hour)
	report, err := engine.Reconcile(context.Background(), ReconcileOptions{Apply: true, OrphanGrace: DefaultOrphanGrace})
	if err != nil {
		t.Fatalf("Reconcile apply: %v", err)
	}
	got := reportByID(t, report, ResourceID(ResourceKindSessionRuntime, "", "", hash))
	if got.Class != ClassOrphanObserved || got.Action != ActionDeleted {
		t.Fatalf("class=%q action=%q, want orphan_observed/deleted", got.Class, got.Action)
	}
	if _, err := os.Lstat(filepath.Join(f.root, ".sessions", hash)); !os.IsNotExist(err) {
		t.Fatalf("grace-expired orphan must be removed, err=%v", err)
	}
}

func TestOrphanGraceIsConfigurable(t *testing.T) {
	f := newEngineFixture(t)
	hash := strings.Repeat("de", 16)
	f.mkRuntimeDir(hash)
	engine := f.newEngine(f.stateLoader(state.NewState()), nil)
	if _, err := engine.Reconcile(context.Background(), ReconcileOptions{Apply: true, OrphanGrace: time.Hour}); err != nil {
		t.Fatalf("Reconcile observe: %v", err)
	}
	f.now = f.now.Add(2 * time.Hour)
	report, err := engine.Reconcile(context.Background(), ReconcileOptions{Apply: true, OrphanGrace: time.Hour})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := reportByID(t, report, ResourceID(ResourceKindSessionRuntime, "", "", hash)); got.Action != ActionDeleted {
		t.Fatalf("action=%q, want deleted with 1h grace", got.Action)
	}
}

func TestSidecarProvenOwnershipRetiresPrunedSessionRuntime(t *testing.T) {
	f := newEngineFixture(t)
	wsPath := filepath.Join(f.root, "ws-pruned")
	hash := f.runtimeHash("o/r", "ps-1", wsPath)
	f.mkRuntimeDir(hash)
	// First pass: session still retained and protected (workspace exists).
	f.mkWorkspace("ws-pruned")
	st := state.NewState()
	if err := st.UpsertPublicSession(terminalSession("o/r", "ps-1", "ws-pruned", wsPath, f.now.Add(-time.Hour))); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	engine := f.newEngine(f.stateLoader(st), nil)
	if _, err := engine.Reconcile(context.Background(), ReconcileOptions{Apply: true, OrphanGrace: DefaultOrphanGrace}); err != nil {
		t.Fatalf("Reconcile first: %v", err)
	}
	// Session pruned from state (TTL/count) and workspace removed; sidecar proof remains.
	engine.StateLoader = f.stateLoader(state.NewState())
	if err := os.RemoveAll(wsPath); err != nil {
		t.Fatalf("remove workspace: %v", err)
	}
	report, err := engine.Reconcile(context.Background(), ReconcileOptions{Apply: true, OrphanGrace: DefaultOrphanGrace})
	if err != nil {
		t.Fatalf("Reconcile second: %v", err)
	}
	id := ResourceID(ResourceKindSessionRuntime, "o/r", "ps-1", hash)
	got := reportByID(t, report, id)
	if got.Class != ClassRetiredKnown || got.Action != ActionDeleted {
		t.Fatalf("class=%q action=%q, want retired_known/deleted from sidecar proof", got.Class, got.Action)
	}
}

func TestPrunedSessionWithoutProofStaysOrphan(t *testing.T) {
	f := newEngineFixture(t)
	hash := strings.Repeat("de", 16)
	f.mkRuntimeDir(hash)
	// Empty state, no sidecar history: unmatched and unproven.
	engine := f.newEngine(f.stateLoader(state.NewState()), nil)
	report, err := engine.Reconcile(context.Background(), ReconcileOptions{Apply: true, OrphanGrace: DefaultOrphanGrace})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := reportByID(t, report, ResourceID(ResourceKindSessionRuntime, "", "", hash)); got.Class != ClassOrphanObserved {
		t.Fatalf("class=%q, want orphan_observed", got.Class)
	}
}

func TestRejectedInventoryEntries(t *testing.T) {
	f := newEngineFixture(t)
	sessionsDir := filepath.Join(f.root, ".sessions")
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Invalid name, symlink, and regular file entries are rejected, never deleted.
	if err := os.Mkdir(filepath.Join(sessionsDir, "not-a-hash"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	realDir := f.mkWorkspace("ws-real")
	if err := os.Symlink(realDir, filepath.Join(sessionsDir, strings.Repeat("ab", 16))); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionsDir, strings.Repeat("cd", 16)), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	engine := f.newEngine(f.stateLoader(state.NewState()), nil)
	report, err := engine.Reconcile(context.Background(), ReconcileOptions{Apply: true, OrphanGrace: DefaultOrphanGrace})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	rejected := 0
	for _, r := range report.Resources {
		if r.Class == ClassRejected {
			rejected++
			if r.Action != ActionRejected {
				t.Fatalf("rejected action=%q", r.Action)
			}
		}
	}
	if rejected != 3 {
		t.Fatalf("rejected=%d, want 3: %+v", rejected, report.Resources)
	}
	for _, name := range []string{"not-a-hash", strings.Repeat("ab", 16), strings.Repeat("cd", 16)} {
		if _, err := os.Lstat(filepath.Join(sessionsDir, name)); err != nil {
			t.Fatalf("rejected entry %q must remain: %v", name, err)
		}
	}
	// Protected infrastructure is never inventoried.
	for _, r := range report.Resources {
		if strings.Contains(r.ID, ".storage") || strings.Contains(r.ID, ".locks") {
			t.Fatalf("protected entry inventoried: %q", r.ID)
		}
	}
}

func TestDryRunReportsWouldDeleteWithoutSideEffects(t *testing.T) {
	f := newEngineFixture(t)
	wsPath := filepath.Join(f.root, "ws-gone")
	hash := f.runtimeHash("o/r", "ps-1", wsPath)
	f.mkRuntimeDir(hash)
	st := state.NewState()
	if err := st.UpsertPublicSession(terminalSession("o/r", "ps-1", "ws-gone", wsPath, f.now.Add(-48*time.Hour))); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	engine := f.newEngine(f.stateLoader(st), nil)
	report, err := engine.Reconcile(context.Background(), ReconcileOptions{Apply: false, OrphanGrace: DefaultOrphanGrace, MeasureAll: true})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !report.DryRun {
		t.Fatalf("report must be marked dry-run")
	}
	got := reportByID(t, report, ResourceID(ResourceKindSessionRuntime, "o/r", "ps-1", hash))
	if got.Class != ClassRetiredKnown || got.Action != ActionWouldDelete {
		t.Fatalf("class=%q action=%q, want retired_known/would_delete", got.Class, got.Action)
	}
	if _, err := os.Lstat(filepath.Join(f.root, ".sessions", hash)); err != nil {
		t.Fatalf("dry-run must not delete: %v", err)
	}
	if len(engine.Store.State().Resources) != 0 {
		t.Fatalf("dry-run must not persist sidecar entries: %+v", engine.Store.State().Resources)
	}
}

func TestReportOnlySidecarBlocksAllMutations(t *testing.T) {
	f := newEngineFixture(t)
	wsPath := filepath.Join(f.root, "ws-gone")
	hash := f.runtimeHash("o/r", "ps-1", wsPath)
	f.mkRuntimeDir(hash)
	st := state.NewState()
	if err := st.UpsertPublicSession(terminalSession("o/r", "ps-1", "ws-gone", wsPath, f.now.Add(-48*time.Hour))); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	engine := f.newEngine(f.stateLoader(st), nil)
	if err := engine.Store.Update(func(st *StorageState) error { return nil }); err != nil {
		t.Fatalf("seed sidecar: %v", err)
	}
	// Flip the on-disk identity so the reload yields a report-only store.
	sidecarPath := filepath.Join(f.root, ".storage", "state.json")
	data, err := os.ReadFile(sidecarPath)
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	decoded["root_identity"] = strings.Repeat("ff", 32)
	raw, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if err := os.WriteFile(sidecarPath, raw, 0o600); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
	report, err := engine.Reconcile(context.Background(), ReconcileOptions{Apply: true, OrphanGrace: DefaultOrphanGrace})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !report.ReportOnly {
		t.Fatalf("report must be marked report-only")
	}
	if _, err := os.Lstat(filepath.Join(f.root, ".sessions", hash)); err != nil {
		t.Fatalf("report-only reconcile must not delete: %v", err)
	}
}

func TestIdempotentReconcile(t *testing.T) {
	f := newEngineFixture(t)
	wsPath := filepath.Join(f.root, "ws-gone")
	hash := f.runtimeHash("o/r", "ps-1", wsPath)
	f.mkRuntimeDir(hash)
	st := state.NewState()
	if err := st.UpsertPublicSession(terminalSession("o/r", "ps-1", "ws-gone", wsPath, f.now.Add(-48*time.Hour))); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	engine := f.newEngine(f.stateLoader(st), nil)
	first, err := engine.Reconcile(context.Background(), ReconcileOptions{Apply: true, OrphanGrace: DefaultOrphanGrace})
	if err != nil {
		t.Fatalf("Reconcile first: %v", err)
	}
	second, err := engine.Reconcile(context.Background(), ReconcileOptions{Apply: true, OrphanGrace: DefaultOrphanGrace})
	if err != nil {
		t.Fatalf("Reconcile second: %v", err)
	}
	if len(second.Resources) != 0 {
		t.Fatalf("second pass must be empty after deletion: %+v", second.Resources)
	}
	if first.ReclaimedBytes != 0 || second.ReclaimedBytes != 0 {
		t.Fatalf("empty dirs reclaim zero bytes: %d %d", first.ReclaimedBytes, second.ReclaimedBytes)
	}
}
