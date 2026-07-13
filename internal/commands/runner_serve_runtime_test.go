package commands

import (
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/higress-group/issue-spec/internal/codereview"
	"github.com/higress-group/issue-spec/internal/commentrunner"
	webhook "github.com/higress-group/issue-spec/internal/commentrunner/intake/webhook"
	"github.com/higress-group/issue-spec/internal/commentrunner/jobs"
	runnerserver "github.com/higress-group/issue-spec/internal/commentrunner/server"
	"github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/processworkspace"
)

func TestDefaultRunnerServeRuntimeResolvesNativeScopesAndNeverPollsNotifications(t *testing.T) {
	orgID, repoID := uuid.New(), uuid.New()
	var mu sync.Mutex
	requests := []string{}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.URL.Path)
		mu.Unlock()
		if got := r.Header.Get("Authorization"); got != "Bearer parent-token" {
			t.Fatalf("authorization=%q", got)
		}
		switch r.URL.Path {
		case "/api/v1/context":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"user": map[string]string{"id": uuid.NewString(), "login": "runner"},
				"organizations": []map[string]string{{"id": orgID.String(), "name": "owner"}}})
		case "/api/v1/context/orgs/" + orgID.String() + "/repos":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"repositories": []map[string]interface{}{{"repository": map[string]string{
				"id": repoID.String(), "organization_id": orgID.String(), "name": "repo"}}}})
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
		OperatorRegistryFile: writeRunnerWorkspaceRegistry(t, []codereview.Capability{codereview.CapabilityEvidenceSnapshot, codereview.CapabilityChangeComment})}
	runner := commentrunner.Config{Profile: profile.Name, Hostname: profile.Hostname, Repositories: []string{"owner/repo"},
		RunnerIdentity: "runner", StatePath: filepath.Join(temp, "state.json"), WorkspaceRoot: filepath.Join(temp, "workspaces"),
		WorkspaceRetention: commentrunner.NewDuration(time.Hour), MaxConcurrentJobs: 1, AcpxPath: "acpx",
		Agent: commentrunner.DefaultAgentConfig(), CancellationEnabled: true, UnsafeNoSandbox: true}
	runtime, err := defaultBuildRunnerServeRuntime(t.Context(), runnerServeRuntimeInput{Profile: profile, ParentToken: "parent-token",
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

func TestRunnerOperatorProcessWorkspaceAdaptersUseTrustedRegistryAndExactIdentity(t *testing.T) {
	path := writeRunnerWorkspaceRegistry(t, []codereview.Capability{codereview.CapabilityWorkspaceReady, codereview.CapabilityWorkspaceCleanup})
	profile := auth.Profile{OperatorRegistryFile: path}
	adapters, err := runnerOperatorProcessWorkspaceAdapters(t.Context(), profile)
	if err != nil {
		t.Fatal(err)
	}
	identity := state.ProcessWorkspaceProviderIdentity{ProviderKey: "code.example", ServerInstance: "server", Host: "code.example"}
	key, err := jobs.ProcessWorkspaceAdapterKey(identity)
	if err != nil {
		t.Fatal(err)
	}
	adapter := adapters[key]
	if adapter == nil || len(adapters) != 1 {
		t.Fatalf("adapters=%v exact key=%q", adapters, key)
	}
	association := state.ProcessWorkspaceAssociation{Provider: identity, Repository: "owner/repo", ProcessID: "PROCESS-028",
		WorkspaceID: "workspace-028", ReservationID: "reservation:workspace-028",
		ReservationIdentity: "identity:workspace-028", RuntimeNamespace: "process-workspace-028",
		ExecutionClass: processworkspace.ExecutionExternal, Mode: processworkspace.ModeNone}
	if ready, err := adapter.Ready(t.Context(), association); err != nil || !ready {
		t.Fatalf("ready=%t err=%v", ready, err)
	}
	if cleaned, err := adapter.Cleanup(t.Context(), association); err != nil || !cleaned {
		t.Fatalf("cleanup=%t err=%v", cleaned, err)
	}
	association.Provider.ServerInstance = "other-server"
	if ready, err := adapter.Ready(t.Context(), association); err == nil || ready {
		t.Fatalf("mismatched identity ready=%t err=%v", ready, err)
	}
}

func TestRunnerOperatorProcessWorkspaceAdaptersRequireBothDeclaredLifecycleCapabilities(t *testing.T) {
	for _, test := range []struct {
		name         string
		capabilities []codereview.Capability
	}{
		{name: "ready only", capabilities: []codereview.Capability{codereview.CapabilityWorkspaceReady}},
		{name: "cleanup only", capabilities: []codereview.Capability{codereview.CapabilityWorkspaceCleanup}},
	} {
		t.Run(test.name, func(t *testing.T) {
			profile := auth.Profile{OperatorRegistryFile: writeRunnerWorkspaceRegistry(t, test.capabilities)}
			if adapters, err := runnerOperatorProcessWorkspaceAdapters(t.Context(), profile); err == nil ||
				!strings.Contains(err.Error(), codereview.ErrCapabilityMissing.Error()) || len(adapters) != 0 {
				t.Fatalf("adapters=%v err=%v", adapters, err)
			}
		})
	}
}

func TestRunnerOperatorProcessWorkspaceAdaptersSkipNonWorkspaceProviders(t *testing.T) {
	full := []codereview.Capability{codereview.CapabilityWorkspaceReady, codereview.CapabilityWorkspaceCleanup}
	unrelated := []codereview.Capability{codereview.CapabilityEvidenceSnapshot, codereview.CapabilityChangeComment}
	t.Run("only evidence and change provider", func(t *testing.T) {
		profile := auth.Profile{OperatorRegistryFile: writeRunnerWorkspaceRegistryProviders(t, map[string][]codereview.Capability{
			"evidence.example": unrelated,
		})}
		adapters, err := runnerOperatorProcessWorkspaceAdapters(t.Context(), profile)
		if err != nil || len(adapters) != 0 {
			t.Fatalf("adapters=%v err=%v", adapters, err)
		}
	})
	t.Run("unrelated and full workspace providers", func(t *testing.T) {
		profile := auth.Profile{OperatorRegistryFile: writeRunnerWorkspaceRegistryProviders(t, map[string][]codereview.Capability{
			"evidence.example": unrelated,
			"code.example":     full,
		})}
		adapters, err := runnerOperatorProcessWorkspaceAdapters(t.Context(), profile)
		identity := state.ProcessWorkspaceProviderIdentity{ProviderKey: "code.example", ServerInstance: "server", Host: "code.example"}
		key, keyErr := jobs.ProcessWorkspaceAdapterKey(identity)
		if err != nil || keyErr != nil || len(adapters) != 1 || adapters[key] == nil {
			t.Fatalf("adapters=%v err=%v key_err=%v", adapters, err, keyErr)
		}
	})
}

func writeRunnerWorkspaceRegistry(t *testing.T, capabilities []codereview.Capability) string {
	t.Helper()
	return writeRunnerWorkspaceRegistryProviders(t, map[string][]codereview.Capability{"code.example": capabilities})
}

func writeRunnerWorkspaceRegistryProviders(t *testing.T, providers map[string][]codereview.Capability) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	configured := make(map[string]any, len(providers))
	for key, capabilities := range providers {
		declared := make([]string, len(capabilities))
		for i, capability := range capabilities {
			declared[i] = string(capability)
		}
		configured[key] = map[string]any{
			"path": executable, "args": []string{"-test.run=^TestRunnerWorkspaceLifecycleProviderHelper$"},
			"environment": []string{"ISSUE_SPEC_RUNNER_WORKSPACE_HELPER=1"},
			"description": map[string]any{"remote_authorities": []string{key + ":8443"}, "capabilities": declared},
		}
	}
	config := map[string]any{"version": 1, "providers": configured}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "operator-providers.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunnerWorkspaceLifecycleProviderHelper(t *testing.T) {
	if os.Getenv("ISSUE_SPEC_RUNNER_WORKSPACE_HELPER") != "1" {
		return
	}
	var request struct {
		Protocol  string          `json:"protocol"`
		RequestID string          `json:"request_id"`
		Action    string          `json:"action"`
		Payload   json.RawMessage `json:"payload"`
	}
	if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil {
		os.Exit(2)
	}
	response := map[string]any{"protocol": codereview.ProtocolVersion, "request_id": request.RequestID}
	switch request.Action {
	case "capabilities":
		response["capabilities"] = map[string]any{"protocol_version": codereview.ProtocolVersion,
			"values": []string{string(codereview.CapabilityWorkspaceReady), string(codereview.CapabilityWorkspaceCleanup)}}
	case "workspace_lifecycle":
		var payload map[string]any
		if err := json.Unmarshal(request.Payload, &payload); err != nil {
			os.Exit(2)
		}
		if payload["remote_authority"] != "code.example:8443" {
			os.Exit(4)
		}
		response["workspace_lifecycle"] = map[string]any{"kind": payload["kind"], "provider_key": payload["provider_key"],
			"server_instance": payload["server_instance"], "provider_host": payload["provider_host"],
			"remote_authority": payload["remote_authority"], "repository": payload["repository"], "process_id": payload["process_id"],
			"workspace_id": payload["workspace_id"], "reservation_id": payload["reservation_id"],
			"reservation_identity": payload["reservation_identity"], "runtime_namespace": payload["runtime_namespace"], "confirmed": true}
	default:
		_, _ = fmt.Fprintln(os.Stderr, "unexpected action")
		os.Exit(3)
	}
	_ = json.NewEncoder(os.Stdout).Encode(response)
	os.Exit(0)
}
