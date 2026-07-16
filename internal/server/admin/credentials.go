package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/jackc/pgx/v5"
)

var serviceAccountUnsafeName = regexp.MustCompile(`[^a-z0-9-]+`)

var managedPATScopes = map[string]struct{}{
	"read:user": {}, "read:org": {}, "repo": {}, "issues:read": {}, "issues:write": {},
	"admin:org": {}, "admin:repo": {}, "runner:delegate": {}, "evidence:write": {},
}

type CreateServiceAccountInput struct{ Name string }

type CreateManagedPATInput struct {
	TargetUserID uuid.UUID
	Name         string
	Scopes       []string
	// RepositoryIDs is empty for a site-wide PAT and non-empty for an explicit repository allowlist.
	RepositoryIDs []uuid.UUID
	ExpiresAt     *time.Time
}

type ManagedPATCreated struct {
	models.AdminPAT
	Plaintext string `json:"token"`
}

func (s *Service) CreateServiceAccount(ctx context.Context, actor Actor, orgID uuid.UUID, input CreateServiceAccountInput) (models.AdminServiceAccount, error) {
	name := strings.TrimSpace(input.Name)
	if err := actor.validate(); err != nil || orgID == uuid.Nil || name == "" {
		return models.AdminServiceAccount{}, ErrInvalidInput
	}
	account := models.AdminServiceAccount{ID: uuid.New(), UserID: uuid.New(), OrganizationID: orgID,
		Name: name, RepresentationVersion: 1}
	base := strings.Trim(serviceAccountUnsafeName.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if base == "" {
		base = "service"
	}
	digest := sha256.Sum256(append(orgID[:], []byte(name)...))
	account.Login = "svc-" + base + "-" + hex.EncodeToString(digest[:4])
	if len(account.Login) > 64 {
		account.Login = account.Login[:55] + "-" + hex.EncodeToString(digest[:4])
	}
	now := s.now().Truncate(time.Microsecond)
	account.CreatedAt, account.UpdatedAt = now, now
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if err := requireActiveOrganization(ctx, tx, orgID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO users (id, login, display_name, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $4)`, account.UserID, account.Login, name, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO service_accounts
			(id, user_id, organization_id, name, created_by_user_id, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $6)`, account.ID, account.UserID,
			orgID, name, actor.UserID, now); err != nil {
			return err
		}
		return audit(ctx, tx, actor, orgID, uuid.Nil, account.ID,
			"service_account.create", "service_account", map[string]any{"user_id": account.UserID})
	})
	return account, mapError(err)
}

func (s *Service) DisableServiceAccount(ctx context.Context, actor Actor, orgID, accountID uuid.UUID) error {
	if err := actor.validate(); err != nil || orgID == uuid.Nil || accountID == uuid.Nil {
		return ErrInvalidInput
	}
	return mapError(pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var userID uuid.UUID
		now := s.now().Truncate(time.Microsecond)
		err := tx.QueryRow(ctx, `UPDATE service_accounts SET disabled_at = COALESCE(disabled_at, $3),
			updated_at = $3, representation_version = representation_version + 1
			WHERE organization_id = $1 AND id = $2 RETURNING user_id`, orgID, accountID, now).Scan(&userID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE users SET status = 'disabled', updated_at = $2,
			representation_version = representation_version + 1 WHERE id = $1`, userID, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at = COALESCE(revoked_at, $2) WHERE user_id = $1`, userID, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE personal_access_tokens SET revoked_at = COALESCE(revoked_at, $2),
			updated_at = $2, representation_version = representation_version + 1 WHERE user_id = $1`, userID, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE delegated_tokens SET revoked_at = COALESCE(revoked_at, $2) WHERE user_id = $1`, userID, now); err != nil {
			return err
		}
		return audit(ctx, tx, actor, orgID, uuid.Nil, accountID,
			"service_account.disable", "service_account", map[string]any{"user_id": userID})
	}))
}

func (s *Service) ListServiceAccounts(ctx context.Context, orgID uuid.UUID, includeDisabled bool) ([]models.AdminServiceAccount, error) {
	query := `SELECT sa.id, sa.user_id, sa.organization_id, sa.name, u.login, sa.disabled_at,
		sa.representation_version, sa.created_at, sa.updated_at
		FROM service_accounts sa JOIN users u ON u.id = sa.user_id WHERE sa.organization_id = $1`
	if !includeDisabled {
		query += ` AND sa.disabled_at IS NULL`
	}
	query += ` ORDER BY sa.name_key, sa.id`
	rows, err := s.pool.Query(ctx, query, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]models.AdminServiceAccount, 0)
	for rows.Next() {
		var account models.AdminServiceAccount
		if err := rows.Scan(&account.ID, &account.UserID, &account.OrganizationID, &account.Name,
			&account.Login, &account.DisabledAt, &account.RepresentationVersion,
			&account.CreatedAt, &account.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, account)
	}
	return result, rows.Err()
}

func (s *Service) CreateManagedPAT(ctx context.Context, actor Actor, orgID uuid.UUID, input CreateManagedPATInput) (ManagedPATCreated, error) {
	input.Name = strings.TrimSpace(input.Name)
	scopes, scopeErr := validateManagedScopes(input.Scopes)
	if actorErr := actor.validate(); actorErr != nil || orgID == uuid.Nil || input.TargetUserID == uuid.Nil || input.Name == "" || scopeErr != nil {
		return ManagedPATCreated{}, ErrInvalidInput
	}
	now := s.now().Truncate(time.Microsecond)
	if input.ExpiresAt != nil && !input.ExpiresAt.After(now) {
		return ManagedPATCreated{}, ErrInvalidInput
	}
	plaintext, prefix, err := s.secrets.RandomToken("pat")
	if err != nil {
		return ManagedPATCreated{}, err
	}
	created := ManagedPATCreated{Plaintext: plaintext, AdminPAT: models.AdminPAT{ID: uuid.New(),
		OrganizationID: orgID, UserID: input.TargetUserID, Name: input.Name, Prefix: prefix,
		Scopes: scopes, ExpiresAt: input.ExpiresAt, RepresentationVersion: 1, CreatedAt: now, UpdatedAt: now}}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if err := requireActiveOrganization(ctx, tx, orgID); err != nil {
			return err
		}
		if err := requireOrgCredentialSubject(ctx, tx, orgID, input.TargetUserID); err != nil {
			return err
		}
		repositories, err := resolveManagedRepositories(ctx, tx, orgID, input.RepositoryIDs)
		if err != nil {
			return err
		}
		created.RepositoryIDs = repositories
		if _, err := tx.Exec(ctx, `INSERT INTO personal_access_tokens
			(id, user_id, name, token_prefix, token_hash, expires_at, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $7)`, created.ID, created.UserID, created.Name,
			created.Prefix, s.secrets.Digest("pat-token", plaintext), created.ExpiresAt, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO managed_personal_access_tokens
			(personal_access_token_id, organization_id, created_by_user_id) VALUES ($1, $2, $3)`,
			created.ID, orgID, actor.UserID); err != nil {
			return err
		}
		if err := insertPATCaps(ctx, tx, created.ID, orgID, scopes, repositories); err != nil {
			return err
		}
		return audit(ctx, tx, actor, orgID, uuid.Nil, created.ID,
			"pat.admin_create", "personal_access_token", map[string]any{"target_user_id": created.UserID,
				"scopes": scopes, "repository_ids": repositories})
	})
	if err != nil {
		return ManagedPATCreated{}, mapError(err)
	}
	return created, nil
}

func (s *Service) RotateManagedPAT(ctx context.Context, actor Actor, orgID, tokenID uuid.UUID) (ManagedPATCreated, error) {
	if err := actor.validate(); err != nil || orgID == uuid.Nil || tokenID == uuid.Nil {
		return ManagedPATCreated{}, ErrInvalidInput
	}
	plaintext, prefix, err := s.secrets.RandomToken("pat")
	if err != nil {
		return ManagedPATCreated{}, err
	}
	now := s.now().Truncate(time.Microsecond)
	created := ManagedPATCreated{Plaintext: plaintext, AdminPAT: models.AdminPAT{ID: uuid.New(),
		OrganizationID: orgID, Prefix: prefix, RepresentationVersion: 1, CreatedAt: now, UpdatedAt: now}}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var revokedAt *time.Time
		if err := tx.QueryRow(ctx, `SELECT p.user_id, p.name, p.expires_at, p.revoked_at
			FROM personal_access_tokens p JOIN managed_personal_access_tokens m
			ON m.personal_access_token_id = p.id
			WHERE m.organization_id = $1 AND p.id = $2 FOR UPDATE OF p`, orgID, tokenID).
			Scan(&created.UserID, &created.Name, &created.ExpiresAt, &revokedAt); err != nil {
			return err
		}
		if revokedAt != nil || (created.ExpiresAt != nil && !created.ExpiresAt.After(now)) {
			return ErrConflict
		}
		scopes, repos, err := loadPATCaps(ctx, tx, tokenID, orgID)
		if err != nil {
			return err
		}
		created.Scopes, created.RepositoryIDs = scopes, repos
		if _, err := tx.Exec(ctx, `INSERT INTO personal_access_tokens
			(id, user_id, name, token_prefix, token_hash, expires_at, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $7)`, created.ID, created.UserID, created.Name,
			created.Prefix, s.secrets.Digest("pat-token", plaintext), created.ExpiresAt, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO managed_personal_access_tokens
			(personal_access_token_id, organization_id, created_by_user_id) VALUES ($1, $2, $3)`,
			created.ID, orgID, actor.UserID); err != nil {
			return err
		}
		if err := insertPATCaps(ctx, tx, created.ID, orgID, scopes, repos); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE personal_access_tokens SET revoked_at = $2,
			updated_at = $2, representation_version = representation_version + 1 WHERE id = $1`, tokenID, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE delegated_tokens SET revoked_at = COALESCE(revoked_at, $2)
			WHERE personal_access_token_id = $1`, tokenID, now); err != nil {
			return err
		}
		return audit(ctx, tx, actor, orgID, uuid.Nil, created.ID,
			"pat.admin_rotate", "personal_access_token", map[string]any{"replaces_token_id": tokenID})
	})
	if err != nil {
		return ManagedPATCreated{}, mapError(err)
	}
	return created, nil
}

func (s *Service) RevokeManagedPAT(ctx context.Context, actor Actor, orgID, tokenID uuid.UUID) error {
	if err := actor.validate(); err != nil || orgID == uuid.Nil || tokenID == uuid.Nil {
		return ErrInvalidInput
	}
	return mapError(pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		now := s.now().Truncate(time.Microsecond)
		tag, err := tx.Exec(ctx, `UPDATE personal_access_tokens p SET revoked_at = COALESCE(p.revoked_at, $3),
			updated_at = $3, representation_version = p.representation_version + 1
			FROM managed_personal_access_tokens m WHERE m.personal_access_token_id = p.id
			AND m.organization_id = $1 AND p.id = $2`, orgID, tokenID, now)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		if _, err := tx.Exec(ctx, `UPDATE delegated_tokens SET revoked_at = COALESCE(revoked_at, $2)
			WHERE personal_access_token_id = $1`, tokenID, now); err != nil {
			return err
		}
		return audit(ctx, tx, actor, orgID, uuid.Nil, tokenID,
			"pat.admin_revoke", "personal_access_token", nil)
	}))
}

func (s *Service) ListManagedPATs(ctx context.Context, orgID, targetUserID uuid.UUID) ([]models.AdminPAT, error) {
	query := `SELECT p.id, m.organization_id, p.user_id, p.name, p.token_prefix, p.expires_at,
		p.last_used_at, p.revoked_at, p.representation_version, p.created_at, p.updated_at
		FROM personal_access_tokens p JOIN managed_personal_access_tokens m
		ON m.personal_access_token_id = p.id WHERE m.organization_id = $1`
	args := []any{orgID}
	if targetUserID != uuid.Nil {
		query += ` AND p.user_id = $2`
		args = append(args, targetUserID)
	}
	query += ` ORDER BY p.created_at DESC, p.id`
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []models.AdminPAT
	for rows.Next() {
		var token models.AdminPAT
		if err := rows.Scan(&token.ID, &token.OrganizationID, &token.UserID, &token.Name, &token.Prefix,
			&token.ExpiresAt, &token.LastUsedAt, &token.RevokedAt, &token.RepresentationVersion,
			&token.CreatedAt, &token.UpdatedAt); err != nil {
			return nil, err
		}
		token.Scopes, token.RepositoryIDs, err = loadPATCaps(ctx, s.pool, token.ID, orgID)
		if err != nil {
			return nil, err
		}
		result = append(result, token)
	}
	return result, rows.Err()
}

func validateManagedScopes(input []string) ([]string, error) {
	if len(input) == 0 {
		return nil, ErrInvalidInput
	}
	seen := map[string]struct{}{}
	for _, scope := range input {
		scope = strings.TrimSpace(scope)
		if _, ok := managedPATScopes[scope]; !ok {
			return nil, ErrInvalidInput
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

func requireOrgCredentialSubject(ctx context.Context, tx pgx.Tx, orgID, userID uuid.UUID) error {
	var allowed bool
	err := tx.QueryRow(ctx, `SELECT u.status = 'active' AND (
		EXISTS (SELECT 1 FROM org_memberships om WHERE om.organization_id = $1 AND om.user_id = u.id
			AND om.state = 'active' AND om.archived_at IS NULL)
		OR EXISTS (SELECT 1 FROM service_accounts sa WHERE sa.organization_id = $1 AND sa.user_id = u.id
			AND sa.disabled_at IS NULL)) FROM users u WHERE u.id = $2 FOR UPDATE`, orgID, userID).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrForbidden
	}
	return nil
}

func resolveManagedRepositories(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, input []uuid.UUID) ([]uuid.UUID, error) {
	// An empty selection is the explicit site-wide mode. The PAT remains capped
	// by the subject identity's live organization and repository permissions.
	if len(input) == 0 {
		return []uuid.UUID{}, nil
	}
	seen := map[uuid.UUID]struct{}{}
	for _, id := range input {
		if id == uuid.Nil {
			return nil, ErrInvalidInput
		}
		seen[id] = struct{}{}
	}
	result := make([]uuid.UUID, 0, len(seen))
	for id := range seen {
		var found uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT id FROM repos WHERE organization_id = $1 AND id = $2
			AND archived_at IS NULL`, orgID, id).Scan(&found); err != nil {
			return nil, err
		}
		result = append(result, found)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].String() < result[j].String() })
	return result, nil
}

func insertPATCaps(ctx context.Context, tx pgx.Tx, tokenID, orgID uuid.UUID, scopes []string, repos []uuid.UUID) error {
	for _, scope := range scopes {
		if _, err := tx.Exec(ctx, `INSERT INTO pat_scopes (id, personal_access_token_id, scope) VALUES ($1, $2, $3)`,
			uuid.New(), tokenID, scope); err != nil {
			return err
		}
	}
	for _, repoID := range repos {
		if _, err := tx.Exec(ctx, `INSERT INTO pat_repositories
			(personal_access_token_id, organization_id, repository_id) VALUES ($1, $2, $3)`,
			tokenID, orgID, repoID); err != nil {
			return err
		}
	}
	return nil
}

func loadPATCaps(ctx context.Context, db interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, tokenID, orgID uuid.UUID) ([]string, []uuid.UUID, error) {
	scopeRows, err := db.Query(ctx, `SELECT scope FROM pat_scopes WHERE personal_access_token_id = $1 ORDER BY scope`, tokenID)
	if err != nil {
		return nil, nil, err
	}
	var scopes []string
	for scopeRows.Next() {
		var scope string
		if err := scopeRows.Scan(&scope); err != nil {
			scopeRows.Close()
			return nil, nil, err
		}
		scopes = append(scopes, scope)
	}
	scopeRows.Close()
	if err := scopeRows.Err(); err != nil {
		return nil, nil, err
	}
	repoRows, err := db.Query(ctx, `SELECT repository_id FROM pat_repositories
		WHERE personal_access_token_id = $1 AND organization_id = $2 ORDER BY repository_id`, tokenID, orgID)
	if err != nil {
		return nil, nil, err
	}
	defer repoRows.Close()
	var repos []uuid.UUID
	for repoRows.Next() {
		var id uuid.UUID
		if err := repoRows.Scan(&id); err != nil {
			return nil, nil, err
		}
		repos = append(repos, id)
	}
	return scopes, repos, repoRows.Err()
}
