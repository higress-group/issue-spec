package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/assignment"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/processworkspace"
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

func TestResolveReceiptRelationshipAuthorityUsesBoundChangeAndImmutableAssignmentCoverage(t *testing.T) {
	const (
		assignmentID = "issue-297-process-036-assignment-1"
		receiptID    = "receipt-verification-036"
	)
	assignmentDigest, receiptDigest := strings.Repeat("a", 64), strings.Repeat("b", 64)
	processBody := relationshipAuthorityProcessBody(t, "PROCESS-036", assignmentID, assignmentDigest)
	verifyBody, err := model.EnsureTypedBody("VERIFY", "VERIFY-036",
		"## Verification Summary: final\n\n### Covered SPECs\n\n- SPEC-005", model.BodyOptions{Status: "done"})
	if err != nil {
		t.Fatal(err)
	}
	verifyBody = strings.TrimRight(verifyBody, "\n") + "\n\n<!-- issue-spec:accepted-verification-receipt version=1 -->\n" +
		`{"receipt_id":"` + receiptID + `","receipt_digest":"` + receiptDigest + `","assignment_id":"` + assignmentID +
		`","assignment_digest":"` + assignmentDigest + `","assignment_generation":1}` +
		"\n<!-- /issue-spec:accepted-verification-receipt -->\n"
	specBody, _ := model.EnsureTypedBody("SPEC", "SPEC-005", "## Requirement: authority\n\nAuthority MUST be exact.", model.BodyOptions{Status: "confirmed"})
	wrongSpecBody, _ := model.EnsureTypedBody("SPEC", "SPEC-999", "## Requirement: wrong\n\nWrong MUST fail.", model.BodyOptions{Status: "confirmed"})
	ownerBody, _ := model.EnsureTypedBody("PROCESS", "PROCESS-032", "## Process: owner\n\n### Parent TASK\n\n- TASK-005", model.BodyOptions{Status: "done"})
	comment := func(issue int, id int64, body string) github.Comment {
		return github.Comment{ID: id, HTMLURL: fmt.Sprintf("https://github.com/o/r/issues/%d#issuecomment-%d", issue, id), Body: body}
	}
	baseComments := map[int][]github.Comment{
		295: {comment(295, 1, specBody), comment(295, 2, wrongSpecBody)},
		297: {comment(297, 3, processBody), comment(297, 4, verifyBody), comment(297, 5, ownerBody)},
		305: {comment(305, 6, specBody), {ID: 7, Body: "https://github.com/o/r/issues/296#issuecomment-7 https://github.com/o/r/issues/297#issuecomment-8"}},
	}
	changeIssues := map[int]github.Issue{
		295: {Number: 295, HTMLURL: "https://github.com/o/r/issues/295", Body: "<!-- issue-spec:issue=proposal change=change-295 version=1 -->"},
		296: {Number: 296, HTMLURL: "https://github.com/o/r/issues/296", Body: "<!-- issue-spec:issue=design change=change-295 version=1 -->\n- Proposal Issue: 295"},
		297: {Number: 297, HTMLURL: "https://github.com/o/r/issues/297", Body: "<!-- issue-spec:issue=implement change=change-295 version=1 -->\n- Design Issue: 296"},
		305: {Number: 305, HTMLURL: "https://github.com/o/r/issues/305", Body: "<!-- issue-spec:issue=proposal change=change-295 version=1 -->"},
	}
	projection := reconcile.ReceiptProjection{Version: 1, Repo: "o/r", Hostname: "github.com", Proposal: 295, Issue: 297,
		AcceptedReceipts: []reconcile.AcceptedReceiptProjection{{Role: assignment.RoleVerification,
			Carrier: reconcile.Target{Type: "VERIFY", ID: "VERIFY-036"}, ReceiptID: receiptID, ReceiptDigest: receiptDigest, Generation: 1,
			Lifecycle:       []reconcile.ReceiptLifecycle{{Target: reconcile.Target{Type: "VERIFY", ID: "VERIFY-036"}, Status: "done"}},
			CoverageTargets: []reconcile.Target{{Type: "SPEC", ID: "SPEC-005"}, {Type: "PROCESS", ID: "PROCESS-032"}},
			CurrentTargets:  []reconcile.Target{{Type: "PROCESS", ID: "PROCESS-036"}}}}}
	resolve := func(value reconcile.ReceiptProjection, comments map[int][]github.Comment) (reconcile.Plan, error) {
		plan, err := reconcile.CompileReceiptProjection(value)
		if err != nil {
			return plan, err
		}
		backend := fakeGitHubBackend{
			getIssue:          func(_ context.Context, _ string, issue int) (github.Issue, error) { return changeIssues[issue], nil },
			listIssueComments: func(_ context.Context, _ string, issue int) ([]github.Comment, error) { return comments[issue], nil },
		}
		located, err := locateReceiptProjectionChange(t.Context(), backend, plan.Repo, value)
		if err != nil {
			return plan, err
		}
		if err := resolvePlanRoles(&plan, located); err != nil {
			return plan, err
		}
		return plan, resolveReceiptRelationshipAuthority(t.Context(), backend, &plan, located)
	}
	plan, err := resolve(projection, baseComments)
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range plan.Operations {
		if operation.Kind != "link" {
			continue
		}
		authority := operation.Precondition.RelationshipAuthority
		if authority == nil || authority.AssignmentProcess.ID != "PROCESS-036" ||
			authority.AssignmentID != assignmentID || authority.CarrierURL != baseComments[297][1].HTMLURL ||
			strings.Contains(verifyBody, "issuecomment-1") || strings.Contains(verifyBody, "issuecomment-5") {
			t.Fatalf("resolved authority=%+v carrier=%s", authority, verifyBody)
		}
	}

	wrongID := projection
	wrongID.AcceptedReceipts = append([]reconcile.AcceptedReceiptProjection(nil), projection.AcceptedReceipts...)
	wrongID.AcceptedReceipts[0].CoverageTargets = []reconcile.Target{{Type: "SPEC", ID: "SPEC-999"}}
	if _, err := resolve(wrongID, baseComments); err == nil || !strings.Contains(err.Error(), "does not cover") {
		t.Fatalf("unauthorized same-type id err=%v", err)
	}
	duplicate := map[int][]github.Comment{295: append([]github.Comment(nil), baseComments[295]...), 297: baseComments[297]}
	duplicate[295] = append(duplicate[295], comment(295, 6, specBody))
	if _, err := resolve(projection, duplicate); err == nil || !strings.Contains(err.Error(), "2 canonical provider carriers") {
		t.Fatalf("duplicate target err=%v", err)
	}
	wrongIssue := projection
	wrongIssue.AcceptedReceipts = append([]reconcile.AcceptedReceiptProjection(nil), projection.AcceptedReceipts...)
	wrongIssue.AcceptedReceipts[0].CoverageTargets = []reconcile.Target{{Issue: 297, Type: "SPEC", ID: "SPEC-005"}}
	if _, err := resolve(wrongIssue, baseComments); err == nil || !strings.Contains(err.Error(), "outside canonical change issue 295") {
		t.Fatalf("wrong change issue err=%v", err)
	}
	wrongProposal := projection
	wrongProposal.Proposal = 305
	if _, err := resolve(wrongProposal, baseComments); err == nil || !strings.Contains(err.Error(), "differs from canonical proposal issue 295") {
		t.Fatalf("duplicate same-key proposal root err=%v", err)
	}
}

func relationshipAuthorityProcessBody(t *testing.T, processID, assignmentID, digest string) string {
	t.Helper()
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	revision := strings.Repeat("c", 40)
	workspace := processworkspace.PortableLease{SchemaVersion: processworkspace.LeaseSchemaVersion,
		WorkspaceID: "issue-297-process-036", Repository: "o/r", ProcessID: processID,
		ExecutionClass: processworkspace.ExecutionVerification, Mode: processworkspace.ModeSnapshot,
		BaseSHA: revision, DetachedRevision: revision, RuntimeNamespace: "issue-297-process-036",
		Assignment: &processworkspace.AssignmentBinding{SchemaVersion: assignment.AssignmentSchemaVersion,
			AssignmentID: assignmentID, Digest: digest, Role: assignment.RoleVerification,
			SubjectRevision: revision, Generation: 1},
		State: processworkspace.StatePrepared, CreatedAt: now, UpdatedAt: now}
	section, err := model.RenderProcessWorkspaceSection(workspace)
	if err != nil {
		t.Fatal(err)
	}
	input, err := assignment.ProcessInputJSON(assignment.ProcessInput{ScenarioSelectors: []assignment.ScenarioRef{{SpecID: "SPEC-005", Scenario: "authority"}}})
	if err != nil {
		t.Fatal(err)
	}
	logical := "## Process: verification\n\n### Parent TASK\n\n- TASK-005\n\n### Execution Class\n\n- verification\n\n" +
		"### Dependencies\n\n- PROCESS-032\n\n### Covers\n\n- SPEC-005\n\n### Assignment\n\n```json\n" + string(input) +
		"\n```\n\n" + section + "\n\n### Handoff\n\nN/A"
	body, err := model.EnsureTypedBody("PROCESS", processID, logical, model.BodyOptions{Status: "done"})
	if err != nil {
		t.Fatal(err)
	}
	return body
}
