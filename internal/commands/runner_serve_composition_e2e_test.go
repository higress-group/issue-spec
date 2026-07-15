package commands

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/acpx"
	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/commentrunner"
	runnercontext "github.com/higress-group/issue-spec/internal/commentrunner/context"
	webhook "github.com/higress-group/issue-spec/internal/commentrunner/intake/webhook"
	"github.com/higress-group/issue-spec/internal/commentrunner/jobs"
	runnerserver "github.com/higress-group/issue-spec/internal/commentrunner/server"
	"github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/server/events/outbox"
	"github.com/higress-group/issue-spec/internal/workspace"
)

func TestRunnerServeCompositionAcceptedDeliveryReachesChildAuthenticatedJob(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	orgID, repoID, issueID, commentID, actorID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	commentNumericID := int64(71)
	var reactionCalls, writebackCalls atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer profile-pat" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if strings.Contains(r.URL.Path, "notifications") {
			http.Error(w, "notifications forbidden", http.StatusInternalServerError)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/context":
			writeRunnerJSON(w, http.StatusOK, map[string]any{"user": map[string]string{"id": uuid.NewString(), "login": "runner"},
				"organizations": []map[string]string{{"id": orgID.String(), "name": "owner"}}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/context/orgs/"+orgID.String()+"/repos":
			writeRunnerJSON(w, http.StatusOK, map[string]any{"repositories": []map[string]any{{"repository": map[string]string{
				"id": repoID.String(), "organization_id": orgID.String(), "name": "repo"}}}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/orgs/"+orgID.String()+"/repos/"+repoID.String()+"/bindings/active":
			writeRunnerJSON(w, http.StatusOK, map[string]any{"id": uuid.NewString(), "provider_key": "test-git",
				"external_repository_id": "org/repo", "clone_url": "https://git.example.test/org/repo.git",
				"web_url": "https://git.example.test/org/repo", "default_branch": "main", "version": 1, "active": true,
				"created_at": now, "updated_at": now})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v3/user":
			writeRunnerJSON(w, http.StatusOK, map[string]any{"id": 1, "node_id": "runner", "login": "runner"})
		case r.Method == http.MethodGet && r.URL.Path == fmt.Sprintf("/api/v3/repos/owner/repo/issues/comments/%d", commentNumericID):
			w.Header().Set("X-Issue-Spec-Representation-Version", "1")
			writeRunnerJSON(w, http.StatusOK, runnerCommentJSON(apiURLPlaceholder(r), commentID, actorID,
				commentNumericID, "/new verify production composition", "alice", now))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v3/repos/owner/repo/issues/17":
			writeRunnerJSON(w, http.StatusOK, map[string]any{"id": 17, "node_id": runnerNodeID("Issue", issueID), "number": 17,
				"html_url": "https://issues.test/owner/repo/issues/17", "url": "https://issues.test/api/v3/repos/owner/repo/issues/17",
				"title": "runner composition", "body": "", "state": "open"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v3/repos/owner/repo/collaborators/alice/permission":
			writeRunnerJSON(w, http.StatusOK, map[string]string{"permission": "write", "role_name": "write"})
		case r.Method == http.MethodGet && r.URL.Path == fmt.Sprintf("/api/v3/repos/owner/repo/issues/comments/%d/reactions", commentNumericID):
			writeRunnerJSON(w, http.StatusOK, []any{})
		case r.Method == http.MethodPost && r.URL.Path == fmt.Sprintf("/api/v3/repos/owner/repo/issues/comments/%d/reactions", commentNumericID):
			reactionCalls.Add(1)
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v3/repos/owner/repo/issues/17/comments":
			writeRunnerJSON(w, http.StatusOK, []any{})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v3/repos/owner/repo/issues/17/comments":
			writebackCalls.Add(1)
			writeRunnerJSON(w, http.StatusCreated, runnerStatusCommentJSON(900, now))
		case r.Method == http.MethodPatch && r.URL.Path == "/api/v3/repos/owner/repo/issues/comments/900":
			writebackCalls.Add(1)
			writeRunnerJSON(w, http.StatusOK, runnerStatusCommentJSON(900, now))
		default:
			http.Error(w, "unexpected path "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer api.Close()

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	auditPath := filepath.Join(t.TempDir(), "provider-audit.log")
	root := t.TempDir()
	store, err := state.OpenFileStore(filepath.Join(root, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	queue, err := webhook.NewQueue(store, webhook.QueueConfig{})
	if err != nil {
		t.Fatal(err)
	}
	secret := strings.Repeat("s", webhook.MinSecretBytes)
	subscriptionID := uuid.NewString()
	webhookCredentials, err := webhook.NewCredentials(subscriptionID, webhook.Secret{Value: []byte(secret)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := webhook.NewHandler(webhook.HandlerConfig{Credentials: webhookCredentials, Queue: queue})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	httpService, err := runnerserver.New(runnerserver.Config{Listener: listener}, handler)
	if err != nil {
		t.Fatal(err)
	}
	workspaces := &compositionWorkspace{root: filepath.Join(root, "workspaces")}
	sandboxer := &compositionSandbox{}
	profile := auth.Profile{Name: "runner-composition", Kind: auth.ProfileKindHosted, Hostname: "127.0.0.1",
		APIURL: api.URL + "/api/v3", NativeAPIURL: api.URL + "/api/v1", WebURL: api.URL, ServerInstanceID: "instance-e2e",
		OperatorRegistryFile: filepath.Join(root, "deliberately-missing-test-override-registry.json")}
	runner := commentrunner.Config{Profile: profile.Name, Hostname: profile.Hostname, Repositories: []string{"owner/repo"},
		RunnerIdentity: "runner", AllowedUsers: []string{"alice"}, StatePath: filepath.Join(root, "state.json"),
		WorkspaceRoot: workspaces.root, WorkspaceRetention: commentrunner.NewDuration(time.Hour), MaxConcurrentJobs: 1,
		AcpxPath: "acpx-e2e", Agent: commentrunner.DefaultAgentConfig(), CancellationEnabled: true, UnsafeNoSandbox: true}
	runtime, err := defaultBuildRunnerServeRuntime(t.Context(), runnerServeRuntimeInput{Profile: profile, ProfileToken: "profile-pat",
		Runner: runner, Queue: queue, Store: store, HTTP: httpService, GitCredentialCommand: executable,
		GitCredentialArgs:    []string{"-test.run=^TestRunnerServeCredentialCommandHelper$", "--", auditPath},
		GitCredentialTimeout: 5 * time.Second, GitCredentialMaxOutput: 1 << 20, GitCredentialConcurrency: 1,
		ReconcileWorkers: 1, ReconcileLease: time.Minute, Dependencies: &runnerServeRuntimeDependencies{
			Workspaces: workspaces, Sandbox: sandboxer, Acpx: compositionAcpxFactory{}, Artifacts: jobs.NoopArtifactProvider{},
			IssueSpecBinary: "issue-spec-e2e"}})
	if err != nil {
		t.Fatal(err)
	}

	envelope := runnerCompositionEnvelope(now, orgID, repoID, issueID, commentID, actorID, commentNumericID)
	body, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, webhook.Endpoint, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+secret)
	request.Header.Set(webhook.HeaderDeliveryID, uuid.NewString())
	request.Header.Set(webhook.HeaderEventID, envelope.EventID.String())
	request.Header.Set(webhook.HeaderTimestamp, strconv.FormatInt(now.Unix(), 10))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("webhook acceptance=%d body=%s", response.Code, response.Body.String())
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- runtime.Run(ctx) }()
	var completed state.Job
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		current, loadErr := store.Load(t.Context())
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		for _, job := range current.Jobs {
			if job.Status == state.StatusCompleted && job.CredentialCleanup.Status == state.CredentialCleanupComplete {
				completed = job
				break
			}
		}
		if completed.ID != "" {
			break
		}
		select {
		case runErr := <-done:
			t.Fatalf("runtime stopped before completion: %v state=%+v", runErr, current)
		default:
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if completed.ID == "" {
		current, _ := store.Load(t.Context())
		t.Fatalf("accepted delivery did not complete a job: %+v", current)
	}
	if runErr := <-done; runErr != nil {
		t.Fatal(runErr)
	}
	if !sandboxer.authenticated.Load() || !workspaces.cloneCredentialUsed.Load() ||
		reactionCalls.Load() != 1 || writebackCalls.Load() != 2 {
		t.Fatalf("child_auth=%t clone_credential=%t reactions=%d writebacks=%d",
			sandboxer.authenticated.Load(), workspaces.cloneCredentialUsed.Load(), reactionCalls.Load(), writebackCalls.Load())
	}
	serialized, err := json.Marshal(mustLoadRunnerState(t, store))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"profile-pat", "git-child-secret"} {
		if bytes.Contains(serialized, []byte(forbidden)) {
			t.Fatalf("credential leaked into state: %s", serialized)
		}
	}
	audit, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"acquire", "revoke_lease", "acquire", "revoke_lease", "revoke_job"} {
		line, rest, ok := strings.Cut(string(audit), "\n")
		if !ok || line != action {
			t.Fatalf("provider audit=%q want action=%s", audit, action)
		}
		audit = []byte(rest)
	}
}

func TestRunnerServeCredentialCommandHelper(t *testing.T) {
	marker := -1
	for index, argument := range os.Args {
		if argument == "--" && index+1 < len(os.Args) {
			marker = index
			break
		}
	}
	if marker < 0 {
		return
	}
	if marker+1 >= len(os.Args) || os.Getenv("ISSUE_SPEC_TOKEN") != "" || os.Getenv("GH_TOKEN") != "" {
		os.Exit(2)
	}
	auditPath := os.Args[marker+1]
	var request struct {
		Protocol  string         `json:"protocol"`
		RequestID string         `json:"request_id"`
		Action    string         `json:"action"`
		Identity  map[string]any `json:"identity"`
	}
	decoder := json.NewDecoder(os.Stdin)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || request.Protocol != "issue-spec-git-credential-v1" || request.RequestID == "" {
		os.Exit(2)
	}
	file, err := os.OpenFile(auditPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		os.Exit(3)
	}
	_, writeErr := fmt.Fprintln(file, request.Action)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		os.Exit(3)
	}
	response := map[string]any{"protocol": request.Protocol, "request_id": request.RequestID,
		"action": request.Action, "identity": request.Identity}
	if request.Action == "acquire" {
		request.Identity["lease_id"] = "lease-e2e"
		response["lease"] = map[string]any{"lease_id": "lease-e2e", "username": "runner",
			"password": "git-child-secret", "expires_at": time.Now().UTC().Add(4 * time.Minute)}
	} else {
		response["revoked"] = true
	}
	if err := json.NewEncoder(os.Stdout).Encode(response); err != nil {
		os.Exit(3)
	}
	os.Exit(0)
}

func runnerCompositionEnvelope(now time.Time, orgID, repoID, issueID, commentID, actorID uuid.UUID, commentNumericID int64) outbox.Envelope {
	raw := "/new verify production composition"
	digest := sha256.Sum256([]byte(raw))
	return outbox.Envelope{SchemaVersion: outbox.SchemaVersion, EventID: uuid.New(), EventKey: "issue_comment:" + commentID.String() + ":v1",
		EventType: "issue_comment.created", Action: "created", OccurredAt: now, OrganizationID: orgID, RepositoryID: repoID,
		Issue: outbox.IssueIdentity{StableID: issueID, Number: 17, RepresentationVersion: 1,
			CreatedAt: now.Add(-time.Hour), UpdatedAt: now},
		Comment: &outbox.CommentRevision{StableID: commentID, NumericID: commentNumericID, RepresentationVersion: 1,
			CreatedAt: now.Add(-time.Minute), UpdatedAt: now},
		RawBody: raw, BodyHash: hex.EncodeToString(digest[:]), ActorUserID: actorID,
		Author: outbox.AuthorIdentity{UserID: &actorID, Login: "alice"}}
}

func runnerCommentJSON(base string, commentID, actorID uuid.UUID, numericID int64, body, login string, now time.Time) map[string]any {
	return map[string]any{"id": numericID, "node_id": runnerNodeID("IssueComment", commentID),
		"html_url":  base + "/owner/repo/issues/17#issuecomment-71",
		"url":       base + "/api/v3/repos/owner/repo/issues/comments/71",
		"issue_url": base + "/api/v3/repos/owner/repo/issues/17", "body": body,
		"user":       map[string]any{"id": 2, "node_id": runnerNodeID("User", actorID), "login": login},
		"created_at": now.Add(-time.Minute), "updated_at": now,
		"reactions": map[string]any{"total_count": 0, "eyes": 0}}
}

func runnerStatusCommentJSON(id int64, now time.Time) map[string]any {
	return map[string]any{"id": id, "node_id": "status", "html_url": "https://issues.test/owner/repo/issues/17#issuecomment-900",
		"url":       "https://issues.test/api/v3/repos/owner/repo/issues/comments/900",
		"issue_url": "https://issues.test/api/v3/repos/owner/repo/issues/17", "body": "status",
		"user": map[string]any{"id": 1, "node_id": "runner", "login": "runner"}, "created_at": now, "updated_at": now}
}

func runnerNodeID(kind string, id uuid.UUID) string {
	return base64.RawStdEncoding.EncodeToString([]byte(kind + ":" + id.String()))
}

func apiURLPlaceholder(r *http.Request) string { return "http://" + r.Host }

func writeRunnerJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func mustLoadRunnerState(t *testing.T, store state.StateStore) state.RunnerState {
	t.Helper()
	current, err := store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return current
}

type compositionWorkspace struct {
	root                string
	cloneCredentialUsed atomic.Bool
}

func (w *compositionWorkspace) PrepareNew(ctx context.Context, request workspace.NewRequest) (workspace.Binding, error) {
	if request.Credentials == nil {
		return workspace.Binding{}, errors.New("clone credential missing")
	}
	credential, err := request.Credentials.BeforeGit(ctx, "clone", request.CloneURL)
	if err != nil {
		return workspace.Binding{}, err
	}
	for _, entry := range credential.Env {
		if strings.Contains(entry, "profile-pat") || strings.Contains(entry, "iss_dgt") || strings.Contains(entry, "git-child-secret") {
			return workspace.Binding{}, errors.New("plaintext credential entered git environment")
		}
	}
	w.cloneCredentialUsed.Store(true)
	if credential.Cleanup == nil {
		return workspace.Binding{}, errors.New("clone credential cleanup missing")
	}
	if err := credential.Cleanup(); err != nil {
		return workspace.Binding{}, err
	}
	path := filepath.Join(w.root, "workspace-e2e")
	if err := os.MkdirAll(path, 0o700); err != nil {
		return workspace.Binding{}, err
	}
	metadata := state.WorkspaceMetadata{ID: "workspace-e2e", Path: path, Repo: request.Repo, CloneURL: request.CloneURL,
		Branch: "issue-spec-e2e", Ref: request.Ref, CheckoutSHA: strings.Repeat("a", 40), CreatedAt: time.Now().UTC(),
		LastUsedAt: time.Now().UTC(), CleanupAfter: time.Now().UTC().Add(time.Hour), RepositoryBinding: request.RepositoryBinding}
	return workspace.Binding{Workspace: metadata, AcpxWorkingDirectory: path, SandboxWorkspacePath: path}, nil
}

func (*compositionWorkspace) ResolveResume(context.Context, workspace.ResumeRequest) (workspace.Binding, error) {
	return workspace.Binding{}, errors.New("unexpected resume")
}

func (*compositionWorkspace) AcquireLock(_ context.Context, request workspace.LockRequest) (state.SessionLock, error) {
	return state.SessionLock{OwnerJobID: request.JobID, WorkspaceLockToken: "lock-e2e",
		WorkspaceLockPath: filepath.Join(os.TempDir(), "runner-composition.lock"), AcquiredAt: time.Now().UTC()}, nil
}

func (*compositionWorkspace) ReleaseLock(state.SessionLock) error { return nil }

type compositionSandbox struct {
	authenticated atomic.Bool
	mu            sync.Mutex
	requests      []jobs.SandboxRequest
}

func (s *compositionSandbox) Prepare(_ context.Context, request jobs.SandboxRequest) (jobs.ExecutionEnvironment, error) {
	s.mu.Lock()
	s.requests = append(s.requests, request)
	s.mu.Unlock()
	if request.ChildProfile == nil || len(request.FileCapabilities) != 4 {
		return jobs.ExecutionEnvironment{}, errors.New("profile child credential or file capabilities missing")
	}
	profileCredentialFound := false
	for _, capability := range request.FileCapabilities {
		data, err := os.ReadFile(capability.Source)
		if err != nil {
			return jobs.ExecutionEnvironment{}, err
		}
		if capability.EnvName == "ISSUE_SPEC_TOKEN_FILE" {
			profileCredentialFound = string(data) == "profile-pat\n"
		}
	}
	if !profileCredentialFound {
		return jobs.ExecutionEnvironment{}, errors.New("stable profile PAT capability missing")
	}
	return jobs.ExecutionEnvironment{WorkingDirectory: request.AcpxWorkingDirectory, AcpxBinary: request.AcpxBinary,
		Sandbox: state.SandboxMetadata{Enabled: false, UnsafeNoSandbox: true, SandboxProvider: "unsafe-explicit-test", FSBoundary: "workspace"},
		Runner:  compositionAuthRunner{authenticated: &s.authenticated}}, nil
}

type compositionAuthRunner struct{ authenticated *atomic.Bool }

func (r compositionAuthRunner) Run(_ context.Context, command acpx.Command) (acpx.CommandResult, error) {
	if command.Binary != "issue-spec-e2e" || len(command.Args) != 3 || command.Args[0] != "auth" || command.Args[1] != "status" || command.Args[2] != "--json" {
		return acpx.CommandResult{ExitCode: 1}, errors.New("unexpected child auth command")
	}
	if r.authenticated != nil {
		r.authenticated.Store(true)
	}
	return acpx.CommandResult{Stdout: []byte(`{"ok":true,"host":"127.0.0.1","source":"env:ISSUE_SPEC_TOKEN_FILE","auth":{"host":"127.0.0.1","source":"env:ISSUE_SPEC_TOKEN_FILE","user":"runner"},"backend":{"name":"rest","selection_source":"profile","token_source":"env:ISSUE_SPEC_TOKEN_FILE"}}`), ExitCode: 0}, nil
}

type compositionAcpxFactory struct{}

func (compositionAcpxFactory) NewCoordinator(jobs.ExecutionEnvironment) (jobs.Coordinator, error) {
	return compositionCoordinator{}, nil
}

type compositionCoordinator struct{}

func (compositionCoordinator) NewSession(_ context.Context, request acpx.NewSessionRequest) (acpx.DispatchResult, error) {
	return acpx.DispatchResult{PublicSessionID: request.PublicSessionID,
		Metadata: acpx.Metadata{StableRecordID: "record-e2e", TrueSessionID: "true-e2e", ProviderSessionID: "provider-e2e", LastTurnID: "turn-e2e"},
		Output:   acpx.TurnOutput{ReplyText: "completed", SummaryFound: true, Summary: runnercontext.CoordinatorSummary{Status: "completed"}}}, nil
}

func (compositionCoordinator) Resume(context.Context, acpx.ResumeRequest) (acpx.DispatchResult, error) {
	return acpx.DispatchResult{}, errors.New("unexpected resume")
}

var _ jobs.WorkspaceManager = (*compositionWorkspace)(nil)
var _ jobs.SandboxPreparer = (*compositionSandbox)(nil)
var _ jobs.AcpxFactory = compositionAcpxFactory{}
