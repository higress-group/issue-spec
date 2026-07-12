package commands

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/codereview"
	"github.com/higress-group/issue-spec/internal/model"
)

// This test deliberately enters through Execute twice. It proves production
// newApp wiring resolves both change.comment and change.create from operator
// configuration; no app test field is replaced.
func TestExecuteLoadsOperatorProviderForCommentAndCreate(t *testing.T) {
	clearCommandAuthEnv(t)
	fixture := newOperatorCLIFixture(t)
	profile := auth.Profile{Name: "operator-e2e", Kind: auth.ProfileKindHosted,
		APIURL: fixture.server.URL + "/api/v3", NativeAPIURL: fixture.server.URL + "/api/v1",
		WebURL: fixture.server.URL, ServerInstanceID: "operator-e2e-instance"}
	if err := auth.SaveProfile(profile, true); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.StoreProfileToken(t.Context(), profile, "realm-token", true); err != nil {
		t.Fatal(err)
	}
	providerConfig := filepath.Join(t.TempDir(), "providers.json")
	rawConfig, _ := json.Marshal(map[string]any{"version": 1, "providers": map[string]any{
		"code.example": map[string]any{"path": os.Args[0], "args": []string{"-test.run=^TestOperatorBridgeCLIHelper$"},
			"environment": []string{"ISSUE_SPEC_OPERATOR_BRIDGE_HELPER=1", "ISSUE_SPEC_OPERATOR_BRIDGE_LOG=" + fixture.bridgeLog},
			"timeout":     "10s", "max_output_bytes": 1 << 20},
	}})
	if err := os.WriteFile(providerConfig, rawConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(codereview.OperatorProvidersFileEnv, providerConfig)

	var out, errOut bytes.Buffer
	commentCode := Execute([]string{"--profile", "operator-e2e", "review", "finding", "--repo", "acme/widgets",
		"--implement", "9", "--path", "internal/example.go", "--line", "12", "--id", "FINDING-101",
		"--severity", "P2", "--process", "PROCESS-101", "--spec", "SPEC-007",
		"--spec-url", "https://issues.example/spec-7", "--body", "Review this neutral change.", "--json"},
		strings.NewReader(""), &out, &errOut)
	if commentCode != 0 {
		t.Fatalf("review finding exit=%d stdout=%q stderr=%q", commentCode, out.String(), errOut.String())
	}

	repo := initOperatorGitRepository(t)
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
	out.Reset()
	errOut.Reset()
	createCode := Execute([]string{"--profile", "operator-e2e", "archive", "durable-spec", "--repo", "acme/widgets",
		"--proposal", "1", "--implement", "9", "--capability", "operator-provider-e2e", "--create-pr",
		"--branch", "issue-spec/operator-provider-e2e", "--json"}, strings.NewReader(""), &out, &errOut)
	if createCode != 0 {
		t.Fatalf("archive create exit=%d stdout=%q stderr=%q", createCode, out.String(), errOut.String())
	}
	log, err := os.ReadFile(fixture.bridgeLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "mutate comment change-42") || !strings.Contains(string(log), "mutate create_change archive-77") {
		t.Fatalf("operator bridge log = %q", log)
	}
	if !fixture.archiveUpserted.Load() {
		t.Fatal("create_change succeeded without native archive_change upsert")
	}
}

func TestOperatorBridgeCLIHelper(t *testing.T) {
	if os.Getenv("ISSUE_SPEC_OPERATOR_BRIDGE_HELPER") != "1" {
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
	response := map[string]any{"protocol": request.Protocol, "request_id": request.RequestID}
	switch request.Action {
	case "capabilities":
		response["capabilities"] = codereview.Capabilities{ProtocolVersion: codereview.ProtocolVersion,
			Values: []codereview.Capability{codereview.CapabilityEvidenceSnapshot,
				codereview.CapabilityChangeComment, codereview.CapabilityChangeCreate}}
	case "mutate":
		var mutation codereview.MutationRequest
		if err := json.Unmarshal(request.Payload, &mutation); err != nil {
			os.Exit(3)
		}
		if mutation.Kind == codereview.MutationCreateChange {
			mutation.Reference.ChangeID = "archive-77"
		}
		response["mutation"] = codereview.MutationResult{Reference: mutation.Reference,
			CanonicalURL: "https://code.example/changes/" + mutation.Reference.ChangeID,
			ExternalID:   "external-" + string(mutation.Kind)}
		file, err := os.OpenFile(os.Getenv("ISSUE_SPEC_OPERATOR_BRIDGE_LOG"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			os.Exit(4)
		}
		_, _ = fmt.Fprintf(file, "mutate %s %s\n", mutation.Kind, mutation.Reference.ChangeID)
		_ = file.Close()
	default:
		os.Exit(5)
	}
	_ = json.NewEncoder(os.Stdout).Encode(response)
	os.Exit(0)
}

type operatorCLIFixture struct {
	t               *testing.T
	server          *httptest.Server
	orgID           uuid.UUID
	repoID          uuid.UUID
	issueID         uuid.UUID
	bridgeLog       string
	archiveUpserted atomic.Bool
	specBody        string
}

func newOperatorCLIFixture(t *testing.T) *operatorCLIFixture {
	t.Helper()
	specBody, err := model.EnsureTypedBody("SPEC", "SPEC-007", "## Requirement: operator registry\n\nThe CLI MUST load trusted operator providers.\n\n### Scenario: registered\n\n- **WHEN** a provider is configured\n- **THEN** mutations succeed\n",
		model.BodyOptions{Agent: "Coordinator", Status: "confirmed", Scope: "operator providers"})
	if err != nil {
		t.Fatal(err)
	}
	f := &operatorCLIFixture{t: t, orgID: uuid.New(), repoID: uuid.New(), issueID: uuid.New(),
		bridgeLog: filepath.Join(t.TempDir(), "bridge.log"), specBody: specBody}
	f.server = httptest.NewServer(http.HandlerFunc(f.serveHTTP))
	t.Cleanup(f.server.Close)
	return f
}

func (f *operatorCLIFixture) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer realm-token" {
		f.t.Errorf("authorization = %q", r.Header.Get("Authorization"))
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Request-ID", "operator-cli-test")
	now := time.Now().UTC()
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/context":
		writeOperatorJSON(w, map[string]any{"user": map[string]any{}, "credential": map[string]any{}, "allowed_actions": []string{},
			"organizations": []any{map[string]any{"id": f.orgID, "name": "acme", "display_name": "Acme",
				"effective_permission": "maintain", "container_only": false, "allowed_actions": []string{"organization.read"}}}})
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/context/orgs/"+f.orgID.String()+"/repos":
		writeOperatorJSON(w, map[string]any{"repositories": []any{map[string]any{"repository": map[string]any{
			"id": f.repoID, "organization_id": f.orgID, "name": "widgets", "display_name": "Widgets",
			"visibility": "private", "contribution_policy": "members"}, "effective_permission": "maintain", "allowed_actions": []string{"read"}}}})
	case r.Method == http.MethodGet && r.URL.Path == "/api/v3/repos/acme/widgets/issues/9":
		nodeID := base64.RawStdEncoding.EncodeToString([]byte("Issue:" + f.issueID.String()))
		writeOperatorJSON(w, map[string]any{"node_id": nodeID})
	case r.Method == http.MethodGet && r.URL.Path == "/api/v3/repos/acme/widgets/issues/1":
		writeOperatorJSON(w, map[string]any{"id": 1, "number": 1, "html_url": f.server.URL + "/acme/widgets/issues/1",
			"url": f.server.URL + "/api/v3/repos/acme/widgets/issues/1", "title": "Proposal", "body": "proposal", "state": "open"})
	case r.Method == http.MethodGet && r.URL.Path == "/api/v3/repos/acme/widgets/issues/1/comments":
		writeOperatorJSON(w, []any{map[string]any{"id": 7, "html_url": f.server.URL + "/acme/widgets/issues/1#issuecomment-7",
			"url": f.server.URL + "/api/v3/repos/acme/widgets/issues/comments/7", "body": f.specBody}})
	case r.Method == http.MethodGet && r.URL.Path == fmt.Sprintf("/api/v1/orgs/%s/repos/%s/evidence/policy", f.orgID, f.repoID):
		writeOperatorJSON(w, map[string]any{"representation_version": 1, "requirements": []any{}, "created_at": now, "updated_at": now})
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/references"):
		writeOperatorJSON(w, map[string]any{"references": []any{map[string]any{"id": uuid.New(), "issue_id": f.issueID,
			"provider_key": "code.example", "relation_kind": "code_change", "external_repository_id": "acme/widgets-code",
			"external_id": "change-42", "canonical_url": "https://code.example/changes/change-42", "lifecycle_state": "active",
			"visibility": "repository", "metadata": map[string]any{"head_revision": "head-abc", "base_revision": "base-123"},
			"representation_version": 1, "created_at": now, "updated_at": now}}})
	case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/references"):
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			f.t.Fatal(err)
		}
		f.archiveUpserted.Store(true)
		body["id"], body["representation_version"], body["created_at"], body["updated_at"] = uuid.New(), 1, now, now
		writeOperatorJSON(w, body)
	default:
		w.WriteHeader(http.StatusNotFound)
		writeOperatorJSON(w, map[string]any{"status": 404, "path": r.URL.Path})
	}
}

func initOperatorGitRepository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	repo := filepath.Join(root, "repo")
	runOperatorGit(t, root, "init", "--bare", origin)
	runOperatorGit(t, root, "init", "-b", "main", repo)
	runOperatorGit(t, repo, "config", "user.name", "Issue Spec Test")
	runOperatorGit(t, repo, "config", "user.email", "issue-spec-test@example.com")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runOperatorGit(t, repo, "add", "README.md")
	runOperatorGit(t, repo, "commit", "-m", "fixture")
	runOperatorGit(t, repo, "remote", "add", "origin", origin)
	runOperatorGit(t, repo, "push", "-u", "origin", "main")
	return repo
}

func runOperatorGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func writeOperatorJSON(w http.ResponseWriter, value any) { _ = json.NewEncoder(w).Encode(value) }
