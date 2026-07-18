package profilemail

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/emaildelivery"
	serverstore "github.com/higress-group/issue-spec/internal/server/store"
	"github.com/jackc/pgx/v5"
)

type preparedVerification struct {
	RequestID  uuid.UUID
	UserID     uuid.UUID
	Address    string
	Ciphertext []byte
	ExpiresAt  time.Time
	CreatedAt  time.Time
}

type verificationLoader interface {
	loadVerification(context.Context, uuid.UUID, uuid.UUID) (preparedVerification, error)
}

type databaseLoader struct{ db serverstore.DBTX }

func (l databaseLoader) loadVerification(ctx context.Context, requestID, userID uuid.UUID) (preparedVerification, error) {
	var result preparedVerification
	err := l.db.QueryRow(ctx, `SELECT r.id, r.user_id, r.pending_email, r.token_ciphertext,
		r.expires_at, r.created_at FROM email_verification_requests r
		JOIN users u ON u.id = r.user_id
		WHERE r.id = $1 AND r.user_id = $2 AND r.consumed_at IS NULL
		AND r.superseded_at IS NULL AND u.status = 'active'`, requestID, userID).
		Scan(&result.RequestID, &result.UserID, &result.Address, &result.Ciphertext,
			&result.ExpiresAt, &result.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return preparedVerification{}, emaildelivery.Suppressed(emaildelivery.ReasonRecipientUnavailable)
	}
	if err != nil {
		return preparedVerification{}, emaildelivery.Retryable(emaildelivery.ReasonPreparationUnavailable)
	}
	return result, nil
}

// VerificationPreparer reloads all private data immediately before sending.
// Delivery snapshots consequently contain neither addresses nor confirmation
// tokens, and a superseded/disabled recipient is suppressed at send time.
type VerificationPreparer struct {
	loader  verificationLoader
	secrets *serverauth.Secrets
	baseURL *url.URL
	subject string
	policy  emaildelivery.AddressPolicy
	now     func() time.Time
}

func NewVerificationPreparer(db serverstore.DBTX, secrets *serverauth.Secrets, config Config) (*VerificationPreparer, error) {
	if db == nil || secrets == nil {
		return nil, ErrInvalid
	}
	baseURL, err := url.Parse(strings.TrimSpace(config.ConfirmationURL))
	if err != nil || baseURL.Scheme == "" || baseURL.Host == "" || (baseURL.Scheme != "https" && baseURL.Scheme != "http") {
		return nil, ErrInvalid
	}
	if config.Subject == "" {
		config.Subject = "Confirm your notification email"
	}
	return &VerificationPreparer{loader: databaseLoader{db: db}, secrets: secrets,
		baseURL: baseURL, subject: config.Subject, policy: config.AddressPolicy,
		now: func() time.Time { return time.Now().UTC() }}, nil
}

func (p *VerificationPreparer) Prepare(ctx context.Context, delivery emaildelivery.Delivery) (emaildelivery.Message, error) {
	if p == nil || delivery.Kind != emaildelivery.KindVerification || delivery.ID == uuid.Nil ||
		delivery.RecipientUserID == uuid.Nil || delivery.VerificationRequestID == nil ||
		*delivery.VerificationRequestID == uuid.Nil {
		return emaildelivery.Message{}, emaildelivery.Permanent(emaildelivery.ReasonInvalidMessage)
	}
	request, err := p.loader.loadVerification(ctx, *delivery.VerificationRequestID, delivery.RecipientUserID)
	if err != nil {
		return emaildelivery.Message{}, err
	}
	if !request.ExpiresAt.After(p.now()) || len(request.Ciphertext) == 0 {
		return emaildelivery.Message{}, emaildelivery.Suppressed(emaildelivery.ReasonRecipientUnavailable)
	}
	if !p.policy.Allows(request.Address) {
		return emaildelivery.Message{}, emaildelivery.Suppressed(emaildelivery.ReasonRecipientUnavailable)
	}
	plaintext, err := p.secrets.Decrypt(tokenCipherPurpose(request.RequestID), request.Ciphertext)
	if err != nil {
		return emaildelivery.Message{}, emaildelivery.Permanent(emaildelivery.ReasonInvalidMessage)
	}
	confirmation := *p.baseURL
	// Keep the bearer token in the browser fragment. Fragments are not sent in
	// the initial navigation request or Referer; the SPA explicitly inspects and
	// then consumes it through authenticated API calls.
	confirmation.Fragment = url.Values{"token": []string{string(plaintext)}}.Encode()
	body := fmt.Sprintf("Confirm this address for issue-spec notifications:\n\n%s\n\nThis link expires at %s. If you did not request this change, ignore this email.\n",
		confirmation.String(), request.ExpiresAt.UTC().Format(time.RFC3339))
	return emaildelivery.Message{DeliveryID: delivery.ID, To: request.Address,
		Subject: p.subject, Body: body, OccurredAt: request.CreatedAt}, nil
}
