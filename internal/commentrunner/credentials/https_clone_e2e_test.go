package credentials

import (
	"bytes"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	clientauth "github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/higress-group/issue-spec/internal/workspace"
)

func TestBrokerCommandProviderClonesRealHTTPSRepositoryAndRevokesEveryLease(t *testing.T) {
	gitBinary, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git executable unavailable")
	}
	projectRoot := t.TempDir()
	prepareHTTPSGitRepository(t, gitBinary, projectRoot)

	var exchanges, remoteRevokes, authenticatedGitRequests atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/delegated-tokens/exchange"):
			if r.Header.Get("Authorization") != "Bearer origin-bound-parent" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			exchanges.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": uuid.New(), "token": delegatedTestToken("https-child"),
				"expires_at": time.Now().UTC().Add(time.Minute)})
		case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/delegated-tokens"):
			if r.Header.Get("Authorization") != "Bearer origin-bound-parent" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			remoteRevokes.Add(1)
			w.WriteHeader(http.StatusNoContent)
		case strings.HasPrefix(r.URL.Path, "/org/repo.git/"):
			username, password, ok := r.BasicAuth()
			if !ok {
				w.Header().Set("WWW-Authenticate", `Basic realm="runner-git"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if username != "runner" || password != "short-lived-secret" {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			authenticatedGitRequests.Add(1)
			serveGitHTTPBackend(w, r, gitBinary, projectRoot)
		default:
			http.NotFound(w, r)
		}
	})
	server := httptest.NewTLSServer(handler)
	defer server.Close()

	caFile := filepath.Join(t.TempDir(), "git-ca.pem")
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(caFile, certificate, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_SSL_CAINFO", caFile)
	for _, name := range []string{"HTTPS_PROXY", "HTTP_PROXY", "https_proxy", "http_proxy"} {
		t.Setenv(name, "")
	}
	t.Setenv("NO_PROXY", "127.0.0.1,localhost")
	t.Setenv("ISSUE_SPEC_TOKEN", "host-token-must-not-reach-provider")
	t.Setenv("GH_TOKEN", "host-gh-token-must-not-reach-provider")

	auditPath := filepath.Join(t.TempDir(), "provider-audit.log")
	provider := commandGitProviderForTestArgs(t, "audit", 1<<20, auditPath)
	binding := state.RepositoryBindingSnapshot{Source: "server", IssueRepositoryKey: "owner/repo",
		BindingID: uuid.NewString(), Version: 3, ProviderKey: "test-git", ExternalRepositoryID: "org/repo",
		CloneURL: server.URL + "/org/repo.git", WebURL: server.URL + "/org/repo", DefaultBranch: "main"}
	repoScope := models.RepoScope{OrgID: uuid.New(), RepoID: uuid.New()}
	profile := clientauth.Profile{Name: "runner-e2e", Kind: clientauth.ProfileKindHosted, Hostname: "127.0.0.1",
		APIURL: server.URL + "/api/v3", NativeAPIURL: server.URL + "/api/v1", WebURL: server.URL,
		ServerInstanceID: "instance-e2e"}
	credentialRoot := filepath.Join(t.TempDir(), "credentials")
	broker := &Broker{Profile: profile, Audience: profile.ServerInstanceID, Subject: "runner-bot",
		ParentToken: "origin-bound-parent", HTTPClient: server.Client(), Materializer: Materializer{Root: credentialRoot},
		GitProvider: provider, TTL: time.Minute}
	lease, err := broker.Acquire(t.Context(), AcquireRequest{Repo: repoScope, JobID: "job-real-https", Binding: binding})
	if err != nil {
		t.Fatal(err)
	}
	issueTokenPath := lease.IssueToken.HostPath
	manager := workspace.Manager{Root: filepath.Join(t.TempDir(), "workspaces"), Retention: time.Hour, GitBinary: gitBinary}
	prepared, err := manager.PrepareNew(t.Context(), workspace.NewRequest{Repo: "owner/repo", CloneURL: binding.CloneURL,
		DefaultBranch: binding.DefaultBranch, Ref: binding.DefaultBranch, PublicSessionID: "session-e2e", JobID: "job-real-https",
		WorkspaceID: "workspace-e2e", RepositoryBinding: binding, Credentials: lease.Git})
	if err != nil {
		t.Fatal(err)
	}
	readme, err := os.ReadFile(filepath.Join(prepared.Workspace.Path, "README.md"))
	if err != nil || string(readme) != "runner https clone\n" {
		t.Fatalf("cloned README=%q err=%v", readme, err)
	}
	if err := lease.PrepareChildGit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(lease.FileCapabilities()) != 4 {
		t.Fatalf("child capabilities=%+v", lease.FileCapabilities())
	}
	for name, value := range lease.ChildEnv() {
		if strings.Contains(value, "origin-bound-parent") || strings.Contains(value, "host-token") {
			t.Fatalf("host credential leaked through child env %s=%q", name, value)
		}
	}
	if err := lease.Revoke(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := lease.Revoke(t.Context()); err != nil {
		t.Fatalf("idempotent revoke: %v", err)
	}
	if _, err := os.Stat(issueTokenPath); !os.IsNotExist(err) {
		t.Fatalf("delegated token file survived revoke: %v", err)
	}
	if exchanges.Load() != 1 || remoteRevokes.Load() != 1 || authenticatedGitRequests.Load() == 0 {
		t.Fatalf("exchange=%d remote_revoke=%d authenticated_git=%d", exchanges.Load(), remoteRevokes.Load(), authenticatedGitRequests.Load())
	}
	audit, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "acquire job-real-https\nrevoke_lease job-real-https\nacquire job-real-https\nrevoke_lease job-real-https\nrevoke_job job-real-https\n"
	if string(audit) != want {
		t.Fatalf("provider lifecycle:\n%s\nwant:\n%s", audit, want)
	}
}

func prepareHTTPSGitRepository(t *testing.T, gitBinary, projectRoot string) {
	t.Helper()
	bare := filepath.Join(projectRoot, "org", "repo.git")
	if err := os.MkdirAll(filepath.Dir(bare), 0o700); err != nil {
		t.Fatal(err)
	}
	runGitE2E(t, gitBinary, "", "init", "--bare", bare)
	seed := t.TempDir()
	runGitE2E(t, gitBinary, seed, "init")
	runGitE2E(t, gitBinary, seed, "config", "user.name", "Runner E2E")
	runGitE2E(t, gitBinary, seed, "config", "user.email", "runner-e2e@example.test")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("runner https clone\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitE2E(t, gitBinary, seed, "add", "README.md")
	runGitE2E(t, gitBinary, seed, "commit", "-m", "seed")
	runGitE2E(t, gitBinary, seed, "branch", "-M", "main")
	runGitE2E(t, gitBinary, seed, "remote", "add", "origin", bare)
	runGitE2E(t, gitBinary, seed, "push", "origin", "main")
	runGitE2E(t, gitBinary, "", "--git-dir", bare, "symbolic-ref", "HEAD", "refs/heads/main")
}

func runGitE2E(t *testing.T, gitBinary, directory string, args ...string) {
	t.Helper()
	command := exec.Command(gitBinary, args...)
	command.Dir = directory
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func serveGitHTTPBackend(w http.ResponseWriter, r *http.Request, gitBinary, projectRoot string) {
	command := exec.CommandContext(r.Context(), gitBinary, "http-backend")
	command.Env = append(os.Environ(),
		"GIT_PROJECT_ROOT="+projectRoot,
		"GIT_HTTP_EXPORT_ALL=1",
		"PATH_INFO="+r.URL.Path,
		"QUERY_STRING="+r.URL.RawQuery,
		"REQUEST_METHOD="+r.Method,
		"CONTENT_TYPE="+r.Header.Get("Content-Type"),
		"CONTENT_LENGTH="+strconv.FormatInt(r.ContentLength, 10),
		"REMOTE_USER=runner",
		"SERVER_PROTOCOL="+r.Proto,
	)
	command.Stdin = r.Body
	output, err := command.Output()
	if err != nil {
		http.Error(w, "git backend failed", http.StatusBadGateway)
		return
	}
	headerEnd, separatorLength := bytes.Index(output, []byte("\r\n\r\n")), 4
	if headerEnd < 0 {
		headerEnd, separatorLength = bytes.Index(output, []byte("\n\n")), 2
	}
	if headerEnd < 0 {
		http.Error(w, "git backend malformed response", http.StatusBadGateway)
		return
	}
	status := http.StatusOK
	for _, rawLine := range strings.Split(strings.ReplaceAll(string(output[:headerEnd]), "\r\n", "\n"), "\n") {
		name, value, ok := strings.Cut(rawLine, ":")
		if !ok {
			continue
		}
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		if strings.EqualFold(name, "Status") {
			fields := strings.Fields(value)
			if len(fields) > 0 {
				if parsed, err := strconv.Atoi(fields[0]); err == nil {
					status = parsed
				}
			}
			continue
		}
		w.Header().Add(name, value)
	}
	w.WriteHeader(status)
	_, _ = io.Copy(w, bytes.NewReader(output[headerEnd+separatorLength:]))
}
