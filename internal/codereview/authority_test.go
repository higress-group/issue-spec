package codereview

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func validMergeCapabilities() Capabilities {
	return Capabilities{ProtocolVersion: ProtocolVersion, SemanticGeneration: MergeAuthorityGeneration,
		ProviderBuildIdentity: "code.example@sha256:0123456789abcdef",
		Values:                RequiredMergeAuthorityCapabilities()}
}

func validMergeRequest() MergeSnapshotRequest {
	return MergeSnapshotRequest{Reference: Reference{ProviderKey: "code.example", ExternalRepository: "acme/widgets", ChangeID: "42"},
		ExpectedSubjectRevision: "abc123", RequiredChecks: []CheckIdentity{
			{Provider: "code.example", Key: "app:42/context:unit", Owner: "app:42", DisplayName: "unit"},
			{Provider: "code.example", Key: "app:91/context:unit", Owner: "app:91", DisplayName: "unit"},
		}}
}

func validMergeSnapshot(request MergeSnapshotRequest) MergeSnapshot {
	author := ActorIdentity{Provider: "code.example", StableID: "user:7",
		CanonicalPrincipal: PrincipalIdentity{Realm: "people.example", StableID: "person:7"}, Kind: ActorHuman, Display: "Author"}
	reviewer := ActorIdentity{Provider: "code.example", StableID: "user:9",
		CanonicalPrincipal: PrincipalIdentity{Realm: "people.example", StableID: "person:9"}, Kind: ActorHuman, Display: "Reviewer"}
	checks := make([]CheckConclusion, 0, len(request.RequiredChecks))
	for i, check := range request.RequiredChecks {
		checks = append(checks, CheckConclusion{Identity: check, SubjectRevision: request.ExpectedSubjectRevision,
			CurrentAttemptID: "attempt:" + string(rune('1'+i)), ConfigurationGeneration: "ruleset:3", Conclusion: CheckSuccess})
	}
	return MergeSnapshot{ProtocolVersion: ProtocolVersion, SemanticGeneration: MergeAuthorityGeneration,
		ProviderBuildIdentity: "code.example@sha256:0123456789abcdef", Reference: request.Reference,
		SubjectRevision: request.ExpectedSubjectRevision, ChangeState: ChangeOpen, CapturedAt: time.Date(2026, 8, 2, 1, 2, 3, 0, time.UTC),
		Review: ReviewAuthority{Mode: ReviewProviderNative, AuthorSetComplete: true, Authors: []ActorIdentity{author},
			Policy: ReviewPolicy{RequiredApprovalCount: 1, CodeOwnerApprovalRequired: true,
				DismissStaleApprovals: true, ConversationResolutionRequired: true},
			Decisions: []ReviewDecision{{ID: "review:9", SubjectRevision: request.ExpectedSubjectRevision,
				Reviewer: reviewer, Verdict: ReviewApproved, ObservationID: "observation:11"}},
			Findings: []ReviewFinding{}, UnresolvedConversations: []string{}, CodeOwnerSatisfied: true},
		Checks: checks, AuthorityToken: "authority-token:opaque-17"}
}

func TestMergeAuthorityCapabilitiesRequireClosedGeneration(t *testing.T) {
	valid := validMergeCapabilities()
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		edit func(*Capabilities)
	}{
		{name: "missing generation", edit: func(c *Capabilities) { c.SemanticGeneration = "" }},
		{name: "wrong generation", edit: func(c *Capabilities) { c.SemanticGeneration = "minimal-merge-authority/v2" }},
		{name: "missing build", edit: func(c *Capabilities) { c.ProviderBuildIdentity = "" }},
		{name: "runtime build", edit: func(c *Capabilities) { c.ProviderBuildIdentity = "build session 9" }},
		{name: "unknown capability", edit: func(c *Capabilities) { c.Values = append(c.Values, Capability("merge.approved")) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			candidate.Values = append([]Capability(nil), valid.Values...)
			test.edit(&candidate)
			if err := candidate.Validate(); !errors.Is(err, ErrInvalidProviderData) {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func TestFetchMergeSnapshotRejectsMixedBridgeBeforeAuthority(t *testing.T) {
	provider := &fakeMergeAuthorityProvider{capabilities: Capabilities{ProtocolVersion: ProtocolVersion,
		Values: []Capability{CapabilityEvidenceSnapshot}}}
	if _, err := FetchMergeSnapshot(t.Context(), provider, validMergeRequest()); !errors.Is(err, ErrCapabilityMissing) {
		t.Fatalf("FetchMergeSnapshot() error = %v", err)
	}
	if provider.snapshots.Load() != 0 {
		t.Fatal("old bridge was asked for usable authority")
	}

	provider.capabilities = validMergeCapabilities()
	provider.capabilities.Values = provider.capabilities.Values[:2]
	if _, err := MergeChange(t.Context(), provider, ConditionalMergeRequest{Reference: validMergeRequest().Reference,
		ExpectedHead: "abc123", AuthorityToken: "token:1"}); !errors.Is(err, ErrCapabilityMissing) {
		t.Fatalf("MergeChange() error = %v", err)
	}
	if provider.merges.Load() != 0 {
		t.Fatal("incomplete new bridge reached mutation")
	}
}

func TestValidateMergeSnapshotUsesOpaqueCheckOwnerIdentity(t *testing.T) {
	request := validMergeRequest()
	snapshot := validMergeSnapshot(request)
	if err := ValidateMergeSnapshot(snapshot, request); err != nil {
		t.Fatal(err)
	}
	// Both checks have the same display name. Swapping one owner cannot satisfy
	// the requested identity even though the human-facing name still matches.
	snapshot.Checks[0].Identity.Owner = snapshot.Checks[1].Identity.Owner
	if err := ValidateMergeSnapshot(snapshot, request); !errors.Is(err, ErrInvalidProviderData) {
		t.Fatalf("owner substitution error = %v", err)
	}
}

func TestReviewAuthorityRejectsUnmappedAndWriterSubstitution(t *testing.T) {
	request := validMergeRequest()
	snapshot := validMergeSnapshot(request)
	snapshot.Review.Decisions[0].Reviewer.CanonicalPrincipal = PrincipalIdentity{}
	if err := ValidateMergeSnapshot(snapshot, request); !errors.Is(err, ErrInvalidProviderData) {
		t.Fatalf("unmapped reviewer error = %v", err)
	}

	snapshot = validMergeSnapshot(request)
	snapshot.Review.Decisions[0].Reviewer = ActorIdentity{Provider: "code.example", StableID: "bridge-writer",
		Kind: ActorService, Display: "transport"}
	if err := ValidateMergeSnapshot(snapshot, request); !errors.Is(err, ErrInvalidProviderData) {
		t.Fatalf("writer substitution error = %v", err)
	}
}

func TestReviewPolicyWireRequiresEveryClosedRule(t *testing.T) {
	var policy ReviewPolicy
	if err := json.Unmarshal([]byte(`{"required_approval_count":1,"code_owner_approval_required":false,"dismiss_stale_approvals":false}`), &policy); !errors.Is(err, ErrInvalidProviderData) {
		t.Fatalf("omitted policy rule error = %v", err)
	}
	if err := json.Unmarshal([]byte(`{"required_approval_count":1,"code_owner_approval_required":false,"dismiss_stale_approvals":false,"conversation_resolution_required":false,"approved":true}`), &policy); err == nil {
		t.Fatal("unknown opaque approval field was accepted")
	}
}

func TestCommandProviderMergeAuthorityRoundTrip(t *testing.T) {
	provider := commandTestProvider(t, "authority", 1<<20, 10*time.Second)
	request := validMergeRequest()
	snapshot, err := FetchMergeSnapshot(t.Context(), provider, request)
	if err != nil || snapshot.AuthorityToken != "authority-token:opaque-17" || len(snapshot.Checks) != 2 {
		t.Fatalf("FetchMergeSnapshot() = %+v, %v", snapshot, err)
	}
	result, err := MergeChange(t.Context(), provider, ConditionalMergeRequest{Reference: request.Reference,
		ExpectedHead: request.ExpectedSubjectRevision, AuthorityToken: snapshot.AuthorityToken})
	if err != nil || result.MergeID != "merge-42" || result.ExpectedHead != request.ExpectedSubjectRevision {
		t.Fatalf("MergeChange() = %+v, %v", result, err)
	}
}

func TestPrincipalMapperRejectsAmbiguousSourceAndSupportsCrossDomainIdentity(t *testing.T) {
	principal := PrincipalIdentity{Realm: "people.example", StableID: "person:9"}
	mapper, err := NewPrincipalMapper([]PrincipalMapping{
		{Provider: "code.example", StableID: "user:9", Principal: principal},
		{Provider: "issue.example", StableID: "subject:44", Principal: principal},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := mapper.Map("issue.example", "subject:44"); err != nil || got != principal {
		t.Fatalf("Map() = %+v, %v", got, err)
	}
	_, err = NewPrincipalMapper([]PrincipalMapping{
		{Provider: "code.example", StableID: "user:9", Principal: principal},
		{Provider: "code.example", StableID: "user:9", Principal: PrincipalIdentity{Realm: "other", StableID: "9"}},
	})
	if !errors.Is(err, ErrInvalidProviderData) || !strings.Contains(err.Error(), "conflicting") {
		t.Fatalf("ambiguous mapping error = %v", err)
	}
}

type fakeMergeAuthorityProvider struct {
	capabilities Capabilities
	snapshots    atomic.Int64
	merges       atomic.Int64
}

func (f *fakeMergeAuthorityProvider) Capabilities(context.Context) (Capabilities, error) {
	return f.capabilities, nil
}

func (f *fakeMergeAuthorityProvider) MergeSnapshot(_ context.Context, request MergeSnapshotRequest) (MergeSnapshot, error) {
	f.snapshots.Add(1)
	return validMergeSnapshot(request), nil
}

func (f *fakeMergeAuthorityProvider) MergeChange(_ context.Context, request ConditionalMergeRequest) (ConditionalMergeResult, error) {
	f.merges.Add(1)
	return ConditionalMergeResult{Reference: request.Reference, ExpectedHead: request.ExpectedHead,
		MergeID: "merge:1", MergedRevision: "merge789", CanonicalURL: "https://code.example/acme/widgets/change/42"}, nil
}
