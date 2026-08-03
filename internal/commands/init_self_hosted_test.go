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
	"github.com/higress-group/issue-spec/internal/codereview"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/workflow"
)

func minimalProviderPlanForTest(key string) workflow.ProviderPlan {
	return workflow.ProviderPlan{ProviderKey: key, DisplayName: "Example Code", CodeChangeLabel: "change",
		SemanticGeneration: codereview.MergeAuthorityGeneration, ProviderBuildIdentity: "bridge@sha256:0123456789abcdef",
		Capabilities: codereview.RequiredMergeAuthorityCapabilities(), ReviewDecision: true,
		AuthoritativeCheckConclusion: true, MergeConditional: true}
}

func TestGeneratedExternalCodeWorkflowDoesNotPreGateFirstRunnerDispatch(t *testing.T) {
	root := t.TempDir()
	provider := minimalProviderPlanForTest("code.example")
	if err := writeExternalCodeWorkflowConfig(root, provider); err != nil {
		t.Fatal(err)
	}

	config := readTestFile(t, filepath.Join(root, "issue-spec", "config.yaml"))
	if !strings.Contains(config, "provider_key: code.example") || strings.Contains(config, "evidence:") {
		t.Fatalf("generated provider-bound policy =\n%s", config)
	}

}

func TestExternalCodeWorkflowConfigRejectsLegacyEvidenceWithoutOverwrite(t *testing.T) {
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

	before := readTestFile(t, path)
	err := writeExternalCodeWorkflowConfig(root, minimalProviderPlanForTest("code.example"))
	if err == nil || !strings.Contains(err.Error(), "deprecated") {
		t.Fatalf("legacy evidence error = %v", err)
	}
	if after := readTestFile(t, path); after != before {
		t.Fatalf("legacy evidence config changed on failure:\n%s", after)
	}
}

func TestExternalCodeWorkflowConfigRerunPreservesMergePolicy(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "issue-spec", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`context:
  note: preserved
external_code:
  provider_key: code.example
  merge:
    required_checks:
      - source: provider
        provider: code.example
        key: app:7/context:unit
        owner: app:7
`), 0o644); err != nil {
		t.Fatal(err)
	}

	provider := minimalProviderPlanForTest("code.example")
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
	providerKey, checks, err := plan.MergeAuthorityConfiguration()
	if err != nil || providerKey != "code.example" || len(checks) != 1 || checks[0].Key != "app:7/context:unit" {
		t.Fatalf("existing merge policy was not preserved: provider=%q checks=%+v err=%v", providerKey, checks, err)
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

	err := writeExternalCodeWorkflowConfig(root, minimalProviderPlanForTest("second.example"))
	if err == nil || !strings.Contains(err.Error(), "selects external code provider") {
		t.Fatalf("provider replacement error = %v", err)
	}
	if config := readTestFile(t, path); !strings.Contains(config, "provider_key: first.example") {
		t.Fatalf("conflicting provider config was modified:\n%s", config)
	}
}

func TestParseCanonicalGitRemoteConfigNormalizesExampleRepository(t *testing.T) {
	remotes, err := parseCanonicalGitRemoteConfig("remote.origin.url git@git.example.test:acme/widgets.git\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(remotes) != 1 {
		t.Fatalf("remotes = %+v", remotes)
	}
	got := remotes[0]
	if got.RemoteName != "origin" || got.Authority != "git.example.test" ||
		got.ExternalRepository != "acme/widgets" ||
		got.CloneURL != "https://git.example.test/acme/widgets.git" ||
		got.WebURL != "https://git.example.test/acme/widgets" {
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

func TestProjectServerHostnameUsesHandshakeWebIdentity(t *testing.T) {
	if got := projectServerHostname("http://11.164.3.16:18080", "127.0.0.1"); got != "11.164.3.16" {
		t.Fatalf("project server hostname = %q", got)
	}
	if got := projectServerHostname("not a URL", "127.0.0.1"); got != "127.0.0.1" {
		t.Fatalf("fallback project server hostname = %q", got)
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

func TestSelfHostedInitKeepsLegacyAndPartialProvidersPlanningOnly(t *testing.T) {
	for _, test := range []struct {
		name, providerKey, displayName, generation, build string
		capabilities                                      []string
	}{
		{name: "legacy Aone", providerKey: "aone", displayName: "Aone Legacy",
			capabilities: []string{"evidence.snapshot"}},
		{name: "partial merge authority", providerKey: "code.partial", displayName: "Partial Code",
			generation: codereview.MergeAuthorityGeneration, build: "partial@sha256:0123456789abcdef",
			capabilities: []string{"evidence.review-decision"}},
	} {
		t.Run(test.name, func(t *testing.T) {
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
			runInitTestGit(t, "remote", "add", "origin", "git@git.example.test:acme/widgets.git")

			orgID := uuid.New()
			repoID := uuid.New()
			bindingID := uuid.New()
			mutationCalls := 0
			description := map[string]any{"provider_key": test.providerKey, "display_name": test.displayName,
				"remote_authorities": []string{"git.example.test"}, "code_change_label": "Merge request",
				"capabilities": test.capabilities}
			if test.generation != "" {
				description["semantic_generation"] = test.generation
				description["provider_build_identity"] = test.build
			}
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet && r.Method != http.MethodPut {
					mutationCalls++
					http.Error(w, "unexpected mutation", http.StatusInternalServerError)
					return
				}
				switch r.URL.Path {
				case "/api/v1/meta":
					writeTestJSON(w, map[string]any{"api_version": "v1", "server_instance_id": "issue-spec:preflight",
						"api_url": server.URL, "native_api_url": server.URL + "/api/v1", "web_url": server.URL,
						"transport": map[string]any{"mode": "loopback-http", "secure": false},
						"providers": []any{description}})
				case "/user":
					w.Header().Set("X-OAuth-Scopes", "repo, admin:repo")
					writeTestJSON(w, map[string]any{"login": "operator"})
				case "/api/v1/context":
					writeTestJSON(w, map[string]any{"user": map[string]any{"id": uuid.NewString(), "login": "operator"},
						"credential":    map[string]any{"kind": "pat", "scopes": []string{"repo", "admin:repo"}},
						"organizations": []map[string]any{{"id": orgID, "name": "acme"}}})
				case "/api/v1/context/orgs/" + orgID.String() + "/repos":
					writeTestJSON(w, map[string]any{
						"repositories": []any{
							map[string]any{"repository": map[string]any{
								"id": repoID, "organization_id": orgID, "name": "widgets-spec",
							}},
						},
					})
				case "/api/v1/orgs/" + orgID.String() + "/repos/" + repoID.String() + "/bindings/active":
					if r.Method == http.MethodGet {
						http.NotFound(w, r)
						return
					}
					mutationCalls++
					binding := testBinding(bindingID)
					binding["provider_key"] = test.providerKey
					writeTestJSON(w, map[string]any{"created": true, "binding": binding})
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			registryPath := writeProviderRegistryFixture(t, root, test.providerKey, description,
				map[string]any{"protocol_version": codereview.ProtocolVersion, "semantic_generation": test.generation,
					"provider_build_identity": test.build, "values": test.capabilities})
			t.Setenv("ISSUE_SPEC_TOKEN", "realm-token")
			t.Setenv("ISSUE_SPEC_CODE_PROVIDERS_FILE", "")
			profile := auth.Profile{Name: "preflight", Kind: auth.ProfileKindHosted, Hostname: "127.0.0.1",
				APIURL: server.URL, NativeAPIURL: server.URL + "/api/v1", WebURL: server.URL,
				ServerInstanceID: "issue-spec:preflight", OperatorRegistryFile: registryPath,
				OnboardingPolicy: auth.OnboardingPolicy{AllowRepositoryCreate: true, AllowSourceBinding: true, AllowUnattended: true}}
			var out, errOut bytes.Buffer
			app := newApp(strings.NewReader(""), &out, &errOut)
			code := app.runSelfHostedInit(t.Context(), profile, selfHostedInitOptions{Repo: "local/source",
				ServerOrg: "acme", ServerRepo: "widgets-spec", ProviderKey: test.providerKey,
				SourceWebURL: "https://git.example.test/acme/widgets",
				Tools:        "codex", Delivery: "skills", Yes: true, JSON: true})
			if code != 0 {
				t.Fatalf("init exit=%d stderr=%s stdout=%s", code, errOut.String(), out.String())
			}
			if mutationCalls != 1 {
				t.Fatalf("planning-only init mutations=%d, want one Source Binding write", mutationCalls)
			}
			var result struct {
				WorkflowReadiness selfHostedWorkflowReadiness `json:"workflow_readiness"`
			}
			if err := json.Unmarshal(out.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if !result.WorkflowReadiness.PlanningOnly || result.WorkflowReadiness.MergeCapable ||
				!result.WorkflowReadiness.ProviderFresh || result.WorkflowReadiness.ProviderKey != test.providerKey {
				t.Fatalf("workflow readiness = %+v", result.WorkflowReadiness)
			}
			projectConfig := readTestFile(t, filepath.Join(root, ".issue-spec", "config.json"))
			for _, want := range []string{`"mode": "planning-only"`, `"planning_only": true`, `"merge_capable": false`} {
				if !strings.Contains(projectConfig, want) {
					t.Fatalf("planning-only project config missing %q:\n%s", want, projectConfig)
				}
			}
			workflowConfig := readTestFile(t, filepath.Join(root, "issue-spec", "config.yaml"))
			if !strings.Contains(workflowConfig, "provider_key: "+test.providerKey) || strings.Contains(workflowConfig, "evidence:") {
				t.Fatalf("planning-only workflow config =\n%s", workflowConfig)
			}
			if _, err := os.Stat(filepath.Join(root, ".agents", "skills", "issue-spec-code-provider", "SKILL.md")); !os.IsNotExist(err) {
				t.Fatalf("planning-only init generated provider authority skill: %v", err)
			}
		})
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
	runInitTestGit(t, "remote", "add", "origin", "git@git.example.test:acme/widgets.git")

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
				"providers": []map[string]any{{"provider_key": "code.example", "display_name": "Example Code",
					"remote_authorities": []string{"git.example.test"}, "code_change_label": "Merge request",
					"semantic_generation": "minimal-merge-authority/v1", "provider_build_identity": "code-example@sha256:0123456789abcdef",
					"capabilities": []string{"change.comment", "evidence.review-decision",
						"evidence.authoritative-check-conclusion", "change.merge-conditional"}}}})
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
		"--provider", "code.example", "--source-web-url", "https://git.example.test/acme/widgets",
		"--tools", "codex", "--delivery", "skills", "--json"}
	previewDir := filepath.Join(root, "isolated-global-prompts")
	planArgs := []string{"--repo", "local-source/local-checkout", "--server-org", "browser-e2e", "--server-repo", "httpbin",
		"--provider", "code.example", "--source-web-url", "https://git.example.test/acme/widgets",
		"--tools", "codex", "--delivery", "both", "--plan",
		"--global-prompts-dir", previewDir, "--global-prompts-dry-run"}
	for _, test := range []struct {
		name       string
		jsonOutput bool
		marker     string
	}{
		{name: "text", marker: "user-global prompt dry-run:"},
		{name: "json", jsonOutput: true, marker: `"global_prompt_files"`},
	} {
		t.Run("plan global prompt preview "+test.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			app := newApp(strings.NewReader(""), &out, &errOut)
			app.profileName = "e2e"
			currentArgs := append([]string(nil), planArgs...)
			if test.jsonOutput {
				currentArgs = append(currentArgs, "--json")
			}
			if code := app.runInit(t.Context(), currentArgs); code != 0 {
				t.Fatalf("plan exit=%d stderr=%s stdout=%s", code, errOut.String(), out.String())
			}
			if ensureCalls != 0 || bindingCalls != 0 || labelCalls != 0 {
				t.Fatalf("plan mutated remote state: repository=%d binding=%d labels=%d", ensureCalls, bindingCalls, labelCalls)
			}
			if !strings.Contains(out.String(), test.marker) || strings.Contains(out.String(), "local-source/local-checkout") {
				t.Fatalf("plan did not report the resolved global prompt preview: %s", out.String())
			}
			for _, command := range []string{"propose", "apply"} {
				path := filepath.Join(previewDir, "issue-spec-"+command+".md")
				if !strings.Contains(out.String(), path) {
					t.Fatalf("plan output missing absolute global prompt path %q: %s", path, out.String())
				}
			}
			if path := filepath.Join(previewDir, "issue-spec-archive.md"); strings.Contains(out.String(), path) {
				t.Fatalf("plan output unexpectedly includes removed Archive prompt path %q: %s", path, out.String())
			}
			for _, path := range []string{
				filepath.Join(root, ".issue-spec"), filepath.Join(root, "issue-spec"),
				filepath.Join(root, ".agents"), filepath.Join(root, ".claude"), previewDir,
			} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("plan created local artifact %q: %v", path, err)
				}
			}
		})
	}
	{
		var out, errOut bytes.Buffer
		app := newApp(strings.NewReader(""), &out, &errOut)
		app.profileName = "e2e"
		neutralPlanArgs := []string{"--repo", "local-source/local-checkout", "--server-org", "browser-e2e", "--server-repo", "httpbin",
			"--provider", "code.example", "--source-web-url", "https://git.example.test/acme/widgets",
			"--tools", "nOnE", "--delivery", "not-used", "--language", "zh", "--skip-labels", "--plan", "--json"}
		if code := app.runInit(t.Context(), neutralPlanArgs); code != 0 {
			t.Fatalf("tools-none plan exit=%d stderr=%s stdout=%s", code, errOut.String(), out.String())
		}
		for _, want := range []string{`"language": "Simplified Chinese (简体中文)"`, `"language_applied": false`, "openspec/config.yaml"} {
			if !strings.Contains(out.String(), want) {
				t.Fatalf("tools-none plan output missing %q: %s", want, out.String())
			}
		}
		if ensureCalls != 0 || bindingCalls != 0 || labelCalls != 0 {
			t.Fatalf("tools-none plan mutated remote state: repository=%d binding=%d labels=%d", ensureCalls, bindingCalls, labelCalls)
		}
		for _, path := range []string{filepath.Join(root, ".issue-spec"), filepath.Join(root, "issue-spec"), filepath.Join(root, ".agents")} {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("tools-none plan created local artifact %q: %v", path, err)
			}
		}
	}
	{
		strict := profile
		strict.OnboardingPolicy.AllowUnattended = false
		var out, errOut bytes.Buffer
		app := newApp(strings.NewReader(""), &out, &errOut)
		code := app.runSelfHostedInit(t.Context(), strict, selfHostedInitOptions{Repo: "local-source/local-checkout",
			ServerOrg: "browser-e2e", ServerRepo: "httpbin",
			ProviderKey: "code.example", SourceWebURL: "https://git.example.test/acme/widgets",
			CreateLabels: true, Tools: "codex", Delivery: "skills", JSON: true})
		if code == 0 || !strings.Contains(errOut.String(), "requires --yes") {
			t.Fatalf("non-interactive mutation policy exit=%d stderr=%s", code, errOut.String())
		}
		if ensureCalls != 0 || bindingCalls != 0 {
			t.Fatalf("rejected non-interactive run mutated remote state: repository=%d binding=%d", ensureCalls, bindingCalls)
		}
	}
	openspecPath := filepath.Join(root, "openspec", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(openspecPath), 0o755); err != nil {
		t.Fatal(err)
	}
	openspecConfig := "schema: legacy-workflow\nrules:\n  language: Existing OpenSpec Language\n"
	if err := os.WriteFile(openspecPath, []byte(openspecConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	legacySchemaPath := filepath.Join(root, "openspec", "schemas", "legacy-workflow", "schema.yaml")
	if err := os.MkdirAll(filepath.Dir(legacySchemaPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacySchemaPath, []byte("artifacts:\n  specs:\n    type: specs\n    generates: specs/**/*.md\n    template: spec.md\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	legacyTemplatePath := filepath.Join(root, "openspec", "schemas", "legacy-workflow", "templates", "spec.md")
	if err := os.MkdirAll(filepath.Dir(legacyTemplatePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyTemplatePath, []byte("## Requirement: {{.Input.requirement.title}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	neutralArgs := []string{"--repo", "local-source/local-checkout", "--server-org", "browser-e2e", "--server-repo", "httpbin",
		"--provider", "code.example", "--source-web-url", "https://git.example.test/acme/widgets",
		"--tools", "NoNe", "--delivery", "not-used", "--language", "zh", "--skip-labels", "--json"}
	var neutralOutput string
	{
		var out, errOut bytes.Buffer
		app := newApp(strings.NewReader(""), &out, &errOut)
		app.profileName = "e2e"
		if code := app.runInit(t.Context(), neutralArgs); code != 0 {
			t.Fatalf("tools-none init exit=%d stderr=%s stdout=%s", code, errOut.String(), out.String())
		}
		neutralOutput = out.String()
	}
	for _, want := range []string{`"language_applied": false`, `"provider_key": "code.example"`,
		`"mode": "operator-preflight-required"`, `"merge_capable": false`,
		`"provider_authority_capable": true`, "openspec/config.yaml"} {
		if !strings.Contains(neutralOutput, want) {
			t.Fatalf("tools-none init output missing %q: %s", want, neutralOutput)
		}
	}
	if got := readTestFile(t, openspecPath); got != openspecConfig {
		t.Fatalf("tools-none init changed OpenSpec config:\nwant %q\n got %q", openspecConfig, got)
	}
	legacyPlan, err := workflow.Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	if legacyPlan.Source.Kind != workflow.SourceLegacyOpenSpec || legacyPlan.Config.Rules["language"] != "Existing OpenSpec Language" {
		t.Fatalf("workflow plan after tools-none init = source %q rules=%+v", legacyPlan.Source.Kind, legacyPlan.Config.Rules)
	}
	for _, path := range []string{filepath.Join(root, "issue-spec", "config.yaml"), filepath.Join(root, ".agents"), filepath.Join(root, ".claude"), filepath.Join(root, ".codex")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("tools-none init created workflow path %q: %v", path, err)
		}
	}
	neutralConfig := readTestFile(t, filepath.Join(root, ".issue-spec", "config.json"))
	for _, want := range []string{`"repo": "browser-e2e/httpbin"`, `"profile": "e2e"`, `"server_instance_id": "issue-spec:e2e"`,
		`"key": "code.example"`, `"external_repository": "acme/widgets"`, `"change.comment"`, `"change.merge-conditional"`} {
		if !strings.Contains(neutralConfig, want) {
			t.Fatalf("tools-none runtime config missing %q:\n%s", want, neutralConfig)
		}
	}
	if strings.Contains(neutralConfig, "language") {
		t.Fatalf("tools-none runtime config persisted language:\n%s", neutralConfig)
	}
	neutralJournal := readTestFile(t, filepath.Join(root, ".issue-spec", "init-state.json"))
	var neutralJournalState selfHostedInitJournal
	if err := json.Unmarshal([]byte(neutralJournal), &neutralJournalState); err != nil {
		t.Fatal(err)
	}
	if stage := neutralJournalState.Stages["workflow"]; stage.State != "skipped" || stage.Detail != "--tools none" {
		t.Fatalf("tools-none workflow journal stage = %+v", stage)
	}
	invalidWorkflowPath := filepath.Join(root, "issue-spec", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(invalidWorkflowPath), 0o755); err != nil {
		t.Fatal(err)
	}
	invalidWorkflowConfig := "external_code: [invalid provider workflow\n"
	if err := os.WriteFile(invalidWorkflowPath, []byte(invalidWorkflowConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	{
		var out, errOut bytes.Buffer
		app := newApp(strings.NewReader(""), &out, &errOut)
		app.profileName = "e2e"
		if code := app.runInit(t.Context(), neutralArgs); code != 0 {
			t.Fatalf("tools-none invalid-config rerun exit=%d stderr=%s stdout=%s", code, errOut.String(), out.String())
		}
	}
	if got := readTestFile(t, invalidWorkflowPath); got != invalidWorkflowConfig {
		t.Fatalf("tools-none init changed invalid workflow config:\nwant %q\n got %q", invalidWorkflowConfig, got)
	}
	if got := readTestFile(t, openspecPath); got != openspecConfig {
		t.Fatalf("tools-none invalid-config rerun changed OpenSpec config:\nwant %q\n got %q", openspecConfig, got)
	}
	projectConfigBefore := readTestFile(t, filepath.Join(root, ".issue-spec", "config.json"))
	journalBefore := readTestFile(t, filepath.Join(root, ".issue-spec", "init-state.json"))
	remoteCallsBefore := [3]int{ensureCalls, bindingCalls, labelCalls}
	noProviderArgs := []string{"--repo", "local-source/local-checkout", "--server-org", "browser-e2e", "--server-repo", "httpbin",
		"--skip-source-binding", "--skip-labels", "--tools", "codex", "--delivery", "skills", "--json"}
	{
		var out, errOut bytes.Buffer
		app := newApp(strings.NewReader(""), &out, &errOut)
		app.profileName = "e2e"
		code := app.runInit(t.Context(), noProviderArgs)
		preflightError := strings.Contains(errOut.String(), "validate workflow generation inputs") ||
			strings.Contains(errOut.String(), "validate existing provider workflow config")
		if code == 0 || !preflightError {
			t.Fatalf("invalid workflow init exit=%d stderr=%s stdout=%s", code, errOut.String(), out.String())
		}
	}
	if got := readTestFile(t, filepath.Join(root, ".issue-spec", "config.json")); got != projectConfigBefore {
		t.Fatalf("invalid workflow init changed project config:\nwant %s\n got %s", projectConfigBefore, got)
	}
	if got := readTestFile(t, filepath.Join(root, ".issue-spec", "init-state.json")); got != journalBefore {
		t.Fatalf("invalid workflow init changed journal:\nwant %s\n got %s", journalBefore, got)
	}
	if got := [3]int{ensureCalls, bindingCalls, labelCalls}; got != remoteCallsBefore {
		t.Fatalf("invalid workflow init mutated remote state: before=%v after=%v", remoteCallsBefore, got)
	}
	if err := os.RemoveAll(filepath.Join(root, "issue-spec")); err != nil {
		t.Fatal(err)
	}
	{
		var out, errOut bytes.Buffer
		app := newApp(strings.NewReader(""), &out, &errOut)
		app.profileName = "e2e"
		if code := app.runInit(t.Context(), noProviderArgs); code != 0 {
			t.Fatalf("provider-neutral rerun exit=%d stderr=%s stdout=%s", code, errOut.String(), out.String())
		}
	}
	preservedConfig := readTestFile(t, filepath.Join(root, ".issue-spec", "config.json"))
	for _, want := range []string{`"key": "code.example"`, `"external_repository": "acme/widgets"`, `"change.merge-conditional"`,
		`"mode": "planning-only"`, `"planning_only": true`, `"merge_capable": false`} {
		if !strings.Contains(preservedConfig, want) {
			t.Fatalf("provider-neutral rerun dropped existing provider metadata %q:\n%s", want, preservedConfig)
		}
	}
	var preservedJournal selfHostedInitJournal
	if err := json.Unmarshal([]byte(readTestFile(t, filepath.Join(root, ".issue-spec", "init-state.json"))), &preservedJournal); err != nil {
		t.Fatal(err)
	}
	if stage := preservedJournal.Stages["provider"]; stage.State != "complete" || stage.Detail != "code.example" {
		t.Fatalf("provider-neutral rerun journal provider stage = %+v", stage)
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
	if !strings.Contains(finalOutput, `"repo": "browser-e2e/httpbin"`) ||
		!strings.Contains(finalOutput, `"mode": "operator-preflight-required"`) ||
		!strings.Contains(finalOutput, `"merge_capable": false`) ||
		!strings.Contains(finalOutput, `"provider_authority_capable": true`) || strings.Contains(finalOutput, "local-source/local-checkout") {
		t.Fatalf("init output did not use the resolved server target: %s", finalOutput)
	}
	config := readTestFile(t, filepath.Join(root, ".issue-spec", "config.json"))
	for _, want := range []string{`"version": 2`, `"repo": "browser-e2e/httpbin"`, `"profile": "e2e"`, `"key": "code.example"`,
		`"external_repository": "acme/widgets"`, `"mode": "operator-preflight-required"`,
		`"merge_capable": false`, `"provider_authority_capable": true`} {
		if !strings.Contains(config, want) {
			t.Fatalf("project config missing %q:\n%s", want, config)
		}
	}
	workflowConfig := readTestFile(t, filepath.Join(root, "issue-spec", "config.yaml"))
	for _, want := range []string{"external_code:", "provider_key: code.example"} {
		if !strings.Contains(workflowConfig, want) {
			t.Fatalf("workflow config missing %q:\n%s", want, workflowConfig)
		}
	}
	if strings.Contains(workflowConfig, "evidence:") {
		t.Fatalf("workflow config must not emit the retired evidence gate:\n%s", workflowConfig)
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
	var journalState selfHostedInitJournal
	if err := json.Unmarshal([]byte(journal), &journalState); err != nil {
		t.Fatal(err)
	}
	if labels := journalState.Stages["labels"]; labels.State != "complete" || labels.Detail != "ensured" {
		t.Fatalf("default labels stage = %+v", labels)
	}
}

func runInitTestGit(t *testing.T, args ...string) {
	t.Helper()
	skipWithoutRealGit(t)
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
	return map[string]any{"id": id, "provider_key": "code.example", "external_repository_id": "acme/widgets",
		"clone_url": "https://git.example.test/acme/widgets.git",
		"web_url":   "https://git.example.test/acme/widgets", "default_branch": "main",
		"version": 1, "active": true, "created_at": "2026-07-12T00:00:00Z", "updated_at": "2026-07-12T00:00:00Z"}
}

func writeTestBinding(w http.ResponseWriter, id uuid.UUID) { writeTestJSON(w, testBinding(id)) }

func writeTestProviderRegistry(t *testing.T, root string) string {
	t.Helper()
	bridge := filepath.Join(root, "code-example-bridge")
	bridgeBody := `#!/usr/bin/python3
import json, sys
request = json.load(sys.stdin)
response = {"protocol": request["protocol"], "request_id": request["request_id"]}
if request["action"] == "capabilities":
    response["capabilities"] = {"protocol_version": request["protocol"], "semantic_generation": "minimal-merge-authority/v1", "provider_build_identity": "code-example@sha256:0123456789abcdef", "values": ["change.comment", "evidence.review-decision", "evidence.authoritative-check-conclusion", "change.merge-conditional"]}
else:
    response["error"] = {"code": "unsupported", "message": "unsupported in init test"}
json.dump(response, sys.stdout)
`
	if err := os.WriteFile(bridge, []byte(bridgeBody), 0o700); err != nil {
		t.Fatal(err)
	}
	registry := filepath.Join(root, "providers.json")
	body := fmt.Sprintf(`{"version":1,"providers":{"code.example":{"path":%q,"description":{"provider_key":"code.example","display_name":"Example Code","remote_authorities":["git.example.test"],"code_change_label":"Merge request","semantic_generation":"minimal-merge-authority/v1","provider_build_identity":"code-example@sha256:0123456789abcdef","capabilities":["change.comment","evidence.review-decision","evidence.authoritative-check-conclusion","change.merge-conditional"]}}}}`, bridge)
	if err := os.WriteFile(registry, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return registry
}

func writeProviderRegistryFixture(t *testing.T, root, key string, description, capabilities map[string]any) string {
	t.Helper()
	bridge := filepath.Join(root, strings.ReplaceAll(key, ".", "-")+"-bridge")
	capabilityJSON, err := json.Marshal(capabilities)
	if err != nil {
		t.Fatal(err)
	}
	bridgeBody := fmt.Sprintf(`#!/usr/bin/python3
import json, sys
request = json.load(sys.stdin)
json.dump({"protocol": request["protocol"], "request_id": request["request_id"], "capabilities": json.loads(%q)}, sys.stdout)
`, string(capabilityJSON))
	if err := os.WriteFile(bridge, []byte(bridgeBody), 0o700); err != nil {
		t.Fatal(err)
	}
	registry := filepath.Join(root, strings.ReplaceAll(key, ".", "-")+"-providers.json")
	body, err := json.Marshal(map[string]any{"version": 1, "providers": map[string]any{key: map[string]any{
		"path": bridge, "description": description}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registry, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return registry
}
