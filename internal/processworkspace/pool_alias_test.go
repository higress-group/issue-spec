package processworkspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestInspectPoolPreservesActiveLeaseThroughParentSymlink(t *testing.T) {
	repo, base := newGitRepository(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	const hash = "0123456789abcdef0123456789abcdef"
	pool := filepath.Join(root, ".process-workspaces", hash)
	aliasPool := filepath.Join(alias, ".process-workspaces", hash)
	ctx := context.Background()
	manager, err := OpenManager(ctx, repo, pool, ManagerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := manager.Prepare(ctx, PrepareRequest{
		Lease: testLease("ws-alias", "PROCESS-001", ModeWritable, "process-alias", base, []string{"internal/a/**"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"present", "missing-worktree"} {
		if state == "missing-worktree" {
			// Recovery must still see the active registry lease and Git marker
			// when the worktree directory has disappeared.
			if err := os.RemoveAll(prepared.Lease.WorktreePath); err != nil {
				t.Fatal(err)
			}
		}
		for _, entry := range []struct{ name, path string }{{"canonical", pool}, {"alias", aliasPool}} {
			t.Run(state+"/"+entry.name, func(t *testing.T) {
				inspection, err := InspectPool(ctx, repo, entry.path, ManagerOptions{})
				if err != nil {
					t.Fatal(err)
				}
				if inspection.ActiveLeases != 1 || inspection.OwnershipMarkers != 1 || inspection.RegisteredWorktrees != 1 || inspection.ProvenEmpty() {
					t.Fatalf("active pool lost ownership evidence: %+v", inspection)
				}
				_, removed, err := RemoveEmptyPool(ctx, repo, entry.path, ManagerOptions{})
				if err != nil || removed {
					t.Fatalf("active pool removal: removed=%v err=%v", removed, err)
				}
				if _, err := os.Stat(pool); err != nil {
					t.Fatalf("active pool must remain: %v", err)
				}
			})
		}
	}
}

func TestRemoveEmptyPoolRejectsSymlinkTarget(t *testing.T) {
	repo, _ := newGitRepository(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	foreignRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const hash = "0123456789abcdef0123456789abcdef"
	pool := filepath.Join(root, ".process-workspaces", hash)
	foreignPool := filepath.Join(foreignRoot, ".process-workspaces", hash)
	if err := os.MkdirAll(foreignPool, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(pool), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(foreignPool, pool); err != nil {
		t.Fatal(err)
	}
	_, removed, err := RemoveEmptyPool(context.Background(), repo, pool, ManagerOptions{})
	if err == nil || removed {
		t.Fatalf("symlink deletion target must be rejected: removed=%v err=%v", removed, err)
	}
	if info, err := os.Lstat(pool); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("pool symlink must remain: info=%v err=%v", info, err)
	}
	if _, err := os.Stat(foreignPool); err != nil {
		t.Fatalf("foreign pool must remain: %v", err)
	}
}
