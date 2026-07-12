package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/templates"
)

type conditionalTransitionBackend struct {
	fakeGitHubBackend
	observe func(context.Context, string, int64) (github.CommentRepresentation, error)
	update  func(context.Context, string, int64, int64, string) (github.CommentRepresentation, error)
}

func (b conditionalTransitionBackend) GetCommentRepresentation(ctx context.Context, repo string, id int64) (github.CommentRepresentation, error) {
	return b.observe(ctx, repo, id)
}
func (b conditionalTransitionBackend) UpdateCommentConditional(ctx context.Context, repo string, id, expected int64, body string) (github.CommentRepresentation, error) {
	return b.update(ctx, repo, id, expected, body)
}

func TestCommentTransitionStrictSuccessAndUnchanged(t *testing.T) {
	body := transitionCommandBody(t, "confirmed")
	writes := 0
	var strictUpdated string
	backend := conditionalTransitionBackend{fakeGitHubBackend: transitionFake(body),
		observe: func(context.Context, string, int64) (github.CommentRepresentation, error) {
			return github.CommentRepresentation{Comment: github.Comment{ID: 7, Body: body}, RepresentationVersion: 2, Guarantee: github.CommentMutationStrictConditional}, nil
		},
		update: func(_ context.Context, _ string, _ int64, expected int64, updated string) (github.CommentRepresentation, error) {
			writes++
			strictUpdated = updated
			if expected != 2 || model.ParseTypedComment(updated).Status != "in-progress" {
				t.Fatalf("expected=%d body=%s", expected, updated)
			}
			if !strings.Contains(updated, "https://example.test/pr/7") || !strings.Contains(updated, "handoff ready") {
				t.Fatalf("declared mutations missing: %s", updated)
			}
			return github.CommentRepresentation{Comment: github.Comment{ID: 7, HTMLURL: "https://example.test/c/7", Body: updated}, RepresentationVersion: 3}, nil
		}}
	app, out := transitionApp(backend)
	handoff := writeTempInput(t, "handoff ready")
	if code := app.runCommentTransition(context.Background(), []string{"--repo", "o/r", "--issue", "5", "--id", "PROCESS-001", "--to", "in-progress", "--expected-version", "2", "--handoff-file", handoff, "--pr", "https://example.test/pr/7", "--json"}); code != 0 {
		t.Fatalf("code=%d out=%s", code, out.String())
	}
	var got commentTransitionResult
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Atomic || got.Guarantee != github.CommentMutationStrictConditional || got.Action != "updated" || got.RepresentationVersion != 3 || writes != 1 {
		t.Fatalf("got=%+v writes=%d", got, writes)
	}

	// Desired state already present: no PATCH.
	backend.observe = func(context.Context, string, int64) (github.CommentRepresentation, error) {
		return github.CommentRepresentation{Comment: github.Comment{ID: 7, Body: strictUpdated}, RepresentationVersion: 3}, nil
	}
	app, out = transitionApp(backend)
	if code := app.runCommentTransition(context.Background(), []string{"--repo", "o/r", "--issue", "5", "--id", "PROCESS-001", "--to", "in-progress", "--json"}); code != 0 {
		t.Fatal(out.String())
	}
	if !strings.Contains(out.String(), `"action": "unchanged"`) || writes != 1 {
		t.Fatalf("out=%s writes=%d", out.String(), writes)
	}
}

func TestCommentTransitionConflictAndStrictUnsupportedDoNotWrite(t *testing.T) {
	body := transitionCommandBody(t, "confirmed")
	writes := 0
	strict := conditionalTransitionBackend{fakeGitHubBackend: transitionFake(body),
		observe: func(context.Context, string, int64) (github.CommentRepresentation, error) {
			return github.CommentRepresentation{Comment: github.Comment{ID: 7, Body: body}, RepresentationVersion: 4}, nil
		},
		update: func(context.Context, string, int64, int64, string) (github.CommentRepresentation, error) {
			writes++
			return github.CommentRepresentation{}, nil
		}}
	app, out := transitionApp(strict)
	if code := app.runCommentTransition(context.Background(), []string{"--repo", "o/r", "--issue", "5", "--id", "PROCESS-001", "--to", "done", "--expected-version", "3", "--json"}); code != 1 {
		t.Fatalf("code=%d out=%s", code, out.String())
	}
	if !strings.Contains(out.String(), `"expected": 3`) || !strings.Contains(out.String(), `"current": 4`) || writes != 0 {
		t.Fatalf("out=%s writes=%d", out.String(), writes)
	}

	plain := transitionFake(body)
	app, _ = transitionApp(plain)
	if code := app.runCommentTransition(context.Background(), []string{"--repo", "o/r", "--issue", "5", "--id", "PROCESS-001", "--to", "done"}); code != 1 || writes != 0 {
		t.Fatalf("code=%d writes=%d", code, writes)
	}
}

func TestCommentTransitionExplicitNonAtomicReportsDigests(t *testing.T) {
	body := transitionCommandBody(t, "confirmed")
	var updated string
	plain := transitionFake(body)
	plain.updateComment = func(_ context.Context, _ string, _ int64, value string) (github.Comment, error) {
		updated = value
		return github.Comment{ID: 7, HTMLURL: "https://example.test/c/7", Body: value}, nil
	}
	app, out := transitionApp(plain)
	args := []string{"--repo", "o/r", "--issue", "5", "--id", "PROCESS-001", "--to", "done", "--expected-digest", bodyDigest(body), "--allow-nonatomic", "--json"}
	if code := app.runCommentTransition(context.Background(), args); code != 0 {
		t.Fatalf("code=%d out=%s", code, out.String())
	}
	var got commentTransitionResult
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Atomic || got.Guarantee != github.CommentMutationNonAtomicSingleWriter || got.BeforeDigest != bodyDigest(body) || got.AfterDigest == got.BeforeDigest || model.ParseTypedComment(updated).Status != "done" {
		t.Fatalf("got=%+v updated=%s", got, updated)
	}
}

func TestQuestionResolveKeepsCompatibilityOutputThroughTransitionPrimitive(t *testing.T) {
	body, err := templates.QuestionComment(templates.QuestionOptions{ID: "QUESTION-001", Agent: "Coordinator", Status: "blocked", Scope: "test", Blocking: true, Question: "Which?", Assumption: "A"})
	if err != nil {
		t.Fatal(err)
	}
	var updated string
	backend := transitionFake(body)
	backend.updateComment = func(_ context.Context, _ string, _ int64, value string) (github.Comment, error) {
		updated = value
		return github.Comment{ID: 7, HTMLURL: "https://example.test/c/7"}, nil
	}
	app, out := transitionApp(backend)
	if code := app.runQuestionResolve(context.Background(), []string{"--repo", "o/r", "--issue", "5", "--id", "QUESTION-001", "--resolution", "Use A"}); code != 0 {
		t.Fatalf("code=%d out=%s", code, out.String())
	}
	if model.ParseTypedComment(updated).Status != "confirmed" || !strings.Contains(updated, "Use A") || !strings.Contains(out.String(), "resolved QUESTION QUESTION-001 on issue #5") {
		t.Fatalf("updated=%s out=%s", updated, out.String())
	}
}

func transitionCommandBody(t *testing.T, status string) string {
	t.Helper()
	body, err := model.EnsureTypedBody("PROCESS", "PROCESS-001", "## Process: work\n\n### Parent TASK\n\n- TASK-001\n\n### Handoff\n\nN/A", model.BodyOptions{Agent: "Worker", Status: status, Scope: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func transitionFake(body string) fakeGitHubBackend {
	return fakeGitHubBackend{info: github.BackendInfo{Name: "fake", Kind: "test"}, listIssueComments: func(context.Context, string, int) ([]github.Comment, error) {
		return []github.Comment{{ID: 7, HTMLURL: "https://example.test/c/7", Body: body}}, nil
	}}
}

func transitionApp(backend github.Backend) (*app, *bytes.Buffer) {
	out := &bytes.Buffer{}
	a := newApp(strings.NewReader(""), out, &bytes.Buffer{})
	a.selectGitHubBackend = func(context.Context, string) (auth.GitHubBackendSelection, error) {
		return auth.GitHubBackendSelection{Host: "github.com"}, nil
	}
	a.newGitHubBackend = func(context.Context, auth.GitHubBackendSelection) (github.Backend, error) {
		if backend == nil {
			return nil, errors.New("missing backend")
		}
		return backend, nil
	}
	return a, out
}
