package evidence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/codereview"
	adminservice "github.com/higress-group/issue-spec/internal/server/admin"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/jackc/pgx/v5"
)

const (
	reviewFallbackPayloadSchema     = "issue-spec.review-decision-fallback/v1"
	checkAttestationPayloadSchema   = "issue-spec.check-attestation/v1"
	fallbackRevocationPayloadSchema = "issue-spec.fallback-revocation/v1"
	fallbackProvenanceSchema        = "issue-spec.fallback-authority-provenance/v1"
)

type reviewFallbackPayload struct {
	SchemaVersion string                     `json:"schema_version"`
	ChangeID      string                     `json:"change_id"`
	Decision      codereview.ReviewDecision  `json:"decision"`
	Findings      []codereview.ReviewFinding `json:"findings"`
}

type checkAttestationPayload struct {
	SchemaVersion string                      `json:"schema_version"`
	ChangeID      string                      `json:"change_id"`
	Attestation   codereview.CheckAttestation `json:"attestation"`
}

type fallbackProvenance struct {
	SchemaVersion      string `json:"schema_version"`
	ProtocolVersion    string `json:"protocol_version"`
	SemanticGeneration string `json:"semantic_generation"`
}

type fallbackRevocationPayload struct {
	SchemaVersion   string                   `json:"schema_version"`
	ChangeID        string                   `json:"change_id"`
	ID              string                   `json:"id"`
	SupersedesID    string                   `json:"supersedes_id"`
	SubjectRevision string                   `json:"subject_revision"`
	TargetType      string                   `json:"target_type"`
	TargetStream    string                   `json:"target_stream"`
	Administrator   codereview.ActorIdentity `json:"administrator"`
	Reason          string                   `json:"reason"`
}

func reservedFallbackEvidenceType(value string) bool {
	return value == EvidenceTypeReviewDecisionFallbackV1 || value == EvidenceTypeCheckAttestationV1 ||
		value == EvidenceTypeFallbackRevocationV1
}

func (s *Service) AppendReviewDecisionFallback(ctx context.Context, subject authz.Subject, actor adminservice.Actor,
	scope models.RepoScope, input ReviewDecisionFallbackInput) (Evidence, error) {
	if input.Visibility == "" {
		input.Visibility = VisibilityRepository
	}
	if input.IssueID == uuid.Nil || input.Reference.Validate() != nil || input.Decision.Validate() != nil ||
		input.Decision.Reviewer.Provider != IssueServerActorProvider || input.Decision.Reviewer.StableID != actor.UserID.String() ||
		input.ObservedAt.IsZero() || (input.Decision.SupersedesID == "") != (input.SupersedesEvidenceID == nil) ||
		(input.Visibility != VisibilityRepository && input.Visibility != VisibilityMaintainers) {
		return Evidence{}, adminservice.ErrInvalidInput
	}
	seenFindings := map[string]bool{}
	for _, finding := range input.Findings {
		if finding.Validate() != nil || finding.SubjectRevision != input.Decision.SubjectRevision || seenFindings[finding.ID] ||
			actorAuthorityKey(finding.Owner) != actorAuthorityKey(input.Decision.Reviewer) ||
			(finding.StateOwner != nil && actorAuthorityKey(*finding.StateOwner) != actorAuthorityKey(input.Decision.Reviewer)) {
			return Evidence{}, adminservice.ErrInvalidInput
		}
		seenFindings[finding.ID] = true
	}
	payload, err := json.Marshal(reviewFallbackPayload{SchemaVersion: reviewFallbackPayloadSchema,
		ChangeID: input.Reference.ChangeID, Decision: input.Decision, Findings: input.Findings})
	if err != nil {
		return Evidence{}, err
	}
	provenance, err := json.Marshal(newFallbackProvenance())
	if err != nil {
		return Evidence{}, err
	}
	appendInput := AppendInput{IssueID: input.IssueID, ProviderKey: input.Reference.ProviderKey,
		ExternalRepositoryID: input.Reference.ExternalRepository, EvidenceType: EvidenceTypeReviewDecisionFallbackV1,
		ExternalID: fallbackStreamID("reviewer", input.Decision.Reviewer.Provider, input.Decision.Reviewer.StableID),
		IngestKey: fallbackIngestKey(EvidenceTypeReviewDecisionFallbackV1, input.IssueID, input.Reference,
			input.Decision.SubjectRevision, input.Decision.ID), NormalizedState: string(input.Decision.Verdict),
		SubjectRevision: input.Decision.SubjectRevision, ObservedAt: input.ObservedAt,
		Payload: payload, Provenance: provenance, SupersedesEvidenceID: input.SupersedesEvidenceID, Visibility: input.Visibility}
	return s.appendEvidence(ctx, subject, actor, scope, appendInput, true, authz.OperationPublishEvidence)
}

func (s *Service) AppendCheckAttestation(ctx context.Context, subject authz.Subject, actor adminservice.Actor,
	scope models.RepoScope, input CheckAttestationInput) (Evidence, error) {
	if input.Visibility == "" {
		input.Visibility = VisibilityRepository
	}
	attestation := input.Attestation
	if input.IssueID == uuid.Nil || input.Reference.Validate() != nil || attestation.Validate() != nil ||
		attestation.Executor.Provider != IssueServerActorProvider || attestation.Executor.StableID != actor.UserID.String() ||
		input.ObservedAt.IsZero() || (attestation.SupersedesID == "") != (input.SupersedesEvidenceID == nil) ||
		(input.Visibility != VisibilityRepository && input.Visibility != VisibilityMaintainers) {
		return Evidence{}, adminservice.ErrInvalidInput
	}
	payload, err := json.Marshal(checkAttestationPayload{SchemaVersion: checkAttestationPayloadSchema,
		ChangeID: input.Reference.ChangeID, Attestation: attestation})
	if err != nil {
		return Evidence{}, err
	}
	provenance, err := json.Marshal(newFallbackProvenance())
	if err != nil {
		return Evidence{}, err
	}
	appendInput := AppendInput{IssueID: input.IssueID, ProviderKey: input.Reference.ProviderKey,
		ExternalRepositoryID: input.Reference.ExternalRepository, EvidenceType: EvidenceTypeCheckAttestationV1,
		ExternalID: fallbackStreamID("executor-check", attestation.Executor.Provider, attestation.Executor.StableID,
			checkAuthorityKey(attestation.Check)),
		IngestKey: fallbackIngestKey(EvidenceTypeCheckAttestationV1, input.IssueID, input.Reference,
			attestation.SubjectRevision, attestation.ID), NormalizedState: string(attestation.Conclusion),
		SubjectRevision: attestation.SubjectRevision, ObservedAt: input.ObservedAt,
		Payload: payload, Provenance: provenance, SupersedesEvidenceID: input.SupersedesEvidenceID, Visibility: input.Visibility}
	return s.appendEvidence(ctx, subject, actor, scope, appendInput, true, authz.OperationPublishEvidence)
}

func (s *Service) AppendFallbackRevocation(ctx context.Context, subject authz.Subject, actor adminservice.Actor,
	scope models.RepoScope, input FallbackRevocationInput) (Evidence, error) {
	if input.Visibility == "" {
		input.Visibility = VisibilityRepository
	}
	if input.IssueID == uuid.Nil || input.Reference.Validate() != nil || !validFallbackOpaque(input.SubjectRevision, 512) ||
		!validFallbackOpaque(input.ID, 256) || input.TargetEvidenceID == uuid.Nil || input.Administrator.Validate() != nil ||
		input.Administrator.Provider != IssueServerActorProvider || input.Administrator.StableID != actor.UserID.String() ||
		strings.TrimSpace(input.Reason) == "" || len(input.Reason) > 1024 || strings.ContainsRune(input.Reason, 0) ||
		input.ObservedAt.IsZero() || (input.Visibility != VisibilityRepository && input.Visibility != VisibilityMaintainers) {
		return Evidence{}, adminservice.ErrInvalidInput
	}
	decision, err := s.authz.EvaluateRepository(ctx, subject, authz.RepositoryRequest{Scope: scope, Operation: authz.OperationAdminRepository})
	if err != nil {
		return Evidence{}, err
	}
	if err := decision.AuthorizationError(); err != nil {
		return Evidence{}, err
	}
	target, err := scanEvidence(s.pool.QueryRow(ctx, `SELECT `+evidenceColumns+` FROM external_evidence
		WHERE organization_id = $1 AND repository_id = $2 AND issue_id = $3 AND id = $4
		AND provider_key = $5 AND external_repository_id = $6 AND subject_revision = $7
		AND evidence_type IN ($8, $9)`, scope.OrgID, scope.RepoID, input.IssueID, input.TargetEvidenceID,
		input.Reference.ProviderKey, input.Reference.ExternalRepository, input.SubjectRevision,
		EvidenceTypeReviewDecisionFallbackV1, EvidenceTypeCheckAttestationV1))
	if err != nil {
		return Evidence{}, mapError(err)
	}
	if input.Visibility != target.Visibility {
		return Evidence{}, adminservice.ErrInvalidInput
	}
	query := FallbackAuthorityQuery{IssueID: input.IssueID, Reference: input.Reference, SubjectRevision: input.SubjectRevision,
		AtomicBinding: ExternalAuthorityBinding{ProviderKey: input.Reference.ProviderKey, EnforcementMode: ExternalAuthorityEnforcementToken}}
	targetNode, err := decodeFallbackNode(target, query)
	if err != nil {
		return Evidence{}, adminservice.ErrConflict
	}
	payload, err := json.Marshal(fallbackRevocationPayload{SchemaVersion: fallbackRevocationPayloadSchema,
		ChangeID: input.Reference.ChangeID, ID: input.ID, SupersedesID: targetNode.recordID,
		SubjectRevision: input.SubjectRevision, TargetType: target.EvidenceType, TargetStream: targetNode.stream,
		Administrator: input.Administrator, Reason: strings.TrimSpace(input.Reason)})
	if err != nil {
		return Evidence{}, err
	}
	provenance, err := json.Marshal(newFallbackProvenance())
	if err != nil {
		return Evidence{}, err
	}
	appendInput := AppendInput{IssueID: input.IssueID, ProviderKey: input.Reference.ProviderKey,
		ExternalRepositoryID: input.Reference.ExternalRepository, EvidenceType: EvidenceTypeFallbackRevocationV1,
		ExternalID: target.ExternalID, IngestKey: fallbackIngestKey(EvidenceTypeFallbackRevocationV1, input.IssueID,
			input.Reference, input.SubjectRevision, input.ID), NormalizedState: "revoked", SubjectRevision: input.SubjectRevision,
		ObservedAt: input.ObservedAt, Payload: payload, Provenance: provenance,
		SupersedesEvidenceID: &input.TargetEvidenceID, Visibility: input.Visibility}
	return s.appendEvidence(ctx, subject, actor, scope, appendInput, true, authz.OperationAdminRepository)
}

func validFallbackOpaque(value string, limit int) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > limit {
		return false
	}
	for _, r := range value {
		if r < 0x21 || r > 0x7e {
			return false
		}
	}
	return true
}

func newFallbackProvenance() fallbackProvenance {
	return fallbackProvenance{SchemaVersion: fallbackProvenanceSchema,
		ProtocolVersion: codereview.ProtocolVersion, SemanticGeneration: codereview.MergeAuthorityGeneration}
}

func fallbackStreamID(prefix string, values ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(append([]string{prefix}, values...), "\x00")))
	return prefix + ":v1:" + hex.EncodeToString(digest[:])
}

func fallbackIngestKey(kind string, issueID uuid.UUID, reference codereview.Reference, subject, recordID string) string {
	material := strings.Join([]string{codereview.MergeAuthorityGeneration, kind, issueID.String(), reference.ProviderKey,
		reference.ExternalRepository, reference.ChangeID, subject, recordID}, "\x00")
	digest := sha256.Sum256([]byte(material))
	return "fallback-authority:v1:" + hex.EncodeToString(digest[:])
}

func actorAuthorityKey(actor codereview.ActorIdentity) string {
	return actor.Provider + "\x00" + actor.StableID + "\x00" + actor.CanonicalPrincipal.Realm + "\x00" + actor.CanonicalPrincipal.StableID
}

func checkAuthorityKey(check codereview.CheckIdentity) string {
	return check.Provider + "\x00" + check.Key + "\x00" + check.Owner
}

// FallbackAuthority reads only the two new strict evidence types and resolves
// one active immutable leaf per logical reviewer/executor stream. The atomic
// binding is validated before any authority rows are read.
func (s *Service) FallbackAuthority(ctx context.Context, subject authz.Subject, scope models.RepoScope,
	query FallbackAuthorityQuery) (FallbackAuthority, error) {
	if err := validateFallbackQuery(query); err != nil {
		return FallbackAuthority{}, err
	}
	decision, err := s.authz.EvaluateRepository(ctx, subject, authz.RepositoryRequest{Scope: scope, Operation: authz.OperationRead})
	if err != nil {
		return FallbackAuthority{}, err
	}
	if err := decision.AuthorizationError(); err != nil {
		return FallbackAuthority{}, err
	}
	var result FallbackAuthority
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var generation int64
		if err := tx.QueryRow(ctx, `SELECT evidence_collection_version FROM issues
			WHERE organization_id = $1 AND repository_id = $2 AND id = $3`, scope.OrgID, scope.RepoID, query.IssueID).Scan(&generation); err != nil {
			return err
		}
		sql := `SELECT ` + evidenceColumns + ` FROM external_evidence WHERE organization_id = $1 AND repository_id = $2
			AND issue_id = $3 AND provider_key = $4 AND external_repository_id = $5 AND subject_revision = $6
			AND evidence_type IN ($7, $8, $9)`
		if decision.EffectivePermission < authz.PermissionMaintain {
			sql += ` AND visibility = 'repository'`
		}
		sql += ` ORDER BY created_at, id`
		rows, err := tx.Query(ctx, sql, scope.OrgID, scope.RepoID, query.IssueID, query.Reference.ProviderKey,
			query.Reference.ExternalRepository, query.SubjectRevision,
			EvidenceTypeReviewDecisionFallbackV1, EvidenceTypeCheckAttestationV1, EvidenceTypeFallbackRevocationV1)
		if err != nil {
			return err
		}
		defer rows.Close()
		items := make([]Evidence, 0)
		for rows.Next() {
			item, scanErr := scanEvidence(rows)
			if scanErr != nil {
				return scanErr
			}
			items = append(items, item)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		result, err = resolveFallbackAuthority(items, query, generation)
		return err
	})
	return result, mapError(err)
}

func validateFallbackQuery(query FallbackAuthorityQuery) error {
	if query.IssueID == uuid.Nil || query.Reference.Validate() != nil || strings.TrimSpace(query.SubjectRevision) == "" ||
		query.AtomicBinding.ProviderKey != query.Reference.ProviderKey ||
		query.AtomicBinding.EnforcementMode != ExternalAuthorityEnforcementToken {
		return ErrAtomicFallbackUnsupported
	}
	seen := map[string]bool{}
	for _, requirement := range query.RequiredAttestations {
		if requirement.Check.Validate() != nil || requirement.Executor.Validate() != nil {
			return adminservice.ErrInvalidInput
		}
		key := checkAuthorityKey(requirement.Check)
		if seen[key] {
			return adminservice.ErrInvalidInput
		}
		seen[key] = true
	}
	return nil
}

type fallbackNode struct {
	evidence    Evidence
	review      *FallbackReviewDecision
	attestation *codereview.CheckAttestation
	revocation  *fallbackRevocationPayload
	recordID    string
	supersedes  string
	stream      string
}

func resolveFallbackAuthority(items []Evidence, query FallbackAuthorityQuery, generation int64) (FallbackAuthority, error) {
	result := FallbackAuthority{ExternalAuthorityGeneration: fmt.Sprintf("issue:%s:evidence:%d", query.IssueID, generation),
		ReviewDecisions: []FallbackReviewDecision{}, CheckAttestations: []codereview.CheckAttestation{}}
	nodes := make(map[uuid.UUID]fallbackNode, len(items))
	stableIDs := map[string]bool{}
	for _, item := range items {
		node, err := decodeFallbackNode(item, query)
		if err != nil || stableIDs[item.EvidenceType+"\x00"+node.recordID] {
			return FallbackAuthority{}, ErrFallbackAuthorityConflict
		}
		stableIDs[item.EvidenceType+"\x00"+node.recordID] = true
		nodes[item.ID] = node
	}
	children := map[uuid.UUID]int{}
	for _, node := range nodes {
		if node.evidence.SupersedesEvidenceID == nil {
			if node.supersedes != "" {
				return FallbackAuthority{}, ErrFallbackAuthorityConflict
			}
			continue
		}
		predecessor, ok := nodes[*node.evidence.SupersedesEvidenceID]
		if !ok || predecessor.stream != node.stream || (node.revocation == nil && predecessor.evidence.EvidenceType != node.evidence.EvidenceType) ||
			predecessor.recordID != node.supersedes || (node.revocation == nil && predecessor.evidence.WriterUserID != node.evidence.WriterUserID) {
			return FallbackAuthority{}, ErrFallbackAuthorityConflict
		}
		children[predecessor.evidence.ID]++
		if children[predecessor.evidence.ID] > 1 {
			return FallbackAuthority{}, ErrFallbackAuthorityConflict
		}
	}
	leaves := map[string][]fallbackNode{}
	streams := map[string]bool{}
	for id, node := range nodes {
		streams[node.stream] = true
		if children[id] == 0 {
			leaves[node.stream] = append(leaves[node.stream], node)
		}
	}
	if len(leaves) != len(streams) {
		return FallbackAuthority{}, ErrFallbackAuthorityConflict
	}
	for _, nodes := range leaves {
		if len(nodes) != 1 {
			return FallbackAuthority{}, ErrFallbackAuthorityConflict
		}
		node := nodes[0]
		if node.review != nil {
			if query.ReviewFallbackEnabled {
				result.ReviewDecisions = append(result.ReviewDecisions, *node.review)
			}
			continue
		}
		if node.attestation != nil {
			for _, requirement := range query.RequiredAttestations {
				if checkAuthorityKey(requirement.Check) != checkAuthorityKey(node.attestation.Check) {
					continue
				}
				if actorAuthorityKey(requirement.Executor) != actorAuthorityKey(node.attestation.Executor) {
					return FallbackAuthority{}, ErrFallbackAuthorityConflict
				}
				result.CheckAttestations = append(result.CheckAttestations, *node.attestation)
			}
		}
		// A revocation tombstone intentionally contributes no approval or check.
	}
	sort.Slice(result.ReviewDecisions, func(i, j int) bool {
		return result.ReviewDecisions[i].Decision.ID < result.ReviewDecisions[j].Decision.ID
	})
	sort.Slice(result.CheckAttestations, func(i, j int) bool {
		return checkAuthorityKey(result.CheckAttestations[i].Check) < checkAuthorityKey(result.CheckAttestations[j].Check)
	})
	return result, nil
}

func decodeFallbackNode(item Evidence, query FallbackAuthorityQuery) (fallbackNode, error) {
	if item.IssueID != query.IssueID || item.ProviderKey != query.Reference.ProviderKey ||
		item.ExternalRepositoryID != query.Reference.ExternalRepository || item.SubjectRevision != query.SubjectRevision {
		return fallbackNode{}, ErrFallbackAuthorityConflict
	}
	switch item.EvidenceType {
	case EvidenceTypeReviewDecisionFallbackV1:
		var payload reviewFallbackPayload
		if err := decodeStrictFallback(item.Payload, &payload); err != nil || payload.SchemaVersion != reviewFallbackPayloadSchema ||
			payload.ChangeID != query.Reference.ChangeID || payload.Decision.Validate() != nil ||
			payload.Decision.SubjectRevision != query.SubjectRevision || payload.Decision.Reviewer.Provider != IssueServerActorProvider ||
			payload.Decision.Reviewer.StableID != item.WriterUserID.String() || item.NormalizedState != string(payload.Decision.Verdict) ||
			item.ExternalID != fallbackStreamID("reviewer", payload.Decision.Reviewer.Provider, payload.Decision.Reviewer.StableID) {
			return fallbackNode{}, ErrFallbackAuthorityConflict
		}
		seen := map[string]bool{}
		for _, finding := range payload.Findings {
			if finding.Validate() != nil || finding.SubjectRevision != query.SubjectRevision || seen[finding.ID] ||
				actorAuthorityKey(finding.Owner) != actorAuthorityKey(payload.Decision.Reviewer) {
				return fallbackNode{}, ErrFallbackAuthorityConflict
			}
			seen[finding.ID] = true
		}
		review := FallbackReviewDecision{Decision: payload.Decision, Findings: payload.Findings}
		return fallbackNode{evidence: item, review: &review, recordID: payload.Decision.ID,
			supersedes: payload.Decision.SupersedesID, stream: item.EvidenceType + "\x00" + item.ExternalID}, nil
	case EvidenceTypeCheckAttestationV1:
		var payload checkAttestationPayload
		attestation := &payload.Attestation
		if err := decodeStrictFallback(item.Payload, &payload); err != nil || payload.SchemaVersion != checkAttestationPayloadSchema ||
			payload.ChangeID != query.Reference.ChangeID || attestation.Validate() != nil ||
			attestation.SubjectRevision != query.SubjectRevision || attestation.Executor.Provider != IssueServerActorProvider ||
			attestation.Executor.StableID != item.WriterUserID.String() || item.NormalizedState != string(attestation.Conclusion) ||
			item.ExternalID != fallbackStreamID("executor-check", attestation.Executor.Provider, attestation.Executor.StableID,
				checkAuthorityKey(attestation.Check)) {
			return fallbackNode{}, ErrFallbackAuthorityConflict
		}
		return fallbackNode{evidence: item, attestation: attestation, recordID: attestation.ID,
			supersedes: attestation.SupersedesID, stream: item.EvidenceType + "\x00" + item.ExternalID}, nil
	case EvidenceTypeFallbackRevocationV1:
		var payload fallbackRevocationPayload
		if err := decodeStrictFallback(item.Payload, &payload); err != nil || payload.SchemaVersion != fallbackRevocationPayloadSchema ||
			payload.ChangeID != query.Reference.ChangeID || !validFallbackOpaque(payload.ID, 256) ||
			!validFallbackOpaque(payload.SupersedesID, 256) || payload.ID == payload.SupersedesID ||
			payload.SubjectRevision != query.SubjectRevision ||
			(payload.TargetType != EvidenceTypeReviewDecisionFallbackV1 && payload.TargetType != EvidenceTypeCheckAttestationV1) ||
			payload.TargetStream != payload.TargetType+"\x00"+item.ExternalID || payload.Administrator.Validate() != nil ||
			payload.Administrator.Provider != IssueServerActorProvider || payload.Administrator.StableID != item.WriterUserID.String() ||
			strings.TrimSpace(payload.Reason) == "" || len(payload.Reason) > 1024 || item.NormalizedState != "revoked" {
			return fallbackNode{}, ErrFallbackAuthorityConflict
		}
		return fallbackNode{evidence: item, revocation: &payload, recordID: payload.ID,
			supersedes: payload.SupersedesID, stream: payload.TargetStream}, nil
	default:
		return fallbackNode{}, ErrFallbackAuthorityConflict
	}
}

func decodeStrictFallback(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}
