// Package codereview defines the provider-neutral boundary between issue-spec
// core and operator-installed code-host bridges. It intentionally contains no
// vendor-specific fields or implementations.
package codereview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

const ProtocolVersion = "issue-spec.code-provider/v1"

type Capability string

const (
	CapabilityEvidenceSnapshot Capability = "evidence.snapshot"
	CapabilityChangeCreate     Capability = "change.create"
	CapabilityChangeComment    Capability = "change.comment"
)

var (
	ErrProviderNotFound    = errors.New("code provider is not registered by the operator")
	ErrCapabilityMissing   = errors.New("code provider capability is missing")
	ErrInvalidProviderData = errors.New("code provider returned invalid data")
)

type Capabilities struct {
	ProtocolVersion string       `json:"protocol_version"`
	Values          []Capability `json:"values"`
}

func (c Capabilities) Has(value Capability) bool {
	for _, candidate := range c.Values {
		if candidate == value {
			return true
		}
	}
	return false
}

func (c Capabilities) Validate() error {
	if c.ProtocolVersion != ProtocolVersion {
		return fmt.Errorf("%w: unsupported protocol %q", ErrInvalidProviderData, c.ProtocolVersion)
	}
	seen := make(map[Capability]struct{}, len(c.Values))
	for _, value := range c.Values {
		switch value {
		case CapabilityEvidenceSnapshot, CapabilityChangeCreate, CapabilityChangeComment:
		default:
			return fmt.Errorf("%w: unsupported capability %q", ErrInvalidProviderData, value)
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("%w: duplicate capability %q", ErrInvalidProviderData, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

type Reference struct {
	ProviderKey        string `json:"provider_key"`
	ExternalRepository string `json:"external_repository"`
	ChangeID           string `json:"change_id"`
}

func (r Reference) Validate() error {
	if !validKey(r.ProviderKey) || strings.TrimSpace(r.ExternalRepository) == "" || strings.TrimSpace(r.ChangeID) == "" {
		return fmt.Errorf("%w: incomplete code change reference", ErrInvalidProviderData)
	}
	if len(r.ExternalRepository) > 512 || len(r.ChangeID) > 256 {
		return fmt.Errorf("%w: code change reference is too long", ErrInvalidProviderData)
	}
	return nil
}

type EvidenceKind string

const (
	EvidenceChange  EvidenceKind = "change"
	EvidenceReview  EvidenceKind = "review"
	EvidenceCheck   EvidenceKind = "check"
	EvidenceMerge   EvidenceKind = "merge"
	EvidenceArchive EvidenceKind = "archive"
)

type EvidenceRecord struct {
	ID              string          `json:"id"`
	Kind            EvidenceKind    `json:"kind"`
	ExternalID      string          `json:"external_id,omitempty"`
	State           string          `json:"state"`
	SubjectRevision string          `json:"subject_revision"`
	BaseRevision    string          `json:"base_revision,omitempty"`
	MergeRevision   string          `json:"merge_revision,omitempty"`
	Name            string          `json:"name,omitempty"`
	Severity        string          `json:"severity,omitempty"`
	FindingID       string          `json:"finding_id,omitempty"`
	ProcessID       string          `json:"process_id,omitempty"`
	SpecID          string          `json:"spec_id,omitempty"`
	ObservedAt      time.Time       `json:"observed_at"`
	ValidUntil      *time.Time      `json:"valid_until,omitempty"`
	Trusted         bool            `json:"trusted"`
	WriterIdentity  string          `json:"writer_identity"`
	SupersedesID    string          `json:"supersedes_id,omitempty"`
	CanonicalURL    string          `json:"canonical_url,omitempty"`
	PayloadDigest   string          `json:"payload_digest"`
	Payload         json.RawMessage `json:"payload,omitempty"`
}

var neutralArtifactIDPattern = regexp.MustCompile(`^[A-Z]+-[0-9]{3,}$`)

// ValidateReviewLinkage keeps external line discussions provider-owned while
// requiring their canonical workflow identity to survive normalization.  The
// gate consumes only canonical FINDING/PROCESS/SPEC identifiers and known
// severity/state values; arbitrary bridge text cannot stand in for linkage.
func (r EvidenceRecord) ValidateReviewLinkage() error {
	finding := strings.TrimSpace(r.FindingID)
	process := strings.TrimSpace(r.ProcessID)
	spec := strings.TrimSpace(r.SpecID)
	if r.Kind != EvidenceReview {
		if finding != "" || process != "" || spec != "" {
			return fmt.Errorf("%w: non-review evidence contains review linkage", ErrInvalidProviderData)
		}
		return nil
	}
	if !neutralArtifactIDPattern.MatchString(finding) || !strings.HasPrefix(finding, "FINDING-") ||
		!neutralArtifactIDPattern.MatchString(process) || !strings.HasPrefix(process, "PROCESS-") ||
		!neutralArtifactIDPattern.MatchString(spec) || !strings.HasPrefix(spec, "SPEC-") {
		return fmt.Errorf("%w: review evidence requires canonical FINDING, PROCESS, and SPEC linkage", ErrInvalidProviderData)
	}
	switch strings.ToUpper(strings.TrimSpace(r.Severity)) {
	case "P0", "P1", "P2":
	default:
		return fmt.Errorf("%w: review evidence has invalid severity", ErrInvalidProviderData)
	}
	switch strings.ToLower(strings.TrimSpace(r.State)) {
	case "open", "resolved", "dismissed", "closed", "superseded":
	default:
		return fmt.Errorf("%w: review evidence has invalid state", ErrInvalidProviderData)
	}
	return nil
}

type SnapshotRequest struct {
	Reference       Reference `json:"reference"`
	SubjectRevision string    `json:"subject_revision"`
}

type Snapshot struct {
	ProtocolVersion string           `json:"protocol_version"`
	Reference       Reference        `json:"reference"`
	SubjectRevision string           `json:"subject_revision"`
	Records         []EvidenceRecord `json:"records"`
	CapturedAt      time.Time        `json:"captured_at"`
}

type MutationKind string

const (
	MutationCreateChange MutationKind = "create_change"
	MutationComment      MutationKind = "comment"
)

type MutationRequest struct {
	Kind         MutationKind   `json:"kind"`
	Reference    Reference      `json:"reference"`
	Title        string         `json:"title,omitempty"`
	Body         string         `json:"body,omitempty"`
	BaseRevision string         `json:"base_revision,omitempty"`
	HeadRevision string         `json:"head_revision,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

type MutationResult struct {
	Reference    Reference `json:"reference"`
	CanonicalURL string    `json:"canonical_url"`
	ExternalID   string    `json:"external_id"`
}

type Provider interface {
	Capabilities(context.Context) (Capabilities, error)
	Snapshot(context.Context, SnapshotRequest) (Snapshot, error)
}

// FetchSnapshot discovers support before asking a bridge for evidence. This
// keeps capability failure ahead of any command-side workflow mutation.
func FetchSnapshot(ctx context.Context, provider Provider, request SnapshotRequest) (Snapshot, error) {
	if _, err := RequireCapabilities(ctx, provider, CapabilityEvidenceSnapshot); err != nil {
		return Snapshot{}, err
	}
	return provider.Snapshot(ctx, request)
}

type MutationProvider interface {
	Provider
	Mutate(context.Context, MutationRequest) (MutationResult, error)
}

func RequireCapabilities(ctx context.Context, provider Provider, required ...Capability) (Capabilities, error) {
	if provider == nil {
		return Capabilities{}, fmt.Errorf("%w: nil provider", ErrProviderNotFound)
	}
	capabilities, err := provider.Capabilities(ctx)
	if err != nil {
		return Capabilities{}, err
	}
	if err := capabilities.Validate(); err != nil {
		return Capabilities{}, err
	}
	missing := make([]string, 0)
	for _, value := range required {
		if !capabilities.Has(value) {
			missing = append(missing, string(value))
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return Capabilities{}, fmt.Errorf("%w: %s", ErrCapabilityMissing, strings.Join(missing, ", "))
	}
	return capabilities, nil
}

func RequiredCapability(kind MutationKind) (Capability, error) {
	switch kind {
	case MutationCreateChange:
		return CapabilityChangeCreate, nil
	case MutationComment:
		return CapabilityChangeComment, nil
	default:
		return "", fmt.Errorf("%w: unsupported mutation %q", ErrInvalidProviderData, kind)
	}
}

// Mutate performs capability discovery before invoking a provider mutation.
// Callers should finish every other preflight before this function; the helper
// guarantees that an unsupported operation never reaches the mutation method.
func Mutate(ctx context.Context, provider MutationProvider, request MutationRequest) (MutationResult, error) {
	capability, err := RequiredCapability(request.Kind)
	if err != nil {
		return MutationResult{}, err
	}
	if _, err := RequireCapabilities(ctx, provider, capability); err != nil {
		return MutationResult{}, err
	}
	return provider.Mutate(ctx, request)
}

var providerKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$`)

func validKey(value string) bool {
	return len(value) <= 128 && providerKeyPattern.MatchString(value)
}

func ValidateProviderKey(value string) error {
	if !validKey(strings.TrimSpace(value)) {
		return errors.New("provider key must be a lowercase operator registration key")
	}
	return nil
}
