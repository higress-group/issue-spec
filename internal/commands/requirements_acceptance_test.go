package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/requirements"
)

func TestRequirementsAcceptanceSetupTargetsAndSecretBoundary(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	homeDir := filepath.Join(root, "home")
	codexDir := filepath.Join(root, "codex")
	claudeDir := filepath.Join(root, "claude")
	t.Setenv(auth.ConfigDirEnv, configDir)
	t.Setenv("HOME", homeDir)
	t.Setenv("CODEX_HOME", codexDir)
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	browserLog := filepath.Join(root, "browser.log")
	browser := filepath.Join(root, "isolated-browser")
	if err := os.WriteFile(browser, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \""+browserLog+"\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BROWSER", browser)

	secret := "requirements-acceptance-secret-0123456789"
	policy := "public"
	actions := []string{"read", "contribute"}
	server := newRequirementsAcceptanceServer(t, secret, &policy, &actions)
	defer server.Close()
	common := []string{"--server", server.URL, "--repo", "owner/repo", "--profile", "acceptance", "--json"}

	var transcript bytes.Buffer
	preview := newApp(strings.NewReader(secret+"\n"), &transcript, &transcript)
	preview.resolveRequirementsToken = noRequirementsToken
	previewArgs := append(append([]string(nil), common...), "--agent", "codex", "--token-stdin")
	if code := preview.runRequirementsSetup(t.Context(), previewArgs); code != 0 {
		t.Fatalf("preview exit=%d transcript=%q", code, transcript.String())
	}
	assertRequirementsOutputDoesNotContain(t, transcript.String(), secret)
	var previewResult requirementsSetupResult
	if err := json.Unmarshal(transcript.Bytes(), &previewResult); err != nil {
		t.Fatal(err)
	}
	if previewResult.Applied || previewResult.SkillPlan.Target != requirements.TargetCodex || previewResult.SkillPlan.Action != requirements.ActionCreate {
		t.Fatalf("unexpected preview: %+v", previewResult)
	}
	if _, _, err := auth.ResolveProfile("acceptance", ""); err == nil {
		t.Fatal("preview wrote the profile")
	}
	if _, err := os.Stat(previewResult.SkillPlan.Path); !os.IsNotExist(err) {
		t.Fatalf("preview wrote the skill: %v", err)
	}

	transcript.Reset()
	storedSecret := ""
	applyCodex := newApp(strings.NewReader(secret+"\n"), &transcript, &transcript)
	applyCodex.resolveRequirementsToken = noRequirementsToken
	applyCodex.storeRequirementsToken = func(_ context.Context, _ auth.Profile, token string, insecure bool) (string, error) {
		if insecure {
			return "", fmt.Errorf("plaintext storage was requested")
		}
		storedSecret = token
		return "keyring", nil
	}
	applyArgs := append(append([]string(nil), common...), "--agent", "codex", "--token-stdin", "--yes")
	if code := applyCodex.runRequirementsSetup(t.Context(), applyArgs); code != 0 {
		t.Fatalf("codex apply exit=%d transcript=%q", code, transcript.String())
	}
	assertRequirementsOutputDoesNotContain(t, transcript.String(), secret)
	if storedSecret != secret {
		t.Fatalf("keyring received %q", storedSecret)
	}

	transcript.Reset()
	applyClaude := newApp(strings.NewReader(""), &transcript, &transcript)
	applyClaude.resolveRequirementsToken = func(context.Context, auth.Profile) (auth.Token, error) {
		return auth.Token{Value: secret, Source: "keyring"}, nil
	}
	applyClaude.storeRequirementsToken = func(context.Context, auth.Profile, string, bool) (string, error) {
		t.Fatal("second target attempted to store the existing PAT")
		return "", nil
	}
	claudeArgs := append(append([]string(nil), common...), "--agent", "claude", "--yes")
	if code := applyClaude.runRequirementsSetup(t.Context(), claudeArgs); code != 0 {
		t.Fatalf("claude apply exit=%d transcript=%q", code, transcript.String())
	}
	assertRequirementsOutputDoesNotContain(t, transcript.String(), secret)

	for _, target := range []string{
		filepath.Join(codexDir, "skills", requirements.SkillName, "SKILL.md"),
		filepath.Join(claudeDir, "skills", requirements.SkillName, "SKILL.md"),
	} {
		if info, err := os.Stat(target); err != nil || !info.Mode().IsRegular() {
			t.Fatalf("skill target %s: info=%v err=%v", target, info, err)
		}
	}
	configured, err := requirements.LoadActiveContext()
	if err != nil || configured.Agent != requirements.TargetClaude || configured.Repository != "owner/repo" {
		t.Fatalf("active context=%+v err=%v", configured, err)
	}
	assertRequirementsTreeDoesNotContain(t, root, secret)
	if _, err := os.Stat(browserLog); !os.IsNotExist(err) {
		t.Fatalf("requirements setup invoked a browser opener: %v", err)
	}
}

func TestRequirementsAcceptancePolicyAndSkillBoundary(t *testing.T) {
	root := t.TempDir()
	t.Setenv(auth.ConfigDirEnv, filepath.Join(root, "config"))
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("CODEX_HOME", filepath.Join(root, "codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "claude"))
	secret := "requirements-policy-secret-0123456789"
	policy := "public"
	actions := []string{"read", "contribute"}
	server := newRequirementsAcceptanceServer(t, secret, &policy, &actions)
	defer server.Close()

	var output bytes.Buffer
	setup := newApp(strings.NewReader(secret+"\n"), &output, &output)
	setup.resolveRequirementsToken = noRequirementsToken
	setup.storeRequirementsToken = func(context.Context, auth.Profile, string, bool) (string, error) { return "keyring", nil }
	if code := setup.runRequirementsSetup(t.Context(), []string{"--server", server.URL, "--repo", "owner/repo", "--profile", "policy", "--agent", "codex", "--token-stdin", "--yes", "--json"}); code != 0 {
		t.Fatalf("setup exit=%d output=%q", code, output.String())
	}
	assertRequirementsOutputDoesNotContain(t, output.String(), secret)

	status := newApp(strings.NewReader(""), &output, &output)
	status.resolveRequirementsToken = func(context.Context, auth.Profile) (auth.Token, error) {
		return auth.Token{Value: secret, Source: "keyring"}, nil
	}
	checks := []struct {
		policy     string
		actions    []string
		contribute bool
	}{
		{policy: "public", actions: []string{"read", "contribute"}, contribute: true},
		{policy: "members", actions: []string{"read"}, contribute: false},
		{policy: "disabled", actions: []string{"read"}, contribute: false},
	}
	for _, check := range checks {
		policy = check.policy
		actions = append([]string(nil), check.actions...)
		output.Reset()
		if code := status.runRequirementsStatus(t.Context(), []string{"--json"}); code != 0 {
			t.Fatalf("status policy=%s exit=%d output=%q", check.policy, code, output.String())
		}
		assertRequirementsOutputDoesNotContain(t, output.String(), secret)
		var result requirementsStatusResult
		if err := json.Unmarshal(output.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		expectedActions := append([]string(nil), check.actions...)
		slices.Sort(expectedActions)
		if result.ContributionPolicy != check.policy || result.CanContribute != check.contribute || result.ReadOnly == check.contribute || !slices.Equal(result.AllowedActions, expectedActions) {
			t.Fatalf("policy=%s status=%+v", check.policy, result)
		}
		if result.CanContribute != slices.Contains(result.AllowedActions, "contribute") {
			t.Fatalf("policy=%s inferred contribution outside allowed_actions: %+v", check.policy, result)
		}
	}

	skillPath := filepath.Join(root, "codex", "skills", requirements.SkillName, "SKILL.md")
	skill, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, contract := range []string{
		"Simple requirement issue", "Standard Proposal", "exact current remote-write plan",
		"explicit confirmation", "return every browser `url`", "Stop before design",
		"allowed_actions` contains the existing\n`contribute` action", "invent a requirement-specific role or granular action",
	} {
		if !strings.Contains(string(skill), contract) {
			t.Errorf("installed skill is missing boundary %q", contract)
		}
	}
	assertRequirementsTreeDoesNotContain(t, root, secret)
}

func newRequirementsAcceptanceServer(t *testing.T, secret string, policy *string, actions *[]string) *httptest.Server {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/meta" {
			if r.Header.Get("Authorization") != "" {
				t.Error("metadata discovery included authorization")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"api_version": "v1", "server_instance_id": "issue-spec:acceptance", "api_url": server.URL,
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
				"credential":    map[string]any{"kind": "pat", "scopes": []string{"read:user", "issues:write"}, "repository_restricted": false},
				"organizations": []map[string]any{{"id": "org-id", "name": "owner"}},
			})
		case "/api/v1/context/orgs/org-id/repos":
			_ = json.NewEncoder(w).Encode(map[string]any{"repositories": []map[string]any{{
				"repository":           map[string]any{"id": "repo-id", "organization_id": "org-id", "name": "repo", "visibility": "public", "contribution_policy": *policy},
				"effective_permission": "read", "allowed_actions": append([]string(nil), (*actions)...),
			}}})
		default:
			http.NotFound(w, r)
		}
	}))
	return server
}

func assertRequirementsTreeDoesNotContain(t *testing.T, root, secret string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || entry.Name() == "isolated-browser" {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(content, []byte(secret)) {
			return fmt.Errorf("secret persisted in %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func assertRequirementsOutputDoesNotContain(t *testing.T, output, secret string) {
	t.Helper()
	if strings.Contains(output, secret) {
		t.Fatal("requirements terminal output exposed the PAT")
	}
}
