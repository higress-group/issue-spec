package commands

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/commentrunner"
	webhook "github.com/higress-group/issue-spec/internal/commentrunner/intake/webhook"
	runnerserver "github.com/higress-group/issue-spec/internal/commentrunner/server"
	"github.com/higress-group/issue-spec/internal/commentrunner/state"
)

func TestDefaultRunnerServeRuntimeIgnoresUnrelatedOperatorRegistryAndNeverPollsNotifications(t *testing.T) {
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
		OperatorRegistryFile: filepath.Join(temp, "deliberately-missing-unrelated-operator-registry.json")}
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
