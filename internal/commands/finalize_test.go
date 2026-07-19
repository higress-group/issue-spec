package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/finalization"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/reconcile"
)

const (
	finalizeTestHead         = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	finalizeTestBaseline     = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	finalizeTestProviderBase = "dddddddddddddddddddddddddddddddddddddddd"
)

type finalizeCommandBackend struct {
	fakeGitHubBackend
	pr       github.PullRequest
	issues   map[int]github.Issue
	comments map[int][]github.Comment
	writes   int
}

func (b *finalizeCommandBackend) GetIssue(_ context.Context, _ string, number int) (github.Issue, error) {
	issue, ok := b.issues[number]
	if !ok {
		return github.Issue{}, errors.New("missing issue")
	}
	return issue, nil
}

func (b *finalizeCommandBackend) ListIssueComments(_ context.Context, _ string, number int) ([]github.Comment, error) {
	return append([]github.Comment(nil), b.comments[number]...), nil
}

func (b *finalizeCommandBackend) GetPullRequest(context.Context, string, int) (github.PullRequest, error) {
	return b.pr, nil
}

func (*finalizeCommandBackend) ListPullRequestReviewComments(context.Context, string, int) ([]github.PullRequestReviewComment, error) {
	return []github.PullRequestReviewComment{}, nil
}

func (*finalizeCommandBackend) GetCombinedStatus(context.Context, string, string) (github.CombinedStatus, error) {
	return github.CombinedStatus{State: "success", Statuses: []github.Status{}}, nil
}

func (*finalizeCommandBackend) ListCheckRuns(context.Context, string, string) ([]github.CheckRun, error) {
	return []github.CheckRun{}, nil
}

func (b *finalizeCommandBackend) ListPullRequestCommits(context.Context, string, int) ([]github.PullRequestCommit, error) {
	return []github.PullRequestCommit{{SHA: b.pr.Head.SHA}}, nil
}

func (b *finalizeCommandBackend) CreateComment(context.Context, string, int, string) (github.Comment, error) {
	b.writes++
	return github.Comment{}, errors.New("preview attempted a write")
}

func (b *finalizeCommandBackend) UpdateComment(context.Context, string, int64, string) (github.Comment, error) {
	b.writes++
	return github.Comment{}, errors.New("preview attempted a write")
}

func TestFinalizePreviewIsWriteFreeDeterministicAndDetailOmitsBodies(t *testing.T) {
	backend := finalizePreviewFixture()
	application := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	application.selectGitHubBackend = ghSelection
	application.newGitHubBackend = func(context.Context, auth.GitHubBackendSelection) (github.Backend, error) { return backend, nil }
	var resolvedProviderBases []string
	application.resolveFinalizationBaseline = func(_ context.Context, subject, providerBase string) (string, error) {
		if subject != finalizeTestHead {
			t.Fatalf("baseline subject=%s", subject)
		}
		resolvedProviderBases = append(resolvedProviderBases, providerBase)
		return finalizeTestBaseline, nil
	}
	directory := t.TempDir()
	intentPath := filepath.Join(directory, "intent.json")
	if err := os.WriteFile(intentPath, []byte(`{"version":1,"baseline_revision":"`+finalizeTestBaseline+`","superseded_by":[{"from":"PROCESS-001","to":"PROCESS-002"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	preview := func(name string) finalization.Plan {
		planPath := filepath.Join(directory, name)
		application.out, application.err = &bytes.Buffer{}, &bytes.Buffer{}
		code := application.runFinalizePreview(t.Context(), []string{"--repo", "o/r", "--proposal", "1", "--design", "2", "--implement", "3", "--pr", "9",
			"--intent-file", intentPath, "--plan-out", planPath, "--json"})
		if code != 0 {
			t.Fatalf("preview code=%d stderr=%s", code, application.err.(*bytes.Buffer).String())
		}
		file, err := os.Open(planPath)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		plan, err := finalization.ReadPlan(file)
		if err != nil {
			t.Fatal(err)
		}
		return plan
	}
	first, second := preview("plan-1.json"), preview("plan-2.json")
	if backend.writes != 0 {
		t.Fatalf("preview provider writes=%d", backend.writes)
	}
	if first.PlanDigest != second.PlanDigest || first.Subject.BaselineRevision != finalizeTestBaseline ||
		first.Subject.ProviderBaseRevision != finalizeTestProviderBase {
		t.Fatalf("plans differ or baseline drifted: first=%+v second=%+v", first.Subject, second.Subject)
	}
	if len(resolvedProviderBases) != 2 || resolvedProviderBases[0] != finalizeTestProviderBase || resolvedProviderBases[1] != finalizeTestProviderBase {
		t.Fatalf("baseline resolver inputs=%v", resolvedProviderBases)
	}
	application.out, application.err = &bytes.Buffer{}, &bytes.Buffer{}
	if code := application.runFinalizeDetail([]string{"--plan", filepath.Join(directory, "plan-1.json"), "--json"}); code != 0 {
		t.Fatalf("detail code=%d stderr=%s", code, application.err.(*bytes.Buffer).String())
	}
	detail := application.out.(*bytes.Buffer).String()
	if strings.Contains(detail, "issue-spec:superseded-by") || strings.Contains(detail, `"desired"`) {
		t.Fatalf("detail leaked mutation bodies: %s", detail)
	}
}

func TestFinalizePreviewAndApplyBindSharedGateToExactImplementIssue(t *testing.T) {
	backend := finalizeImplementBoundaryFixture(t)
	application := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	application.selectGitHubBackend = ghSelection
	application.newGitHubBackend = func(context.Context, auth.GitHubBackendSelection) (github.Backend, error) { return backend, nil }
	application.resolveFinalizationBaseline = func(context.Context, string, string) (string, error) { return finalizeTestBaseline, nil }
	directory := t.TempDir()
	intentPath, planPath := filepath.Join(directory, "intent.json"), filepath.Join(directory, "plan.json")
	if err := os.WriteFile(intentPath, []byte(`{"version":1,"baseline_revision":"`+finalizeTestBaseline+`","superseded_by":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := application.runFinalizePreview(t.Context(), []string{"--repo", "o/r", "--proposal", "1", "--design", "2", "--implement", "3", "--pr", "9",
		"--intent-file", intentPath, "--plan-out", planPath}); code != 0 {
		t.Fatalf("preview code=%d stderr=%s", code, application.err.(*bytes.Buffer).String())
	}
	data, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	var plan finalization.Plan
	if err := json.Unmarshal(data, &plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.Blockers) != 0 {
		t.Fatalf("persisted Proposal/Design PROCESS comments became preview gate carriers: %+v", plan.Blockers)
	}
	if len(plan.Selection.ActiveProcessIDs) != 1 || plan.Selection.ActiveProcessIDs[0] != "PROCESS-001" {
		t.Fatalf("preview active PROCESS selection=%v", plan.Selection.ActiveProcessIDs)
	}
	if len(plan.Reconcile.Operations) != 0 {
		t.Fatalf("already-final lifecycle unexpectedly produced mutations: %+v", plan.Reconcile.Operations)
	}
	// Keep one frozen, non-gate blocker so the persisted plan has a non-empty
	// blocker representation. Apply must still publish the independent final
	// shared-gate observation, which is the boundary under test here.
	plan.Blockers = []finalization.Blocker{{Code: "manual-approval", Message: "manual approval remains pending"}}
	plan.PlanDigest = ""
	plan.PlanDigest, err = finalization.DigestPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	data, err = finalization.CanonicalJSON(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(planPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	application.out, application.err = &bytes.Buffer{}, &bytes.Buffer{}
	code := application.runFinalizeApply(t.Context(), []string{"--plan", planPath,
		"--checkpoint", filepath.Join(directory, "checkpoint.json"), "--allow-nonatomic", "--json"})
	if code != 1 {
		t.Fatalf("apply code=%d stderr=%s stdout=%s", code, application.err.(*bytes.Buffer).String(), application.out.(*bytes.Buffer).String())
	}
	var result finalizeApplyResult
	if err := json.Unmarshal(application.out.(*bytes.Buffer).Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.FinalGateReady {
		t.Fatalf("persisted Proposal/Design PROCESS comments became apply final-gate carriers: %+v", result)
	}
	if len(result.FinalSelection.ActiveProcessIDs) != 1 || result.FinalSelection.ActiveProcessIDs[0] != "PROCESS-001" {
		t.Fatalf("apply final PROCESS selection=%v", result.FinalSelection.ActiveProcessIDs)
	}
	if backend.writes != 0 {
		t.Fatalf("already-final apply provider writes=%d", backend.writes)
	}
}

func TestFinalizeApplyRejectsSubjectDriftBeforeWrite(t *testing.T) {
	backend := finalizePreviewFixture()
	application := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	application.selectGitHubBackend = ghSelection
	application.newGitHubBackend = func(context.Context, auth.GitHubBackendSelection) (github.Backend, error) { return backend, nil }
	application.resolveFinalizationBaseline = func(context.Context, string, string) (string, error) { return finalizeTestBaseline, nil }
	directory := t.TempDir()
	intentPath, planPath := filepath.Join(directory, "intent.json"), filepath.Join(directory, "plan.json")
	if err := os.WriteFile(intentPath, []byte(`{"version":1,"baseline_revision":"`+finalizeTestBaseline+`","superseded_by":[{"from":"PROCESS-001","to":"PROCESS-002"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := application.runFinalizePreview(t.Context(), []string{"--repo", "o/r", "--proposal", "1", "--design", "2", "--implement", "3", "--pr", "9",
		"--intent-file", intentPath, "--plan-out", planPath}); code != 0 {
		t.Fatalf("preview code=%d", code)
	}
	backend.pr.Head.SHA = "cccccccccccccccccccccccccccccccccccccccc"
	application.out, application.err = &bytes.Buffer{}, &bytes.Buffer{}
	code := application.runFinalizeApply(t.Context(), []string{"--plan", planPath, "--checkpoint", filepath.Join(directory, "checkpoint.json"), "--allow-nonatomic"})
	if code != 1 || backend.writes != 0 || !strings.Contains(application.err.(*bytes.Buffer).String(), "subject identity differs") {
		t.Fatalf("apply code=%d writes=%d stderr=%s", code, backend.writes, application.err.(*bytes.Buffer).String())
	}
}

func TestFinalizeApplyRejectsProviderBaseDriftBeforeWrite(t *testing.T) {
	backend := finalizePreviewFixture()
	application := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	application.selectGitHubBackend = ghSelection
	application.newGitHubBackend = func(context.Context, auth.GitHubBackendSelection) (github.Backend, error) { return backend, nil }
	application.resolveFinalizationBaseline = func(context.Context, string, string) (string, error) { return finalizeTestBaseline, nil }
	directory := t.TempDir()
	intentPath, planPath := filepath.Join(directory, "intent.json"), filepath.Join(directory, "plan.json")
	if err := os.WriteFile(intentPath, []byte(`{"version":1,"baseline_revision":"`+finalizeTestBaseline+`","superseded_by":[{"from":"PROCESS-001","to":"PROCESS-002"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := application.runFinalizePreview(t.Context(), []string{"--repo", "o/r", "--proposal", "1", "--design", "2", "--implement", "3", "--pr", "9",
		"--intent-file", intentPath, "--plan-out", planPath}); code != 0 {
		t.Fatalf("preview code=%d", code)
	}
	backend.pr.Base.SHA = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	application.out, application.err = &bytes.Buffer{}, &bytes.Buffer{}
	code := application.runFinalizeApply(t.Context(), []string{"--plan", planPath, "--checkpoint", filepath.Join(directory, "checkpoint.json"), "--allow-nonatomic"})
	if code != 1 || backend.writes != 0 || !strings.Contains(application.err.(*bytes.Buffer).String(), "provider base revision differs") {
		t.Fatalf("apply code=%d writes=%d stderr=%s", code, backend.writes, application.err.(*bytes.Buffer).String())
	}
}

func TestRunRemainingFinalizationOperationsResumesCheckpoint(t *testing.T) {
	firstBody := finalizeTypedBody("PROCESS", "PROCESS-001", "done", nil, "first")
	secondBody := finalizeTypedBody("PROCESS", "PROCESS-002", "in-progress", nil, "second")
	comments := []github.Comment{
		{ID: 1, HTMLURL: "https://github.com/o/r/issues/3#issuecomment-1", URL: "https://api.github.com/comments/1", Body: firstBody},
		{ID: 2, HTMLURL: "https://github.com/o/r/issues/3#issuecomment-2", URL: "https://api.github.com/comments/2", Body: secondBody},
	}
	updates := []int64{}
	backend := fakeGitHubBackend{
		listIssueComments: func(context.Context, string, int) ([]github.Comment, error) {
			return append([]github.Comment(nil), comments...), nil
		},
		updateComment: func(_ context.Context, _ string, id int64, body string) (github.Comment, error) {
			updates = append(updates, id)
			for i := range comments {
				if comments[i].ID == id {
					comments[i].Body = body
					return comments[i], nil
				}
			}
			return github.Comment{}, errors.New("missing")
		},
	}
	plan := reconcile.Plan{Version: reconcile.PlanVersion, Repo: "o/r", AllowNonAtomic: true, Operations: []reconcile.Operation{
		{ID: "one", Kind: "transition", Target: reconcile.Target{Issue: 3, Type: "PROCESS", ID: "PROCESS-001"}, Desired: reconcile.Desired{Status: "done"}},
		{ID: "two", Kind: "transition", DependsOn: []string{"one"}, Target: reconcile.Target{Issue: 3, Type: "PROCESS", ID: "PROCESS-002"}, Desired: reconcile.Desired{Status: "done"},
			Precondition: reconcile.Precondition{BodyDigest: model.RepresentationDigest(secondBody)}},
	}}
	_, digest, err := reconcile.Validate(plan)
	if err != nil {
		t.Fatal(err)
	}
	plan.PlanDigest = digest
	checkpointPath := filepath.Join(t.TempDir(), "checkpoint.json")
	checkpoint := reconcile.Checkpoint{Version: 1, PlanDigest: digest, Completed: map[string]string{"one": "updated"}}
	if err := reconcile.SaveCheckpoint(checkpointPath, checkpoint); err != nil {
		t.Fatal(err)
	}
	result, err := runRemainingFinalizationOperations(t.Context(), backend, plan, checkpointPath, checkpoint)
	if err != nil || !result.OK {
		t.Fatalf("resume result=%+v err=%v", result, err)
	}
	if len(updates) != 1 || updates[0] != 2 || model.ParseTypedComment(comments[1].Body).Status != "done" {
		t.Fatalf("updates=%v comments=%+v", updates, comments)
	}
	reloaded, err := reconcile.LoadCheckpoint(checkpointPath, digest)
	if err != nil || len(reloaded.Completed) != 2 {
		t.Fatalf("checkpoint=%+v err=%v", reloaded, err)
	}
}

func TestValidateFinalizationRepresentationsAcceptsOnlyPlannedHalfLink(t *testing.T) {
	leftURL := "https://github.com/o/r/issues/3#issuecomment-1"
	rightURL := "https://github.com/o/r/issues/3#issuecomment-2"
	leftBefore := finalizeTypedBody("PROCESS", "PROCESS-001", "in-progress", nil, "left")
	rightBefore := finalizeTypedBody("PROCESS", "PROCESS-002", "in-progress", nil, "right")
	leftAfter, changed, err := model.AddRelatedCommentLink(leftBefore, rightURL)
	if err != nil || !changed {
		t.Fatalf("plan half-link: changed=%v err=%v", changed, err)
	}
	comments := []github.Comment{
		{ID: 1, HTMLURL: leftURL, URL: "https://api.github.com/comments/1", Body: leftAfter},
		{ID: 2, HTMLURL: rightURL, URL: "https://api.github.com/comments/2", Body: rightBefore},
	}
	backend := fakeGitHubBackend{listIssueComments: func(context.Context, string, int) ([]github.Comment, error) {
		return append([]github.Comment(nil), comments...), nil
	}}
	leftTarget := reconcile.Target{Issue: 3, Type: "PROCESS", ID: "PROCESS-001"}
	rightTarget := reconcile.Target{Issue: 3, Type: "PROCESS", ID: "PROCESS-002"}
	plan := finalization.Plan{Repository: "o/r", Representations: []finalization.Representation{
		{Issue: 3, CommentID: 1, URL: leftURL, APIURL: comments[0].URL, Type: "PROCESS", ID: "PROCESS-001", RepresentationDigest: model.RepresentationDigest(leftBefore)},
		{Issue: 3, CommentID: 2, URL: rightURL, APIURL: comments[1].URL, Type: "PROCESS", ID: "PROCESS-002", RepresentationDigest: model.RepresentationDigest(rightBefore)},
	}, Reconcile: reconcile.Plan{Operations: []reconcile.Operation{{ID: "link", Kind: "link", Target: leftTarget,
		Desired: reconcile.Desired{Peer: &rightTarget}, Precondition: reconcile.Precondition{Endpoints: []reconcile.EndpointPrecondition{
			{Target: leftTarget, BodyDigest: model.RepresentationDigest(leftBefore), AfterDigest: model.RepresentationDigest(leftAfter)},
			{Target: rightTarget, BodyDigest: model.RepresentationDigest(rightBefore), AfterDigest: model.RepresentationDigest(rightBefore)},
		}}}}}}
	if _, err := validateFinalizationRepresentations(t.Context(), backend, plan, map[string]string{}); err != nil {
		t.Fatalf("planned half-link was not resumable: %v", err)
	}
	comments[0].Body = leftAfter + "unplanned drift\n"
	if _, err := validateFinalizationRepresentations(t.Context(), backend, plan, map[string]string{}); err == nil {
		t.Fatal("unplanned half-link representation drift was accepted")
	}
}

func finalizePreviewFixture() *finalizeCommandBackend {
	taskURL := "https://github.com/o/r/issues/1#issuecomment-10"
	p1URL := "https://github.com/o/r/issues/3#issuecomment-11"
	p2URL := "https://github.com/o/r/issues/3#issuecomment-12"
	prURL := "https://github.com/o/r/pull/9"
	backend := &finalizeCommandBackend{issues: map[int]github.Issue{}, comments: map[int][]github.Comment{}}
	backend.info = github.BackendInfo{Name: "fake", Kind: "test", Host: "github.com"}
	backend.issues[1] = github.Issue{Number: 1, HTMLURL: "https://github.com/o/r/issues/1", Body: "<!-- issue-spec:issue=proposal change=x version=1 -->\n"}
	backend.issues[2] = github.Issue{Number: 2, HTMLURL: "https://github.com/o/r/issues/2", Body: "<!-- issue-spec:issue=design change=x version=1 -->\n- Proposal Issue: 1\n"}
	backend.issues[3] = github.Issue{Number: 3, HTMLURL: "https://github.com/o/r/issues/3", Body: "<!-- issue-spec:issue=implement change=x version=1 -->\n- Design Issue: 2\n"}
	backend.pr = github.PullRequest{Number: 9, HTMLURL: prURL}
	backend.pr.Head.SHA, backend.pr.Head.Ref = finalizeTestHead, "feature"
	backend.pr.Base.SHA, backend.pr.Base.Ref = finalizeTestProviderBase, "main"
	backend.comments[1] = []github.Comment{{ID: 10, HTMLURL: taskURL, URL: "https://api.github.com/comments/10",
		Body: finalizeTypedBody("TASK", "TASK-001", "confirmed", map[string][]string{"Related Comments": {p1URL, p2URL}}, "task")}}
	backend.comments[3] = []github.Comment{
		{ID: 11, HTMLURL: p1URL, URL: "https://api.github.com/comments/11", Body: finalizeTypedBody("PROCESS", "PROCESS-001", "in-progress",
			map[string][]string{"Related Comments": {taskURL}, "PR": {prURL}}, "### Execution Class\n\nchange-bearing")},
		{ID: 12, HTMLURL: p2URL, URL: "https://api.github.com/comments/12", Body: finalizeTypedBody("PROCESS", "PROCESS-002", "in-progress",
			map[string][]string{"Related Comments": {taskURL}, "PR": {prURL}}, "### Execution Class\n\nchange-bearing")},
	}
	return backend
}

func finalizeImplementBoundaryFixture(t *testing.T) *finalizeCommandBackend {
	t.Helper()
	backend := finalizePreviewFixture()
	prURL := backend.pr.HTMLURL
	spec := typedArtifact(t, 1, "SPEC", "SPEC-001", "confirmed", "## Requirement: X\n\nX MUST work.\n\n### Scenario: ok\n\n- **WHEN** x\n- **THEN** y")
	task := typedArtifact(t, 2, "TASK", "TASK-001", "done", canonicalTaskContent)
	currentContent := strings.Replace(canonicalProcessContentWithClass(model.ProcessExecutionOrchestration),
		"### Handoff\n\nN/A", "### Handoff\n\nCoordinates the exact final lifecycle without changing code.", 1)
	currentContent = strings.Replace(currentContent, "### Covers\n\n- TASK-001", "### Covers\n\n- TASK-001\n- SPEC-001", 1)
	current := typedArtifact(t, 3, "PROCESS", "PROCESS-001", "done", currentContent)
	foreignProposal := typedArtifact(t, 1, "PROCESS", "PROCESS-901", "done", canonicalProcessContentWithClass(model.ProcessExecutionChangeBearing))
	foreignDesign := typedArtifact(t, 2, "PROCESS", "PROCESS-902", "done", canonicalProcessContentWithClass(model.ProcessExecutionChangeBearing))
	verify := typedArtifact(t, 3, "VERIFY", "VERIFY-001", "done", canonicalVerifyContent)
	artifacts := []*model.Artifact{&spec, &task, &current, &foreignProposal, &foreignDesign, &verify}
	for index, artifact := range artifacts {
		artifact.URL = fmt.Sprintf("https://github.com/o/r/issues/%d#issuecomment-%d", artifact.Issue, 100+index)
		artifact.APIURL = fmt.Sprintf("https://api.github.com/comments/%d", 100+index)
	}
	linkArtifacts(t, &spec, &task)
	linkArtifacts(t, &task, &current)
	linkArtifacts(t, &spec, &current)
	linkArtifacts(t, &task, &foreignProposal)
	linkArtifacts(t, &task, &foreignDesign)
	currentLinks := make(map[string][]string, len(current.Comment.Links)+1)
	for name, urls := range current.Comment.Links {
		currentLinks[name] = append([]string(nil), urls...)
	}
	currentLinks["PR"] = []string{prURL}
	currentBody, err := model.EnsureTypedBody("PROCESS", current.Comment.ID, model.LogicalBody(current.Comment.Body),
		model.BodyOptions{Status: current.Comment.Status, Links: currentLinks})
	if err != nil {
		t.Fatal(err)
	}
	current.Comment = model.ParseTypedComment(currentBody)
	backend.comments = map[int][]github.Comment{}
	for index, artifact := range artifacts {
		backend.comments[artifact.Issue] = append(backend.comments[artifact.Issue], github.Comment{
			ID: int64(100 + index), HTMLURL: artifact.URL, URL: artifact.APIURL, Body: artifact.Comment.Body,
		})
	}
	return backend
}

func finalizeTypedBody(typ, id, status string, links map[string][]string, logical string) string {
	return model.RenderMarker(typ, id, 1) + "\n" + model.RenderHeader(typ, id, model.BodyOptions{
		Agent: "Worker", Status: status, Scope: "test", Links: links,
	}) + "\n\n" + logical + "\n"
}

var _ github.Backend = (*finalizeCommandBackend)(nil)
var _ github.PullRequestCommitBackend = (*finalizeCommandBackend)(nil)
