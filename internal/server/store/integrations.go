package store

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/jackc/pgx/v5"
)

const externalEvidenceColumns = `
	id, organization_id, repository_id, issue_id, provider_key, evidence_type,
	external_id, ingest_key, normalized_state, subject_revision, base_revision,
	merge_revision, observed_at, valid_until, payload_hash, payload, provenance,
	writer_user_id, writer_identity_key, supersedes_evidence_id, created_at`

// AppendExternalEvidence records immutable, trusted evidence. Retrying the
// same ingest key with the same semantic input returns the existing row;
// reusing it for different input is rejected explicitly.
func (s RepoStore) AppendExternalEvidence(ctx context.Context, input models.NewExternalEvidence) (models.ExternalEvidence, error) {
	if err := s.validate(); err != nil {
		return models.ExternalEvidence{}, err
	}
	if input.IssueID == uuid.Nil || strings.TrimSpace(input.ProviderKey) == "" ||
		strings.TrimSpace(input.EvidenceType) == "" || strings.TrimSpace(input.IngestKey) == "" ||
		strings.TrimSpace(input.NormalizedState) == "" || strings.TrimSpace(input.SubjectRevision) == "" ||
		strings.TrimSpace(input.WriterIdentityKey) == "" || input.ObservedAt.IsZero() {
		return models.ExternalEvidence{}, fmt.Errorf("%w: evidence issue, provider, type, ingest key, state, revision, observed time, and writer identity are required", ErrInvalidInput)
	}
	canonical, err := canonicalJSON(input.Payload)
	if err != nil {
		return models.ExternalEvidence{}, fmt.Errorf("%w: evidence payload: %v", ErrInvalidInput, err)
	}
	provenance, err := canonicalJSON(input.Provenance)
	if err != nil {
		return models.ExternalEvidence{}, fmt.Errorf("%w: evidence provenance: %v", ErrInvalidInput, err)
	}
	if input.ID == uuid.Nil {
		input.ID = uuid.New()
	}
	digest, err := semanticDigest(struct {
		IssueID              uuid.UUID       `json:"issue_id"`
		ProviderKey          string          `json:"provider_key"`
		EvidenceType         string          `json:"evidence_type"`
		ExternalID           string          `json:"external_id"`
		NormalizedState      string          `json:"normalized_state"`
		SubjectRevision      string          `json:"subject_revision"`
		BaseRevision         *string         `json:"base_revision,omitempty"`
		MergeRevision        *string         `json:"merge_revision,omitempty"`
		ObservedAt           time.Time       `json:"observed_at"`
		ValidUntil           *time.Time      `json:"valid_until,omitempty"`
		Payload              json.RawMessage `json:"payload"`
		Provenance           json.RawMessage `json:"provenance"`
		WriterUserID         *uuid.UUID      `json:"writer_user_id,omitempty"`
		WriterIdentityKey    string          `json:"writer_identity_key"`
		SupersedesEvidenceID *uuid.UUID      `json:"supersedes_evidence_id,omitempty"`
	}{input.IssueID, input.ProviderKey, input.EvidenceType, input.ExternalID,
		input.NormalizedState, input.SubjectRevision, input.BaseRevision, input.MergeRevision,
		input.ObservedAt, input.ValidUntil, canonical, provenance, input.WriterUserID,
		input.WriterIdentityKey, input.SupersedesEvidenceID})
	if err != nil {
		return models.ExternalEvidence{}, fmt.Errorf("hash evidence: %w", err)
	}

	row := s.db.QueryRow(ctx, `
		INSERT INTO external_evidence (
			id, organization_id, repository_id, issue_id, provider_key, evidence_type,
			external_id, ingest_key, normalized_state, subject_revision, base_revision,
			merge_revision, observed_at, valid_until, payload_hash, payload, provenance,
			writer_user_id, writer_identity_key, supersedes_evidence_id
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14,
			$15, $16::jsonb, $17::jsonb, $18, $19, $20)
		ON CONFLICT (organization_id, repository_id, provider_key, ingest_key) DO NOTHING
		RETURNING `+externalEvidenceColumns,
		input.ID, s.scope.OrgID, s.scope.RepoID, input.IssueID, input.ProviderKey,
		input.EvidenceType, input.ExternalID, input.IngestKey, input.NormalizedState,
		input.SubjectRevision, input.BaseRevision, input.MergeRevision, input.ObservedAt,
		input.ValidUntil, digest, string(canonical), string(provenance), input.WriterUserID,
		input.WriterIdentityKey, input.SupersedesEvidenceID)
	evidence, err := scanExternalEvidence(row)
	if err == nil {
		return evidence, nil
	}
	if err != pgx.ErrNoRows {
		return models.ExternalEvidence{}, fmt.Errorf("insert external evidence: %w", mapError(err))
	}

	row = s.db.QueryRow(ctx, `SELECT `+externalEvidenceColumns+`
		FROM external_evidence
		WHERE organization_id = $1 AND repository_id = $2 AND provider_key = $3 AND ingest_key = $4`,
		s.scope.OrgID, s.scope.RepoID, input.ProviderKey, input.IngestKey)
	evidence, err = scanExternalEvidence(row)
	if err != nil {
		return models.ExternalEvidence{}, fmt.Errorf("read idempotent external evidence: %w", mapError(err))
	}
	if subtle.ConstantTimeCompare(evidence.PayloadHash, digest) != 1 {
		return models.ExternalEvidence{}, fmt.Errorf("%w: external evidence %q", ErrIdempotencyMismatch, input.IngestKey)
	}
	return evidence, nil
}

func scanExternalEvidence(row rowScanner) (models.ExternalEvidence, error) {
	var evidence models.ExternalEvidence
	err := row.Scan(
		&evidence.ID,
		&evidence.Scope.OrgID,
		&evidence.Scope.RepoID,
		&evidence.IssueID,
		&evidence.ProviderKey,
		&evidence.EvidenceType,
		&evidence.ExternalID,
		&evidence.IngestKey,
		&evidence.NormalizedState,
		&evidence.SubjectRevision,
		&evidence.BaseRevision,
		&evidence.MergeRevision,
		&evidence.ObservedAt,
		&evidence.ValidUntil,
		&evidence.PayloadHash,
		&evidence.Payload,
		&evidence.Provenance,
		&evidence.WriterUserID,
		&evidence.WriterIdentityKey,
		&evidence.SupersedesEvidenceID,
		&evidence.CreatedAt,
	)
	return evidence, err
}

const outboxEventColumns = `
	id, organization_id, repository_id, schema_version, repository_sequence,
	aggregate_type, aggregate_id, event_type, event_key, payload_hash, payload,
	available_at, published_at, created_at`

// EnqueueEvent uses a caller-defined semantic event key so retries do not
// create duplicate deliveries. The key may only be reused for the same event.
func (s RepoStore) EnqueueEvent(ctx context.Context, input models.NewOutboxEvent) (models.OutboxEvent, error) {
	if err := s.validate(); err != nil {
		return models.OutboxEvent{}, err
	}
	if strings.TrimSpace(input.AggregateType) == "" || input.AggregateID == uuid.Nil ||
		strings.TrimSpace(input.EventType) == "" || strings.TrimSpace(input.EventKey) == "" {
		return models.OutboxEvent{}, fmt.Errorf("%w: aggregate, event type, and semantic event key are required", ErrInvalidInput)
	}
	if input.SchemaVersion == 0 {
		input.SchemaVersion = 1
	}
	if input.SchemaVersion < 1 {
		return models.OutboxEvent{}, fmt.Errorf("%w: positive outbox schema version is required", ErrInvalidInput)
	}
	canonical, err := canonicalJSON(input.Payload)
	if err != nil {
		return models.OutboxEvent{}, fmt.Errorf("%w: outbox payload: %v", ErrInvalidInput, err)
	}
	if input.ID == uuid.Nil {
		input.ID = uuid.New()
	}
	if input.AvailableAt.IsZero() {
		input.AvailableAt = time.Now().UTC()
	}
	digest, err := semanticDigest(struct {
		AggregateType string          `json:"aggregate_type"`
		AggregateID   uuid.UUID       `json:"aggregate_id"`
		EventType     string          `json:"event_type"`
		Payload       json.RawMessage `json:"payload"`
	}{input.AggregateType, input.AggregateID, input.EventType, canonical})
	if err != nil {
		return models.OutboxEvent{}, fmt.Errorf("hash outbox event: %w", err)
	}

	row := s.db.QueryRow(ctx, `
		INSERT INTO event_outbox (
			id, organization_id, repository_id, schema_version, aggregate_type,
			aggregate_id, event_type, event_key, payload_hash, payload, available_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb, $11)
		ON CONFLICT (organization_id, repository_id, event_key) DO NOTHING
		RETURNING `+outboxEventColumns,
		input.ID, s.scope.OrgID, s.scope.RepoID, input.SchemaVersion,
		input.AggregateType, input.AggregateID, input.EventType, input.EventKey,
		digest, string(canonical), input.AvailableAt)
	event, err := scanOutboxEvent(row)
	if err == nil {
		return event, nil
	}
	if err != pgx.ErrNoRows {
		return models.OutboxEvent{}, fmt.Errorf("insert outbox event: %w", mapError(err))
	}

	row = s.db.QueryRow(ctx, `SELECT `+outboxEventColumns+`
		FROM event_outbox
		WHERE organization_id = $1 AND repository_id = $2 AND event_key = $3`,
		s.scope.OrgID, s.scope.RepoID, input.EventKey)
	event, err = scanOutboxEvent(row)
	if err != nil {
		return models.OutboxEvent{}, fmt.Errorf("read idempotent outbox event: %w", mapError(err))
	}
	if event.SchemaVersion != input.SchemaVersion || subtle.ConstantTimeCompare(event.PayloadHash, digest) != 1 {
		return models.OutboxEvent{}, fmt.Errorf("%w: outbox event %q", ErrIdempotencyMismatch, input.EventKey)
	}
	return event, nil
}

func scanOutboxEvent(row rowScanner) (models.OutboxEvent, error) {
	var event models.OutboxEvent
	err := row.Scan(
		&event.ID,
		&event.Scope.OrgID,
		&event.Scope.RepoID,
		&event.SchemaVersion,
		&event.RepositorySequence,
		&event.AggregateType,
		&event.AggregateID,
		&event.EventType,
		&event.EventKey,
		&event.PayloadHash,
		&event.Payload,
		&event.AvailableAt,
		&event.PublishedAt,
		&event.CreatedAt,
	)
	return event, err
}

func canonicalJSON(value json.RawMessage) (json.RawMessage, error) {
	if len(value) == 0 {
		return json.RawMessage(`{}`), nil
	}
	var decoded any
	if err := json.Unmarshal(value, &decoded); err != nil {
		return nil, err
	}
	canonical, err := json.Marshal(decoded)
	if err != nil {
		return nil, err
	}
	return canonical, nil
}

func semanticDigest(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(encoded)
	return digest[:], nil
}
