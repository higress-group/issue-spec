package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/jackc/pgx/v5"
)

// RepositoryNotificationCandidate is the minimal identity needed to create a
// repository delivery. Notification addresses never enter render snapshots.
type RepositoryNotificationCandidate struct {
	UserID      uuid.UUID
	Login       string
	DisplayName string
}

// RepositoryNotificationRecipient is reloaded immediately before delivery.
// Callers must not log or persist Address outside the sender boundary.
type RepositoryNotificationRecipient struct {
	RepositoryNotificationCandidate
	Address         string
	RepositoryOwner string
	RepositoryName  string
}

func (s *Store) RepositoryNotificationResource(ctx context.Context, scope models.RepoScope) (models.RepositoryResource, error) {
	if s == nil || s.pool == nil {
		return models.RepositoryResource{}, errors.New("store: nil database")
	}
	return repositoryNotificationResource(ctx, s.pool, scope)
}

func (t *Tx) RepositoryNotificationResource(ctx context.Context, scope models.RepoScope) (models.RepositoryResource, error) {
	if t == nil || t.tx == nil {
		return models.RepositoryResource{}, errors.New("store: nil transaction")
	}
	return repositoryNotificationResource(ctx, t.tx, scope)
}

func repositoryNotificationResource(ctx context.Context, db DBTX, scope models.RepoScope) (models.RepositoryResource, error) {
	if err := scope.Validate(); err != nil {
		return models.RepositoryResource{}, ErrInvalidScope
	}
	var result models.RepositoryResource
	err := db.QueryRow(ctx, `SELECT o.id, r.id, o.name, r.name, r.visibility,
		r.issues_collection_version, r.comments_collection_version, r.updated_at
		FROM orgs o JOIN repos r ON r.organization_id = o.id
		WHERE o.id = $1 AND r.id = $2 AND o.archived_at IS NULL AND r.archived_at IS NULL`,
		scope.OrgID, scope.RepoID).Scan(&result.Scope.OrgID, &result.Scope.RepoID, &result.Owner,
		&result.Name, &result.Visibility, &result.IssuesCollectionVersion,
		&result.CommentsCollectionVersion, &result.UpdatedAt)
	if err != nil {
		return models.RepositoryResource{}, fmt.Errorf("load repository notification resource: %w", mapError(err))
	}
	return result, nil
}

// RepositoryNotificationActorEligible verifies the account/address boundary
// used by subscription mutations. Repository authority is evaluated by the
// service through the canonical authorization component.
func (s RepoStore) RepositoryNotificationActorEligible(ctx context.Context, userID uuid.UUID) (bool, error) {
	if err := s.validate(); err != nil || userID == uuid.Nil {
		return false, ErrInvalidInput
	}
	var eligible bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM users u WHERE u.id = $1 AND u.status = 'active'
		AND u.notification_email IS NOT NULL AND u.notification_email_verified_at IS NOT NULL
		AND NOT EXISTS (SELECT 1 FROM service_accounts sa WHERE sa.user_id = u.id)
	)`, userID).Scan(&eligible)
	if err != nil {
		return false, fmt.Errorf("check repository notification actor: %w", err)
	}
	return eligible, nil
}

// SetManualRepositorySubscription is idempotent. The repository collection
// version advances only when the stored relation changes.
func (s RepoStore) SetManualRepositorySubscription(ctx context.Context, userID uuid.UUID, subscribed bool) (models.RepositorySubscription, bool, error) {
	if err := s.validate(); err != nil || userID == uuid.Nil || !s.inTx {
		return models.RepositorySubscription{}, false, ErrInvalidInput
	}
	changed := false
	if subscribed {
		var id uuid.UUID
		err := s.db.QueryRow(ctx, `INSERT INTO repo_subscriptions
			(organization_id, repository_id, user_id, reason)
			VALUES ($1, $2, $3, 'manual')
			ON CONFLICT (organization_id, repository_id, user_id) DO UPDATE
			SET reason = 'manual', representation_version = repo_subscriptions.representation_version + 1,
				updated_at = clock_timestamp()
			WHERE repo_subscriptions.reason <> 'manual'
			RETURNING id`, s.scope.OrgID, s.scope.RepoID, userID).Scan(&id)
		switch {
		case err == nil:
			changed = true
		case errors.Is(err, pgx.ErrNoRows):
			// An existing manual row already represents the requested state.
		default:
			return models.RepositorySubscription{}, false, fmt.Errorf("subscribe repository notifications: %w", mapError(err))
		}
	} else {
		tag, err := s.db.Exec(ctx, `DELETE FROM repo_subscriptions
			WHERE organization_id = $1 AND repository_id = $2 AND user_id = $3`,
			s.scope.OrgID, s.scope.RepoID, userID)
		if err != nil {
			return models.RepositorySubscription{}, false, fmt.Errorf("unsubscribe repository notifications: %w", mapError(err))
		}
		changed = tag.RowsAffected() == 1
	}
	if changed {
		if _, err := s.IncrementCollectionVersions(ctx, RepoCollectionSubscriptions); err != nil {
			return models.RepositorySubscription{}, false, err
		}
	}
	result, err := s.RepositorySubscription(ctx, userID)
	return result, changed, err
}

// ManualRepositoryNotificationSubscribers returns only current manual,
// active-human, verified-address recipients with repository read authority.
// It is safe to call through a transaction-backed RepoStore.
func (s RepoStore) ManualRepositoryNotificationSubscribers(ctx context.Context, excludeUserID uuid.UUID) ([]RepositoryNotificationCandidate, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT u.id, u.login, COALESCE(u.nickname, u.display_name, u.login)
		FROM repo_subscriptions rs
		JOIN users u ON u.id = rs.user_id
		JOIN repos r ON r.organization_id = rs.organization_id AND r.id = rs.repository_id
		JOIN orgs o ON o.id = r.organization_id
		WHERE rs.organization_id = $1 AND rs.repository_id = $2 AND rs.reason = 'manual'
		AND ($3::uuid = '00000000-0000-0000-0000-000000000000' OR u.id <> $3)
		AND `+repositoryNotificationEligibilitySQL+`
		ORDER BY u.login_key, u.id`, s.scope.OrgID, s.scope.RepoID, excludeUserID)
	if err != nil {
		return nil, fmt.Errorf("list repository notification subscribers: %w", err)
	}
	defer rows.Close()
	result := make([]RepositoryNotificationCandidate, 0)
	for rows.Next() {
		var candidate RepositoryNotificationCandidate
		if err := rows.Scan(&candidate.UserID, &candidate.Login, &candidate.DisplayName); err != nil {
			return nil, fmt.Errorf("scan repository notification subscriber: %w", err)
		}
		result = append(result, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate repository notification subscribers: %w", err)
	}
	return result, nil
}

// RepositoryNotificationRecipientForDelivery rechecks subscription, address,
// account kind/state and repository read authority immediately before send.
func RepositoryNotificationRecipientForDelivery(ctx context.Context, db DBTX, scope models.RepoScope, userID uuid.UUID) (RepositoryNotificationRecipient, error) {
	if db == nil || scope.Validate() != nil || userID == uuid.Nil {
		return RepositoryNotificationRecipient{}, ErrInvalidInput
	}
	var result RepositoryNotificationRecipient
	err := db.QueryRow(ctx, `SELECT u.id, u.login, COALESCE(u.nickname, u.display_name, u.login),
		u.notification_email, o.name, r.name
		FROM repo_subscriptions rs
		JOIN users u ON u.id = rs.user_id
		JOIN repos r ON r.organization_id = rs.organization_id AND r.id = rs.repository_id
		JOIN orgs o ON o.id = r.organization_id
		WHERE rs.organization_id = $1 AND rs.repository_id = $2 AND rs.user_id = $3
		AND rs.reason = 'manual' AND `+repositoryNotificationEligibilitySQL,
		scope.OrgID, scope.RepoID, userID).Scan(&result.UserID, &result.Login, &result.DisplayName,
		&result.Address, &result.RepositoryOwner, &result.RepositoryName)
	if errors.Is(err, pgx.ErrNoRows) {
		return RepositoryNotificationRecipient{}, ErrNotFound
	}
	if err != nil {
		return RepositoryNotificationRecipient{}, fmt.Errorf("load repository notification recipient: %w", err)
	}
	return result, nil
}

func (s RepoStore) RepositoryNotificationActor(ctx context.Context, userID uuid.UUID) (RepositoryNotificationCandidate, error) {
	if err := s.validate(); err != nil || userID == uuid.Nil {
		return RepositoryNotificationCandidate{}, ErrInvalidInput
	}
	var result RepositoryNotificationCandidate
	err := s.db.QueryRow(ctx, `SELECT id, login, COALESCE(nickname, display_name, login)
		FROM users WHERE id = $1 AND status = 'active'`, userID).
		Scan(&result.UserID, &result.Login, &result.DisplayName)
	if err != nil {
		return RepositoryNotificationCandidate{}, fmt.Errorf("load repository notification actor: %w", mapError(err))
	}
	return result, nil
}

func (s RepoStore) IssueHasActiveArtifactProjection(ctx context.Context, issueID uuid.UUID) (bool, error) {
	if err := s.validate(); err != nil || issueID == uuid.Nil {
		return false, ErrInvalidInput
	}
	var projected bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM issue_spec_artifacts
		WHERE organization_id = $1 AND repository_id = $2 AND issue_id = $3 AND active
		AND artifact_type IN ('proposal','design','implement'))`, s.scope.OrgID, s.scope.RepoID, issueID).Scan(&projected)
	if err != nil {
		return false, fmt.Errorf("classify repository notification issue: %w", err)
	}
	return projected, nil
}

const repositoryNotificationEligibilitySQL = `
	u.status = 'active'
	AND u.notification_email IS NOT NULL
	AND u.notification_email_verified_at IS NOT NULL
	AND NOT EXISTS (SELECT 1 FROM service_accounts sa WHERE sa.user_id = u.id)
	AND o.archived_at IS NULL AND r.archived_at IS NULL
	AND (
		r.visibility IN ('public', 'internal')
		OR EXISTS (SELECT 1 FROM site_role_assignments sr WHERE sr.user_id = u.id AND sr.role = 'site_admin')
		OR EXISTS (SELECT 1 FROM org_memberships om WHERE om.organization_id = r.organization_id
			AND om.user_id = u.id AND om.state = 'active')
		OR EXISTS (SELECT 1 FROM repo_collaborators rc WHERE rc.organization_id = r.organization_id
			AND rc.repository_id = r.id AND rc.user_id = u.id)
	)`
