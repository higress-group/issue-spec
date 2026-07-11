// Package delegation issues short-lived, revocable runner child credentials.
package delegation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const MaxTTL = 15 * time.Minute

type IssueInput struct {
	Issuer    serverauth.Principal
	Repo      models.RepoScope
	JobID     string
	Purpose   string
	Audience  string
	Subject   string
	Scopes    []string
	TTL       time.Duration
	RequestID string
}

type Created struct {
	ID        uuid.UUID `json:"id"`
	Plaintext string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

type Expected struct {
	Repo     models.RepoScope
	JobID    string
	Purpose  string
	Audience string
}

type Service struct {
	pool    *pgxpool.Pool
	secrets *serverauth.Secrets
	now     func() time.Time
}

func New(pool *pgxpool.Pool, secrets *serverauth.Secrets) *Service {
	return &Service{pool: pool, secrets: secrets, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) Issue(ctx context.Context, input IssueInput) (Created, error) {
	if err := input.Repo.Validate(); err != nil || input.Issuer.User.ID == uuid.Nil ||
		strings.TrimSpace(input.JobID) == "" || strings.TrimSpace(input.Purpose) == "" ||
		strings.TrimSpace(input.Audience) == "" || strings.TrimSpace(input.Subject) == "" {
		return Created{}, serverauth.ErrInvalidCredential
	}
	if !input.Issuer.HasScope("runner:delegate") || !input.Issuer.AllowsRepository(input.Repo.OrgID, input.Repo.RepoID) {
		return Created{}, serverauth.ErrInsufficientScope
	}
	if input.TTL <= 0 || input.TTL > MaxTTL {
		return Created{}, serverauth.ErrExpiredCredential
	}
	allowedScopes := make([]string, 0, len(input.Scopes))
	for _, scope := range input.Scopes {
		if !input.Issuer.HasScope(scope) || scope == "runner:delegate" || strings.TrimSpace(scope) == "" {
			return Created{}, serverauth.ErrInsufficientScope
		}
		allowedScopes = append(allowedScopes, scope)
	}
	claims, _ := json.Marshal(map[string]any{"scopes": allowedScopes})
	plaintext, _, err := s.secrets.RandomToken("dgt")
	if err != nil {
		return Created{}, err
	}
	now := s.now()
	created := Created{ID: uuid.New(), Plaintext: plaintext, ExpiresAt: now.Add(input.TTL)}
	var patID any
	if input.Issuer.Kind == serverauth.CredentialPAT {
		patID = input.Issuer.CredentialID
	}
	requestID := strings.TrimSpace(input.RequestID)
	if requestID == "" {
		requestID = "delegated:issue:" + created.ID.String()
	}
	metadata, _ := json.Marshal(map[string]string{"job_id": input.JobID, "purpose": input.Purpose, "audience": input.Audience})
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO delegated_tokens
			(id, user_id, personal_access_token_id, organization_id, repository_id, job_id,
			 purpose, token_hash, audience, subject, claims, expires_at, created_at)
			SELECT $1, u.id, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13
			FROM users u WHERE u.id = $2 AND u.status = 'active' RETURNING id`, created.ID, input.Issuer.User.ID,
			patID, input.Repo.OrgID, input.Repo.RepoID, input.JobID, input.Purpose,
			s.secrets.Digest("delegated-token", plaintext), input.Audience, input.Subject, claims, created.ExpiresAt, now).Scan(&created.ID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO audit_events
			(id, organization_id, repository_id, actor_user_id, actor_identity_key,
			 action, resource_type, resource_id, request_id, metadata)
			VALUES ($1, $2, $3, $4, $5, 'delegated_token.issue', 'delegated_token', $6, $7, $8)`,
			uuid.New(), input.Repo.OrgID, input.Repo.RepoID, input.Issuer.User.ID,
			"user:"+input.Issuer.User.ID.String(), created.ID, requestID, metadata)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Created{}, serverauth.ErrDisabledAccount
	}
	if err != nil {
		return Created{}, fmt.Errorf("delegation: issue: %w", err)
	}
	return created, nil
}

func (s *Service) Authenticate(ctx context.Context, plaintext string, expected Expected) (serverauth.Principal, error) {
	if _, err := serverauth.TokenPrefix(plaintext, "dgt"); err != nil {
		return serverauth.Principal{}, err
	}
	var principal serverauth.Principal
	var digest, claims []byte
	var revoked *time.Time
	var patRevoked *time.Time
	var patExpires *time.Time
	err := s.pool.QueryRow(ctx, `SELECT d.id, d.token_hash, d.organization_id, d.repository_id,
		d.job_id, d.purpose, d.audience, d.claims, d.expires_at, d.revoked_at,
		u.id, u.login, u.display_name, u.email, u.status, p.revoked_at, p.expires_at
		FROM delegated_tokens d JOIN users u ON u.id = d.user_id
		LEFT JOIN personal_access_tokens p ON p.id = d.personal_access_token_id
		WHERE d.token_hash = $1`, s.secrets.Digest("delegated-token", plaintext)).
		Scan(&principal.CredentialID, &digest, &principal.OrgID, &principal.RepoID,
			&principal.JobID, &principal.Purpose, &principal.Audience, &claims, &principal.ExpiresAt, &revoked,
			&principal.User.ID, &principal.User.Login, &principal.User.DisplayName, &principal.User.Email,
			&principal.User.Status, &patRevoked, &patExpires)
	if errors.Is(err, pgx.ErrNoRows) {
		return serverauth.Principal{}, serverauth.ErrInvalidCredential
	}
	if err != nil {
		return serverauth.Principal{}, err
	}
	if !serverauth.EqualDigest(digest, s.secrets.Digest("delegated-token", plaintext)) {
		return serverauth.Principal{}, serverauth.ErrInvalidCredential
	}
	if principal.User.Status != "active" {
		return serverauth.Principal{}, serverauth.ErrDisabledAccount
	}
	if revoked != nil || patRevoked != nil {
		return serverauth.Principal{}, serverauth.ErrRevokedCredential
	}
	now := s.now()
	if !now.Before(principal.ExpiresAt) || (patExpires != nil && !now.Before(*patExpires)) {
		return serverauth.Principal{}, serverauth.ErrExpiredCredential
	}
	if (expected.Repo.OrgID != uuid.Nil && (expected.Repo.OrgID != principal.OrgID || expected.Repo.RepoID != principal.RepoID)) ||
		(expected.JobID != "" && expected.JobID != principal.JobID) ||
		(expected.Purpose != "" && expected.Purpose != principal.Purpose) ||
		(expected.Audience != "" && expected.Audience != principal.Audience) {
		return serverauth.Principal{}, serverauth.ErrInsufficientScope
	}
	var payload struct {
		Scopes []string `json:"scopes"`
	}
	if err := json.Unmarshal(claims, &payload); err != nil {
		return serverauth.Principal{}, serverauth.ErrInvalidCredential
	}
	principal.Kind = serverauth.CredentialDelegated
	principal.Scopes = payload.Scopes
	principal.RepoRestricted = true
	principal.RepositoryCaps = []serverauth.RepositoryCap{{OrgID: principal.OrgID, RepoID: principal.RepoID}}
	_, _ = s.pool.Exec(ctx, `UPDATE delegated_tokens SET used_at = $2 WHERE id = $1 AND revoked_at IS NULL`, principal.CredentialID, now)
	return principal, nil
}

func (s *Service) AuthenticateBearer(ctx context.Context, plaintext string) (serverauth.Principal, error) {
	return s.Authenticate(ctx, plaintext, Expected{})
}

func (s *Service) Revoke(ctx context.Context, tokenID uuid.UUID) error {
	now := s.now()
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var orgID, repoID uuid.UUID
		var jobID string
		err := tx.QueryRow(ctx, `UPDATE delegated_tokens SET revoked_at = COALESCE(revoked_at, $2)
			WHERE id = $1 RETURNING organization_id, repository_id, job_id`, tokenID, now).
			Scan(&orgID, &repoID, &jobID)
		if errors.Is(err, pgx.ErrNoRows) {
			return serverauth.ErrNotFound
		}
		if err != nil {
			return err
		}
		metadata, _ := json.Marshal(map[string]string{"job_id": jobID})
		_, err = tx.Exec(ctx, `INSERT INTO audit_events
			(id, organization_id, repository_id, actor_identity_key, action, resource_type,
			 resource_id, request_id, metadata)
			VALUES ($1, $2, $3, 'system:credential-broker', 'delegated_token.revoke',
			'delegated_token', $4, $5, $6)`, uuid.New(), orgID, repoID, tokenID,
			"delegated:revoke:"+tokenID.String(), metadata)
		return err
	})
}

func (s *Service) RevokeJob(ctx context.Context, repo models.RepoScope, jobID string) error {
	if err := repo.Validate(); err != nil || strings.TrimSpace(jobID) == "" {
		return serverauth.ErrInvalidCredential
	}
	now := s.now()
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE delegated_tokens SET revoked_at = COALESCE(revoked_at, $4)
			WHERE organization_id = $1 AND repository_id = $2 AND job_id = $3`, repo.OrgID, repo.RepoID, jobID, now); err != nil {
			return err
		}
		metadata, _ := json.Marshal(map[string]string{"job_id": jobID})
		_, err := tx.Exec(ctx, `INSERT INTO audit_events
			(id, organization_id, repository_id, actor_identity_key, action, resource_type,
			 request_id, metadata) VALUES ($1, $2, $3, 'system:credential-broker',
			'delegated_token.revoke_job', 'runner_job', $4, $5)`, uuid.New(), repo.OrgID, repo.RepoID,
			"delegated:revoke-job:"+jobID, metadata)
		return err
	})
}
