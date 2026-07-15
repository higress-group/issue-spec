// Package pat implements personal access token lifecycle and authentication.
package pat

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var allowedScopes = map[string]struct{}{
	"read:user": {}, "read:org": {}, "repo": {}, "issues:read": {}, "issues:write": {},
	"admin:org": {}, "admin:repo": {}, "evidence:write": {}, "runner:delegate": {},
}

type Token struct {
	ID                    uuid.UUID             `json:"id"`
	Name                  string                `json:"name"`
	Prefix                string                `json:"token_prefix"`
	Scopes                []string              `json:"scopes"`
	Repositories          []RepositorySelection `json:"repositories,omitempty"`
	RepositoryRestricted  bool                  `json:"repository_restricted"`
	ExpiresAt             *time.Time            `json:"expires_at,omitempty"`
	LastUsedAt            *time.Time            `json:"last_used_at,omitempty"`
	RevokedAt             *time.Time            `json:"revoked_at,omitempty"`
	CreatedAt             time.Time             `json:"created_at"`
	RepresentationVersion int64                 `json:"representation_version"`
}

type RepositorySelection struct {
	OrganizationID uuid.UUID `json:"organization_id"`
	RepositoryID   uuid.UUID `json:"repository_id"`
}

type Created struct {
	Token
	Plaintext string `json:"token"`
}

type CreateInput struct {
	Name         string
	Scopes       []string
	Repositories []models.RepoScope
	ExpiresAt    *time.Time
}

type Service struct {
	pool    *pgxpool.Pool
	secrets *serverauth.Secrets
	now     func() time.Time
}

func New(pool *pgxpool.Pool, secrets *serverauth.Secrets) *Service {
	return &Service{pool: pool, secrets: secrets, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) Create(ctx context.Context, userID uuid.UUID, input CreateInput) (Created, error) {
	if userID == uuid.Nil || strings.TrimSpace(input.Name) == "" {
		return Created{}, serverauth.ErrInvalidCredential
	}
	scopes, err := validateScopes(input.Scopes)
	if err != nil {
		return Created{}, err
	}
	now := s.now()
	if input.ExpiresAt != nil && !input.ExpiresAt.After(now) {
		return Created{}, serverauth.ErrExpiredCredential
	}
	if input.Repositories != nil && len(input.Repositories) == 0 {
		return Created{}, serverauth.ErrInvalidCredential
	}
	plaintext, prefix, err := s.secrets.RandomToken("pat")
	if err != nil {
		return Created{}, err
	}
	created := Created{Plaintext: plaintext, Token: Token{ID: uuid.New(), Name: strings.TrimSpace(input.Name),
		Prefix: prefix, Scopes: scopes, Repositories: toSelections(uniqueRepos(input.Repositories)),
		RepositoryRestricted: input.Repositories != nil, ExpiresAt: input.ExpiresAt, CreatedAt: now, RepresentationVersion: 1}}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM users WHERE id = $1 FOR UPDATE`, userID).Scan(&status); err != nil {
			return err
		}
		if status != "active" {
			return serverauth.ErrDisabledAccount
		}
		_, err := tx.Exec(ctx, `INSERT INTO personal_access_tokens
			(id, user_id, name, token_prefix, token_hash, expires_at, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $7)`, created.ID, userID, created.Name,
			created.Prefix, s.secrets.Digest("pat-token", plaintext), input.ExpiresAt, now)
		if err != nil {
			return err
		}
		for _, scope := range scopes {
			if _, err := tx.Exec(ctx, `INSERT INTO pat_scopes (id, personal_access_token_id, scope) VALUES ($1, $2, $3)`,
				uuid.New(), created.ID, scope); err != nil {
				return err
			}
		}
		for _, repo := range created.Repositories {
			if err := (models.RepoScope{OrgID: repo.OrganizationID, RepoID: repo.RepositoryID}).Validate(); err != nil {
				return serverauth.ErrInvalidCredential
			}
			if _, err := tx.Exec(ctx, `INSERT INTO pat_repositories
				(personal_access_token_id, organization_id, repository_id) VALUES ($1, $2, $3)`,
				created.ID, repo.OrganizationID, repo.RepositoryID); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `INSERT INTO audit_events
			(id, actor_user_id, actor_identity_key, action, resource_type, resource_id, request_id)
			VALUES ($1, $2, $3, 'pat.create', 'personal_access_token', $4, $5)`,
			uuid.New(), userID, "user:"+userID.String(), created.ID, "pat:create:"+created.ID.String())
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Created{}, serverauth.ErrNotFound
	}
	if err != nil {
		return Created{}, fmt.Errorf("pat: create: %w", err)
	}
	return created, nil
}

func (s *Service) List(ctx context.Context, userID uuid.UUID) ([]Token, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, token_prefix, expires_at, last_used_at, revoked_at,
		created_at, representation_version FROM personal_access_tokens WHERE user_id = $1 ORDER BY created_at DESC, id`, userID)
	if err != nil {
		return nil, fmt.Errorf("pat: list: %w", err)
	}
	defer rows.Close()
	var result []Token
	for rows.Next() {
		var token Token
		if err := rows.Scan(&token.ID, &token.Name, &token.Prefix, &token.ExpiresAt, &token.LastUsedAt,
			&token.RevokedAt, &token.CreatedAt, &token.RepresentationVersion); err != nil {
			return nil, err
		}
		if err := s.loadCaps(ctx, &token); err != nil {
			return nil, err
		}
		result = append(result, token)
	}
	return result, rows.Err()
}

func (s *Service) Rotate(ctx context.Context, userID, tokenID uuid.UUID) (Created, error) {
	plaintext, prefix, err := s.secrets.RandomToken("pat")
	if err != nil {
		return Created{}, err
	}
	created := Created{Plaintext: plaintext, Token: Token{ID: uuid.New(), Prefix: prefix, CreatedAt: s.now(), RepresentationVersion: 1}}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var revokedAt *time.Time
		var userStatus string
		if err := tx.QueryRow(ctx, `SELECT p.name, p.expires_at, p.revoked_at, u.status
			FROM personal_access_tokens p JOIN users u ON u.id = p.user_id
			WHERE p.id = $1 AND p.user_id = $2 FOR UPDATE OF p, u`, tokenID, userID).
			Scan(&created.Name, &created.ExpiresAt, &revokedAt, &userStatus); err != nil {
			return err
		}
		if userStatus != "active" {
			return serverauth.ErrDisabledAccount
		}
		if revokedAt != nil {
			return serverauth.ErrRevokedCredential
		}
		if created.ExpiresAt != nil && !created.ExpiresAt.After(created.CreatedAt) {
			return serverauth.ErrExpiredCredential
		}
		scopeRows, err := tx.Query(ctx, `SELECT scope FROM pat_scopes WHERE personal_access_token_id = $1 ORDER BY scope`, tokenID)
		if err != nil {
			return err
		}
		for scopeRows.Next() {
			var scope string
			if err := scopeRows.Scan(&scope); err != nil {
				scopeRows.Close()
				return err
			}
			created.Scopes = append(created.Scopes, scope)
		}
		scopeRows.Close()
		if err := scopeRows.Err(); err != nil {
			return err
		}
		repoRows, err := tx.Query(ctx, `SELECT organization_id, repository_id FROM pat_repositories
			WHERE personal_access_token_id = $1 ORDER BY organization_id, repository_id`, tokenID)
		if err != nil {
			return err
		}
		for repoRows.Next() {
			var repo RepositorySelection
			if err := repoRows.Scan(&repo.OrganizationID, &repo.RepositoryID); err != nil {
				repoRows.Close()
				return err
			}
			created.Repositories = append(created.Repositories, repo)
		}
		repoRows.Close()
		if err := repoRows.Err(); err != nil {
			return err
		}
		created.RepositoryRestricted = len(created.Repositories) > 0
		if _, err := tx.Exec(ctx, `INSERT INTO personal_access_tokens
			(id, user_id, name, token_prefix, token_hash, expires_at, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $7)`, created.ID, userID, created.Name,
			created.Prefix, s.secrets.Digest("pat-token", plaintext), created.ExpiresAt, created.CreatedAt); err != nil {
			return err
		}
		for _, scope := range created.Scopes {
			if _, err := tx.Exec(ctx, `INSERT INTO pat_scopes (id, personal_access_token_id, scope) VALUES ($1, $2, $3)`,
				uuid.New(), created.ID, scope); err != nil {
				return err
			}
		}
		for _, repo := range created.Repositories {
			if _, err := tx.Exec(ctx, `INSERT INTO pat_repositories
				(personal_access_token_id, organization_id, repository_id) VALUES ($1, $2, $3)`,
				created.ID, repo.OrganizationID, repo.RepositoryID); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE personal_access_tokens SET revoked_at = $2,
			updated_at = $2, representation_version = representation_version + 1 WHERE id = $1`, tokenID, created.CreatedAt); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE delegated_tokens SET revoked_at = COALESCE(revoked_at, $2)
			WHERE personal_access_token_id = $1`, tokenID, created.CreatedAt); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO audit_events
			(id, actor_user_id, actor_identity_key, action, resource_type, resource_id, request_id, metadata)
			VALUES ($1, $2, $3, 'pat.rotate', 'personal_access_token', $4, $5,
			jsonb_build_object('replaces_token_id', $6::text))`, uuid.New(), userID, "user:"+userID.String(),
			created.ID, "pat:rotate:"+created.ID.String(), tokenID)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Created{}, serverauth.ErrNotFound
	}
	return created, err
}

func (s *Service) Revoke(ctx context.Context, userID, tokenID uuid.UUID) error {
	now := s.now()
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE personal_access_tokens SET revoked_at = COALESCE(revoked_at, $3),
			updated_at = $3, representation_version = representation_version + 1 WHERE id = $1 AND user_id = $2`,
			tokenID, userID, now)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return serverauth.ErrNotFound
		}
		if _, err = tx.Exec(ctx, `UPDATE delegated_tokens SET revoked_at = COALESCE(revoked_at, $2)
			WHERE personal_access_token_id = $1`, tokenID, now); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO audit_events
			(id, actor_user_id, actor_identity_key, action, resource_type, resource_id, request_id)
			VALUES ($1, $2, $3, 'pat.revoke', 'personal_access_token', $4, $5)`,
			uuid.New(), userID, "user:"+userID.String(), tokenID, "pat:revoke:"+tokenID.String())
		return err
	})
}

func (s *Service) AuthenticateBearer(ctx context.Context, plaintext string) (serverauth.Principal, error) {
	prefix, err := serverauth.TokenPrefix(plaintext, "pat")
	if err != nil {
		return serverauth.Principal{}, err
	}
	var p serverauth.Principal
	var digest []byte
	var expires, revoked *time.Time
	err = s.pool.QueryRow(ctx, `SELECT p.id, p.token_hash, p.expires_at, p.revoked_at,
		u.id, u.login, COALESCE(u.nickname, u.display_name), u.email, u.status
		FROM personal_access_tokens p JOIN users u ON u.id = p.user_id WHERE p.token_prefix = $1`, prefix).
		Scan(&p.CredentialID, &digest, &expires, &revoked, &p.User.ID, &p.User.Login,
			&p.User.DisplayName, &p.User.Email, &p.User.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return serverauth.Principal{}, serverauth.ErrInvalidCredential
	}
	if err != nil {
		return serverauth.Principal{}, err
	}
	if !serverauth.EqualDigest(digest, s.secrets.Digest("pat-token", plaintext)) {
		return serverauth.Principal{}, serverauth.ErrInvalidCredential
	}
	if p.User.Status != "active" {
		return serverauth.Principal{}, serverauth.ErrDisabledAccount
	}
	if revoked != nil {
		return serverauth.Principal{}, serverauth.ErrRevokedCredential
	}
	now := s.now()
	if expires != nil {
		p.ExpiresAt = *expires
		if !now.Before(*expires) {
			return serverauth.Principal{}, serverauth.ErrExpiredCredential
		}
	}
	metadata := Token{ID: p.CredentialID}
	if err := s.loadCaps(ctx, &metadata); err != nil {
		return serverauth.Principal{}, err
	}
	p.Kind, p.Scopes, p.RepositoryCaps, p.RepoRestricted = serverauth.CredentialPAT,
		metadata.Scopes, toCaps(metadata.Repositories), metadata.RepositoryRestricted
	_, _ = s.pool.Exec(ctx, `UPDATE personal_access_tokens SET last_used_at = $2 WHERE id = $1`, p.CredentialID, now)
	return p, nil
}

func (s *Service) loadCaps(ctx context.Context, token *Token) error {
	scopeRows, err := s.pool.Query(ctx, `SELECT scope FROM pat_scopes WHERE personal_access_token_id = $1 ORDER BY scope`, token.ID)
	if err != nil {
		return err
	}
	defer scopeRows.Close()
	for scopeRows.Next() {
		var scope string
		if err := scopeRows.Scan(&scope); err != nil {
			return err
		}
		token.Scopes = append(token.Scopes, scope)
	}
	if err := scopeRows.Err(); err != nil {
		return err
	}
	repoRows, err := s.pool.Query(ctx, `SELECT organization_id, repository_id FROM pat_repositories
		WHERE personal_access_token_id = $1 ORDER BY organization_id, repository_id`, token.ID)
	if err != nil {
		return err
	}
	defer repoRows.Close()
	for repoRows.Next() {
		var repo RepositorySelection
		if err := repoRows.Scan(&repo.OrganizationID, &repo.RepositoryID); err != nil {
			return err
		}
		token.Repositories = append(token.Repositories, repo)
	}
	if err := repoRows.Err(); err != nil {
		return err
	}
	var count int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM pat_repositories WHERE personal_access_token_id = $1`, token.ID).Scan(&count); err != nil {
		return err
	}
	token.RepositoryRestricted = count > 0
	return nil
}

func validateScopes(input []string) ([]string, error) {
	if len(input) == 0 {
		return nil, serverauth.ErrInsufficientScope
	}
	seen := make(map[string]struct{}, len(input))
	for _, scope := range input {
		scope = strings.TrimSpace(scope)
		if _, ok := allowedScopes[scope]; !ok {
			return nil, fmt.Errorf("%w: unsupported scope", serverauth.ErrInsufficientScope)
		}
		seen[scope] = struct{}{}
	}
	result := make([]string, 0, len(seen))
	for scope := range seen {
		result = append(result, scope)
	}
	sort.Strings(result)
	return result, nil
}

func uniqueRepos(input []models.RepoScope) []models.RepoScope {
	seen := make(map[models.RepoScope]struct{}, len(input))
	result := make([]models.RepoScope, 0, len(input))
	for _, repo := range input {
		if _, ok := seen[repo]; ok {
			continue
		}
		seen[repo] = struct{}{}
		result = append(result, repo)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].OrgID != result[j].OrgID {
			return result[i].OrgID.String() < result[j].OrgID.String()
		}
		return result[i].RepoID.String() < result[j].RepoID.String()
	})
	return result
}

func toSelections(repos []models.RepoScope) []RepositorySelection {
	result := make([]RepositorySelection, len(repos))
	for i, repo := range repos {
		result[i] = RepositorySelection{OrganizationID: repo.OrgID, RepositoryID: repo.RepoID}
	}
	return result
}

func toCaps(repos []RepositorySelection) []serverauth.RepositoryCap {
	result := make([]serverauth.RepositoryCap, len(repos))
	for i, repo := range repos {
		result[i] = serverauth.RepositoryCap{OrgID: repo.OrganizationID, RepoID: repo.RepositoryID}
	}
	return result
}
