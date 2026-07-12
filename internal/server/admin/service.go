package admin

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/auth/recovery"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Actor struct {
	UserID      uuid.UUID
	IdentityKey string
	RequestID   string
}

func ActorFromPrincipal(principal serverauth.Principal, requestID string) Actor {
	identityKey := "user:" + principal.User.ID.String()
	if principal.Kind == serverauth.CredentialRecovery {
		identityKey = "recovery:" + principal.CredentialID.String()
	}
	return Actor{UserID: principal.User.ID, IdentityKey: identityKey, RequestID: requestID}
}

func (a Actor) validate() error {
	if a.UserID == uuid.Nil || strings.TrimSpace(a.IdentityKey) == "" || strings.TrimSpace(a.RequestID) == "" {
		return ErrInvalidInput
	}
	return nil
}

type Service struct {
	pool            *pgxpool.Pool
	secrets         *serverauth.Secrets
	bootstrapDigest [32]byte
	hasBootstrap    bool
	now             func() time.Time
}

func New(pool *pgxpool.Pool, bootstrapSecret []byte, secrets *serverauth.Secrets) (*Service, error) {
	if pool == nil || secrets == nil {
		return nil, errors.New("admin: database and token secrets are required")
	}
	service := &Service{pool: pool, secrets: secrets, now: func() time.Time { return time.Now().UTC() }}
	if len(bootstrapSecret) > 0 {
		if len(bootstrapSecret) < 32 {
			return nil, errors.New("admin: bootstrap secret must contain at least 32 bytes")
		}
		service.bootstrapDigest = sha256.Sum256(bootstrapSecret)
		service.hasBootstrap = true
	}
	return service, nil
}

func (s *Service) verifyBootstrapSecret(presented string) bool {
	if !s.hasBootstrap || presented == "" {
		return false
	}
	digest := sha256.Sum256([]byte(presented))
	return subtle.ConstantTimeCompare(digest[:], s.bootstrapDigest[:]) == 1
}

func audit(ctx context.Context, tx pgx.Tx, actor Actor, orgID, repoID, resourceID uuid.UUID, action, resourceType string, metadata any) error {
	if err := actor.validate(); err != nil {
		return err
	}
	payload := []byte(`{}`)
	if metadata != nil {
		encoded, err := json.Marshal(metadata)
		if err != nil {
			return fmt.Errorf("admin: encode audit metadata: %w", err)
		}
		payload = encoded
	}
	_, err := tx.Exec(ctx, `INSERT INTO audit_events
		(id, organization_id, repository_id, actor_user_id, actor_identity_key,
		 action, resource_type, resource_id, request_id, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`, uuid.New(), nullableUUID(orgID),
		nullableUUID(repoID), actor.UserID, actor.IdentityKey, action, resourceType,
		nullableUUID(resourceID), actor.RequestID, payload)
	return err
}

func nullableUUID(id uuid.UUID) any {
	if id == uuid.Nil {
		return nil
	}
	return id
}

func mapError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23503", "23505", "23514", "23P01":
			return fmt.Errorf("%w: %s", ErrConflict, pgErr.ConstraintName)
		case "40001", "40P01":
			return ErrConflict
		}
	}
	return err
}

func requireRows(tag pgconn.CommandTag) error {
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func scanUser(row pgx.Row) (serverauth.User, error) {
	var user serverauth.User
	err := row.Scan(&user.ID, &user.Login, &user.DisplayName, &user.Email, &user.Status)
	return user, mapError(err)
}

func (s *Service) RecoverAdmin(ctx context.Context, targetUserID uuid.UUID, operator, reason, requestID string, ttl time.Duration) (recovery.Created, error) {
	if strings.TrimSpace(operator) == "" || strings.TrimSpace(reason) == "" || strings.TrimSpace(requestID) == "" {
		return recovery.Created{}, ErrInvalidInput
	}
	query := `SELECT u.id FROM users u JOIN site_role_assignments sr ON sr.user_id = u.id
		WHERE sr.role = 'site_admin' AND u.status = 'active'`
	args := []any{}
	if targetUserID != uuid.Nil {
		query += ` AND u.id = $1`
		args = append(args, targetUserID)
	}
	query += ` ORDER BY sr.created_at, u.id LIMIT 1 FOR UPDATE OF u, sr`
	var created recovery.Created
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, query, args...).Scan(&targetUserID); errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		var err error
		created, err = recovery.New(s.pool, s.secrets).MintInTx(ctx, tx, targetUserID, operator, reason, requestID, ttl)
		return err
	})
	if err != nil {
		return recovery.Created{}, mapError(err)
	}
	return created, nil
}
