package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDurableWorkflowSnapshotRejectsUncommittedOrUnsafeGuidance(t *testing.T) {
	for _, kind := range []string{"uncommitted", "missing", "symlink", "escape"} {
		t.Run(kind, func(t *testing.T) {
			root, _ := durableCommandRepository(t)
			guide := "guidance.md"
			if kind == "escape" {
				guide = "../guidance.md"
			}
			config := "schema: issue-spec\ndurable_specs:\n  mode: repository\nguidance:\n  shared: [" + guide + "]\n"
			if err := os.WriteFile(filepath.Join(root, "issue-spec/config.yaml"), []byte(config), 0o644); err != nil {
				t.Fatal(err)
			}
			if kind == "symlink" {
				if err := os.Symlink("../outside.md", filepath.Join(root, guide)); err != nil {
					t.Fatal(err)
				}
				durableCommandGit(t, root, "add", guide)
			}
			durableCommandGit(t, root, "add", "issue-spec/config.yaml")
			durableCommandGit(t, root, "commit", "-q", "-m", "declare unavailable subject guidance")
			subject := durableCommandGitOutput(t, root, "rev-parse", "HEAD")
			// A valid local file must never satisfy a missing exact-subject dependency.
			if kind == "uncommitted" {
				if err := os.WriteFile(filepath.Join(root, guide), []byte("Working tree only.\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			before := durableCommandGitOutput(t, root, "status", "--porcelain=v1")
			_, err := observeDurableWorkflowAtRevision(context.Background(), root, subject)
			if err == nil || !strings.Contains(err.Error(), "guidance") {
				t.Fatalf("unsafe or uncommitted guidance accepted: %v", err)
			}
			if after := durableCommandGitOutput(t, root, "status", "--porcelain=v1"); after != before {
				t.Fatalf("snapshot observation mutated checkout: before=%q after=%q", before, after)
			}
		})
	}
}
