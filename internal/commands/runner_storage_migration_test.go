package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	crstate "github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/commentrunner/storage"
)

func TestRunnerStorageReconcileApplyCompactsLegacyStateBeforeCleanup(t *testing.T) {
	base := t.TempDir()
	statePath := filepath.Join(base, "state.json")
	workspaceRoot := filepath.Join(base, "workspaces")
	wsPath := filepath.Join(workspaceRoot, "ws-old")
	if err := os.MkdirAll(wsPath, 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-31 * 24 * time.Hour)
	st := crstate.NewState()
	session := crstate.PublicSession{Repo: "o/r", PublicSessionID: "ps-old", AcpxRecordID: "rec-old", Status: crstate.StatusCompleted,
		Workspace: crstate.WorkspaceMetadata{ID: "ws-old", Path: wsPath, Repo: "o/r"}, CreatedAt: old, LastUsedAt: old}
	if err := st.UpsertPublicSession(session); err != nil {
		t.Fatal(err)
	}
	st.SchemaVersion = 1
	data, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	hash, err := storage.SessionRuntimeHash("o/r", "ps-old", wsPath)
	if err != nil {
		t.Fatal(err)
	}
	runtimeDir := filepath.Join(workspaceRoot, storage.SessionsDirName, hash)
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	if code := app.runRunner(context.Background(), []string{"storage", "reconcile", "--state", statePath, "--workspace-root", workspaceRoot, "--apply", "--json"}); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, errOut.String())
	}
	loaded, err := crstate.LoadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := loaded.GetPublicSession("o/r", "ps-old"); ok {
		t.Fatalf("expired terminal session was not compacted")
	}
	if _, err := os.Lstat(runtimeDir); !os.IsNotExist(err) {
		t.Fatalf("runtime was not reclaimed after compaction, err=%v", err)
	}
	backup := filepath.Join(workspaceRoot, storage.StorageDirName, "backups", "state-first.json")
	backupData, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(backupData, data) {
		t.Fatalf("raw migration backup differs from original state")
	}
}
