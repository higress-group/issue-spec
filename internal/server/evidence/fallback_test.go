package evidence

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/codereview"
	adminservice "github.com/higress-group/issue-spec/internal/server/admin"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/models"
)

func TestFallbackAuthorityResolvesOneCurrentLeafPerReviewer(t *testing.T) {
	query := fallbackQueryFixture()
	reviewer := fallbackActor(uuid.New(), "person:9")
	first := fallbackReviewEvidence(t, query, reviewer, "decision:1", "", nil, codereview.ReviewChangesRequested)
	predecessorID := first.ID
	second := fallbackReviewEvidence(t, query, reviewer, "decision:2", "decision:1", &predecessorID, codereview.ReviewApproved)

	result, err := resolveFallbackAuthority([]Evidence{second, first}, query, 17)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExternalAuthorityGeneration != "issue:"+query.IssueID.String()+":evidence:17" ||
		len(result.ReviewDecisions) != 1 || result.ReviewDecisions[0].Decision.ID != "decision:2" {
		t.Fatalf("fallback authority = %+v", result)
	}
}

func TestFallbackAuthorityRejectsForkedReviewRoots(t *testing.T) {
	query := fallbackQueryFixture()
	reviewer := fallbackActor(uuid.New(), "person:9")
	left := fallbackReviewEvidence(t, query, reviewer, "decision:left", "", nil, codereview.ReviewApproved)
	right := fallbackReviewEvidence(t, query, reviewer, "decision:right", "", nil, codereview.ReviewApproved)
	if _, err := resolveFallbackAuthority([]Evidence{left, right}, query, 18); !errors.Is(err, ErrFallbackAuthorityConflict) {
		t.Fatalf("fork error = %v", err)
	}
}

func TestFallbackAuthorityAdminRevocationCannotBecomeApproval(t *testing.T) {
	query := fallbackQueryFixture()
	reviewer := fallbackActor(uuid.New(), "person:9")
	decision := fallbackReviewEvidence(t, query, reviewer, "decision:1", "", nil, codereview.ReviewApproved)
	administrator := fallbackActor(uuid.New(), "admin:1")
	payload := fallbackRevocationPayload{SchemaVersion: fallbackRevocationPayloadSchema,
		ChangeID: query.Reference.ChangeID, ID: "revocation:1", SupersedesID: "decision:1",
		SubjectRevision: query.SubjectRevision, TargetType: decision.EvidenceType,
		TargetStream: decision.EvidenceType + "\x00" + decision.ExternalID, Administrator: administrator, Reason: "compromised review credential"}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	adminID, err := uuid.Parse(administrator.StableID)
	if err != nil {
		t.Fatal(err)
	}
	revocation := Evidence{ID: uuid.New(), IssueID: query.IssueID, ProviderKey: query.Reference.ProviderKey,
		ExternalRepositoryID: query.Reference.ExternalRepository, EvidenceType: EvidenceTypeFallbackRevocationV1,
		ExternalID: decision.ExternalID, NormalizedState: "revoked", SubjectRevision: query.SubjectRevision,
		Payload: raw, WriterUserID: adminID, SupersedesEvidenceID: &decision.ID, Visibility: VisibilityRepository}
	result, err := resolveFallbackAuthority([]Evidence{decision, revocation}, query, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ReviewDecisions) != 0 || len(result.CheckAttestations) != 0 {
		t.Fatalf("revoked authority remained usable: %+v", result)
	}
}

func TestFallbackAuthorityRejectsUntrustedExecutorAndLegacyEvidence(t *testing.T) {
	query := fallbackQueryFixture()
	trusted := fallbackActor(uuid.New(), "executor:trusted")
	check := codereview.CheckIdentity{Provider: "external", Key: "durable-spec", Owner: "executor:durable-spec"}
	query.RequiredAttestations = []FallbackCheckRequirement{{Check: check, Executor: trusted}}
	untrusted := fallbackActor(uuid.New(), "executor:other")
	attestation := codereview.CheckAttestation{ID: "attestation:1", SubjectRevision: query.SubjectRevision,
		Check: check, Executor: untrusted, CommandIdentity: "command:durable-spec/v1",
		ProtocolIdentity: "protocol:exit-code/v1", EnvironmentIdentity: "environment:linux-amd64/v1",
		Conclusion: codereview.CheckSuccess}
	item := fallbackCheckEvidence(t, query, attestation, nil)
	if _, err := resolveFallbackAuthority([]Evidence{item}, query, 19); !errors.Is(err, ErrFallbackAuthorityConflict) {
		t.Fatalf("executor mismatch error = %v", err)
	}

	legacy := item
	legacy.ID = uuid.New()
	legacy.EvidenceType = "check"
	if _, err := decodeFallbackNode(legacy, query); !errors.Is(err, ErrFallbackAuthorityConflict) {
		t.Fatalf("legacy decode error = %v", err)
	}
}

func TestFallbackQueryRequiresAtomicProviderGenerationBinding(t *testing.T) {
	query := fallbackQueryFixture()
	query.AtomicBinding.EnforcementMode = "expected_head_only"
	if err := validateFallbackQuery(query); !errors.Is(err, ErrAtomicFallbackUnsupported) {
		t.Fatalf("unsupported binding error = %v", err)
	}
	query = fallbackQueryFixture()
	query.AtomicBinding.ProviderKey = "other"
	if err := validateFallbackQuery(query); !errors.Is(err, ErrAtomicFallbackUnsupported) {
		t.Fatalf("wrong provider binding error = %v", err)
	}
}

func TestTypedFallbackWriterBindsAuthenticatedSourceIdentityBeforeWrite(t *testing.T) {
	query := fallbackQueryFixture()
	userID := uuid.New()
	actor := adminservice.Actor{UserID: userID, IdentityKey: "user:" + userID.String(), RequestID: "request-1"}
	decision := codereview.ReviewDecision{ID: "decision:1", SubjectRevision: query.SubjectRevision,
		Reviewer: fallbackActor(uuid.New(), "person:9"), Verdict: codereview.ReviewApproved, ObservationID: "observation:1"}
	service := &Service{}
	if _, err := service.AppendReviewDecisionFallback(t.Context(), authz.Subject{}, actor, models.RepoScope{},
		ReviewDecisionFallbackInput{IssueID: query.IssueID, Reference: query.Reference, Decision: decision,
			ObservedAt: time.Now().UTC()}); !errors.Is(err, adminservice.ErrInvalidInput) {
		t.Fatalf("forged reviewer error = %v", err)
	}
}

func fallbackQueryFixture() FallbackAuthorityQuery {
	reference := codereview.Reference{ProviderKey: "code.example", ExternalRepository: "acme/widgets", ChangeID: "42"}
	return FallbackAuthorityQuery{IssueID: uuid.New(), Reference: reference, SubjectRevision: "abc123",
		ReviewFallbackEnabled: true, AtomicBinding: ExternalAuthorityBinding{ProviderKey: reference.ProviderKey,
			EnforcementMode: ExternalAuthorityEnforcementToken}}
}

func fallbackActor(userID uuid.UUID, principal string) codereview.ActorIdentity {
	return codereview.ActorIdentity{Provider: IssueServerActorProvider, StableID: userID.String(), Kind: codereview.ActorHuman,
		CanonicalPrincipal: codereview.PrincipalIdentity{Realm: "people.example", StableID: principal}}
}

func fallbackReviewEvidence(t *testing.T, query FallbackAuthorityQuery, reviewer codereview.ActorIdentity,
	id, supersedes string, predecessor *uuid.UUID, verdict codereview.ReviewVerdict) Evidence {
	t.Helper()
	decision := codereview.ReviewDecision{ID: id, SupersedesID: supersedes, SubjectRevision: query.SubjectRevision,
		Reviewer: reviewer, Verdict: verdict, ObservationID: "observation:" + id}
	payload, err := json.Marshal(reviewFallbackPayload{SchemaVersion: reviewFallbackPayloadSchema,
		ChangeID: query.Reference.ChangeID, Decision: decision, Findings: []codereview.ReviewFinding{}})
	if err != nil {
		t.Fatal(err)
	}
	userID, err := uuid.Parse(reviewer.StableID)
	if err != nil {
		t.Fatal(err)
	}
	return Evidence{ID: uuid.New(), IssueID: query.IssueID, ProviderKey: query.Reference.ProviderKey,
		ExternalRepositoryID: query.Reference.ExternalRepository, EvidenceType: EvidenceTypeReviewDecisionFallbackV1,
		ExternalID: fallbackStreamID("reviewer", reviewer.Provider, reviewer.StableID), NormalizedState: string(verdict),
		SubjectRevision: query.SubjectRevision, Payload: payload, WriterUserID: userID,
		SupersedesEvidenceID: predecessor, Visibility: VisibilityRepository}
}

func fallbackCheckEvidence(t *testing.T, query FallbackAuthorityQuery, attestation codereview.CheckAttestation,
	predecessor *uuid.UUID) Evidence {
	t.Helper()
	payload, err := json.Marshal(checkAttestationPayload{SchemaVersion: checkAttestationPayloadSchema,
		ChangeID: query.Reference.ChangeID, Attestation: attestation})
	if err != nil {
		t.Fatal(err)
	}
	userID, err := uuid.Parse(attestation.Executor.StableID)
	if err != nil {
		t.Fatal(err)
	}
	return Evidence{ID: uuid.New(), IssueID: query.IssueID, ProviderKey: query.Reference.ProviderKey,
		ExternalRepositoryID: query.Reference.ExternalRepository, EvidenceType: EvidenceTypeCheckAttestationV1,
		ExternalID: fallbackStreamID("executor-check", attestation.Executor.Provider, attestation.Executor.StableID,
			checkAuthorityKey(attestation.Check)), NormalizedState: string(attestation.Conclusion),
		SubjectRevision: query.SubjectRevision, Payload: payload, WriterUserID: userID,
		SupersedesEvidenceID: predecessor, Visibility: VisibilityRepository}
}
