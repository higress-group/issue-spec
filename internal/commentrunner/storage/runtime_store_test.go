package storage

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenRuntimeStoreMissingRebuilds(t *testing.T) {
	root := testRoot(t)
	store, err := OpenRuntimeStore(root)
	if err != nil {
		t.Fatalf("OpenRuntimeStore: %v", err)
	}
	defer store.Close()
	if store.Status() != SidecarRebuilt {
		t.Fatalf("status = %q, want %q", store.Status(), SidecarRebuilt)
	}
	st := store.State()
	if st.SchemaVersion != RuntimeSchemaVersion {
		t.Fatalf("schema = %d, want %d", st.SchemaVersion, RuntimeSchemaVersion)
	}
	if st.RootIdentity != wantIdentity(t, root) {
		t.Fatalf("root identity = %q, want %q", st.RootIdentity, wantIdentity(t, root))
	}
	if len(st.Homes) != 0 || len(st.Scratch) != 0 || len(st.Migrations) != 0 {
		t.Fatalf("fresh state must have empty maps: %+v", st)
	}
}

func TestRuntimeStoreUpdatePersistsAtomically(t *testing.T) {
	root := testRoot(t)
	store, err := OpenRuntimeStore(root)
	if err != nil {
		t.Fatalf("OpenRuntimeStore: %v", err)
	}
	home := RuntimeHomeRecord{
		Hash: strings.Repeat("ab", 16), Path: filepath.Join(root, RunnerHomesDirName, strings.Repeat("ab", 16)),
		Hostname: "host-1", Repo: "o/r", Runner: "runner-1", CreatedAt: time.Now().UTC(),
	}
	if err := store.Update(func(st *RuntimeState) error {
		st.Homes[home.Hash] = home
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if store.Status() != SidecarReady {
		t.Fatalf("status after update = %q, want %q", store.Status(), SidecarReady)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := OpenRuntimeStore(root)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	if reopened.Status() != SidecarReady {
		t.Fatalf("reopened status = %q, want %q", reopened.Status(), SidecarReady)
	}
	got, ok := reopened.State().Homes[home.Hash]
	if !ok || got != home {
		t.Fatalf("home record = %+v ok=%v, want %+v", got, ok, home)
	}
}

func TestRuntimeStoreUpdatePinsSchemaAndRoot(t *testing.T) {
	root := testRoot(t)
	store, err := OpenRuntimeStore(root)
	if err != nil {
		t.Fatalf("OpenRuntimeStore: %v", err)
	}
	defer store.Close()
	if err := store.Update(func(st *RuntimeState) error {
		st.SchemaVersion = RuntimeSchemaVersion + 1
		return nil
	}); err == nil {
		t.Fatalf("schema bump must be rejected")
	}
	if err := store.Update(func(st *RuntimeState) error {
		st.RootIdentity = strings.Repeat("0", 64)
		return nil
	}); err == nil {
		t.Fatalf("root identity change must be rejected")
	}
}

func TestRuntimeStoreCorruptBackupAndRebuild(t *testing.T) {
	root := testRoot(t)
	store, err := OpenRuntimeStore(root)
	if err != nil {
		t.Fatalf("OpenRuntimeStore: %v", err)
	}
	if err := store.Update(func(st *RuntimeState) error {
		st.Scratch["job-aaaaaaaaaaaaaaaa"] = JobScratchRecord{JobID: "job-aaaaaaaaaaaaaaaa", CleanupState: CleanupManaged}
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	path := store.Path()
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	corrupt := []byte("{not json")
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatalf("corrupt: %v", err)
	}
	rebuilt, err := OpenRuntimeStore(root)
	if err != nil {
		t.Fatalf("reopen corrupt: %v", err)
	}
	defer rebuilt.Close()
	if rebuilt.Status() != SidecarRebuilt {
		t.Fatalf("status = %q, want %q", rebuilt.Status(), SidecarRebuilt)
	}
	if rebuilt.LoadCause() == nil {
		t.Fatalf("load cause must explain the rebuild")
	}
	backup := filepath.Join(root, StorageDirName, backupDirName, runtimeCorruptBackup)
	data, err := os.ReadFile(backup)
	if err != nil {
		t.Fatalf("corrupt backup missing: %v", err)
	}
	if string(data) != string(corrupt) {
		t.Fatalf("corrupt backup content mismatch")
	}
	if len(rebuilt.State().Scratch) != 0 {
		t.Fatalf("rebuilt state must drop corrupt records")
	}
}

func TestRuntimeStoreNewerSchemaReportOnly(t *testing.T) {
	root := testRoot(t)
	path := filepath.Join(root, StorageDirName, runtimeFileName)
	payload := []byte(`{"schema_version":2,"root_identity":"` + wantIdentity(t, root) + `"}`)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	store, err := OpenRuntimeStore(root)
	if err != nil {
		t.Fatalf("OpenRuntimeStore: %v", err)
	}
	defer store.Close()
	if store.Status() != SidecarReportOnly {
		t.Fatalf("status = %q, want %q", store.Status(), SidecarReportOnly)
	}
	if store.State().SchemaVersion != 2 {
		t.Fatalf("report-only state must expose the newer schema for inventory")
	}
	if err := store.Update(func(st *RuntimeState) error { return nil }); !errors.Is(err, ErrReportOnly) {
		t.Fatalf("Update err = %v, want ErrReportOnly", err)
	}
}

func TestRuntimeStoreForeignRootReportOnly(t *testing.T) {
	root := testRoot(t)
	path := filepath.Join(root, StorageDirName, runtimeFileName)
	payload := []byte(`{"schema_version":1,"root_identity":"` + strings.Repeat("0", 64) + `"}`)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	store, err := OpenRuntimeStore(root)
	if err != nil {
		t.Fatalf("OpenRuntimeStore: %v", err)
	}
	defer store.Close()
	if store.Status() != SidecarReportOnly {
		t.Fatalf("status = %q, want %q", store.Status(), SidecarReportOnly)
	}
	if err := store.Update(func(st *RuntimeState) error { return nil }); !errors.Is(err, ErrReportOnly) {
		t.Fatalf("Update err = %v, want ErrReportOnly", err)
	}
}

func TestRuntimeStoreSequentialWritersPreserved(t *testing.T) {
	root := testRoot(t)
	first, err := OpenRuntimeStore(root)
	if err != nil {
		t.Fatalf("open first: %v", err)
	}
	defer first.Close()
	second, err := OpenRuntimeStore(root)
	if err != nil {
		t.Fatalf("open second: %v", err)
	}
	defer second.Close()
	if err := first.Update(func(st *RuntimeState) error {
		st.Homes["home-1"] = RuntimeHomeRecord{Hash: "home-1"}
		return nil
	}); err != nil {
		t.Fatalf("first update: %v", err)
	}
	if err := second.Update(func(st *RuntimeState) error {
		st.Scratch["job-cccccccccccccccc"] = JobScratchRecord{JobID: "job-cccccccccccccccc", CleanupState: CleanupManaged}
		return nil
	}); err != nil {
		t.Fatalf("second update: %v", err)
	}
	if err := first.Reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	st := first.State()
	if _, ok := st.Homes["home-1"]; !ok {
		t.Fatalf("first writer's home record lost: %+v", st.Homes)
	}
	if _, ok := st.Scratch["job-cccccccccccccccc"]; !ok {
		t.Fatalf("second writer's scratch record lost: %+v", st.Scratch)
	}
}

func TestRuntimeStoreInvalidCleanupStateRebuilds(t *testing.T) {
	root := testRoot(t)
	path := filepath.Join(root, StorageDirName, runtimeFileName)
	payload := []byte(`{"schema_version":1,"root_identity":"` + wantIdentity(t, root) + `","scratch":{"job-x":{"job_id":"job-x","cleanup_state":"bogus"}}}`)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	store, err := OpenRuntimeStore(root)
	if err != nil {
		t.Fatalf("OpenRuntimeStore: %v", err)
	}
	defer store.Close()
	if store.Status() != SidecarRebuilt {
		t.Fatalf("status = %q, want %q for invalid cleanup state", store.Status(), SidecarRebuilt)
	}
}
