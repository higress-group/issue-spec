package commands

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/commentrunner"
	"github.com/higress-group/issue-spec/internal/commentrunner/credentials"
	webhook "github.com/higress-group/issue-spec/internal/commentrunner/intake/webhook"
	repository "github.com/higress-group/issue-spec/internal/commentrunner/repository"
	runnerserver "github.com/higress-group/issue-spec/internal/commentrunner/server"
	"github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/server/models"
)

func TestRunnerServeCredentialRootIsBoundToFullStatePath(t *testing.T) {
	parent := t.TempDir()
	first, err := runnerServeCredentialRoot(filepath.Join(parent, "first.json"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := runnerServeCredentialRoot(filepath.Join(parent, "second.json"))
	if err != nil {
		t.Fatal(err)
	}
	if first == second || first != filepath.Join(parent, "first.json.credentials") ||
		second != filepath.Join(parent, "second.json.credentials") {
		t.Fatalf("credential roots first=%q second=%q", first, second)
	}
	firstMaterializer := credentials.Materializer{Root: first}
	secondMaterializer := credentials.Materializer{Root: second}
	firstToken, err := firstMaterializer.WriteProfileToken("first-token")
	if err != nil {
		t.Fatal(err)
	}
	secondToken, err := secondMaterializer.WriteProfileToken("second-token")
	if err != nil {
		t.Fatal(err)
	}
	firstValue, err := os.ReadFile(firstToken.HostPath)
	if err != nil {
		t.Fatal(err)
	}
	secondValue, err := os.ReadFile(secondToken.HostPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstValue) != "first-token\n" || string(secondValue) != "second-token\n" || firstToken.HostPath == secondToken.HostPath {
		t.Fatalf("profile token isolation first=%q second=%q", firstToken.HostPath, secondToken.HostPath)
	}
}

func TestValidateOrganizationRunnerSubscriptionRequiresExactRunnerContract(t *testing.T) {
	organizationID, subscriptionID, repositoryID := uuid.New(), uuid.New(), uuid.New()
	valid := github.NativeWebhookSubscription{ID: subscriptionID, OrganizationID: organizationID,
		ScopeType: "organization", Active: true, EventTypes: []string{"issue_comment.created", "issue_comment.edited"},
		DeliveryFormat: "issue-spec.v1", SigningMode: "bearer"}
	if err := validateOrganizationRunnerSubscription(valid, organizationID, subscriptionID); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*github.NativeWebhookSubscription){
		func(item *github.NativeWebhookSubscription) { item.RepositoryID = &repositoryID },
		func(item *github.NativeWebhookSubscription) { item.Active = false },
		func(item *github.NativeWebhookSubscription) { item.DeliveryFormat = "github.v3" },
		func(item *github.NativeWebhookSubscription) { item.SigningMode = "none" },
		func(item *github.NativeWebhookSubscription) { item.EventTypes = []string{"issue_comment.created"} },
		func(item *github.NativeWebhookSubscription) {
			item.EventTypes = append(item.EventTypes, "issue.created")
		},
	} {
		invalid := valid
		invalid.EventTypes = append([]string(nil), valid.EventTypes...)
		mutate(&invalid)
		if err := validateOrganizationRunnerSubscription(invalid, organizationID, subscriptionID); err == nil {
			t.Fatalf("invalid subscription accepted: %+v", invalid)
		}
	}
}

func TestBuildRunnerRepositoryRegistryVerifiesSubscriptionAndLazilyEnrolls(t *testing.T) {
	organizationID, repositoryID, subscriptionID := uuid.New(), uuid.New(), uuid.New()
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.Path)
		wantAuthorization := "Bearer profile-token"
		if strings.HasSuffix(r.URL.Path, "/runner-verification") {
			wantAuthorization = "Bearer subscription-secret"
		}
		if got := r.Header.Get("Authorization"); got != wantAuthorization {
			t.Fatalf("path=%q authorization=%q want=%q", r.URL.Path, got, wantAuthorization)
		}
		switch r.URL.Path {
		case "/api/v1/context":
			_ = json.NewEncoder(w).Encode(github.NativeContext{Organizations: []github.NativeOrganizationContext{{ID: organizationID.String(), Name: "owner"}}})
		case "/api/v1/orgs/" + organizationID.String() + "/webhooks/" + subscriptionID.String() + "/runner-verification":
			_ = json.NewEncoder(w).Encode(github.NativeWebhookSubscription{ID: subscriptionID, OrganizationID: organizationID,
				ScopeType: "organization", Active: true, EventTypes: []string{"issue_comment.created", "issue_comment.edited"},
				DeliveryFormat: "issue-spec.v1", SigningMode: "bearer"})
		case "/api/v1/context/orgs/" + organizationID.String() + "/repos":
			_ = json.NewEncoder(w).Encode(github.NativeRepositoriesContext{Repositories: []github.NativeRepositoryContext{{
				Repository:     github.NativeRepositorySummary{ID: repositoryID.String(), OrganizationID: organizationID.String(), Name: "new-repo"},
				AllowedActions: []string{"runner.trigger"},
			}}})
		case "/api/v1/orgs/" + organizationID.String() + "/repos/" + repositoryID.String() + "/bindings/active":
			_ = json.NewEncoder(w).Encode(github.NativeBinding{ID: uuid.NewString(), Version: 1, Active: true,
				ProviderKey: "github", ExternalRepositoryID: "owner/new-repo", CloneURL: "https://example.test/new-repo.git",
				WebURL: "https://example.test/new-repo", DefaultBranch: "main"})
		default:
			t.Fatalf("unexpected request %s", r.URL.Path)
		}
	}))
	defer server.Close()
	native, err := github.NewClientWithOptions(github.ClientOptions{Host: "issues.test", BaseURL: server.URL + "/api/v1",
		Token: "profile-token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := github.NewClientWithOptions(github.ClientOptions{Host: "issues.test", BaseURL: server.URL + "/api/v1",
		Token: "subscription-secret", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	registry, legacyScopes, err := buildRunnerRepositoryRegistry(t.Context(), native, verifier,
		commentrunner.Config{Organization: "owner", SubscriptionID: subscriptionID.String()})
	if err != nil {
		t.Fatal(err)
	}
	entry, err := registry.ResolveScope(t.Context(), models.RepoScope{OrgID: organizationID, RepoID: repositoryID})
	if err != nil || entry.Repository != "owner/new-repo" || entry.Binding.CloneURL != "https://example.test/new-repo.git" {
		t.Fatalf("entry=%+v err=%v", entry, err)
	}
	if len(requests) != 4 {
		t.Fatalf("requests=%v", requests)
	}
	if len(legacyScopes) != 0 {
		t.Fatalf("organization registry retained static legacy scopes: %+v", legacyScopes)
	}
}

func TestBuildRunnerRepositoryRegistryRetainsExplicitScopesForLegacyCleanup(t *testing.T) {
	organizationID, repositoryID := uuid.New(), uuid.New()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/context":
			_ = json.NewEncoder(w).Encode(github.NativeContext{Organizations: []github.NativeOrganizationContext{{
				ID: organizationID.String(), Name: "owner",
			}}})
		case "/api/v1/context/orgs/" + organizationID.String() + "/repos":
			_ = json.NewEncoder(w).Encode(github.NativeRepositoriesContext{Repositories: []github.NativeRepositoryContext{{
				Repository: github.NativeRepositorySummary{ID: repositoryID.String(),
					OrganizationID: organizationID.String(), Name: "repo"},
			}}})
		default:
			t.Fatalf("unexpected request %s", r.URL.Path)
		}
	}))
	defer server.Close()
	native, err := github.NewClientWithOptions(github.ClientOptions{Host: "issues.test", BaseURL: server.URL + "/api/v1",
		Token: "profile-token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	registry, legacyScopes, err := buildRunnerRepositoryRegistry(t.Context(), native, nil,
		commentrunner.Config{Repositories: []string{"owner/repo"}})
	want := models.RepoScope{OrgID: organizationID, RepoID: repositoryID}
	if err != nil || legacyScopes["owner/repo"] != want {
		t.Fatalf("legacy scopes=%+v err=%v", legacyScopes, err)
	}
	static, ok := registry.(*repository.StaticRegistry)
	if !ok || static.Scopes["owner/repo"] != want {
		t.Fatalf("registry=%T scopes=%+v", registry, static)
	}
	static.Scopes["owner/repo"] = models.RepoScope{}
	if legacyScopes["owner/repo"] != want {
		t.Fatal("legacy cleanup scope map aliases the live registry map")
	}
}

func TestDefaultRunnerServeRuntimeSupportsMultipleRepositoriesAndNeverPollsNotifications(t *testing.T) {
	orgID, repoID, otherRepoID := uuid.New(), uuid.New(), uuid.New()
	var mu sync.Mutex
	requests := []string{}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.URL.Path)
		mu.Unlock()
		if got := r.Header.Get("Authorization"); got != "Bearer profile-token" {
			t.Fatalf("authorization=%q", got)
		}
		switch r.URL.Path {
		case "/api/v1/context":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"user": map[string]string{"id": uuid.NewString(), "login": "runner"},
				"organizations": []map[string]string{{"id": orgID.String(), "name": "owner"}}})
		case "/api/v1/context/orgs/" + orgID.String() + "/repos":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"repositories": []map[string]interface{}{
				{"repository": map[string]string{"id": repoID.String(), "organization_id": orgID.String(), "name": "repo"}},
				{"repository": map[string]string{"id": otherRepoID.String(), "organization_id": orgID.String(), "name": "other"}},
			}})
		default:
			t.Fatalf("unexpected API request %s", r.URL.Path)
		}
	}))
	defer api.Close()
	command, err := exec.LookPath("true")
	if err != nil {
		t.Skip("true executable unavailable")
	}
	command, _ = filepath.Abs(command)
	temp := t.TempDir()
	store, err := state.OpenFileStore(filepath.Join(temp, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	queue, _ := webhook.NewQueue(store, webhook.QueueConfig{})
	credentials, _ := webhook.NewCredentials(uuid.NewString(), webhook.Secret{Value: []byte(strings.Repeat("s", 32))}, nil)
	handler, _ := webhook.NewHandler(webhook.HandlerConfig{Credentials: credentials, Queue: queue})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	httpService, err := runnerserver.New(runnerserver.Config{Listener: listener}, handler)
	if err != nil {
		t.Fatal(err)
	}
	profile := auth.Profile{Name: "self-hosted-test", Kind: auth.ProfileKindHosted, Hostname: "issues.test",
		APIURL: api.URL + "/api/v3", NativeAPIURL: api.URL + "/api/v1", WebURL: api.URL, ServerInstanceID: "instance-test",
		OperatorRegistryFile: filepath.Join(temp, "deliberately-missing-unrelated-operator-registry.json")}
	runner := commentrunner.Config{Profile: profile.Name, Hostname: profile.Hostname, Repositories: []string{"owner/repo", "owner/other"},
		RunnerIdentity: "runner", StatePath: filepath.Join(temp, "state.json"), WorkspaceRoot: filepath.Join(temp, "workspaces"),
		WorkspaceRetention: commentrunner.NewDuration(time.Hour), MaxConcurrentJobs: 1, AcpxPath: "acpx",
		Agent: commentrunner.DefaultAgentConfig(), CancellationEnabled: true, UnsafeNoSandbox: true}
	runtime, err := defaultBuildRunnerServeRuntime(t.Context(), runnerServeRuntimeInput{Profile: profile, ProfileToken: "profile-token",
		Runner: runner, Queue: queue, Store: store, HTTP: httpService, GitCredentialCommand: command,
		GitCredentialTimeout: time.Second, GitCredentialMaxOutput: 1024, GitCredentialConcurrency: 1,
		ReconcileWorkers: 1, ReconcileLease: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := runtime.(*runnerserver.Runtime); !ok {
		t.Fatalf("production builder returned %T, want composed runner runtime", runtime)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- runtime.Run(ctx) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("requests=%v", requests)
	}
	for _, path := range requests {
		if strings.Contains(path, "notifications") {
			t.Fatalf("runner serve called notifications: %v", requests)
		}
	}
}
