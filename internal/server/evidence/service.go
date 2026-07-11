package evidence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	adminservice "github.com/higress-group/issue-spec/internal/server/admin"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	pool  *pgxpool.Pool
	authz *authz.Service
}

func New(pool *pgxpool.Pool, authorization *authz.Service) (*Service, error) {
	if pool == nil || authorization == nil {
		return nil, errors.New("evidence: database and authorization are required")
	}
	return &Service{pool: pool, authz: authorization}, nil
}

// EvidencePolicy returns the durable required evidence types and freshness
// windows for a repository. An unset policy is represented by version zero.
func (s *Service) EvidencePolicy(ctx context.Context, subject authz.Subject, scope models.RepoScope) (Policy, error) {
	decision, err := s.authz.EvaluateRepository(ctx, subject, authz.RepositoryRequest{Scope: scope, Operation: authz.OperationRead})
	if err != nil {
		return Policy{}, err
	}
	if err := decision.AuthorizationError(); err != nil {
		return Policy{}, err
	}
	return loadPolicy(ctx, s.pool, scope)
}

func (s *Service) SetEvidencePolicy(ctx context.Context, subject authz.Subject, actor adminservice.Actor, scope models.RepoScope, input SetPolicyInput) (Policy, error) {
	requirements, err := normalizeRequirements(input.Requirements)
	if err != nil || validateActor(subject, actor) != nil || input.ExpectedVersion < 0 {
		return Policy{}, adminservice.ErrInvalidInput
	}
	var result Policy
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		decision, err := s.authz.EvaluateRepositoryTx(ctx, tx, subject, authz.RepositoryRequest{Scope: scope, Operation: authz.OperationManageIntegrations})
		if err != nil {
			return err
		}
		if err := decision.AuthorizationError(); err != nil {
			return err
		}
		current, err := loadPolicyForUpdate(ctx, tx, scope)
		if err != nil {
			return err
		}
		if current.RepresentationVersion != input.ExpectedVersion {
			return adminservice.ErrVersionConflict
		}
		if requirementsEqual(current.Requirements, requirements) {
			result = current
			return nil
		}
		if current.RepresentationVersion == 0 {
			if _, err := tx.Exec(ctx, `INSERT INTO repository_evidence_policies
				(organization_id, repository_id, created_by_user_id, updated_by_user_id)
				VALUES ($1, $2, $3, $3)`, scope.OrgID, scope.RepoID, actor.UserID); err != nil {
				return err
			}
		} else {
			if _, err := tx.Exec(ctx, `UPDATE repository_evidence_policies SET
				representation_version = representation_version + 1, updated_by_user_id = $3,
				updated_at = clock_timestamp() WHERE organization_id = $1 AND repository_id = $2`,
				scope.OrgID, scope.RepoID, actor.UserID); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM repository_evidence_requirements
			WHERE organization_id = $1 AND repository_id = $2`, scope.OrgID, scope.RepoID); err != nil {
			return err
		}
		for _, requirement := range requirements {
			if _, err := tx.Exec(ctx, `INSERT INTO repository_evidence_requirements
				(id, organization_id, repository_id, evidence_type, freshness, created_by_user_id, updated_by_user_id)
				VALUES ($1, $2, $3, $4, $5, $6, $6)`, uuid.New(), scope.OrgID, scope.RepoID,
				requirement.EvidenceType, durationInterval(requirement.Freshness), actor.UserID); err != nil {
				return err
			}
		}
		if err := bumpEvidenceCollection(ctx, tx, scope); err != nil {
			return err
		}
		if err := insertAudit(ctx, tx, actor, scope, uuid.Nil, "evidence_policy.update", "evidence_policy", map[string]any{
			"required_evidence_types": requirementNames(requirements),
		}); err != nil {
			return err
		}
		result, err = loadPolicy(ctx, tx, scope)
		return err
	})
	return result, mapError(err)
}

// IsDesignatedWriter is the durable writer-assignment contract consumed by
// ingestion and future bridge adapters.
func (s *Service) IsDesignatedWriter(ctx context.Context, scope models.RepoScope, userID uuid.UUID) (bool, error) {
	if err := scope.Validate(); err != nil || userID == uuid.Nil {
		return false, adminservice.ErrInvalidInput
	}
	var designated bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM repository_evidence_writers
		WHERE organization_id = $1 AND repository_id = $2 AND user_id = $3 AND active)`,
		scope.OrgID, scope.RepoID, userID).Scan(&designated)
	return designated, err
}

func (s *Service) SetDesignatedWriter(ctx context.Context, subject authz.Subject, actor adminservice.Actor, scope models.RepoScope, userID uuid.UUID, active bool) (WriterAssignment, error) {
	if userID == uuid.Nil || validateActor(subject, actor) != nil {
		return WriterAssignment{}, adminservice.ErrInvalidInput
	}
	var result WriterAssignment
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		decision, err := s.authz.EvaluateRepositoryTx(ctx, tx, subject, authz.RepositoryRequest{Scope: scope, Operation: authz.OperationManageIntegrations})
		if err != nil {
			return err
		}
		if err := decision.AuthorizationError(); err != nil {
			return err
		}
		var eligible bool
		if err := tx.QueryRow(ctx, `SELECT u.status = 'active' AND (
			EXISTS (SELECT 1 FROM org_memberships om WHERE om.organization_id = $1 AND om.user_id = u.id
				AND om.state = 'active' AND om.archived_at IS NULL)
			OR EXISTS (SELECT 1 FROM service_accounts sa WHERE sa.organization_id = $1 AND sa.user_id = u.id
				AND sa.disabled_at IS NULL)
			OR EXISTS (SELECT 1 FROM repo_collaborators rc WHERE rc.organization_id = $1
				AND rc.repository_id = $2 AND rc.user_id = u.id AND rc.archived_at IS NULL)
		) FROM users u WHERE u.id = $3 FOR SHARE`, scope.OrgID, scope.RepoID, userID).Scan(&eligible); err != nil {
			return err
		}
		if !eligible {
			return adminservice.ErrNotFound
		}
		row := tx.QueryRow(ctx, `SELECT id, organization_id, repository_id, user_id, active,
			representation_version, created_at, updated_at FROM repository_evidence_writers
			WHERE organization_id = $1 AND repository_id = $2 AND user_id = $3 FOR UPDATE`,
			scope.OrgID, scope.RepoID, userID)
		current, scanErr := scanWriter(row)
		switch {
		case scanErr == nil && current.Active == active:
			result = current
			return nil
		case scanErr == nil:
			result, err = scanWriter(tx.QueryRow(ctx, `UPDATE repository_evidence_writers SET active = $4,
				representation_version = representation_version + 1, updated_by_user_id = $5,
				updated_at = clock_timestamp() WHERE organization_id = $1 AND repository_id = $2 AND user_id = $3
				RETURNING id, organization_id, repository_id, user_id, active, representation_version,
				created_at, updated_at`, scope.OrgID, scope.RepoID, userID, active, actor.UserID))
		case errors.Is(scanErr, pgx.ErrNoRows):
			result, err = scanWriter(tx.QueryRow(ctx, `INSERT INTO repository_evidence_writers
				(id, organization_id, repository_id, user_id, active, created_by_user_id, updated_by_user_id)
				VALUES ($1, $2, $3, $4, $5, $6, $6)
				RETURNING id, organization_id, repository_id, user_id, active, representation_version,
				created_at, updated_at`, uuid.New(), scope.OrgID, scope.RepoID, userID, active, actor.UserID))
		default:
			return scanErr
		}
		if err != nil {
			return err
		}
		if err := bumpEvidenceCollection(ctx, tx, scope); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, scope, result.ID, "evidence_writer.update", "evidence_writer", map[string]any{
			"writer_user_id": userID, "active": active,
		})
	})
	return result, mapError(err)
}

// AppendEvidence enforces four independent gates: durable writer assignment,
// evidence:write scope, live repository identity permission, and exactly one
// cap for this repository. Denials are audited after the protected transaction
// rolls back, without copying payload, provenance or credential material.
func (s *Service) AppendEvidence(ctx context.Context, subject authz.Subject, actor adminservice.Actor, scope models.RepoScope, input AppendInput) (Evidence, error) {
	input = normalizeAppendInput(input)
	payload, payloadErr := canonicalObject(input.Payload)
	provenance, provenanceErr := canonicalObject(input.Provenance)
	if payloadErr != nil || provenanceErr != nil || validateActor(subject, actor) != nil || validateAppendInput(input) != nil {
		return Evidence{}, adminservice.ErrInvalidInput
	}
	input.Payload, input.Provenance = payload, provenance
	var result Evidence
	var denial string
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		principal := *subject.Principal
		designated, err := designatedWriterForUpdate(ctx, tx, scope, principal.User.ID)
		if err != nil {
			return err
		}
		decision, err := s.authz.EvaluateRepositoryTx(ctx, tx, subject, authz.RepositoryRequest{
			Scope: scope, Operation: authz.OperationPublishEvidence, DesignatedEvidenceWriter: designated,
		})
		if err != nil {
			return err
		}
		if !decision.Allowed {
			denial = string(decision.Reason)
			return decision.AuthorizationError()
		}
		if !exactRepositoryCap(principal, scope) {
			denial = "exact_repository_cap_required"
			return adminservice.ErrForbidden
		}
		if err := ensureIssue(ctx, tx, scope, input.IssueID); err != nil {
			return err
		}
		if input.SupersedesEvidenceID != nil {
			if err := validateSupersedes(ctx, tx, scope, input); err != nil {
				return err
			}
		}
		digest := sha256.Sum256(input.Payload)
		candidateID := uuid.New()
		result, err = scanEvidence(tx.QueryRow(ctx, `INSERT INTO external_evidence
			(id, organization_id, repository_id, issue_id, provider_key, external_repository_id,
			 evidence_type, external_id, ingest_key, normalized_state, subject_revision, base_revision,
			 merge_revision, observed_at, valid_until, payload_hash, payload, provenance, writer_user_id,
			 writer_identity_key, supersedes_evidence_id, visibility)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15,
			 $16, $17::jsonb, $18::jsonb, $19, $20, $21, $22)
			ON CONFLICT (organization_id, repository_id, provider_key, ingest_key) DO NOTHING
			RETURNING `+evidenceColumns, candidateID, scope.OrgID, scope.RepoID, input.IssueID,
			input.ProviderKey, input.ExternalRepositoryID, input.EvidenceType, input.ExternalID,
			input.IngestKey, input.NormalizedState, input.SubjectRevision, input.BaseRevision,
			input.MergeRevision, input.ObservedAt, input.ValidUntil, digest[:], string(input.Payload),
			string(input.Provenance), principal.User.ID, actor.IdentityKey, input.SupersedesEvidenceID,
			input.Visibility))
		if errors.Is(err, pgx.ErrNoRows) {
			result, err = scanEvidence(tx.QueryRow(ctx, `SELECT `+evidenceColumns+` FROM external_evidence
				WHERE organization_id = $1 AND repository_id = $2 AND provider_key = $3 AND ingest_key = $4`,
				scope.OrgID, scope.RepoID, input.ProviderKey, input.IngestKey))
			if err != nil {
				return err
			}
			if !evidenceMatches(result, input, principal.User.ID, actor.IdentityKey, digest[:]) {
				return ErrIdempotencyMismatch
			}
			return nil
		}
		if err != nil {
			return err
		}
		if err := bumpEvidenceCollections(ctx, tx, scope, input.IssueID); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, scope, result.ID, "external_evidence.append", "external_evidence", map[string]any{
			"provider_key": input.ProviderKey, "external_repository_id": input.ExternalRepositoryID,
			"evidence_type": input.EvidenceType, "subject_revision": input.SubjectRevision,
			"visibility": input.Visibility,
		})
	})
	if denial != "" {
		if auditErr := s.auditRejected(ctx, actor, scope, denial); auditErr != nil {
			return Evidence{}, fmt.Errorf("evidence: rejected publish audit: %w", auditErr)
		}
	}
	return result, mapError(err)
}

// ExactRevision only returns evidence whose provider, external repository and
// subject revision all match exactly. Payload and provenance remain
// maintainer-only even for repository-visible summaries.
func (s *Service) ExactRevision(ctx context.Context, subject authz.Subject, scope models.RepoScope, query ExactRevisionQuery) ([]Evidence, error) {
	query.ProviderKey = strings.TrimSpace(query.ProviderKey)
	query.ExternalRepositoryID = strings.TrimSpace(query.ExternalRepositoryID)
	query.SubjectRevision = strings.TrimSpace(query.SubjectRevision)
	query.EvidenceType = strings.TrimSpace(query.EvidenceType)
	if query.IssueID == uuid.Nil || query.ProviderKey == "" || query.ExternalRepositoryID == "" || query.SubjectRevision == "" {
		return nil, adminservice.ErrInvalidInput
	}
	decision, err := s.authz.EvaluateRepository(ctx, subject, authz.RepositoryRequest{Scope: scope, Operation: authz.OperationRead})
	if err != nil {
		return nil, err
	}
	if err := decision.AuthorizationError(); err != nil {
		return nil, err
	}
	if err := ensureIssue(ctx, s.pool, scope, query.IssueID); err != nil {
		return nil, mapError(err)
	}
	sql := `SELECT ` + evidenceColumns + ` FROM external_evidence WHERE organization_id = $1
		AND repository_id = $2 AND issue_id = $3 AND provider_key = $4 AND external_repository_id = $5 AND subject_revision = $6`
	args := []any{scope.OrgID, scope.RepoID, query.IssueID, query.ProviderKey, query.ExternalRepositoryID, query.SubjectRevision}
	if query.EvidenceType != "" {
		sql += ` AND evidence_type = $7`
		args = append(args, query.EvidenceType)
	}
	if decision.EffectivePermission < authz.PermissionMaintain {
		sql += ` AND visibility = 'repository'`
	}
	sql += ` ORDER BY observed_at DESC, id`
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Evidence, 0)
	for rows.Next() {
		item, scanErr := scanEvidence(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		if decision.EffectivePermission < authz.PermissionMaintain {
			item.Payload = nil
			item.Provenance = nil
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

const evidenceColumns = `id, organization_id, repository_id, issue_id, provider_key,
	external_repository_id, evidence_type, external_id, ingest_key, normalized_state,
	subject_revision, base_revision, merge_revision, observed_at, valid_until, payload_hash,
	payload, provenance, writer_user_id, writer_identity_key, supersedes_evidence_id,
	visibility, created_at`

type rowScanner interface{ Scan(...any) error }
type queryRower interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func scanEvidence(row rowScanner) (Evidence, error) {
	var item Evidence
	err := row.Scan(&item.ID, &item.Scope.OrgID, &item.Scope.RepoID, &item.IssueID,
		&item.ProviderKey, &item.ExternalRepositoryID, &item.EvidenceType, &item.ExternalID,
		&item.IngestKey, &item.NormalizedState, &item.SubjectRevision, &item.BaseRevision,
		&item.MergeRevision, &item.ObservedAt, &item.ValidUntil, &item.PayloadHash, &item.Payload,
		&item.Provenance, &item.WriterUserID, &item.WriterIdentityKey, &item.SupersedesEvidenceID,
		&item.Visibility, &item.CreatedAt)
	return item, err
}

func scanWriter(row rowScanner) (WriterAssignment, error) {
	var item WriterAssignment
	err := row.Scan(&item.ID, &item.Scope.OrgID, &item.Scope.RepoID, &item.UserID,
		&item.Active, &item.RepresentationVersion, &item.CreatedAt, &item.UpdatedAt)
	return item, err
}

func loadPolicy(ctx context.Context, db queryRower, scope models.RepoScope) (Policy, error) {
	var policy Policy
	policy.Scope = scope
	err := db.QueryRow(ctx, `SELECT representation_version, created_at, updated_at
		FROM repository_evidence_policies WHERE organization_id = $1 AND repository_id = $2`,
		scope.OrgID, scope.RepoID).Scan(&policy.RepresentationVersion, &policy.CreatedAt, &policy.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		policy.Requirements = []Requirement{}
		return policy, nil
	}
	if err != nil {
		return Policy{}, err
	}
	rows, err := queryRequirements(ctx, db, scope, false)
	if err != nil {
		return Policy{}, err
	}
	defer rows.Close()
	policy.Requirements, err = scanRequirements(rows)
	return policy, err
}

func loadPolicyForUpdate(ctx context.Context, tx pgx.Tx, scope models.RepoScope) (Policy, error) {
	var policy Policy
	policy.Scope = scope
	err := tx.QueryRow(ctx, `SELECT representation_version, created_at, updated_at
		FROM repository_evidence_policies WHERE organization_id = $1 AND repository_id = $2 FOR UPDATE`,
		scope.OrgID, scope.RepoID).Scan(&policy.RepresentationVersion, &policy.CreatedAt, &policy.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		policy.Requirements = []Requirement{}
		return policy, nil
	}
	if err != nil {
		return Policy{}, err
	}
	rows, err := queryRequirements(ctx, tx, scope, true)
	if err != nil {
		return Policy{}, err
	}
	defer rows.Close()
	policy.Requirements, err = scanRequirements(rows)
	return policy, err
}

type queryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func queryRequirements(ctx context.Context, db any, scope models.RepoScope, lock bool) (pgx.Rows, error) {
	query := `SELECT evidence_type, freshness, representation_version FROM repository_evidence_requirements
		WHERE organization_id = $1 AND repository_id = $2 ORDER BY evidence_type`
	if lock {
		query += ` FOR UPDATE`
	}
	return db.(queryer).Query(ctx, query, scope.OrgID, scope.RepoID)
}

func scanRequirements(rows pgx.Rows) ([]Requirement, error) {
	items := make([]Requirement, 0)
	for rows.Next() {
		var item Requirement
		var freshness *time.Duration
		if err := rows.Scan(&item.EvidenceType, &freshness, &item.RepresentationVersion); err != nil {
			return nil, err
		}
		item.Freshness = freshness
		items = append(items, item)
	}
	return items, rows.Err()
}

func normalizeRequirements(items []Requirement) ([]Requirement, error) {
	result := append([]Requirement(nil), items...)
	seen := make(map[string]struct{}, len(result))
	for i := range result {
		result[i].EvidenceType = strings.TrimSpace(result[i].EvidenceType)
		result[i].RepresentationVersion = 0
		if result[i].EvidenceType == "" || (result[i].Freshness != nil && *result[i].Freshness <= 0) {
			return nil, adminservice.ErrInvalidInput
		}
		if _, exists := seen[result[i].EvidenceType]; exists {
			return nil, adminservice.ErrInvalidInput
		}
		seen[result[i].EvidenceType] = struct{}{}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].EvidenceType < result[j].EvidenceType })
	return result, nil
}

func requirementsEqual(left, right []Requirement) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i].EvidenceType != right[i].EvidenceType || !durationEqual(left[i].Freshness, right[i].Freshness) {
			return false
		}
	}
	return true
}

func durationEqual(left, right *time.Duration) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func durationInterval(value *time.Duration) any {
	if value == nil {
		return nil
	}
	return *value
}

func requirementNames(items []Requirement) []string {
	names := make([]string, len(items))
	for i := range items {
		names[i] = items[i].EvidenceType
	}
	return names
}

func designatedWriterForUpdate(ctx context.Context, tx pgx.Tx, scope models.RepoScope, userID uuid.UUID) (bool, error) {
	var active bool
	err := tx.QueryRow(ctx, `SELECT active FROM repository_evidence_writers
		WHERE organization_id = $1 AND repository_id = $2 AND user_id = $3 FOR SHARE`,
		scope.OrgID, scope.RepoID, userID).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return active, err
}

func exactRepositoryCap(principal serverauth.Principal, scope models.RepoScope) bool {
	return principal.RepoRestricted && len(principal.RepositoryCaps) == 1 &&
		principal.RepositoryCaps[0].OrgID == scope.OrgID && principal.RepositoryCaps[0].RepoID == scope.RepoID
}

func validateSupersedes(ctx context.Context, tx pgx.Tx, scope models.RepoScope, input AppendInput) error {
	var id uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM external_evidence WHERE organization_id = $1 AND repository_id = $2
		AND id = $3 AND issue_id = $4 AND provider_key = $5 AND external_repository_id = $6
		AND evidence_type = $7 AND external_id = $8 FOR UPDATE`, scope.OrgID, scope.RepoID,
		*input.SupersedesEvidenceID, input.IssueID, input.ProviderKey, input.ExternalRepositoryID,
		input.EvidenceType, input.ExternalID).Scan(&id)
	if err != nil {
		return err
	}
	var alreadySuperseded bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM external_evidence
		WHERE organization_id = $1 AND repository_id = $2 AND supersedes_evidence_id = $3)`,
		scope.OrgID, scope.RepoID, id).Scan(&alreadySuperseded); err != nil {
		return err
	}
	if alreadySuperseded {
		return adminservice.ErrConflict
	}
	return nil
}

func normalizeAppendInput(input AppendInput) AppendInput {
	input.ProviderKey = strings.TrimSpace(input.ProviderKey)
	input.ExternalRepositoryID = strings.TrimSpace(input.ExternalRepositoryID)
	input.EvidenceType = strings.TrimSpace(input.EvidenceType)
	input.ExternalID = strings.TrimSpace(input.ExternalID)
	input.IngestKey = strings.TrimSpace(input.IngestKey)
	input.NormalizedState = strings.TrimSpace(input.NormalizedState)
	input.SubjectRevision = strings.TrimSpace(input.SubjectRevision)
	if input.Visibility == "" {
		input.Visibility = VisibilityRepository
	}
	if !input.ObservedAt.IsZero() {
		input.ObservedAt = input.ObservedAt.UTC().Truncate(time.Microsecond)
	}
	if input.ValidUntil != nil {
		value := input.ValidUntil.UTC().Truncate(time.Microsecond)
		input.ValidUntil = &value
	}
	return input
}

func validateAppendInput(input AppendInput) error {
	if input.IssueID == uuid.Nil || input.ProviderKey == "" || input.ExternalRepositoryID == "" ||
		input.EvidenceType == "" || input.IngestKey == "" || input.NormalizedState == "" ||
		input.SubjectRevision == "" || input.ObservedAt.IsZero() ||
		(input.Visibility != VisibilityRepository && input.Visibility != VisibilityMaintainers) ||
		(input.ValidUntil != nil && input.ValidUntil.Before(input.ObservedAt)) {
		return adminservice.ErrInvalidInput
	}
	return nil
}

func evidenceMatches(existing Evidence, input AppendInput, writerID uuid.UUID, writerKey string, digest []byte) bool {
	return existing.IssueID == input.IssueID && existing.ProviderKey == input.ProviderKey &&
		existing.ExternalRepositoryID == input.ExternalRepositoryID && existing.EvidenceType == input.EvidenceType &&
		existing.ExternalID == input.ExternalID && existing.NormalizedState == input.NormalizedState &&
		existing.SubjectRevision == input.SubjectRevision && optionalStringEqual(existing.BaseRevision, input.BaseRevision) &&
		optionalStringEqual(existing.MergeRevision, input.MergeRevision) && existing.ObservedAt.Equal(input.ObservedAt) &&
		optionalTimeEqual(existing.ValidUntil, input.ValidUntil) && bytes.Equal(existing.PayloadHash, digest) &&
		jsonEqual(existing.Payload, input.Payload) && jsonEqual(existing.Provenance, input.Provenance) &&
		existing.WriterUserID == writerID && existing.WriterIdentityKey == writerKey &&
		optionalUUIDEqual(existing.SupersedesEvidenceID, input.SupersedesEvidenceID) && existing.Visibility == input.Visibility
}

func optionalStringEqual(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func optionalTimeEqual(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func optionalUUIDEqual(left, right *uuid.UUID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func jsonEqual(left, right json.RawMessage) bool {
	canonicalLeft, leftErr := canonicalObject(left)
	canonicalRight, rightErr := canonicalObject(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(canonicalLeft, canonicalRight)
}

func canonicalObject(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return json.RawMessage(`{}`), nil
	}
	var object map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, adminservice.ErrInvalidInput
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, adminservice.ErrInvalidInput
	}
	return json.Marshal(object)
}

func validateActor(subject authz.Subject, actor adminservice.Actor) error {
	if subject.Principal == nil || subject.Principal.User.ID == uuid.Nil || actor.UserID != subject.Principal.User.ID ||
		strings.TrimSpace(actor.IdentityKey) == "" || strings.TrimSpace(actor.RequestID) == "" {
		return adminservice.ErrInvalidInput
	}
	return nil
}

func ensureIssue(ctx context.Context, db queryRower, scope models.RepoScope, issueID uuid.UUID) error {
	var found uuid.UUID
	return db.QueryRow(ctx, `SELECT id FROM issues WHERE organization_id = $1 AND repository_id = $2 AND id = $3`,
		scope.OrgID, scope.RepoID, issueID).Scan(&found)
}

func bumpEvidenceCollection(ctx context.Context, tx pgx.Tx, scope models.RepoScope) error {
	tag, err := tx.Exec(ctx, `UPDATE repos SET evidence_collection_version = evidence_collection_version + 1,
		updated_at = clock_timestamp() WHERE organization_id = $1 AND id = $2`, scope.OrgID, scope.RepoID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	return nil
}

func bumpEvidenceCollections(ctx context.Context, tx pgx.Tx, scope models.RepoScope, issueID uuid.UUID) error {
	tag, err := tx.Exec(ctx, `UPDATE issues SET evidence_collection_version = evidence_collection_version + 1,
		updated_at = clock_timestamp() WHERE organization_id = $1 AND repository_id = $2 AND id = $3`,
		scope.OrgID, scope.RepoID, issueID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	return bumpEvidenceCollection(ctx, tx, scope)
}

func insertAudit(ctx context.Context, tx pgx.Tx, actor adminservice.Actor, scope models.RepoScope, resourceID uuid.UUID, action, resourceType string, metadata any) error {
	if metadata == nil {
		metadata = map[string]any{}
	}
	payload, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	var nullableResource any
	if resourceID != uuid.Nil {
		nullableResource = resourceID
	}
	_, err = tx.Exec(ctx, `INSERT INTO audit_events
		(id, organization_id, repository_id, actor_user_id, actor_identity_key, action,
		 resource_type, resource_id, request_id, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb)`, uuid.New(), scope.OrgID,
		scope.RepoID, actor.UserID, actor.IdentityKey, action, resourceType, nullableResource,
		actor.RequestID, string(payload))
	return err
}

func (s *Service) auditRejected(ctx context.Context, actor adminservice.Actor, scope models.RepoScope, reason string) error {
	payload, _ := json.Marshal(map[string]string{
		"reason": reason, "operation": "evidence.publish",
		"target_organization_id": scope.OrgID.String(), "target_repository_id": scope.RepoID.String(),
	})
	_, err := s.pool.Exec(ctx, `INSERT INTO audit_events
		(id, actor_user_id, actor_identity_key, action, resource_type, request_id, metadata)
		VALUES ($1, $2, $3, 'external_evidence.publish_rejected', 'external_evidence', $4, $5::jsonb)`,
		uuid.New(), actor.UserID, actor.IdentityKey, actor.RequestID, string(payload))
	return err
}

func mapError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return adminservice.ErrNotFound
	}
	if errors.Is(err, ErrIdempotencyMismatch) || errors.Is(err, adminservice.ErrForbidden) ||
		errors.Is(err, adminservice.ErrVersionConflict) || errors.Is(err, adminservice.ErrConflict) ||
		errors.Is(err, adminservice.ErrInvalidInput) || errors.Is(err, adminservice.ErrNotFound) {
		return err
	}
	return fmt.Errorf("evidence: %w", err)
}
