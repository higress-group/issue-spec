package credentials

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/higress-group/issue-spec/internal/capability"
	"github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/server/auth/delegation"
	"github.com/higress-group/issue-spec/internal/workspace"
)

const (
	GitAskPassSandboxPath  = "/run/issue-spec/git/askpass"
	GitUsernameSandboxPath = "/run/issue-spec/git/username"
	GitSecretSandboxPath   = "/run/issue-spec/git/secret"
)

type GitSecret struct {
	Username string
	Password string
}

type GitProvider interface {
	Acquire(context.Context, GitRequest) (GitProviderLease, error)
	RevokeJob(context.Context, string) error
}

type GitPurposeProvider interface {
	SupportsPurpose(capability.Operation) bool
}

type GitRequest struct {
	JobID   string
	Purpose string
	Binding state.RepositoryBindingSnapshot
}

type GitProviderLease struct {
	Credential GitSecret
	// CloneURL overrides the binding's credential-free HTTPS URL for the
	// command-local git transport. It is only accepted for a canonical host SSH
	// lease; the persisted repository binding remains unchanged.
	CloneURL  string
	Env       []string
	HostSSH   bool
	ExpiresAt time.Time
	Revoke    func(context.Context) error
}

type GitLease struct {
	Purpose        capability.Operation
	PinnedCloneURL string
	CloneURL       string
	HostSSH        bool
	Env            []string
	AskPassPath    string
	UsernamePath   string
	SecretPath     string
	cleanup        func(context.Context) error
}

func NewGitLease(ctx context.Context, root, jobID string, purpose capability.Operation, binding state.RepositoryBindingSnapshot, provider GitProvider) (*GitLease, error) {
	if provider == nil || !validJobID(jobID) || !binding.Complete() || strings.TrimSpace(root) == "" || !filepath.IsAbs(root) {
		return nil, errors.New("credential broker: complete binding and git provider are required")
	}
	if err := validatePinnedHTTPS(binding); err != nil {
		return nil, err
	}
	if purpose != capability.OperationGitClone && purpose != capability.OperationGitPush {
		return nil, errors.New("credential broker: git purpose must be git.clone or git.push")
	}
	providerLease, err := provider.Acquire(ctx, GitRequest{JobID: jobID, Purpose: string(purpose), Binding: binding})
	if err != nil {
		return nil, err
	}
	revokeProvider := func() {
		if providerLease.Revoke != nil {
			cleanupCtx, cancel := credentialCleanupContext(ctx)
			defer cancel()
			_ = providerLease.Revoke(cleanupCtx)
		}
	}
	if providerLease.HostSSH {
		return newHostSSHGitLease(ctx, purpose, binding, providerLease)
	}
	secret := providerLease.Credential
	now := time.Now().UTC()
	if invalidGitSecret(secret.Username) || invalidGitSecret(secret.Password) || providerLease.Revoke == nil ||
		providerLease.ExpiresAt.IsZero() || !now.Before(providerLease.ExpiresAt) ||
		providerLease.ExpiresAt.After(now.Add(delegation.MaxTTL+credentialClockSkew)) {
		revokeProvider()
		return nil, errors.New("credential broker: git provider returned an empty credential")
	}
	secureRoot, err := (Materializer{Root: root}).secureRoot()
	if err != nil {
		revokeProvider()
		return nil, err
	}
	dir := filepath.Join(secureRoot, "git")
	if err := secureMkdir(dir); err != nil {
		revokeProvider()
		return nil, err
	}
	usernamePath, secretPath, askpassPath := filepath.Join(dir, "username"), filepath.Join(dir, "secret"), filepath.Join(dir, "askpass")
	if err := atomicSecretFile(usernamePath, []byte(secret.Username+"\n")); err != nil {
		revokeProvider()
		return nil, err
	}
	if err := atomicSecretFile(secretPath, []byte(secret.Password+"\n")); err != nil {
		_ = os.Remove(usernamePath)
		revokeProvider()
		return nil, err
	}
	script := "#!/bin/sh\ncase \"$1\" in\n  *Username*) exec /bin/cat \"$ISSUE_SPEC_GIT_USERNAME_FILE\" ;;\n  *Password*) exec /bin/cat \"$ISSUE_SPEC_GIT_SECRET_FILE\" ;;\n  *) exit 1 ;;\nesac\n"
	if err := atomicSecretFile(askpassPath, []byte(script)); err != nil {
		_ = os.Remove(usernamePath)
		_ = os.Remove(secretPath)
		revokeProvider()
		return nil, err
	}
	if err := os.Chmod(askpassPath, 0o500); err != nil {
		_ = os.RemoveAll(dir)
		revokeProvider()
		return nil, err
	}
	for _, privateDir := range []string{filepath.Join(dir, "home"), filepath.Join(dir, "xdg")} {
		if err := secureMkdir(privateDir); err != nil {
			_ = os.RemoveAll(dir)
			revokeProvider()
			return nil, err
		}
	}
	lease := &GitLease{Purpose: purpose, PinnedCloneURL: binding.CloneURL, CloneURL: binding.CloneURL,
		AskPassPath: askpassPath, UsernamePath: usernamePath, SecretPath: secretPath}
	lease.cleanup = func(cleanupCtx context.Context) error {
		var cleanupErr error
		if err := os.RemoveAll(dir); err != nil {
			cleanupErr = err
		}
		if err := providerLease.Revoke(cleanupCtx); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("revoke git provider lease: %w", err))
		}
		return cleanupErr
	}
	return lease, nil
}

func (l *GitLease) BeforeGit(_ context.Context, operation, cloneURL string) (workspace.GitCredential, error) {
	if l == nil || l.Purpose != capability.OperationGitClone || operation != "clone" || strings.TrimSpace(cloneURL) != strings.TrimSpace(l.PinnedCloneURL) {
		return workspace.GitCredential{}, errors.New("credential broker: git credential requested outside pinned clone")
	}
	if l.HostSSH {
		return workspace.GitCredential{CloneURL: l.CloneURL, Env: append([]string(nil), l.Env...), Cleanup: l.Cleanup}, nil
	}
	env := safeGitEnvironment(os.Environ())
	env = append(env,
		"HOME="+filepath.Join(filepath.Dir(l.AskPassPath), "home"),
		"XDG_CONFIG_HOME="+filepath.Join(filepath.Dir(l.AskPassPath), "xdg"),
		"GIT_ASKPASS="+l.AskPassPath,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"ISSUE_SPEC_GIT_USERNAME_FILE="+l.UsernamePath,
		"ISSUE_SPEC_GIT_SECRET_FILE="+l.SecretPath,
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.followRedirects",
		"GIT_CONFIG_VALUE_0=false",
	)
	return workspace.GitCredential{CloneURL: l.CloneURL, Env: env, Cleanup: l.Cleanup}, nil
}

func newHostSSHGitLease(ctx context.Context, purpose capability.Operation, binding state.RepositoryBindingSnapshot, providerLease GitProviderLease) (*GitLease, error) {
	revoke := func() {
		if providerLease.Revoke != nil {
			cleanupCtx, cancel := credentialCleanupContext(ctx)
			defer cancel()
			_ = providerLease.Revoke(cleanupCtx)
		}
	}
	cloneURL, err := canonicalHostSSHCloneURL(binding)
	if err != nil || strings.TrimSpace(providerLease.CloneURL) != cloneURL ||
		providerLease.Credential != (GitSecret{}) || !providerLease.ExpiresAt.IsZero() || providerLease.Revoke == nil {
		revoke()
		return nil, errors.New("credential broker: host SSH provider returned an invalid lease")
	}
	env, err := validateHostSSHGitEnvironment(providerLease.Env)
	if err != nil {
		revoke()
		return nil, err
	}
	lease := &GitLease{Purpose: purpose, PinnedCloneURL: binding.CloneURL, CloneURL: cloneURL, HostSSH: true, Env: env}
	lease.cleanup = func(cleanupCtx context.Context) error { return providerLease.Revoke(cleanupCtx) }
	return lease, nil
}

func validateHostSSHGitEnvironment(entries []string) ([]string, error) {
	values := map[string]string{}
	for _, entry := range entries {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || (name != "HOME" && name != "SSH_AUTH_SOCK") || strings.TrimSpace(value) == "" || !filepath.IsAbs(value) {
			return nil, errors.New("credential broker: host SSH environment is invalid")
		}
		values[name] = filepath.Clean(value)
	}
	if values["HOME"] == "" {
		return nil, errors.New("credential broker: host SSH HOME is required")
	}
	result := safeGitEnvironment(os.Environ())
	result = append(result, "HOME="+values["HOME"], "GIT_TERMINAL_PROMPT=0")
	if socket := values["SSH_AUTH_SOCK"]; socket != "" {
		result = append(result, "SSH_AUTH_SOCK="+socket)
	}
	return result, nil
}

func safeGitEnvironment(host []string) []string {
	allowed := map[string]bool{"PATH": true, "LANG": true, "LC_ALL": true, "LC_CTYPE": true, "TZ": true,
		"SSL_CERT_FILE": true, "SSL_CERT_DIR": true, "GIT_SSL_CAINFO": true, "http_proxy": true,
		"https_proxy": true, "HTTP_PROXY": true, "HTTPS_PROXY": true, "no_proxy": true, "NO_PROXY": true}
	var result []string
	for _, entry := range host {
		name, _, ok := strings.Cut(entry, "=")
		if ok && allowed[name] {
			result = append(result, entry)
		}
	}
	return result
}

func (l *GitLease) Cleanup() error {
	if l == nil || l.cleanup == nil {
		return nil
	}
	cleanupCtx, cancel := credentialCleanupContext(context.Background())
	defer cancel()
	err := l.cleanup(cleanupCtx)
	l.cleanup = nil
	return err
}

func validatePinnedHTTPS(binding state.RepositoryBindingSnapshot) error {
	parsed, err := url.Parse(strings.TrimSpace(binding.CloneURL))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("credential broker: git clone URL must be credential-free HTTPS")
	}
	expected := "/" + strings.TrimPrefix(strings.TrimSuffix(strings.TrimSpace(binding.ExternalRepositoryID), ".git"), "/")
	actual := strings.TrimSuffix(path.Clean(parsed.EscapedPath()), ".git")
	if expected == "/" || actual != expected {
		return fmt.Errorf("credential broker: clone path does not match pinned external repository")
	}
	if strings.TrimSpace(binding.ProviderKey) == "" {
		return errors.New("credential broker: provider key is required")
	}
	return nil
}

func invalidGitSecret(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 1<<20 {
		return true
	}
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return true
		}
	}
	return false
}

// OperatorGitProvider binds an operator lease issuer to one exact provider,
// host, and external repository. AcquireLease must mint a fresh lease for each
// call; the broker never reuses a credential across jobs or clone/child phases.
type OperatorGitProvider struct {
	ProviderKey          string
	Host                 string
	ExternalRepositoryID string
	AcquireLease         func(context.Context, GitRequest) (GitProviderLease, error)
	RevokeJobLease       func(context.Context, string) error
}

func (p OperatorGitProvider) Acquire(ctx context.Context, request GitRequest) (GitProviderLease, error) {
	binding := request.Binding
	parsed, _ := url.Parse(binding.CloneURL)
	bindingAuthority, bindingErr := normalizedAuthority(parsed.Host)
	configuredAuthority, configuredErr := normalizedAuthority(p.Host)
	if binding.ProviderKey != p.ProviderKey || bindingErr != nil || configuredErr != nil || bindingAuthority != configuredAuthority ||
		binding.ExternalRepositoryID != p.ExternalRepositoryID || p.AcquireLease == nil || p.RevokeJobLease == nil ||
		!validJobID(request.JobID) || (request.Purpose != string(capability.OperationGitClone) && request.Purpose != string(capability.OperationGitPush)) {
		return GitProviderLease{}, errors.New("credential broker: operator git credential does not match pinned binding")
	}
	return p.AcquireLease(ctx, request)
}

func normalizedAuthority(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsAny(raw, "/?#@\r\n\t") {
		return "", errors.New("credential broker: invalid git authority")
	}
	parsed, err := url.Parse("https://" + raw)
	if err != nil || parsed.Host == "" || parsed.Hostname() == "" || parsed.User != nil {
		return "", errors.New("credential broker: invalid git authority")
	}
	host := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if port != "" {
		return net.JoinHostPort(host, port), nil
	}
	if strings.Contains(host, ":") {
		return "[" + host + "]", nil
	}
	return host, nil
}

func (p OperatorGitProvider) RevokeJob(ctx context.Context, jobID string) error {
	if p.RevokeJobLease == nil || !validJobID(jobID) {
		return errors.New("credential broker: operator git job revoke is unavailable")
	}
	return p.RevokeJobLease(ctx, jobID)
}

func (p OperatorGitProvider) SupportsPurpose(purpose capability.Operation) bool {
	return (purpose == capability.OperationGitClone || purpose == capability.OperationGitPush) &&
		p.AcquireLease != nil && p.RevokeJobLease != nil
}

// HostSSHGitProvider is an explicit operator opt-in that reuses the runner
// process user's SSH identity. The authoritative binding remains a
// credential-free HTTPS URL; only command-local clone/push transport is
// rewritten to the canonical git@host:external/repository.git form.
//
// This provider deliberately does not claim short-lived or job-scoped
// credentials. Isolation is provided by the runner's sandbox mounts.
type HostSSHGitProvider struct {
	SSHDir         string
	SSHAgentSocket string
}

type HostSSHGitProviderConfig struct {
	SSHDir      string
	AgentSocket string
}

func NewHostSSHGitProvider(config HostSSHGitProviderConfig) (*HostSSHGitProvider, error) {
	provider := &HostSSHGitProvider{SSHDir: strings.TrimSpace(config.SSHDir), SSHAgentSocket: strings.TrimSpace(config.AgentSocket)}
	if err := provider.validate(); err != nil {
		return nil, err
	}
	return provider, nil
}

func (p HostSSHGitProvider) Acquire(_ context.Context, request GitRequest) (GitProviderLease, error) {
	if err := p.validate(); err != nil || !validJobID(request.JobID) ||
		(request.Purpose != string(capability.OperationGitClone) && request.Purpose != string(capability.OperationGitPush)) {
		return GitProviderLease{}, errors.New("credential broker: host SSH provider request is invalid")
	}
	cloneURL, err := canonicalHostSSHCloneURL(request.Binding)
	if err != nil {
		return GitProviderLease{}, err
	}
	env := []string{"HOME=" + filepath.Dir(filepath.Clean(p.SSHDir))}
	if socket := strings.TrimSpace(p.SSHAgentSocket); socket != "" {
		env = append(env, "SSH_AUTH_SOCK="+filepath.Clean(socket))
	}
	return GitProviderLease{CloneURL: cloneURL, Env: env, HostSSH: true,
		Revoke: func(context.Context) error { return nil }}, nil
}

func (p HostSSHGitProvider) RevokeJob(_ context.Context, jobID string) error {
	if !validJobID(jobID) {
		return errors.New("credential broker: host SSH job id is invalid")
	}
	return nil
}

func (p HostSSHGitProvider) SupportsPurpose(purpose capability.Operation) bool {
	return purpose == capability.OperationGitClone || purpose == capability.OperationGitPush
}

func (p HostSSHGitProvider) validate() error {
	if err := validateHostSSHDirectory(p.SSHDir); err != nil {
		return fmt.Errorf("credential broker: %w", err)
	}
	if socket := strings.TrimSpace(p.SSHAgentSocket); socket != "" {
		if err := validateHostSSHSocket(socket); err != nil {
			return fmt.Errorf("credential broker: %w", err)
		}
	}
	return nil
}

func validateHostSSHDirectory(raw string) error {
	value := strings.TrimSpace(raw)
	if value == "" || !filepath.IsAbs(value) || filepath.Base(filepath.Clean(value)) != ".ssh" {
		return errors.New("host SSH directory must be an absolute .ssh path")
	}
	info, err := os.Lstat(value)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("host SSH directory must be a non-symlink directory")
	}
	return nil
}

func validateHostSSHSocket(raw string) error {
	value := strings.TrimSpace(raw)
	if value == "" || !filepath.IsAbs(value) {
		return errors.New("host SSH agent socket must be absolute")
	}
	info, err := os.Lstat(value)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeSocket == 0 {
		return errors.New("host SSH agent socket must be a non-symlink Unix socket")
	}
	return nil
}

func canonicalHostSSHCloneURL(binding state.RepositoryBindingSnapshot) (string, error) {
	if err := validatePinnedHTTPS(binding); err != nil {
		return "", err
	}
	parsed, _ := url.Parse(strings.TrimSpace(binding.CloneURL))
	if parsed.Port() != "" || strings.Contains(parsed.Hostname(), ":") {
		return "", errors.New("credential broker: host SSH transport does not support ports or IPv6 authorities")
	}
	repository := strings.Trim(strings.TrimSpace(binding.ExternalRepositoryID), "/")
	if repository == "" || repository != strings.TrimSuffix(repository, ".git") || path.Clean(repository) != repository ||
		strings.ContainsAny(repository, "\\:@?#\r\n\t") {
		return "", errors.New("credential broker: external repository id is invalid for host SSH")
	}
	for _, segment := range strings.Split(repository, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", errors.New("credential broker: external repository id is invalid for host SSH")
		}
	}
	return "git@" + strings.ToLower(parsed.Hostname()) + ":" + repository + ".git", nil
}

var _ GitProvider = (*HostSSHGitProvider)(nil)
var _ GitPurposeProvider = (*HostSSHGitProvider)(nil)
