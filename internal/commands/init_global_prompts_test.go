package commands

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitGlobalPromptsDryRunPrintsAbsolutePathsWithoutWriting(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	target := filepath.Join(t.TempDir(), "preview-prompts")
	var out, errOut bytes.Buffer
	app := newInitTestApp(t, &out, &errOut)

	code := app.runInit(context.Background(), []string{
		"--repo", "o/r",
		"--tools", "none",
		"--skip-labels",
		"--global-prompts-dir", target,
		"--global-prompts-dry-run",
	})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "user-global prompt dry-run:") {
		t.Fatalf("stdout does not distinguish the global dry-run:\n%s", out.String())
	}
	for _, command := range []string{"propose", "apply", "review", "verify", "archive"} {
		path := filepath.Join(target, "issue-spec-"+command+".md")
		if !strings.Contains(out.String(), path) {
			t.Fatalf("stdout missing complete global prompt path %q:\n%s", path, out.String())
		}
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("global prompt dry-run wrote target directory, err=%v", err)
	}
}

func TestInitRejectsGlobalPromptsWithSkillsOnlyBeforeWrites(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	var out, errOut bytes.Buffer
	app := newInitTestApp(t, &out, &errOut)

	code := app.runInit(context.Background(), []string{
		"--repo", "o/r",
		"--tools", "codex",
		"--delivery", "skills",
		"--skip-labels",
		"--install-global-prompts",
	})
	if code != 2 {
		t.Fatalf("exit code = %d, want 2; stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if !strings.Contains(errOut.String(), "requires --delivery both or commands") {
		t.Fatalf("stderr missing delivery conflict: %q", errOut.String())
	}
	if _, err := os.Stat(filepath.Join(root, ".issue-spec")); !os.IsNotExist(err) {
		t.Fatalf("invalid global prompt options wrote project config, err=%v", err)
	}
}
