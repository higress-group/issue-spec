package credentials

import (
	"context"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/capability"
	"github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/server/auth/delegation"
)

func TestMaterializerPrivateAtomicRotationAndRevoke(t *testing.T) {
	root := filepath.Join(t.TempDir(), "credentials")
	m := Materializer{Root: root}
	lease, err := m.WriteIssueToken("job/../../escape", "dgt_first")
	if err != nil {
		t.Fatal(err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(lease.HostPath, canonicalRoot+string(os.PathSeparator)) || lease.SandboxPath != IssueTokenSandboxPath {
		t.Fatalf("lease = %+v", lease)
	}
	assertMode(t, root, 0o700)
	assertMode(t, filepath.Dir(lease.HostPath), 0o700)
	assertMode(t, lease.HostPath, 0o600)
	info, _ := os.Stat(lease.HostPath)
	if !singleLink(info) {
		t.Fatal("credential is not single-linked")
	}
	if data, _ := os.ReadFile(lease.HostPath); string(data) != "dgt_first\n" {
		t.Fatalf("token file = %q", data)
	}
	rotated, err := m.WriteIssueToken("job/../../escape", "dgt_second")
	if err != nil {
		t.Fatal(err)
	}
	if rotated.HostPath != lease.HostPath {
		t.Fatalf("rotation path changed: %s != %s", rotated.HostPath, lease.HostPath)
	}
	if data, _ := os.ReadFile(rotated.HostPath); string(data) != "dgt_second\n" {
		t.Fatalf("rotated token file = %q", data)
	}
	if err := m.Revoke("job/../../escape"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lease.HostPath); !os.IsNotExist(err) {
		t.Fatalf("credential remains after revoke: %v", err)
	}
}

func TestMaterializerProfileTokenKeepsStablePathOutsideJobCleanup(t *testing.T) {
	root := filepath.Join(t.TempDir(), "credentials")
	materializer := Materializer{Root: root}
	profile, err := materializer.WriteProfileToken("iss_pat_profile")
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := materializer.WriteProfileToken("iss_pat_rotated")
	if err != nil {
		t.Fatal(err)
	}
	if rotated.HostPath != profile.HostPath || rotated.SandboxPath != IssueTokenSandboxPath {
		t.Fatalf("rotated profile token = %+v, initial = %+v", rotated, profile)
	}
	assertMode(t, profile.HostPath, 0o600)
	if data, readErr := os.ReadFile(profile.HostPath); readErr != nil || string(data) != "iss_pat_rotated\n" {
		t.Fatalf("profile token = %q, %v", data, readErr)
	}
	if err := materializer.Revoke("job-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(profile.HostPath); err != nil {
		t.Fatalf("profile token removed by job cleanup: %v", err)
	}
}

func TestMaterializerRejectsSymlinkRootAndHardlink(t *testing.T) {
	base := t.TempDir()
	realRoot := filepath.Join(base, "real")
	if err := os.Mkdir(realRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(realRoot, link); err != nil {
		t.Fatal(err)
	}
	if _, err := (Materializer{Root: link}).WriteIssueToken("job", "dgt"); err == nil {
		t.Fatal("symlink root accepted")
	}
	file := filepath.Join(realRoot, "secret")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(file, filepath.Join(realRoot, "secret-link")); err != nil {
		t.Fatal(err)
	}
	if err := verifyRegularPrivateFile(file); err == nil {
		t.Fatal("hard-linked credential accepted")
	}
}

func TestMaterializerRejectsTokenControlCharacters(t *testing.T) {
	m := Materializer{Root: t.TempDir()}
	for _, token := range []string{"", "dgt_one\ndgt_two", "dgt_null\x00suffix", "dgt_tab\tvalue"} {
		if _, err := m.WriteIssueToken("job", token); err == nil {
			t.Fatalf("invalid token accepted: %q", token)
		}
	}
}

func TestGitLeasePinsHTTPSAndKeepsSecretOutOfEnv(t *testing.T) {
	binding := testBinding()
	revoked := 0
	provider := staticGitProvider{lease: GitProviderLease{Credential: GitSecret{Username: "runner", Password: "super-secret"},
		ExpiresAt: time.Now().Add(time.Minute), Revoke: func(context.Context) error { revoked++; return nil }}}
	lease, err := NewGitLease(context.Background(), t.TempDir(), "job-1", capability.OperationGitClone, binding, provider)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := lease.BeforeGit(context.Background(), "clone", binding.CloneURL)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(credential.Env, "\n")
	if strings.Contains(joined, "super-secret") || strings.Contains(joined, "https://runner") {
		t.Fatalf("secret leaked into git env: %s", joined)
	}
	for _, required := range []string{"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_VALUE_0=false"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("missing hardened git environment %q: %s", required, joined)
		}
	}
	if _, err := lease.BeforeGit(context.Background(), "fetch", binding.CloneURL); err == nil {
		t.Fatal("non-clone credential request accepted")
	}
	if _, err := lease.BeforeGit(context.Background(), "clone", "https://code.example/acme/other.git"); err == nil {
		t.Fatal("clone URL drift accepted")
	}
	if err := lease.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lease.SecretPath); !os.IsNotExist(err) {
		t.Fatalf("git secret remains: %v", err)
	}
	if revoked != 1 {
		t.Fatalf("provider revoke calls = %d", revoked)
	}
}

func TestGitLeaseRejectsSSHAndRepositoryPathDrift(t *testing.T) {
	binding := testBinding()
	provider := staticGitProvider{lease: GitProviderLease{Credential: GitSecret{Username: "u", Password: "p"},
		ExpiresAt: time.Now().Add(time.Minute), Revoke: func(context.Context) error { return nil }}}
	binding.CloneURL = "git@code.example:acme/widgets.git"
	if _, err := NewGitLease(context.Background(), t.TempDir(), "job-1", capability.OperationGitClone, binding, provider); err == nil {
		t.Fatal("SSH binding accepted without controlled SSH lease")
	}
	binding = testBinding()
	binding.CloneURL = "https://code.example/acme/other.git"
	if _, err := NewGitLease(context.Background(), t.TempDir(), "job-1", capability.OperationGitClone, binding, provider); err == nil {
		t.Fatal("repository path drift accepted")
	}
}

func TestHostSSHGitProviderDerivesTransportWithoutChangingBinding(t *testing.T) {
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	if err := os.Mkdir(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(t.TempDir(), "agent.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Skipf("Unix sockets unavailable: %v", err)
	}
	defer listener.Close()
	provider, err := NewHostSSHGitProvider(HostSSHGitProviderConfig{SSHDir: sshDir, AgentSocket: socketPath})
	if err != nil {
		t.Fatal(err)
	}
	binding := testBinding()
	original := binding.CloneURL
	lease, err := NewGitLease(context.Background(), t.TempDir(), "job-ssh", capability.OperationGitClone, binding, provider)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := lease.BeforeGit(context.Background(), "clone", original)
	if err != nil {
		t.Fatal(err)
	}
	if credential.CloneURL != "git@code.example:acme/widgets.git" || binding.CloneURL != original {
		t.Fatalf("credential=%+v binding=%+v", credential, binding)
	}
	joined := strings.Join(credential.Env, "\n")
	for _, want := range []string{"HOME=" + home, "SSH_AUTH_SOCK=" + socketPath, "GIT_TERMINAL_PROMPT=0"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("host SSH git environment missing %q: %s", want, joined)
		}
	}
	if lease.AskPassPath != "" || lease.SecretPath != "" {
		t.Fatalf("host SSH lease materialized HTTPS secret files: %+v", lease)
	}
	if err := lease.Cleanup(); err != nil {
		t.Fatal(err)
	}
}

func TestHostSSHGitProviderRejectsInvalidHostPathsAndBinding(t *testing.T) {
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	if err := os.Mkdir(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := NewHostSSHGitProvider(HostSSHGitProviderConfig{SSHDir: "relative/.ssh"}); err == nil {
		t.Fatal("relative host SSH directory accepted")
	}
	link := filepath.Join(t.TempDir(), ".ssh")
	if err := os.Symlink(sshDir, link); err != nil {
		t.Fatal(err)
	}
	if _, err := NewHostSSHGitProvider(HostSSHGitProviderConfig{SSHDir: link}); err == nil {
		t.Fatal("symlink host SSH directory accepted")
	}
	provider, err := NewHostSSHGitProvider(HostSSHGitProviderConfig{SSHDir: sshDir})
	if err != nil {
		t.Fatal(err)
	}
	binding := testBinding()
	binding.CloneURL = "https://code.example:8443/acme/widgets.git"
	if _, err := provider.Acquire(context.Background(), GitRequest{JobID: "job-ssh", Purpose: "git.clone", Binding: binding}); err == nil {
		t.Fatal("host SSH binding with non-canonical port accepted")
	}
}

func TestHostSSHLeaseExposesOnlyIssueTokenFileCapability(t *testing.T) {
	lease := &Lease{IssueToken: FileLease{HostPath: "/private/issue.token", SandboxPath: IssueTokenSandboxPath},
		Git: &GitLease{HostSSH: true}}
	capabilities := lease.FileCapabilities()
	if len(capabilities) != 1 || capabilities[0].EnvName != "ISSUE_SPEC_TOKEN_FILE" {
		t.Fatalf("host SSH file capabilities = %+v", capabilities)
	}
}

func TestCurrentUserHostSSHConfigIgnoresHOMEOverride(t *testing.T) {
	account, err := user.Current()
	if err != nil {
		t.Skipf("current OS account unavailable: %v", err)
	}
	t.Setenv("HOME", filepath.Join(t.TempDir(), "service-home"))
	config, err := CurrentUserHostSSHGitProviderConfig("/run/host-agent.sock")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(filepath.Clean(account.HomeDir), ".ssh")
	if config.SSHDir != want || config.AgentSocket != "/run/host-agent.sock" {
		t.Fatalf("config = %+v, want SSHDir=%q from passwd account", config, want)
	}
}

func TestHostSSHCloneDisablesHostGlobalGitURLRewrite(t *testing.T) {
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	if err := os.Mkdir(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	gitConfig := "[url \"https://attacker.invalid/redirect/\"]\n\tinsteadOf = git@code.example:\n"
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte(gitConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	provider, err := NewHostSSHGitProvider(HostSSHGitProviderConfig{SSHDir: sshDir})
	if err != nil {
		t.Fatal(err)
	}
	binding := testBinding()
	lease, err := NewGitLease(context.Background(), t.TempDir(), "job-host-config", capability.OperationGitClone, binding, provider)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := lease.BeforeGit(context.Background(), "clone", binding.CloneURL)
	if err != nil {
		t.Fatal(err)
	}
	resolveURL := func(env []string) string {
		t.Helper()
		command := exec.Command("git", "ls-remote", "--get-url", credential.CloneURL)
		command.Env = env
		output, commandErr := command.Output()
		if commandErr != nil {
			t.Fatalf("git ls-remote --get-url: %v", commandErr)
		}
		return strings.TrimSpace(string(output))
	}
	controlEnv := safeGitEnvironment(os.Environ())
	controlEnv = append(controlEnv, "HOME="+home, "GIT_CONFIG_NOSYSTEM=1")
	if got := resolveURL(controlEnv); got != "https://attacker.invalid/redirect/acme/widgets.git" {
		t.Fatalf("hostile control config was not active: %q", got)
	}
	if got := resolveURL(credential.Env); got != credential.CloneURL {
		t.Fatalf("host global config redirected canonical clone URL: %q", got)
	}
	if err := lease.Cleanup(); err != nil {
		t.Fatal(err)
	}
}

func TestGitLeaseRejectsLongLivedProviderLeaseWithBoundedCleanup(t *testing.T) {
	binding := testBinding()
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()
	var cleanupHadDeadline, cleanupWasCancelled bool
	provider := staticGitProvider{lease: GitProviderLease{Credential: GitSecret{Username: "u", Password: "p"},
		ExpiresAt: time.Now().Add(delegation.MaxTTL + time.Minute), Revoke: func(ctx context.Context) error {
			_, cleanupHadDeadline = ctx.Deadline()
			cleanupWasCancelled = ctx.Err() != nil
			return nil
		}}}
	if _, err := NewGitLease(requestCtx, t.TempDir(), "job-expiry", capability.OperationGitClone, binding, provider); err == nil {
		t.Fatal("long-lived provider lease accepted")
	}
	if !cleanupHadDeadline || cleanupWasCancelled {
		t.Fatalf("cleanup context deadline=%t cancelled=%t", cleanupHadDeadline, cleanupWasCancelled)
	}
}

func TestOperatorGitProviderMatchesExactAuthorityIncludingPort(t *testing.T) {
	binding := testBinding()
	binding.CloneURL = "https://code.example:8443/acme/widgets.git"
	called := false
	provider := OperatorGitProvider{ProviderKey: "github", Host: "code.example", ExternalRepositoryID: "acme/widgets",
		AcquireLease: func(context.Context, GitRequest) (GitProviderLease, error) {
			called = true
			return GitProviderLease{}, nil
		}, RevokeJobLease: func(context.Context, string) error { return nil }}
	if _, err := provider.Acquire(context.Background(), GitRequest{JobID: "job-port", Purpose: "git.clone", Binding: binding}); err == nil || called {
		t.Fatalf("port drift accepted: err=%v called=%t", err, called)
	}
	provider.Host = "CODE.EXAMPLE:8443"
	if _, err := provider.Acquire(context.Background(), GitRequest{JobID: "job-port", Purpose: "git.clone", Binding: binding}); err != nil || !called {
		t.Fatalf("exact authority rejected: err=%v called=%t", err, called)
	}
}

type staticGitProvider struct{ lease GitProviderLease }

func (s staticGitProvider) Acquire(context.Context, GitRequest) (GitProviderLease, error) {
	return s.lease, nil
}

func (staticGitProvider) RevokeJob(context.Context, string) error { return nil }

func (staticGitProvider) SupportsPurpose(purpose capability.Operation) bool {
	return purpose == capability.OperationGitClone || purpose == capability.OperationGitPush
}

func testBinding() state.RepositoryBindingSnapshot {
	return state.RepositoryBindingSnapshot{Source: "self-hosted", IssueRepositoryKey: "acme/widgets", BindingID: uuid.NewString(), Version: 1,
		ProviderKey: "github", ExternalRepositoryID: "acme/widgets", CloneURL: "https://code.example/acme/widgets.git",
		WebURL: "https://code.example/acme/widgets", DefaultBranch: "main"}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %o, want %o", path, got, want)
	}
}
