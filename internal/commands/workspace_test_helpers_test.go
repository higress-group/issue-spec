package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/templates"
)

type workspaceCASBackend struct {
	fakeGitHubBackend
	body      string
	version   int64
	writes    int
	updateErr error
}

func newWorkspaceCASBackend(body string) *workspaceCASBackend {
	backend := &workspaceCASBackend{body: body, version: 1}
	backend.info = github.BackendInfo{Name: "workspace-cas", Kind: "test"}
	backend.listIssueComments = func(context.Context, string, int) ([]github.Comment, error) {
		return []github.Comment{{ID: 77, HTMLURL: "https://example.test/process/77", Body: backend.body}}, nil
	}
	backend.getIssue = func(_ context.Context, _ string, issue int) (github.Issue, error) {
		return github.Issue{Number: issue, HTMLURL: fmt.Sprintf("https://github.com/o/r/issues/%d", issue),
			Body: "<!-- issue-spec:issue=implement change=portable-assignments version=1 -->"}, nil
	}
	return backend
}

func (b *workspaceCASBackend) GetCommentRepresentation(context.Context, string, int64) (github.CommentRepresentation, error) {
	return github.CommentRepresentation{Comment: github.Comment{ID: 77, HTMLURL: "https://example.test/process/77", Body: b.body},
		RepresentationVersion: b.version, Guarantee: github.CommentMutationStrictConditional}, nil
}

func (b *workspaceCASBackend) UpdateCommentConditional(_ context.Context, _ string, _ int64, expected int64, body string) (github.CommentRepresentation, error) {
	if b.updateErr != nil {
		return github.CommentRepresentation{}, b.updateErr
	}
	if expected != b.version {
		return github.CommentRepresentation{}, &github.CommentMutationConflictError{Expected: expected, Current: b.version}
	}
	b.writes++
	b.version++
	b.body = body
	return github.CommentRepresentation{Comment: github.Comment{ID: 77, HTMLURL: "https://example.test/process/77", Body: body},
		RepresentationVersion: b.version}, nil
}

func workspaceProcessBody(t *testing.T, class model.ProcessExecutionClass) string {
	t.Helper()
	body, err := templates.ProcessComment(templates.ProcessCommentOptions{Common: templates.CommonOptions{ID: "PROCESS-004", Status: "in-progress"},
		Input: templates.ProcessInput{Title: "workspace", ParentTask: "TASK-002", ExecutionClass: class,
			WriteOwnership: []string{"internal/commands/**"}, Handoff: "N/A"}})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func decodeWorkspaceResult(t *testing.T, out *bytes.Buffer) workspaceCommandResult {
	t.Helper()
	var result workspaceCommandResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("decode workspace result: %v\n%s", err, out.String())
	}
	return result
}

func workspaceGitRepository(t *testing.T) (string, string) {
	t.Helper()
	skipWithoutRealGit(t)
	repo := filepath.Join(t.TempDir(), "integration")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	workspaceGit(t, repo, "init", "-b", "main")
	workspaceGit(t, repo, "config", "user.name", "Workspace Test")
	workspaceGit(t, repo, "config", "user.email", "workspace@example.com")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspaceGit(t, repo, "add", "README.md")
	workspaceGit(t, repo, "commit", "-s", "-m", "base")
	return repo, workspaceGitOutput(t, repo, "rev-parse", "HEAD")
}

func workspaceGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
}

func workspaceGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(output))
}
