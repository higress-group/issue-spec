package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/durable"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
)

func TestDurableSpecPreviewDetailApplyKeepsSidecarOutsideWorktree(t *testing.T) {
	root, baseline := durableCommandRepository(t)
	specBody := durableCommandSpec(t, `{"version":1,"intent":"OPERATIONS","operations":[{"id":"SPEC-001-OP-01","kind":"ADDED","capability":"cap","path":"issue-spec/specs/cap/spec.md","new_requirement":"Durable command","projection":{"source":"current-spec"}}]}`)
	backend := durableCommandBackend(specBody)
	planPath := filepath.Join(filepath.Dir(root), "sidecars", "durable-plan.json")

	application, out, errOut := durableCommandApp(backend)
	code := application.runDurableSpec(context.Background(), []string{"preview", "--repo", "o/r", "--proposal", "9",
		"--baseline", baseline, "--root", root, "--plan-out", planPath, "--json"})
	if code != 0 {
		t.Fatalf("preview code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	var compact durable.CompactPlan
	if err := json.Unmarshal(out.Bytes(), &compact); err != nil {
		t.Fatal(err)
	}
	if !compact.OK || compact.PlanPath != planPath || compact.OperationCount != 1 || compact.FileCount != 1 {
		t.Fatalf("compact=%+v", compact)
	}
	if _, err := os.Stat(planPath); err != nil {
		t.Fatal(err)
	}
	if relative, err := filepath.Rel(root, planPath); err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		t.Fatalf("plan was written inside worktree: %s", planPath)
	}

	application, detailOut, detailErr := durableCommandApp(backend)
	if code := application.runDurableSpec(context.Background(), []string{"detail", "--plan", planPath, "--json"}); code != 0 {
		t.Fatalf("detail code=%d stdout=%q stderr=%q", code, detailOut.String(), detailErr.String())
	}
	if strings.Contains(detailOut.String(), `"postimage"`) || strings.Contains(detailOut.String(), `"content"`) {
		t.Fatalf("detail leaked complete durable file postimages: %s", detailOut.String())
	}

	application, applyOut, applyErr := durableCommandApp(backend)
	if code := application.runDurableSpec(context.Background(), []string{"apply", "--plan", planPath,
		"--expected-plan-digest", compact.PlanDigest, "--root", root, "--json"}); code != 0 {
		t.Fatalf("apply code=%d stdout=%q stderr=%q", code, applyOut.String(), applyErr.String())
	}
	target := filepath.Join(root, "issue-spec", "specs", "cap", "spec.md")
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "### Requirement: Durable command") || !strings.Contains(string(data), "Source SPEC comments:") {
		t.Fatalf("unexpected durable postimage:\n%s", data)
	}

	application, retryOut, retryErr := durableCommandApp(backend)
	if code := application.runDurableSpec(context.Background(), []string{"apply", "--plan", planPath,
		"--expected-plan-digest", compact.PlanDigest, "--root", root, "--json"}); code != 0 {
		t.Fatalf("retry code=%d stdout=%q stderr=%q", code, retryOut.String(), retryErr.String())
	}
	if !strings.Contains(retryOut.String(), `"unchanged": 1`) {
		t.Fatalf("retry did not recognize complete postimage: %s", retryOut.String())
	}
}

func TestDurableSpecPreviewEmitsBoundedExecutableBlockerDetail(t *testing.T) {
	root, baseline := durableCommandRepository(t)
	specBody := durableCommandSpec(t, `{"version":1,"intent":"OPERATIONS","operations":[{"id":"SPEC-001-OP-01","kind":"MODIFIED","capability":"cap","path":"issue-spec/specs/cap/spec.md","current_requirement":"Durable command","projection":{"source":"current-spec"}}]}`)
	backend := durableCommandBackend(specBody)
	planPath := filepath.Join(filepath.Dir(root), "blocked-plan.json")
	application, out, errOut := durableCommandApp(backend)
	if code := application.runDurableSpec(context.Background(), []string{"preview", "--repo", "o/r", "--proposal", "9",
		"--baseline", baseline, "--root", root, "--plan-out", planPath, "--json"}); code != 0 {
		t.Fatalf("preview code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	var compact durable.CompactPlan
	if err := json.Unmarshal(out.Bytes(), &compact); err != nil {
		t.Fatal(err)
	}
	if compact.OK || compact.BlockerCount != 1 || len(compact.Blockers) != 1 || compact.Blockers[0].Code != durable.BlockTargetMissing ||
		!strings.Contains(compact.Blockers[0].DetailAction, "--code target_missing") {
		t.Fatalf("compact=%+v", compact)
	}
	application, detailOut, detailErr := durableCommandApp(backend)
	if code := application.runDurableSpec(context.Background(), []string{"detail", "--plan", planPath,
		"--code", durable.BlockTargetMissing, "--json"}); code != 0 {
		t.Fatalf("detail code=%d stdout=%q stderr=%q", code, detailOut.String(), detailErr.String())
	}
	if !strings.Contains(detailOut.String(), `"operation_id": "SPEC-001-OP-01"`) || !strings.Contains(detailOut.String(), `"code": "target_missing"`) {
		t.Fatalf("detail=%s", detailOut.String())
	}

	inside := filepath.Join(root, "blocked-plan.json")
	application, _, insideErr := durableCommandApp(backend)
	if code := application.runDurableSpec(context.Background(), []string{"preview", "--repo", "o/r", "--proposal", "9",
		"--baseline", baseline, "--root", root, "--plan-out", inside}); code != 2 || !strings.Contains(insideErr.String(), "outside") {
		t.Fatalf("inside plan code=%d stderr=%q", code, insideErr.String())
	}
}

func durableCommandRepository(t *testing.T) (string, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(root, "issue-spec"), 0o755); err != nil {
		t.Fatal(err)
	}
	config := "schema: issue-spec\ndurable_specs:\n  mode: repository\n"
	if err := os.WriteFile(filepath.Join(root, "issue-spec", "config.yaml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	durableCommandGit(t, root, "init", "-q")
	durableCommandGit(t, root, "config", "user.name", "Durable Test")
	durableCommandGit(t, root, "config", "user.email", "durable@example.test")
	durableCommandGit(t, root, "add", "issue-spec/config.yaml")
	durableCommandGit(t, root, "commit", "-q", "-m", "baseline")
	return root, durableCommandGitOutput(t, root, "rev-parse", "HEAD")
}

func durableCommandSpec(t *testing.T, intent string) string {
	t.Helper()
	logical := "## Requirement: Durable command\n\nThe command MUST project durable contracts.\n\n### Scenario: projection\n\n- **WHEN** preview and apply run\n- **THEN** the contract is projected\n\n## Durable Intent\n\n```json\n" + intent + "\n```"
	body, err := model.EnsureTypedBody("SPEC", "SPEC-001", logical,
		model.BodyOptions{Agent: "Author", Status: "confirmed", Scope: "durable command test"})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func durableCommandBackend(specBody string) github.Backend {
	return fakeGitHubBackend{info: github.BackendInfo{Name: "test", Kind: "test", Host: "github.com"},
		listIssueComments: func(context.Context, string, int) ([]github.Comment, error) {
			return []github.Comment{{ID: 7, HTMLURL: "https://github.com/o/r/issues/9#issuecomment-7",
				URL: "https://api.github.com/repos/o/r/issues/comments/7", Body: specBody}}, nil
		}}
}

func durableCommandApp(backend github.Backend) (*app, *bytes.Buffer, *bytes.Buffer) {
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	application := newApp(strings.NewReader(""), out, errOut)
	application.selectGitHubBackend = func(context.Context, string) (auth.GitHubBackendSelection, error) {
		return auth.GitHubBackendSelection{Mode: auth.GitHubBackendModeGH, Name: auth.GitHubBackendNameGH,
			Kind: auth.GitHubBackendKindCLI, Host: "github.com", SelectionSource: "test"}, nil
	}
	application.newGitHubBackend = func(context.Context, auth.GitHubBackendSelection) (github.Backend, error) { return backend, nil }
	return application, out, errOut
}

func durableCommandGit(t *testing.T, root string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func durableCommandGitOutput(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(output))
}
