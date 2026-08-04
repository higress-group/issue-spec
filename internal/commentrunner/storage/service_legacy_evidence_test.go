package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/commentrunner/state"
)

// End-to-end at the Service seam: a pre-Normalize raw state backup supplies
// the only legacy ownership evidence. Both the typed acpx.cwd and the
// flattened acpx.raw.cwd forms must prove retired-known ownership, the
// deletion must go through the shared engine, and the backup bytes must equal
// the original raw state exactly.
func TestServiceReconcileUsesRawBackupLegacyEvidence(t *testing.T) {
	root := testRoot(t)
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	stateDir := t.TempDir()
	rawStatePath := filepath.Join(stateDir, "state.json")

	legacyA := filepath.Join(root, "ws-legacy-a")
	legacyB := filepath.Join(root, "ws-legacy-b")
	writeRawState(t, rawStatePath, map[string]map[string]any{
		"o/r#ps-a": {
			"repo": "o/r", "public_session_id": "ps-a", "acpx_record_id": "rec-a",
			"status": "completed",
			"acpx":   map[string]any{"cwd": legacyA},
		},
		"o/r#ps-b": {
			"repo": "o/r", "public_session_id": "ps-b", "acpx_record_id": "rec-b",
			"status": "completed",
			"acpx":   map[string]any{"raw": map[string]any{"cwd": legacyB}},
		},
	})
	rawBytes, err := os.ReadFile(rawStatePath)
	if err != nil {
		t.Fatalf("read raw fixture: %v", err)
	}
	backup, err := EnsureRawStateBackup(root, rawStatePath)
	if err != nil {
		t.Fatalf("EnsureRawStateBackup: %v", err)
	}

	runtimeDir := func(repo, sid, wsPath string) string {
		hash, err := SessionRuntimeHash(repo, sid, wsPath)
		if err != nil {
			t.Fatalf("runtime hash: %v", err)
		}
		path := filepath.Join(root, SessionsDirName, hash)
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatalf("mkdir runtime: %v", err)
		}
		return path
	}
	dirA := runtimeDir("o/r", "ps-a", legacyA)
	dirB := runtimeDir("o/r", "ps-b", legacyB)

	// Current state retains only ps-c with an existing workspace; ps-a and
	// ps-b were pruned, which is exactly the legacy-evidence scenario.
	wsC := filepath.Join(root, "ws-c")
	if err := os.MkdirAll(wsC, 0o700); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	dirC := runtimeDir("o/r", "ps-c", wsC)
	current := state.NewState()
	if err := current.UpsertPublicSession(terminalSession("o/r", "ps-c", "ws-c", wsC, now.Add(-2*time.Hour))); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	svc, err := NewService(ServiceConfig{
		WorkspaceRoot: root,
		RawStatePath:  rawStatePath,
		StateLoader: func(context.Context) (state.RunnerState, error) {
			return current, nil
		},
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	defer svc.Close()
	report, err := svc.ReconcileStorage(context.Background(), true, false)
	if err != nil {
		t.Fatalf("ReconcileStorage: %v", err)
	}

	hashA, _ := SessionRuntimeHash("o/r", "ps-a", legacyA)
	hashB, _ := SessionRuntimeHash("o/r", "ps-b", legacyB)
	hashC, _ := SessionRuntimeHash("o/r", "ps-c", wsC)
	for id, dir := range map[string]string{
		ResourceID(ResourceKindSessionRuntime, "o/r", "ps-a", hashA): dirA,
		ResourceID(ResourceKindSessionRuntime, "o/r", "ps-b", hashB): dirB,
	} {
		got := reportByID(t, report, id)
		if got.Class != ClassRetiredKnown || got.Action != ActionDeleted {
			t.Fatalf("%s: class=%q action=%q reason=%q, want retired_known/deleted via raw evidence", id, got.Class, got.Action, got.Reason)
		}
		if _, err := os.Lstat(dir); !os.IsNotExist(err) {
			t.Fatalf("%s: legacy-proven runtime must be deleted, err=%v", id, err)
		}
	}
	gotC := reportByID(t, report, ResourceID(ResourceKindSessionRuntime, "o/r", "ps-c", hashC))
	if gotC.Class != ClassProtected || gotC.Action != ActionKept {
		t.Fatalf("retained session: class=%q action=%q, want protected/kept", gotC.Class, gotC.Action)
	}
	if _, err := os.Lstat(dirC); err != nil {
		t.Fatalf("retained session runtime must remain: %v", err)
	}

	backupBytes, err := os.ReadFile(backup)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if string(backupBytes) != string(rawBytes) {
		t.Fatalf("backup bytes differ from original raw state:\nbackup: %s\nraw: %s", backupBytes, rawBytes)
	}
}
