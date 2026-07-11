package credentials

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	clientauth "github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/sandbox"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/auth/delegation"
	"github.com/higress-group/issue-spec/internal/server/models"
)

const (
	maxExchangeResponseBytes = 1 << 20
	credentialClockSkew      = 5 * time.Second
	credentialCleanupTimeout = 5 * time.Second
)

type Broker struct {
	Profile      clientauth.Profile
	Audience     string
	Subject      string
	ParentToken  string
	HTTPClient   *http.Client
	Materializer Materializer
	GitProvider  GitProvider
	TTL          time.Duration
	Scopes       []string
}

type AcquireRequest struct {
	Repo    models.RepoScope
	JobID   string
	Binding state.RepositoryBindingSnapshot
}

type Lease struct {
	JobID      string
	Repo       models.RepoScope
	IssueToken FileLease
	Git        *GitLease
	Profile    clientauth.Profile

	broker  *Broker
	binding state.RepositoryBindingSnapshot
	gitRoot string
	revoked sync.Once
	err     error
}

func (b *Broker) Acquire(ctx context.Context, request AcquireRequest) (*Lease, error) {
	if b == nil || request.Repo.Validate() != nil || !validJobID(request.JobID) || !request.Binding.Complete() ||
		invalidToken(b.ParentToken) || !validLeaseValue(b.Audience) || !validLeaseValue(b.Subject) || b.GitProvider == nil {
		return nil, errors.New("credential broker: invalid acquisition request")
	}
	profile, err := b.Profile.Normalized()
	if err != nil || profile.Kind != clientauth.ProfileKindHosted {
		return nil, errors.New("credential broker: a valid self-hosted profile is required")
	}
	ttl := b.TTL
	if ttl == 0 {
		ttl = 5 * time.Minute
	}
	if ttl < delegation.MinTTL || ttl > delegation.MaxTTL {
		return nil, errors.New("credential broker: lease TTL is outside server limits")
	}
	scopes := append([]string(nil), b.Scopes...)
	if len(scopes) == 0 {
		scopes = []string{"read:user", "issues:read", "issues:write"}
	}
	created, err := b.exchange(ctx, request, ttl, scopes)
	if err != nil {
		return nil, err
	}
	lease := &Lease{JobID: request.JobID, Repo: request.Repo, Profile: profile, broker: b, binding: request.Binding}
	lease.IssueToken, err = b.Materializer.WriteIssueToken(request.JobID, created.Plaintext)
	if err != nil {
		cleanupCtx, cancel := credentialCleanupContext(ctx)
		defer cancel()
		_ = b.GitProvider.RevokeJob(cleanupCtx, request.JobID)
		_ = b.revokeRemote(cleanupCtx, request.Repo, request.JobID)
		return nil, fmt.Errorf("credential broker: materialize issue credential: %w", err)
	}
	jobRoot := filepath.Dir(lease.IssueToken.HostPath)
	lease.gitRoot = jobRoot
	lease.Git, err = NewGitLease(ctx, jobRoot, request.JobID, request.Binding, b.GitProvider)
	if err != nil {
		_ = b.Materializer.Revoke(request.JobID)
		cleanupCtx, cancel := credentialCleanupContext(ctx)
		defer cancel()
		_ = b.GitProvider.RevokeJob(cleanupCtx, request.JobID)
		_ = b.revokeRemote(cleanupCtx, request.Repo, request.JobID)
		return nil, fmt.Errorf("credential broker: acquire git credential: %w", err)
	}
	return lease, nil
}

// PrepareChildGit rotates the command-local clone lease before the sandboxed
// child starts. A clone credential is never reused by the agent.
func (l *Lease) PrepareChildGit(ctx context.Context) error {
	if l == nil || l.broker == nil {
		return errors.New("credential broker: lease is unavailable")
	}
	if l.Git != nil {
		if err := l.Git.Cleanup(); err != nil {
			return err
		}
	}
	next, err := NewGitLease(ctx, l.gitRoot, l.JobID, l.binding, l.broker.GitProvider)
	if err != nil {
		return err
	}
	l.Git = next
	return nil
}

func (l *Lease) FileCapabilities() []sandbox.FileCapability {
	if l == nil || l.Git == nil {
		return nil
	}
	return []sandbox.FileCapability{
		{Source: l.IssueToken.HostPath, Destination: l.IssueToken.SandboxPath, EnvName: clientauth.IssueSpecTokenFileEnv},
		{Source: l.Git.AskPassPath, Destination: GitAskPassSandboxPath, EnvName: "GIT_ASKPASS"},
		{Source: l.Git.UsernamePath, Destination: GitUsernameSandboxPath, EnvName: "ISSUE_SPEC_GIT_USERNAME_FILE"},
		{Source: l.Git.SecretPath, Destination: GitSecretSandboxPath, EnvName: "ISSUE_SPEC_GIT_SECRET_FILE"},
	}
}

func (l *Lease) ChildEnv() map[string]string {
	if l == nil {
		return nil
	}
	return map[string]string{
		"GIT_TERMINAL_PROMPT": "0",
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_CONFIG_GLOBAL":   "/dev/null",
		"GIT_CONFIG_COUNT":    "1",
		"GIT_CONFIG_KEY_0":    "http.followRedirects",
		"GIT_CONFIG_VALUE_0":  "false",
	}
}

func (l *Lease) Revoke(ctx context.Context) error {
	if l == nil {
		return nil
	}
	l.revoked.Do(func() {
		cleanupCtx, cancel := credentialCleanupContext(ctx)
		defer cancel()
		if l.Git != nil {
			l.err = errors.Join(l.err, l.Git.Cleanup())
		}
		if l.broker != nil {
			l.err = errors.Join(l.err, l.broker.Materializer.Revoke(l.JobID))
			l.err = errors.Join(l.err, l.broker.GitProvider.RevokeJob(cleanupCtx, l.JobID))
			l.err = errors.Join(l.err, l.broker.revokeRemote(cleanupCtx, l.Repo, l.JobID))
		}
	})
	return l.err
}

func (b *Broker) RevokeJob(ctx context.Context, repo models.RepoScope, jobID string) error {
	if b == nil || b.GitProvider == nil || repo.Validate() != nil || !validJobID(jobID) || invalidToken(b.ParentToken) {
		return errors.New("credential broker: invalid revoke request")
	}
	cleanupCtx, cancel := credentialCleanupContext(ctx)
	defer cancel()
	return errors.Join(b.Materializer.Revoke(jobID), b.GitProvider.RevokeJob(cleanupCtx, jobID), b.revokeRemote(cleanupCtx, repo, jobID))
}

func (b *Broker) exchange(ctx context.Context, request AcquireRequest, ttl time.Duration, scopes []string) (delegation.Created, error) {
	body, err := json.Marshal(map[string]any{"job_id": request.JobID, "purpose": "issue-api", "audience": b.Audience,
		"subject": b.Subject, "scopes": scopes, "ttl_seconds": int64(ttl / time.Second), "replace": true})
	if err != nil {
		return delegation.Created{}, err
	}
	endpoint, err := b.endpoint("api/v1/orgs/" + request.Repo.OrgID.String() + "/repos/" + request.Repo.RepoID.String() + "/delegated-tokens/exchange")
	if err != nil {
		return delegation.Created{}, err
	}
	response, err := b.do(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return delegation.Created{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return delegation.Created{}, fmt.Errorf("credential broker: exchange rejected with status %d request_id=%s", response.StatusCode, safeRequestID(response.Header.Get("X-Request-ID")))
	}
	mediaType, _, contentTypeErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if contentTypeErr != nil || mediaType != "application/json" {
		return delegation.Created{}, errors.New("credential broker: exchange response is not JSON")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxExchangeResponseBytes+1))
	if err != nil || len(data) > maxExchangeResponseBytes {
		return delegation.Created{}, errors.New("credential broker: invalid exchange response")
	}
	var created delegation.Created
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&created); err != nil {
		return delegation.Created{}, errors.New("credential broker: invalid exchange response")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return delegation.Created{}, errors.New("credential broker: invalid exchange response")
	}
	now := time.Now().UTC()
	if _, err := serverauth.TokenPrefix(created.Plaintext, "dgt"); err != nil || created.ID == uuid.Nil ||
		invalidToken(created.Plaintext) || !now.Before(created.ExpiresAt) || created.ExpiresAt.After(now.Add(ttl+credentialClockSkew)) {
		return delegation.Created{}, errors.New("credential broker: invalid exchange response")
	}
	return created, nil
}

func credentialCleanupContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	} else {
		parent = context.WithoutCancel(parent)
	}
	return context.WithTimeout(parent, credentialCleanupTimeout)
}

func (b *Broker) revokeRemote(ctx context.Context, repo models.RepoScope, jobID string) error {
	endpoint, err := b.endpoint("api/v1/orgs/" + repo.OrgID.String() + "/repos/" + repo.RepoID.String() + "/jobs/" + url.PathEscape(jobID) + "/delegated-tokens")
	if err != nil {
		return err
	}
	response, err := b.do(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("credential broker: revoke rejected with status %d request_id=%s", response.StatusCode, safeRequestID(response.Header.Get("X-Request-ID")))
	}
	return nil
}

func (b *Broker) endpoint(relative string) (string, error) {
	profile, err := b.Profile.Normalized()
	if err != nil {
		return "", err
	}
	base, err := url.Parse(profile.NativeAPIURL)
	if err != nil {
		return "", err
	}
	routePath := strings.TrimLeft(relative, "/")
	basePath := strings.TrimRight(base.Path, "/")
	if strings.HasSuffix(basePath, "/api/v1") && strings.HasPrefix(routePath, "api/v1/") {
		routePath = strings.TrimPrefix(routePath, "api/v1/")
	}
	base.Path = basePath + "/" + routePath
	base.RawPath, base.RawQuery, base.Fragment = "", "", ""
	return base.String(), nil
}

func (b *Broker) do(ctx context.Context, method, endpoint string, body []byte) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("credential broker: build request")
	}
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(b.ParentToken))
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	client := b.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	} else {
		copy := *client
		client = &copy
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return nil, errors.New("credential broker: server request failed")
	}
	return response, nil
}

func validLeaseValue(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if char < 0x21 || char == 0x7f {
			return false
		}
	}
	return true
}

func validJobID(value string) bool {
	if !validLeaseValue(value) {
		return false
	}
	for _, char := range strings.TrimSpace(value) {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || strings.ContainsRune("-_.:", char) {
			continue
		}
		return false
	}
	return true
}

func safeRequestID(value string) string {
	if !validLeaseValue(value) {
		return "unknown"
	}
	return value
}
