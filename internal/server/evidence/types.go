// Package evidence owns repository evidence policies, designated writers and
// immutable revision-bound external evidence.
package evidence

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/models"
)

var ErrIdempotencyMismatch = errors.New("evidence idempotency key reused with different content")

type Visibility string

const (
	VisibilityRepository  Visibility = "repository"
	VisibilityMaintainers Visibility = "maintainers"
)

type Requirement struct {
	EvidenceType          string         `json:"evidence_type"`
	Freshness             *time.Duration `json:"freshness,omitempty"`
	RepresentationVersion int64          `json:"representation_version"`
}

type Policy struct {
	Scope                 models.RepoScope `json:"-"`
	RepresentationVersion int64            `json:"representation_version"`
	Requirements          []Requirement    `json:"requirements"`
	CreatedAt             time.Time        `json:"created_at"`
	UpdatedAt             time.Time        `json:"updated_at"`
}

type SetPolicyInput struct {
	Requirements    []Requirement `json:"requirements"`
	ExpectedVersion int64         `json:"expected_version"`
}

type WriterAssignment struct {
	ID                    uuid.UUID        `json:"id"`
	Scope                 models.RepoScope `json:"-"`
	UserID                uuid.UUID        `json:"user_id"`
	Active                bool             `json:"active"`
	RepresentationVersion int64            `json:"representation_version"`
	CreatedAt             time.Time        `json:"created_at"`
	UpdatedAt             time.Time        `json:"updated_at"`
}

type Evidence struct {
	ID                   uuid.UUID        `json:"id"`
	Scope                models.RepoScope `json:"-"`
	IssueID              uuid.UUID        `json:"issue_id"`
	ProviderKey          string           `json:"provider_key"`
	ExternalRepositoryID string           `json:"external_repository_id"`
	EvidenceType         string           `json:"evidence_type"`
	ExternalID           string           `json:"external_id,omitempty"`
	IngestKey            string           `json:"ingest_key"`
	NormalizedState      string           `json:"normalized_state"`
	SubjectRevision      string           `json:"subject_revision"`
	BaseRevision         *string          `json:"base_revision,omitempty"`
	MergeRevision        *string          `json:"merge_revision,omitempty"`
	ObservedAt           time.Time        `json:"observed_at"`
	ValidUntil           *time.Time       `json:"valid_until,omitempty"`
	PayloadHash          []byte           `json:"payload_hash"`
	Payload              json.RawMessage  `json:"payload,omitempty"`
	Provenance           json.RawMessage  `json:"provenance,omitempty"`
	WriterUserID         uuid.UUID        `json:"writer_user_id"`
	WriterIdentityKey    string           `json:"writer_identity_key"`
	SupersedesEvidenceID *uuid.UUID       `json:"supersedes_evidence_id,omitempty"`
	Visibility           Visibility       `json:"visibility"`
	CreatedAt            time.Time        `json:"created_at"`
}

type AppendInput struct {
	IssueID              uuid.UUID       `json:"issue_id"`
	ProviderKey          string          `json:"provider_key"`
	ExternalRepositoryID string          `json:"external_repository_id"`
	EvidenceType         string          `json:"evidence_type"`
	ExternalID           string          `json:"external_id,omitempty"`
	IngestKey            string          `json:"ingest_key"`
	NormalizedState      string          `json:"normalized_state"`
	SubjectRevision      string          `json:"subject_revision"`
	BaseRevision         *string         `json:"base_revision,omitempty"`
	MergeRevision        *string         `json:"merge_revision,omitempty"`
	ObservedAt           time.Time       `json:"observed_at"`
	ValidUntil           *time.Time      `json:"valid_until,omitempty"`
	Payload              json.RawMessage `json:"payload"`
	Provenance           json.RawMessage `json:"provenance"`
	SupersedesEvidenceID *uuid.UUID      `json:"supersedes_evidence_id,omitempty"`
	Visibility           Visibility      `json:"visibility"`
}

type ExactRevisionQuery struct {
	IssueID              uuid.UUID `json:"issue_id"`
	ProviderKey          string    `json:"provider_key"`
	ExternalRepositoryID string    `json:"external_repository_id"`
	SubjectRevision      string    `json:"subject_revision"`
	EvidenceType         string    `json:"evidence_type,omitempty"`
}
