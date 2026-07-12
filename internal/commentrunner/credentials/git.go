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
	ExpiresAt  time.Time
	Revoke     func(context.Context) error
}

type GitLease struct {
	Purpose      capability.Operation
	CloneURL     string
	AskPassPath  string
	UsernamePath string
	SecretPath   string
	cleanup      func(context.Context) error
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
	lease := &GitLease{Purpose: purpose, CloneURL: binding.CloneURL, AskPassPath: askpassPath, UsernamePath: usernamePath, SecretPath: secretPath}
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
	if l == nil || l.Purpose != capability.OperationGitClone || operation != "clone" || strings.TrimSpace(cloneURL) != strings.TrimSpace(l.CloneURL) {
		return workspace.GitCredential{}, errors.New("credential broker: git credential requested outside pinned clone")
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
	return workspace.GitCredential{Env: env, Cleanup: l.Cleanup}, nil
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
