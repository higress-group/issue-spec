package commands

import (
	"bytes"
	"context"
	"errors"
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
	finalizeTestHead     = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	finalizeTestBaseline = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
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
	application.resolveFinalizationBaseline = func(context.Context, string, string) (string, error) { return finalizeTestBaseline, nil }
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
	if first.PlanDigest != second.PlanDigest || first.Subject.BaselineRevision != finalizeTestBaseline {
		t.Fatalf("plans differ or baseline drifted: first=%+v second=%+v", first.Subject, second.Subject)
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
	backend.pr.Head.SHA, backend.pr.Head.Ref, backend.pr.Base.Ref = finalizeTestHead, "feature", "main"
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

func finalizeTypedBody(typ, id, status string, links map[string][]string, logical string) string {
	return model.RenderMarker(typ, id, 1) + "\n" + model.RenderHeader(typ, id, model.BodyOptions{
		Agent: "Worker", Status: status, Scope: "test", Links: links,
	}) + "\n\n" + logical + "\n"
}

var _ github.Backend = (*finalizeCommandBackend)(nil)
var _ github.PullRequestCommitBackend = (*finalizeCommandBackend)(nil)
