package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var loginUnsafe = regexp.MustCompile(`[^a-z0-9-]+`)

type User struct {
	ID          uuid.UUID `json:"id"`
	Login       string    `json:"login"`
	DisplayName string    `json:"name"`
	Email       *string   `json:"email,omitempty"`
	Status      string    `json:"status"`
}

type Provider struct {
	ID      uuid.UUID
	Name    string
	Kind    string
	Issuer  string
	Enabled bool
	Config  json.RawMessage
}

type ExternalIdentity struct {
	Issuer      string
	Subject     string
	Login       string
	DisplayName string
	Email       *string
	AvatarURL   string
	Claims      json.RawMessage
}

func NormalizeExternalAvatarURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 2048 || strings.ContainsAny(raw, "\\\r\n\t") {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || !parsed.IsAbs() || parsed.Host == "" || parsed.Hostname() == "" ||
		parsed.User != nil || parsed.Opaque != "" || parsed.Fragment != "" {
		return ""
	}
	parsed.Scheme = "https"
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	host := strings.ToLower(parsed.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port := parsed.Port(); port != "" && port != "443" {
		host = net.JoinHostPort(strings.Trim(host, "[]"), port)
	}
	parsed.Host = host
	return parsed.String()
}

type IdentityService struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

func NewIdentityService(pool *pgxpool.Pool) *IdentityService {
	return &IdentityService{pool: pool, now: func() time.Time { return time.Now().UTC() }}
}

func (s *IdentityService) Provider(ctx context.Context, id uuid.UUID) (Provider, error) {
	if s == nil || s.pool == nil || id == uuid.Nil {
		return Provider{}, ErrNotFound
	}
	var p Provider
	err := s.pool.QueryRow(ctx, `SELECT id, name, kind, issuer, enabled, config FROM auth_providers WHERE id = $1`, id).
		Scan(&p.ID, &p.Name, &p.Kind, &p.Issuer, &p.Enabled, &p.Config)
	if errors.Is(err, pgx.ErrNoRows) {
		return Provider{}, ErrNotFound
	}
	if err != nil {
		return Provider{}, fmt.Errorf("auth: load provider: %w", err)
	}
	return p, nil
}

// ResolveOrProvision keys identity exclusively by provider, issuer and subject.
// Email is intentionally display metadata and never participates in merging.
func (s *IdentityService) ResolveOrProvision(ctx context.Context, provider Provider, ext ExternalIdentity) (User, error) {
	if s == nil || s.pool == nil || provider.ID == uuid.Nil || !provider.Enabled ||
		strings.TrimSpace(ext.Issuer) == "" || strings.TrimSpace(ext.Subject) == "" {
		return User{}, ErrInvalidCredential
	}
	if provider.Issuer != ext.Issuer {
		return User{}, ErrInvalidCredential
	}
	if len(ext.Claims) == 0 {
		ext.Claims = json.RawMessage(`{}`)
	}
	identityKey := provider.Kind + ":" + ext.Issuer + ":" + ext.Subject
	ext.AvatarURL = NormalizeExternalAvatarURL(ext.AvatarURL)
	var avatarURL *string
	if ext.AvatarURL != "" {
		avatarURL = &ext.AvatarURL
	}
	var user User
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) error {
		var identityID uuid.UUID
		var profileIdentityID *uuid.UUID
		err := tx.QueryRow(ctx, `SELECT i.id, u.id, u.login, u.display_name, u.email, u.status, u.profile_identity_id
			FROM identities i JOIN users u ON u.id = i.user_id
			WHERE i.provider_id = $1 AND i.issuer = $2 AND i.subject = $3
			FOR UPDATE OF i, u`, provider.ID, ext.Issuer, ext.Subject).
			Scan(&identityID, &user.ID, &user.Login, &user.DisplayName, &user.Email, &user.Status, &profileIdentityID)
		if err == nil {
			if user.Status != "active" {
				return ErrDisabledAccount
			}
			_, err = tx.Exec(ctx, `UPDATE identities SET claims = $2, avatar_url = $3, updated_at = $4,
				representation_version = representation_version + 1 WHERE provider_id = $1 AND issuer = $5 AND subject = $6`,
				provider.ID, ext.Claims, avatarURL, s.now(), ext.Issuer, ext.Subject)
			if err != nil {
				return err
			}
			if profileIdentityID == nil {
				if err := tx.QueryRow(ctx, `UPDATE users SET profile_identity_id = (
					SELECT id FROM identities WHERE user_id = $1 ORDER BY created_at, id LIMIT 1)
					WHERE id = $1 RETURNING profile_identity_id`, user.ID).Scan(&profileIdentityID); err != nil {
					return err
				}
			}
			if profileIdentityID != nil && *profileIdentityID == identityID {
				displayName := strings.TrimSpace(ext.DisplayName)
				if displayName == "" {
					displayName = user.DisplayName
				}
				if err := tx.QueryRow(ctx, `UPDATE users SET display_name=$2, email=$3,
					representation_version=representation_version+1, updated_at=$4 WHERE id=$1
					RETURNING display_name,email`, user.ID, displayName, ext.Email, s.now()).Scan(&user.DisplayName, &user.Email); err != nil {
					return err
				}
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		login := normalizedLogin(ext.Login)
		var loginExists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE login_key = lower($1))`, login).Scan(&loginExists); err != nil {
			return err
		}
		if loginExists {
			login = collisionLogin(login, ext.Issuer, ext.Subject)
		}
		displayName := strings.TrimSpace(ext.DisplayName)
		if displayName == "" {
			displayName = login
		}
		user.ID = uuid.New()
		_, err = tx.Exec(ctx, `INSERT INTO users (id, login, display_name, email) VALUES ($1, $2, $3, $4)`,
			user.ID, login, displayName, ext.Email)
		if err != nil {
			return err
		}
		identityID = uuid.New()
		_, err = tx.Exec(ctx, `INSERT INTO identities
			(id, user_id, provider_id, issuer, subject, identity_key, claims, avatar_url)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, identityID, user.ID, provider.ID,
			ext.Issuer, ext.Subject, identityKey, ext.Claims, avatarURL)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE users SET profile_identity_id=$2 WHERE id=$1`, user.ID, identityID); err != nil {
			return err
		}
		user.Login, user.DisplayName, user.Email, user.Status = login, displayName, ext.Email, "active"
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrDisabledAccount) {
			return User{}, err
		}
		return User{}, fmt.Errorf("auth: resolve identity: %w", err)
	}
	return user, nil
}

// LinkIdentity is an explicit administrative operation. It never infers a
// target from email and refuses to move an identity already linked elsewhere.
func (s *IdentityService) LinkIdentity(ctx context.Context, actorID, targetUserID uuid.UUID, provider Provider, ext ExternalIdentity, requestID string) error {
	if actorID == uuid.Nil || targetUserID == uuid.Nil || strings.TrimSpace(requestID) == "" || provider.ID == uuid.Nil {
		return ErrInvalidCredential
	}
	if provider.Issuer != ext.Issuer || ext.Subject == "" {
		return ErrInvalidCredential
	}
	claims := ext.Claims
	if len(claims) == 0 {
		claims = json.RawMessage(`{}`)
	}
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM users WHERE id = $1 FOR UPDATE`, targetUserID).Scan(&status); err != nil {
			return err
		}
		if status != "active" {
			return ErrDisabledAccount
		}
		identityID := uuid.New()
		avatar := NormalizeExternalAvatarURL(ext.AvatarURL)
		var avatarURL *string
		if avatar != "" {
			avatarURL = &avatar
		}
		_, err := tx.Exec(ctx, `INSERT INTO identities
			(id, user_id, provider_id, issuer, subject, identity_key, claims, avatar_url)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, identityID, targetUserID, provider.ID,
			ext.Issuer, ext.Subject, provider.Kind+":"+ext.Issuer+":"+ext.Subject, claims, avatarURL)
		if err != nil {
			if isUniqueViolation(err) {
				return ErrConflict
			}
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE users SET profile_identity_id=$2 WHERE id=$1 AND profile_identity_id IS NULL`, targetUserID, identityID); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actorID, "user:"+actorID.String(), "identity.link", "identity", identityID, requestID, nil)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func (s *IdentityService) DisableUser(ctx context.Context, actorID, userID uuid.UUID, requestID string) error {
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE users SET status = 'disabled', updated_at = clock_timestamp(),
			representation_version = representation_version + 1 WHERE id = $1 AND status = 'active'`, userID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		if _, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at = COALESCE(revoked_at, clock_timestamp()) WHERE user_id = $1`, userID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE personal_access_tokens SET revoked_at = COALESCE(revoked_at, clock_timestamp()),
			updated_at = clock_timestamp(), representation_version = representation_version + 1 WHERE user_id = $1`, userID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE delegated_tokens SET revoked_at = COALESCE(revoked_at, clock_timestamp()) WHERE user_id = $1`, userID); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actorID, "user:"+actorID.String(), "user.disable", "user", userID, requestID, nil)
	})
}

func normalizedLogin(candidate string) string {
	login := strings.ToLower(strings.TrimSpace(candidate))
	login = loginUnsafe.ReplaceAllString(login, "-")
	login = strings.Trim(login, "-")
	if login == "" {
		login = "user"
	}
	if len(login) > 48 {
		login = strings.Trim(login[:48], "-")
	}
	return login
}

func collisionLogin(base, issuer, subject string) string {
	sum := sha256.Sum256([]byte(issuer + "\x00" + subject))
	suffix := hex.EncodeToString(sum[:5])
	if len(base) > 48 {
		base = base[:48]
	}
	return strings.Trim(base, "-") + "-" + suffix
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func insertAudit(ctx context.Context, tx pgx.Tx, actorID uuid.UUID, actorKey, action, resourceType string, resourceID uuid.UUID, requestID string, metadata json.RawMessage) error {
	if len(metadata) == 0 {
		metadata = json.RawMessage(`{}`)
	}
	_, err := tx.Exec(ctx, `INSERT INTO audit_events
		(id, actor_user_id, actor_identity_key, action, resource_type, resource_id, request_id, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, uuid.New(), nullableUUID(actorID), actorKey,
		action, resourceType, nullableUUID(resourceID), requestID, metadata)
	return err
}

func nullableUUID(id uuid.UUID) any {
	if id == uuid.Nil {
		return nil
	}
	return id
}
