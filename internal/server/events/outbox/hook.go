// Package outbox turns issue mutations into immutable v1 event envelopes and
// inserts them through the caller's transaction-bound repository store.
package outbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/api/github/codec"
	"github.com/higress-group/issue-spec/internal/server/api/github/issues"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/higress-group/issue-spec/internal/server/store"
)

const SchemaVersion = 1

type Envelope struct {
	SchemaVersion  int              `json:"schema_version"`
	EventID        uuid.UUID        `json:"event_id"`
	EventKey       string           `json:"event_key"`
	EventType      string           `json:"event_type"`
	Action         string           `json:"action"`
	OccurredAt     time.Time        `json:"occurred_at"`
	OrganizationID uuid.UUID        `json:"organization_id"`
	RepositoryID   uuid.UUID        `json:"repository_id"`
	Issue          IssueIdentity    `json:"issue"`
	Comment        *CommentRevision `json:"comment,omitempty"`
	RawBody        string           `json:"raw_body"`
	BodyHash       string           `json:"body_hash"`
	ActorUserID    uuid.UUID        `json:"actor_user_id"`
	Author         AuthorIdentity   `json:"author"`
}

type IssueIdentity struct {
	StableID              uuid.UUID `json:"stable_id"`
	Number                int64     `json:"number"`
	RepresentationVersion int64     `json:"representation_version,omitempty"`
	CreatedAt             time.Time `json:"created_at,omitempty"`
	UpdatedAt             time.Time `json:"updated_at,omitempty"`
}

type CommentRevision struct {
	StableID uuid.UUID `json:"stable_id"`
	// NumericID is the compatibility value captured when this immutable
	// schema-versioned envelope is emitted. StableID remains authoritative for
	// replay across compatibility-ID migrations.
	NumericID             int64     `json:"numeric_id"`
	RepresentationVersion int64     `json:"representation_version"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}

type AuthorIdentity struct {
	UserID *uuid.UUID `json:"user_id,omitempty"`
	Login  string     `json:"login"`
}

type Hook struct{}

var eventNamespace = uuid.MustParse("7e4bb90d-80f7-5bb5-9a99-ce4a487b9d32")

var _ issues.MutationEventHook = Hook{}

func (Hook) Emit(ctx context.Context, repository store.RepoStore, mutation issues.MutationEvent) error {
	if mutation.Scope != repository.Scope() || mutation.Scope != mutation.Issue.Scope {
		return fmt.Errorf("outbox: mutation scope mismatch")
	}
	_, eventKey, err := mutationIdentity(mutation)
	if err != nil {
		return err
	}
	eventID := uuid.NewSHA1(eventNamespace, []byte(eventKey))
	envelope, aggregateID, err := BuildEnvelope(eventID, mutation)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("outbox: marshal envelope: %w", err)
	}
	_, err = repository.EnqueueEvent(ctx, models.NewOutboxEvent{ID: eventID,
		SchemaVersion: SchemaVersion, AggregateType: aggregateType(mutation.Type),
		AggregateID: aggregateID, EventType: mutation.Type, EventKey: envelope.EventKey, Payload: payload,
		AvailableAt: envelope.OccurredAt})
	return err
}

func BuildEnvelope(eventID uuid.UUID, mutation issues.MutationEvent) (Envelope, uuid.UUID, error) {
	aggregateID, eventKey, err := mutationIdentity(mutation)
	if err != nil {
		return Envelope{}, uuid.Nil, err
	}
	author := AuthorIdentity{UserID: mutation.Issue.AuthorID}
	occurredAt := mutation.Issue.UpdatedAt
	var comment *CommentRevision
	if mutation.Comment != nil {
		revision := mutation.Comment.Comment
		aggregateID = revision.ID
		author = AuthorIdentity{UserID: revision.AuthorID, Login: mutation.Comment.AuthorLogin}
		occurredAt = revision.UpdatedAt
		comment = &CommentRevision{StableID: revision.ID,
			NumericID:             codec.StableNumericID(revision.ID.String()),
			RepresentationVersion: revision.RepresentationVersion,
			CreatedAt:             revision.CreatedAt, UpdatedAt: revision.UpdatedAt}
	}
	if eventID == uuid.Nil || mutation.Scope.Validate() != nil || mutation.ActorUserID == uuid.Nil {
		return Envelope{}, uuid.Nil, fmt.Errorf("outbox: complete event, scope, actor and stable aggregate identity are required")
	}
	if mutation.Issue.Number <= 0 || mutation.Issue.CreatedAt.IsZero() || mutation.Issue.UpdatedAt.IsZero() {
		return Envelope{}, uuid.Nil, fmt.Errorf("outbox: complete issue identity and timestamps are required")
	}
	if sha256.Sum256([]byte(mutation.RawBody)) != mutation.BodyHash {
		return Envelope{}, uuid.Nil, fmt.Errorf("outbox: raw body hash mismatch")
	}
	if occurredAt.IsZero() {
		return Envelope{}, uuid.Nil, fmt.Errorf("outbox: mutation timestamp is required")
	}
	envelope := Envelope{SchemaVersion: SchemaVersion, EventID: eventID, EventKey: eventKey,
		EventType: mutation.Type, Action: action(mutation.Type), OccurredAt: occurredAt.UTC(),
		OrganizationID: mutation.Scope.OrgID, RepositoryID: mutation.Scope.RepoID,
		Issue: IssueIdentity{StableID: mutation.Issue.ID, Number: mutation.Issue.Number,
			RepresentationVersion: mutation.Issue.RepresentationVersion,
			CreatedAt:             mutation.Issue.CreatedAt, UpdatedAt: mutation.Issue.UpdatedAt},
		Comment: comment, RawBody: mutation.RawBody, BodyHash: hex.EncodeToString(mutation.BodyHash[:]),
		ActorUserID: mutation.ActorUserID, Author: author}
	return envelope, aggregateID, nil
}

func mutationIdentity(mutation issues.MutationEvent) (uuid.UUID, string, error) {
	aggregateID := mutation.Issue.ID
	if mutation.Comment != nil {
		aggregateID = mutation.Comment.Comment.ID
	}
	if aggregateID == uuid.Nil || strings.TrimSpace(mutation.Type) == "" || mutation.RepresentationVersion < 1 {
		return uuid.Nil, "", fmt.Errorf("outbox: event type, representation version and aggregate identity are required")
	}
	return aggregateID, fmt.Sprintf("%s:%s:v%d", mutation.Type, aggregateID, mutation.RepresentationVersion), nil
}

func action(eventType string) string {
	if _, value, ok := strings.Cut(eventType, "."); ok {
		return value
	}
	return eventType
}

func aggregateType(eventType string) string {
	if strings.HasPrefix(eventType, "issue_comment.") {
		return "comment"
	}
	return "issue"
}
