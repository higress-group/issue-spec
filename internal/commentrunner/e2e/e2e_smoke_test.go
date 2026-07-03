package e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestGatedGitHubRunnerNewResumeFallbackSmoke(t *testing.T) {
	env := requireGate(t, "ISSUE_SPEC_E2E_GITHUB", "real GitHub notification/comment fallback runner dry-run", "ISSUE_SPEC_E2E_REPO", "ISSUE_SPEC_E2E_RUNNER")
	root := repoRoot(t)
	temp := t.TempDir()
	args := []string{
		"runner", "poll",
		"--repo", env["ISSUE_SPEC_E2E_REPO"],
		"--runner", env["ISSUE_SPEC_E2E_RUNNER"],
		"--state", filepath.Join(temp, "state.json"),
		"--workspace-root", filepath.Join(temp, "workspaces"),
		"--poll-interval", "1s",
		"--fallback-interval", "1s",
		"--once",
		"--dry-run",
		"--unsafe-no-sandbox",
		"--json",
	}
	if acpxPath := strings.TrimSpace(os.Getenv("ISSUE_SPEC_E2E_ACPX_PATH")); acpxPath != "" {
		args = append(args, "--acpx-path", acpxPath)
	}
	runIssueSpec(t, root, args...)
}

func TestGatedBwrapCLIAuthCreateUpsertSmoke(t *testing.T) {
	env := requireGate(t, "ISSUE_SPEC_E2E_BWRAP_CLI", "real bwrap plus issue-spec CLI auth and comment upsert smoke", "ISSUE_SPEC_E2E_REPO", "ISSUE_SPEC_E2E_ISSUE", "ISSUE_SPEC_E2E_RUNNER")
	root := repoRoot(t)
	temp := t.TempDir()
	preflightArgs := []string{
		"runner", "preflight",
		"--repo", env["ISSUE_SPEC_E2E_REPO"],
		"--runner", env["ISSUE_SPEC_E2E_RUNNER"],
		"--state", filepath.Join(temp, "state.json"),
		"--workspace-root", filepath.Join(temp, "workspaces"),
		"--gh-config-dir", filepath.Join(temp, "gh"),
		"--json",
	}
	if bwrapPath := strings.TrimSpace(os.Getenv("ISSUE_SPEC_E2E_BWRAP_PATH")); bwrapPath != "" {
		preflightArgs = append(preflightArgs, "--bwrap-path", bwrapPath)
	}
	if acpxPath := strings.TrimSpace(os.Getenv("ISSUE_SPEC_E2E_ACPX_PATH")); acpxPath != "" {
		preflightArgs = append(preflightArgs, "--acpx-path", acpxPath)
	}
	runIssueSpec(t, root, preflightArgs...)
	runIssueSpec(t, root, "auth", "status", "--json")

	bodyPath := filepath.Join(temp, "process.md")
	body := "## E2E Smoke\n\nGated bwrap CLI smoke wrote this PROCESS test artifact.\n"
	if err := os.WriteFile(bodyPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	runIssueSpec(t, root,
		"comment", "upsert",
		"--repo", env["ISSUE_SPEC_E2E_REPO"],
		"--issue", env["ISSUE_SPEC_E2E_ISSUE"],
		"--type", "PROCESS",
		"--id", "E2E-SMOKE",
		"--status", "done",
		"--body-file", bodyPath,
		"--json",
	)
}

func TestGatedAcpxCoordinatorCLIDirectSmoke(t *testing.T) {
	env := requireGate(t, "ISSUE_SPEC_E2E_ACPX_COORDINATOR", "real acpx coordinator CLI-direct artifact smoke", "ISSUE_SPEC_E2E_ACPX_PATH")
	runExternal(t, env["ISSUE_SPEC_E2E_ACPX_PATH"], "--help")
	if raw := strings.TrimSpace(os.Getenv("ISSUE_SPEC_E2E_ACPX_COORDINATOR_ARGS")); raw != "" {
		runExternal(t, env["ISSUE_SPEC_E2E_ACPX_PATH"], strings.Fields(raw)...)
	}
}

func TestGatedCodexNativeWorkersSmoke(t *testing.T) {
	env := requireGate(t, "ISSUE_SPEC_E2E_CODEX_NATIVE", "real Codex native worker capability smoke", "ISSUE_SPEC_E2E_CODEX_COMMAND")
	runExternal(t, env["ISSUE_SPEC_E2E_CODEX_COMMAND"], envArgs("ISSUE_SPEC_E2E_CODEX_ARGS", "--version")...)
}

func TestGatedClaudeTaskAgentsSmoke(t *testing.T) {
	env := requireGate(t, "ISSUE_SPEC_E2E_CLAUDE_TASK", "real Claude Task agent capability smoke", "ISSUE_SPEC_E2E_CLAUDE_COMMAND")
	runExternal(t, env["ISSUE_SPEC_E2E_CLAUDE_COMMAND"], envArgs("ISSUE_SPEC_E2E_CLAUDE_ARGS", "--version")...)
}

func requireGate(t *testing.T, gate, reason string, required ...string) map[string]string {
	t.Helper()
	if os.Getenv(gate) != "1" {
		t.Skipf("%s=1 not set; skipping opt-in real-path smoke for %s", gate, reason)
	}
	values := map[string]string{}
	for _, name := range required {
		value := strings.TrimSpace(os.Getenv(name))
		if value == "" {
			t.Skipf("%s=1 set but %s is empty; skipping %s", gate, name, reason)
		}
		values[name] = value
	}
	return values
}

func runIssueSpec(t *testing.T, root string, args ...string) {
	t.Helper()
	goArgs := append([]string{"run", "./cmd/issue-spec"}, args...)
	runCommand(t, root, "go", goArgs...)
}

func runExternal(t *testing.T, binary string, args ...string) {
	t.Helper()
	runCommand(t, "", binary, args...)
}

func runCommand(t *testing.T, dir, binary string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("%s %s timed out: %v", binary, strings.Join(args, " "), ctx.Err())
	}
	if err != nil {
		t.Fatalf("%s %s failed: %v\n%s", binary, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
}

func envArgs(name string, fallback ...string) []string {
	if raw := strings.TrimSpace(os.Getenv(name)); raw != "" {
		return strings.Fields(raw)
	}
	return fallback
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}
