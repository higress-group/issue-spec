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
	"github.com/higress-group/issue-spec/internal/commentrunner/jobs"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/workflow"
)

func TestGeneratedExternalCodeWorkflowDoesNotPreGateFirstRunnerDispatch(t *testing.T) {
	root := t.TempDir()
	provider := workflow.ProviderPlan{ProviderKey: "code.example", EvidenceSnapshot: true}
	if err := writeExternalCodeWorkflowConfig(root, provider); err != nil {
		t.Fatal(err)
	}

	config := readTestFile(t, filepath.Join(root, "issue-spec", "config.yaml"))
	if !strings.Contains(config, "- verify") || strings.Contains(config, "- runner") {
		t.Fatalf("generated evidence synchronization policy =\n%s", config)
	}

	// A first /new dispatch has no code-change reference yet. The generated
	// policy must therefore skip the runner pre-gate before it reads evidence
	// credentials or attempts to resolve an external change.
	result, err := (&runnerEvidencePreGate{}).BeforeDispatch(t.Context(), jobs.EvidencePreGateRequest{
		WorkflowRoot:   root,
		CredentialFile: filepath.Join(root, "missing-first-dispatch-credential"),
	})
	if err != nil {
		t.Fatalf("first runner dispatch evidence pre-gate: %v", err)
	}
	if !result.Skipped {
		t.Fatalf("first runner dispatch evidence pre-gate = %+v, want skipped", result)
	}
}

func TestExternalCodeWorkflowConfigDefaultsMissingSyncWithoutOverwritingEvidence(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "issue-spec", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`external_code:
  provider_key: code.example
  evidence:
    required_checks: [unit]
    freshness:
      check: 1h
`), 0o644); err != nil {
		t.Fatal(err)
	}

	provider := workflow.ProviderPlan{ProviderKey: "code.example", EvidenceSnapshot: true}
	if err := writeExternalCodeWorkflowConfig(root, provider); err != nil {
		t.Fatal(err)
	}
	plan, err := workflow.Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	evidence := plan.Config.ExternalCode.Evidence
	if !evidence.SynchronizesBefore("verify") || evidence.SynchronizesBefore("runner") {
		t.Fatalf("generated sync timing = %+v", evidence.SyncBefore)
	}
	if len(evidence.RequiredChecks) != 1 || evidence.RequiredChecks[0] != "unit" || evidence.Freshness["check"] != "1h" {
		t.Fatalf("existing evidence policy was not preserved: %+v", evidence)
	}
}

func TestExternalCodeWorkflowConfigRerunPreservesExplicitRunnerAndEvidencePolicy(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "issue-spec", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`context:
  note: preserved
external_code:
  provider_key: code.example
  evidence:
    required: [review]
    required_checks: [unit, dco]
    freshness:
      review: 24h
      check: 1h
    sync_before: [verify, runner]
`), 0o644); err != nil {
		t.Fatal(err)
	}

	provider := workflow.ProviderPlan{ProviderKey: "code.example", EvidenceSnapshot: true}
	if err := writeExternalCodeWorkflowConfig(root, provider); err != nil {
		t.Fatal(err)
	}
	afterFirstRun := readTestFile(t, path)
	if err := writeExternalCodeWorkflowConfig(root, provider); err != nil {
		t.Fatal(err)
	}
	afterSecondRun := readTestFile(t, path)
	if afterSecondRun != afterFirstRun {
		t.Fatalf("provider workflow config is not idempotent:\nfirst:\n%s\nsecond:\n%s", afterFirstRun, afterSecondRun)
	}

	plan, err := workflow.Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Config.ExternalCode.ProviderKey != "code.example" {
		t.Fatalf("provider key = %q", plan.Config.ExternalCode.ProviderKey)
	}
	evidence := plan.Config.ExternalCode.Evidence
	if !evidence.SynchronizesBefore("verify") || !evidence.SynchronizesBefore("runner") {
		t.Fatalf("explicit sync timing was not preserved: %+v", evidence.SyncBefore)
	}
	if len(evidence.Required) != 1 || evidence.Required[0] != "review" ||
		len(evidence.RequiredChecks) != 2 || evidence.RequiredChecks[0] != "unit" || evidence.RequiredChecks[1] != "dco" ||
		evidence.Freshness["review"] != "24h" || evidence.Freshness["check"] != "1h" {
		t.Fatalf("existing evidence policy was not preserved: %+v", evidence)
	}
	if !strings.Contains(afterSecondRun, "note: preserved") {
		t.Fatalf("existing workflow config was not preserved:\n%s", afterSecondRun)
	}
}

func TestExternalCodeWorkflowConfigRejectsProviderReplacement(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "issue-spec", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("external_code:\n  provider_key: first.example\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := writeExternalCodeWorkflowConfig(root, workflow.ProviderPlan{ProviderKey: "second.example", EvidenceSnapshot: true})
	if err == nil || !strings.Contains(err.Error(), "selects external code provider") {
		t.Fatalf("provider replacement error = %v", err)
	}
	if config := readTestFile(t, path); !strings.Contains(config, "provider_key: first.example") {
		t.Fatalf("conflicting provider config was modified:\n%s", config)
	}
}

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
	for _, test := range []struct {
		name string
		raw  string
	}{
		{name: "https credential", raw: "https://token@git.example.test/acme/widgets.git"},
		{name: "insecure scheme", raw: "http://git.example.test/acme/widgets.git"},
		{name: "URL traversal", raw: "https://git.example.test/acme/../widgets.git"},
		{name: "scp encoded ordinary byte", raw: "git@git.example.test:acme/widget%73.git"},
		{name: "scp encoded slash", raw: "git@git.example.test:acme%2Fwidgets.git"},
		{name: "scp encoded dot traversal", raw: "git@git.example.test:acme/%2e%2e/widgets.git"},
		{name: "scp traversal", raw: "git@git.example.test:acme/../widgets.git"},
		{name: "scp leading slash alias", raw: "git@git.example.test:/acme/widgets.git"},
		{name: "scp trailing slash alias", raw: "git@git.example.test:acme/widgets.git/"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseCanonicalGitRemoteConfig("remote.origin.url " + test.raw + "\n"); err == nil {
				t.Fatalf("expected remote to fail closed: %q", test.raw)
			}
		})
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
	ensureCalls, bindingCalls, labelCalls := 0, 0, 0
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/meta" && r.Header.Get("Authorization") != "Bearer realm-token" {
			http.Error(w, "missing realm credential", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/meta":
			writeTestJSON(w, map[string]any{"api_version": "v1", "server_instance_id": "issue-spec:e2e",
				"api_url": server.URL, "native_api_url": server.URL + "/api/v1", "web_url": server.URL,
				"transport": map[string]any{"mode": "loopback-http", "secure": false},
				"providers": []map[string]any{{"provider_key": "aone", "display_name": "Aone Code",
					"remote_authorities": []string{"gitlab.alibaba-inc.com"}, "code_change_label": "Merge request",
					"capabilities":         []string{"change.comment", "evidence.snapshot"},
					"recommended_evidence": []string{"review", "check"}}}})
		case r.Method == http.MethodGet && r.URL.Path == "/user":
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
		case r.Method == http.MethodPost && r.URL.Path == "/repos/browser-e2e/httpbin/labels":
			labelCalls++
			writeTestJSON(w, map[string]any{"name": "created"})
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
		APIURL: server.URL, NativeAPIURL: server.URL + "/api/v1", WebURL: server.URL,
		ServerInstanceID: "issue-spec:e2e", OperatorRegistryFile: registryPath,
		OnboardingPolicy: auth.OnboardingPolicy{AllowRepositoryCreate: true, AllowSourceBinding: true, AllowUnattended: true}}
	if err := auth.SaveProfile(profile, true); err != nil {
		t.Fatal(err)
	}

	args := []string{"--repo", "local-source/local-checkout", "--server-org", "browser-e2e", "--server-repo", "httpbin",
		"--provider", "aone", "--source-web-url", "https://code.alibaba-inc.com/Ingress/httpbin",
		"--create-labels", "--tools", "codex", "--delivery", "skills", "--json"}
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
		if !strings.Contains(out.String(), `"key": "browser-e2e/httpbin"`) || strings.Contains(out.String(), "local-source/local-checkout") {
			t.Fatalf("plan did not use the resolved server target: %s", out.String())
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
		code := app.runSelfHostedInit(t.Context(), strict, selfHostedInitOptions{Repo: "local-source/local-checkout",
			ServerOrg: "browser-e2e", ServerRepo: "httpbin",
			ProviderKey: "aone", SourceWebURL: "https://code.alibaba-inc.com/Ingress/httpbin",
			CreateLabels: true, Tools: "codex", Delivery: "skills", JSON: true})
		if code == 0 || !strings.Contains(errOut.String(), "requires --yes") {
			t.Fatalf("non-interactive mutation policy exit=%d stderr=%s", code, errOut.String())
		}
		if ensureCalls != 0 || bindingCalls != 0 {
			t.Fatalf("rejected non-interactive run mutated remote state: repository=%d binding=%d", ensureCalls, bindingCalls)
		}
	}
	var finalOutput string
	for run := 1; run <= 2; run++ {
		var out, errOut bytes.Buffer
		app := newApp(strings.NewReader(""), &out, &errOut)
		app.profileName = "e2e"
		if code := app.runInit(t.Context(), args); code != 0 {
			t.Fatalf("run %d exit=%d stderr=%s stdout=%s", run, code, errOut.String(), out.String())
		}
		finalOutput = out.String()
	}
	if ensureCalls != 1 || bindingCalls != 1 {
		t.Fatalf("ensure calls repository=%d binding=%d, want one each", ensureCalls, bindingCalls)
	}
	if labelCalls != 2*len(issueSpecLabels()) {
		t.Fatalf("label calls=%d, want %d on the exact server target", labelCalls, 2*len(issueSpecLabels()))
	}
	if !strings.Contains(finalOutput, `"repo": "browser-e2e/httpbin"`) || strings.Contains(finalOutput, "local-source/local-checkout") {
		t.Fatalf("init output did not use the resolved server target: %s", finalOutput)
	}
	config := readTestFile(t, filepath.Join(root, ".issue-spec", "config.json"))
	for _, want := range []string{`"version": 2`, `"repo": "browser-e2e/httpbin"`, `"profile": "e2e"`, `"key": "aone"`, `"external_repository": "Ingress/httpbin"`} {
		if !strings.Contains(config, want) {
			t.Fatalf("project config missing %q:\n%s", want, config)
		}
	}
	workflowConfig := readTestFile(t, filepath.Join(root, "issue-spec", "config.yaml"))
	for _, want := range []string{"sync_before:", "- verify"} {
		if !strings.Contains(workflowConfig, want) {
			t.Fatalf("workflow config missing %q:\n%s", want, workflowConfig)
		}
	}
	if strings.Contains(workflowConfig, "- runner") {
		t.Fatalf("workflow config must leave runner synchronization opt-in:\n%s", workflowConfig)
	}
	workflowSkill := readTestFile(t, filepath.Join(root, ".agents", "skills", "issue-spec-workflow", "SKILL.md"))
	if !strings.Contains(workflowSkill, "browser-e2e/httpbin") || strings.Contains(workflowSkill, "local-source/local-checkout") {
		t.Fatalf("generated workflow did not use the resolved server target:\n%s", workflowSkill)
	}
	journal := readTestFile(t, filepath.Join(root, ".issue-spec", "init-state.json"))
	for _, want := range []string{`"organization_id": "` + orgID.String() + `"`, `"repository_name": "httpbin"`,
		`"repository_id": "` + repoID.String() + `"`, `"binding": {`, `"state": "complete"`} {
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
