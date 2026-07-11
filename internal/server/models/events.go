package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type OutboxEvent struct {
	ID            uuid.UUID
	Scope         RepoScope
	AggregateType string
	AggregateID   uuid.UUID
	EventType     string
	EventKey      string
	PayloadHash   []byte
	Payload       json.RawMessage
	AvailableAt   time.Time
	PublishedAt   *time.Time
	CreatedAt     time.Time
}

type NewOutboxEvent struct {
	ID            uuid.UUID
	AggregateType string
	AggregateID   uuid.UUID
	EventType     string
	EventKey      string
	Payload       json.RawMessage
	AvailableAt   time.Time
}

type ExternalEvidence struct {
	ID                   uuid.UUID
	Scope                RepoScope
	IssueID              uuid.UUID
	ProviderKey          string
	EvidenceType         string
	ExternalID           string
	IngestKey            string
	NormalizedState      string
	SubjectRevision      string
	BaseRevision         *string
	MergeRevision        *string
	ObservedAt           time.Time
	ValidUntil           *time.Time
	PayloadHash          []byte
	Payload              json.RawMessage
	Provenance           json.RawMessage
	WriterUserID         *uuid.UUID
	WriterIdentityKey    string
	SupersedesEvidenceID *uuid.UUID
	CreatedAt            time.Time
}

type NewExternalEvidence struct {
	ID                   uuid.UUID
	IssueID              uuid.UUID
	ProviderKey          string
	EvidenceType         string
	ExternalID           string
	IngestKey            string
	NormalizedState      string
	SubjectRevision      string
	BaseRevision         *string
	MergeRevision        *string
	ObservedAt           time.Time
	ValidUntil           *time.Time
	Payload              json.RawMessage
	Provenance           json.RawMessage
	WriterUserID         *uuid.UUID
	WriterIdentityKey    string
	SupersedesEvidenceID *uuid.UUID
}

type Artifact struct {
	ID                    uuid.UUID
	Scope                 RepoScope
	ChangeKey             string
	ArtifactType          string
	Content               string
	Metadata              json.RawMessage
	Active                bool
	RepresentationVersion int64
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

type SourceBinding struct {
	ID                   uuid.UUID
	Scope                RepoScope
	ProviderKey          string
	ExternalRepositoryID string
	CloneURL             string
	WebURL               string
	DefaultBranch        string
	Version              int64
	Active               bool
	CreatedByUserID      *uuid.UUID
	UpdatedByUserID      *uuid.UUID
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

type ExternalReference struct {
	ID                    uuid.UUID
	Scope                 RepoScope
	IssueID               uuid.UUID
	ProviderKey           string
	RelationKind          string
	ExternalRepositoryID  string
	ExternalID            string
	CanonicalURL          string
	Title                 *string
	LifecycleState        string
	Metadata              json.RawMessage
	RepresentationVersion int64
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

type WebhookSubscription struct {
	ID                    uuid.UUID
	OrgID                 uuid.UUID
	RepoID                *uuid.UUID
	ScopeType             string
	URL                   string
	Active                bool
	EventTypes            []string
	RepresentationVersion int64
	CreatedByUserID       *uuid.UUID
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

type WebhookSecretVersion struct {
	ID               uuid.UUID
	OrgID            uuid.UUID
	RepoID           *uuid.UUID
	SubscriptionID   uuid.UUID
	Version          int64
	SecretCiphertext []byte
	Active           bool
	CreatedByUserID  *uuid.UUID
	RetiredAt        *time.Time
	CreatedAt        time.Time
}

type WebhookDelivery struct {
	ID                    uuid.UUID
	Scope                 RepoScope
	EventID               uuid.UUID
	SubscriptionID        uuid.UUID
	SecretVersionID       uuid.UUID
	State                 string
	NextAttemptAt         time.Time
	DeliveredAt           *time.Time
	LastError             *string
	RepresentationVersion int64
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

type WebhookDeliveryAttempt struct {
	ID              uuid.UUID
	Scope           RepoScope
	DeliveryID      uuid.UUID
	AttemptNumber   int64
	RequestHeaders  json.RawMessage
	ResponseStatus  *int
	ResponseHeaders json.RawMessage
	ResponseBody    *string
	Error           *string
	StartedAt       time.Time
	CompletedAt     *time.Time
	CreatedAt       time.Time
}

type TypedCommentProjection struct {
	ID                    uuid.UUID
	Scope                 RepoScope
	IssueID               uuid.UUID
	CommentID             *uuid.UUID
	ArtifactID            *uuid.UUID
	CommentType           string
	CommentKey            string
	Body                  string
	Metadata              json.RawMessage
	RepresentationVersion int64
	CreatedByUserID       *uuid.UUID
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

type ProjectionAnomaly struct {
	ID             uuid.UUID
	Scope          RepoScope
	ProjectionName string
	SourceType     string
	SourceID       uuid.UUID
	AnomalyKey     string
	Details        json.RawMessage
	ObservedAt     time.Time
	ResolvedAt     *time.Time
	CreatedAt      time.Time
}
