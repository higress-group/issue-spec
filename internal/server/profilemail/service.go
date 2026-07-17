package profilemail

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/emaildelivery"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const tokenPurpose = "notification-email-verification"

var supersededReason = emaildelivery.ReasonCode("verification_superseded")

type Service struct {
	pool    *pgxpool.Pool
	secrets *serverauth.Secrets
	config  Config
	now     func() time.Time
}

func New(pool *pgxpool.Pool, secrets *serverauth.Secrets, config Config) (*Service, error) {
	if pool == nil || secrets == nil {
		return nil, ErrInvalid
	}
	if config.VerificationTTL == 0 {
		config.VerificationTTL = DefaultVerificationTTL
	}
	if config.RateWindow == 0 {
		config.RateWindow = DefaultRateWindow
	}
	if config.RateLimit == 0 {
		config.RateLimit = DefaultRateLimit
	}
	if config.ResendInterval == 0 {
		config.ResendInterval = DefaultResendInterval
	}
	if config.VerificationTTL <= 0 || config.RateWindow <= 0 || config.RateLimit < 1 || config.ResendInterval < 0 {
		return nil, ErrInvalid
	}
	return &Service{pool: pool, secrets: secrets, config: config, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (s *Service) Get(ctx context.Context, userID uuid.UUID) (Profile, error) {
	if s == nil || userID == uuid.Nil {
		return Profile{}, ErrInvalid
	}
	var result Profile
	result.UserID = userID
	var pendingID *uuid.UUID
	var pendingEmail *string
	var expiresAt, sentAt, pendingCreated *time.Time
	var pendingVersion *int64
	err := s.pool.QueryRow(ctx, `SELECT u.onboarding_completed_at, u.notification_email, u.notification_email_verified_at,
		u.representation_version, r.id, r.pending_email, r.expires_at, r.sent_at,
		r.representation_version, r.created_at
		FROM users u LEFT JOIN LATERAL (
			SELECT id, pending_email, expires_at, sent_at, representation_version, created_at
			FROM email_verification_requests
			WHERE user_id = u.id AND consumed_at IS NULL AND superseded_at IS NULL
			ORDER BY created_at DESC, id DESC LIMIT 1
		) r ON true WHERE u.id = $1`, userID).Scan(&result.OnboardingCompletedAt, &result.NotificationEmail,
		&result.NotificationVerifiedAt, &result.RepresentationVersion, &pendingID, &pendingEmail,
		&expiresAt, &sentAt, &pendingVersion, &pendingCreated)
	if errors.Is(err, pgx.ErrNoRows) {
		return Profile{}, ErrNotFound
	}
	if err != nil {
		return Profile{}, safeError(err)
	}
	if pendingID != nil && pendingEmail != nil && expiresAt != nil && pendingVersion != nil && pendingCreated != nil {
		result.Pending = &Verification{ID: *pendingID, UserID: userID, PendingEmail: *pendingEmail,
			ExpiresAt: *expiresAt, SentAt: sentAt, RepresentationVersion: *pendingVersion, CreatedAt: *pendingCreated}
	}
	return result, nil
}

// Set starts initial verification or atomically replaces an outstanding
// request. An already verified address remains active until the new address is
// confirmed. The caller's user version prevents stale profile forms winning.
func (s *Service) Set(ctx context.Context, input SetInput) (Verification, error) {
	address, err := normalizeAddress(input.Email)
	if s == nil || input.UserID == uuid.Nil || input.ExpectedUserVersion < 1 || err != nil {
		return Verification{}, ErrInvalid
	}
	var result Verification
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) error {
		now := s.now().Truncate(time.Microsecond)
		if err := lockUser(ctx, tx, input.UserID, input.ExpectedUserVersion); err != nil {
			return err
		}
		if err := s.checkRate(ctx, tx, input.UserID, now); err != nil {
			return err
		}
		if err := supersedeCurrent(ctx, tx, input.UserID, now, 0); err != nil {
			return err
		}
		result, err = s.issue(ctx, tx, input.UserID, address, now)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE users SET onboarding_completed_at = COALESCE(onboarding_completed_at, $3),
			representation_version = representation_version + 1,
			updated_at = $3 WHERE id = $1 AND representation_version = $2`, input.UserID,
			input.ExpectedUserVersion, now)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		return nil
	})
	if err != nil {
		return Verification{}, safeError(err)
	}
	return result, nil
}

// InspectForUser validates a confirmation token without consuming it. The
// authenticated user binding prevents one signed-in account from learning
// another account's pending address or consuming its link.
func (s *Service) InspectForUser(ctx context.Context, userID uuid.UUID, token string) (Confirmation, error) {
	if s == nil || userID == uuid.Nil || strings.TrimSpace(token) == "" {
		return Confirmation{}, ErrInvalid
	}
	var result Confirmation
	var consumed, superseded *time.Time
	err := s.pool.QueryRow(ctx, `SELECT id, expires_at, consumed_at, superseded_at, representation_version
		FROM email_verification_requests WHERE user_id = $1 AND token_digest = $2`, userID,
		s.secrets.Digest(tokenPurpose, token)).Scan(&result.RequestID, &result.ExpiresAt,
		&consumed, &superseded, &result.RepresentationVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return Confirmation{}, ErrNotFound
	}
	if err != nil {
		return Confirmation{}, safeError(err)
	}
	switch {
	case consumed != nil:
		return Confirmation{}, ErrConsumed
	case superseded != nil:
		return Confirmation{}, ErrSuperseded
	case !result.ExpiresAt.After(s.now()):
		return Confirmation{}, ErrExpired
	default:
		return result, nil
	}
}

func (s *Service) ConfirmForUser(ctx context.Context, userID uuid.UUID, token string) (Confirmed, error) {
	if userID == uuid.Nil {
		return Confirmed{}, ErrInvalid
	}
	return s.confirm(ctx, userID, token)
}

// Resend rotates the token rather than reviving sent ciphertext. This makes
// every confirmation link single-request and lets old links be fenced.
func (s *Service) Resend(ctx context.Context, input ResendInput) (Verification, error) {
	if s == nil || input.UserID == uuid.Nil || input.ExpectedUserVersion < 1 || input.ExpectedVerificationVersion < 1 {
		return Verification{}, ErrInvalid
	}
	var result Verification
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) error {
		now := s.now().Truncate(time.Microsecond)
		if err := lockUser(ctx, tx, input.UserID, input.ExpectedUserVersion); err != nil {
			return err
		}
		var address string
		var created time.Time
		err := tx.QueryRow(ctx, `SELECT pending_email, created_at FROM email_verification_requests
			WHERE user_id = $1 AND consumed_at IS NULL AND superseded_at IS NULL
			AND representation_version = $2 FOR UPDATE`, input.UserID,
			input.ExpectedVerificationVersion).Scan(&address, &created)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict
		}
		if err != nil {
			return err
		}
		if now.Before(created.Add(s.config.ResendInterval)) {
			return ErrRateLimited
		}
		if err := s.checkRate(ctx, tx, input.UserID, now); err != nil {
			return err
		}
		if err := supersedeCurrent(ctx, tx, input.UserID, now, input.ExpectedVerificationVersion); err != nil {
			return err
		}
		result, err = s.issue(ctx, tx, input.UserID, address, now)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE users SET representation_version = representation_version + 1,
			updated_at = $3 WHERE id = $1 AND representation_version = $2`, input.UserID,
			input.ExpectedUserVersion, now)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		return nil
	})
	if err != nil {
		return Verification{}, safeError(err)
	}
	return result, nil
}

func (s *Service) Remove(ctx context.Context, input RemoveInput) error {
	if s == nil || input.UserID == uuid.Nil || input.ExpectedUserVersion < 1 {
		return ErrInvalid
	}
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) error {
		now := s.now().Truncate(time.Microsecond)
		if err := lockUser(ctx, tx, input.UserID, input.ExpectedUserVersion); err != nil {
			return err
		}
		if err := supersedeCurrent(ctx, tx, input.UserID, now, 0); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE users SET notification_email = NULL,
			notification_email_verified_at = NULL, representation_version = representation_version + 1,
			updated_at = $3 WHERE id = $1 AND representation_version = $2`, input.UserID,
			input.ExpectedUserVersion, now)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		return nil
	})
	return safeError(err)
}

// Confirm consumes exactly one live request. Digest lookup, row locks and the
// representation predicate jointly fence expiry, replay and concurrent edits.
func (s *Service) Confirm(ctx context.Context, token string) (Confirmed, error) {
	return s.confirm(ctx, uuid.Nil, token)
}

func (s *Service) confirm(ctx context.Context, expectedUserID uuid.UUID, token string) (Confirmed, error) {
	if s == nil || strings.TrimSpace(token) == "" {
		return Confirmed{}, ErrInvalid
	}
	digest := s.secrets.Digest(tokenPurpose, token)
	var result Confirmed
	expired := false
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) error {
		now := s.now().Truncate(time.Microsecond)
		var requestID uuid.UUID
		var expires time.Time
		var consumed, superseded *time.Time
		var requestVersion int64
		var userStatus string
		err := tx.QueryRow(ctx, `SELECT r.id, r.user_id, r.pending_email, r.expires_at,
			r.consumed_at, r.superseded_at, r.representation_version, u.status
			FROM email_verification_requests r JOIN users u ON u.id = r.user_id
			WHERE r.token_digest = $1 FOR UPDATE OF r, u`, digest).Scan(&requestID,
			&result.UserID, &result.NotificationEmail, &expires, &consumed, &superseded, &requestVersion, &userStatus)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if expectedUserID != uuid.Nil && result.UserID != expectedUserID {
			return ErrNotFound
		}
		if userStatus != "active" {
			return ErrAccountDisabled
		}
		switch {
		case consumed != nil:
			return ErrConsumed
		case superseded != nil:
			return ErrSuperseded
		case !expires.After(now):
			tag, err := tx.Exec(ctx, `UPDATE email_verification_requests SET superseded_at = $2,
				token_ciphertext = NULL, representation_version = representation_version + 1,
				updated_at = $2 WHERE id = $1 AND representation_version = $3`, requestID, now, requestVersion)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return ErrConflict
			}
			queue, _ := emaildelivery.NewStore(tx)
			if err := queue.SuppressVerification(ctx, requestID, now, emaildelivery.ReasonRecipientUnavailable); err != nil {
				return err
			}
			expired = true
			return nil
		}
		tag, err := tx.Exec(ctx, `UPDATE email_verification_requests SET consumed_at = $2,
			token_ciphertext = NULL, representation_version = representation_version + 1, updated_at = $2
			WHERE id = $1 AND representation_version = $3 AND consumed_at IS NULL
			AND superseded_at IS NULL AND expires_at > $2`, requestID, now, requestVersion)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		err = tx.QueryRow(ctx, `UPDATE users SET notification_email = $2,
			notification_email_verified_at = $3, representation_version = representation_version + 1,
			updated_at = $3 WHERE id = $1
			RETURNING representation_version`, result.UserID, result.NotificationEmail, now).
			Scan(&result.RepresentationVersion)
		if err != nil {
			return err
		}
		result.VerifiedAt = now
		return nil
	})
	if err != nil {
		return Confirmed{}, safeError(err)
	}
	if expired {
		return Confirmed{}, ErrExpired
	}
	return result, nil
}

func (s *Service) issue(ctx context.Context, tx pgx.Tx, userID uuid.UUID, address string, now time.Time) (Verification, error) {
	token, _, err := s.secrets.RandomToken("email_verify")
	if err != nil {
		return Verification{}, err
	}
	request := Verification{ID: uuid.New(), UserID: userID, PendingEmail: address,
		ExpiresAt: now.Add(s.config.VerificationTTL), RepresentationVersion: 1, CreatedAt: now}
	ciphertext, err := s.secrets.Encrypt(tokenCipherPurpose(request.ID), []byte(token))
	if err != nil {
		return Verification{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO email_verification_requests
		(id, user_id, pending_email, token_digest, token_ciphertext, expires_at, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$7)`, request.ID, userID, address,
		s.secrets.Digest(tokenPurpose, token), ciphertext, request.ExpiresAt, now)
	if err != nil {
		return Verification{}, err
	}
	queue, err := emaildelivery.NewStore(tx)
	if err != nil {
		return Verification{}, err
	}
	snapshot := []byte(`{"template":"notification_email_verification","version":1}`)
	_, _, err = queue.Enqueue(ctx, emaildelivery.EnqueueInput{Kind: emaildelivery.KindVerification,
		IdempotencyKey: request.ID.String(), RecipientUserID: userID,
		VerificationRequestID: &request.ID, Snapshot: snapshot})
	if err != nil {
		return Verification{}, err
	}
	return request, nil
}

// Expire clears temporary token ciphertext and fences queued mail for expired
// requests. It is safe for multiple maintenance workers to call concurrently.
func (s *Service) Expire(ctx context.Context, limit int) (int, error) {
	if s == nil || limit < 1 || limit > 1000 {
		return 0, ErrInvalid
	}
	count := 0
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		now := s.now().Truncate(time.Microsecond)
		rows, err := tx.Query(ctx, `SELECT id FROM email_verification_requests
			WHERE consumed_at IS NULL AND superseded_at IS NULL AND expires_at <= $1
			ORDER BY expires_at, id FOR UPDATE SKIP LOCKED LIMIT $2`, now, limit)
		if err != nil {
			return err
		}
		var ids []uuid.UUID
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		queue, _ := emaildelivery.NewStore(tx)
		for _, id := range ids {
			tag, err := tx.Exec(ctx, `UPDATE email_verification_requests SET superseded_at = $2,
				token_ciphertext = NULL, representation_version = representation_version + 1,
				updated_at = $2 WHERE id = $1 AND consumed_at IS NULL AND superseded_at IS NULL
				AND expires_at <= $2`, id, now)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				continue
			}
			if err := queue.SuppressVerification(ctx, id, now, emaildelivery.ReasonRecipientUnavailable); err != nil {
				return err
			}
			count++
		}
		return nil
	})
	if err != nil {
		return 0, safeError(err)
	}
	return count, nil
}

func (s *Service) checkRate(ctx context.Context, tx pgx.Tx, userID uuid.UUID, now time.Time) error {
	var count int
	err := tx.QueryRow(ctx, `SELECT count(*) FROM email_verification_requests
		WHERE user_id = $1 AND created_at >= $2`, userID, now.Add(-s.config.RateWindow)).Scan(&count)
	if err != nil {
		return err
	}
	if count >= s.config.RateLimit {
		return ErrRateLimited
	}
	return nil
}

func lockUser(ctx context.Context, tx pgx.Tx, userID uuid.UUID, expectedVersion int64) error {
	var status string
	var actual int64
	err := tx.QueryRow(ctx, `SELECT status, representation_version FROM users WHERE id = $1 FOR UPDATE`, userID).
		Scan(&status, &actual)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if status != "active" {
		return ErrAccountDisabled
	}
	if actual != expectedVersion {
		return ErrConflict
	}
	return nil
}

func supersedeCurrent(ctx context.Context, tx pgx.Tx, userID uuid.UUID, now time.Time, expectedVersion int64) error {
	rows, err := tx.Query(ctx, `UPDATE email_verification_requests SET superseded_at = $2,
		token_ciphertext = NULL, representation_version = representation_version + 1, updated_at = $2
		WHERE user_id = $1 AND consumed_at IS NULL AND superseded_at IS NULL
		AND ($3::bigint = 0 OR representation_version = $3) RETURNING id`, userID, now, expectedVersion)
	if err != nil {
		return err
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if expectedVersion > 0 && len(ids) != 1 {
		return ErrConflict
	}
	queue, _ := emaildelivery.NewStore(tx)
	for _, id := range ids {
		if err := queue.SuppressVerification(ctx, id, now, supersededReason); err != nil {
			return err
		}
	}
	return nil
}

func normalizeAddress(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 320 || strings.ContainsAny(value, "\r\n") {
		return "", ErrInvalid
	}
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Name != "" || parsed.Address != value || strings.Count(value, "@") != 1 {
		return "", ErrInvalid
	}
	return value, nil
}

func tokenCipherPurpose(requestID uuid.UUID) string {
	return tokenPurpose + ":ciphertext:" + requestID.String()
}

func safeError(err error) error {
	if err == nil {
		return nil
	}
	for _, known := range []error{ErrInvalid, ErrNotFound, ErrConflict, ErrEmailInUse, ErrExpired,
		ErrConsumed, ErrSuperseded, ErrRateLimited, ErrAccountDisabled} {
		if errors.Is(err, known) {
			return known
		}
	}
	var postgres *pgconn.PgError
	if errors.As(err, &postgres) {
		switch postgres.Code {
		case "23505":
			if postgres.ConstraintName == "users_notification_email_key_unique" {
				return ErrEmailInUse
			}
			return ErrConflict
		case "40001":
			return ErrConflict
		}
	}
	return fmt.Errorf("%w", ErrUnavailable)
}
