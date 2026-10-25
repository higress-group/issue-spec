package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/github"
)

func setupDefaultTargetProfile(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	clearCommandAuthEnv(t)
	t.Setenv(auth.ProfileEnv, "")
	t.Chdir(t.TempDir()) // Reproduce use outside a project with a GitHub marker.
	requests := &atomic.Int64{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	profile := auth.Profile{Name: "team", Kind: auth.ProfileKindHosted, APIURL: server.URL,
		NativeAPIURL: server.URL + "/api/v1", WebURL: server.URL, ServerInstanceID: "team-instance"}
	if err := auth.SaveProfile(profile, true); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ISSUE_SPEC_TOKEN", "profile-target-secret")
	return server, requests
}

func TestExplicitHostnameConflictRejectsBeforeCredentialsOrRequests(t *testing.T) {
	server, requests := setupDefaultTargetProfile(t, func(http.ResponseWriter, *http.Request) {
		t.Error("conflicting hostname reached the selected server")
	})
	// Resolving credentials would fail first if target validation ran too late.
	t.Setenv(auth.IssueSpecTokenFileEnv, filepath.Join(t.TempDir(), "missing-token"))
	cases := [][]string{
		{"issue", "list", "--repo", "o/r", "--json"},
		{"auth", "status"},
		{"auth", "token", "--plain"},
		{"auth", "logout"},
		{"auth", "login", "--with-token"},
		{"search", "issues", "--repo", "o/r", "--query", "example"},
		{"runner", "preflight", "--repo", "o/r", "--runner", "bot"},
		{"runner", "poll", "--repo", "o/r", "--runner", "bot", "--once", "--dry-run"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args[:2], "_"), func(t *testing.T) {
			var out, errOut bytes.Buffer
			code := Execute(append(args, "--hostname", "github.com"), strings.NewReader("new-secret"), &out, &errOut)
			if code == 0 {
				t.Fatalf("unexpected success: %s", out.String())
			}
			for _, want := range []string{`--hostname "github.com"`, `profile "team"`, "selection_source=profile:config", server.URL, "--profile github"} {
				if !strings.Contains(errOut.String(), want) {
					t.Fatalf("missing %q in %s", want, errOut.String())
				}
			}
			if strings.Contains(out.String()+errOut.String(), "secret") {
				t.Fatal("credential appeared in diagnostics")
			}
		})
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("requests=%d", got)
	}
}

func TestAuthLoginChecksHostnameAgainstConfiguredProfile(t *testing.T) {
	_, requests := setupDefaultTargetProfile(t, func(http.ResponseWriter, *http.Request) { t.Error("login advice made a request") })
	newAPI := "http://new-team.test:8765"
	endpoint, _ := url.Parse(newAPI)
	for _, hostname := range []string{"github.com", endpoint.Host} {
		var out, errOut bytes.Buffer
		args := []string{"auth", "login", "--profile", "new-team", "--kind", "self-hosted", "--api-url", newAPI,
			"--native-api-url", newAPI + "/api/v1", "--web-url", newAPI, "--instance-id", "new-team-instance", "--hostname", hostname, "--json"}
		code := Execute(args, strings.NewReader(""), &out, &errOut)
		if hostname == "github.com" {
			if code == 0 || !strings.Contains(errOut.String(), `profile "new-team"`) || !strings.Contains(errOut.String(), "selection_source=profile:flags") {
				t.Fatalf("conflict code=%d stdout=%s stderr=%s", code, out.String(), errOut.String())
			}
		} else if code != 0 || !strings.Contains(out.String(), `"profile": "new-team"`) {
			t.Fatalf("configured profile code=%d stdout=%s stderr=%s", code, out.String(), errOut.String())
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("requests=%d", requests.Load())
	}
}

func TestImplicitAndMatchingHostnameKeepSavedDefaultProfile(t *testing.T) {
	server, requests := setupDefaultTargetProfile(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user" || r.Header.Get("Authorization") != "Bearer profile-target-secret" {
			t.Errorf("unexpected request path=%s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"login":"team-user"}`))
	})
	endpoint, _ := url.Parse(server.URL)
	for _, hostname := range []string{"", endpoint.Hostname(), endpoint.Host} {
		t.Run(hostname, func(t *testing.T) {
			args := []string{"auth", "status", "--json"}
			if hostname != "" {
				args = append(args, "--hostname", hostname)
			}
			var out, errOut bytes.Buffer
			if code := Execute(args, strings.NewReader(""), &out, &errOut); code != 0 {
				t.Fatalf("code=%d stdout=%s stderr=%s", code, out.String(), errOut.String())
			}
			var result struct {
				OK   bool       `json:"ok"`
				Auth auth.Token `json:"auth"`
			}
			if err := json.Unmarshal(out.Bytes(), &result); err != nil || !result.OK || result.Auth.Profile != "team" || result.Auth.User != "team-user" {
				t.Fatalf("result=%+v decode=%v stdout=%s", result, err, out.String())
			}
		})
	}
	if requests.Load() != 3 {
		t.Fatalf("requests=%d", requests.Load())
	}
}

func TestExplicitGitHubProfileAndProjectMarkerResolveHostnameConflict(t *testing.T) {
	_, requests := setupDefaultTargetProfile(t, func(http.ResponseWriter, *http.Request) {
		t.Error("GitHub remedy contacted self-hosted server")
	})
	for _, mode := range []string{"explicit", "project"} {
		t.Run(mode, func(t *testing.T) {
			var out, errOut bytes.Buffer
			a := newApp(strings.NewReader(""), &out, &errOut)
			wantSource := "builtin"
			if mode == "explicit" {
				a.profileName = "github"
			} else {
				wantSource = "project"
				if err := os.Mkdir(".git", 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(".issue-spec", 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(".issue-spec/config.json", []byte(`{"version":1,"profile":"github","hostname":"github.com"}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			a.selectGitHubBackend = ghSelection
			a.newGitHubBackend = func(_ context.Context, selection auth.GitHubBackendSelection) (github.Backend, error) {
				if selection.Profile.Name != "github" || selection.ProfileSource != wantSource || selection.Host != "github.com" {
					t.Fatalf("selection=%+v", selection)
				}
				return fakeGitHubBackend{user: github.User{Login: "github-user"}}, nil
			}
			if code := a.runAuthStatus(t.Context(), []string{"--hostname", "github.com", "--json"}); code != 0 || !strings.Contains(out.String(), `"ok": true`) {
				t.Fatalf("code=%d stdout=%s stderr=%s", code, out.String(), errOut.String())
			}
		})
	}
	if requests.Load() != 0 {
		t.Fatalf("requests=%d", requests.Load())
	}
}

func TestRepoURLRemainsUnsupportedWithoutRequests(t *testing.T) {
	_, requests := setupDefaultTargetProfile(t, func(http.ResponseWriter, *http.Request) { t.Error("unsupported repo reached server") })
	var out, errOut bytes.Buffer
	code := Execute([]string{"issue", "list", "--repo", "https://github.com/o/r", "--json"}, strings.NewReader(""), &out, &errOut)
	if code == 0 || !strings.Contains(errOut.String(), "repo must be owner/name") || requests.Load() != 0 {
		t.Fatalf("code=%d requests=%d stderr=%s", code, requests.Load(), errOut.String())
	}
}

func TestExplicitHostnameChecksPortAndRejectsInvalidValues(t *testing.T) {
	server, requests := setupDefaultTargetProfile(t, func(http.ResponseWriter, *http.Request) { t.Error("invalid target reached server") })
	endpoint, _ := url.Parse(server.URL)
	for _, hostname := range []string{endpoint.Hostname() + ":1", "", "private-secret@example.test"} {
		var out, errOut bytes.Buffer
		code := Execute([]string{"auth", "status", "--hostname", hostname}, strings.NewReader(""), &out, &errOut)
		if code == 0 || strings.Contains(out.String()+errOut.String(), "private-secret") {
			t.Fatalf("hostname=%q code=%d stdout=%s stderr=%s", hostname, code, out.String(), errOut.String())
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("requests=%d", requests.Load())
	}
}

func TestLocalDurableCheckDoesNotResolveExplicitHostname(t *testing.T) {
	clearCommandAuthEnv(t)
	if err := os.WriteFile(filepath.Join(os.Getenv("ISSUE_SPEC_CONFIG_DIR"), "profiles.json"), []byte("invalid profile config"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, baseline := durableCommandRepository(t)
	durableCommandGit(t, root, "rm", "-q", "issue-spec/config.yaml")
	durableCommandGit(t, root, "commit", "-q", "-m", "disable durable specs")
	subject := durableCommandGitOutput(t, root, "rev-parse", "HEAD")
	var out, errOut bytes.Buffer
	code := Execute([]string{"durable-spec", "check", "--repo", "o/r", "--proposal", "9", "--hostname", "example.test",
		"--baseline", baseline, "--subject", subject, "--root", root, "--json"}, strings.NewReader(""), &out, &errOut)
	if code != 0 || !strings.Contains(out.String(), `"ok": true`) {
		t.Fatalf("local check resolved profile: code=%d stdout=%s stderr=%s", code, out.String(), errOut.String())
	}
}

func TestSelectedProfileRequestErrorIncludesContextAndPreservesAPIError(t *testing.T) {
	_, _ = setupDefaultTargetProfile(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"not found profile-target-secret"}`))
	})
	var out, errOut bytes.Buffer
	code := Execute([]string{"read", "issue", "--repo", "o/r", "--issue", "1"}, strings.NewReader(""), &out, &errOut)
	if code == 0 {
		t.Fatal("unexpected successful read")
	}
	for _, want := range []string{`profile "team"`, "selection_source=profile:config", "HTTP 404"} {
		if !strings.Contains(errOut.String(), want) {
			t.Fatalf("missing %q in %s", want, errOut.String())
		}
	}
	if strings.Contains(errOut.String(), "profile-target-secret") {
		t.Fatal("credential leaked")
	}
	a := newApp(strings.NewReader(""), &out, &errOut)
	backend, _, err := a.clientFor(t.Context(), "github.com")
	if err != nil {
		t.Fatal(err)
	}
	_, err = backend.GetIssue(t.Context(), "o/r", 1)
	var apiErr *github.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusNotFound {
		t.Fatalf("API error classification lost: %v", err)
	}
}
