package codereview

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	defaultCommandTimeout = 30 * time.Second
	maximumCommandTimeout = 2 * time.Minute
	defaultOutputLimit    = int64(1 << 20)
	maximumOutputLimit    = int64(4 << 20)
	maximumInputSize      = 1 << 20
)

var ErrAdapterOutputLimit = errors.New("code provider output exceeded its configured bound")

// CommandConfig is operator-owned process configuration. It must never be
// populated from repository workflow content. The bridge is executed directly
// without a shell, receives one JSON request on stdin, and returns one strict
// JSON response on stdout.
type CommandConfig struct {
	Path        string
	Args        []string
	Environment []string
	Timeout     time.Duration
	MaxOutput   int64
}

type CommandProvider struct{ config CommandConfig }

func NewCommandProvider(config CommandConfig) (*CommandProvider, error) {
	config.Path = strings.TrimSpace(config.Path)
	if config.Path == "" || !filepath.IsAbs(config.Path) || filepath.Clean(config.Path) != config.Path {
		return nil, errors.New("code provider command path must be a clean absolute operator path")
	}
	info, err := os.Stat(config.Path)
	if err != nil {
		return nil, fmt.Errorf("inspect code provider command: %w", err)
	}
	if !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0) {
		return nil, errors.New("code provider command must be an executable regular file")
	}
	if len(config.Args) > 32 {
		return nil, errors.New("code provider command has too many operator arguments")
	}
	for _, value := range config.Args {
		if len(value) > 4096 || strings.ContainsRune(value, 0) {
			return nil, errors.New("code provider command has an invalid operator argument")
		}
	}
	if config.Timeout == 0 {
		config.Timeout = defaultCommandTimeout
	}
	if config.Timeout <= 0 || config.Timeout > maximumCommandTimeout {
		return nil, errors.New("code provider command timeout must be between zero and two minutes")
	}
	if config.MaxOutput == 0 {
		config.MaxOutput = defaultOutputLimit
	}
	if config.MaxOutput < 1024 || config.MaxOutput > maximumOutputLimit {
		return nil, errors.New("code provider output bound must be between 1 KiB and 4 MiB")
	}
	seenEnvironment := make(map[string]struct{}, len(config.Environment))
	for _, value := range config.Environment {
		name, _, ok := strings.Cut(value, "=")
		if !ok || name == "" || strings.ContainsAny(name, "\x00\r\n") || strings.ContainsRune(value, 0) {
			return nil, errors.New("code provider command has an invalid operator environment entry")
		}
		if _, exists := seenEnvironment[name]; exists {
			return nil, fmt.Errorf("duplicate code provider environment entry %q", name)
		}
		seenEnvironment[name] = struct{}{}
	}
	config.Args = append([]string(nil), config.Args...)
	config.Environment = append([]string(nil), config.Environment...)
	return &CommandProvider{config: config}, nil
}

type protocolRequest struct {
	Protocol  string          `json:"protocol"`
	RequestID string          `json:"request_id"`
	Action    string          `json:"action"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

type protocolResponse struct {
	Protocol           string                    `json:"protocol"`
	RequestID          string                    `json:"request_id"`
	Capabilities       *Capabilities             `json:"capabilities,omitempty"`
	Snapshot           *Snapshot                 `json:"snapshot,omitempty"`
	Mutation           *MutationResult           `json:"mutation,omitempty"`
	WorkspaceLifecycle *WorkspaceLifecycleResult `json:"workspace_lifecycle,omitempty"`
	Error              *protocolError            `json:"error,omitempty"`
}

type protocolError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (p *CommandProvider) Capabilities(ctx context.Context) (Capabilities, error) {
	response, err := p.invoke(ctx, "capabilities", nil)
	if err != nil {
		return Capabilities{}, err
	}
	if response.Capabilities == nil || response.Snapshot != nil || response.Mutation != nil || response.WorkspaceLifecycle != nil {
		return Capabilities{}, fmt.Errorf("%w: capabilities response shape", ErrInvalidProviderData)
	}
	if err := response.Capabilities.Validate(); err != nil {
		return Capabilities{}, err
	}
	return *response.Capabilities, nil
}

func (p *CommandProvider) Snapshot(ctx context.Context, request SnapshotRequest) (Snapshot, error) {
	if err := request.Reference.Validate(); err != nil || strings.TrimSpace(request.SubjectRevision) == "" {
		if err != nil {
			return Snapshot{}, err
		}
		return Snapshot{}, fmt.Errorf("%w: snapshot subject revision is required", ErrInvalidProviderData)
	}
	response, err := p.invoke(ctx, "snapshot", request)
	if err != nil {
		return Snapshot{}, err
	}
	if response.Snapshot == nil || response.Capabilities != nil || response.Mutation != nil || response.WorkspaceLifecycle != nil {
		return Snapshot{}, fmt.Errorf("%w: snapshot response shape", ErrInvalidProviderData)
	}
	if response.Snapshot.ProtocolVersion != ProtocolVersion || response.Snapshot.Reference != request.Reference ||
		response.Snapshot.SubjectRevision != request.SubjectRevision || response.Snapshot.CapturedAt.IsZero() {
		return Snapshot{}, fmt.Errorf("%w: snapshot identity mismatch", ErrInvalidProviderData)
	}
	if err := ValidateProviderSnapshot(*response.Snapshot); err != nil {
		return Snapshot{}, err
	}
	return *response.Snapshot, nil
}

func (p *CommandProvider) Mutate(ctx context.Context, request MutationRequest) (MutationResult, error) {
	if err := validateMutationRequest(request); err != nil {
		return MutationResult{}, err
	}
	if _, err := RequiredCapability(request.Kind); err != nil {
		return MutationResult{}, err
	}
	response, err := p.invoke(ctx, "mutate", request)
	if err != nil {
		return MutationResult{}, err
	}
	if response.Mutation == nil || response.Capabilities != nil || response.Snapshot != nil || response.WorkspaceLifecycle != nil ||
		response.Mutation.Reference.ProviderKey != request.Reference.ProviderKey ||
		response.Mutation.Reference.ExternalRepository != request.Reference.ExternalRepository ||
		response.Mutation.Reference.Validate() != nil || strings.TrimSpace(response.Mutation.ExternalID) == "" ||
		!safeCanonicalURL(response.Mutation.CanonicalURL) {
		return MutationResult{}, fmt.Errorf("%w: mutation response shape", ErrInvalidProviderData)
	}
	// A comment is an operation on one already-authoritative change.  A bridge
	// must not be able to acknowledge the request with a comment written to a
	// different change.  create_change is the only mutation whose response is
	// allowed to introduce a new change identifier.
	if request.Kind == MutationComment && response.Mutation.Reference != request.Reference {
		return MutationResult{}, fmt.Errorf("%w: mutation response change identity mismatch", ErrInvalidProviderData)
	}
	return *response.Mutation, nil
}

func (p *CommandProvider) WorkspaceLifecycle(ctx context.Context, request WorkspaceLifecycleRequest) (WorkspaceLifecycleResult, error) {
	if err := validateWorkspaceLifecycleRequest(request); err != nil {
		return WorkspaceLifecycleResult{}, err
	}
	response, err := p.invoke(ctx, "workspace_lifecycle", request)
	if err != nil {
		return WorkspaceLifecycleResult{}, err
	}
	if response.WorkspaceLifecycle == nil || response.Capabilities != nil || response.Snapshot != nil || response.Mutation != nil {
		return WorkspaceLifecycleResult{}, fmt.Errorf("%w: workspace lifecycle response shape", ErrInvalidProviderData)
	}
	if err := validateWorkspaceLifecycleResult(request, *response.WorkspaceLifecycle); err != nil {
		return WorkspaceLifecycleResult{}, err
	}
	return *response.WorkspaceLifecycle, nil
}

func validateMutationRequest(request MutationRequest) error {
	if err := ValidateProviderKey(request.Reference.ProviderKey); err != nil ||
		strings.TrimSpace(request.Reference.ExternalRepository) == "" || len(request.Reference.ExternalRepository) > 512 {
		return fmt.Errorf("%w: mutation provider and repository are required", ErrInvalidProviderData)
	}
	switch request.Kind {
	case MutationCreateChange:
		if strings.TrimSpace(request.Reference.ChangeID) != "" || strings.TrimSpace(request.Title) == "" ||
			strings.TrimSpace(request.HeadRevision) == "" {
			return fmt.Errorf("%w: create change requires title and head revision but no existing change id", ErrInvalidProviderData)
		}
	case MutationComment:
		if err := request.Reference.Validate(); err != nil || strings.TrimSpace(request.Body) == "" {
			return fmt.Errorf("%w: comment requires a complete change reference and body", ErrInvalidProviderData)
		}
	default:
		return fmt.Errorf("%w: unsupported mutation %q", ErrInvalidProviderData, request.Kind)
	}
	return nil
}

func (p *CommandProvider) invoke(ctx context.Context, action string, payload any) (protocolResponse, error) {
	requestID := uuid.NewString()
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		return protocolResponse{}, fmt.Errorf("encode code provider request: %w", err)
	}
	request := protocolRequest{Protocol: ProtocolVersion, RequestID: requestID, Action: action}
	if payload != nil {
		request.Payload = rawPayload
	}
	rawRequest, err := json.Marshal(request)
	if err != nil {
		return protocolResponse{}, fmt.Errorf("encode code provider envelope: %w", err)
	}
	if len(rawRequest) > maximumInputSize {
		return protocolResponse{}, errors.New("code provider request exceeds 1 MiB")
	}

	commandContext, cancel := context.WithTimeout(ctx, p.config.Timeout)
	defer cancel()
	command := exec.CommandContext(commandContext, p.config.Path, p.config.Args...)
	command.Env = append([]string{"LANG=C.UTF-8", "LC_ALL=C.UTF-8", "PATH=/usr/bin:/bin"}, p.config.Environment...)
	command.Stdin = bytes.NewReader(append(rawRequest, '\n'))
	stdout := &boundedBuffer{limit: p.config.MaxOutput}
	stderr := &boundedBuffer{limit: p.config.MaxOutput}
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Run(); err != nil {
		if stdout.exceeded() || stderr.exceeded() {
			return protocolResponse{}, ErrAdapterOutputLimit
		}
		if commandContext.Err() != nil {
			return protocolResponse{}, fmt.Errorf("code provider %s: %w", action, commandContext.Err())
		}
		// Bridge stderr is intentionally not surfaced: even an operator-trusted
		// adapter can accidentally print an upstream credential.
		return protocolResponse{}, fmt.Errorf("code provider %s failed: %w", action, err)
	}
	if stdout.exceeded() || stderr.exceeded() {
		return protocolResponse{}, ErrAdapterOutputLimit
	}

	rawResponse := stdout.Bytes()
	if err := rejectDuplicateKeys(rawResponse); err != nil {
		return protocolResponse{}, fmt.Errorf("%w: %v", ErrInvalidProviderData, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(rawResponse))
	decoder.DisallowUnknownFields()
	var response protocolResponse
	if err := decoder.Decode(&response); err != nil {
		return protocolResponse{}, fmt.Errorf("%w: decode response: %v", ErrInvalidProviderData, err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return protocolResponse{}, fmt.Errorf("%w: %v", ErrInvalidProviderData, err)
	}
	if response.Protocol != ProtocolVersion || response.RequestID != requestID {
		return protocolResponse{}, fmt.Errorf("%w: protocol or request identity mismatch", ErrInvalidProviderData)
	}
	if response.Error != nil {
		if strings.TrimSpace(response.Error.Code) == "" || strings.TrimSpace(response.Error.Message) == "" {
			return protocolResponse{}, fmt.Errorf("%w: malformed adapter error", ErrInvalidProviderData)
		}
		return protocolResponse{}, fmt.Errorf("code provider %s: %s: %s", action, response.Error.Code, response.Error.Message)
	}
	return response, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("response contains more than one JSON value")
		}
		return err
	}
	return nil
}

func rejectDuplicateKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := consumeValue(decoder); err != nil {
		return err
	}
	return requireJSONEOF(decoder)
}

func consumeValue(decoder *json.Decoder) error {
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
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("invalid JSON object key")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate JSON field %q", key)
			}
			seen[key] = struct{}{}
			if err := consumeValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
	case '[':
		for decoder.More() {
			if err := consumeValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
	default:
		return errors.New("invalid JSON delimiter")
	}
	return err
}

func safeCanonicalURL(raw string) bool {
	if raw == "" || raw != strings.TrimSpace(raw) || strings.ContainsAny(raw, "?#\x00\r\n\t\\") {
		return false
	}
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.Hostname() != "" &&
		parsed.User == nil && parsed.Opaque == "" && parsed.RawQuery == "" && !parsed.ForceQuery &&
		parsed.Fragment == "" && parsed.RawFragment == "" && parsed.Host == strings.ToLower(parsed.Host) &&
		parsed.Port() != "443" && parsed.String() == raw
}

type boundedBuffer struct {
	mu       sync.Mutex
	buffer   bytes.Buffer
	limit    int64
	overflow bool
}

func (b *boundedBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.limit - int64(b.buffer.Len())
	if remaining <= 0 {
		b.overflow = true
		return 0, ErrAdapterOutputLimit
	}
	writtenValue := value
	if int64(len(value)) > remaining {
		writtenValue = value[:remaining]
		b.overflow = true
	}
	written, err := b.buffer.Write(writtenValue)
	if b.overflow && err == nil {
		err = ErrAdapterOutputLimit
	}
	return written, err
}

func (b *boundedBuffer) exceeded() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.overflow
}

func (b *boundedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buffer.Bytes()...)
}

func (b *boundedBuffer) String() string { return string(b.Bytes()) }
