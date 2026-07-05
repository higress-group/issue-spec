package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestGHBackendPullRequestOperations(t *testing.T) {
	runner := &sequenceCLIRunner{results: []ExternalCLIResult{
		{Stdout: []byte(`{"number":7,"html_url":"https://github.com/owner/repo/pull/7","state":"closed","merged":true,"body":"details","head":{"sha":"abc123","ref":"feature"},"base":{"ref":"main"}}`)},
		{Stdout: []byte(`{"number":8,"html_url":"https://github.com/owner/repo/pull/8","state":"open","head":{"sha":"def456","ref":"feature-2"},"base":{"ref":"main"}}`)},
		{Stdout: []byte(`{"number":7,"html_url":"https://github.com/owner/repo/pull/7","state":"open","body":"updated body","head":{"sha":"abc123","ref":"feature"},"base":{"ref":"main"}}`)},
	}}
	backend := newTestGHBackend(t, "ghe.example.com", runner)

	pr, err := backend.GetPullRequest(context.Background(), "owner/repo", 7)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 7 || pr.Head.SHA != "abc123" || pr.Base.Ref != "main" || !pr.Merged || pr.Body != "details" {
		t.Fatalf("pull request = %+v", pr)
	}
	wantArgs := []string{"api", "--method", http.MethodGet, "--header", githubAPIVersion, "--hostname", "ghe.example.com", "/repos/owner/repo/pulls/7"}
	if !reflect.DeepEqual(runner.commands[0].Args, wantArgs) {
		t.Fatalf("get PR args = %#v, want %#v", runner.commands[0].Args, wantArgs)
	}

	created, err := backend.CreatePullRequest(context.Background(), "owner/repo", CreatePullRequestOptions{
		Title: "Add feature",
		Head:  "feature-2",
		Base:  "main",
		Body:  "details",
		Draft: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Number != 8 || created.Head.Ref != "feature-2" {
		t.Fatalf("created PR = %+v", created)
	}
	wantArgs = []string{"api", "--method", http.MethodPost, "--header", githubAPIVersion, "--hostname", "ghe.example.com", "--input", "-", "/repos/owner/repo/pulls"}
	if !reflect.DeepEqual(runner.commands[1].Args, wantArgs) {
		t.Fatalf("create PR args = %#v, want %#v", runner.commands[1].Args, wantArgs)
	}
	var body map[string]any
	if err := json.Unmarshal(runner.commands[1].Stdin, &body); err != nil {
		t.Fatal(err)
	}
	if body["title"] != "Add feature" || body["head"] != "feature-2" || body["base"] != "main" || body["body"] != "details" || body["draft"] != true {
		t.Fatalf("create PR body = %#v", body)
	}

	updatedBody := "updated body"
	updated, err := backend.UpdatePullRequest(context.Background(), "owner/repo", 7, UpdatePullRequestOptions{Body: &updatedBody})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Number != 7 || updated.Body != updatedBody {
		t.Fatalf("updated PR = %+v", updated)
	}
	wantArgs = []string{"api", "--method", http.MethodPatch, "--header", githubAPIVersion, "--hostname", "ghe.example.com", "--input", "-", "/repos/owner/repo/pulls/7"}
	if !reflect.DeepEqual(runner.commands[2].Args, wantArgs) {
		t.Fatalf("update PR args = %#v, want %#v", runner.commands[2].Args, wantArgs)
	}
	body = map[string]any{}
	if err := json.Unmarshal(runner.commands[2].Stdin, &body); err != nil {
		t.Fatal(err)
	}
	if body["body"] != updatedBody {
		t.Fatalf("update PR body = %#v", body)
	}
}

func TestGHBackendListPullRequestFilesNormalizesPaginatedArraysAndPreservesPatch(t *testing.T) {
	runner := &sequenceCLIRunner{results: []ExternalCLIResult{{
		Stdout: []byte(`[{"filename":"a.go","patch":"@@ -1,2 +1,3 @@\n package a\n+var A = 1\n"}]
[{"filename":"b.go","patch":"@@ -5,2 +5,3 @@\n package b\n+var B = 1\n"}]`),
	}}}
	backend := newTestGHBackend(t, "github.com", runner)

	files, err := backend.ListPullRequestFiles(context.Background(), "owner/repo", 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].Filename != "a.go" || !strings.Contains(files[0].Patch, "+var A = 1") || files[1].Filename != "b.go" {
		t.Fatalf("files = %+v", files)
	}
	wantArgs := []string{"api", "--method", http.MethodGet, "--header", githubAPIVersion, "--paginate", "/repos/owner/repo/pulls/7/files?per_page=100"}
	if !reflect.DeepEqual(runner.commands[0].Args, wantArgs) {
		t.Fatalf("files args = %#v, want %#v", runner.commands[0].Args, wantArgs)
	}
}

func TestGHBackendPullRequestReviewCommentOperations(t *testing.T) {
	runner := &sequenceCLIRunner{results: []ExternalCLIResult{
		{Stdout: []byte(`[{"id":1,"html_url":"https://github.com/owner/repo/pull/7#discussion_r1","body":"root","path":"a.go","line":10,"commit_id":"abc123"}]
[{"id":2,"html_url":"https://github.com/owner/repo/pull/7#discussion_r2","body":"reply","path":"a.go","line":10,"commit_id":"abc123","in_reply_to_id":1}]`)},
		{Stdout: []byte(`{"id":3,"html_url":"https://github.com/owner/repo/pull/7#discussion_r3","body":"created","path":"a.go","line":10,"commit_id":"abc123"}`)},
		{Stdout: []byte(`{"id":4,"html_url":"https://github.com/owner/repo/pull/7#discussion_r4","body":"fixed","path":"a.go","line":10,"commit_id":"abc123","in_reply_to_id":3}`)},
	}}
	backend := newTestGHBackend(t, "github.com", runner)

	comments, err := backend.ListPullRequestReviewComments(context.Background(), "owner/repo", 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 2 || comments[1].InReplyToID != 1 {
		t.Fatalf("comments = %+v", comments)
	}
	wantArgs := []string{"api", "--method", http.MethodGet, "--header", githubAPIVersion, "--paginate", "/repos/owner/repo/pulls/7/comments?per_page=100"}
	if !reflect.DeepEqual(runner.commands[0].Args, wantArgs) {
		t.Fatalf("review comment list args = %#v, want %#v", runner.commands[0].Args, wantArgs)
	}

	created, err := backend.CreatePullRequestReviewComment(context.Background(), "owner/repo", 7, "created", "abc123", "a.go", 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != 3 {
		t.Fatalf("created comment = %+v", created)
	}
	wantArgs = []string{"api", "--method", http.MethodPost, "--header", githubAPIVersion, "--input", "-", "/repos/owner/repo/pulls/7/comments"}
	if !reflect.DeepEqual(runner.commands[1].Args, wantArgs) {
		t.Fatalf("review comment create args = %#v, want %#v", runner.commands[1].Args, wantArgs)
	}
	var createBody map[string]any
	if err := json.Unmarshal(runner.commands[1].Stdin, &createBody); err != nil {
		t.Fatal(err)
	}
	if createBody["body"] != "created" || createBody["commit_id"] != "abc123" || createBody["path"] != "a.go" || createBody["line"] != float64(10) || createBody["side"] != "RIGHT" {
		t.Fatalf("review comment create body = %#v", createBody)
	}

	reply, err := backend.ReplyPullRequestReviewComment(context.Background(), "owner/repo", 7, 3, "fixed")
	if err != nil {
		t.Fatal(err)
	}
	if reply.ID != 4 || reply.InReplyToID != 3 {
		t.Fatalf("reply comment = %+v", reply)
	}
	wantArgs = []string{"api", "--method", http.MethodPost, "--header", githubAPIVersion, "--input", "-", "/repos/owner/repo/pulls/7/comments/3/replies"}
	if !reflect.DeepEqual(runner.commands[2].Args, wantArgs) {
		t.Fatalf("review comment reply args = %#v, want %#v", runner.commands[2].Args, wantArgs)
	}
	var replyBody map[string]string
	if err := json.Unmarshal(runner.commands[2].Stdin, &replyBody); err != nil {
		t.Fatal(err)
	}
	if replyBody["body"] != "fixed" {
		t.Fatalf("review comment reply body = %#v", replyBody)
	}
}

func TestGHBackendStatusAndCheckRuns(t *testing.T) {
	runner := &sequenceCLIRunner{results: []ExternalCLIResult{
		{Stdout: []byte(`{"state":"failure","statuses":[{"context":"ci/test","state":"failure","description":"failed","target_url":"https://ci.example/test"}]}`)},
		{Stdout: []byte(`{"total_count":2,"check_runs":[{"id":11,"name":"build","status":"completed","conclusion":"success","details_url":"https://ci.example/build"}]}
{"total_count":2,"check_runs":[{"id":12,"name":"lint","status":"queued","html_url":"https://github.com/owner/repo/runs/12"}]}`)},
	}}
	backend := newTestGHBackend(t, "github.com", runner)

	status, err := backend.GetCombinedStatus(context.Background(), "owner/repo", "feature/branch")
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "failure" || len(status.Statuses) != 1 || status.Statuses[0].Context != "ci/test" {
		t.Fatalf("combined status = %+v", status)
	}
	wantArgs := []string{"api", "--method", http.MethodGet, "--header", githubAPIVersion, "/repos/owner/repo/commits/feature%2Fbranch/status"}
	if !reflect.DeepEqual(runner.commands[0].Args, wantArgs) {
		t.Fatalf("status args = %#v, want %#v", runner.commands[0].Args, wantArgs)
	}

	runs, err := backend.ListCheckRuns(context.Background(), "owner/repo", "feature/branch")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || runs[0].Name != "build" || runs[0].DetailsURL == "" || runs[1].Name != "lint" || runs[1].HTMLURL == "" {
		t.Fatalf("check runs = %+v", runs)
	}
	wantArgs = []string{"api", "--method", http.MethodGet, "--header", githubAPIVersion, "--paginate", "/repos/owner/repo/commits/feature%2Fbranch/check-runs?per_page=100"}
	if !reflect.DeepEqual(runner.commands[1].Args, wantArgs) {
		t.Fatalf("check runs args = %#v, want %#v", runner.commands[1].Args, wantArgs)
	}
}

func TestGHBackendCheckRunErrorRedactsStderr(t *testing.T) {
	secret := "ghp_secret"
	runner := &sequenceCLIRunner{
		results: []ExternalCLIResult{{Stderr: []byte("token " + secret + " rejected"), ExitCode: 1}},
		errs:    []error{errors.New("exit status 1")},
	}
	backend, err := NewGHBackend(GHBackendOptions{
		Host: "ghe.example.com",
		CLIOptions: GHCLIOptions{
			Runner:   runner,
			Redactor: NewExternalCLIRedactor(secret),
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = backend.ListCheckRuns(context.Background(), "owner/repo", "abc123")
	if err == nil {
		t.Fatal("ListCheckRuns succeeded, want error")
	}
	message := err.Error()
	if strings.Contains(message, secret) {
		t.Fatalf("error leaked secret: %s", message)
	}
	for _, want := range []string{"gh command failed", "operation ListCheckRuns", "host ghe.example.com", "method GET", "endpoint /repos/owner/repo/commits/abc123/check-runs", "--hostname ghe.example.com", "[REDACTED]"} {
		if !strings.Contains(message, want) {
			t.Fatalf("error %q missing %q", message, want)
		}
	}
}

func newTestGHBackend(t *testing.T, host string, runner ExternalCLIRunner) *GHBackend {
	t.Helper()
	backend, err := NewGHBackend(GHBackendOptions{
		Host:       host,
		CLIOptions: GHCLIOptions{Runner: runner},
	})
	if err != nil {
		t.Fatal(err)
	}
	return backend
}

type sequenceCLIRunner struct {
	commands []ExternalCLICommand
	results  []ExternalCLIResult
	errs     []error
}

func (r *sequenceCLIRunner) RunCLI(_ context.Context, command ExternalCLICommand) (ExternalCLIResult, error) {
	r.commands = append(r.commands, command)
	index := len(r.commands) - 1
	var result ExternalCLIResult
	if index < len(r.results) {
		result = r.results[index]
	}
	var err error
	if index < len(r.errs) {
		err = r.errs[index]
	}
	return result, err
}
