package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/requirements"
)

func TestRequirementsSetupRecoversFromKeyringFailureAndRerunsIdempotently(t *testing.T) {
	configDir, home := t.TempDir(), t.TempDir()
	t.Setenv(auth.ConfigDirEnv, configDir)
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	secret := "requirements-super-secret"
	actions := []string{"issue.read"}
	server := newRequirementsTestServer(t, "issue-spec:realm-a", secret, &actions)
	defer server.Close()

	var firstOut, firstErr bytes.Buffer
	first := newApp(strings.NewReader(secret+"\n"), &firstOut, &firstErr)
	first.resolveRequirementsToken = noRequirementsToken
	first.storeRequirementsToken = func(_ context.Context, _ auth.Profile, token string, insecure bool) (string, error) {
		if token != secret || insecure {
			t.Fatalf("store token=%q insecure=%t", token, insecure)
		}
		return "", fmt.Errorf("keyring unavailable while storing %s; rerun with --insecure-storage", token)
	}
	args := []string{"--server", server.URL, "--repo", "owner/repo", "--agent", "codex", "--profile", "team", "--token-stdin", "--yes", "--json"}
	if code := first.runRequirementsSetup(t.Context(), args); code != 1 {
		t.Fatalf("first exit=%d stdout=%q stderr=%q", code, firstOut.String(), firstErr.String())
	}
	combined := firstOut.String() + firstErr.String()
	if strings.Contains(combined, secret) || strings.Contains(combined, "--insecure-storage") || !strings.Contains(combined, "no plaintext credential was written") {
		t.Fatalf("unsafe keyring diagnostic stdout=%q stderr=%q", firstOut.String(), firstErr.String())
	}
	if _, err := requirements.LoadActiveContext(); !errors.Is(err, requirements.ErrContextNotConfigured) {
		t.Fatalf("context written before keyring success: %v", err)
	}
	profile, source, err := auth.ResolveProfile("team", "")
	if err != nil || source != "config" || profile.ServerInstanceID != "issue-spec:realm-a" {
		t.Fatalf("recoverable saved profile=%+v source=%q err=%v", profile, source, err)
	}

	storeCalls := 0
	var recoveredOut, recoveredErr bytes.Buffer
	recovered := newApp(strings.NewReader(secret+"\n"), &recoveredOut, &recoveredErr)
	recovered.resolveRequirementsToken = noRequirementsToken
	recovered.storeRequirementsToken = func(_ context.Context, _ auth.Profile, token string, insecure bool) (string, error) {
		storeCalls++
		if token != secret || insecure {
			t.Fatalf("recovery store token=%q insecure=%t", token, insecure)
		}
		return "keyring", nil
	}
	if code := recovered.runRequirementsSetup(t.Context(), args); code != 0 {
		t.Fatalf("recovery exit=%d stdout=%q stderr=%q", code, recoveredOut.String(), recoveredErr.String())
	}
	var recoveredResult requirementsSetupResult
	if err := json.Unmarshal(recoveredOut.Bytes(), &recoveredResult); err != nil {
		t.Fatal(err)
	}
	if recoveredResult.ProfileCreated || !recoveredResult.TokenStored || !recoveredResult.ContextChanged ||
		recoveredResult.SkillResult == nil || !recoveredResult.SkillResult.Changed || storeCalls != 1 {
		t.Fatalf("recovery result=%+v storeCalls=%d", recoveredResult, storeCalls)
	}
	if context, err := requirements.LoadActiveContext(); err != nil || context.Repository != "owner/repo" || context.Agent != requirements.TargetCodex {
		t.Fatalf("context=%+v err=%v", context, err)
	}

	var rerunOut, rerunErr bytes.Buffer
	rerun := newApp(strings.NewReader(""), &rerunOut, &rerunErr)
	rerun.resolveRequirementsToken = func(context.Context, auth.Profile) (auth.Token, error) {
		return auth.Token{Value: secret, Source: "keyring"}, nil
	}
	rerun.storeRequirementsToken = func(context.Context, auth.Profile, string, bool) (string, error) {
		t.Fatal("idempotent rerun attempted to store an existing token")
		return "", nil
	}
	if code := rerun.runRequirementsSetup(t.Context(), []string{"--server", server.URL, "--repo", "owner/repo", "--agent", "codex", "--profile", "team", "--yes", "--json"}); code != 0 {
		t.Fatalf("rerun exit=%d stdout=%q stderr=%q", code, rerunOut.String(), rerunErr.String())
	}
	var rerunResult requirementsSetupResult
	if err := json.Unmarshal(rerunOut.Bytes(), &rerunResult); err != nil {
		t.Fatal(err)
	}
	if rerunResult.ProfileCreated || rerunResult.TokenStored || rerunResult.ContextChanged ||
		rerunResult.SkillResult == nil || rerunResult.SkillResult.Changed || rerunResult.SkillPlan.Action != requirements.ActionNoop {
		t.Fatalf("non-idempotent rerun=%+v", rerunResult)
	}

	var statusOut, statusErr bytes.Buffer
	status := newApp(strings.NewReader(""), &statusOut, &statusErr)
	status.resolveRequirementsToken = rerun.resolveRequirementsToken
	if code := status.runRequirementsStatus(t.Context(), []string{"--json"}); code != 0 {
		t.Fatalf("status exit=%d stdout=%q stderr=%q", code, statusOut.String(), statusErr.String())
	}
	var readOnly requirementsStatusResult
	if err := json.Unmarshal(statusOut.Bytes(), &readOnly); err != nil {
		t.Fatal(err)
	}
	if !readOnly.ReadOnly || readOnly.CanContribute || len(readOnly.AllowedActions) != 1 || readOnly.AllowedActions[0] != "issue.read" ||
		readOnly.Visibility != "public" || readOnly.ContributionPolicy != "public" || readOnly.EffectivePermission != "read" {
		t.Fatalf("read-only status=%+v", readOnly)
	}
	actions = []string{"issue.create", "contribute"}
	statusOut.Reset()
	statusErr.Reset()
	if code := status.runRequirementsStatus(t.Context(), []string{"--json"}); code != 0 {
		t.Fatalf("writable status exit=%d stderr=%q", code, statusErr.String())
	}
	var writable requirementsStatusResult
	if err := json.Unmarshal(statusOut.Bytes(), &writable); err != nil {
		t.Fatal(err)
	}
	if writable.ReadOnly || !writable.CanContribute {
		t.Fatalf("writable status=%+v", writable)
	}
	if strings.Contains(statusOut.String()+statusErr.String(), secret) {
		t.Fatal("status exposed the PAT")
	}
}

func TestRequirementsSetupRejectsSavedProfileRealmMismatchBeforePAT(t *testing.T) {
	t.Setenv(auth.ConfigDirEnv, t.TempDir())
	t.Setenv("HOME", t.TempDir())
	server := newRequirementsTestServer(t, "issue-spec:realm-b", "unused", new([]string))
	defer server.Close()
	profile := auth.Profile{Name: "team", Kind: auth.ProfileKindHosted, Hostname: "127.0.0.1", APIURL: server.URL,
		NativeAPIURL: server.URL + "/api/v1", WebURL: server.URL, ServerInstanceID: "issue-spec:realm-a"}
	if err := auth.SaveProfile(profile, false); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader("must-not-be-read"), &out, &errOut)
	secretRead := false
	app.readRequirementsSecret = func(io.Reader, io.Writer) (string, error) {
		secretRead = true
		return "", nil
	}
	code := app.runRequirementsSetup(t.Context(), []string{"--server", server.URL, "--repo", "owner/repo", "--agent", "codex", "--profile", "team", "--yes"})
	if code != 1 || secretRead || !strings.Contains(errOut.String(), "realm mismatch") {
		t.Fatalf("exit=%d secretRead=%t stdout=%q stderr=%q", code, secretRead, out.String(), errOut.String())
	}
	loaded, _, err := auth.ResolveProfile("team", "")
	if err != nil || loaded.ServerInstanceID != "issue-spec:realm-a" {
		t.Fatalf("saved realm was overwritten: profile=%+v err=%v", loaded, err)
	}
}

func TestRequirementsSetupPreviewDoesNotWrite(t *testing.T) {
	t.Setenv(auth.ConfigDirEnv, t.TempDir())
	t.Setenv("HOME", t.TempDir())
	secret := "preview-only-secret"
	actions := []string{"contribute"}
	server := newRequirementsTestServer(t, "issue-spec:preview", secret, &actions)
	defer server.Close()
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(secret+"\n"), &out, &errOut)
	app.resolveRequirementsToken = noRequirementsToken
	code := app.runRequirementsSetup(t.Context(), []string{"--server", server.URL, "--repo", "owner/repo", "--agent", "claude", "--profile", "preview", "--token-stdin", "--json"})
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	var result requirementsSetupResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Applied || !result.ProfileCreated || result.SkillPlan.Action != requirements.ActionCreate {
		t.Fatalf("preview=%+v", result)
	}
	if _, _, err := auth.ResolveProfile("preview", ""); err == nil {
		t.Fatal("preview persisted a profile")
	}
	if _, err := requirements.LoadActiveContext(); !errors.Is(err, requirements.ErrContextNotConfigured) {
		t.Fatalf("preview persisted context: %v", err)
	}
	if _, err := os.Stat(result.SkillPlan.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preview installed skill: %v", err)
	}
	if strings.Contains(out.String()+errOut.String(), secret) {
		t.Fatal("preview exposed the PAT")
	}
}

func noRequirementsToken(context.Context, auth.Profile) (auth.Token, error) {
	return auth.Token{}, auth.ErrNoToken
}

func newRequirementsTestServer(t *testing.T, realm, secret string, actions *[]string) *httptest.Server {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/meta" {
			if authorization := r.Header.Get("Authorization"); authorization != "" {
				t.Errorf("metadata discovery sent authorization %q", authorization)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"api_version": "v1", "server_instance_id": realm, "api_url": server.URL,
				"native_api_url": server.URL + "/api/v1", "web_url": server.URL,
				"transport": map[string]any{"mode": "loopback-http", "secure": false},
				"features":  map[string]any{"requirements_onboarding": true, "search": true},
			})
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+secret {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/user":
			w.Header().Set("X-OAuth-Scopes", "read:user, issues:write")
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 7, "login": "external-user"})
		case "/api/v1/context":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"user":          map[string]any{"id": "user-id", "login": "external-user"},
				"credential":    map[string]any{"kind": "pat", "scopes": []string{"read:user", "issues:write"}, "repository_restricted": false, "repository_count": 1},
				"organizations": []map[string]any{{"id": "org-id", "name": "owner"}},
			})
		case "/api/v1/context/orgs/org-id/repos":
			_ = json.NewEncoder(w).Encode(map[string]any{"repositories": []map[string]any{{
				"repository":           map[string]any{"id": "repo-id", "organization_id": "org-id", "name": "repo", "visibility": "public", "contribution_policy": "public"},
				"effective_permission": "read", "allowed_actions": append([]string(nil), (*actions)...),
			}}})
		default:
			http.NotFound(w, r)
		}
	}))
	return server
}

func TestRequirementsContextFileDoesNotContainPAT(t *testing.T) {
	t.Setenv(auth.ConfigDirEnv, t.TempDir())
	context := requirements.ActiveContext{Profile: "team", ServerInstanceID: "issue-spec:test", Repository: "owner/repo", Agent: requirements.TargetCodex}
	if _, err := requirements.SaveActiveContext(context); err != nil {
		t.Fatal(err)
	}
	path, _ := requirements.ContextPath()
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "token") || strings.Contains(string(raw), "secret") {
		t.Fatalf("context contains secret-shaped fields: %s", raw)
	}
}
