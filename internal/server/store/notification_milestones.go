package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type NotificationMilestone string

const (
	NotificationMilestoneProposal  NotificationMilestone = "proposal"
	NotificationMilestoneDesign    NotificationMilestone = "design"
	NotificationMilestoneImplement NotificationMilestone = "implement"
	NotificationMilestoneCompleted NotificationMilestone = "completed"
)

func (m NotificationMilestone) Valid() bool {
	return m == NotificationMilestoneProposal || m == NotificationMilestoneDesign ||
		m == NotificationMilestoneImplement || m == NotificationMilestoneCompleted
}

type CreatedArtifactNotification struct {
	ChangeKey string
	Milestone NotificationMilestone
}

type NotificationMilestoneInput struct {
	ID                  uuid.UUID
	ChangeKey           string
	Milestone           NotificationMilestone
	TriggeringIssueID   uuid.UUID
	TriggeringCommentID *uuid.UUID
	ActorUserID         uuid.UUID
	OccurredAt          time.Time
}

type NotificationMilestoneFact struct {
	NotificationMilestoneInput
	Scope     RepoScope
	CreatedAt time.Time
}

// CreatedIssueArtifactNotification reads only the authoritative active issue
// projection. Invalid, duplicate, or anomalous markers do not have an active
// projection and therefore do not enter the milestone path.
func (s RepoStore) CreatedIssueArtifactNotification(ctx context.Context, issueID uuid.UUID) (CreatedArtifactNotification, bool, error) {
	if err := s.validate(); err != nil || issueID == uuid.Nil {
		return CreatedArtifactNotification{}, false, ErrInvalidInput
	}
	var result CreatedArtifactNotification
	err := s.db.QueryRow(ctx, `SELECT change_key, artifact_type FROM issue_spec_artifacts
		WHERE organization_id = $1 AND repository_id = $2 AND issue_id = $3 AND active
		AND artifact_type IN ('proposal','design','implement')`, s.scope.OrgID, s.scope.RepoID, issueID).
		Scan(&result.ChangeKey, &result.Milestone)
	if errors.Is(err, pgx.ErrNoRows) {
		return CreatedArtifactNotification{}, false, nil
	}
	if err != nil {
		return CreatedArtifactNotification{}, false, fmt.Errorf("load created artifact notification: %w", err)
	}
	result.ChangeKey = strings.ToLower(strings.TrimSpace(result.ChangeKey))
	if result.ChangeKey == "" || len(result.ChangeKey) > 200 || !result.Milestone.Valid() ||
		result.Milestone == NotificationMilestoneCompleted {
		return CreatedArtifactNotification{}, false, ErrInvalidInput
	}
	return result, true, nil
}

// InsertNotificationMilestone records the repository-wide fact before any
// recipient fanout. Only the transaction which inserts the unique fact may
// enqueue deliveries; retries and lifecycle re-entry return inserted=false.
func (s RepoStore) InsertNotificationMilestone(ctx context.Context, input NotificationMilestoneInput) (NotificationMilestoneFact, bool, error) {
	input.ChangeKey = strings.ToLower(strings.TrimSpace(input.ChangeKey))
	if err := s.validate(); err != nil || !s.inTx || input.ID == uuid.Nil || input.ChangeKey == "" ||
		len(input.ChangeKey) > 200 || !input.Milestone.Valid() || input.TriggeringIssueID == uuid.Nil ||
		input.ActorUserID == uuid.Nil || input.OccurredAt.IsZero() ||
		(input.TriggeringCommentID != nil && *input.TriggeringCommentID == uuid.Nil) {
		return NotificationMilestoneFact{}, false, ErrInvalidInput
	}
	row := s.db.QueryRow(ctx, `INSERT INTO change_notification_milestones
		(id, organization_id, repository_id, change_key, milestone, triggering_issue_id,
		 triggering_comment_id, actor_user_id, occurred_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (organization_id, repository_id, change_key_key, milestone) DO NOTHING
		RETURNING created_at`, input.ID, s.scope.OrgID, s.scope.RepoID, input.ChangeKey, input.Milestone,
		input.TriggeringIssueID, input.TriggeringCommentID, input.ActorUserID, input.OccurredAt)
	var created time.Time
	if err := row.Scan(&created); err == nil {
		return NotificationMilestoneFact{NotificationMilestoneInput: input, Scope: s.scope, CreatedAt: created}, true, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return NotificationMilestoneFact{}, false, fmt.Errorf("insert notification milestone: %w", mapError(err))
	}
	var existing NotificationMilestoneFact
	existing.Scope = s.scope
	err := s.db.QueryRow(ctx, `SELECT id, change_key, milestone, triggering_issue_id,
		triggering_comment_id, actor_user_id, occurred_at, created_at
		FROM change_notification_milestones
		WHERE organization_id = $1 AND repository_id = $2
		AND change_key_key = lower($3) AND milestone = $4`, s.scope.OrgID, s.scope.RepoID,
		input.ChangeKey, input.Milestone).Scan(&existing.ID, &existing.ChangeKey, &existing.Milestone,
		&existing.TriggeringIssueID, &existing.TriggeringCommentID, &existing.ActorUserID,
		&existing.OccurredAt, &existing.CreatedAt)
	if err != nil {
		return NotificationMilestoneFact{}, false, fmt.Errorf("load notification milestone: %w", mapError(err))
	}
	return existing, false, nil
}
