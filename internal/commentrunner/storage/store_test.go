package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	canonical, err := Canonicalize(root)
	if err != nil {
		t.Fatalf("canonicalize test root: %v", err)
	}
	return canonical
}

func wantIdentity(t *testing.T, root string) string {
	t.Helper()
	canonical, err := Canonicalize(root)
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

func TestOpenStoreMissingSidecarRebuilds(t *testing.T) {
	root := testRoot(t)
	store, err := OpenStore(root)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()
	if store.Status() != SidecarRebuilt {
		t.Fatalf("status = %q, want %q", store.Status(), SidecarRebuilt)
	}
	st := store.State()
	if st.SchemaVersion != SidecarSchemaVersion {
		t.Fatalf("schema = %d, want %d", st.SchemaVersion, SidecarSchemaVersion)
	}
	if st.RootIdentity != wantIdentity(t, root) {
		t.Fatalf("root identity = %q, want %q", st.RootIdentity, wantIdentity(t, root))
	}
	if len(st.Resources) != 0 {
		t.Fatalf("resources = %d, want 0", len(st.Resources))
	}
}

func TestStoreUpdatePersistsResourcesAtomically(t *testing.T) {
	root := testRoot(t)
	store, err := OpenStore(root)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	res := PhysicalResource{
		ID:              ResourceID(ResourceKindSessionRuntime, "o/r", "ps-1", strings.Repeat("ab", 16)),
		Kind:            ResourceKindSessionRuntime,
		Path:            filepath.Join(root, ".sessions", strings.Repeat("ab", 16)),
		Repo:            "o/r",
		PublicSessionID: "ps-1",
		PhysicalHash:    strings.Repeat("ab", 16),
		FirstObservedAt: now,
		CleanupState:    CleanupManaged,
	}
	if err := store.Update(func(st *StorageState) error {
		st.Resources[res.ID] = res
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	reopened, err := OpenStore(root)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	if reopened.Status() != SidecarReady {
		t.Fatalf("status = %q, want %q", reopened.Status(), SidecarReady)
	}
	got, ok := reopened.State().Resources[res.ID]
	if !ok {
		t.Fatalf("resource %q missing after reload", res.ID)
	}
	if got.Kind != res.Kind || got.Path != res.Path || got.CleanupState != CleanupManaged || !got.FirstObservedAt.Equal(now) {
		t.Fatalf("resource mismatch: %+v", got)
	}

	info, err := os.Stat(filepath.Join(root, ".storage", "state.json"))
	if err != nil {
		t.Fatalf("stat sidecar: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("sidecar mode = %o, want 600", info.Mode().Perm())
	}
}

func TestOpenStoreRootMismatchIsReportOnly(t *testing.T) {
	root := testRoot(t)
	store, err := OpenStore(root)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if err := store.Update(func(st *StorageState) error {
		st.Resources["session_runtime:o/r:ps-1:"+strings.Repeat("ab", 16)] = PhysicalResource{ID: "x"}
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	store.Close()

	// Rewrite the sidecar with a foreign root identity.
	sidecarPath := filepath.Join(root, ".storage", "state.json")
	data, err := os.ReadFile(sidecarPath)
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode sidecar: %v", err)
	}
	decoded["root_identity"] = strings.Repeat("ff", 32)
	raw, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("encode sidecar: %v", err)
	}
	if err := os.WriteFile(sidecarPath, raw, 0o600); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}

	foreign, err := OpenStore(root)
	if err != nil {
		t.Fatalf("OpenStore foreign: %v", err)
	}
	defer foreign.Close()
	if foreign.Status() != SidecarReportOnly {
		t.Fatalf("status = %q, want %q", foreign.Status(), SidecarReportOnly)
	}
	if err := foreign.Update(func(st *StorageState) error { return nil }); err == nil {
		t.Fatalf("Update on report-only store must fail")
	}
	// Report-only must not mutate the on-disk sidecar.
	after, err := os.ReadFile(sidecarPath)
	if err != nil {
		t.Fatalf("re-read sidecar: %v", err)
	}
	if string(after) != string(raw) {
		t.Fatalf("report-only store mutated the sidecar")
	}
}

func TestOpenStoreNewerSchemaIsReportOnly(t *testing.T) {
	root := testRoot(t)
	store, err := OpenStore(root)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if err := store.Update(func(st *StorageState) error { return nil }); err != nil {
		t.Fatalf("Update: %v", err)
	}
	store.Close()

	sidecarPath := filepath.Join(root, ".storage", "state.json")
	data, err := os.ReadFile(sidecarPath)
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode sidecar: %v", err)
	}
	decoded["schema_version"] = float64(SidecarSchemaVersion + 1)
	raw, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if err := os.WriteFile(sidecarPath, raw, 0o600); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}

	future, err := OpenStore(root)
	if err != nil {
		t.Fatalf("OpenStore newer: %v", err)
	}
	defer future.Close()
	if future.Status() != SidecarReportOnly {
		t.Fatalf("status = %q, want %q", future.Status(), SidecarReportOnly)
	}
	if err := future.Update(func(st *StorageState) error { return nil }); err == nil {
		t.Fatalf("Update on newer-schema store must fail")
	}
}

func TestOpenStoreCorruptSidecarRebuildsWithBackup(t *testing.T) {
	root := testRoot(t)
	store, err := OpenStore(root)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if err := store.Update(func(st *StorageState) error {
		st.Resources["session_runtime:o/r:ps-1:"+strings.Repeat("ab", 16)] = PhysicalResource{ID: "x"}
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	store.Close()

	sidecarPath := filepath.Join(root, ".storage", "state.json")
	if err := os.WriteFile(sidecarPath, []byte("{not-json"), 0o600); err != nil {
		t.Fatalf("corrupt sidecar: %v", err)
	}
	rebuilt, err := OpenStore(root)
	if err != nil {
		t.Fatalf("OpenStore corrupt: %v", err)
	}
	defer rebuilt.Close()
	if rebuilt.Status() != SidecarRebuilt {
		t.Fatalf("status = %q, want %q", rebuilt.Status(), SidecarRebuilt)
	}
	if len(rebuilt.State().Resources) != 0 {
		t.Fatalf("rebuilt sidecar must start empty, got %d resources", len(rebuilt.State().Resources))
	}
	matches, err := filepath.Glob(filepath.Join(root, ".storage", "backups", "sidecar-corrupt-*.json"))
	if err != nil || len(matches) == 0 {
		t.Fatalf("corrupt sidecar backup missing: matches=%v err=%v", matches, err)
	}
	// Rebuilt store accepts writes again.
	if err := rebuilt.Update(func(st *StorageState) error {
		st.Resources["session_runtime:o/r:ps-9:"+strings.Repeat("cd", 16)] = PhysicalResource{ID: "y"}
		return nil
	}); err != nil {
		t.Fatalf("Update on rebuilt store: %v", err)
	}
}

func TestResourceIDFormat(t *testing.T) {
	hash := strings.Repeat("ab", 16)
	if got := ResourceID(ResourceKindSessionRuntime, "o/r", "ps-1", hash); got != "session_runtime:o/r:ps-1:"+hash {
		t.Fatalf("runtime id = %q", got)
	}
	if got := ResourceID(ResourceKindSessionProcessPool, "o/r", "ps-1", hash); got != "session_process_pool:o/r:ps-1:"+hash {
		t.Fatalf("pool id = %q", got)
	}
	if got := ResourceID(ResourceKindSessionRuntime, "", "", hash); got != "session_runtime:::"+hash {
		t.Fatalf("unowned id = %q", got)
	}
}

func TestSessionRuntimeRootMatchesDispatcherAlgorithm(t *testing.T) {
	workspacePath := filepath.Join(string(filepath.Separator), "tmp", "root", "ws-1")
	sum := sha256.Sum256([]byte("o/r" + "\x00" + "ps-1" + "\x00" + workspacePath))
	want := filepath.Join(string(filepath.Separator), "tmp", "root", ".sessions", hex.EncodeToString(sum[:16]))
	got, err := SessionRuntimeRoot(workspacePath, "o/r", "ps-1")
	if err != nil {
		t.Fatalf("SessionRuntimeRoot: %v", err)
	}
	if got != want {
		t.Fatalf("runtime root = %q, want %q", got, want)
	}
	hash, err := SessionRuntimeHash("o/r", "ps-1", workspacePath)
	if err != nil {
		t.Fatalf("SessionRuntimeHash: %v", err)
	}
	if hash != hex.EncodeToString(sum[:16]) {
		t.Fatalf("runtime hash = %q", hash)
	}
}

func TestSessionProcessPoolHashMatchesDispatcherAlgorithm(t *testing.T) {
	canonical := filepath.Join(string(filepath.Separator), "tmp", "root", "ws-1")
	sum := sha256.Sum256([]byte("o/r" + "\x00" + "ps-1" + "\x00" + canonical))
	hash, err := SessionProcessPoolHash("o/r", "ps-1", canonical)
	if err != nil {
		t.Fatalf("SessionProcessPoolHash: %v", err)
	}
	if hash != hex.EncodeToString(sum[:16]) {
		t.Fatalf("pool hash = %q", hash)
	}
}
