package auth

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestParseGitHubBackendMode(t *testing.T) {
	tests := []struct {
		value string
		want  GitHubBackendMode
		ok    bool
	}{
		{want: GitHubBackendModeAuto, ok: true},
		{value: "auto", want: GitHubBackendModeAuto, ok: true},
		{value: "REST", want: GitHubBackendModeREST, ok: true},
		{value: " gh ", want: GitHubBackendModeGH, ok: true},
		{value: "cli", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			got, err := ParseGitHubBackendMode(tt.value)
			if tt.ok && err != nil {
				t.Fatalf("ParseGitHubBackendMode returned error: %v", err)
			}
			if !tt.ok && err == nil {
				t.Fatal("ParseGitHubBackendMode succeeded, want error")
			}
			if got != tt.want {
				t.Fatalf("mode = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSelectGitHubBackendPrefersTokenInAuto(t *testing.T) {
	clearAuthEnv(t)
	t.Setenv("ISSUE_SPEC_TOKEN", "issue-token")

	selection, err := SelectGitHubBackendWithOptions(context.Background(), "github.com", GitHubBackendSelectionOptions{
		GHAuthenticated: func(context.Context, string) error {
			t.Fatal("gh probe should not run when an explicit token exists")
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if selection.Name != GitHubBackendNameREST {
		t.Fatalf("backend = %q, want rest", selection.Name)
	}
	if selection.SelectionSource != "auto:token" {
		t.Fatalf("selection source = %q", selection.SelectionSource)
	}
	if selection.TokenSource != "env:ISSUE_SPEC_TOKEN" {
		t.Fatalf("token source = %q", selection.TokenSource)
	}
	if selection.Token.Value != "issue-token" {
		t.Fatal("selected REST token value was not preserved for backend construction")
	}
}

func TestSelectGitHubBackendForcedRESTRequiresToken(t *testing.T) {
	clearAuthEnv(t)
	t.Setenv(GitHubBackendEnv, "rest")
	t.Setenv("ISSUE_SPEC_CONFIG_DIR", t.TempDir())

	selection, err := SelectGitHubBackendWithOptions(context.Background(), "example.invalid", GitHubBackendSelectionOptions{})
	if !errors.Is(err, ErrNoToken) {
		t.Fatalf("error = %v, want ErrNoToken", err)
	}
	if selection.Name != GitHubBackendNameREST {
		t.Fatalf("backend = %q, want rest", selection.Name)
	}
	if selection.SelectionSource != "override:rest" {
		t.Fatalf("selection source = %q", selection.SelectionSource)
	}
}

func TestSelectGitHubBackendAutoUsesAuthenticatedGHWithoutToken(t *testing.T) {
	clearAuthEnv(t)
	t.Setenv("ISSUE_SPEC_CONFIG_DIR", t.TempDir())
	var probedHost string

	selection, err := SelectGitHubBackendWithOptions(context.Background(), "https://example.invalid/", GitHubBackendSelectionOptions{
		GHAuthenticated: func(_ context.Context, host string) error {
			probedHost = host
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if selection.Name != GitHubBackendNameGH || selection.Kind != GitHubBackendKindCLI {
		t.Fatalf("backend = %q/%q, want gh/external-cli", selection.Name, selection.Kind)
	}
	if selection.SelectionSource != "auto:gh" {
		t.Fatalf("selection source = %q", selection.SelectionSource)
	}
	if probedHost != "example.invalid" {
		t.Fatalf("probed host = %q, want example.invalid", probedHost)
	}
}

func TestSelectGitHubBackendAutoPrefersStoredTokenAndRedactsDiagnostics(t *testing.T) {
	clearAuthEnv(t)
	t.Setenv("ISSUE_SPEC_CONFIG_DIR", t.TempDir())
	const secret = "stored-secret-token"
	if _, err := StoreToken(context.Background(), "stored.example.com", secret, true); err != nil {
		t.Fatal(err)
	}

	selection, err := SelectGitHubBackendWithOptions(context.Background(), "stored.example.com", GitHubBackendSelectionOptions{
		GHAuthenticated: func(context.Context, string) error {
			t.Fatal("gh probe should not run when a stored token exists")
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if selection.Name != GitHubBackendNameREST || selection.SelectionSource != "auto:token" || selection.TokenSource != "config" {
		t.Fatalf("unexpected selection: %+v", selection)
	}
	if selection.Token.Value != secret {
		t.Fatal("stored token value was not preserved for REST backend construction")
	}
	data, err := json.Marshal(selection.TokenWithDiagnostics())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) {
		t.Fatalf("stored token leaked in JSON diagnostics: %s", data)
	}
}

func TestLegacyCustomAPIURLRejectsGitHubEnvironmentTokens(t *testing.T) {
	clearAuthEnv(t)
	t.Setenv(GitHubBackendAPIURLEnv, "https://api.example.test/custom/")
	t.Setenv("GH_TOKEN", "rest-token")

	selection, err := SelectGitHubBackendWithOptions(context.Background(), "ghe.example.com", GitHubBackendSelectionOptions{
		GHAuthenticated: func(context.Context, string) error {
			t.Fatal("gh probe should not run when a REST token and custom API URL are configured")
			return nil
		},
	})
	if !errors.Is(err, ErrNoToken) {
		t.Fatalf("error = %v, want ErrNoToken", err)
	}
	if selection.Profile.Kind != ProfileKindHosted || selection.Profile.Name != "legacy-api-url" {
		t.Fatalf("selection profile = %+v", selection.Profile)
	}
	if selection.Token.Value != "" || selection.Token.Source != "" {
		t.Fatalf("GitHub token crossed into custom profile: %+v", selection.Token)
	}
}

func TestSelectGitHubBackendAutoReportsGHProbeFailure(t *testing.T) {
	clearAuthEnv(t)
	t.Setenv("ISSUE_SPEC_CONFIG_DIR", t.TempDir())

	selection, err := SelectGitHubBackendWithOptions(context.Background(), "github.com", GitHubBackendSelectionOptions{
		GHAuthenticated: func(context.Context, string) error {
			return errors.New("not logged in")
		},
	})
	if err == nil {
		t.Fatal("selection succeeded, want gh probe failure")
	}
	if !errors.Is(err, ErrNoToken) {
		t.Fatalf("error = %v, want ErrNoToken context", err)
	}
	if len(selection.Probes) != 2 {
		t.Fatalf("probes = %+v, want rest and gh probe records", selection.Probes)
	}
	if selection.Probes[0].Name != GitHubBackendNameREST || selection.Probes[0].Status != "unavailable" {
		t.Fatalf("rest probe = %+v", selection.Probes[0])
	}
	if selection.Probes[1].Name != GitHubBackendNameGH || selection.Probes[1].Status != "unavailable" || !strings.Contains(selection.Probes[1].Error, "not logged in") {
		t.Fatalf("gh probe = %+v", selection.Probes[1])
	}
	if !strings.Contains(err.Error(), "gh authentication probe failed for github.com") {
		t.Fatalf("error missing probe context: %v", err)
	}
}

func TestSelectGitHubBackendForcedGHRejectsCustomAPIURL(t *testing.T) {
	clearAuthEnv(t)
	t.Setenv(GitHubBackendEnv, "gh")
	t.Setenv(GitHubBackendAPIURLEnv, "https://api.example.test")

	selection, err := SelectGitHubBackendWithOptions(context.Background(), "github.com", GitHubBackendSelectionOptions{})
	if err == nil {
		t.Fatal("forced gh with custom API URL succeeded, want error")
	}
	if selection.Name != GitHubBackendNameGH {
		t.Fatalf("backend = %q, want gh", selection.Name)
	}
	if !strings.Contains(err.Error(), GitHubBackendAPIURLEnv) {
		t.Fatalf("error does not mention %s: %v", GitHubBackendAPIURLEnv, err)
	}
}

func TestSelectGitHubBackendInvalidOverride(t *testing.T) {
	clearAuthEnv(t)
	t.Setenv(GitHubBackendEnv, "bad")

	_, err := SelectGitHubBackendWithOptions(context.Background(), "github.com", GitHubBackendSelectionOptions{})
	if err == nil {
		t.Fatal("invalid backend override succeeded, want error")
	}
	if !strings.Contains(err.Error(), GitHubBackendEnv) {
		t.Fatalf("error does not mention %s: %v", GitHubBackendEnv, err)
	}
}

func clearAuthEnv(t *testing.T) {
	t.Helper()
	t.Setenv(GitHubBackendEnv, "")
	t.Setenv(GitHubBackendAPIURLEnv, "")
	t.Setenv(ProfileEnv, "")
	t.Setenv("ISSUE_SPEC_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
}
