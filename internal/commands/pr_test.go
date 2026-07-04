package commands

import (
	"context"
	"testing"

	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
)

func TestChangedRightLines(t *testing.T) {
	patch := `@@ -1,3 +1,5 @@
 package main
+import "fmt"
 
 func main() {
-	println("x")
+	fmt.Println("x")
 }`
	got := changedRightLines(patch)
	want := []int{2, 5}
	if len(got) != len(want) {
		t.Fatalf("lines = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("lines = %v, want %v", got, want)
		}
	}
}

func TestCreateRationaleIsIdempotent(t *testing.T) {
	ctx := context.Background()
	client := &fakePRClient{
		files: []github.PullRequestFile{{
			Filename: "internal/foo.go",
			Patch: `@@ -1,2 +1,3 @@
 package foo
+var X = 1
`,
		}},
		pr: github.PullRequest{Number: 7},
	}
	client.pr.Head.SHA = "abc123"
	result, err := createRationale(ctx, client, "o/r", 7, "internal/foo.go", 2, "PROCESS-001", "SPEC-001", "https://github.com/o/r/issues/1#issuecomment-1", "Worker Agent A", writerSession{}, "Explain why.")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Created || result.CommentID == 0 {
		t.Fatalf("unexpected result: %+v", result)
	}
	result, err = createRationale(ctx, client, "o/r", 7, "internal/foo.go", 2, "PROCESS-001", "SPEC-001", "https://github.com/o/r/issues/1#issuecomment-1", "Worker Agent A", writerSession{}, "Explain why.")
	if err != nil {
		t.Fatal(err)
	}
	if result.Created {
		t.Fatalf("expected idempotent existing result: %+v", result)
	}
}

func TestCreateRationaleWritesSessionMetadata(t *testing.T) {
	ctx := context.Background()
	client := &fakePRClient{
		files: []github.PullRequestFile{{
			Filename: "internal/foo.go",
			Patch: `@@ -1,2 +1,3 @@
 package foo
+var X = 1
`,
		}},
		pr: github.PullRequest{Number: 7},
	}
	client.pr.Head.SHA = "abc123"
	_, err := createRationale(ctx, client, "o/r", 7, "internal/foo.go", 2, "PROCESS-001", "SPEC-001", "https://github.com/o/r/issues/1#issuecomment-1", "Worker Agent A", writerSession{ID: "codex-session-123", Source: codexThreadIDEnv}, "Explain why.")
	if err != nil {
		t.Fatal(err)
	}
	marker, ok, err := model.FindRationaleMarker(client.comments[0].Body)
	if err != nil || !ok {
		t.Fatalf("missing marker ok=%v err=%v", ok, err)
	}
	if marker.Agent != "Worker Agent A" || marker.AgentSessionID != "codex-session-123" || marker.AgentSessionSource != codexThreadIDEnv {
		t.Fatalf("unexpected marker metadata: %+v\n%s", marker, client.comments[0].Body)
	}
}

func TestCreateRationaleWithGHBackendUsesPatchLines(t *testing.T) {
	ctx := context.Background()
	runner := &commandSequenceRunner{results: []github.ExternalCLIResult{
		{Stdout: []byte(`[{"filename":"internal/foo.go","patch":"@@ -1,2 +1,3 @@\n package foo\n+var X = 1\n"}]`)},
		{Stdout: []byte(`[]`)},
		{Stdout: []byte(`{"number":7,"html_url":"https://github.com/o/r/pull/7","head":{"sha":"abc123","ref":"feature"},"base":{"ref":"main"}}`)},
		{Stdout: []byte(`{"id":99,"html_url":"https://github.com/o/r/pull/7#discussion_r99","body":"created","path":"internal/foo.go","line":2,"commit_id":"abc123"}`)},
	}}
	client := newCommandTestGHBackend(t, runner)

	result, err := createRationale(ctx, client, "o/r", 7, "internal/foo.go", 2, "PROCESS-001", "SPEC-001", "https://github.com/o/r/issues/1#issuecomment-1", "Worker Agent A", writerSession{}, "Explain why.")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Created || result.CommentID != 99 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if got, want := len(runner.commands), 4; got != want {
		t.Fatalf("gh commands = %d, want %d", got, want)
	}
}

type fakePRClient struct {
	pr       github.PullRequest
	files    []github.PullRequestFile
	comments []github.PullRequestReviewComment
}

func (f *fakePRClient) GetPullRequest(context.Context, string, int) (github.PullRequest, error) {
	return f.pr, nil
}

func (f *fakePRClient) ListPullRequestFiles(context.Context, string, int) ([]github.PullRequestFile, error) {
	return f.files, nil
}

func (f *fakePRClient) ListPullRequestReviewComments(context.Context, string, int) ([]github.PullRequestReviewComment, error) {
	return f.comments, nil
}

func (f *fakePRClient) CreatePullRequestReviewComment(_ context.Context, _ string, _ int, body, commitID, path string, line int, side string) (github.PullRequestReviewComment, error) {
	if commitID == "" || path == "" || line == 0 || side != "RIGHT" {
		panic("invalid create review comment args")
	}
	comment := github.PullRequestReviewComment{
		ID:       int64(len(f.comments) + 1),
		HTMLURL:  "https://github.com/o/r/pull/7#discussion_r1",
		Body:     body,
		Path:     path,
		Line:     line,
		CommitID: commitID,
	}
	marker, ok, err := model.FindRationaleMarker(body)
	if err != nil || !ok || marker.Process != "PROCESS-001" || marker.Spec != "SPEC-001" {
		panic("missing rationale marker")
	}
	f.comments = append(f.comments, comment)
	return comment, nil
}

func newCommandTestGHBackend(t *testing.T, runner github.ExternalCLIRunner) *github.GHBackend {
	t.Helper()
	backend, err := github.NewGHBackend(github.GHBackendOptions{
		Host:       "github.com",
		CLIOptions: github.GHCLIOptions{Runner: runner},
	})
	if err != nil {
		t.Fatal(err)
	}
	return backend
}

type commandSequenceRunner struct {
	commands []github.ExternalCLICommand
	results  []github.ExternalCLIResult
	errs     []error
}

func (r *commandSequenceRunner) RunCLI(_ context.Context, command github.ExternalCLICommand) (github.ExternalCLIResult, error) {
	r.commands = append(r.commands, command)
	index := len(r.commands) - 1
	var result github.ExternalCLIResult
	if index < len(r.results) {
		result = r.results[index]
	}
	var err error
	if index < len(r.errs) {
		err = r.errs[index]
	}
	return result, err
}
