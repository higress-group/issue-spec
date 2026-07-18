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

func TestReadReceiptProjectionRejectsReceiptContentAndCompilesIdentityOnly(t *testing.T) {
	valid := `{"version":1,"repo":"o/r","hostname":"issues.example","proposal":7,"issue":9,"allow_nonatomic":true,"accepted_receipts":[{` +
		`"role":"review","carrier":{"type":"REVIEW","id":"REVIEW-001"},"receipt_id":"receipt-review-1",` +
		`"receipt_digest":"` + strings.Repeat("a", 64) + `","generation":1,` +
		`"lifecycle":[{"target":{"type":"REVIEW","id":"REVIEW-001"},"status":"done"}],` +
		`"coverage_targets":[{"type":"SPEC","id":"SPEC-001"}],` +
		`"current_targets":[{"type":"PROCESS","id":"PROCESS-001"}]}]}`
	projection, err := readReceiptProjection("-", strings.NewReader(valid))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := reconcile.CompileReceiptProjection(projection)
	if err != nil || !plan.AllowNonAtomic || len(plan.Operations) != 3 || plan.Operations[0].Precondition.AcceptedReceipt == nil {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	for _, forbidden := range []string{"subject_revision", "provenance", "assurance", "content", "evidence_refs"} {
		candidate := strings.Replace(valid, `"role":"review"`, `"role":"review","`+forbidden+`":"forged"`, 1)
		if _, err := readReceiptProjection("-", strings.NewReader(candidate)); err == nil {
			t.Fatalf("projection accepted forbidden %s", forbidden)
		}
	}
}

func TestWorkflowReconcileRequiresExactlyOneInputKind(t *testing.T) {
	for _, args := range [][]string{nil, {"--plan", "a.json", "--projection", "b.json"}} {
		app, _, errOut := transitionAppWithError(nil)
		if code := app.runWorkflowReconcile(t.Context(), args); code != 2 ||
			!strings.Contains(errOut.String(), "exactly one of --plan or --projection") {
			t.Fatalf("args=%v code=%d err=%q", args, code, errOut.String())
		}
	}
}

func TestWorkflowReconcileNonAtomicFlagRequiresProjection(t *testing.T) {
	app, _, errOut := transitionAppWithError(nil)
	if code := app.runWorkflowReconcile(t.Context(), []string{"--plan", "unused.json", "--allow-nonatomic"}); code != 2 ||
		!strings.Contains(errOut.String(), "only valid with --projection") {
		t.Fatalf("code=%d err=%q", code, errOut.String())
	}
}
