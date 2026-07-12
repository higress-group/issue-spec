package credentials

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/auth/delegation"
)

const commandGitProtocol = "issue-spec-git-credential-v1"

var ErrGitCredentialOutputLimit = errors.New("git credential provider output exceeded its configured bound")

// CommandGitProviderConfig is trusted operator configuration. The executable
// is invoked directly without a shell, inherits no ambient credential
// environment, and exchanges one strict JSON value per invocation.
type CommandGitProviderConfig struct {
	Path          string
	Args          []string
	Timeout       time.Duration
	MaxOutput     int64
	MaxConcurrent int
}

type CommandGitProvider struct {
	config CommandGitProviderConfig
	sem    chan struct{}
}

func NewCommandGitProvider(config CommandGitProviderConfig) (*CommandGitProvider, error) {
	config.Path = strings.TrimSpace(config.Path)
	if config.Path == "" || !filepath.IsAbs(config.Path) || filepath.Clean(config.Path) != config.Path {
		return nil, errors.New("git credential command path must be a clean absolute operator path")
	}
	info, err := os.Stat(config.Path)
	if err != nil {
		return nil, fmt.Errorf("inspect git credential command: %w", err)
	}
	if !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0) {
		return nil, errors.New("git credential command must be an executable regular file")
	}
	if len(config.Args) > 32 {
		return nil, errors.New("git credential command has too many operator arguments")
	}
	for _, argument := range config.Args {
		if len(argument) > 4096 || strings.ContainsRune(argument, 0) {
			return nil, errors.New("git credential command has an invalid operator argument")
		}
	}
	if config.Timeout == 0 {
		config.Timeout = 30 * time.Second
	}
	if config.Timeout <= 0 || config.Timeout > 2*time.Minute {
		return nil, errors.New("git credential command timeout must be at most two minutes")
	}
	if config.MaxOutput == 0 {
		config.MaxOutput = 1 << 20
	}
	if config.MaxOutput < 1024 || config.MaxOutput > 4<<20 {
		return nil, errors.New("git credential command output bound must be between 1 KiB and 4 MiB")
	}
	if config.MaxConcurrent == 0 {
		config.MaxConcurrent = 4
	}
	if config.MaxConcurrent < 1 || config.MaxConcurrent > 32 {
		return nil, errors.New("git credential command concurrency must be between one and 32")
	}
	config.Args = append([]string(nil), config.Args...)
	return &CommandGitProvider{config: config, sem: make(chan struct{}, config.MaxConcurrent)}, nil
}

type gitCommandRequest struct {
	Protocol  string             `json:"protocol"`
	RequestID string             `json:"request_id"`
	Action    string             `json:"action"`
	Identity  gitCommandIdentity `json:"identity"`
}

type gitCommandResponse struct {
	Protocol  string             `json:"protocol"`
	RequestID string             `json:"request_id"`
	Action    string             `json:"action"`
	Identity  gitCommandIdentity `json:"identity"`
	Lease     *gitCommandLease   `json:"lease,omitempty"`
	Revoked   *bool              `json:"revoked,omitempty"`
	Error     *gitCommandError   `json:"error,omitempty"`
}

type gitCommandIdentity struct {
	JobID   string             `json:"job_id"`
	Purpose string             `json:"purpose,omitempty"`
	Binding *gitCommandBinding `json:"binding,omitempty"`
	LeaseID string             `json:"lease_id,omitempty"`
}

type gitCommandBinding struct {
	BindingID            string `json:"binding_id"`
	Version              int64  `json:"version"`
	ProviderKey          string `json:"provider_key"`
	ExternalRepositoryID string `json:"external_repository_id"`
	CloneURL             string `json:"clone_url"`
}

type gitCommandLease struct {
	LeaseID   string    `json:"lease_id"`
	Username  string    `json:"username"`
	Password  string    `json:"password"`
	ExpiresAt time.Time `json:"expires_at"`
}

type gitCommandError struct {
	Code string `json:"code"`
}

func (p *CommandGitProvider) Acquire(ctx context.Context, request GitRequest) (GitProviderLease, error) {
	if !validJobID(request.JobID) || request.Purpose != "git" || !request.Binding.Complete() {
		return GitProviderLease{}, errors.New("git credential command requires a complete job, purpose, and pinned binding")
	}
	identity := gitAcquireIdentity(request)
	response, err := p.invoke(ctx, "acquire", identity)
	if err != nil {
		return GitProviderLease{}, errors.Join(err, p.compensateAcquire(request.JobID))
	}
	if response.Lease == nil || response.Revoked != nil || strings.TrimSpace(response.Lease.LeaseID) == "" ||
		invalidGitSecret(response.Lease.Username) || invalidGitSecret(response.Lease.Password) || response.Lease.ExpiresAt.IsZero() ||
		!time.Now().UTC().Before(response.Lease.ExpiresAt) || response.Lease.ExpiresAt.After(time.Now().UTC().Add(delegation.MaxTTL+credentialClockSkew)) {
		return GitProviderLease{}, errors.Join(errors.New("git credential command returned an invalid lease"), p.compensateAcquire(request.JobID))
	}
	leaseID := response.Lease.LeaseID
	var once sync.Once
	var revokeErr error
	return GitProviderLease{Credential: GitSecret{Username: response.Lease.Username, Password: response.Lease.Password},
		ExpiresAt: response.Lease.ExpiresAt, Revoke: func(revokeCtx context.Context) error {
			once.Do(func() {
				_, revokeErr = p.invoke(revokeCtx, "revoke_lease", gitCommandIdentity{JobID: request.JobID, LeaseID: leaseID})
			})
			return revokeErr
		}}, nil
}

func (p *CommandGitProvider) RevokeJob(ctx context.Context, jobID string) error {
	if !validJobID(jobID) {
		return errors.New("git credential command requires a valid job id")
	}
	_, err := p.invoke(ctx, "revoke_job", gitCommandIdentity{JobID: jobID})
	return err
}

func (p *CommandGitProvider) compensateAcquire(jobID string) error {
	cleanupCtx, cancel := credentialCleanupContext(context.Background())
	defer cancel()
	_, err := p.invoke(cleanupCtx, "revoke_job", gitCommandIdentity{JobID: jobID})
	if err != nil {
		return fmt.Errorf("git credential command uncertain acquire cleanup failed: %w", err)
	}
	return nil
}

func gitAcquireIdentity(request GitRequest) gitCommandIdentity {
	return gitCommandIdentity{JobID: request.JobID, Purpose: request.Purpose, Binding: &gitCommandBinding{
		BindingID: request.Binding.BindingID, Version: request.Binding.Version, ProviderKey: request.Binding.ProviderKey,
		ExternalRepositoryID: request.Binding.ExternalRepositoryID, CloneURL: request.Binding.CloneURL,
	}}
}

func (p *CommandGitProvider) invoke(ctx context.Context, action string, identity gitCommandIdentity) (gitCommandResponse, error) {
	if p == nil {
		return gitCommandResponse{}, errors.New("git credential command provider is required")
	}
	select {
	case p.sem <- struct{}{}:
		defer func() { <-p.sem }()
	case <-ctx.Done():
		return gitCommandResponse{}, ctx.Err()
	}
	requestID := uuid.NewString()
	request := gitCommandRequest{Protocol: commandGitProtocol, RequestID: requestID, Action: action, Identity: identity}
	raw, err := json.Marshal(request)
	if err != nil {
		return gitCommandResponse{}, errors.New("encode git credential command request")
	}
	if len(raw) > 64<<10 {
		return gitCommandResponse{}, errors.New("git credential command request exceeds 64 KiB")
	}
	commandContext, cancel := context.WithTimeout(ctx, p.config.Timeout)
	defer cancel()
	command := exec.CommandContext(commandContext, p.config.Path, p.config.Args...)
	command.Env = []string{"LANG=C.UTF-8", "LC_ALL=C.UTF-8", "PATH=/usr/bin:/bin"}
	command.Stdin = bytes.NewReader(append(raw, '\n'))
	stdout, stderr := &gitBoundedBuffer{limit: p.config.MaxOutput}, &gitBoundedBuffer{limit: p.config.MaxOutput}
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Run(); err != nil {
		if stdout.exceeded || stderr.exceeded {
			return gitCommandResponse{}, ErrGitCredentialOutputLimit
		}
		if commandContext.Err() != nil {
			return gitCommandResponse{}, fmt.Errorf("git credential command %s: %w", action, commandContext.Err())
		}
		return gitCommandResponse{}, fmt.Errorf("git credential command %s failed", action)
	}
	if stdout.exceeded || stderr.exceeded {
		return gitCommandResponse{}, ErrGitCredentialOutputLimit
	}
	if err := rejectGitDuplicateKeys(stdout.Bytes()); err != nil {
		return gitCommandResponse{}, errors.New("git credential command returned invalid JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	decoder.DisallowUnknownFields()
	var response gitCommandResponse
	if err := decoder.Decode(&response); err != nil {
		return gitCommandResponse{}, errors.New("git credential command returned invalid JSON")
	}
	if err := requireGitJSONEOF(decoder); err != nil || response.Protocol != commandGitProtocol || response.RequestID != requestID ||
		response.Action != action || !validGitResponseIdentity(action, identity, response) {
		return gitCommandResponse{}, errors.New("git credential command response identity mismatch")
	}
	if response.Error != nil {
		if !safeGitErrorCode(response.Error.Code) || response.Lease != nil || response.Revoked != nil {
			return gitCommandResponse{}, errors.New("git credential command returned malformed error")
		}
		return gitCommandResponse{}, fmt.Errorf("git credential command %s returned %s", action, response.Error.Code)
	}
	switch action {
	case "acquire":
		if response.Lease == nil || response.Revoked != nil {
			return gitCommandResponse{}, errors.New("git credential command acquire response shape is invalid")
		}
	case "revoke_lease", "revoke_job":
		if response.Lease != nil || response.Revoked == nil || !*response.Revoked {
			return gitCommandResponse{}, errors.New("git credential command revoke response shape is invalid")
		}
	default:
		return gitCommandResponse{}, errors.New("git credential command action is invalid")
	}
	return response, nil
}

func validGitResponseIdentity(action string, requested gitCommandIdentity, response gitCommandResponse) bool {
	actual := response.Identity
	if actual.JobID != requested.JobID || actual.Purpose != requested.Purpose || !equalGitBinding(actual.Binding, requested.Binding) {
		return false
	}
	if response.Error != nil {
		return actual.LeaseID == requested.LeaseID
	}
	switch action {
	case "acquire":
		return response.Lease != nil && validLeaseValue(response.Lease.LeaseID) && actual.LeaseID == response.Lease.LeaseID
	case "revoke_lease":
		return actual.LeaseID == requested.LeaseID && validLeaseValue(actual.LeaseID)
	case "revoke_job":
		return actual.LeaseID == ""
	default:
		return false
	}
}

func equalGitBinding(actual, expected *gitCommandBinding) bool {
	if actual == nil || expected == nil {
		return actual == nil && expected == nil
	}
	return *actual == *expected
}

type gitBoundedBuffer struct {
	bytes.Buffer
	limit    int64
	exceeded bool
}

func (b *gitBoundedBuffer) Write(value []byte) (int, error) {
	if b.exceeded {
		return len(value), nil
	}
	remaining := b.limit - int64(b.Len())
	if int64(len(value)) > remaining {
		if remaining > 0 {
			_, _ = b.Buffer.Write(value[:remaining])
		}
		b.exceeded = true
		return len(value), nil
	}
	return b.Buffer.Write(value)
}

func safeGitErrorCode(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 64 {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '_' || char == '-' {
			continue
		}
		return false
	}
	return true
}

func rejectGitDuplicateKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := consumeGitJSONValue(decoder); err != nil {
		return err
	}
	return requireGitJSONEOF(decoder)
}

func consumeGitJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok || seen[key] {
				return errors.New("duplicate or invalid object key")
			}
			seen[key] = true
			if err := consumeGitJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := consumeGitJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	default:
		return errors.New("unexpected JSON delimiter")
	}
}

func requireGitJSONEOF(decoder *json.Decoder) error {
	var extra interface{}
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

var _ GitProvider = (*CommandGitProvider)(nil)
