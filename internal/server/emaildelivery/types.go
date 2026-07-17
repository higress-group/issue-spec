// Package emaildelivery provides the durable, provider-neutral outbound email
// boundary shared by verified-address, mention, issue and change notifications.
package emaildelivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	MaxAttempts       = 5
	InitialBackoff    = time.Minute
	MaximumBackoff    = time.Hour
	DefaultLease      = 30 * time.Second
	DefaultPoll       = 100 * time.Millisecond
	DefaultConcurrent = 4
	maxSnapshotBytes  = 256 << 10
)

type Kind string

const (
	KindVerification     Kind = "verification"
	KindMention          Kind = "mention"
	KindRepoIssueCreated Kind = "repo_issue_created"
	KindChangeMilestone  Kind = "change_milestone"
)

func (k Kind) Valid() bool {
	switch k {
	case KindVerification, KindMention, KindRepoIssueCreated, KindChangeMilestone:
		return true
	default:
		return false
	}
}

type State string

const (
	StatePending    State = "pending"
	StateDelivering State = "delivering"
	StateSucceeded  State = "succeeded"
	StateFailed     State = "failed"
	StateSuppressed State = "suppressed"
)

type ReasonCode string

const (
	ReasonPreparationUnavailable ReasonCode = "preparation_unavailable"
	ReasonInvalidMessage         ReasonCode = "invalid_message"
	ReasonRecipientUnavailable   ReasonCode = "recipient_unavailable"
	ReasonPolicyUnavailable      ReasonCode = "policy_unavailable"
	ReasonSMTPUnavailable        ReasonCode = "smtp_unavailable"
	ReasonSMTPTimeout            ReasonCode = "smtp_timeout"
	ReasonSMTPAuthentication     ReasonCode = "smtp_authentication"
	ReasonSMTPTLSValidation      ReasonCode = "smtp_tls_validation"
	ReasonSMTPRejected           ReasonCode = "smtp_rejected"
	ReasonSMTPAmbiguous          ReasonCode = "smtp_ambiguous"
)

var reasonPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func (r ReasonCode) Valid() bool { return reasonPattern.MatchString(string(r)) }

var (
	ErrInvalid   = errors.New("email delivery: invalid input")
	ErrConflict  = errors.New("email delivery: idempotency conflict")
	ErrNoWork    = errors.New("email delivery: no work")
	ErrLeaseLost = errors.New("email delivery: lease lost")
	ErrStore     = errors.New("email delivery: storage unavailable")
)

// OutcomeError carries only a stable reason and disposition. It deliberately
// omits relay text, addresses, credentials, subjects and private content.
type OutcomeError struct {
	Reason     ReasonCode
	Retryable  bool
	Suppressed bool
}

func (e *OutcomeError) Error() string {
	if e == nil || !e.Reason.Valid() {
		return "email delivery failed"
	}
	return "email delivery failed: " + string(e.Reason)
}

func Retryable(reason ReasonCode) error {
	return &OutcomeError{Reason: reason, Retryable: true}
}

func Permanent(reason ReasonCode) error { return &OutcomeError{Reason: reason} }

func Suppressed(reason ReasonCode) error { return &OutcomeError{Reason: reason, Suppressed: true} }

// Message is the plain-text sender boundary. DeliveryID and OccurredAt make
// retry headers deterministic; To is resolved immediately before each send.
type Message struct {
	DeliveryID uuid.UUID
	To         string
	Subject    string
	Body       string
	OccurredAt time.Time
}

func (m Message) String() string {
	return fmt.Sprintf("email message delivery=%s", m.DeliveryID)
}

func (m Message) GoString() string { return m.String() }

func (m Message) Format(state fmt.State, _ rune) {
	_, _ = fmt.Fprint(state, m.String())
}

// Preparer owns recipient reload, live authorization and rendering. Keeping it
// injected prevents transport code from becoming a notification policy engine.
type Preparer interface {
	Prepare(context.Context, Delivery) (Message, error)
}

type Sender interface {
	Send(context.Context, Message) error
}

type Delivery struct {
	ID                    uuid.UUID
	Kind                  Kind
	IdempotencyKey        string
	RecipientUserID       uuid.UUID
	OrganizationID        *uuid.UUID
	RepositoryID          *uuid.UUID
	VerificationRequestID *uuid.UUID
	CommentID             *uuid.UUID
	IssueID               *uuid.UUID
	MilestoneID           *uuid.UUID
	Snapshot              json.RawMessage
	State                 State
	Attempts              int
	NextAttemptAt         time.Time
	LeaseExpiresAt        *time.Time
	DeliveredAt           *time.Time
	LastReason            *ReasonCode
	RepresentationVersion int64
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

type Claim struct {
	Delivery
	LeaseVersion int64
}

type EnqueueInput struct {
	Kind                  Kind
	IdempotencyKey        string
	RecipientUserID       uuid.UUID
	OrganizationID        *uuid.UUID
	RepositoryID          *uuid.UUID
	VerificationRequestID *uuid.UUID
	CommentID             *uuid.UUID
	IssueID               *uuid.UUID
	MilestoneID           *uuid.UUID
	Snapshot              json.RawMessage
	AvailableAt           *time.Time
}

func (input EnqueueInput) validate() (json.RawMessage, error) {
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	if !input.Kind.Valid() || input.RecipientUserID == uuid.Nil || input.IdempotencyKey == "" || len(input.IdempotencyKey) > 512 {
		return nil, ErrInvalid
	}
	hasScope := input.OrganizationID != nil && input.RepositoryID != nil && *input.OrganizationID != uuid.Nil && *input.RepositoryID != uuid.Nil
	if (input.OrganizationID == nil) != (input.RepositoryID == nil) {
		return nil, ErrInvalid
	}
	validReferences := false
	switch input.Kind {
	case KindVerification:
		validReferences = !hasScope && nonNil(input.VerificationRequestID) && input.CommentID == nil && input.IssueID == nil && input.MilestoneID == nil
	case KindMention:
		validReferences = hasScope && input.VerificationRequestID == nil && nonNil(input.CommentID) && input.IssueID == nil && input.MilestoneID == nil
	case KindRepoIssueCreated:
		validReferences = hasScope && input.VerificationRequestID == nil && input.CommentID == nil && nonNil(input.IssueID) && input.MilestoneID == nil
	case KindChangeMilestone:
		validReferences = hasScope && input.VerificationRequestID == nil && input.CommentID == nil && input.IssueID == nil && nonNil(input.MilestoneID)
	}
	if !validReferences {
		return nil, ErrInvalid
	}
	var object map[string]json.RawMessage
	if len(input.Snapshot) == 0 || len(input.Snapshot) > maxSnapshotBytes || json.Unmarshal(input.Snapshot, &object) != nil || object == nil {
		return nil, ErrInvalid
	}
	canonical, err := json.Marshal(object)
	if err != nil || len(canonical) > maxSnapshotBytes {
		return nil, ErrInvalid
	}
	if input.AvailableAt != nil && input.AvailableAt.IsZero() {
		return nil, ErrInvalid
	}
	return canonical, nil
}

func nonNil(value *uuid.UUID) bool { return value != nil && *value != uuid.Nil }

var deliveryNamespace = uuid.MustParse("9c02383f-1c8b-5ae6-8986-daeb5306b321")

// StableDeliveryID is deterministic for one kind-specific logical key.
func StableDeliveryID(kind Kind, idempotencyKey string) (uuid.UUID, error) {
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if !kind.Valid() || idempotencyKey == "" || len(idempotencyKey) > 512 {
		return uuid.Nil, ErrInvalid
	}
	return uuid.NewSHA1(deliveryNamespace, []byte(string(kind)+"\x00"+idempotencyKey)), nil
}

func outcome(err error) *OutcomeError {
	var result *OutcomeError
	if errors.As(err, &result) && result.Reason.Valid() {
		return result
	}
	return &OutcomeError{Reason: ReasonPreparationUnavailable, Retryable: true}
}

func (d Delivery) String() string {
	return fmt.Sprintf("email delivery %s kind=%s state=%s attempt=%d", d.ID, d.Kind, d.State, d.Attempts)
}
