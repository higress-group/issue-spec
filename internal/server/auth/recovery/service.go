// Package recovery implements offline operator-minted, short-lived one-time
// administrative credentials. No HTTP mint endpoint is exposed.
package recovery

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const MaxTTL = 30 * time.Minute

type Created struct {
	ID        uuid.UUID `json:"id"`
	Plaintext string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

type Service struct {
	pool    *pgxpool.Pool
	secrets *serverauth.Secrets
	now     func() time.Time
}

func New(pool *pgxpool.Pool, secrets *serverauth.Secrets) *Service {
	return &Service{pool: pool, secrets: secrets, now: func() time.Time { return time.Now().UTC() }}
}

// Mint is intended for an operator-only local command after filesystem and DB
// access have already been established. issuedBy identifies the OS/operator
// principal and reason is mandatory audit evidence.
func (s *Service) Mint(ctx context.Context, userID uuid.UUID, issuedBy, reason, requestID string, ttl time.Duration) (Created, error) {
	var created Created
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var err error
		created, err = s.MintInTx(ctx, tx, userID, issuedBy, reason, requestID, ttl)
		return err
	})
	if err != nil {
		return Created{}, fmt.Errorf("recovery: mint: %w", err)
	}
	return created, nil
}

// MintInTx lets bootstrap and offline recovery couple role validation, token
// issuance and immutable audit in one caller-owned transaction.
func (s *Service) MintInTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, issuedBy, reason, requestID string, ttl time.Duration) (Created, error) {
	if userID == uuid.Nil || strings.TrimSpace(issuedBy) == "" || strings.TrimSpace(reason) == "" ||
		strings.TrimSpace(requestID) == "" || ttl <= 0 || ttl > MaxTTL || tx == nil {
		return Created{}, serverauth.ErrInvalidCredential
	}
	plaintext, prefix, err := s.secrets.RandomToken("rcv")
	if err != nil {
		return Created{}, err
	}
	now := s.now()
	created := Created{ID: uuid.New(), Plaintext: plaintext, ExpiresAt: now.Add(ttl)}
	var status string
	if err := tx.QueryRow(ctx, `SELECT u.status FROM users u
		JOIN site_role_assignments sr ON sr.user_id = u.id AND sr.role = 'site_admin'
		WHERE u.id = $1 FOR UPDATE OF u, sr`, userID).Scan(&status); errors.Is(err, pgx.ErrNoRows) {
		return Created{}, serverauth.ErrInvalidCredential
	} else if err != nil {
		return Created{}, err
	}
	if status != "active" {
		return Created{}, serverauth.ErrDisabledAccount
	}
	auditID := uuid.New()
	if _, err := tx.Exec(ctx, `INSERT INTO audit_events
			(id, actor_identity_key, action, resource_type, resource_id, request_id, metadata)
			VALUES ($1, $2, 'recovery.mint', 'recovery_credential', $3, $4, jsonb_build_object('reason', $5::text))`,
		auditID, "operator:"+issuedBy, created.ID, requestID, reason); err != nil {
		return Created{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO recovery_credentials
			(id, user_id, token_prefix, token_hash, scope, issued_by, reason, expires_at, audit_event_id, created_at)
			VALUES ($1, $2, $3, $4, 'site-admin-recovery', $5, $6, $7, $8, $9)`,
		created.ID, userID, prefix, s.secrets.Digest("recovery-token", plaintext), issuedBy,
		reason, created.ExpiresAt, auditID, now); err != nil {
		return Created{}, err
	}
	return created, nil
}

func (s *Service) Consume(ctx context.Context, plaintext, requestID string) (serverauth.Principal, error) {
	var principal serverauth.Principal
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var err error
		principal, err = s.ConsumeInTx(ctx, tx, plaintext, requestID)
		return err
	})
	if err != nil {
		return serverauth.Principal{}, err
	}
	return principal, nil
}

// ConsumeInTx verifies and consumes one recovery credential inside a
// caller-owned transaction. It is used only by the browser-session takeover
// exchange; recovery mint remains operator/bootstrap only.
func (s *Service) ConsumeInTx(ctx context.Context, tx pgx.Tx, plaintext, requestID string) (serverauth.Principal, error) {
	prefix, err := serverauth.TokenPrefix(plaintext, "rcv")
	if s == nil || tx == nil || err != nil || strings.TrimSpace(requestID) == "" {
		return serverauth.Principal{}, serverauth.ErrInvalidCredential
	}
	now := s.now()
	var principal serverauth.Principal
	var digest []byte
	err = tx.QueryRow(ctx, `SELECT r.id, r.token_hash, r.expires_at,
			u.id, u.login, u.display_name, u.email, u.status
			FROM recovery_credentials r JOIN users u ON u.id = r.user_id
			WHERE r.token_prefix = $1 AND r.consumed_at IS NULL AND r.revoked_at IS NULL
			AND r.expires_at > $2 AND u.status = 'active' FOR UPDATE OF r, u`, prefix, now).
		Scan(&principal.CredentialID, &digest, &principal.ExpiresAt, &principal.User.ID,
			&principal.User.Login, &principal.User.DisplayName, &principal.User.Email, &principal.User.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return serverauth.Principal{}, serverauth.ErrInvalidCredential
	}
	if err != nil {
		return serverauth.Principal{}, err
	}
	if !serverauth.EqualDigest(digest, s.secrets.Digest("recovery-token", plaintext)) {
		return serverauth.Principal{}, serverauth.ErrInvalidCredential
	}
	if _, err := tx.Exec(ctx, `UPDATE recovery_credentials SET consumed_at = $2 WHERE id = $1`, principal.CredentialID, now); err != nil {
		return serverauth.Principal{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO audit_events
			(id, actor_user_id, actor_identity_key, action, resource_type, resource_id, request_id)
			VALUES ($1, $2, $3, 'recovery.consume', 'recovery_credential', $4, $5)`,
		uuid.New(), principal.User.ID, "recovery:"+principal.CredentialID.String(), principal.CredentialID, requestID)
	if err != nil {
		return serverauth.Principal{}, err
	}
	principal.Kind = serverauth.CredentialRecovery
	principal.Scopes = []string{"site:admin"}
	return principal, nil
}

func (s *Service) Revoke(ctx context.Context, credentialID uuid.UUID, issuedBy, requestID string) error {
	if credentialID == uuid.Nil || strings.TrimSpace(issuedBy) == "" || strings.TrimSpace(requestID) == "" {
		return serverauth.ErrInvalidCredential
	}
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE recovery_credentials SET revoked_at = clock_timestamp()
			WHERE id = $1 AND consumed_at IS NULL AND revoked_at IS NULL`, credentialID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return serverauth.ErrNotFound
		}
		_, err = tx.Exec(ctx, `INSERT INTO audit_events
			(id, actor_identity_key, action, resource_type, resource_id, request_id)
			VALUES ($1, $2, 'recovery.revoke', 'recovery_credential', $3, $4)`,
			uuid.New(), "operator:"+issuedBy, credentialID, requestID)
		return err
	})
}
