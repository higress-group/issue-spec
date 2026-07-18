// Package profilemail manages the user-selected, verified notification email.
// Provider identity email is deliberately outside this package's trust boundary.
package profilemail

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

const (
	DefaultVerificationTTL = 24 * time.Hour
	DefaultRateWindow      = time.Hour
	DefaultRateLimit       = 5
	DefaultResendInterval  = time.Minute
)

var (
	ErrInvalid         = errors.New("profile mail: invalid input")
	ErrNotFound        = errors.New("profile mail: user or verification not found")
	ErrConflict        = errors.New("profile mail: representation changed")
	ErrEmailInUse      = errors.New("profile mail: notification email is already in use")
	ErrExpired         = errors.New("profile mail: verification expired")
	ErrConsumed        = errors.New("profile mail: verification already consumed")
	ErrSuperseded      = errors.New("profile mail: verification superseded")
	ErrRateLimited     = errors.New("profile mail: verification rate limited")
	ErrAccountDisabled = errors.New("profile mail: account disabled")
	ErrUnavailable     = errors.New("profile mail: storage unavailable")
)

type Config struct {
	VerificationTTL time.Duration
	RateWindow      time.Duration
	RateLimit       int
	ResendInterval  time.Duration
	ConfirmationURL string
	Subject         string
}

// Profile exposes only the explicitly verified notification address. It never
// falls back to provider-owned users.email metadata.
type Profile struct {
	UserID                 uuid.UUID
	OnboardingCompletedAt  *time.Time
	NotificationEmail      *string
	NotificationVerifiedAt *time.Time
	Pending                *Verification
	RepresentationVersion  int64
}

// Verification is safe to return from service methods. In particular it does
// not contain the plaintext token or its temporary ciphertext.
type Verification struct {
	ID                    uuid.UUID
	UserID                uuid.UUID
	PendingEmail          string
	ExpiresAt             time.Time
	SentAt                *time.Time
	RepresentationVersion int64
	CreatedAt             time.Time
}

type SetInput struct {
	UserID              uuid.UUID
	Email               string
	ExpectedUserVersion int64
}

// OnboardingInput is the one operation allowed to complete first-session
// onboarding. The preferred name and verification request are committed in the
// same transaction so neither half can become visible by itself.
type OnboardingInput struct {
	UserID              uuid.UUID
	PreferredName       string
	Email               string
	ExpectedUserVersion int64
}

type ResendInput struct {
	UserID                      uuid.UUID
	ExpectedUserVersion         int64
	ExpectedVerificationVersion int64
}

type RemoveInput struct {
	UserID              uuid.UUID
	ExpectedUserVersion int64
}

type Confirmed struct {
	UserID                uuid.UUID
	NotificationEmail     string
	VerifiedAt            time.Time
	RepresentationVersion int64
}
