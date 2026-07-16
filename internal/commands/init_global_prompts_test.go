package commands

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/auth"
)

func TestInitGlobalPromptsDryRunPrintsAbsolutePathsWithoutWriting(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	target := filepath.Join(t.TempDir(), "preview-prompts")
	var out, errOut bytes.Buffer
	app := newInitTestApp(t, &out, &errOut)

	code := app.runInit(context.Background(), []string{
		"--repo", "o/r",
		"--tools", "codex",
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

func TestInitToolsNoneRejectsEveryGlobalPromptOptionBeforeBackendSelection(t *testing.T) {
	for mask := 1; mask < 8; mask++ {
		t.Run(fmt.Sprintf("combination-%d", mask), func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			target := filepath.Join(root, "prompts")
			var out, errOut bytes.Buffer
			app := newInitTestApp(t, &out, &errOut)
			backendSelections := 0
			app.selectGitHubBackend = func(context.Context, string) (auth.GitHubBackendSelection, error) {
				backendSelections++
				return auth.GitHubBackendSelection{}, fmt.Errorf("backend selection must not run")
			}
			args := []string{"--repo", "o/r", "--tools", " NoNe "}
			if mask&1 != 0 {
				args = append(args, "--install-global-prompts")
			}
			if mask&2 != 0 {
				args = append(args, "--global-prompts-dir", target)
			}
			if mask&4 != 0 {
				args = append(args, "--global-prompts-dry-run")
			}
			if code := app.runInit(context.Background(), args); code != 2 {
				t.Fatalf("exit code = %d, stdout=%q stderr=%q", code, out.String(), errOut.String())
			}
			if backendSelections != 0 {
				t.Fatalf("backend selections = %d", backendSelections)
			}
			if !strings.Contains(errOut.String(), "cannot be combined with explicit --tools none") {
				t.Fatalf("stderr missing tools-none conflict: %q", errOut.String())
			}
			for _, path := range []string{filepath.Join(root, ".issue-spec"), target} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("conflict created %q: %v", path, err)
				}
			}
		})
	}

	for _, args := range [][]string{
		{"--install-global-prompts=false"},
		{"--global-prompts-dir="},
		{"--global-prompts-dry-run=false"},
	} {
		t.Run(args[0], func(t *testing.T) {
			t.Chdir(t.TempDir())
			var out, errOut bytes.Buffer
			app := newInitTestApp(t, &out, &errOut)
			fullArgs := append([]string{"--repo", "o/r", "--tools", "none"}, args...)
			if code := app.runInit(context.Background(), fullArgs); code != 2 {
				t.Fatalf("exit code = %d, stdout=%q stderr=%q", code, out.String(), errOut.String())
			}
			if !strings.Contains(errOut.String(), "cannot be combined with explicit --tools none") {
				t.Fatalf("stderr missing tools-none conflict: %q", errOut.String())
			}
		})
	}
}

func TestSelfHostedInitToolsNoneRejectsGlobalPromptOptionsBeforeNetwork(t *testing.T) {
	for mask := 1; mask < 8; mask++ {
		t.Run(fmt.Sprintf("combination-%d", mask), func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			var out, errOut bytes.Buffer
			app := newApp(strings.NewReader(""), &out, &errOut)
			options := selfHostedInitOptions{Repo: "o/r", Tools: "NONE"}
			if mask&1 != 0 {
				options.InstallGlobalPrompts = true
			}
			if mask&2 != 0 {
				options.GlobalPromptsDir = filepath.Join(root, "prompts")
			}
			if mask&4 != 0 {
				options.GlobalPromptsDryRun = true
			}
			profile := auth.Profile{Kind: auth.ProfileKindHosted, Hostname: "network-must-not-run.invalid"}
			if code := app.runSelfHostedInit(context.Background(), profile, options); code != 2 {
				t.Fatalf("exit code = %d, stdout=%q stderr=%q", code, out.String(), errOut.String())
			}
			if !strings.Contains(errOut.String(), "cannot be combined with explicit --tools none") {
				t.Fatalf("stderr missing tools-none conflict: %q", errOut.String())
			}
			if _, err := os.Stat(filepath.Join(root, ".issue-spec")); !os.IsNotExist(err) {
				t.Fatalf("conflict created local state: %v", err)
			}
		})
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
