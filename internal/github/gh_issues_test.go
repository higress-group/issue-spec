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

func TestGHBackendIssueOperationsCommandConstructionAndDecoding(t *testing.T) {
	tests := []struct {
		name      string
		stdout    string
		call      func(*GHBackend) (any, error)
		wantArgs  []string
		wantBody  map[string]any
		assertion func(t *testing.T, got any)
	}{
		{
			name:   "get user",
			stdout: `{"login":"octocat"}`,
			call: func(b *GHBackend) (any, error) {
				user, scopes, err := b.GetUser(context.Background())
				if len(scopes) != 0 {
					t.Fatalf("scopes = %#v, want none for gh JSON response", scopes)
				}
				return user, err
			},
			wantArgs: []string{"api", "--method", http.MethodGet, "--header", githubAPIVersion, "/user"},
			assertion: func(t *testing.T, got any) {
				if got.(User).Login != "octocat" {
					t.Fatalf("user = %+v", got)
				}
			},
		},
		{
			name:   "create issue",
			stdout: `{"number":12,"html_url":"https://github.com/o/r/issues/12","title":"created","body":"body","state":"open"}`,
			call: func(b *GHBackend) (any, error) {
				return b.CreateIssue(context.Background(), "o/r", "created", "body", []string{"issue-spec/proposal"})
			},
			wantArgs: []string{"api", "--method", http.MethodPost, "--header", githubAPIVersion, "--input", "-", "/repos/o/r/issues"},
			wantBody: map[string]any{"title": "created", "body": "body", "labels": []any{"issue-spec/proposal"}},
			assertion: func(t *testing.T, got any) {
				issue := got.(Issue)
				if issue.Number != 12 || issue.Title != "created" {
					t.Fatalf("issue = %+v", issue)
				}
			},
		},
		{
			name:   "get issue",
			stdout: `{"number":9,"html_url":"https://github.com/o/r/issues/9","title":"proposal","body":"body","state":"open"}`,
			call: func(b *GHBackend) (any, error) {
				return b.GetIssue(context.Background(), "o/r", 9)
			},
			wantArgs: []string{"api", "--method", http.MethodGet, "--header", githubAPIVersion, "/repos/o/r/issues/9"},
			assertion: func(t *testing.T, got any) {
				if got.(Issue).Number != 9 {
					t.Fatalf("issue = %+v", got)
				}
			},
		},
		{
			name:   "update issue",
			stdout: `{"number":9,"html_url":"https://github.com/o/r/issues/9","title":"new","body":"body","state":"closed"}`,
			call: func(b *GHBackend) (any, error) {
				title := "new"
				state := "closed"
				return b.UpdateIssue(context.Background(), "o/r", 9, UpdateIssueOptions{Title: &title, State: &state})
			},
			wantArgs: []string{"api", "--method", http.MethodPatch, "--header", githubAPIVersion, "--input", "-", "/repos/o/r/issues/9"},
			wantBody: map[string]any{"state": "closed", "title": "new"},
			assertion: func(t *testing.T, got any) {
				if got.(Issue).Title != "new" || got.(Issue).State != "closed" {
					t.Fatalf("issue = %+v", got)
				}
			},
		},
		{
			name:   "create comment",
			stdout: `{"id":101,"html_url":"https://github.com/o/r/issues/9#issuecomment-101","url":"https://api.github.com/repos/o/r/issues/comments/101","body":"body"}`,
			call: func(b *GHBackend) (any, error) {
				return b.CreateComment(context.Background(), "o/r", 9, "body")
			},
			wantArgs: []string{"api", "--method", http.MethodPost, "--header", githubAPIVersion, "--input", "-", "/repos/o/r/issues/9/comments"},
			wantBody: map[string]any{"body": "body"},
			assertion: func(t *testing.T, got any) {
				if got.(Comment).ID != 101 {
					t.Fatalf("comment = %+v", got)
				}
			},
		},
		{
			name:   "update comment",
			stdout: `{"id":101,"html_url":"https://github.com/o/r/issues/9#issuecomment-101","url":"https://api.github.com/repos/o/r/issues/comments/101","body":"updated"}`,
			call: func(b *GHBackend) (any, error) {
				return b.UpdateComment(context.Background(), "o/r", 101, "updated")
			},
			wantArgs: []string{"api", "--method", http.MethodPatch, "--header", githubAPIVersion, "--input", "-", "/repos/o/r/issues/comments/101"},
			wantBody: map[string]any{"body": "updated"},
			assertion: func(t *testing.T, got any) {
				if got.(Comment).Body != "updated" {
					t.Fatalf("comment = %+v", got)
				}
			},
		},
		{
			name:   "create label",
			stdout: `{"name":"issue-spec/proposal"}`,
			call: func(b *GHBackend) (any, error) {
				return b.CreateLabel(context.Background(), "o/r", "issue-spec/proposal", "0969da", "Proposal")
			},
			wantArgs: []string{"api", "--method", http.MethodPost, "--header", githubAPIVersion, "--input", "-", "/repos/o/r/labels"},
			wantBody: map[string]any{"name": "issue-spec/proposal", "color": "0969da", "description": "Proposal"},
			assertion: func(t *testing.T, got any) {
				label := got.(LabelResult)
				if label.Name != "issue-spec/proposal" || !label.Created || label.Skipped {
					t.Fatalf("label = %+v", label)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &recordingCLIRunner{result: ExternalCLIResult{Stdout: []byte(tt.stdout)}}
			backend, err := NewGHBackend(GHBackendOptions{Host: "github.com", CLIOptions: GHCLIOptions{Runner: runner}})
			if err != nil {
				t.Fatal(err)
			}
			got, err := tt.call(backend)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(runner.command.Args, tt.wantArgs) {
				t.Fatalf("args = %#v, want %#v", runner.command.Args, tt.wantArgs)
			}
			if tt.wantBody != nil {
				assertJSONBody(t, runner.command.Stdin, tt.wantBody)
			} else if len(runner.command.Stdin) != 0 {
				t.Fatalf("stdin = %q, want empty", string(runner.command.Stdin))
			}
			tt.assertion(t, got)
		})
	}
}

func TestGHBackendListIssueCommentsUsesPaginationHostAndNormalizesPages(t *testing.T) {
	runner := &recordingCLIRunner{
		result: ExternalCLIResult{Stdout: []byte(`[{"id":1,"html_url":"https://github.com/o/r/issues/7#issuecomment-1","body":"one"}]
[{"id":2,"html_url":"https://github.com/o/r/issues/7#issuecomment-2","body":"two"}]`)},
	}
	backend, err := NewGHBackend(GHBackendOptions{Host: "ghe.example.com", CLIOptions: GHCLIOptions{Runner: runner}})
	if err != nil {
		t.Fatal(err)
	}

	comments, err := backend.ListIssueComments(context.Background(), "o/r", 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 2 || comments[0].Body != "one" || comments[1].ID != 2 {
		t.Fatalf("comments = %+v", comments)
	}
	wantArgs := []string{
		"api",
		"--method", http.MethodGet,
		"--header", githubAPIVersion,
		"--hostname", "ghe.example.com",
		"--paginate",
		"/repos/o/r/issues/7/comments?per_page=100",
	}
	if !reflect.DeepEqual(runner.command.Args, wantArgs) {
		t.Fatalf("args = %#v, want %#v", runner.command.Args, wantArgs)
	}
}

func TestGHBackendCreateLabelAlreadyExistsIsSkipped(t *testing.T) {
	runner := &recordingCLIRunner{
		result: ExternalCLIResult{Stderr: []byte("Validation Failed: code already_exists"), ExitCode: 1},
	}
	backend, err := NewGHBackend(GHBackendOptions{Host: "github.com", CLIOptions: GHCLIOptions{Runner: runner}})
	if err != nil {
		t.Fatal(err)
	}

	result, err := backend.CreateLabel(context.Background(), "o/r", "issue-spec/proposal", "0969da", "Proposal")
	if err != nil {
		t.Fatal(err)
	}
	if result.Name != "issue-spec/proposal" || !result.Skipped || result.Created {
		t.Fatalf("label result = %+v", result)
	}
}

func TestGHBackendIssueOperationErrorsAreRedacted(t *testing.T) {
	secret := "ghp_secret"
	runner := &recordingCLIRunner{
		result: ExternalCLIResult{Stderr: []byte("token " + secret + " rejected"), ExitCode: 1},
		err:    errors.New("exit status 1"),
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

	_, err = backend.GetIssue(context.Background(), "o/r", 9)
	if err == nil {
		t.Fatal("GetIssue succeeded, want error")
	}
	message := err.Error()
	if strings.Contains(message, secret) {
		t.Fatalf("error leaked secret: %s", message)
	}
	for _, want := range []string{"gh command failed", "operation GetIssue", "host ghe.example.com", "method GET", "endpoint /repos/o/r/issues/9", "[REDACTED]", "rejected"} {
		if !strings.Contains(message, want) {
			t.Fatalf("error %q missing %q", message, want)
		}
	}
}

func assertJSONBody(t *testing.T, data []byte, want map[string]any) {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("body = %#v, want %#v", got, want)
	}
}
