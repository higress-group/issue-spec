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
	SchemaVersion  int                `json:"schema_version"`
	EventID        uuid.UUID          `json:"event_id"`
	EventKey       string             `json:"event_key"`
	EventType      string             `json:"event_type"`
	Action         string             `json:"action"`
	OccurredAt     time.Time          `json:"occurred_at"`
	OrganizationID uuid.UUID          `json:"organization_id"`
	RepositoryID   uuid.UUID          `json:"repository_id"`
	Issue          IssueIdentity      `json:"issue"`
	Comment        *CommentRevision   `json:"comment,omitempty"`
	RawBody        string             `json:"raw_body"`
	BodyHash       string             `json:"body_hash"`
	ActorUserID    uuid.UUID          `json:"actor_user_id"`
	Author         AuthorIdentity     `json:"author"`
	Notification   *NotificationFacts `json:"notification,omitempty"`
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

type NotificationFacts struct {
	IssueKind    string                   `json:"issue_kind"`
	CommentClass string                   `json:"comment_class,omitempty"`
	ActorClass   string                   `json:"actor_class"`
	Organization NotificationOrganization `json:"organization"`
	Repository   NotificationRepository   `json:"repository"`
	Sender       NotificationUser         `json:"sender"`
	Issue        NotificationIssue        `json:"issue"`
	Comment      *NotificationComment     `json:"comment,omitempty"`
}

type NotificationOrganization struct {
	ID          uuid.UUID `json:"id"`
	Login       string    `json:"login"`
	DisplayName string    `json:"display_name"`
}
type NotificationRepository struct {
	ID       uuid.UUID `json:"id"`
	Name     string    `json:"name"`
	FullName string    `json:"full_name"`
	Private  bool      `json:"private"`
}
type NotificationUser struct {
	ID    uuid.UUID `json:"id"`
	Login string    `json:"login"`
}
type NotificationLabel struct {
	Name        string `json:"name"`
	Color       string `json:"color"`
	Description string `json:"description,omitempty"`
}
type NotificationIssue struct {
	ID        uuid.UUID           `json:"id"`
	Number    int64               `json:"number"`
	Title     string              `json:"title"`
	Body      string              `json:"body"`
	State     string              `json:"state"`
	Author    NotificationUser    `json:"author"`
	Labels    []NotificationLabel `json:"labels"`
	CreatedAt time.Time           `json:"created_at"`
	UpdatedAt time.Time           `json:"updated_at"`
	ClosedAt  *time.Time          `json:"closed_at,omitempty"`
}
type NotificationComment struct {
	ID        uuid.UUID        `json:"id"`
	NumericID int64            `json:"numeric_id"`
	Body      string           `json:"body"`
	Author    NotificationUser `json:"author"`
	CreatedAt time.Time        `json:"created_at"`
	UpdatedAt time.Time        `json:"updated_at"`
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
	var commentID *uuid.UUID
	if mutation.Comment != nil {
		commentID = &mutation.Comment.Comment.ID
	}
	snapshot, err := repository.NotificationSnapshot(ctx, mutation.Issue.Number, commentID, mutation.ActorUserID)
	if err != nil {
		return fmt.Errorf("outbox: notification snapshot: %w", err)
	}
	envelope.Notification = notificationFacts(snapshot, mutation)
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

func notificationFacts(snapshot store.NotificationSnapshot, mutation issues.MutationEvent) *NotificationFacts {
	issue := snapshot.Issue.Issue
	labels := make([]NotificationLabel, 0, len(snapshot.Issue.Labels))
	for _, label := range snapshot.Issue.Labels {
		labels = append(labels, NotificationLabel{Name: label.Name, Color: label.Color, Description: label.Description})
	}
	result := &NotificationFacts{IssueKind: snapshot.IssueKind, ActorClass: "human",
		Organization: NotificationOrganization{ID: mutation.Scope.OrgID, Login: snapshot.OrganizationName, DisplayName: snapshot.OrganizationDisplayName},
		Repository:   NotificationRepository{ID: mutation.Scope.RepoID, Name: snapshot.RepositoryName, FullName: snapshot.OrganizationName + "/" + snapshot.RepositoryName, Private: snapshot.RepositoryVisibility == "private"},
		Sender:       NotificationUser{ID: mutation.ActorUserID, Login: snapshot.ActorLogin},
		Issue: NotificationIssue{ID: issue.ID, Number: issue.Number, Title: issue.Title, Body: issue.Body,
			State: string(issue.State), Author: NotificationUser{Login: snapshot.Issue.AuthorLogin}, Labels: labels,
			CreatedAt: issue.CreatedAt, UpdatedAt: issue.UpdatedAt, ClosedAt: issue.ClosedAt}}
	if issue.AuthorID != nil {
		result.Issue.Author.ID = *issue.AuthorID
	}
	if mutation.Comment != nil {
		comment := mutation.Comment.Comment
		result.CommentClass = "human-untyped"
		if snapshot.CommentTyped {
			result.CommentClass = "typed"
		}
		result.Comment = &NotificationComment{ID: comment.ID, NumericID: codec.StableNumericID(comment.ID.String()),
			Body: comment.Body, Author: NotificationUser{Login: mutation.Comment.AuthorLogin},
			CreatedAt: comment.CreatedAt, UpdatedAt: comment.UpdatedAt}
		if comment.AuthorID != nil {
			result.Comment.Author.ID = *comment.AuthorID
		}
	}
	return result
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
