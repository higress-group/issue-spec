package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
)

func TestAuthStatusJSONIncludesBackendDiagnosticsWithoutToken(t *testing.T) {
	t.Setenv(auth.ProfileEnv, "")
	t.Setenv(auth.ConfigDirEnv, t.TempDir())
	t.Chdir(t.TempDir())
	const secret = "secret-token-value"
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.selectGitHubBackend = func(context.Context, string) (auth.GitHubBackendSelection, error) {
		return auth.GitHubBackendSelection{
			Mode:            auth.GitHubBackendModeAuto,
			Name:            auth.GitHubBackendNameREST,
			Kind:            auth.GitHubBackendKindREST,
			Host:            "github.com",
			SelectionSource: "auto:token",
			TokenSource:     "env:ISSUE_SPEC_TOKEN",
			Token:           auth.Token{Value: secret, Source: "env:ISSUE_SPEC_TOKEN", Host: "github.com"},
		}, nil
	}
	app.newGitHubBackend = func(_ context.Context, selection auth.GitHubBackendSelection) (github.Backend, error) {
		return fakeGitHubBackend{
			info:   github.BackendInfo{Name: selection.Name, Kind: selection.Kind, Host: selection.Host},
			user:   github.User{Login: "octocat"},
			scopes: []string{"repo"},
		}, nil
	}

	code := app.runAuthStatus(context.Background(), []string{"--json"})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%q", code, errOut.String())
	}
	if strings.Contains(out.String(), secret) || strings.Contains(errOut.String(), secret) {
		t.Fatalf("token leaked in output: stdout=%q stderr=%q", out.String(), errOut.String())
	}
	var got struct {
		OK      bool                          `json:"ok"`
		Auth    auth.Token                    `json:"auth"`
		Backend auth.GitHubBackendDiagnostics `json:"backend"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.OK {
		t.Fatalf("ok = false in %s", out.String())
	}
	if got.Auth.Source != "env:ISSUE_SPEC_TOKEN" || got.Auth.User != "octocat" || got.Auth.Host != "github.com" {
		t.Fatalf("unexpected auth metadata: %+v", got.Auth)
	}
	if got.Backend.Name != "rest" || got.Backend.SelectionSource != "auto:token" || got.Backend.TokenSource != "env:ISSUE_SPEC_TOKEN" {
		t.Fatalf("unexpected backend diagnostics: %+v", got.Backend)
	}
}

func TestDefaultAuthStatusAutoSelectsGHWhenProbeSucceeds(t *testing.T) {
	clearCommandAuthEnv(t)
	oldGHAuthenticated := ghAuthenticated
	t.Cleanup(func() { ghAuthenticated = oldGHAuthenticated })
	var probedHost string
	ghAuthenticated = func(_ context.Context, host string) error {
		probedHost = host
		return nil
	}

	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	var selected auth.GitHubBackendSelection
	app.newGitHubBackend = func(_ context.Context, selection auth.GitHubBackendSelection) (github.Backend, error) {
		selected = selection
		return fakeGitHubBackend{
			info: github.BackendInfo{Name: selection.Name, Kind: selection.Kind, Host: selection.Host},
			user: github.User{Login: "octocat"},
		}, nil
	}

	code := app.runAuthStatus(context.Background(), []string{"--hostname", "https://no-token-gh.example.invalid/", "--json"})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%q", code, errOut.String())
	}
	if probedHost != "no-token-gh.example.invalid" {
		t.Fatalf("probed host = %q, want normalized gh host", probedHost)
	}
	if selected.Name != auth.GitHubBackendNameGH || selected.Kind != auth.GitHubBackendKindCLI || selected.SelectionSource != "auto:gh" {
		t.Fatalf("selection = %+v, want auto gh", selected)
	}
	var got struct {
		OK      bool                          `json:"ok"`
		Backend auth.GitHubBackendDiagnostics `json:"backend"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.OK || got.Backend.Name != "gh" || got.Backend.SelectionSource != "auto:gh" {
		t.Fatalf("unexpected auth status JSON: %+v", got)
	}
}

func TestDefaultGHBackendRedactsEnvTokensInAuthStatusErrorPaths(t *testing.T) {
	clearCommandAuthEnv(t)
	const issueToken = "issue-spec-env-secret"
	const ghToken = "gh-env-secret"
	const githubToken = "github-env-secret"
	t.Setenv(auth.GitHubBackendEnv, "gh")
	t.Setenv("ISSUE_SPEC_TOKEN", issueToken)
	t.Setenv("GH_TOKEN", ghToken)
	t.Setenv("GITHUB_TOKEN", githubToken)
	installFakeGH(t, `#!/bin/sh
printf 'fake gh stderr: %s %s %s\n' "$ISSUE_SPEC_TOKEN" "$GH_TOKEN" "$GITHUB_TOKEN" >&2
exit 1
`)

	for _, tt := range []struct {
		name string
		args []string
	}{
		{name: "json", args: []string{"--json"}},
		{name: "stderr"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			app := newApp(strings.NewReader(""), &out, &errOut)

			code := app.runAuthStatus(context.Background(), tt.args)
			if tt.name == "stderr" && code != 1 {
				t.Fatalf("exit code = %d, want 1, stdout=%q stderr=%q", code, out.String(), errOut.String())
			}
			combined := out.String() + errOut.String()
			for _, secret := range []string{issueToken, ghToken, githubToken} {
				if strings.Contains(combined, secret) {
					t.Fatalf("token leaked in %s output: stdout=%q stderr=%q", tt.name, out.String(), errOut.String())
				}
			}
			if !strings.Contains(combined, "[REDACTED]") {
				t.Fatalf("output missing redaction marker: stdout=%q stderr=%q", out.String(), errOut.String())
			}
			if tt.name == "json" {
				var got struct {
					OK    bool   `json:"ok"`
					Error string `json:"error"`
				}
				if err := json.Unmarshal(out.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if got.OK || !strings.Contains(got.Error, "[REDACTED]") {
					t.Fatalf("unexpected JSON error: %+v", got)
				}
			}
		})
	}
}

func TestAuthStatusJSONRedactsRESTErrorBodyToken(t *testing.T) {
	clearCommandAuthEnv(t)
	t.Setenv(auth.ProfileEnv, "")
	const secret = "rest-error-body-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+secret {
			t.Errorf("authorization header = %q", got)
		}
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"message":"proxy echoed Authorization Bearer ` + secret + `"}`))
	}))
	defer server.Close()
	t.Setenv("ISSUE_SPEC_TOKEN", secret)
	t.Setenv(auth.GitHubBackendAPIURLEnv, server.URL)

	var out, errOut bytes.Buffer
	code := Execute([]string{"auth", "status", "--json"}, strings.NewReader(""), &out, &errOut)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%q", code, errOut.String())
	}
	combined := out.String() + errOut.String()
	if strings.Contains(combined, secret) {
		t.Fatalf("REST token leaked in auth status output: stdout=%q stderr=%q", out.String(), errOut.String())
	}
	var got struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.OK || !strings.Contains(got.Error, "[REDACTED]") {
		t.Fatalf("unexpected JSON error: %+v", got)
	}
}

func TestAuthTokenJSONForGHDoesNotFetchTokenUnlessIncluded(t *testing.T) {
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.selectGitHubBackend = func(context.Context, string) (auth.GitHubBackendSelection, error) {
		return auth.GitHubBackendSelection{
			Mode:            auth.GitHubBackendModeGH,
			Name:            auth.GitHubBackendNameGH,
			Kind:            auth.GitHubBackendKindCLI,
			Host:            "github.com",
			SelectionSource: "override:gh",
			Token:           auth.Token{Source: "gh", Host: "github.com"},
		}, nil
	}
	app.gitHubBackendToken = func(context.Context, auth.GitHubBackendSelection) (string, error) {
		t.Fatal("gh token provider should not run without --include-token")
		return "", nil
	}

	code := app.runAuthToken(context.Background(), []string{"--json"})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%q", code, errOut.String())
	}
	if strings.Contains(out.String(), "token") {
		t.Fatalf("unexpected token field in output: %s", out.String())
	}
	var got struct {
		Host    string                        `json:"host"`
		Source  string                        `json:"source"`
		Backend auth.GitHubBackendDiagnostics `json:"backend"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Source != "gh" || got.Backend.Name != "gh" || got.Backend.Kind != "external-cli" {
		t.Fatalf("unexpected gh token metadata: %+v", got)
	}
}

func TestAuthTokenPlainForGHUsesExplicitTokenProvider(t *testing.T) {
	const secret = "gh-secret-token"
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.selectGitHubBackend = func(context.Context, string) (auth.GitHubBackendSelection, error) {
		return auth.GitHubBackendSelection{
			Mode:            auth.GitHubBackendModeGH,
			Name:            auth.GitHubBackendNameGH,
			Kind:            auth.GitHubBackendKindCLI,
			Host:            "github.com",
			SelectionSource: "override:gh",
			Token:           auth.Token{Source: "gh", Host: "github.com"},
		}, nil
	}
	app.gitHubBackendToken = func(context.Context, auth.GitHubBackendSelection) (string, error) {
		return secret, nil
	}

	code := app.runAuthToken(context.Background(), []string{"--plain"})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%q", code, errOut.String())
	}
	if strings.TrimSpace(out.String()) != secret {
		t.Fatalf("plain token output = %q", out.String())
	}
}

func TestAuthTokenJSONIncludeTokenForGHUsesExplicitTokenProvider(t *testing.T) {
	t.Setenv(auth.ProfileEnv, "")
	t.Setenv(auth.ConfigDirEnv, t.TempDir())
	t.Chdir(t.TempDir())
	const secret = "gh-secret-token"
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.selectGitHubBackend = func(context.Context, string) (auth.GitHubBackendSelection, error) {
		return auth.GitHubBackendSelection{
			Mode:            auth.GitHubBackendModeGH,
			Name:            auth.GitHubBackendNameGH,
			Kind:            auth.GitHubBackendKindCLI,
			Host:            "github.com",
			SelectionSource: "override:gh",
			Token:           auth.Token{Source: "gh", Host: "github.com"},
		}, nil
	}
	app.gitHubBackendToken = func(context.Context, auth.GitHubBackendSelection) (string, error) {
		return secret, nil
	}

	code := app.runAuthToken(context.Background(), []string{"--json", "--include-token"})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%q", code, errOut.String())
	}
	var got struct {
		Token   string                        `json:"token"`
		Backend auth.GitHubBackendDiagnostics `json:"backend"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Token != secret || got.Backend.Name != "gh" || got.Backend.SelectionSource != "override:gh" {
		t.Fatalf("unexpected token JSON: %+v", got)
	}
}

func TestResolveAuthLoginProfilePreservesTrustedSelfHostedOperatorSettings(t *testing.T) {
	clearCommandAuthEnv(t)
	t.Setenv(auth.ConfigDirEnv, t.TempDir())
	existing := auth.Profile{
		Name: "team", Kind: auth.ProfileKindHosted,
		APIURL: "https://issues.example.test/api/v3", NativeAPIURL: "https://issues.example.test/api/v1",
		WebURL: "https://issues.example.test", ServerInstanceID: "issue-spec:team",
		OperatorRegistryFile: "/opt/issue-spec/providers.json",
		OnboardingPolicy:     auth.OnboardingPolicy{AllowRepositoryCreate: true, AllowSourceBinding: true},
	}
	if err := auth.SaveProfile(existing, false); err != nil {
		t.Fatal(err)
	}

	flags := authLoginProfileFlags{Name: "team", Kind: "self-hosted", APIURL: existing.APIURL,
		NativeAPIURL: existing.NativeAPIURL, WebURL: existing.WebURL, InstanceID: existing.ServerInstanceID}
	resolved, configured, err := resolveAuthLoginProfile(flags, "issues.example.test")
	if err != nil || !configured {
		t.Fatalf("resolveAuthLoginProfile error = %v configured=%v", err, configured)
	}
	if resolved.OperatorRegistryFile != existing.OperatorRegistryFile || resolved.OnboardingPolicy != existing.OnboardingPolicy {
		t.Fatalf("trusted realm lost operator settings: %+v", resolved)
	}

	flags.InstanceID = "issue-spec:other"
	resolved, configured, err = resolveAuthLoginProfile(flags, "issues.example.test")
	if err != nil || !configured {
		t.Fatalf("resolveAuthLoginProfile changed realm error = %v configured=%v", err, configured)
	}
	if resolved.OperatorRegistryFile != "" || resolved.OnboardingPolicy != (auth.OnboardingPolicy{}) {
		t.Fatalf("changed realm inherited operator settings: %+v", resolved)
	}
}

func TestAuthLoginWithoutTokenRecommendsAuthenticatedGH(t *testing.T) {
	stubGHDiscovery(t, true, nil)
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)

	code := app.runAuthLogin(context.Background(), nil)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%q", code, errOut.String())
	}
	output := out.String()
	for _, want := range []string{
		"GitHub CLI is installed and authenticated",
		"reuse your gh CLI login directly",
		"issue-spec auth status --json",
		"For the REST token storage path instead",
		"issue-spec auth login --with-token",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("auth login output missing %q:\n%s", want, output)
		}
	}
	if errOut.Len() != 0 {
		t.Fatalf("unexpected stderr: %q", errOut.String())
	}
}

func TestAuthLoginWithoutTokenPromptsGHAuthWhenInstalledUnauthenticated(t *testing.T) {
	stubGHDiscovery(t, true, errors.New("not logged in"))
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)

	code := app.runAuthLogin(context.Background(), []string{"--hostname", "https://ghe.example.com/"})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%q", code, errOut.String())
	}
	output := out.String()
	for _, want := range []string{
		"GitHub CLI is installed but is not authenticated for ghe.example.com",
		"gh auth login --hostname ghe.example.com",
		"issue-spec auth status --hostname ghe.example.com --json",
		"For the REST token storage path instead",
		"issue-spec auth login --hostname ghe.example.com --with-token",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("auth login output missing %q:\n%s", want, output)
		}
	}
}

func TestAuthLoginJSONUsesStableGHUnauthenticatedError(t *testing.T) {
	const secret = "gh-stderr-secret"
	stubGHDiscovery(t, true, errors.New("raw gh stderr included "+secret))
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)

	code := app.runAuthLogin(context.Background(), []string{"--hostname", "https://ghe.example.com/", "--json"})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%q", code, errOut.String())
	}
	combined := out.String() + errOut.String()
	for _, forbidden := range []string{"raw gh stderr", secret} {
		if strings.Contains(combined, forbidden) {
			t.Fatalf("auth login JSON leaked %q: stdout=%q stderr=%q", forbidden, out.String(), errOut.String())
		}
	}
	var got struct {
		OK        bool     `json:"ok"`
		Host      string   `json:"host"`
		Backend   string   `json:"backend"`
		Mode      string   `json:"mode"`
		NextSteps []string `json:"next_steps"`
		GitHubCLI struct {
			Installed     bool   `json:"installed"`
			Authenticated bool   `json:"authenticated"`
			Error         string `json:"error"`
		} `json:"github_cli"`
		RESTLoginCommand string `json:"rest_login_command"`
		GHLoginCommand   string `json:"gh_login_command"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.OK || got.Host != "ghe.example.com" || got.Backend != "gh" || got.Mode != "gh-needs-auth" {
		t.Fatalf("unexpected auth login advice: %+v", got)
	}
	if !got.GitHubCLI.Installed || got.GitHubCLI.Authenticated || got.GitHubCLI.Error != "not_authenticated" {
		t.Fatalf("unexpected gh diagnostics: %+v", got.GitHubCLI)
	}
	for _, want := range []string{
		"gh auth login --hostname ghe.example.com",
		"issue-spec auth status --hostname ghe.example.com --json",
		"issue-spec auth login --hostname ghe.example.com --with-token",
	} {
		if !containsString(got.NextSteps, want) && got.RESTLoginCommand != want && got.GHLoginCommand != want {
			t.Fatalf("auth login advice missing host-aware command %q: %+v", want, got)
		}
	}
}

func TestAuthLoginWithoutTokenFallsBackToRESTWhenGHMissing(t *testing.T) {
	stubGHDiscovery(t, false, nil)
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)

	code := app.runAuthLogin(context.Background(), []string{"--json"})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%q", code, errOut.String())
	}
	var got struct {
		OK        bool `json:"ok"`
		GitHubCLI struct {
			Installed bool `json:"installed"`
		} `json:"github_cli"`
		Backend          string `json:"backend"`
		Mode             string `json:"mode"`
		RESTLoginCommand string `json:"rest_login_command"`
		GHDownloadURL    string `json:"gh_download_url"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.OK || got.GitHubCLI.Installed || got.Backend != "rest" || got.Mode != "rest-fallback" {
		t.Fatalf("unexpected auth login advice: %+v", got)
	}
	if got.RESTLoginCommand != "issue-spec auth login --with-token" || got.GHDownloadURL != "https://cli.github.com/" {
		t.Fatalf("unexpected commands in advice: %+v", got)
	}
}

func TestInitJSONIncludesBackendDiagnostics(t *testing.T) {
	t.Setenv(auth.ProfileEnv, "")
	t.Setenv(auth.ConfigDirEnv, t.TempDir())
	t.Chdir(t.TempDir())
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.selectGitHubBackend = func(context.Context, string) (auth.GitHubBackendSelection, error) {
		return auth.GitHubBackendSelection{
			Mode:            auth.GitHubBackendModeAuto,
			Name:            auth.GitHubBackendNameREST,
			Kind:            auth.GitHubBackendKindREST,
			Host:            "github.com",
			SelectionSource: "auto:token",
			TokenSource:     "config",
			Token:           auth.Token{Value: "stored-secret", Source: "config", Host: "github.com"},
		}, nil
	}
	app.newGitHubBackend = func(_ context.Context, selection auth.GitHubBackendSelection) (github.Backend, error) {
		return fakeGitHubBackend{
			info: github.BackendInfo{Name: selection.Name, Kind: selection.Kind, Host: selection.Host},
			user: github.User{Login: "octocat"},
			createLabel: func(_ context.Context, _ string, name, _, _ string) (github.LabelResult, error) {
				return github.LabelResult{Name: name, Skipped: true}, nil
			},
		}, nil
	}

	code := app.runInit(context.Background(), []string{"--repo", "o/r", "--tools", "none", "--json"})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%q", code, errOut.String())
	}
	if strings.Contains(out.String(), "stored-secret") || strings.Contains(errOut.String(), "stored-secret") {
		t.Fatalf("token leaked in init output: stdout=%q stderr=%q", out.String(), errOut.String())
	}
	var got struct {
		OK      bool                          `json:"ok"`
		Backend auth.GitHubBackendDiagnostics `json:"backend"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.OK || got.Backend.Name != "rest" || got.Backend.TokenSource != "config" {
		t.Fatalf("unexpected init result: %+v", got)
	}
}

func TestIssueCreateUsesSelectedGHBackend(t *testing.T) {
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.selectGitHubBackend = ghSelection
	var created bool
	app.newGitHubBackend = func(_ context.Context, selection auth.GitHubBackendSelection) (github.Backend, error) {
		if selection.Name != auth.GitHubBackendNameGH {
			t.Fatalf("backend = %q, want gh", selection.Name)
		}
		return fakeGitHubBackend{
			info: github.BackendInfo{Name: selection.Name, Kind: selection.Kind, Host: selection.Host},
			createIssue: func(_ context.Context, repo, title, body string, labels []string) (github.Issue, error) {
				created = true
				if repo != "o/r" || title != "Proposal: gh-proxy" || !strings.Contains(body, "Proposal: gh-proxy") {
					t.Fatalf("unexpected issue create args repo=%q title=%q body=%q", repo, title, body)
				}
				if len(labels) != 1 || labels[0] != "issue-spec/proposal" {
					t.Fatalf("labels = %#v", labels)
				}
				return github.Issue{Number: 9, HTMLURL: "https://github.com/o/r/issues/9", Title: title}, nil
			},
		}, nil
	}

	code := app.runIssueCreate(context.Background(), "proposal", []string{"--repo", "o/r", "--change", "gh-proxy", "--json"})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%q", code, errOut.String())
	}
	if !created {
		t.Fatal("CreateIssue was not called")
	}
	var got struct {
		OK     bool `json:"ok"`
		Number int  `json:"number"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.OK || got.Number != 9 {
		t.Fatalf("unexpected issue create output: %+v", got)
	}
}

func TestCommentListUsesSelectedGHBackend(t *testing.T) {
	body, err := model.EnsureTypedBody("SPEC", "SPEC-001", "## Requirement: X\n\nX MUST work.\n", model.BodyOptions{Status: "confirmed", Scope: "backend"})
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.selectGitHubBackend = ghSelection
	app.newGitHubBackend = func(_ context.Context, selection auth.GitHubBackendSelection) (github.Backend, error) {
		return fakeGitHubBackend{
			info: github.BackendInfo{Name: selection.Name, Kind: selection.Kind, Host: selection.Host},
			listIssueComments: func(_ context.Context, repo string, issueNumber int) ([]github.Comment, error) {
				if repo != "o/r" || issueNumber != 9 {
					t.Fatalf("unexpected comment list args repo=%q issue=%d", repo, issueNumber)
				}
				return []github.Comment{{ID: 101, HTMLURL: "https://github.com/o/r/issues/9#issuecomment-101", URL: "https://api.github.com/repos/o/r/issues/comments/101", Body: body}}, nil
			},
		}, nil
	}

	code := app.runCommentList(context.Background(), []string{"--repo", "o/r", "--issue", "9", "--type", "SPEC", "--json"})
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%q", code, errOut.String())
	}
	var got struct {
		OK       bool `json:"ok"`
		Comments []struct {
			Comment struct {
				ID string `json:"id"`
			} `json:"comment"`
		} `json:"comments"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.OK || len(got.Comments) != 1 || got.Comments[0].Comment.ID != "SPEC-001" {
		t.Fatalf("unexpected comment list output: %+v", got)
	}
}

func TestDefaultNewGitHubBackendConstructsGHBackend(t *testing.T) {
	backend, err := defaultNewGitHubBackend(context.Background(), auth.GitHubBackendSelection{
		Name: auth.GitHubBackendNameGH,
		Kind: auth.GitHubBackendKindCLI,
		Host: "https://ghe.example.com/",
	})
	if err != nil {
		t.Fatal(err)
	}
	info := backend.BackendInfo()
	if info.Name != "gh" || info.Kind != "external-cli" || info.Host != "ghe.example.com" {
		t.Fatalf("backend info = %+v", info)
	}
}

func TestDefaultGitHubBackendTokenForGHUsesProvider(t *testing.T) {
	old := ghAuthToken
	t.Cleanup(func() { ghAuthToken = old })
	var gotHost string
	ghAuthToken = func(_ context.Context, host string) (string, error) {
		gotHost = host
		return "gh-token", nil
	}

	token, err := defaultGitHubBackendToken(context.Background(), auth.GitHubBackendSelection{
		Name: auth.GitHubBackendNameGH,
		Host: "ghe.example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if token != "gh-token" || gotHost != "ghe.example.com" {
		t.Fatalf("token = %q host = %q", token, gotHost)
	}
}

func TestAuthLoginAndStatusUseNamedSelfHostedProfile(t *testing.T) {
	clearCommandAuthEnv(t)
	t.Setenv(auth.ProfileEnv, "")
	t.Chdir(t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user" || r.Header.Get("Authorization") != "Bearer profile-secret" {
			t.Fatalf("request = %s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		w.Header().Set("X-OAuth-Scopes", "repo, workflow")
		_, _ = w.Write([]byte(`{"login":"profile-user"}`))
	}))
	defer server.Close()

	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader("profile-secret\n"), &out, &errOut)
	code := app.runAuthLogin(context.Background(), []string{
		"--profile", "staging", "--kind", "self-hosted",
		"--api-url", server.URL, "--native-api-url", server.URL + "/api/v1",
		"--web-url", server.URL, "--instance-id", "instance-staging",
		"--with-token", "--insecure-storage", "--make-default", "--json",
	})
	if code != 0 {
		t.Fatalf("login exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if strings.Contains(out.String()+errOut.String(), "profile-secret") {
		t.Fatalf("login leaked token: stdout=%q stderr=%q", out.String(), errOut.String())
	}
	profile, source, err := auth.ResolveProfileAt("", "github.com", t.TempDir())
	if err != nil || source != "config" || profile.Name != "staging" {
		t.Fatalf("default profile = %+v source=%q err=%v", profile, source, err)
	}
	token, err := auth.ResolveProfileToken(context.Background(), profile)
	if err != nil || token.Value != "profile-secret" || token.Source != "config" {
		t.Fatalf("stored token = %+v err=%v", token, err)
	}

	out.Reset()
	errOut.Reset()
	t.Setenv("GH_TOKEN", "must-not-cross-gh-token")
	t.Setenv("GITHUB_TOKEN", "must-not-cross-github-token")
	app = newApp(strings.NewReader(""), &out, &errOut)
	code = app.runAuthStatus(context.Background(), []string{"--profile", "staging", "--json"})
	if code != 0 {
		t.Fatalf("status exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	var status struct {
		Auth auth.Token `json:"auth"`
	}
	if err := json.Unmarshal(out.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Auth.Profile != "staging" || status.Auth.ProfileKind != "self-hosted" || status.Auth.ServerInstanceID != "instance-staging" || status.Auth.User != "profile-user" {
		t.Fatalf("status = %+v", status.Auth)
	}
}

func TestAuthLoginRejectsCrossOriginSelfHostedEndpointsBeforeRequest(t *testing.T) {
	clearCommandAuthEnv(t)
	var apiRequests, nativeRequests int
	apiServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		apiRequests++
	}))
	defer apiServer.Close()
	nativeServer := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		nativeRequests++
		if r.Header.Get("Authorization") != "" {
			t.Fatalf("cross-origin request carried authorization: %q", r.Header.Get("Authorization"))
		}
	}))
	defer nativeServer.Close()

	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader("profile-secret\n"), &out, &errOut)
	code := app.runAuthLogin(context.Background(), []string{
		"--profile", "staging", "--kind", "self-hosted",
		"--api-url", apiServer.URL + "/api/v3", "--native-api-url", nativeServer.URL + "/api/v1",
		"--web-url", apiServer.URL, "--instance-id", "instance-staging",
		"--with-token", "--insecure-storage",
	})
	if code != 1 || !strings.Contains(errOut.String(), "native API URL must use the same origin as API URL") {
		t.Fatalf("login exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if apiRequests != 0 || nativeRequests != 0 {
		t.Fatalf("cross-origin login sent requests: api=%d native=%d", apiRequests, nativeRequests)
	}
	if strings.Contains(out.String()+errOut.String(), "profile-secret") {
		t.Fatalf("login leaked token: stdout=%q stderr=%q", out.String(), errOut.String())
	}
	if _, _, err := auth.ResolveProfile("staging", "github.com"); err == nil {
		t.Fatal("rejected cross-origin profile was persisted")
	}
}

func TestAuthStatusSelfHostedRunnerUsesTokenFileWithoutGH(t *testing.T) {
	clearCommandAuthEnv(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user" || r.Header.Get("Authorization") != "Bearer delegated-child" {
			t.Fatalf("request = %s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"login":"runner-child"}`))
	}))
	defer server.Close()
	profile := auth.Profile{Name: "runner", Kind: auth.ProfileKindHosted, APIURL: server.URL,
		NativeAPIURL: server.URL + "/api/v1", WebURL: server.URL, ServerInstanceID: "instance-runner"}
	if err := auth.SaveProfile(profile, true); err != nil {
		t.Fatal(err)
	}
	tokenPath := filepath.Join(t.TempDir(), "issue.token")
	if err := os.WriteFile(tokenPath, []byte("delegated-child\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(auth.IssueSpecTokenFileEnv, tokenPath)
	t.Setenv(auth.ProfileEnv, "runner")
	t.Setenv(auth.GitHubBackendEnv, "rest")
	t.Setenv("GH_TOKEN", "must-not-cross")
	t.Setenv("GITHUB_TOKEN", "must-not-cross-either")
	oldGHAuthenticated := ghAuthenticated
	t.Cleanup(func() { ghAuthenticated = oldGHAuthenticated })
	ghAuthenticated = func(context.Context, string) error {
		t.Fatal("gh authentication was probed for self-hosted runner profile")
		return nil
	}
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	if code := app.runAuthStatus(context.Background(), []string{"--json"}); code != 0 {
		t.Fatalf("status exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	if strings.Contains(out.String()+errOut.String(), "delegated-child") || strings.Contains(out.String()+errOut.String(), "must-not-cross") {
		t.Fatalf("credential leaked: stdout=%q stderr=%q", out.String(), errOut.String())
	}
	var status struct {
		Auth auth.Token `json:"auth"`
	}
	if err := json.Unmarshal(out.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Auth.Source != "env:"+auth.IssueSpecTokenFileEnv || status.Auth.Profile != "runner" || status.Auth.User != "runner-child" {
		t.Fatalf("status auth = %+v", status.Auth)
	}
}

func TestOrdinaryClientUsesSavedDefaultSelfHostedProfile(t *testing.T) {
	clearCommandAuthEnv(t)
	t.Setenv(auth.ProfileEnv, "")
	t.Chdir(t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer default-secret" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"login":"default-user"}`))
	}))
	defer server.Close()
	profile := auth.Profile{
		Name: "staging", Kind: auth.ProfileKindHosted, APIURL: server.URL,
		NativeAPIURL: server.URL + "/api/v1", WebURL: server.URL, ServerInstanceID: "instance-default",
	}
	if err := auth.SaveProfile(profile, true); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.StoreProfileToken(context.Background(), profile, "default-secret", true); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.selectGitHubBackend = func(context.Context, string) (auth.GitHubBackendSelection, error) {
		t.Fatal("legacy GitHub selector used instead of saved default profile")
		return auth.GitHubBackendSelection{}, nil
	}
	backend, token, err := app.clientFor(context.Background(), "github.com")
	if err != nil {
		t.Fatal(err)
	}
	user, _, err := backend.GetUser(context.Background())
	if err != nil || user.Login != "default-user" || token.Profile != "staging" || token.APIOrigin != server.URL {
		t.Fatalf("user=%+v token=%+v err=%v", user, token, err)
	}
}

func TestGlobalProfileFlagIsAvailableToOrdinaryCommands(t *testing.T) {
	profile, args, err := extractGlobalProfile([]string{"issue", "create", "proposal", "--repo", "o/r", "--profile", "staging", "--json"})
	if err != nil {
		t.Fatal(err)
	}
	if profile != "staging" || strings.Join(args, " ") != "issue create proposal --repo o/r --json" {
		t.Fatalf("profile=%q args=%q", profile, args)
	}
	if _, _, err := extractGlobalProfile([]string{"status", "--profile", "a", "--profile", "b"}); err == nil {
		t.Fatal("conflicting profile flags accepted")
	}
}

func clearCommandAuthEnv(t *testing.T) {
	t.Helper()
	t.Setenv(auth.GitHubBackendEnv, "")
	t.Setenv(auth.GitHubBackendAPIURLEnv, "")
	t.Setenv(auth.ProfileEnv, auth.DefaultProfileName)
	t.Setenv("ISSUE_SPEC_TOKEN", "")
	t.Setenv(auth.IssueSpecTokenFileEnv, "")
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("ISSUE_SPEC_CONFIG_DIR", t.TempDir())
}

func installFakeGH(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "gh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func stubGHDiscovery(t *testing.T, installed bool, authErr error) {
	t.Helper()
	oldLookPath := ghLookPath
	oldAuthenticated := ghAuthenticated
	t.Cleanup(func() {
		ghLookPath = oldLookPath
		ghAuthenticated = oldAuthenticated
	})
	ghLookPath = func(string) (string, error) {
		if installed {
			return "/usr/bin/gh", nil
		}
		return "", errors.New("gh not found")
	}
	ghAuthenticated = func(context.Context, string) error {
		return authErr
	}
}

func ghSelection(context.Context, string) (auth.GitHubBackendSelection, error) {
	return auth.GitHubBackendSelection{
		Mode:            auth.GitHubBackendModeGH,
		Name:            auth.GitHubBackendNameGH,
		Kind:            auth.GitHubBackendKindCLI,
		Host:            "github.com",
		SelectionSource: "override:gh",
		Token:           auth.Token{Source: "gh", Host: "github.com"},
	}, nil
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

type fakeGitHubBackend struct {
	info              github.BackendInfo
	user              github.User
	scopes            []string
	createIssue       func(context.Context, string, string, string, []string) (github.Issue, error)
	getIssue          func(context.Context, string, int) (github.Issue, error)
	updateIssue       func(context.Context, string, int, github.UpdateIssueOptions) (github.Issue, error)
	listIssueComments func(context.Context, string, int) ([]github.Comment, error)
	getPullRequest    func(context.Context, string, int) (github.PullRequest, error)
	updatePullRequest func(context.Context, string, int, github.UpdatePullRequestOptions) (github.PullRequest, error)
	createComment     func(context.Context, string, int, string) (github.Comment, error)
	updateComment     func(context.Context, string, int64, string) (github.Comment, error)
	createLabel       func(context.Context, string, string, string, string) (github.LabelResult, error)

	listPRReviewComments func(context.Context, string, int) ([]github.PullRequestReviewComment, error)
}

func (f fakeGitHubBackend) BackendInfo() github.BackendInfo {
	return f.info
}

func (f fakeGitHubBackend) GetUser(context.Context) (github.User, []string, error) {
	return f.user, f.scopes, nil
}

func (f fakeGitHubBackend) CreateIssue(ctx context.Context, repo, title, body string, labels []string) (github.Issue, error) {
	if f.createIssue != nil {
		return f.createIssue(ctx, repo, title, body, labels)
	}
	return github.Issue{}, errors.New("unused")
}

func (f fakeGitHubBackend) GetIssue(ctx context.Context, repo string, issueNumber int) (github.Issue, error) {
	if f.getIssue != nil {
		return f.getIssue(ctx, repo, issueNumber)
	}
	return github.Issue{}, errors.New("unused")
}

func (f fakeGitHubBackend) UpdateIssue(ctx context.Context, repo string, issueNumber int, opts github.UpdateIssueOptions) (github.Issue, error) {
	if f.updateIssue != nil {
		return f.updateIssue(ctx, repo, issueNumber, opts)
	}
	return github.Issue{}, errors.New("unused")
}

func (f fakeGitHubBackend) ListIssueComments(ctx context.Context, repo string, issueNumber int) ([]github.Comment, error) {
	if f.listIssueComments != nil {
		return f.listIssueComments(ctx, repo, issueNumber)
	}
	return nil, errors.New("unused")
}

func (f fakeGitHubBackend) CreateComment(ctx context.Context, repo string, issueNumber int, body string) (github.Comment, error) {
	if f.createComment != nil {
		return f.createComment(ctx, repo, issueNumber, body)
	}
	return github.Comment{}, errors.New("unused")
}

func (f fakeGitHubBackend) UpdateComment(ctx context.Context, repo string, commentID int64, body string) (github.Comment, error) {
	if f.updateComment != nil {
		return f.updateComment(ctx, repo, commentID, body)
	}
	return github.Comment{}, errors.New("unused")
}

func (f fakeGitHubBackend) CreateLabel(ctx context.Context, repo, name, color, description string) (github.LabelResult, error) {
	if f.createLabel != nil {
		return f.createLabel(ctx, repo, name, color, description)
	}
	return github.LabelResult{}, errors.New("unused")
}

func (f fakeGitHubBackend) GetPullRequest(ctx context.Context, repo string, prNumber int) (github.PullRequest, error) {
	if f.getPullRequest != nil {
		return f.getPullRequest(ctx, repo, prNumber)
	}
	return github.PullRequest{}, errors.New("unused")
}

func (f fakeGitHubBackend) UpdatePullRequest(ctx context.Context, repo string, prNumber int, opts github.UpdatePullRequestOptions) (github.PullRequest, error) {
	if f.updatePullRequest != nil {
		return f.updatePullRequest(ctx, repo, prNumber, opts)
	}
	return github.PullRequest{}, errors.New("unused")
}

func (fakeGitHubBackend) CreatePullRequest(context.Context, string, github.CreatePullRequestOptions) (github.PullRequest, error) {
	return github.PullRequest{}, errors.New("unused")
}

func (fakeGitHubBackend) ListPullRequestFiles(context.Context, string, int) ([]github.PullRequestFile, error) {
	return nil, errors.New("unused")
}

func (f fakeGitHubBackend) ListPullRequestReviewComments(ctx context.Context, repo string, prNumber int) ([]github.PullRequestReviewComment, error) {
	if f.listPRReviewComments != nil {
		return f.listPRReviewComments(ctx, repo, prNumber)
	}
	return nil, errors.New("unused")
}

func (fakeGitHubBackend) CreatePullRequestReviewComment(context.Context, string, int, string, string, string, int, string) (github.PullRequestReviewComment, error) {
	return github.PullRequestReviewComment{}, errors.New("unused")
}

func (fakeGitHubBackend) ReplyPullRequestReviewComment(context.Context, string, int, int64, string) (github.PullRequestReviewComment, error) {
	return github.PullRequestReviewComment{}, errors.New("unused")
}

func (fakeGitHubBackend) GetCombinedStatus(context.Context, string, string) (github.CombinedStatus, error) {
	return github.CombinedStatus{}, errors.New("unused")
}

func (fakeGitHubBackend) ListCheckRuns(context.Context, string, string) ([]github.CheckRun, error) {
	return nil, errors.New("unused")
}
