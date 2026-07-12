package webhook

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	MinSecretBytes = 32
	MaxSecretBytes = 64 << 10
)

type Secret struct {
	Value      []byte
	ValidUntil time.Time
	Revoked    bool
}

type credential struct {
	hash       [sha256.Size]byte
	validUntil time.Time
	revoked    bool
}

// Credentials binds every accepted secret to one operator-configured
// subscription. Request bodies can never select a different credential.
type Credentials struct {
	subscriptionID string
	secrets        []credential
}

func NewCredentials(subscriptionID string, current Secret, previous []Secret) (*Credentials, error) {
	parsed, err := uuid.Parse(strings.TrimSpace(subscriptionID))
	if err != nil || parsed == uuid.Nil || len(current.Value) < MinSecretBytes || len(current.Value) > MaxSecretBytes {
		return nil, errors.New("webhook credentials: subscription id and current secret are required")
	}
	if len(previous) > 4 {
		return nil, errors.New("webhook credentials: at most four previous secrets are supported")
	}
	for _, item := range previous {
		if item.ValidUntil.IsZero() {
			return nil, errors.New("webhook credentials: previous secret expiry is required")
		}
	}
	all := append([]Secret{current}, previous...)
	credentials := make([]credential, 0, len(all))
	seen := make(map[[sha256.Size]byte]struct{}, len(all))
	for _, item := range all {
		if len(item.Value) < MinSecretBytes || len(item.Value) > MaxSecretBytes {
			return nil, errors.New("webhook credentials: secret length is outside safe bounds")
		}
		hash := sha256.Sum256(item.Value)
		if _, duplicate := seen[hash]; duplicate {
			return nil, errors.New("webhook credentials: duplicate secret")
		}
		seen[hash] = struct{}{}
		credentials = append(credentials, credential{hash: hash,
			validUntil: item.ValidUntil, revoked: item.Revoked})
	}
	return &Credentials{subscriptionID: parsed.String(), secrets: credentials}, nil
}

func (c *Credentials) SubscriptionID() string {
	if c == nil {
		return ""
	}
	return c.subscriptionID
}

// Authenticate hashes even absent/malformed input and compares against every
// configured record. Expired and revoked records still participate in the same
// fixed-width comparisons but can never authorize a request.
func (c *Credentials) Authenticate(authorization string, now time.Time) bool {
	provided := ""
	if strings.HasPrefix(authorization, "Bearer ") && strings.Count(authorization, " ") == 1 {
		provided = strings.TrimPrefix(authorization, "Bearer ")
	}
	providedHash := sha256.Sum256([]byte(provided))
	matchedValid := 0
	if c == nil {
		return false
	}
	for _, item := range c.secrets {
		matched := subtle.ConstantTimeCompare(providedHash[:], item.hash[:])
		valid := 1
		if item.revoked || (!item.validUntil.IsZero() && !now.Before(item.validUntil)) || now.IsZero() {
			valid = 0
		}
		matchedValid |= matched & valid
	}
	return matchedValid == 1 && provided != ""
}
