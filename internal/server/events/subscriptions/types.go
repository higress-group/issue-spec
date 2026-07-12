package subscriptions

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

var (
	ErrInvalidInput      = errors.New("webhook subscriptions: invalid input")
	ErrNotFound          = errors.New("webhook subscriptions: not found")
	ErrForbidden         = errors.New("webhook subscriptions: forbidden")
	ErrVersionConflict   = errors.New("webhook subscriptions: version conflict")
	ErrRevoked           = errors.New("webhook subscriptions: revoked")
	ErrUnsafeDestination = errors.New("webhook subscriptions: unsafe stored destination")
)

type ScopeType string

const (
	ScopeOrganization ScopeType = "organization"
	ScopeRepository   ScopeType = "repository"
)

type DeliveryFormat string

const (
	DeliveryFormatIssueSpecV1 DeliveryFormat = "issue-spec.v1"
	DeliveryFormatGitHubV3    DeliveryFormat = "github.v3"
)

type SigningMode string

const (
	SigningModeBearer     SigningMode = "bearer"
	SigningModeNone       SigningMode = "none"
	SigningModeHMACSHA256 SigningMode = "hmac-sha256"
)

type ContentPolicy struct {
	IssueActions   []string `json:"issue_actions"`
	CommentActions []string `json:"comment_actions"`
	IssueKinds     []string `json:"issue_kinds"`
	CommentClasses []string `json:"comment_classes"`
	ActorClasses   []string `json:"actor_classes"`
}

type RetryPolicy struct {
	MaxAttempts    int           `json:"max_attempts"`
	InitialBackoff time.Duration `json:"initial_backoff"`
	MaxBackoff     time.Duration `json:"max_backoff"`
}

type Subscription struct {
	ID                      uuid.UUID      `json:"id"`
	OrganizationID          uuid.UUID      `json:"organization_id"`
	RepositoryID            *uuid.UUID     `json:"repository_id,omitempty"`
	ScopeType               ScopeType      `json:"scope_type"`
	URL                     string         `json:"url"`
	Active                  bool           `json:"active"`
	RevokedAt               *time.Time     `json:"revoked_at,omitempty"`
	EventTypes              []string       `json:"event_types"`
	DeliveryFormat          DeliveryFormat `json:"delivery_format"`
	SigningMode             SigningMode    `json:"signing_mode"`
	ContentPolicy           ContentPolicy  `json:"content_policy"`
	HasDestinationQuery     bool           `json:"has_destination_query"`
	DestinationQueryKeyID   string         `json:"-"`
	DestinationQuery        []byte         `json:"-"`
	DestinationQueryVersion int64          `json:"-"`
	Retry                   RetryPolicy    `json:"retry"`
	RepresentationVersion   int64          `json:"representation_version"`
	CreatedAt               time.Time      `json:"created_at"`
	UpdatedAt               time.Time      `json:"updated_at"`
}

type CreateInput struct {
	OrganizationID uuid.UUID
	RepositoryID   *uuid.UUID
	URL            string
	EventTypes     []string
	DeliveryFormat DeliveryFormat
	SigningMode    SigningMode
	ContentPolicy  ContentPolicy
	Retry          RetryPolicy
}

type UpdateInput struct {
	ExpectedVersion       int64
	URL                   string
	EventTypes            []string
	DeliveryFormat        DeliveryFormat
	SigningMode           SigningMode
	ContentPolicy         ContentPolicy
	ClearDestinationQuery bool
	Active                bool
	Retry                 RetryPolicy
}

type SecretResult struct {
	Subscription  Subscription `json:"subscription"`
	Secret        string       `json:"secret"`
	SecretVersion int64        `json:"secret_version"`
}

type AcceptedSecret struct {
	Version int64
	Secret  []byte
}

type DestinationQuery struct {
	KeyID      string
	Version    int64
	Ciphertext []byte
}

type Suppression struct {
	ID             uuid.UUID `json:"id"`
	OrganizationID uuid.UUID `json:"organization_id"`
	RepositoryID   uuid.UUID `json:"repository_id"`
	EventID        uuid.UUID `json:"event_id"`
	SubscriptionID uuid.UUID `json:"subscription_id"`
	EventType      string    `json:"event_type"`
	Action         string    `json:"action"`
	IssueKind      string    `json:"issue_kind"`
	CommentClass   *string   `json:"comment_class,omitempty"`
	ActorClass     string    `json:"actor_class"`
	Reason         string    `json:"reason"`
	CreatedAt      time.Time `json:"created_at"`
}

type Actor struct {
	UserID      uuid.UUID
	IdentityKey string
	RequestID   string
}
