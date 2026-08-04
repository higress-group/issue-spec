package processworkspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestInspectPoolProvesEmptyPool(t *testing.T) {
	repo, _ := newGitRepository(t)
	pool := filepath.Join(t.TempDir(), "pool")
	if err := os.MkdirAll(pool, 0o700); err != nil {
		t.Fatal(err)
	}
	inspection, err := InspectPool(context.Background(), repo, pool, ManagerOptions{})
	if err != nil {
		t.Fatalf("InspectPool: %v", err)
	}
	if !inspection.ClonePresent {
		t.Fatalf("clone must be present: %+v", inspection)
	}
	if !inspection.RegistryComplete {
		t.Fatalf("missing registry is complete (zero leases): %+v", inspection)
	}
	if !inspection.ProvenEmpty() {
		t.Fatalf("empty pool must be proven empty: %+v", inspection)
	}
}

func TestInspectPoolDetectsActiveLeaseAndWorktree(t *testing.T) {
	repo, base := newGitRepository(t)
	pool := filepath.Join(t.TempDir(), "pool")
	manager, err := OpenManager(context.Background(), repo, pool, ManagerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	lease := testLease("ws-a", "PROCESS-001", ModeWritable, "process-a", base, []string{"internal/a/**"})
	if _, err := manager.Prepare(context.Background(), PrepareRequest{Lease: lease}); err != nil {
		t.Fatal(err)
	}
	inspection, err := InspectPool(context.Background(), repo, pool, ManagerOptions{})
	if err != nil {
		t.Fatalf("InspectPool: %v", err)
	}
	if inspection.ProvenEmpty() {
		t.Fatalf("pool with active lease must not be proven empty: %+v", inspection)
	}
	if inspection.ActiveLeases == 0 {
		t.Fatalf("active lease not counted: %+v", inspection)
	}
	if inspection.RegisteredWorktrees == 0 {
		t.Fatalf("registered worktree not counted: %+v", inspection)
	}
	if inspection.FilesystemEntries == 0 {
		t.Fatalf("filesystem worktree not counted: %+v", inspection)
	}
	if inspection.OwnershipMarkers == 0 {
		t.Fatalf("ownership marker not counted: %+v", inspection)
	}
}

func TestInspectPoolDetectsCleanedLeaseAsEmpty(t *testing.T) {
	repo, base := newGitRepository(t)
	pool := filepath.Join(t.TempDir(), "pool")
	manager, err := OpenManager(context.Background(), repo, pool, ManagerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	lease := testLease("ws-b", "PROCESS-002", ModeWritable, "process-b", base, []string{"internal/b/**"})
	prepared, err := manager.Prepare(context.Background(), PrepareRequest{Lease: lease})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Cleanup(context.Background(), "ws-b", prepared.Lease.Owner.Token); err != nil {
		t.Fatal(err)
	}
	inspection, err := InspectPool(context.Background(), repo, pool, ManagerOptions{})
	if err != nil {
		t.Fatalf("InspectPool: %v", err)
	}
	if inspection.ActiveLeases != 0 {
		t.Fatalf("cleaned lease must not count as active: %+v", inspection)
	}
	if !inspection.ProvenEmpty() {
		t.Fatalf("fully cleaned pool must be proven empty: %+v", inspection)
	}
}

func TestInspectPoolCloneMissing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-clone")
	pool := filepath.Join(t.TempDir(), "pool")
	if err := os.MkdirAll(pool, 0o700); err != nil {
		t.Fatal(err)
	}
	inspection, err := InspectPool(context.Background(), missing, pool, ManagerOptions{})
	if err != nil {
		t.Fatalf("InspectPool must not fail for missing clone: %v", err)
	}
	if inspection.ClonePresent {
		t.Fatalf("clone must be reported missing: %+v", inspection)
	}
	if inspection.ProvenEmpty() {
		t.Fatalf("clone-missing pool is never proven empty")
	}
}

func TestInspectPoolDetectsStrayFilesystemEntries(t *testing.T) {
	repo, _ := newGitRepository(t)
	pool := filepath.Join(t.TempDir(), "pool")
	if err := os.MkdirAll(filepath.Join(pool, "ws-stray"), 0o700); err != nil {
		t.Fatal(err)
	}
	inspection, err := InspectPool(context.Background(), repo, pool, ManagerOptions{})
	if err != nil {
		t.Fatalf("InspectPool: %v", err)
	}
	if inspection.FilesystemEntries == 0 || inspection.ProvenEmpty() {
		t.Fatalf("stray pool entries must block proven-empty: %+v", inspection)
	}
}

func TestInspectPoolDetectsCorruptRegistry(t *testing.T) {
	repo, _ := newGitRepository(t)
	pool := filepath.Join(t.TempDir(), "pool")
	manager, err := OpenManager(context.Background(), repo, pool, ManagerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manager.Store.RegistryPath(), []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	inspection, err := InspectPool(context.Background(), repo, pool, ManagerOptions{})
	if err != nil {
		t.Fatalf("InspectPool: %v", err)
	}
	if inspection.RegistryComplete {
		t.Fatalf("corrupt registry must be incomplete: %+v", inspection)
	}
	if inspection.ProvenEmpty() {
		t.Fatalf("corrupt-registry pool is never proven empty")
	}
}
