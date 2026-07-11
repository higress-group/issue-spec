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

type RetryPolicy struct {
	MaxAttempts    int           `json:"max_attempts"`
	InitialBackoff time.Duration `json:"initial_backoff"`
	MaxBackoff     time.Duration `json:"max_backoff"`
}

type Subscription struct {
	ID                    uuid.UUID   `json:"id"`
	OrganizationID        uuid.UUID   `json:"organization_id"`
	RepositoryID          *uuid.UUID  `json:"repository_id,omitempty"`
	ScopeType             ScopeType   `json:"scope_type"`
	URL                   string      `json:"url"`
	Active                bool        `json:"active"`
	RevokedAt             *time.Time  `json:"revoked_at,omitempty"`
	EventTypes            []string    `json:"event_types"`
	Retry                 RetryPolicy `json:"retry"`
	RepresentationVersion int64       `json:"representation_version"`
	CreatedAt             time.Time   `json:"created_at"`
	UpdatedAt             time.Time   `json:"updated_at"`
}

type CreateInput struct {
	OrganizationID uuid.UUID
	RepositoryID   *uuid.UUID
	URL            string
	EventTypes     []string
	Retry          RetryPolicy
}

type UpdateInput struct {
	ExpectedVersion int64
	URL             string
	EventTypes      []string
	Active          bool
	Retry           RetryPolicy
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

type Actor struct {
	UserID      uuid.UUID
	IdentityKey string
	RequestID   string
}
