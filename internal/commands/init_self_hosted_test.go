package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/github"
)

func TestParseCanonicalGitRemoteConfigNormalizesIngressHTTPBin(t *testing.T) {
	remotes, err := parseCanonicalGitRemoteConfig("remote.origin.url git@gitlab.alibaba-inc.com:Ingress/httpbin.git\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(remotes) != 1 {
		t.Fatalf("remotes = %+v", remotes)
	}
	got := remotes[0]
	if got.RemoteName != "origin" || got.Authority != "gitlab.alibaba-inc.com" ||
		got.ExternalRepository != "Ingress/httpbin" ||
		got.CloneURL != "https://gitlab.alibaba-inc.com/Ingress/httpbin.git" ||
		got.WebURL != "https://gitlab.alibaba-inc.com/Ingress/httpbin" {
		t.Fatalf("normalized remote = %+v", got)
	}
}

func TestParseCanonicalGitRemoteConfigRejectsCredentialAndAmbiguousAlias(t *testing.T) {
	for _, raw := range []string{
		"remote.origin.url https://token@git.example.test/acme/widgets.git\n",
		"remote.origin.url http://git.example.test/acme/widgets.git\n",
		"remote.origin.url https://git.example.test/acme/../widgets.git\n",
	} {
		if _, err := parseCanonicalGitRemoteConfig(raw); err == nil {
			t.Fatalf("expected remote to fail closed: %q", raw)
		}
	}
}

func TestInitJournalResumesOnlyExactServerTarget(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".issue-spec", "init-state.json")
	profile := auth.Profile{Name: "e2e", Kind: auth.ProfileKindHosted, APIURL: "https://issues.example.test/api/v3",
		NativeAPIURL: "https://issues.example.test/api/v1", WebURL: "https://issues.example.test",
		ServerInstanceID: "issue-spec:instance"}
	organization := github.NativeOrganizationContext{ID: "33abc76b-8f78-4fec-8bcc-8aec27a82f82", Name: "browser-e2e"}
	journal, err := loadOrCreateInitJournal(path, profile, organization, "httpbin")
	if err != nil {
		t.Fatal(err)
	}
	markJournalStage(&journal, "repository", "complete", "created")
	journal.RepositoryID = "e428c473-f7d2-40ca-896c-9b2bbe102c6f"
	if err := writeInitJournal(path, journal); err != nil {
		t.Fatal(err)
	}
	resumed, err := loadOrCreateInitJournal(path, profile, organization, "httpbin")
	if err != nil || resumed.Stages["repository"].State != "complete" || resumed.Stages["binding"].State != "pending" {
		t.Fatalf("resumed journal = %+v, %v", resumed, err)
	}
	if _, err := loadOrCreateInitJournal(path, profile, organization, "other"); err == nil {
		t.Fatal("journal target mismatch must fail closed")
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("journal mode = %v, %v", info, err)
	}
}

func TestSelfHostedInitEnsuresRepositoryBindingAndResumesIdempotently(t *testing.T) {
	root := t.TempDir()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
	runInitTestGit(t, "init")
	runInitTestGit(t, "remote", "add", "origin", "git@gitlab.alibaba-inc.com:Ingress/httpbin.git")

	orgID := uuid.New()
	repoID := uuid.New()
	bindingID := uuid.New()
	ensureCalls, bindingCalls := 0, 0
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/meta" && r.Header.Get("Authorization") != "Bearer realm-token" {
			http.Error(w, "missing realm credential", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/meta":
			writeTestJSON(w, map[string]any{"api_version": "v1", "server_instance_id": "issue-spec:e2e",
				"api_url": server.URL + "/api/v3", "native_api_url": server.URL + "/api/v1", "web_url": server.URL,
				"transport": map[string]any{"mode": "loopback-http", "secure": false},
				"providers": []map[string]any{{"provider_key": "aone", "display_name": "Aone Code",
					"remote_authorities": []string{"gitlab.alibaba-inc.com"}, "code_change_label": "Merge request",
					"capabilities":         []string{"change.comment", "evidence.snapshot"},
					"recommended_evidence": []string{"review", "check"}}}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v3/user":
			w.Header().Set("X-OAuth-Scopes", "repo, admin:repo, evidence:write")
			writeTestJSON(w, map[string]any{"login": "browser-admin"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/context":
			writeTestJSON(w, map[string]any{"user": map[string]any{"id": uuid.NewString(), "login": "browser-admin"},
				"credential":    map[string]any{"kind": "pat", "scopes": []string{"repo", "admin:repo"}},
				"organizations": []map[string]any{{"id": orgID, "name": "browser-e2e"}}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/context/orgs/"+orgID.String()+"/repos":
			repositories := []any{}
			if ensureCalls > 0 {
				repositories = append(repositories, map[string]any{"repository": map[string]any{
					"id": repoID, "organization_id": orgID, "name": "httpbin"}})
			}
			writeTestJSON(w, map[string]any{"repositories": repositories})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/orgs/"+orgID.String()+"/repos/ensure":
			ensureCalls++
			writeTestJSON(w, map[string]any{"created": true, "repository": map[string]any{
				"id": repoID, "organization_id": orgID, "name": "httpbin", "display_name": "httpbin",
				"visibility": "private", "default_branch": "main", "contribution_policy": "members"}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/orgs/"+orgID.String()+"/repos/"+repoID.String()+"/bindings/active":
			if bindingCalls == 0 {
				http.NotFound(w, r)
				return
			}
			writeTestBinding(w, bindingID)
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/orgs/"+orgID.String()+"/repos/"+repoID.String()+"/bindings/active":
			bindingCalls++
			writeTestJSON(w, map[string]any{"created": true, "binding": testBinding(bindingID)})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	registryPath := writeTestProviderRegistry(t, root)
	configDir := filepath.Join(root, "user-config")
	t.Setenv(auth.ConfigDirEnv, configDir)
	t.Setenv("ISSUE_SPEC_TOKEN", "realm-token")
	t.Setenv("ISSUE_SPEC_CODE_PROVIDERS_FILE", "")
	profile := auth.Profile{Name: "e2e", Kind: auth.ProfileKindHosted, Hostname: "127.0.0.1",
		APIURL: server.URL + "/api/v3", NativeAPIURL: server.URL + "/api/v1", WebURL: server.URL,
		ServerInstanceID: "issue-spec:e2e", OperatorRegistryFile: registryPath,
		OnboardingPolicy: auth.OnboardingPolicy{AllowRepositoryCreate: true, AllowSourceBinding: true, AllowUnattended: true}}
	if err := auth.SaveProfile(profile, true); err != nil {
		t.Fatal(err)
	}

	args := []string{"--repo", "browser-e2e/httpbin", "--provider", "aone", "--source-web-url",
		"https://code.alibaba-inc.com/Ingress/httpbin", "--tools", "none", "--json"}
	{
		var out, errOut bytes.Buffer
		app := newApp(strings.NewReader(""), &out, &errOut)
		app.profileName = "e2e"
		planArgs := append(append([]string(nil), args...), "--plan")
		if code := app.runInit(t.Context(), planArgs); code != 0 {
			t.Fatalf("plan exit=%d stderr=%s stdout=%s", code, errOut.String(), out.String())
		}
		if ensureCalls != 0 || bindingCalls != 0 {
			t.Fatalf("plan mutated remote state: repository=%d binding=%d", ensureCalls, bindingCalls)
		}
		if _, err := os.Stat(filepath.Join(root, ".issue-spec")); !os.IsNotExist(err) {
			t.Fatalf("plan created local state: %v", err)
		}
	}
	{
		strict := profile
		strict.OnboardingPolicy.AllowUnattended = false
		var out, errOut bytes.Buffer
		app := newApp(strings.NewReader(""), &out, &errOut)
		code := app.runSelfHostedInit(t.Context(), strict, selfHostedInitOptions{Repo: "browser-e2e/httpbin",
			ProviderKey: "aone", SourceWebURL: "https://code.alibaba-inc.com/Ingress/httpbin",
			Tools: "none", Delivery: "both", JSON: true})
		if code == 0 || !strings.Contains(errOut.String(), "requires --yes") {
			t.Fatalf("non-interactive mutation policy exit=%d stderr=%s", code, errOut.String())
		}
		if ensureCalls != 0 || bindingCalls != 0 {
			t.Fatalf("rejected non-interactive run mutated remote state: repository=%d binding=%d", ensureCalls, bindingCalls)
		}
	}
	for run := 1; run <= 2; run++ {
		var out, errOut bytes.Buffer
		app := newApp(strings.NewReader(""), &out, &errOut)
		app.profileName = "e2e"
		if code := app.runInit(t.Context(), args); code != 0 {
			t.Fatalf("run %d exit=%d stderr=%s stdout=%s", run, code, errOut.String(), out.String())
		}
	}
	if ensureCalls != 1 || bindingCalls != 1 {
		t.Fatalf("ensure calls repository=%d binding=%d, want one each", ensureCalls, bindingCalls)
	}
	config := readTestFile(t, filepath.Join(root, ".issue-spec", "config.json"))
	for _, want := range []string{`"version": 2`, `"profile": "e2e"`, `"key": "aone"`, `"external_repository": "Ingress/httpbin"`} {
		if !strings.Contains(config, want) {
			t.Fatalf("project config missing %q:\n%s", want, config)
		}
	}
	workflowConfig := readTestFile(t, filepath.Join(root, "issue-spec", "config.yaml"))
	for _, want := range []string{"provider_key: aone", "sync_before:", "- verify", "- runner"} {
		if !strings.Contains(workflowConfig, want) {
			t.Fatalf("workflow config missing %q:\n%s", want, workflowConfig)
		}
	}
	journal := readTestFile(t, filepath.Join(root, ".issue-spec", "init-state.json"))
	for _, want := range []string{`"repository_id": "` + repoID.String() + `"`, `"binding": {`, `"state": "complete"`} {
		if !strings.Contains(journal, want) {
			t.Fatalf("journal missing %q:\n%s", want, journal)
		}
	}
}

func runInitTestGit(t *testing.T, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}

func writeTestJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func testBinding(id uuid.UUID) map[string]any {
	return map[string]any{"id": id, "provider_key": "aone", "external_repository_id": "Ingress/httpbin",
		"clone_url": "https://gitlab.alibaba-inc.com/Ingress/httpbin.git",
		"web_url":   "https://code.alibaba-inc.com/Ingress/httpbin", "default_branch": "main",
		"version": 1, "active": true, "created_at": "2026-07-12T00:00:00Z", "updated_at": "2026-07-12T00:00:00Z"}
}

func writeTestBinding(w http.ResponseWriter, id uuid.UUID) { writeTestJSON(w, testBinding(id)) }

func writeTestProviderRegistry(t *testing.T, root string) string {
	t.Helper()
	bridge := filepath.Join(root, "aone-bridge")
	bridgeBody := `#!/usr/bin/python3
import json, sys
request = json.load(sys.stdin)
response = {"protocol": request["protocol"], "request_id": request["request_id"]}
if request["action"] == "capabilities":
    response["capabilities"] = {"protocol_version": request["protocol"], "values": ["change.comment", "evidence.snapshot"]}
else:
    response["error"] = {"code": "unsupported", "message": "unsupported in init test"}
json.dump(response, sys.stdout)
`
	if err := os.WriteFile(bridge, []byte(bridgeBody), 0o700); err != nil {
		t.Fatal(err)
	}
	registry := filepath.Join(root, "providers.json")
	body := fmt.Sprintf(`{"version":1,"providers":{"aone":{"path":%q,"description":{"provider_key":"aone","display_name":"Aone Code","remote_authorities":["gitlab.alibaba-inc.com"],"code_change_label":"Merge request","capabilities":["change.comment","evidence.snapshot"],"recommended_evidence":["review","check"]}}}}`, bridge)
	if err := os.WriteFile(registry, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return registry
}
