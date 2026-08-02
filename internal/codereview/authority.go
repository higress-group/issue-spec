package codereview

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

type PrincipalIdentity struct {
	Realm    string `json:"realm"`
	StableID string `json:"stable_id"`
}

func (p PrincipalIdentity) Validate() error {
	if !validOpaqueIdentity(p.Realm, 128) || !validOpaqueIdentity(p.StableID, 256) {
		return fmt.Errorf("%w: canonical principal is incomplete", ErrInvalidProviderData)
	}
	return nil
}

type ActorKind string

const (
	ActorHuman   ActorKind = "human"
	ActorService ActorKind = "service"
)

type ActorIdentity struct {
	Provider           string            `json:"provider"`
	StableID           string            `json:"stable_id"`
	CanonicalPrincipal PrincipalIdentity `json:"canonical_principal"`
	Kind               ActorKind         `json:"kind"`
	Display            string            `json:"display,omitempty"`
}

func (a ActorIdentity) Validate() error {
	if !validKey(a.Provider) || !validOpaqueIdentity(a.StableID, 256) || a.CanonicalPrincipal.Validate() != nil {
		return fmt.Errorf("%w: actor source or canonical identity is incomplete", ErrInvalidProviderData)
	}
	if a.Kind != ActorHuman && a.Kind != ActorService {
		return fmt.Errorf("%w: unsupported actor kind %q", ErrInvalidProviderData, a.Kind)
	}
	if len(a.Display) > 256 || strings.ContainsAny(a.Display, "\x00\r\n") {
		return fmt.Errorf("%w: actor display is invalid", ErrInvalidProviderData)
	}
	return nil
}

func actorSourceKey(a ActorIdentity) string   { return a.Provider + "\x00" + a.StableID }
func principalKey(p PrincipalIdentity) string { return p.Realm + "\x00" + p.StableID }

// PrincipalMapping is operator-owned. Repository data can ask to resolve an
// actor, but cannot add or override mappings.
type PrincipalMapping struct {
	Provider  string            `json:"provider"`
	StableID  string            `json:"stable_id"`
	Principal PrincipalIdentity `json:"principal"`
}

type PrincipalMapper struct{ bySource map[string]PrincipalIdentity }

func NewPrincipalMapper(mappings []PrincipalMapping) (PrincipalMapper, error) {
	result := PrincipalMapper{bySource: make(map[string]PrincipalIdentity, len(mappings))}
	for _, mapping := range mappings {
		actor := ActorIdentity{Provider: mapping.Provider, StableID: mapping.StableID,
			CanonicalPrincipal: mapping.Principal, Kind: ActorHuman}
		if err := actor.Validate(); err != nil {
			return PrincipalMapper{}, err
		}
		key := actorSourceKey(actor)
		if previous, ok := result.bySource[key]; ok {
			if previous != mapping.Principal {
				return PrincipalMapper{}, fmt.Errorf("%w: conflicting canonical mappings for %s", ErrInvalidProviderData, mapping.StableID)
			}
			return PrincipalMapper{}, fmt.Errorf("%w: duplicate canonical mapping for %s", ErrInvalidProviderData, mapping.StableID)
		}
		result.bySource[key] = mapping.Principal
	}
	return result, nil
}

func (m PrincipalMapper) Map(provider, stableID string) (PrincipalIdentity, error) {
	principal, ok := m.bySource[provider+"\x00"+stableID]
	if !ok {
		return PrincipalIdentity{}, fmt.Errorf("%w: canonical principal is unmapped", ErrInvalidProviderData)
	}
	return principal, nil
}

// MapMergeSnapshot replaces every bridge-supplied actor principal with the
// immutable operator mapping. A bridge reports only provider + stable actor
// identity; repository content and provider output cannot choose the
// cross-domain principal used for independence.
func (m PrincipalMapper) MapMergeSnapshot(snapshot *MergeSnapshot) error {
	if snapshot == nil {
		return fmt.Errorf("%w: merge snapshot is missing", ErrInvalidProviderData)
	}
	mapActor := func(actor *ActorIdentity) error {
		if actor == nil {
			return fmt.Errorf("%w: provider actor is missing", ErrInvalidProviderData)
		}
		principal, err := m.Map(actor.Provider, actor.StableID)
		if err != nil {
			return err
		}
		if actor.CanonicalPrincipal != (PrincipalIdentity{}) && actor.CanonicalPrincipal != principal {
			return fmt.Errorf("%w: provider actor conflicts with the operator canonical principal", ErrInvalidProviderData)
		}
		actor.CanonicalPrincipal = principal
		return nil
	}
	for index := range snapshot.Review.Authors {
		if err := mapActor(&snapshot.Review.Authors[index]); err != nil {
			return err
		}
	}
	for index := range snapshot.Review.Decisions {
		if err := mapActor(&snapshot.Review.Decisions[index].Reviewer); err != nil {
			return err
		}
	}
	for index := range snapshot.Review.Findings {
		finding := &snapshot.Review.Findings[index]
		if err := mapActor(&finding.Owner); err != nil {
			return err
		}
		if finding.StateOwner != nil {
			if err := mapActor(finding.StateOwner); err != nil {
				return err
			}
		}
	}
	return nil
}

type PrincipalMappingSource interface {
	CanonicalPrincipalMappingSource() string
}

func RequireCanonicalPrincipalMapping(provider any) (string, error) {
	source, ok := provider.(PrincipalMappingSource)
	if !ok || !validOpaqueIdentity(source.CanonicalPrincipalMappingSource(), 256) {
		return "", fmt.Errorf("%w: operator-owned canonical-principal mapping is unavailable", ErrCapabilityMissing)
	}
	return source.CanonicalPrincipalMappingSource(), nil
}

type CheckIdentity struct {
	Provider    string `json:"provider"`
	Key         string `json:"key"`
	Owner       string `json:"owner"`
	DisplayName string `json:"display_name,omitempty"`
}

func (c CheckIdentity) Validate() error {
	if !validKey(c.Provider) || !validOpaqueIdentity(c.Key, 512) || !validOpaqueIdentity(c.Owner, 256) ||
		len(c.DisplayName) > 256 || strings.ContainsAny(c.DisplayName, "\x00\r\n") {
		return fmt.Errorf("%w: check provider key and owner are required", ErrInvalidProviderData)
	}
	return nil
}

func checkIdentityKey(c CheckIdentity) string { return c.Provider + "\x00" + c.Key + "\x00" + c.Owner }

type CheckConclusionValue string

const (
	CheckPending   CheckConclusionValue = "pending"
	CheckSuccess   CheckConclusionValue = "success"
	CheckFailure   CheckConclusionValue = "failure"
	CheckCancelled CheckConclusionValue = "cancelled"
	CheckSkipped   CheckConclusionValue = "skipped"
)

type CheckConclusion struct {
	Identity                CheckIdentity        `json:"identity"`
	SubjectRevision         string               `json:"subject_revision"`
	CurrentAttemptID        string               `json:"current_attempt_id"`
	ConfigurationGeneration string               `json:"configuration_generation"`
	Conclusion              CheckConclusionValue `json:"conclusion"`
	Diagnostics             string               `json:"diagnostics,omitempty"`
	CanonicalURL            string               `json:"canonical_url,omitempty"`
}

func (c CheckConclusion) Validate() error {
	if c.Identity.Validate() != nil || !validOpaqueIdentity(c.SubjectRevision, 512) ||
		!validOpaqueIdentity(c.CurrentAttemptID, 256) || !validOpaqueIdentity(c.ConfigurationGeneration, 256) {
		return fmt.Errorf("%w: check conclusion identity is incomplete", ErrInvalidProviderData)
	}
	switch c.Conclusion {
	case CheckPending, CheckSuccess, CheckFailure, CheckCancelled, CheckSkipped:
	default:
		return fmt.Errorf("%w: unsupported check conclusion %q", ErrInvalidProviderData, c.Conclusion)
	}
	if len(c.Diagnostics) > 4096 || strings.ContainsRune(c.Diagnostics, 0) ||
		(c.CanonicalURL != "" && !safeCanonicalURL(c.CanonicalURL)) {
		return fmt.Errorf("%w: check diagnostics are invalid", ErrInvalidProviderData)
	}
	return nil
}

type ReviewMode string

const (
	ReviewProviderNative ReviewMode = "provider_native"
)

type ReviewPolicy struct {
	RequiredApprovalCount          int  `json:"required_approval_count"`
	CodeOwnerApprovalRequired      bool `json:"code_owner_approval_required"`
	DismissStaleApprovals          bool `json:"dismiss_stale_approvals"`
	ConversationResolutionRequired bool `json:"conversation_resolution_required"`
}

func (p *ReviewPolicy) UnmarshalJSON(raw []byte) error {
	type wire struct {
		RequiredApprovalCount          *int  `json:"required_approval_count"`
		CodeOwnerApprovalRequired      *bool `json:"code_owner_approval_required"`
		DismissStaleApprovals          *bool `json:"dismiss_stale_approvals"`
		ConversationResolutionRequired *bool `json:"conversation_resolution_required"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var value wire
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	if err := requireJSONEOF(decoder); err != nil {
		return err
	}
	if value.RequiredApprovalCount == nil || value.CodeOwnerApprovalRequired == nil ||
		value.DismissStaleApprovals == nil || value.ConversationResolutionRequired == nil {
		return fmt.Errorf("%w: review policy must declare every closed rule", ErrInvalidProviderData)
	}
	p.RequiredApprovalCount = *value.RequiredApprovalCount
	p.CodeOwnerApprovalRequired = *value.CodeOwnerApprovalRequired
	p.DismissStaleApprovals = *value.DismissStaleApprovals
	p.ConversationResolutionRequired = *value.ConversationResolutionRequired
	return nil
}

func (p ReviewPolicy) Validate() error {
	if p.RequiredApprovalCount < 0 || p.RequiredApprovalCount > 100 {
		return fmt.Errorf("%w: review approval count is invalid", ErrInvalidProviderData)
	}
	return nil
}

type ReviewVerdict string

const (
	ReviewApproved         ReviewVerdict = "approved"
	ReviewChangesRequested ReviewVerdict = "changes_requested"
	ReviewDismissed        ReviewVerdict = "dismissed"
	ReviewStale            ReviewVerdict = "stale"
)

type ReviewDecision struct {
	ID              string        `json:"id"`
	SupersedesID    string        `json:"supersedes_id,omitempty"`
	SubjectRevision string        `json:"subject_revision"`
	Reviewer        ActorIdentity `json:"reviewer"`
	Verdict         ReviewVerdict `json:"verdict"`
	ObservationID   string        `json:"observation_id"`
}

func (d ReviewDecision) Validate() error {
	if !validOpaqueIdentity(d.ID, 256) || (d.SupersedesID != "" && !validOpaqueIdentity(d.SupersedesID, 256)) ||
		!validOpaqueIdentity(d.SubjectRevision, 512) || d.Reviewer.Validate() != nil ||
		!validOpaqueIdentity(d.ObservationID, 256) || d.ID == d.SupersedesID {
		return fmt.Errorf("%w: review decision identity is invalid", ErrInvalidProviderData)
	}
	switch d.Verdict {
	case ReviewApproved, ReviewChangesRequested, ReviewDismissed, ReviewStale:
	default:
		return fmt.Errorf("%w: unsupported review verdict %q", ErrInvalidProviderData, d.Verdict)
	}
	return nil
}

type FindingSeverity string
type FindingState string

const (
	FindingP0 FindingSeverity = "P0"
	FindingP1 FindingSeverity = "P1"
	FindingP2 FindingSeverity = "P2"

	FindingOpen      FindingState = "open"
	FindingResolved  FindingState = "resolved"
	FindingDismissed FindingState = "dismissed"
)

type ReviewFinding struct {
	ID              string          `json:"id"`
	SubjectRevision string          `json:"subject_revision"`
	Owner           ActorIdentity   `json:"owner"`
	Severity        FindingSeverity `json:"severity"`
	State           FindingState    `json:"state"`
	Path            string          `json:"path,omitempty"`
	Line            *int            `json:"line,omitempty"`
	StateOwner      *ActorIdentity  `json:"state_owner,omitempty"`
	CanonicalURL    string          `json:"canonical_url,omitempty"`
}

func (f ReviewFinding) Validate() error {
	if !validOpaqueIdentity(f.ID, 256) || !validOpaqueIdentity(f.SubjectRevision, 512) || f.Owner.Validate() != nil {
		return fmt.Errorf("%w: review finding identity is invalid", ErrInvalidProviderData)
	}
	if f.Severity != FindingP0 && f.Severity != FindingP1 && f.Severity != FindingP2 {
		return fmt.Errorf("%w: unsupported finding severity %q", ErrInvalidProviderData, f.Severity)
	}
	if f.State != FindingOpen && f.State != FindingResolved && f.State != FindingDismissed {
		return fmt.Errorf("%w: unsupported finding state %q", ErrInvalidProviderData, f.State)
	}
	if len(f.Path) > 1024 || strings.ContainsAny(f.Path, "\x00\r\n") || (f.Line != nil && *f.Line <= 0) ||
		(f.CanonicalURL != "" && !safeCanonicalURL(f.CanonicalURL)) {
		return fmt.Errorf("%w: review finding navigation is invalid", ErrInvalidProviderData)
	}
	if f.State == FindingOpen {
		if f.StateOwner != nil {
			return fmt.Errorf("%w: open finding cannot have a state owner", ErrInvalidProviderData)
		}
	} else if f.StateOwner == nil || f.StateOwner.Validate() != nil ||
		principalKey(f.StateOwner.CanonicalPrincipal) != principalKey(f.Owner.CanonicalPrincipal) {
		return fmt.Errorf("%w: finding resolution or dismissal is not reviewer-owned", ErrInvalidProviderData)
	}
	return nil
}

type ReviewAuthority struct {
	Mode                    ReviewMode       `json:"mode"`
	AuthorSetComplete       bool             `json:"author_set_complete"`
	Authors                 []ActorIdentity  `json:"authors"`
	Policy                  ReviewPolicy     `json:"policy"`
	Decisions               []ReviewDecision `json:"decisions"`
	Findings                []ReviewFinding  `json:"findings"`
	UnresolvedConversations []string         `json:"unresolved_conversations"`
	CodeOwnerSatisfied      bool             `json:"code_owner_satisfied"`
}

func (r *ReviewAuthority) UnmarshalJSON(raw []byte) error {
	type wire struct {
		Mode                    *ReviewMode       `json:"mode"`
		AuthorSetComplete       *bool             `json:"author_set_complete"`
		Authors                 *[]ActorIdentity  `json:"authors"`
		Policy                  *ReviewPolicy     `json:"policy"`
		Decisions               *[]ReviewDecision `json:"decisions"`
		Findings                *[]ReviewFinding  `json:"findings"`
		UnresolvedConversations *[]string         `json:"unresolved_conversations"`
		CodeOwnerSatisfied      *bool             `json:"code_owner_satisfied"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var value wire
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	if err := requireJSONEOF(decoder); err != nil {
		return err
	}
	if value.Mode == nil || value.AuthorSetComplete == nil || value.Authors == nil || value.Policy == nil ||
		value.Decisions == nil || value.Findings == nil || value.UnresolvedConversations == nil || value.CodeOwnerSatisfied == nil {
		return fmt.Errorf("%w: review authority must declare every policy fact", ErrInvalidProviderData)
	}
	r.Mode = *value.Mode
	r.AuthorSetComplete = *value.AuthorSetComplete
	r.Authors = *value.Authors
	r.Policy = *value.Policy
	r.Decisions = *value.Decisions
	r.Findings = *value.Findings
	r.UnresolvedConversations = *value.UnresolvedConversations
	r.CodeOwnerSatisfied = *value.CodeOwnerSatisfied
	return nil
}

func (r ReviewAuthority) Validate(subject string) error {
	if r.Mode != ReviewProviderNative {
		return fmt.Errorf("%w: unsupported review mode %q", ErrInvalidProviderData, r.Mode)
	}
	if !r.AuthorSetComplete || len(r.Authors) == 0 || r.Decisions == nil || r.Findings == nil ||
		r.UnresolvedConversations == nil || r.Policy.Validate() != nil {
		return fmt.Errorf("%w: review policy or closed author set is incomplete", ErrInvalidProviderData)
	}
	actorPrincipals := map[string]PrincipalIdentity{}
	authors := map[string]bool{}
	for _, actor := range r.Authors {
		if err := validateConsistentActor(actorPrincipals, actor); err != nil {
			return err
		}
		key := actorSourceKey(actor)
		if authors[key] {
			return fmt.Errorf("%w: duplicate exact-subject author", ErrInvalidProviderData)
		}
		authors[key] = true
	}
	decisions := map[string]bool{}
	reviewers := map[string]bool{}
	for _, decision := range r.Decisions {
		if err := decision.Validate(); err != nil || decision.SubjectRevision != subject || decision.SupersedesID != "" {
			return fmt.Errorf("%w: invalid current provider review decision", ErrInvalidProviderData)
		}
		if err := validateConsistentActor(actorPrincipals, decision.Reviewer); err != nil {
			return err
		}
		if decisions[decision.ID] || reviewers[actorSourceKey(decision.Reviewer)] {
			return fmt.Errorf("%w: duplicate review decision or current reviewer", ErrInvalidProviderData)
		}
		decisions[decision.ID], reviewers[actorSourceKey(decision.Reviewer)] = true, true
	}
	findings := map[string]bool{}
	for _, finding := range r.Findings {
		if err := finding.Validate(); err != nil || finding.SubjectRevision != subject || findings[finding.ID] {
			return fmt.Errorf("%w: invalid or duplicate review finding", ErrInvalidProviderData)
		}
		if err := validateConsistentActor(actorPrincipals, finding.Owner); err != nil {
			return err
		}
		if finding.StateOwner != nil {
			if err := validateConsistentActor(actorPrincipals, *finding.StateOwner); err != nil {
				return err
			}
		}
		findings[finding.ID] = true
	}
	seenConversations := map[string]bool{}
	for _, conversation := range r.UnresolvedConversations {
		if !validOpaqueIdentity(conversation, 256) || seenConversations[conversation] {
			return fmt.Errorf("%w: invalid or duplicate unresolved conversation", ErrInvalidProviderData)
		}
		seenConversations[conversation] = true
	}
	return nil
}

func validateConsistentActor(seen map[string]PrincipalIdentity, actor ActorIdentity) error {
	if err := actor.Validate(); err != nil {
		return err
	}
	key := actorSourceKey(actor)
	if previous, ok := seen[key]; ok && previous != actor.CanonicalPrincipal {
		return fmt.Errorf("%w: actor has conflicting canonical principals", ErrInvalidProviderData)
	}
	seen[key] = actor.CanonicalPrincipal
	return nil
}

type ChangeState string

const (
	ChangeOpen   ChangeState = "open"
	ChangeClosed ChangeState = "closed"
	ChangeMerged ChangeState = "merged"
)

type MergeSnapshotRequest struct {
	Reference               Reference       `json:"reference"`
	ExpectedSubjectRevision string          `json:"expected_subject_revision"`
	RequiredChecks          []CheckIdentity `json:"required_checks"`
}

func (r MergeSnapshotRequest) Validate() error {
	if r.Reference.Validate() != nil || !validOpaqueIdentity(r.ExpectedSubjectRevision, 512) || r.RequiredChecks == nil {
		return fmt.Errorf("%w: merge snapshot request identity is invalid", ErrInvalidProviderData)
	}
	seen := map[string]bool{}
	for _, check := range r.RequiredChecks {
		if check.Validate() != nil || check.Provider != r.Reference.ProviderKey || seen[checkIdentityKey(check)] {
			return fmt.Errorf("%w: invalid or duplicate required check identity", ErrInvalidProviderData)
		}
		seen[checkIdentityKey(check)] = true
	}
	return nil
}

type MergeSnapshot struct {
	ProtocolVersion       string            `json:"protocol_version"`
	SemanticGeneration    string            `json:"semantic_generation"`
	ProviderBuildIdentity string            `json:"provider_build_identity"`
	Reference             Reference         `json:"reference"`
	SubjectRevision       string            `json:"subject_revision"`
	ChangeState           ChangeState       `json:"change_state"`
	CapturedAt            time.Time         `json:"captured_at"`
	Review                ReviewAuthority   `json:"review"`
	Checks                []CheckConclusion `json:"checks"`
	AuthorityToken        string            `json:"authority_token"`
}

func ValidateMergeSnapshot(snapshot MergeSnapshot, request MergeSnapshotRequest) error {
	if request.Validate() != nil || snapshot.ProtocolVersion != ProtocolVersion ||
		snapshot.SemanticGeneration != MergeAuthorityGeneration || !validOpaqueIdentity(snapshot.ProviderBuildIdentity, 256) ||
		snapshot.Reference != request.Reference || snapshot.SubjectRevision != request.ExpectedSubjectRevision ||
		snapshot.CapturedAt.IsZero() || !validOpaqueIdentity(snapshot.AuthorityToken, 4096) {
		return fmt.Errorf("%w: merge snapshot identity or generation mismatch", ErrInvalidProviderData)
	}
	if snapshot.ChangeState != ChangeOpen && snapshot.ChangeState != ChangeClosed && snapshot.ChangeState != ChangeMerged {
		return fmt.Errorf("%w: unsupported change state %q", ErrInvalidProviderData, snapshot.ChangeState)
	}
	if err := snapshot.Review.Validate(snapshot.SubjectRevision); err != nil {
		return err
	}
	required := make(map[string]CheckIdentity, len(request.RequiredChecks))
	if snapshot.Checks == nil {
		return fmt.Errorf("%w: current check conclusions are omitted", ErrInvalidProviderData)
	}
	for _, check := range request.RequiredChecks {
		required[checkIdentityKey(check)] = check
	}
	seen := map[string]bool{}
	for _, conclusion := range snapshot.Checks {
		key := checkIdentityKey(conclusion.Identity)
		if err := conclusion.Validate(); err != nil || conclusion.SubjectRevision != snapshot.SubjectRevision || seen[key] {
			return fmt.Errorf("%w: invalid or duplicate current check conclusion", ErrInvalidProviderData)
		}
		if _, ok := required[key]; !ok {
			return fmt.Errorf("%w: unrequested check conclusion", ErrInvalidProviderData)
		}
		seen[key] = true
	}
	if len(seen) != len(required) {
		return fmt.Errorf("%w: missing current check conclusion", ErrInvalidProviderData)
	}
	return nil
}

type ConditionalMergeRequest struct {
	Reference      Reference `json:"reference"`
	ExpectedHead   string    `json:"expected_head"`
	AuthorityToken string    `json:"authority_token"`
}

func (r ConditionalMergeRequest) Validate() error {
	if r.Reference.Validate() != nil || !validOpaqueIdentity(r.ExpectedHead, 512) || !validOpaqueIdentity(r.AuthorityToken, 4096) {
		return fmt.Errorf("%w: conditional merge request is incomplete", ErrInvalidProviderData)
	}
	return nil
}

type ConditionalMergeResult struct {
	Reference      Reference `json:"reference"`
	ExpectedHead   string    `json:"expected_head"`
	MergeID        string    `json:"merge_id"`
	MergedRevision string    `json:"merged_revision"`
	CanonicalURL   string    `json:"canonical_url"`
}

func ValidateConditionalMergeResult(result ConditionalMergeResult, request ConditionalMergeRequest) error {
	if request.Validate() != nil || result.Reference != request.Reference || result.ExpectedHead != request.ExpectedHead ||
		!validOpaqueIdentity(result.MergeID, 256) || !validOpaqueIdentity(result.MergedRevision, 512) ||
		!safeCanonicalURL(result.CanonicalURL) {
		return fmt.Errorf("%w: conditional merge response identity is invalid", ErrInvalidProviderData)
	}
	return nil
}

type MergeChangeRequest = ConditionalMergeRequest
type MergeChangeResult = ConditionalMergeResult

type MergeAuthorityProvider interface {
	Capabilities(context.Context) (Capabilities, error)
	MergeSnapshot(context.Context, MergeSnapshotRequest) (MergeSnapshot, error)
	MergeChange(context.Context, ConditionalMergeRequest) (ConditionalMergeResult, error)
}

var requiredMergeAuthorityCapabilities = []Capability{
	CapabilityReviewDecision,
	CapabilityAuthoritativeCheckConclusion,
	CapabilityMergeConditional,
}

func RequiredMergeAuthorityCapabilities() []Capability {
	return append([]Capability(nil), requiredMergeAuthorityCapabilities...)
}

func RequireMergeAuthorityCapabilities(ctx context.Context, provider interface {
	Capabilities(context.Context) (Capabilities, error)
}) (Capabilities, error) {
	capabilities, err := provider.Capabilities(ctx)
	if err != nil {
		return Capabilities{}, err
	}
	if err := capabilities.Validate(); err != nil {
		return Capabilities{}, err
	}
	missing := make([]string, 0)
	for _, capability := range requiredMergeAuthorityCapabilities {
		if !capabilities.Has(capability) {
			missing = append(missing, string(capability))
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return Capabilities{}, fmt.Errorf("%w: %s", ErrCapabilityMissing, strings.Join(missing, ", "))
	}
	if capabilities.SemanticGeneration != MergeAuthorityGeneration || !validOpaqueIdentity(capabilities.ProviderBuildIdentity, 256) {
		return Capabilities{}, fmt.Errorf("%w: merge-authority generation handshake is incomplete", ErrInvalidProviderData)
	}
	return capabilities, nil
}

func FetchMergeSnapshot(ctx context.Context, provider MergeAuthorityProvider, request MergeSnapshotRequest) (MergeSnapshot, error) {
	if provider == nil {
		return MergeSnapshot{}, fmt.Errorf("%w: nil provider", ErrProviderNotFound)
	}
	capabilities, err := RequireMergeAuthorityCapabilities(ctx, provider)
	if err != nil {
		return MergeSnapshot{}, err
	}
	if _, err := RequireCanonicalPrincipalMapping(provider); err != nil {
		return MergeSnapshot{}, err
	}
	if err := request.Validate(); err != nil {
		return MergeSnapshot{}, err
	}
	snapshot, err := provider.MergeSnapshot(ctx, request)
	if err != nil {
		return MergeSnapshot{}, err
	}
	if err := ValidateMergeSnapshot(snapshot, request); err != nil {
		return MergeSnapshot{}, err
	}
	if snapshot.ProviderBuildIdentity != capabilities.ProviderBuildIdentity {
		return MergeSnapshot{}, fmt.Errorf("%w: provider build changed after capability discovery", ErrInvalidProviderData)
	}
	return snapshot, nil
}

// MergeChange performs the complete generation handshake before reaching the
// only merge-authority mutation. It never accepts a saved readiness decision.
func MergeChange(ctx context.Context, provider MergeAuthorityProvider, request ConditionalMergeRequest) (ConditionalMergeResult, error) {
	if provider == nil {
		return ConditionalMergeResult{}, fmt.Errorf("%w: nil provider", ErrProviderNotFound)
	}
	if _, err := RequireMergeAuthorityCapabilities(ctx, provider); err != nil {
		return ConditionalMergeResult{}, err
	}
	if err := request.Validate(); err != nil {
		return ConditionalMergeResult{}, err
	}
	result, err := provider.MergeChange(ctx, request)
	if err != nil {
		return ConditionalMergeResult{}, err
	}
	if err := ValidateConditionalMergeResult(result, request); err != nil {
		return ConditionalMergeResult{}, err
	}
	return result, nil
}
