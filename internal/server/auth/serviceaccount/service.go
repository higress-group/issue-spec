// Package serviceaccount provides explicit non-human identities. Credentials
// are minted through the PAT/delegation services rather than stored here.
package serviceaccount

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var unsafeName = regexp.MustCompile(`[^a-z0-9-]+`)

type Account struct {
	ID             uuid.UUID  `json:"id"`
	UserID         uuid.UUID  `json:"user_id"`
	OrganizationID uuid.UUID  `json:"organization_id"`
	Name           string     `json:"name"`
	Login          string     `json:"login"`
	DisabledAt     *time.Time `json:"disabled_at,omitempty"`
}

type Service struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

func (s *Service) Create(ctx context.Context, actorID, orgID uuid.UUID, name, requestID string) (Account, error) {
	name = strings.TrimSpace(name)
	if actorID == uuid.Nil || orgID == uuid.Nil || name == "" || strings.TrimSpace(requestID) == "" {
		return Account{}, serverauth.ErrInvalidCredential
	}
	account := Account{ID: uuid.New(), UserID: uuid.New(), OrganizationID: orgID, Name: name}
	base := strings.Trim(unsafeName.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if base == "" {
		base = "service"
	}
	digest := sha256.Sum256(append(orgID[:], []byte(name)...))
	account.Login = "svc-" + base + "-" + hex.EncodeToString(digest[:4])
	if len(account.Login) > 64 {
		account.Login = account.Login[:55] + "-" + hex.EncodeToString(digest[:4])
	}
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO users (id, login, display_name) VALUES ($1, $2, $3)`,
			account.UserID, account.Login, name); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO service_accounts
			(id, user_id, organization_id, name, created_by_user_id) VALUES ($1, $2, $3, $4, $5)`,
			account.ID, account.UserID, orgID, name, actorID); err != nil {
			return err
		}
		return insertAuditLocal(ctx, tx, actorID, "service_account.create", "service_account", account.ID, requestID)
	})
	if err != nil {
		return Account{}, fmt.Errorf("serviceaccount: create: %w", err)
	}
	return account, nil
}

func (s *Service) Disable(ctx context.Context, actorID, accountID uuid.UUID, requestID string) error {
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var userID uuid.UUID
		err := tx.QueryRow(ctx, `UPDATE service_accounts SET disabled_at = COALESCE(disabled_at, clock_timestamp()),
			updated_at = clock_timestamp(), representation_version = representation_version + 1
			WHERE id = $1 RETURNING user_id`, accountID).Scan(&userID)
		if errors.Is(err, pgx.ErrNoRows) {
			return serverauth.ErrNotFound
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE users SET status = 'disabled', updated_at = clock_timestamp(),
			representation_version = representation_version + 1 WHERE id = $1`, userID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at = COALESCE(revoked_at, clock_timestamp())
			WHERE user_id = $1`, userID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE personal_access_tokens SET revoked_at = COALESCE(revoked_at, clock_timestamp())
			WHERE user_id = $1`, userID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE delegated_tokens SET revoked_at = COALESCE(revoked_at, clock_timestamp())
			WHERE user_id = $1`, userID); err != nil {
			return err
		}
		return insertAuditLocal(ctx, tx, actorID, "service_account.disable", "service_account", accountID, requestID)
	})
}

func insertAuditLocal(ctx context.Context, tx pgx.Tx, actorID uuid.UUID, action, resourceType string, resourceID uuid.UUID, requestID string) error {
	_, err := tx.Exec(ctx, `INSERT INTO audit_events
		(id, actor_user_id, actor_identity_key, action, resource_type, resource_id, request_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, uuid.New(), actorID, "user:"+actorID.String(),
		action, resourceType, resourceID, requestID)
	return err
}
