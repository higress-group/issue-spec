package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/jackc/pgx/v5"
)

func (s *Store) ProtocolUserForRepository(ctx context.Context, scope models.RepoScope, login string) (models.ProtocolUser, error) {
	if s == nil || s.pool == nil {
		return models.ProtocolUser{}, errors.New("store: nil database")
	}
	if err := scope.Validate(); err != nil {
		return models.ProtocolUser{}, ErrInvalidScope
	}
	var user models.ProtocolUser
	if err := s.pool.QueryRow(ctx, `SELECT u.id, u.login, u.status, u.updated_at FROM users u
		WHERE u.login_key = lower($3) AND u.status = 'active' AND (
			EXISTS (SELECT 1 FROM org_memberships om WHERE om.organization_id = $1
				AND om.user_id = u.id AND om.state = 'active')
			OR EXISTS (SELECT 1 FROM repo_collaborators rc WHERE rc.organization_id = $1
				AND rc.repository_id = $2 AND rc.user_id = u.id)
			OR EXISTS (SELECT 1 FROM site_role_assignments sr WHERE sr.user_id = u.id
				AND sr.role = 'site_admin')
		)`, scope.OrgID, scope.RepoID, login).
		Scan(&user.ID, &user.Login, &user.Status, &user.UpdatedAt); err != nil {
		return models.ProtocolUser{}, fmt.Errorf("load protocol user: %w", mapError(err))
	}
	return user, nil
}

func (s RepoStore) RepositorySubscription(ctx context.Context, userID uuid.UUID) (models.RepositorySubscription, error) {
	if err := s.validate(); err != nil || userID == uuid.Nil {
		return models.RepositorySubscription{}, ErrInvalidInput
	}
	result := models.RepositorySubscription{UserID: userID}
	if err := s.db.QueryRow(ctx, `SELECT subscriptions_collection_version FROM repos
		WHERE organization_id = $1 AND id = $2`, s.scope.OrgID, s.scope.RepoID).
		Scan(&result.CollectionVersion); err != nil {
		return models.RepositorySubscription{}, fmt.Errorf("load subscription collection: %w", mapError(err))
	}
	err := s.db.QueryRow(ctx, `SELECT reason, representation_version, created_at, updated_at FROM repo_subscriptions
		WHERE organization_id = $1 AND repository_id = $2 AND user_id = $3`,
		s.scope.OrgID, s.scope.RepoID, userID).Scan(&result.Reason, &result.RepresentationVersion,
		&result.CreatedAt, &result.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		return models.RepositorySubscription{}, fmt.Errorf("load repository subscription: %w", mapError(err))
	}
	result.Ignored = result.Reason == "ignored"
	result.Subscribed = !result.Ignored
	return result, nil
}
