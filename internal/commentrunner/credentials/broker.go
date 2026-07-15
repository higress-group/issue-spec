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
	"github.com/higress-group/issue-spec/internal/capability"
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
	credentialRequestTimeout = 15 * time.Second
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
	Repo       models.RepoScope
	JobID      string
	Binding    state.RepositoryBindingSnapshot
	Operations []capability.Operation
}

// PreflightRequest is secret-free and may be persisted by the runner. Repo is
// the authoritative self-hosted UUID scope; Request.Repository is its public
// owner/name coordinate used in diagnostics.
type PreflightRequest struct {
	Request capability.Request
	Repo    models.RepoScope
	JobID   string
}

// OperationIssuer is the operator-owned boundary consumed by strict runner
// dispatch. Implementations must preflight without issuing a credential and
// must return only redacted capability reports.
type OperationIssuer interface {
	Probe(context.Context, PreflightRequest) capability.Report
	Acquire(context.Context, AcquireRequest) (*Lease, error)
	RevokeJob(context.Context, models.RepoScope, string) error
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
		ttl = delegation.DefaultTTL
	}
	if ttl < delegation.MinTTL || ttl > delegation.MaxTTL {
		return nil, errors.New("credential broker: lease TTL is outside server limits")
	}
	scopes := append([]string(nil), b.Scopes...)
	if len(scopes) == 0 {
		scopes = []string{"read:user", "issues:read", "issues:write"}
	}
	operations := effectiveBrokerOperations(request.Operations)
	if !brokerScopesAllowOperations(scopes, operations) {
		return nil, errors.New("credential broker: requested operations exceed configured scopes")
	}
	request.Operations = operations
	created, err := b.exchange(ctx, request, ttl, scopes)
	if err != nil {
		return nil, b.compensateFailedAcquire(ctx, request, err)
	}
	lease := &Lease{JobID: request.JobID, Repo: request.Repo, Profile: profile, broker: b, binding: request.Binding}
	lease.IssueToken, err = b.Materializer.WriteIssueToken(request.JobID, created.Plaintext)
	if err != nil {
		return nil, b.compensateFailedAcquire(ctx, request,
			fmt.Errorf("credential broker: materialize issue credential: %w", err))
	}
	jobRoot := filepath.Dir(lease.IssueToken.HostPath)
	lease.gitRoot = jobRoot
	lease.Git, err = NewGitLease(ctx, jobRoot, request.JobID, capability.OperationGitClone, request.Binding, b.GitProvider)
	if err != nil {
		return nil, b.compensateFailedAcquire(ctx, request,
			fmt.Errorf("credential broker: acquire git credential: %w", err))
	}
	return lease, nil
}

func (b *Broker) Probe(_ context.Context, request PreflightRequest) capability.Report {
	req := request.Request
	profile, profileErr := b.Profile.Normalized()
	ttl := b.TTL
	if ttl == 0 {
		ttl = delegation.DefaultTTL
	}
	if profileErr != nil || profile.Kind != clientauth.ProfileKindHosted || request.Repo.Validate() != nil ||
		!validJobID(request.JobID) || strings.TrimSpace(req.Repository) == "" ||
		!strings.EqualFold(strings.TrimSpace(req.Host), profile.Hostname) || ttl < delegation.MinTTL || ttl > delegation.MaxTTL {
		return capability.FailureReport(req, "delegated", "operator-issuer", "unknown", capability.DecisionDenied,
			capability.FailureInvalidRequest, "operator credential issuer request is invalid")
	}
	scopes := append([]string(nil), b.Scopes...)
	if len(scopes) == 0 {
		scopes = []string{"read:user", "issues:read", "issues:write"}
	}
	report := capability.Report{Host: req.Host, Repository: req.Repository, Backend: "operator-issuer",
		Credential: capability.CredentialSummary{SourceClass: "delegated", ExpiryKnown: true},
		Network:    capability.NetworkSummary{Status: "configured"}}
	expiresAt := time.Now().UTC().Add(ttl)
	report.Credential.ExpiresAt = &expiresAt
	for _, operation := range effectiveBrokerOperations(req.Operations) {
		result := capability.OperationResult{Operation: operation}
		switch operation {
		case capability.OperationIssueRead, capability.OperationIssueCommentWrite, capability.OperationArtifactWrite:
			if brokerScopesAllowOperations(scopes, []capability.Operation{operation}) {
				result.Decision = capability.DecisionAllowed
			} else {
				result.Decision, result.Code, result.Detail = capability.DecisionDenied,
					capability.FailureInsufficientPermission, "operator issuer scope does not allow operation"
			}
		case capability.OperationGitClone, capability.OperationGitPush:
			provider, supported := b.GitProvider.(GitPurposeProvider)
			if supported && provider.SupportsPurpose(operation) {
				result.Decision = capability.DecisionAllowed
			} else {
				result.Decision, result.Code, result.Detail = capability.DecisionDenied,
					capability.FailureOperationNotProvable, "operator git credential issuer is unavailable"
			}
		default:
			result.Decision, result.Code, result.Detail = capability.DecisionUnknown,
				capability.FailureUnsupportedOperationSurface, "self-hosted runner issuer does not provide this operation"
		}
		report.Operations = append(report.Operations, result)
	}
	report.Finish()
	return report
}

// compensateFailedAcquire treats every exchange error as an uncertain remote
// result. The server may have committed a short-lived token before the client
// rejected or lost the response, so a job-level tombstone is the only safe
// rollback boundary.
func (b *Broker) compensateFailedAcquire(ctx context.Context, request AcquireRequest, cause error) error {
	// Revoke the issue token first; every external/local cleanup receives its
	// own deadline so a stuck source provider cannot consume the server revoke
	// budget.
	cleanupErr := errors.Join(b.revokeRemoteBounded(ctx, request.Repo, request.JobID),
		b.Materializer.Revoke(request.JobID), b.revokeGitJobBounded(ctx, request.JobID))
	if cleanupErr == nil {
		return cause
	}
	return errors.Join(cause, fmt.Errorf("credential broker: compensate failed acquisition: %w", cleanupErr))
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
	next, err := NewGitLease(ctx, l.gitRoot, l.JobID, capability.OperationGitPush, l.binding, l.broker.GitProvider)
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
	capabilities := []sandbox.FileCapability{
		{Source: l.IssueToken.HostPath, Destination: l.IssueToken.SandboxPath, EnvName: clientauth.IssueSpecTokenFileEnv},
	}
	if !l.Git.HostSSH {
		capabilities = append(capabilities,
			sandbox.FileCapability{Source: l.Git.AskPassPath, Destination: GitAskPassSandboxPath, EnvName: "GIT_ASKPASS"},
			sandbox.FileCapability{Source: l.Git.UsernamePath, Destination: GitUsernameSandboxPath, EnvName: "ISSUE_SPEC_GIT_USERNAME_FILE"},
			sandbox.FileCapability{Source: l.Git.SecretPath, Destination: GitSecretSandboxPath, EnvName: "ISSUE_SPEC_GIT_SECRET_FILE"})
	}
	return capabilities
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
		if l.broker != nil {
			l.err = errors.Join(l.err, l.broker.revokeRemoteBounded(ctx, l.Repo, l.JobID))
		}
		if l.Git != nil {
			l.err = errors.Join(l.err, l.Git.Cleanup())
		}
		if l.broker != nil {
			l.err = errors.Join(l.err, l.broker.Materializer.Revoke(l.JobID))
			l.err = errors.Join(l.err, l.broker.revokeGitJobBounded(ctx, l.JobID))
		}
	})
	return l.err
}

func (b *Broker) RevokeJob(ctx context.Context, repo models.RepoScope, jobID string) error {
	if b == nil || b.GitProvider == nil || repo.Validate() != nil || !validJobID(jobID) || invalidToken(b.ParentToken) {
		return errors.New("credential broker: invalid revoke request")
	}
	return errors.Join(b.revokeRemoteBounded(ctx, repo, jobID), b.Materializer.Revoke(jobID), b.revokeGitJobBounded(ctx, jobID))
}

func (b *Broker) revokeRemoteBounded(parent context.Context, repo models.RepoScope, jobID string) error {
	cleanupCtx, cancel := credentialCleanupContext(parent)
	defer cancel()
	return b.revokeRemote(cleanupCtx, repo, jobID)
}

func (b *Broker) revokeGitJobBounded(parent context.Context, jobID string) error {
	cleanupCtx, cancel := credentialCleanupContext(parent)
	defer cancel()
	return b.GitProvider.RevokeJob(cleanupCtx, jobID)
}

func (b *Broker) exchange(ctx context.Context, request AcquireRequest, ttl time.Duration, scopes []string) (delegation.Created, error) {
	body, err := json.Marshal(map[string]any{"job_id": request.JobID, "purpose": "issue-api", "audience": b.Audience,
		"subject": b.Subject, "scopes": scopes, "operations": issueCredentialOperations(request.Operations),
		"ttl_seconds": int64(ttl / time.Second), "replace": true})
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

func issueCredentialOperations(operations []capability.Operation) []capability.Operation {
	var result []capability.Operation
	for _, operation := range effectiveBrokerOperations(operations) {
		switch operation {
		case capability.OperationIssueRead, capability.OperationIssueCommentWrite, capability.OperationArtifactWrite:
			result = append(result, operation)
		}
	}
	return result
}

func effectiveBrokerOperations(operations []capability.Operation) []capability.Operation {
	if len(operations) == 0 {
		return []capability.Operation{capability.OperationIssueRead, capability.OperationIssueCommentWrite,
			capability.OperationArtifactWrite, capability.OperationGitClone, capability.OperationGitPush}
	}
	seen := map[capability.Operation]bool{}
	result := make([]capability.Operation, 0, len(operations))
	for _, operation := range operations {
		if operation != "" && !seen[operation] {
			seen[operation] = true
			result = append(result, operation)
		}
	}
	return result
}

func brokerScopesAllowOperations(scopes []string, operations []capability.Operation) bool {
	granted := map[string]bool{}
	for _, scope := range scopes {
		granted[strings.TrimSpace(scope)] = true
	}
	for _, operation := range operations {
		switch operation {
		case capability.OperationIssueRead:
			if !granted["issues:read"] && !granted["issues:write"] && !granted["repo"] {
				return false
			}
		case capability.OperationIssueCommentWrite, capability.OperationArtifactWrite:
			if !granted["issues:write"] && !granted["repo"] {
				return false
			}
		case capability.OperationGitClone, capability.OperationGitPush:
			// Git operations are enforced by the independent operator provider.
		default:
			return false
		}
	}
	return true
}

var _ OperationIssuer = (*Broker)(nil)

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
	client := b.boundedHTTPClient()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return nil, errors.New("credential broker: server request failed")
	}
	return response, nil
}

func (b *Broker) boundedHTTPClient() *http.Client {
	client := &http.Client{}
	if b != nil && b.HTTPClient != nil {
		copy := *b.HTTPClient
		client = &copy
	}
	if client.Timeout <= 0 || client.Timeout > credentialRequestTimeout {
		client.Timeout = credentialRequestTimeout
	}
	return client
}

func validLeaseValue(value string) bool {
	trimmed := strings.TrimSpace(value)
	if value != trimmed || trimmed == "" || len(trimmed) > 128 {
		return false
	}
	for _, char := range trimmed {
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
