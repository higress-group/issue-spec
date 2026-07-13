package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestGHAuthProbeUsesHostAwareCommand(t *testing.T) {
	runner := &recordingCLIRunner{}
	cli, err := NewGHCLI(GHCLIOptions{Runner: runner})
	if err != nil {
		t.Fatal(err)
	}

	if err := cli.Authenticated(context.Background(), "github.com"); err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{"auth", "status", "--active"}
	if !reflect.DeepEqual(runner.command.Args, wantArgs) {
		t.Fatalf("github.com auth args = %#v, want %#v", runner.command.Args, wantArgs)
	}

	if err := cli.Authenticated(context.Background(), "ghe.example.com"); err != nil {
		t.Fatal(err)
	}
	wantArgs = []string{"auth", "status", "--active", "--hostname", "ghe.example.com"}
	if !reflect.DeepEqual(runner.command.Args, wantArgs) {
		t.Fatalf("enterprise auth args = %#v, want %#v", runner.command.Args, wantArgs)
	}
	if runner.command.Operation != "auth status" || runner.command.Host != "ghe.example.com" {
		t.Fatalf("command metadata = operation %q host %q", runner.command.Operation, runner.command.Host)
	}
}

func TestGHAuthProbeReportsMissingBinary(t *testing.T) {
	runner := &recordingCLIRunner{
		result: ExternalCLIResult{ExitCode: -1},
		err:    errors.New(`exec: "gh": executable file not found in $PATH`),
	}
	cli, err := NewGHCLI(GHCLIOptions{Runner: runner})
	if err != nil {
		t.Fatal(err)
	}

	err = cli.Authenticated(context.Background(), "github.com")
	if err == nil {
		t.Fatal("missing binary probe succeeded, want error")
	}
	message := err.Error()
	for _, want := range []string{"gh command failed", "operation auth status", "host github.com", "executable file not found"} {
		if !strings.Contains(message, want) {
			t.Fatalf("error %q missing %q", message, want)
		}
	}
}

func TestGHAuthProbeRedactsUnauthenticatedStderr(t *testing.T) {
	secret := "ghp_secret"
	runner := &recordingCLIRunner{
		result: ExternalCLIResult{Stderr: []byte("token " + secret + " is invalid"), ExitCode: 1},
	}
	cli, err := NewGHCLI(GHCLIOptions{Runner: runner, Redactor: NewExternalCLIRedactor(secret)})
	if err != nil {
		t.Fatal(err)
	}

	err = cli.Authenticated(context.Background(), "ghe.example.com")
	if err == nil {
		t.Fatal("unauthenticated probe succeeded, want error")
	}
	message := err.Error()
	if strings.Contains(message, secret) {
		t.Fatalf("error leaked secret: %s", message)
	}
	for _, want := range []string{"exit 1", "operation auth status", "--hostname ghe.example.com", "[REDACTED]", "invalid"} {
		if !strings.Contains(message, want) {
			t.Fatalf("error %q missing %q", message, want)
		}
	}
}

func TestGHAuthTokenTrimsOutputAndUsesHost(t *testing.T) {
	runner := &recordingCLIRunner{
		result: ExternalCLIResult{Stdout: []byte("  ghp_token\n")},
	}
	cli, err := NewGHCLI(GHCLIOptions{Runner: runner})
	if err != nil {
		t.Fatal(err)
	}

	token, err := cli.Token(context.Background(), "ghe.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if token != "ghp_token" {
		t.Fatalf("token = %q, want trimmed token", token)
	}
	wantArgs := []string{"auth", "token", "--hostname", "ghe.example.com"}
	if !reflect.DeepEqual(runner.command.Args, wantArgs) {
		t.Fatalf("token args = %#v, want %#v", runner.command.Args, wantArgs)
	}
}

func TestGHAuthTokenEmptyOutputIsError(t *testing.T) {
	runner := &recordingCLIRunner{
		result: ExternalCLIResult{Stdout: []byte("\n")},
	}
	cli, err := NewGHCLI(GHCLIOptions{Runner: runner})
	if err != nil {
		t.Fatal(err)
	}

	_, err = cli.Token(context.Background(), "github.com")
	if err == nil {
		t.Fatal("empty token output succeeded, want error")
	}
	if !strings.Contains(err.Error(), "empty token") {
		t.Fatalf("error = %v, want empty token context", err)
	}
}

func TestGHAuthTokenErrorRedactsStderr(t *testing.T) {
	secret := "ghp_secret"
	runner := &recordingCLIRunner{
		result: ExternalCLIResult{Stderr: []byte("token " + secret + " rejected"), ExitCode: 1},
		err:    errors.New("exit status 1"),
	}
	cli, err := NewGHCLI(GHCLIOptions{Runner: runner, Redactor: NewExternalCLIRedactor(secret)})
	if err != nil {
		t.Fatal(err)
	}

	_, err = cli.Token(context.Background(), "ghe.example.com")
	if err == nil {
		t.Fatal("token command succeeded, want error")
	}
	message := err.Error()
	if strings.Contains(message, secret) {
		t.Fatalf("error leaked secret: %s", message)
	}
	for _, want := range []string{"operation auth token", "--hostname ghe.example.com", "exit 1", "[REDACTED]", "rejected"} {
		if !strings.Contains(message, want) {
			t.Fatalf("error %q missing %q", message, want)
		}
	}
}

func TestGHAPIAdapterCommandConstruction(t *testing.T) {
	runner := &recordingCLIRunner{
		result: ExternalCLIResult{Stdout: []byte(`{"ok":true}`)},
	}
	cli, err := NewGHCLI(GHCLIOptions{Runner: runner})
	if err != nil {
		t.Fatal(err)
	}

	request := ExternalCLIAPIRequest{
		Operation: "create issue",
		Method:    http.MethodPost,
		Endpoint:  "/repos/owner/repo/issues",
		Query:     url.Values{"page": {"1"}, "per_page": {"100"}},
		Body:      map[string]string{"title": "created"},
		Paginate:  true,
	}
	if _, err := cli.RunAPI(context.Background(), "ghe.example.com", request); err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{
		"api",
		"--method", http.MethodPost,
		"--header", githubAPIVersion,
		"--hostname", "ghe.example.com",
		"--paginate",
		"--input", "-",
		"/repos/owner/repo/issues?page=1&per_page=100",
	}
	if !reflect.DeepEqual(runner.command.Args, wantArgs) {
		t.Fatalf("api args = %#v, want %#v", runner.command.Args, wantArgs)
	}
	if runner.command.Operation != "create issue" || runner.command.Method != http.MethodPost || runner.command.Endpoint != request.Endpoint {
		t.Fatalf("api metadata = %+v", runner.command)
	}
	var body map[string]string
	if err := json.Unmarshal(runner.command.Stdin, &body); err != nil {
		t.Fatal(err)
	}
	if body["title"] != "created" {
		t.Fatalf("stdin body = %#v", body)
	}
}

func TestGHAPIAdapterOmitsDefaultHost(t *testing.T) {
	runner := &recordingCLIRunner{}
	cli, err := NewGHCLI(GHCLIOptions{Runner: runner})
	if err != nil {
		t.Fatal(err)
	}

	_, err = cli.RunAPI(context.Background(), "github.com", ExternalCLIAPIRequest{
		Method:   http.MethodGet,
		Endpoint: "/user",
	})
	if err != nil {
		t.Fatal(err)
	}
	rendered := runner.command.Render(ExternalCLIRedactor{})
	if strings.Contains(rendered, "--hostname") {
		t.Fatalf("default host command included --hostname: %s", rendered)
	}
}

func TestGHBackendInfo(t *testing.T) {
	backend, err := NewGHBackend(GHBackendOptions{Host: "https://ghe.example.com/"})
	if err != nil {
		t.Fatal(err)
	}
	info := backend.BackendInfo()
	if info.Name != "gh" || info.Kind != "external-cli" || info.Host != "ghe.example.com" {
		t.Fatalf("backend info = %+v", info)
	}
}

func TestGHBackendListPullRequestCommitsPaginatesAndReturnsFullSHAs(t *testing.T) {
	firstPage := make([]PullRequestCommit, 100)
	for index := range firstPage {
		firstPage[index].SHA = fmt.Sprintf("%040x", index+1)
	}
	secondPage := []PullRequestCommit{{SHA: fmt.Sprintf("%040x", 101)}}
	firstJSON, err := json.Marshal(firstPage)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := json.Marshal(secondPage)
	if err != nil {
		t.Fatal(err)
	}
	runner := &sequenceCLIRunner{results: []ExternalCLIResult{{Stdout: append(append(firstJSON, '\n'), secondJSON...)}}}
	backend := newTestGHBackend(t, "ghe.example.com", runner)

	commits, err := backend.ListPullRequestCommits(context.Background(), "owner/repo", 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 101 || commits[0].SHA != fmt.Sprintf("%040x", 1) || commits[100].SHA != fmt.Sprintf("%040x", 101) {
		t.Fatalf("commits = %d first=%q last=%q", len(commits), commits[0].SHA, commits[len(commits)-1].SHA)
	}
	wantArgs := []string{"api", "--method", http.MethodGet, "--header", githubAPIVersion, "--hostname", "ghe.example.com", "--paginate", "/repos/owner/repo/pulls/7/commits?per_page=100"}
	if !reflect.DeepEqual(runner.commands[0].Args, wantArgs) {
		t.Fatalf("commit list args = %#v, want %#v", runner.commands[0].Args, wantArgs)
	}
	if _, ok := any(backend).(PullRequestCommitBackend); !ok {
		t.Fatal("production GHBackend does not expose PullRequestCommitBackend")
	}
}

func TestGHBackendListPullRequestCommitsDeduplicatesInProviderOrder(t *testing.T) {
	sha1 := fmt.Sprintf("%040x", 1)
	sha2 := fmt.Sprintf("%040x", 2)
	sha3 := fmt.Sprintf("%040x", 3)
	for _, test := range []struct {
		name   string
		stdout string
		want   []PullRequestCommit
	}{
		{
			name:   "within page",
			stdout: fmt.Sprintf(`[{"sha":%q},{"sha":%q},{"sha":%q}]`, sha1, sha2, sha1),
			want:   []PullRequestCommit{{SHA: sha1}, {SHA: sha2}},
		},
		{
			name:   "page boundary",
			stdout: fmt.Sprintf("[{\"sha\":%q},{\"sha\":%q}]\n[{\"sha\":%q},{\"sha\":%q}]", sha1, sha2, sha2, sha3),
			want:   []PullRequestCommit{{SHA: sha1}, {SHA: sha2}, {SHA: sha3}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &sequenceCLIRunner{results: []ExternalCLIResult{{Stdout: []byte(test.stdout)}}}
			backend := newTestGHBackend(t, "github.com", runner)

			commits, err := backend.ListPullRequestCommits(context.Background(), "owner/repo", 7)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(commits, test.want) {
				t.Fatalf("commits = %+v, want provider-order unique %+v", commits, test.want)
			}
		})
	}
}

func TestGHBackendListPullRequestCommitsRejectsInvalidProviderData(t *testing.T) {
	for _, test := range []struct {
		name   string
		stdout string
	}{
		{name: "empty SHA", stdout: `[{"sha":""}]`},
		{name: "short SHA", stdout: `[{"sha":"abc123"}]`},
		{name: "non hex SHA", stdout: `[{"sha":"zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"}]`},
		{name: "whitespace SHA", stdout: `[{"sha":" 0000000000000000000000000000000000000001"}]`},
		{name: "malformed JSON", stdout: `[{"sha":`},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &sequenceCLIRunner{results: []ExternalCLIResult{{Stdout: []byte(test.stdout)}}}
			backend := newTestGHBackend(t, "github.com", runner)
			commits, err := backend.ListPullRequestCommits(context.Background(), "owner/repo", 7)
			if err == nil {
				t.Fatalf("commits = %+v, want fail-closed error", commits)
			}
			if commits != nil {
				t.Fatalf("commits = %+v, want nil on provider data error", commits)
			}
		})
	}
}

func TestGHBackendListPullRequestCommitsPropagatesCommandError(t *testing.T) {
	runner := &sequenceCLIRunner{
		results: []ExternalCLIResult{{Stderr: []byte("request failed"), ExitCode: 1}},
		errs:    []error{errors.New("exit status 1")},
	}
	backend := newTestGHBackend(t, "github.com", runner)

	commits, err := backend.ListPullRequestCommits(context.Background(), "owner/repo", 7)
	if err == nil || !strings.Contains(err.Error(), "ListPullRequestCommits") || !strings.Contains(err.Error(), "request failed") {
		t.Fatalf("commits = %+v error = %v, want command context", commits, err)
	}
	if commits != nil {
		t.Fatalf("commits = %+v, want nil on command error", commits)
	}
}

func TestExternalCLIRedactorWithValues(t *testing.T) {
	redactor := NewExternalCLIRedactor("one").WithValues("two")
	got := redactor.Redact("one two three")
	if got != "[REDACTED] [REDACTED] three" {
		t.Fatalf("redacted = %q", got)
	}
}
