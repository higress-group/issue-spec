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

func writeRawState(t *testing.T, path string, sessions map[string]map[string]any) {
	t.Helper()
	doc := map[string]any{
		"schema_version":    6,
		"public_sessions":   sessions,
		"jobs":              map[string]any{},
		"workspaces":        map[string]any{},
		"cancellations":     map[string]any{},
		"status_writebacks": map[string]any{},
	}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal raw state: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write raw state: %v", err)
	}
}

func TestEnsureRawStateBackupPreservesFirstBytes(t *testing.T) {
	root := testRoot(t)
	statePath := filepath.Join(t.TempDir(), "state.json")
	writeRawState(t, statePath, map[string]map[string]any{
		"o/r#ps-1": {"repo": "o/r", "public_session_id": "ps-1", "acpx_record_id": "rec-1", "acpx": map[string]any{"cwd": "/legacy/ws-1"}},
	})
	backup, err := EnsureRawStateBackup(root, statePath)
	if err != nil {
		t.Fatalf("EnsureRawStateBackup: %v", err)
	}
	// Second call keeps the original bytes even after the live file changes.
	writeRawState(t, statePath, map[string]map[string]any{
		"o/r#ps-1": {"repo": "o/r", "public_session_id": "ps-1", "acpx_record_id": "rec-1", "acpx": map[string]any{"cwd": "/new/ws-1"}},
	})
	again, err := EnsureRawStateBackup(root, statePath)
	if err != nil {
		t.Fatalf("EnsureRawStateBackup second: %v", err)
	}
	if backup != again {
		t.Fatalf("backup path changed: %q vs %q", backup, again)
	}
	data, err := os.ReadFile(backup)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if !strings.Contains(string(data), "/legacy/ws-1") {
		t.Fatalf("backup must preserve first raw bytes, got %s", data)
	}
}

func TestEnsureRawStateBackupMissingStateIsNoop(t *testing.T) {
	root := testRoot(t)
	backup, err := EnsureRawStateBackup(root, filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("missing state must not fail: %v", err)
	}
	if backup != "" {
		t.Fatalf("missing state yields no backup, got %q", backup)
	}
}

func TestLoadLegacyEvidenceExtractsPreNormalizeCWD(t *testing.T) {
	root := testRoot(t)
	statePath := filepath.Join(t.TempDir(), "state.json")
	writeRawState(t, statePath, map[string]map[string]any{
		"o/r#ps-1": {
			"repo": "o/r", "public_session_id": "ps-1", "acpx_record_id": "rec-1",
			"workspace": map[string]any{"id": "ws-1", "path": "/normalized/ws-1", "repo": "o/r"},
			"acpx":      map[string]any{"cwd": "/legacy/ws-1"},
			"status":    "completed",
		},
		"o/r#ps-2": {
			"repo": "o/r", "public_session_id": "ps-2", "acpx_record_id": "rec-2",
			"acpx": map[string]any{"raw": map[string]any{"cwd": "/legacy/ws-2"}},
		},
	})
	backup, err := EnsureRawStateBackup(root, statePath)
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	evidence, err := LoadLegacyEvidence(backup)
	if err != nil {
		t.Fatalf("LoadLegacyEvidence: %v", err)
	}
	if got := evidence["o/r#ps-1"]; len(got) != 1 || got[0] != "/legacy/ws-1" {
		t.Fatalf("ps-1 evidence = %v, want /legacy/ws-1", got)
	}
	if got := evidence["o/r#ps-2"]; len(got) != 1 || got[0] != "/legacy/ws-2" {
		t.Fatalf("ps-2 evidence = %v, want legacy raw cwd", got)
	}

	// Normal LoadFile output is NOT original-path evidence: Normalize overwrites
	// CWD with Workspace.Path, so evidence must come from the raw backup.
	loaded, err := state.LoadFile(statePath)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	session, ok := loaded.GetPublicSession("o/r", "ps-1")
	if !ok {
		t.Fatalf("session missing")
	}
	if session.Acpx.CWD != "/normalized/ws-1" {
		t.Fatalf("LoadFile CWD = %q, proving normalized output cannot serve as legacy evidence", session.Acpx.CWD)
	}
}

func TestFirstMigrationWithoutBackupSkipsDeletions(t *testing.T) {
	f := newEngineFixture(t)
	wsPath := filepath.Join(f.root, "ws-gone")
	hash := f.runtimeHash("o/r", "ps-1", wsPath)
	f.mkRuntimeDir(hash)
	st := state.NewState()
	if err := st.UpsertPublicSession(terminalSession("o/r", "ps-1", "ws-gone", wsPath, f.now.Add(-48*time.Hour))); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	engine := f.newEngine(f.stateLoader(st), nil)
	engine.RawStatePath = "" // no backup source configured
	engine.RequireMigrationBackup = true
	report, err := engine.Reconcile(context.Background(), ReconcileOptions{Apply: true, OrphanGrace: DefaultOrphanGrace})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got := reportByID(t, report, ResourceID(ResourceKindSessionRuntime, "o/r", "ps-1", hash))
	if got.Action == ActionDeleted {
		t.Fatalf("first migration without backup must not delete")
	}
	if _, err := os.Lstat(filepath.Join(f.root, ".sessions", hash)); err != nil {
		t.Fatalf("runtime must remain without backup: %v", err)
	}
	found := false
	for _, d := range report.Diagnostics {
		if strings.Contains(d, "backup") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected backup diagnostic, got %+v", report.Diagnostics)
	}
}

func TestFirstMigrationWithBackupDeletes(t *testing.T) {
	f := newEngineFixture(t)
	wsPath := filepath.Join(f.root, "ws-gone")
	hash := f.runtimeHash("o/r", "ps-1", wsPath)
	f.mkRuntimeDir(hash)
	st := state.NewState()
	if err := st.UpsertPublicSession(terminalSession("o/r", "ps-1", "ws-gone", wsPath, f.now.Add(-48*time.Hour))); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	statePath := filepath.Join(t.TempDir(), "state.json")
	writeRawState(t, statePath, map[string]map[string]any{})
	engine := f.newEngine(f.stateLoader(st), nil)
	engine.RawStatePath = statePath
	engine.RequireMigrationBackup = true
	report, err := engine.Reconcile(context.Background(), ReconcileOptions{Apply: true, OrphanGrace: DefaultOrphanGrace})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got := reportByID(t, report, ResourceID(ResourceKindSessionRuntime, "o/r", "ps-1", hash))
	if got.Action != ActionDeleted {
		t.Fatalf("action=%q, want deleted with backup present", got.Action)
	}
	if _, err := os.Stat(filepath.Join(f.root, ".storage", "backups", "state-first.json")); err != nil {
		t.Fatalf("first migration backup missing: %v", err)
	}
}

func TestLaterPassesWithBackupDelete(t *testing.T) {
	f := newEngineFixture(t)
	wsPath := filepath.Join(f.root, "ws-gone")
	hash := f.runtimeHash("o/r", "ps-1", wsPath)
	f.mkRuntimeDir(hash)
	st := state.NewState()
	if err := st.UpsertPublicSession(terminalSession("o/r", "ps-1", "ws-gone", wsPath, f.now.Add(-48*time.Hour))); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	engine := f.newEngine(f.stateLoader(st), nil)
	engine.RawStatePath = ""
	engine.RequireMigrationBackup = true
	// A prior sidecar and a completed migration backup exist: not the first
	// destructive pass.
	if err := engine.Store.Update(func(st *StorageState) error { return nil }); err != nil {
		t.Fatalf("seed sidecar: %v", err)
	}
	backupDir := filepath.Join(f.root, ".storage", "backups")
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		t.Fatalf("mkdir backups: %v", err)
	}
	if err := os.WriteFile(filepath.Join(backupDir, "state-first.json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("seed backup: %v", err)
	}
	report, err := engine.Reconcile(context.Background(), ReconcileOptions{Apply: true, OrphanGrace: DefaultOrphanGrace})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got := reportByID(t, report, ResourceID(ResourceKindSessionRuntime, "o/r", "ps-1", hash))
	if got.Action != ActionDeleted {
		t.Fatalf("action=%q, want deleted on later pass with backup", got.Action)
	}
}
