package github

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestCompatibilityGHCommandsAreNonInteractiveAndEnterpriseHostMapped(t *testing.T) {
	runner := &recordingCLIRunner{
		result: ExternalCLIResult{Stdout: []byte(`{"login":"octocat"}`)},
	}
	cli, err := NewGHCLI(GHCLIOptions{Runner: runner})
	if err != nil {
		t.Fatal(err)
	}

	if err := cli.Authenticated(context.Background(), "https://ghe.example.com/"); err != nil {
		t.Fatal(err)
	}
	wantAuthArgs := []string{"auth", "status", "--active", "--hostname", "ghe.example.com"}
	if !reflect.DeepEqual(runner.command.Args, wantAuthArgs) {
		t.Fatalf("auth args = %#v, want %#v", runner.command.Args, wantAuthArgs)
	}
	if len(runner.command.Stdin) != 0 {
		t.Fatalf("auth probe should not require stdin or a TTY, got %q", runner.command.Stdin)
	}

	_, err = cli.RunAPI(context.Background(), "ghe.example.com", ExternalCLIAPIRequest{
		Operation: "get user",
		Method:    http.MethodGet,
		Endpoint:  "/user",
	})
	if err != nil {
		t.Fatal(err)
	}
	wantAPIArgs := []string{"api", "--method", http.MethodGet, "--header", githubAPIVersion, "--hostname", "ghe.example.com", "/user"}
	if !reflect.DeepEqual(runner.command.Args, wantAPIArgs) {
		t.Fatalf("api args = %#v, want %#v", runner.command.Args, wantAPIArgs)
	}
	if len(runner.command.Stdin) != 0 {
		t.Fatalf("GET API call should not require stdin or a TTY, got %q", runner.command.Stdin)
	}
	rendered := runner.command.Render(ExternalCLIRedactor{})
	if strings.Contains(rendered, "auth login") {
		t.Fatalf("non-interactive API command unexpectedly used login flow: %s", rendered)
	}
}

func TestCompatibilityRESTClientBaseIsPurelyHostDerived(t *testing.T) {
	t.Setenv("ISSUE_SPEC_API_URL", "https://api.example.test/custom/")
	if got, want := NewClient("ghe.example.com", "token").BaseURL, "https://ghe.example.com/api/v3"; got != want {
		t.Fatalf("enterprise REST base = %q, want %q", got, want)
	}
}
