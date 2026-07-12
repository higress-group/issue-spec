package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/reconcile"
)

func TestWorkflowReconcileCLIResolvesBodyFileAndCreates(t *testing.T) {
	dir := t.TempDir()
	body, err := model.EnsureTypedBody("TASK", "TASK-001", "## Work\n\nCLI", model.BodyOptions{Agent: "Worker", Status: "confirmed", Scope: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "task.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	plan := reconcile.Plan{Version: 1, Repo: "o/r", AllowNonAtomic: true, Operations: []reconcile.Operation{{ID: "create", Kind: "upsert", Target: reconcile.Target{Issue: 5, Type: "TASK", ID: "TASK-001"}, Desired: reconcile.Desired{BodyFile: "task.md"}}}}
	data, _ := json.Marshal(plan)
	planPath := filepath.Join(dir, "plan.json")
	if err := os.WriteFile(planPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	created := 0
	var comments []github.Comment
	backend := fakeGitHubBackend{info: github.BackendInfo{Name: "fake"}, listIssueComments: func(context.Context, string, int) ([]github.Comment, error) { return comments, nil }, createComment: func(_ context.Context, _ string, _ int, value string) (github.Comment, error) {
		created++
		comment := github.Comment{ID: 7, HTMLURL: "https://x/c/7", Body: value}
		comments = append(comments, comment)
		return comment, nil
	}}
	app, out := transitionApp(backend)
	if code := app.runWorkflow(context.Background(), []string{"reconcile", "--plan", planPath, "--checkpoint", filepath.Join(dir, "checkpoint.json"), "--json"}); code != 0 {
		t.Fatalf("code=%d out=%s", code, out.String())
	}
	if created != 1 || !strings.Contains(out.String(), `"created": 1`) || !strings.Contains(out.String(), `"plan_digest"`) {
		t.Fatalf("created=%d out=%s", created, out.String())
	}
}

func TestReadReconcilePlanRejectsUnknownField(t *testing.T) {
	if _, err := readReconcilePlan("-", strings.NewReader(`{"version":1,"repo":"o/r","operations":[],"surprise":true}`)); err == nil {
		t.Fatal("expected unknown field error")
	}
}
