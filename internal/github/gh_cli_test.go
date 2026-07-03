package github

import (
	"context"
	"encoding/json"
	"errors"
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

func TestGHAPIIncludeParsesHeadersAndStatus(t *testing.T) {
	runner := &recordingCLIRunner{
		result: ExternalCLIResult{Stdout: []byte("HTTP/1.1 200 OK\r\nX-Poll-Interval: 60\r\nX-RateLimit-Remaining: 9\r\n\r\n{\"ok\":true}")},
	}
	cli, err := NewGHCLI(GHCLIOptions{Runner: runner})
	if err != nil {
		t.Fatal(err)
	}

	result, err := cli.RunAPI(context.Background(), "github.com", ExternalCLIAPIRequest{
		Method:   http.MethodGet,
		Endpoint: "/notifications",
		Include:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != 200 {
		t.Fatalf("status = %d, want 200", result.Status)
	}
	if got := result.Headers.Get("X-Poll-Interval"); got != "60" {
		t.Fatalf("poll interval = %q", got)
	}
	if got := result.Headers.Get("X-RateLimit-Remaining"); got != "9" {
		t.Fatalf("rate limit remaining = %q", got)
	}
	if string(result.Stdout) != `{"ok":true}` {
		t.Fatalf("body = %q", string(result.Stdout))
	}
}

func TestGHAPI304MapsToNoChange(t *testing.T) {
	runner := &recordingCLIRunner{
		result: ExternalCLIResult{ExitCode: 1, Stdout: []byte("HTTP/1.1 304 Not Modified\r\n\r\n")},
	}
	cli, err := NewGHCLI(GHCLIOptions{Runner: runner})
	if err != nil {
		t.Fatal(err)
	}

	result, err := cli.RunAPI(context.Background(), "github.com", ExternalCLIAPIRequest{
		Method:   http.MethodGet,
		Endpoint: "/notifications",
		Include:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != 304 || result.ExitCode != 1 {
		t.Fatalf("result = %+v", result)
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

func TestExternalCLIRedactorWithValues(t *testing.T) {
	redactor := NewExternalCLIRedactor("one").WithValues("two")
	got := redactor.Redact("one two three")
	if got != "[REDACTED] [REDACTED] three" {
		t.Fatalf("redacted = %q", got)
	}
}
