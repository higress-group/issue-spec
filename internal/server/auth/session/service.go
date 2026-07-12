// Package session implements opaque, hashed browser sessions.
package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const DefaultCookieName = "issue_spec_session"
const DefaultCSRFCookieName = "issue_spec_csrf"

type Config struct {
	CookieName   string
	CookieDomain string
	Secure       bool
	IdleTTL      time.Duration
	AbsoluteTTL  time.Duration
}

type Created struct {
	Principal serverauth.Principal
	Token     string
	CSRFToken string
}

type Service struct {
	pool    *pgxpool.Pool
	secrets *serverauth.Secrets
	config  Config
	now     func() time.Time
}

func New(pool *pgxpool.Pool, secrets *serverauth.Secrets, cfg Config) (*Service, error) {
	if pool == nil || secrets == nil {
		return nil, errors.New("session: database and secrets are required")
	}
	if cfg.CookieName == "" {
		cfg.CookieName = DefaultCookieName
	}
	if cfg.IdleTTL == 0 {
		cfg.IdleTTL = 12 * time.Hour
	}
	if cfg.AbsoluteTTL == 0 {
		cfg.AbsoluteTTL = 7 * 24 * time.Hour
	}
	if cfg.IdleTTL <= 0 || cfg.AbsoluteTTL < cfg.IdleTTL {
		return nil, errors.New("session: invalid idle/absolute lifetime")
	}
	return &Service{pool: pool, secrets: secrets, config: cfg, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (s *Service) Create(ctx context.Context, userID uuid.UUID, userAgent, remoteAddress string) (Created, error) {
	var created Created
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var err error
		created, err = s.create(ctx, tx, userID, userAgent, remoteAddress, time.Time{})
		return err
	})
	return created, err
}

// CreateInTx lets a higher-level credential exchange atomically consume its
// one-time credential and establish the browser session. The caller owns the
// transaction and its commit/rollback decision.
func (s *Service) CreateInTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, userAgent, remoteAddress string) (Created, error) {
	if s == nil || tx == nil {
		return Created{}, serverauth.ErrInvalidCredential
	}
	return s.create(ctx, tx, userID, userAgent, remoteAddress, time.Time{})
}

// Replace atomically revokes a valid presented browser session and creates the
// post-login session. If creation fails, the prior session remains usable.
// Malformed or unknown cookies are ignored to prevent fixation without turning
// login into an availability oracle.
func (s *Service) Replace(ctx context.Context, priorToken string, userID uuid.UUID, userAgent, remoteAddress string) (Created, error) {
	var created Created
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if prefix, prefixErr := serverauth.TokenPrefix(priorToken, "sess"); prefixErr == nil {
			var priorID uuid.UUID
			var digest []byte
			err := tx.QueryRow(ctx, `SELECT id, token_hash FROM sessions
				WHERE token_prefix = $1 AND revoked_at IS NULL FOR UPDATE`, prefix).Scan(&priorID, &digest)
			switch {
			case errors.Is(err, pgx.ErrNoRows):
			case err != nil:
				return err
			case serverauth.EqualDigest(digest, s.secrets.Digest("session-token", priorToken)):
				if _, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at = $2 WHERE id = $1 AND revoked_at IS NULL`, priorID, s.now()); err != nil {
					return err
				}
			}
		}
		var err error
		created, err = s.create(ctx, tx, userID, userAgent, remoteAddress, time.Time{})
		return err
	})
	return created, err
}

type queryExecer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (s *Service) create(ctx context.Context, db queryExecer, userID uuid.UUID, userAgent, remoteAddress string, absoluteExpiry time.Time) (Created, error) {
	token, prefix, err := s.secrets.RandomToken("sess")
	if err != nil {
		return Created{}, err
	}
	csrf, _, err := s.secrets.RandomToken("csrf")
	if err != nil {
		return Created{}, err
	}
	now := s.now().Truncate(time.Microsecond)
	if absoluteExpiry.IsZero() {
		absoluteExpiry = now.Add(s.AbsoluteTTL())
	}
	idleExpiry := now.Add(s.config.IdleTTL)
	if idleExpiry.After(absoluteExpiry) {
		idleExpiry = absoluteExpiry
	}
	if !idleExpiry.After(now) {
		return Created{}, serverauth.ErrExpiredCredential
	}
	created := Created{Token: token, CSRFToken: csrf, Principal: serverauth.Principal{
		Kind: serverauth.CredentialSession, CredentialID: uuid.New(),
		CSRFHash: s.secrets.Digest("session-csrf", csrf), IdleExpiresAt: idleExpiry, ExpiresAt: absoluteExpiry,
	}}
	var userAgentValue, remoteAddressValue any
	if strings.TrimSpace(userAgent) != "" {
		userAgentValue = userAgent
	}
	if strings.TrimSpace(remoteAddress) != "" {
		remoteAddressValue = remoteAddress
	}
	err = db.QueryRow(ctx, `INSERT INTO sessions
		(id, user_id, token_prefix, token_hash, csrf_hash, user_agent, remote_address,
		 idle_expires_at, absolute_expires_at, created_at, last_seen_at)
		SELECT $1, u.id, $3, $4, $5, $6, $7, $8, $9, $10, $10 FROM users u
		WHERE u.id = $2 AND u.status = 'active'
		RETURNING user_id`, created.Principal.CredentialID, userID, prefix,
		s.secrets.Digest("session-token", token), created.Principal.CSRFHash,
		userAgentValue, remoteAddressValue, idleExpiry, created.Principal.ExpiresAt, now).
		Scan(&created.Principal.User.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Created{}, serverauth.ErrDisabledAccount
	}
	if err != nil {
		return Created{}, fmt.Errorf("session: create: %w", err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO audit_events
		(id, actor_user_id, actor_identity_key, action, resource_type, resource_id, request_id)
		VALUES ($1, $2, $3, 'session.create', 'session', $4, $5)`, uuid.New(), userID,
		"user:"+userID.String(), created.Principal.CredentialID, "session:create:"+created.Principal.CredentialID.String()); err != nil {
		return Created{}, fmt.Errorf("session: audit create: %w", err)
	}
	return created, nil
}

func (s *Service) Authenticate(ctx context.Context, token string) (serverauth.Principal, error) {
	prefix, err := serverauth.TokenPrefix(token, "sess")
	if err != nil {
		return serverauth.Principal{}, err
	}
	now := s.now()
	var principal serverauth.Principal
	var digest []byte
	var idleExpires time.Time
	err = s.pool.QueryRow(ctx, `SELECT s.id, s.token_hash, s.csrf_hash, s.absolute_expires_at,
		s.idle_expires_at, u.id, u.login, u.display_name, u.email, u.status
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_prefix = $1 AND s.revoked_at IS NULL`, prefix).
		Scan(&principal.CredentialID, &digest, &principal.CSRFHash, &principal.ExpiresAt,
			&idleExpires, &principal.User.ID, &principal.User.Login, &principal.User.DisplayName,
			&principal.User.Email, &principal.User.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return serverauth.Principal{}, serverauth.ErrInvalidCredential
	}
	if err != nil {
		return serverauth.Principal{}, fmt.Errorf("session: authenticate: %w", err)
	}
	if !serverauth.EqualDigest(digest, s.secrets.Digest("session-token", token)) {
		return serverauth.Principal{}, serverauth.ErrInvalidCredential
	}
	if principal.User.Status != "active" {
		return serverauth.Principal{}, serverauth.ErrDisabledAccount
	}
	if !now.Before(idleExpires) || !now.Before(principal.ExpiresAt) {
		_, _ = s.pool.Exec(ctx, `UPDATE sessions SET revoked_at = COALESCE(revoked_at, $2) WHERE id = $1`, principal.CredentialID, now)
		return serverauth.Principal{}, serverauth.ErrExpiredCredential
	}
	principal.Kind = serverauth.CredentialSession
	newIdle := now.Add(s.config.IdleTTL)
	if newIdle.After(principal.ExpiresAt) {
		newIdle = principal.ExpiresAt
	}
	if idleExpires.Sub(now) < s.config.IdleTTL/2 {
		_, _ = s.pool.Exec(ctx, `UPDATE sessions SET last_seen_at = $2, idle_expires_at = $3
			WHERE id = $1 AND revoked_at IS NULL`, principal.CredentialID, now, newIdle)
		idleExpires = newIdle
	}
	principal.IdleExpiresAt = idleExpires
	return principal, nil
}

func (s *Service) ValidateCSRF(principal serverauth.Principal, csrf string) error {
	if principal.Kind != serverauth.CredentialSession || csrf == "" ||
		!serverauth.EqualDigest(principal.CSRFHash, s.secrets.Digest("session-csrf", csrf)) {
		return serverauth.ErrInvalidCSRF
	}
	return nil
}

func (s *Service) Rotate(ctx context.Context, oldToken, userAgent, remoteAddress string) (Created, error) {
	principal, err := s.Authenticate(ctx, oldToken)
	if err != nil {
		return Created{}, err
	}
	var created Created
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at = $2 WHERE id = $1 AND revoked_at IS NULL`, principal.CredentialID, s.now())
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return serverauth.ErrRevokedCredential
		}
		created, err = s.create(ctx, tx, principal.User.ID, userAgent, remoteAddress, principal.ExpiresAt)
		if err != nil {
			return err
		}
		metadata, _ := json.Marshal(map[string]string{"replaces_session_id": principal.CredentialID.String()})
		_, err = tx.Exec(ctx, `INSERT INTO audit_events
			(id, actor_user_id, actor_identity_key, action, resource_type, resource_id, request_id, metadata)
			VALUES ($1, $2, $3, 'session.rotate', 'session', $4, $5, $6)`, uuid.New(),
			principal.User.ID, "user:"+principal.User.ID.String(), created.Principal.CredentialID,
			"session:rotate:"+created.Principal.CredentialID.String(), metadata)
		return err
	})
	return created, err
}

func (s *Service) Revoke(ctx context.Context, token string) error {
	prefix, err := serverauth.TokenPrefix(token, "sess")
	if err != nil {
		return err
	}
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var id, userID uuid.UUID
		var digest []byte
		if err := tx.QueryRow(ctx, `SELECT id, user_id, token_hash FROM sessions WHERE token_prefix = $1 FOR UPDATE`, prefix).
			Scan(&id, &userID, &digest); errors.Is(err, pgx.ErrNoRows) {
			return serverauth.ErrInvalidCredential
		} else if err != nil {
			return fmt.Errorf("session: load for revoke: %w", err)
		}
		if !serverauth.EqualDigest(digest, s.secrets.Digest("session-token", token)) {
			return serverauth.ErrInvalidCredential
		}
		if _, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at = COALESCE(revoked_at, $2) WHERE id = $1`, id, s.now()); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO audit_events
			(id, actor_user_id, actor_identity_key, action, resource_type, resource_id, request_id)
			VALUES ($1, $2, $3, 'session.revoke', 'session', $4, $5)`, uuid.New(), userID,
			"user:"+userID.String(), id, "session:revoke:"+id.String())
		return err
	})
}

func (s *Service) RevokeUser(ctx context.Context, userID uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `UPDATE sessions SET revoked_at = COALESCE(revoked_at, $2) WHERE user_id = $1`, userID, s.now())
	return err
}

func (s *Service) Cookie(token string) *http.Cookie {
	return &http.Cookie{Name: s.config.CookieName, Value: token, Path: "/", Domain: s.config.CookieDomain,
		Secure: s.config.Secure, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: int(s.config.AbsoluteTTL.Seconds())}
}

func (s *Service) ClearCookie() *http.Cookie {
	cookie := s.Cookie("")
	cookie.MaxAge = -1
	cookie.Expires = time.Unix(1, 0)
	return cookie
}

func (s *Service) CSRFCookie(token string) *http.Cookie {
	return &http.Cookie{Name: DefaultCSRFCookieName, Value: token, Path: "/", Domain: s.config.CookieDomain,
		Secure: s.config.Secure, HttpOnly: false, SameSite: http.SameSiteStrictMode,
		MaxAge: int(s.config.AbsoluteTTL.Seconds())}
}

func (s *Service) ClearCSRFCookie() *http.Cookie {
	cookie := s.CSRFCookie("")
	cookie.MaxAge = -1
	cookie.Expires = time.Unix(1, 0)
	return cookie
}

func (s *Service) AbsoluteTTL() time.Duration { return s.config.AbsoluteTTL }
func (s *Service) CookieName() string         { return s.config.CookieName }
