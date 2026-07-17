package processworkspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagerPrepareClassifiesExactBaseOwnership(t *testing.T) {
	repo, _ := newGitRepository(t)
	if err := os.MkdirAll(filepath.Join(repo, "tracked-dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "tracked-dir", "child.txt"), []byte("child\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "tracked-file"), []byte("file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("tracked-dir", filepath.Join(repo, "tracked-link")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	literalTree := ":(glob)literal-tree"
	if err := os.MkdirAll(filepath.Join(repo, literalTree), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, literalTree, "child.txt"), []byte("literal\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "--literal-pathspecs", "add", "--", "tracked-dir", "tracked-file", "tracked-link", literalTree)
	runGit(t, repo, "commit", "-m", "ownership fixtures")
	base := gitOutput(t, repo, "rev-parse", "HEAD")

	tests := []struct {
		name        string
		declaration string
		wantTree    bool
	}{
		{name: "tracked tree", declaration: "tracked-dir", wantTree: true},
		{name: "tracked blob", declaration: "tracked-file"},
		{name: "missing path", declaration: "future-path"},
		{name: "explicit recursive tree", declaration: "tracked-dir/**"},
		{name: "symlink remains blob", declaration: "tracked-link"},
		{name: "pathspec magic is literal", declaration: literalTree, wantTree: true},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := openTestManager(t, repo)
			id := fmt.Sprintf("ws-base-%d", index)
			lease := testLease(id, fmt.Sprintf("PROCESS-%03d", 100+index), ModeWritable,
				fmt.Sprintf("base-classification-%d", index), base, []string{test.declaration})
			before := gitOutput(t, repo, "worktree", "list", "--porcelain")
			inspection, err := manager.Prepare(context.Background(), PrepareRequest{Lease: lease})
			if !test.wantTree {
				if err != nil || !inspection.Registered || !inspection.Present || inspection.Head != base {
					t.Fatalf("Prepare(%q)=%+v err=%v", test.declaration, inspection, err)
				}
				return
			}

			if !errors.Is(err, ErrAmbiguousOwnership) {
				t.Fatalf("Prepare(%q) err=%v", test.declaration, err)
			}
			want := fmt.Sprintf("%v: PROCESS %s write-ownership declaration %q resolves to a tracked tree at base %s; declare %q for recursive ownership",
				ErrAmbiguousOwnership, lease.Portable.ProcessID, test.declaration, base, test.declaration+"/**")
			if err.Error() != want {
				t.Fatalf("diagnostic=%q\nwant=%q", err, want)
			}
			_, retryErr := manager.Prepare(context.Background(), PrepareRequest{Lease: lease})
			if retryErr == nil || retryErr.Error() != err.Error() {
				t.Fatalf("diagnostic changed across retry: first=%q retry=%q", err, retryErr)
			}
			if _, found, getErr := manager.Store.Get(context.Background(), id); getErr != nil || found {
				t.Fatalf("rejected ownership created lease: found=%v err=%v", found, getErr)
			}
			path, pathErr := manager.workspacePath(id)
			if pathErr != nil {
				t.Fatal(pathErr)
			}
			if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("rejected ownership created worktree path: %v", statErr)
			}
			if after := gitOutput(t, repo, "worktree", "list", "--porcelain"); after != before {
				t.Fatalf("rejected ownership mutated worktrees:\nbefore=%s\nafter=%s", before, after)
			}
			if branch := gitOutput(t, repo, "branch", "--list", lease.Portable.Branch); branch != "" {
				t.Fatalf("rejected ownership created branch %q", branch)
			}
			markers := gitOutput(t, repo, "for-each-ref", "--format=%(refname)", "refs/issue-spec/process-workspaces/"+id+"/")
			if strings.TrimSpace(markers) != "" {
				t.Fatalf("rejected ownership created marker %q", markers)
			}
		})
	}
}
