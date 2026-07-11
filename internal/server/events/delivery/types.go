package delivery

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/events/networkpolicy"
	"github.com/higress-group/issue-spec/internal/server/events/subscriptions"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/jackc/pgx/v5"
)

var (
	ErrNotFound  = errors.New("webhook delivery: not found")
	ErrForbidden = errors.New("webhook delivery: forbidden")
	ErrInvalid   = errors.New("webhook delivery: invalid input")
	ErrLeaseLost = errors.New("webhook delivery: lease lost")
	ErrNoWork    = errors.New("webhook delivery: no work")
)

type Authorizer interface {
	EvaluateRepository(context.Context, authz.Subject, authz.RepositoryRequest) (authz.Decision, error)
	EvaluateRepositoryTx(context.Context, pgx.Tx, authz.Subject, authz.RepositoryRequest) (authz.Decision, error)
}

type SecretProvider interface {
	AcceptedSecrets(context.Context, uuid.UUID, uuid.UUID, time.Time) ([]subscriptions.AcceptedSecret, error)
}

type Sender interface {
	Send(context.Context, networkpolicy.Request) (networkpolicy.Result, error)
}

type Config struct {
	LeaseDuration  time.Duration
	MaxConcurrency int
	PollInterval   time.Duration
	Clock          func() time.Time
}

type Delivery struct {
	ID                    uuid.UUID        `json:"id"`
	Scope                 models.RepoScope `json:"scope"`
	EventID               uuid.UUID        `json:"event_id"`
	SubscriptionID        uuid.UUID        `json:"subscription_id"`
	State                 string           `json:"state"`
	NextAttemptAt         time.Time        `json:"next_attempt_at"`
	DeliveredAt           *time.Time       `json:"delivered_at,omitempty"`
	LastError             *string          `json:"last_error,omitempty"`
	RepresentationVersion int64            `json:"representation_version"`
	CreatedAt             time.Time        `json:"created_at"`
	UpdatedAt             time.Time        `json:"updated_at"`
	EventType             string           `json:"event_type"`
	RepositorySequence    int64            `json:"repository_sequence"`
	SecretVersion         int64            `json:"secret_version"`
}

type Attempt struct {
	ID              uuid.UUID   `json:"id"`
	AttemptNumber   int64       `json:"attempt_number"`
	ResponseStatus  *int        `json:"response_status,omitempty"`
	ResponseHeaders http.Header `json:"response_headers,omitempty"`
	Error           *string     `json:"error,omitempty"`
	StartedAt       time.Time   `json:"started_at"`
	CompletedAt     *time.Time  `json:"completed_at,omitempty"`
}

type Detail struct {
	Delivery Delivery  `json:"delivery"`
	Attempts []Attempt `json:"attempts"`
}

type Actor struct {
	UserID      uuid.UUID
	IdentityKey string
	RequestID   string
}

func ActorFromPrincipal(principal serverauth.Principal, requestID string) Actor {
	return Actor{UserID: principal.User.ID,
		IdentityKey: string(principal.Kind) + ":" + principal.User.ID.String(), RequestID: requestID}
}

type claim struct {
	Delivery
	SecretVersionID uuid.UUID
	URL             string
	Payload         []byte
	Retry           subscriptions.RetryPolicy
	GlobalAttempt   int64
	CycleAttempt    int
	LeaseVersion    int64
}
