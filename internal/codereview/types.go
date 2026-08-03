// Package codereview defines the provider-neutral boundary between issue-spec
// core and operator-installed code-host bridges. It intentionally contains no
// vendor-specific fields or implementations.
package codereview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
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

// ProviderDescription is the transport-safe, operator-owned metadata clients
// use during onboarding. It deliberately cannot describe an executable,
// arguments, environment variables, credentials, or filesystem paths.
type ProviderDescription struct {
	ProviderKey         string         `json:"provider_key"`
	DisplayName         string         `json:"display_name"`
	RemoteAuthorities   []string       `json:"remote_authorities,omitempty"`
	CodeChangeLabel     string         `json:"code_change_label"`
	Capabilities        []Capability   `json:"capabilities,omitempty"`
	RecommendedEvidence []EvidenceKind `json:"recommended_evidence,omitempty"`
}

func (d ProviderDescription) Normalized(key string) (ProviderDescription, error) {
	key = strings.TrimSpace(key)
	if !validKey(key) {
		return ProviderDescription{}, fmt.Errorf("%w: invalid provider description key %q", ErrInvalidProviderData, key)
	}
	d.ProviderKey = strings.TrimSpace(d.ProviderKey)
	if d.ProviderKey == "" {
		d.ProviderKey = key
	}
	if d.ProviderKey != key {
		return ProviderDescription{}, fmt.Errorf("%w: provider description key does not match registration", ErrInvalidProviderData)
	}
	d.DisplayName = strings.TrimSpace(d.DisplayName)
	if d.DisplayName == "" {
		d.DisplayName = key
	}
	d.CodeChangeLabel = strings.TrimSpace(d.CodeChangeLabel)
	if d.CodeChangeLabel == "" {
		d.CodeChangeLabel = "Code change"
	}
	if len(d.DisplayName) > 128 || len(d.CodeChangeLabel) > 128 {
		return ProviderDescription{}, fmt.Errorf("%w: provider display metadata is too long", ErrInvalidProviderData)
	}
	seenAuthorities := map[string]bool{}
	for i, authority := range d.RemoteAuthorities {
		authority = strings.ToLower(strings.TrimSpace(authority))
		if !validRemoteAuthority(authority) || seenAuthorities[authority] {
			return ProviderDescription{}, fmt.Errorf("%w: invalid or duplicate remote authority %q", ErrInvalidProviderData, authority)
		}
		seenAuthorities[authority] = true
		d.RemoteAuthorities[i] = authority
	}
	seenCapabilities := map[Capability]bool{}
	for _, capability := range d.Capabilities {
		if seenCapabilities[capability] {
			return ProviderDescription{}, fmt.Errorf("%w: duplicate capability %q", ErrInvalidProviderData, capability)
		}
		seenCapabilities[capability] = true
		switch capability {
		case CapabilityEvidenceSnapshot, CapabilityChangeCreate, CapabilityChangeComment:
		default:
			return ProviderDescription{}, fmt.Errorf("%w: unsupported capability %q", ErrInvalidProviderData, capability)
		}
	}
	seenEvidence := map[EvidenceKind]bool{}
	for _, kind := range d.RecommendedEvidence {
		if seenEvidence[kind] {
			return ProviderDescription{}, fmt.Errorf("%w: duplicate recommended evidence %q", ErrInvalidProviderData, kind)
		}
		seenEvidence[kind] = true
		switch kind {
		case EvidenceChange, EvidenceReview, EvidenceCheck, EvidenceMerge, EvidenceArchive:
		default:
			return ProviderDescription{}, fmt.Errorf("%w: unsupported recommended evidence %q", ErrInvalidProviderData, kind)
		}
	}
	sort.Strings(d.RemoteAuthorities)
	sort.Slice(d.Capabilities, func(i, j int) bool { return d.Capabilities[i] < d.Capabilities[j] })
	sort.Slice(d.RecommendedEvidence, func(i, j int) bool { return d.RecommendedEvidence[i] < d.RecommendedEvidence[j] })
	return d, nil
}

func validRemoteAuthority(authority string) bool {
	if authority == "" || len(authority) > 253 || strings.ContainsAny(authority, "/@?#\\\r\n\t ") {
		return false
	}
	host := authority
	if strings.HasPrefix(authority, "[") || strings.Count(authority, ":") == 1 {
		if parsedHost, port, err := net.SplitHostPort(authority); err == nil {
			portNumber, portErr := strconv.Atoi(port)
			if portErr != nil || portNumber < 1 || portNumber > 65535 {
				return false
			}
			host = parsedHost
		}
	}
	host = strings.Trim(host, "[]")
	if net.ParseIP(host) != nil {
		return true
	}
	if strings.Contains(host, ":") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || !regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`).MatchString(label) {
			return false
		}
	}
	return true
}

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
var payloadDigestPattern = regexp.MustCompile(`^(?:sha256:)?[a-fA-F0-9]{64}$`)

// ProviderFact is the untrusted, provider-neutral record returned by an
// operator adapter. It deliberately has no trust, writer, provenance or
// approval fields; those are evidence-authority properties derived only after
// the self-hosted server accepts a snapshot.
type ProviderFact struct {
	ID              string       `json:"id"`
	Kind            EvidenceKind `json:"kind"`
	ExternalID      string       `json:"external_id,omitempty"`
	State           string       `json:"state"`
	SubjectRevision string       `json:"subject_revision"`
	BaseRevision    string       `json:"base_revision,omitempty"`
	MergeRevision   string       `json:"merge_revision,omitempty"`
	Name            string       `json:"name,omitempty"`
	Severity        string       `json:"severity,omitempty"`
	FindingID       string       `json:"finding_id,omitempty"`
	ProcessID       string       `json:"process_id,omitempty"`
	SpecID          string       `json:"spec_id,omitempty"`
	ObservedAt      time.Time    `json:"observed_at"`
	ValidUntil      *time.Time   `json:"valid_until,omitempty"`
	SupersedesID    string       `json:"supersedes_id,omitempty"`
	CanonicalURL    string       `json:"canonical_url,omitempty"`
	PayloadDigest   string       `json:"payload_digest"`
	Summary         string       `json:"summary,omitempty"`
	Path            string       `json:"path,omitempty"`
	Line            *int         `json:"line,omitempty"`
}

func ValidateProviderFact(fact ProviderFact) error {
	fact.ID = strings.TrimSpace(fact.ID)
	fact.ExternalID = strings.TrimSpace(fact.ExternalID)
	fact.State = strings.TrimSpace(fact.State)
	fact.SubjectRevision = strings.TrimSpace(fact.SubjectRevision)
	fact.SupersedesID = strings.TrimSpace(fact.SupersedesID)
	if fact.ID == "" || len(fact.ID) > 256 || fact.ExternalID == "" || len(fact.ExternalID) > 256 ||
		fact.State == "" || len(fact.State) > 64 || fact.SubjectRevision == "" || len(fact.SubjectRevision) > 512 ||
		fact.ObservedAt.IsZero() || !payloadDigestPattern.MatchString(strings.TrimSpace(fact.PayloadDigest)) {
		return fmt.Errorf("%w: provider fact identity, state, revision, observation or digest is invalid", ErrInvalidProviderData)
	}
	switch fact.Kind {
	case EvidenceChange, EvidenceReview, EvidenceCheck, EvidenceMerge, EvidenceArchive:
	default:
		return fmt.Errorf("%w: unsupported evidence kind %q", ErrInvalidProviderData, fact.Kind)
	}
	if fact.ValidUntil != nil && fact.ValidUntil.Before(fact.ObservedAt) {
		return fmt.Errorf("%w: provider fact validity ends before observation", ErrInvalidProviderData)
	}
	if fact.SupersedesID == fact.ID {
		return fmt.Errorf("%w: provider fact cannot supersede itself", ErrInvalidProviderData)
	}
	if fact.CanonicalURL != "" && !safeCanonicalURL(fact.CanonicalURL) {
		return fmt.Errorf("%w: provider fact canonical URL is unsafe", ErrInvalidProviderData)
	}
	if fact.Line != nil && *fact.Line <= 0 {
		return fmt.Errorf("%w: provider fact line is invalid", ErrInvalidProviderData)
	}
	linkage := EvidenceRecord{Kind: fact.Kind, State: fact.State, Severity: fact.Severity,
		FindingID: fact.FindingID, ProcessID: fact.ProcessID, SpecID: fact.SpecID}
	if err := linkage.ValidateReviewLinkage(); err != nil {
		return err
	}
	if fact.Kind == EvidenceCheck && strings.TrimSpace(fact.Name) == "" {
		return fmt.Errorf("%w: check provider fact requires a name", ErrInvalidProviderData)
	}
	return nil
}

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
	ProtocolVersion string         `json:"protocol_version"`
	Reference       Reference      `json:"reference"`
	SubjectRevision string         `json:"subject_revision"`
	Facts           []ProviderFact `json:"facts"`
	// Records is populated only when core reads already-persisted trusted
	// evidence. It is never part of the operator adapter wire protocol.
	Records    []EvidenceRecord `json:"-"`
	CapturedAt time.Time        `json:"captured_at"`
}

func ValidateProviderSnapshot(snapshot Snapshot) error {
	if snapshot.ProtocolVersion != ProtocolVersion || snapshot.Reference.Validate() != nil ||
		strings.TrimSpace(snapshot.SubjectRevision) == "" || snapshot.CapturedAt.IsZero() || snapshot.Records != nil {
		return fmt.Errorf("%w: provider snapshot identity is invalid", ErrInvalidProviderData)
	}
	seen := make(map[string]bool, len(snapshot.Facts))
	for _, fact := range snapshot.Facts {
		if err := ValidateProviderFact(fact); err != nil {
			return err
		}
		if fact.SubjectRevision != snapshot.SubjectRevision {
			return fmt.Errorf("%w: provider fact revision does not match snapshot", ErrInvalidProviderData)
		}
		if seen[fact.ID] {
			return fmt.Errorf("%w: duplicate provider fact id %q", ErrInvalidProviderData, fact.ID)
		}
		seen[fact.ID] = true
	}
	return nil
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
	if err := validateMutationRequest(request); err != nil {
		return MutationResult{}, err
	}
	capability, err := RequiredCapability(request.Kind)
	if err != nil {
		return MutationResult{}, err
	}
	if _, err := RequireCapabilities(ctx, provider, capability); err != nil {
		return MutationResult{}, err
	}
	result, err := provider.Mutate(ctx, request)
	if err != nil {
		return MutationResult{}, err
	}
	if result.Reference.ProviderKey != request.Reference.ProviderKey ||
		result.Reference.ExternalRepository != request.Reference.ExternalRepository ||
		result.Reference.Validate() != nil || strings.TrimSpace(result.ExternalID) == "" ||
		!safeCanonicalURL(result.CanonicalURL) {
		return MutationResult{}, fmt.Errorf("%w: mutation response shape", ErrInvalidProviderData)
	}
	if request.Kind == MutationComment && result.Reference != request.Reference {
		return MutationResult{}, fmt.Errorf("%w: mutation response change identity mismatch", ErrInvalidProviderData)
	}
	return result, nil
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
