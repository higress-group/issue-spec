package admin

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/jackc/pgx/v5"
)

type CreateOrganizationInput struct {
	Name           string
	DisplayName    string
	Description    string
	BasePermission models.BasePermission
}

type UpdateOrganizationInput struct {
	DisplayName     *string
	Description     *string
	BasePermission  *models.BasePermission
	ExpectedVersion int64
}

type InviteMembershipInput struct {
	UserID uuid.UUID
	Role   string
}

type UpdateMembershipInput struct {
	Role            string
	State           models.MembershipState
	ExpectedVersion int64
}

func validMembershipRole(role string) bool {
	switch role {
	case "owner", "maintainer", "member", "reader":
		return true
	default:
		return false
	}
}

func (s *Service) CreateOrganization(ctx context.Context, actor Actor, input CreateOrganizationInput) (models.AdminOrganization, error) {
	input.Name, input.DisplayName = strings.TrimSpace(input.Name), strings.TrimSpace(input.DisplayName)
	if err := actor.validate(); err != nil || input.Name == "" || input.DisplayName == "" || !input.BasePermission.Valid() {
		return models.AdminOrganization{}, ErrInvalidInput
	}
	now := s.now().Truncate(time.Microsecond)
	organization := models.AdminOrganization{ID: uuid.New(), Name: input.Name, DisplayName: input.DisplayName,
		Description: strings.TrimSpace(input.Description), BasePermission: input.BasePermission,
		RepresentationVersion: 1, RepositoriesCollectionVersion: 1, MembersCollectionVersion: 1,
		CreatedAt: now, UpdatedAt: now}
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM users WHERE id = $1 FOR UPDATE`, actor.UserID).Scan(&status); err != nil {
			return err
		}
		if status != "active" {
			return ErrForbidden
		}
		if _, err := tx.Exec(ctx, `INSERT INTO orgs
			(id, name, display_name, description, base_permission, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $6)`, organization.ID, organization.Name,
			organization.DisplayName, organization.Description, organization.BasePermission, now); err != nil {
			return err
		}
		membershipID := uuid.New()
		if _, err := tx.Exec(ctx, `INSERT INTO org_memberships
			(id, organization_id, user_id, role, state, invited_by_user_id, invited_at, activated_at, created_at, updated_at)
			VALUES ($1, $2, $3, 'owner', 'active', $3, $4, $4, $4, $4)`, membershipID,
			organization.ID, actor.UserID, now); err != nil {
			return err
		}
		if err := audit(ctx, tx, actor, organization.ID, uuid.Nil, organization.ID,
			"organization.create", "organization", map[string]any{"name": organization.Name}); err != nil {
			return err
		}
		return audit(ctx, tx, actor, organization.ID, uuid.Nil, membershipID,
			"membership.activate", "org_membership", map[string]any{"role": "owner", "bootstrap_owner": true})
	})
	if err != nil {
		return models.AdminOrganization{}, mapError(err)
	}
	return organization, nil
}

func (s *Service) GetOrganization(ctx context.Context, id uuid.UUID, includeArchived bool) (models.AdminOrganization, error) {
	query := `SELECT id, name, display_name, description, base_permission, representation_version,
		repositories_collection_version, members_collection_version, archived_at, created_at, updated_at
		FROM orgs WHERE id = $1`
	if !includeArchived {
		query += ` AND archived_at IS NULL`
	}
	return scanOrganization(s.pool.QueryRow(ctx, query, id))
}

func (s *Service) ListOrganizations(ctx context.Context, includeArchived bool) ([]models.AdminOrganization, error) {
	query := `SELECT id, name, display_name, description, base_permission, representation_version,
		repositories_collection_version, members_collection_version, archived_at, created_at, updated_at FROM orgs`
	if !includeArchived {
		query += ` WHERE archived_at IS NULL`
	}
	query += ` ORDER BY name_key, id`
	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []models.AdminOrganization
	for rows.Next() {
		organization, err := scanOrganization(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, organization)
	}
	return result, rows.Err()
}

func (s *Service) UpdateOrganization(ctx context.Context, actor Actor, id uuid.UUID, input UpdateOrganizationInput) (models.AdminOrganization, error) {
	if err := actor.validate(); err != nil || id == uuid.Nil || input.ExpectedVersion <= 0 {
		return models.AdminOrganization{}, ErrInvalidInput
	}
	var result models.AdminOrganization
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		current, err := scanOrganization(tx.QueryRow(ctx, `SELECT id, name, display_name, description, base_permission,
			representation_version, repositories_collection_version, members_collection_version,
			archived_at, created_at, updated_at FROM orgs WHERE id = $1 AND archived_at IS NULL FOR UPDATE`, id))
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
		if input.BasePermission != nil {
			if !input.BasePermission.Valid() {
				return ErrInvalidInput
			}
			current.BasePermission = *input.BasePermission
		}
		now := s.now().Truncate(time.Microsecond)
		if err := tx.QueryRow(ctx, `UPDATE orgs SET display_name = $2, description = $3,
			base_permission = $4, representation_version = representation_version + 1,
			updated_at = $5 WHERE id = $1
			RETURNING id, name, display_name, description, base_permission, representation_version,
			repositories_collection_version, members_collection_version, archived_at, created_at, updated_at`,
			id, current.DisplayName, current.Description, current.BasePermission, now).
			Scan(&result.ID, &result.Name, &result.DisplayName, &result.Description, &result.BasePermission,
				&result.RepresentationVersion, &result.RepositoriesCollectionVersion,
				&result.MembersCollectionVersion, &result.ArchivedAt, &result.CreatedAt, &result.UpdatedAt); err != nil {
			return err
		}
		return audit(ctx, tx, actor, id, uuid.Nil, id, "organization.update", "organization",
			map[string]any{"base_permission": result.BasePermission})
	})
	return result, mapError(err)
}

func (s *Service) ArchiveOrganization(ctx context.Context, actor Actor, id uuid.UUID, expectedVersion int64) error {
	if err := actor.validate(); err != nil || id == uuid.Nil || expectedVersion <= 0 {
		return ErrInvalidInput
	}
	return mapError(pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var version int64
		if err := tx.QueryRow(ctx, `SELECT representation_version FROM orgs
			WHERE id = $1 AND archived_at IS NULL FOR UPDATE`, id).Scan(&version); err != nil {
			return err
		}
		if version != expectedVersion {
			return ErrVersionConflict
		}
		now := s.now().Truncate(time.Microsecond)
		if _, err := tx.Exec(ctx, `UPDATE orgs SET archived_at = $2, updated_at = $2,
			representation_version = representation_version + 1 WHERE id = $1`, id, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE repos SET archived_at = COALESCE(archived_at, $2), updated_at = $2,
			representation_version = representation_version + 1 WHERE organization_id = $1`, id, now); err != nil {
			return err
		}
		return audit(ctx, tx, actor, id, uuid.Nil, id, "organization.archive", "organization", nil)
	}))
}

func (s *Service) InviteMembership(ctx context.Context, actor Actor, orgID uuid.UUID, input InviteMembershipInput) (models.AdminMembership, error) {
	if err := actor.validate(); err != nil || orgID == uuid.Nil || input.UserID == uuid.Nil || !validMembershipRole(input.Role) {
		return models.AdminMembership{}, ErrInvalidInput
	}
	var result models.AdminMembership
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if err := requireActiveOrganization(ctx, tx, orgID); err != nil {
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
		membershipID := uuid.New()
		var existingID uuid.UUID
		var existingArchivedAt *time.Time
		existingErr := tx.QueryRow(ctx, `SELECT id, archived_at FROM org_memberships
			WHERE organization_id = $1 AND user_id = $2 FOR UPDATE`, orgID, input.UserID).
			Scan(&existingID, &existingArchivedAt)
		switch {
		case existingErr == nil && existingArchivedAt == nil:
			return ErrConflict
		case existingErr == nil:
			membershipID = existingID
		case !errors.Is(existingErr, pgx.ErrNoRows):
			return existingErr
		}
		var err error
		if existingErr == nil {
			err = tx.QueryRow(ctx, `UPDATE org_memberships SET role = $3, state = 'invited',
				invited_by_user_id = $4, invited_at = $5, activated_at = NULL, archived_at = NULL,
				updated_at = $5, representation_version = representation_version + 1
				WHERE organization_id = $1 AND id = $2
				RETURNING id, organization_id, user_id, role, state, invited_by_user_id,
				invited_at, activated_at, archived_at, representation_version, created_at, updated_at`,
				orgID, membershipID, input.Role, actor.UserID, now).
				Scan(&result.ID, &result.OrganizationID, &result.UserID, &result.Role, &result.State,
					&result.InvitedByUserID, &result.InvitedAt, &result.ActivatedAt, &result.ArchivedAt,
					&result.RepresentationVersion, &result.CreatedAt, &result.UpdatedAt)
		} else {
			err = tx.QueryRow(ctx, `INSERT INTO org_memberships
			(id, organization_id, user_id, role, state, invited_by_user_id, invited_at,
			 activated_at, archived_at, created_at, updated_at)
			VALUES ($1, $2, $3, $4, 'invited', $5, $6, NULL, NULL, $6, $6)
			RETURNING id, organization_id, user_id, role, state, invited_by_user_id,
			invited_at, activated_at, archived_at, representation_version, created_at, updated_at`,
				membershipID, orgID, input.UserID, input.Role, actor.UserID, now).
				Scan(&result.ID, &result.OrganizationID, &result.UserID, &result.Role, &result.State,
					&result.InvitedByUserID, &result.InvitedAt, &result.ActivatedAt, &result.ArchivedAt,
					&result.RepresentationVersion, &result.CreatedAt, &result.UpdatedAt)
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE orgs SET members_collection_version = members_collection_version + 1,
			updated_at = $2 WHERE id = $1`, orgID, now); err != nil {
			return err
		}
		return audit(ctx, tx, actor, orgID, uuid.Nil, result.ID, "membership.invite", "org_membership",
			map[string]any{"target_user_id": input.UserID, "role": input.Role})
	})
	return result, mapError(err)
}

func (s *Service) UpdateMembership(ctx context.Context, actor Actor, orgID, membershipID uuid.UUID, input UpdateMembershipInput) (models.AdminMembership, error) {
	if err := actor.validate(); err != nil || orgID == uuid.Nil || membershipID == uuid.Nil ||
		!validMembershipRole(input.Role) || !input.State.Valid() || input.ExpectedVersion <= 0 {
		return models.AdminMembership{}, ErrInvalidInput
	}
	var result models.AdminMembership
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		current, err := scanMembership(tx.QueryRow(ctx, membershipSelect+` WHERE organization_id = $1 AND id = $2
			AND archived_at IS NULL FOR UPDATE`, orgID, membershipID))
		if err != nil {
			return err
		}
		if current.RepresentationVersion != input.ExpectedVersion {
			return ErrVersionConflict
		}
		if current.Role == "owner" && (input.Role != "owner" || input.State != models.MembershipActive) {
			if err := ensureAnotherOwner(ctx, tx, orgID, membershipID); err != nil {
				return err
			}
		}
		now := s.now().Truncate(time.Microsecond)
		if err := tx.QueryRow(ctx, `UPDATE org_memberships SET role = $3, state = $4,
			activated_at = CASE WHEN $4 = 'active' THEN COALESCE(activated_at, $5) ELSE activated_at END,
			updated_at = $5, representation_version = representation_version + 1
			WHERE organization_id = $1 AND id = $2
			RETURNING id, organization_id, user_id, role, state, invited_by_user_id,
			invited_at, activated_at, archived_at, representation_version, created_at, updated_at`,
			orgID, membershipID, input.Role, input.State, now).
			Scan(&result.ID, &result.OrganizationID, &result.UserID, &result.Role, &result.State,
				&result.InvitedByUserID, &result.InvitedAt, &result.ActivatedAt, &result.ArchivedAt,
				&result.RepresentationVersion, &result.CreatedAt, &result.UpdatedAt); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE orgs SET members_collection_version = members_collection_version + 1,
			updated_at = $2 WHERE id = $1`, orgID, now); err != nil {
			return err
		}
		return audit(ctx, tx, actor, orgID, uuid.Nil, membershipID, "membership.update", "org_membership",
			map[string]any{"role": input.Role, "state": input.State})
	})
	return result, mapError(err)
}

func (s *Service) ArchiveMembership(ctx context.Context, actor Actor, orgID, membershipID uuid.UUID, expectedVersion int64) error {
	if err := actor.validate(); err != nil || orgID == uuid.Nil || membershipID == uuid.Nil || expectedVersion <= 0 {
		return ErrInvalidInput
	}
	return mapError(pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		current, err := scanMembership(tx.QueryRow(ctx, membershipSelect+` WHERE organization_id = $1 AND id = $2
			AND archived_at IS NULL FOR UPDATE`, orgID, membershipID))
		if err != nil {
			return err
		}
		if current.RepresentationVersion != expectedVersion {
			return ErrVersionConflict
		}
		if current.Role == "owner" && current.State == models.MembershipActive {
			if err := ensureAnotherOwner(ctx, tx, orgID, membershipID); err != nil {
				return err
			}
		}
		now := s.now().Truncate(time.Microsecond)
		if _, err := tx.Exec(ctx, `UPDATE org_memberships SET state = 'suspended', archived_at = $3,
			updated_at = $3, representation_version = representation_version + 1
			WHERE organization_id = $1 AND id = $2`, orgID, membershipID, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE orgs SET members_collection_version = members_collection_version + 1,
			updated_at = $2 WHERE id = $1`, orgID, now); err != nil {
			return err
		}
		return audit(ctx, tx, actor, orgID, uuid.Nil, membershipID, "membership.archive", "org_membership", nil)
	}))
}

func (s *Service) ListMemberships(ctx context.Context, orgID uuid.UUID, includeArchived bool) ([]models.AdminMembership, error) {
	query := membershipSelect + ` WHERE organization_id = $1`
	if !includeArchived {
		query += ` AND archived_at IS NULL`
	}
	query += ` ORDER BY created_at, id`
	rows, err := s.pool.Query(ctx, query, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []models.AdminMembership
	for rows.Next() {
		membership, err := scanMembership(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, membership)
	}
	return result, rows.Err()
}

const membershipSelect = `SELECT id, organization_id, user_id, role, state, invited_by_user_id,
	invited_at, activated_at, archived_at, representation_version, created_at, updated_at FROM org_memberships`

func scanOrganization(row pgx.Row) (models.AdminOrganization, error) {
	var organization models.AdminOrganization
	err := row.Scan(&organization.ID, &organization.Name, &organization.DisplayName, &organization.Description,
		&organization.BasePermission, &organization.RepresentationVersion,
		&organization.RepositoriesCollectionVersion, &organization.MembersCollectionVersion,
		&organization.ArchivedAt, &organization.CreatedAt, &organization.UpdatedAt)
	return organization, mapError(err)
}

func scanMembership(row pgx.Row) (models.AdminMembership, error) {
	var membership models.AdminMembership
	err := row.Scan(&membership.ID, &membership.OrganizationID, &membership.UserID, &membership.Role,
		&membership.State, &membership.InvitedByUserID, &membership.InvitedAt, &membership.ActivatedAt,
		&membership.ArchivedAt, &membership.RepresentationVersion, &membership.CreatedAt, &membership.UpdatedAt)
	return membership, mapError(err)
}

func requireActiveOrganization(ctx context.Context, tx pgx.Tx, orgID uuid.UUID) error {
	var found uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM orgs WHERE id = $1 AND archived_at IS NULL FOR UPDATE`, orgID).Scan(&found); err != nil {
		return err
	}
	return nil
}

func ensureAnotherOwner(ctx context.Context, tx pgx.Tx, orgID, excludedMembershipID uuid.UUID) error {
	var owners int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM org_memberships WHERE organization_id = $1
		AND id <> $2 AND role = 'owner' AND state = 'active' AND archived_at IS NULL`, orgID, excludedMembershipID).Scan(&owners); err != nil {
		return err
	}
	if owners == 0 {
		return ErrLastOrganizationOwner
	}
	return nil
}
