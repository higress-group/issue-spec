package admin

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/jackc/pgx/v5"
)

type CreateRepositoryInput struct {
	Name               string
	DisplayName        string
	Description        string
	Visibility         models.Visibility
	DefaultBranch      string
	ContributionPolicy models.ContributionPolicy
}

type UpdateRepositoryInput struct {
	DisplayName        *string
	Description        *string
	Visibility         *models.Visibility
	DefaultBranch      *string
	ContributionPolicy *models.ContributionPolicy
	ExpectedVersion    int64
}

type UpsertCollaboratorInput struct {
	UserID uuid.UUID
	Role   string
}

func validCollaboratorRole(role string) bool {
	switch role {
	case "admin", "maintain", "write", "triage", "read":
		return true
	default:
		return false
	}
}

func (s *Service) CreateRepository(ctx context.Context, actor Actor, orgID uuid.UUID, input CreateRepositoryInput) (models.AdminRepository, error) {
	input.Name, input.DisplayName = strings.TrimSpace(input.Name), strings.TrimSpace(input.DisplayName)
	input.DefaultBranch = strings.TrimSpace(input.DefaultBranch)
	if err := actor.validate(); err != nil || orgID == uuid.Nil || input.Name == "" || input.DisplayName == "" ||
		input.DefaultBranch == "" || !input.Visibility.Valid() || !input.ContributionPolicy.Valid() {
		return models.AdminRepository{}, ErrInvalidInput
	}
	now := s.now().Truncate(time.Microsecond)
	repository := models.AdminRepository{ID: uuid.New(), OrganizationID: orgID,
		Scope: models.RepoScope{OrgID: orgID}, Name: input.Name, DisplayName: input.DisplayName,
		Description: strings.TrimSpace(input.Description), Visibility: input.Visibility,
		DefaultBranch: input.DefaultBranch, ContributionPolicy: input.ContributionPolicy,
		RepresentationVersion: 1, CollaboratorsCollectionVersion: 1, CreatedAt: now, UpdatedAt: now}
	repository.Scope.RepoID = repository.ID
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if err := requireActiveOrganization(ctx, tx, orgID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO repos
			(id, organization_id, name, display_name, description, visibility, default_branch,
			 contribution_policy, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9)`, repository.ID, orgID,
			repository.Name, repository.DisplayName, repository.Description, repository.Visibility,
			repository.DefaultBranch, repository.ContributionPolicy, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE orgs SET repositories_collection_version = repositories_collection_version + 1,
			updated_at = $2 WHERE id = $1`, orgID, now); err != nil {
			return err
		}
		return audit(ctx, tx, actor, orgID, repository.ID, repository.ID,
			"repository.create", "repository", map[string]any{"name": repository.Name, "visibility": repository.Visibility})
	})
	return repository, mapError(err)
}

func (s *Service) GetRepository(ctx context.Context, scope models.RepoScope, includeArchived bool) (models.AdminRepository, error) {
	if err := scope.Validate(); err != nil {
		return models.AdminRepository{}, ErrInvalidInput
	}
	query := repositorySelect + ` WHERE organization_id = $1 AND id = $2`
	if !includeArchived {
		query += ` AND archived_at IS NULL`
	}
	repository, err := scanRepository(s.pool.QueryRow(ctx, query, scope.OrgID, scope.RepoID))
	if err == nil {
		repository.Scope = scope
	}
	return repository, err
}

func (s *Service) ListRepositories(ctx context.Context, orgID uuid.UUID, includeArchived bool) ([]models.AdminRepository, error) {
	if orgID == uuid.Nil {
		return nil, ErrInvalidInput
	}
	query := repositorySelect + ` WHERE organization_id = $1`
	if !includeArchived {
		query += ` AND archived_at IS NULL`
	}
	query += ` ORDER BY name_key, id`
	rows, err := s.pool.Query(ctx, query, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []models.AdminRepository
	for rows.Next() {
		repository, err := scanRepository(rows)
		if err != nil {
			return nil, err
		}
		repository.Scope = models.RepoScope{OrgID: orgID, RepoID: repository.ID}
		result = append(result, repository)
	}
	return result, rows.Err()
}

func (s *Service) UpdateRepository(ctx context.Context, actor Actor, scope models.RepoScope, input UpdateRepositoryInput) (models.AdminRepository, error) {
	if err := actor.validate(); err != nil || scope.Validate() != nil || input.ExpectedVersion <= 0 {
		return models.AdminRepository{}, ErrInvalidInput
	}
	var result models.AdminRepository
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		current, err := scanRepository(tx.QueryRow(ctx, repositorySelect+` WHERE organization_id = $1 AND id = $2
			AND archived_at IS NULL FOR UPDATE`, scope.OrgID, scope.RepoID))
		if err != nil {
			return err
		}
		if current.RepresentationVersion != input.ExpectedVersion {
			return ErrVersionConflict
		}
		if input.DisplayName != nil {
			current.DisplayName = strings.TrimSpace(*input.DisplayName)
			if current.DisplayName == "" {
				return ErrInvalidInput
			}
		}
		if input.Description != nil {
			current.Description = strings.TrimSpace(*input.Description)
		}
		if input.Visibility != nil {
			if !input.Visibility.Valid() {
				return ErrInvalidInput
			}
			current.Visibility = *input.Visibility
		}
		if input.DefaultBranch != nil {
			current.DefaultBranch = strings.TrimSpace(*input.DefaultBranch)
			if current.DefaultBranch == "" {
				return ErrInvalidInput
			}
		}
		if input.ContributionPolicy != nil {
			if !input.ContributionPolicy.Valid() {
				return ErrInvalidInput
			}
			current.ContributionPolicy = *input.ContributionPolicy
		}
		now := s.now().Truncate(time.Microsecond)
		if err := tx.QueryRow(ctx, `UPDATE repos SET display_name = $3, description = $4,
			visibility = $5, default_branch = $6, contribution_policy = $7,
			representation_version = representation_version + 1, updated_at = $8
			WHERE organization_id = $1 AND id = $2
			RETURNING id, organization_id, name, display_name, description, visibility, default_branch,
			contribution_policy, representation_version, collaborators_collection_version,
			archived_at, created_at, updated_at`, scope.OrgID, scope.RepoID, current.DisplayName,
			current.Description, current.Visibility, current.DefaultBranch, current.ContributionPolicy, now).
			Scan(&result.ID, &result.OrganizationID, &result.Name, &result.DisplayName, &result.Description,
				&result.Visibility, &result.DefaultBranch, &result.ContributionPolicy,
				&result.RepresentationVersion, &result.CollaboratorsCollectionVersion,
				&result.ArchivedAt, &result.CreatedAt, &result.UpdatedAt); err != nil {
			return err
		}
		result.Scope = scope
		return audit(ctx, tx, actor, scope.OrgID, scope.RepoID, scope.RepoID,
			"repository.update", "repository", map[string]any{"visibility": result.Visibility,
				"contribution_policy": result.ContributionPolicy})
	})
	return result, mapError(err)
}

func (s *Service) ArchiveRepository(ctx context.Context, actor Actor, scope models.RepoScope, expectedVersion int64) error {
	if err := actor.validate(); err != nil || scope.Validate() != nil || expectedVersion <= 0 {
		return ErrInvalidInput
	}
	return mapError(pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var version int64
		if err := tx.QueryRow(ctx, `SELECT representation_version FROM repos
			WHERE organization_id = $1 AND id = $2 AND archived_at IS NULL FOR UPDATE`,
			scope.OrgID, scope.RepoID).Scan(&version); err != nil {
			return err
		}
		if version != expectedVersion {
			return ErrVersionConflict
		}
		now := s.now().Truncate(time.Microsecond)
		if _, err := tx.Exec(ctx, `UPDATE repos SET archived_at = $3, updated_at = $3,
			representation_version = representation_version + 1 WHERE organization_id = $1 AND id = $2`,
			scope.OrgID, scope.RepoID, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE orgs SET repositories_collection_version = repositories_collection_version + 1,
			updated_at = $2 WHERE id = $1`, scope.OrgID, now); err != nil {
			return err
		}
		return audit(ctx, tx, actor, scope.OrgID, scope.RepoID, scope.RepoID,
			"repository.archive", "repository", nil)
	}))
}

func (s *Service) UpsertCollaborator(ctx context.Context, actor Actor, scope models.RepoScope, input UpsertCollaboratorInput) (models.AdminCollaborator, error) {
	if err := actor.validate(); err != nil || scope.Validate() != nil || input.UserID == uuid.Nil || !validCollaboratorRole(input.Role) {
		return models.AdminCollaborator{}, ErrInvalidInput
	}
	var result models.AdminCollaborator
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if err := requireActiveRepository(ctx, tx, scope); err != nil {
			return err
		}
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM users WHERE id = $1 FOR UPDATE`, input.UserID).Scan(&status); err != nil {
			return err
		}
		if status != "active" {
			return ErrConflict
		}
		now := s.now().Truncate(time.Microsecond)
		if err := tx.QueryRow(ctx, `INSERT INTO repo_collaborators
			(id, organization_id, repository_id, user_id, role, archived_at, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, NULL, $6, $6)
			ON CONFLICT (organization_id, repository_id, user_id) DO UPDATE SET
			role = EXCLUDED.role, archived_at = NULL, updated_at = EXCLUDED.updated_at,
			representation_version = repo_collaborators.representation_version + 1
			RETURNING id, organization_id, repository_id, user_id, role,
			representation_version, archived_at, created_at, updated_at`, uuid.New(), scope.OrgID,
			scope.RepoID, input.UserID, input.Role, now).
			Scan(&result.ID, &result.OrganizationID, &result.RepositoryID, &result.UserID,
				&result.Role, &result.RepresentationVersion, &result.ArchivedAt,
				&result.CreatedAt, &result.UpdatedAt); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE repos SET collaborators_collection_version = collaborators_collection_version + 1,
			updated_at = $3 WHERE organization_id = $1 AND id = $2`, scope.OrgID, scope.RepoID, now); err != nil {
			return err
		}
		return audit(ctx, tx, actor, scope.OrgID, scope.RepoID, result.ID,
			"collaborator.upsert", "repo_collaborator", map[string]any{"target_user_id": input.UserID, "role": input.Role})
	})
	return result, mapError(err)
}

func (s *Service) ArchiveCollaborator(ctx context.Context, actor Actor, scope models.RepoScope, collaboratorID uuid.UUID, expectedVersion int64) error {
	if err := actor.validate(); err != nil || scope.Validate() != nil || collaboratorID == uuid.Nil || expectedVersion <= 0 {
		return ErrInvalidInput
	}
	return mapError(pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var version int64
		if err := tx.QueryRow(ctx, `SELECT representation_version FROM repo_collaborators
			WHERE organization_id = $1 AND repository_id = $2 AND id = $3 AND archived_at IS NULL FOR UPDATE`,
			scope.OrgID, scope.RepoID, collaboratorID).Scan(&version); err != nil {
			return err
		}
		if version != expectedVersion {
			return ErrVersionConflict
		}
		now := s.now().Truncate(time.Microsecond)
		if _, err := tx.Exec(ctx, `UPDATE repo_collaborators SET archived_at = $4, updated_at = $4,
			representation_version = representation_version + 1
			WHERE organization_id = $1 AND repository_id = $2 AND id = $3`,
			scope.OrgID, scope.RepoID, collaboratorID, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE repos SET collaborators_collection_version = collaborators_collection_version + 1,
			updated_at = $3 WHERE organization_id = $1 AND id = $2`, scope.OrgID, scope.RepoID, now); err != nil {
			return err
		}
		return audit(ctx, tx, actor, scope.OrgID, scope.RepoID, collaboratorID,
			"collaborator.archive", "repo_collaborator", nil)
	}))
}

func (s *Service) ListCollaborators(ctx context.Context, scope models.RepoScope, includeArchived bool) ([]models.AdminCollaborator, error) {
	if err := scope.Validate(); err != nil {
		return nil, ErrInvalidInput
	}
	query := collaboratorSelect + ` WHERE organization_id = $1 AND repository_id = $2`
	if !includeArchived {
		query += ` AND archived_at IS NULL`
	}
	query += ` ORDER BY created_at, id`
	rows, err := s.pool.Query(ctx, query, scope.OrgID, scope.RepoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []models.AdminCollaborator
	for rows.Next() {
		collaborator, err := scanCollaborator(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, collaborator)
	}
	return result, rows.Err()
}

const repositorySelect = `SELECT id, organization_id, name, display_name, description, visibility,
	default_branch, contribution_policy, representation_version, collaborators_collection_version,
	archived_at, created_at, updated_at FROM repos`

const collaboratorSelect = `SELECT id, organization_id, repository_id, user_id, role,
	representation_version, archived_at, created_at, updated_at FROM repo_collaborators`

func scanRepository(row pgx.Row) (models.AdminRepository, error) {
	var repository models.AdminRepository
	err := row.Scan(&repository.ID, &repository.OrganizationID, &repository.Name, &repository.DisplayName,
		&repository.Description, &repository.Visibility, &repository.DefaultBranch,
		&repository.ContributionPolicy, &repository.RepresentationVersion,
		&repository.CollaboratorsCollectionVersion, &repository.ArchivedAt,
		&repository.CreatedAt, &repository.UpdatedAt)
	return repository, mapError(err)
}

func scanCollaborator(row pgx.Row) (models.AdminCollaborator, error) {
	var collaborator models.AdminCollaborator
	err := row.Scan(&collaborator.ID, &collaborator.OrganizationID, &collaborator.RepositoryID,
		&collaborator.UserID, &collaborator.Role, &collaborator.RepresentationVersion,
		&collaborator.ArchivedAt, &collaborator.CreatedAt, &collaborator.UpdatedAt)
	return collaborator, mapError(err)
}

func requireActiveRepository(ctx context.Context, tx pgx.Tx, scope models.RepoScope) error {
	var found uuid.UUID
	err := tx.QueryRow(ctx, `SELECT r.id FROM repos r JOIN orgs o ON o.id = r.organization_id
		WHERE r.organization_id = $1 AND r.id = $2 AND r.archived_at IS NULL AND o.archived_at IS NULL
		FOR UPDATE OF r, o`, scope.OrgID, scope.RepoID).Scan(&found)
	return err
}
