package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrProjectionConflict = errors.New("store: projection conflict")

type IssueProjectionInput struct {
	IssueID      uuid.UUID
	ChangeKey    string
	ArtifactType string
	Content      string
	Metadata     json.RawMessage
	UserID       *uuid.UUID
}

type TypedCommentProjectionInput struct {
	IssueID   uuid.UUID
	CommentID uuid.UUID
	Type      string
	Key       string
	Body      string
	Metadata  json.RawMessage
	UserID    *uuid.UUID
}

type ProjectionAnomalyInput struct {
	SourceType string
	SourceID   uuid.UUID
	Key        string
	Details    json.RawMessage
}

func (s RepoStore) ApplyIssueProjection(ctx context.Context, input IssueProjectionInput) error {
	if err := s.validate(); err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx, `UPDATE issue_spec_artifacts SET active = false,
		representation_version = representation_version + 1, updated_at = clock_timestamp()
		WHERE organization_id = $1 AND repository_id = $2 AND issue_id = $3 AND active
		AND (change_key <> $4 OR artifact_type <> $5)`, s.scope.OrgID, s.scope.RepoID,
		input.IssueID, input.ChangeKey, input.ArtifactType); err != nil {
		return fmt.Errorf("clear stale issue projection: %w", err)
	}
	var id uuid.UUID
	err := s.db.QueryRow(ctx, `INSERT INTO issue_spec_artifacts
			(id, organization_id, repository_id, issue_id, change_key, artifact_type,
			 content, metadata, created_by_user_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9)
			ON CONFLICT (organization_id, repository_id, change_key, artifact_type) WHERE active
			DO NOTHING RETURNING id`, uuid.New(),
		s.scope.OrgID, s.scope.RepoID, input.IssueID, input.ChangeKey,
		input.ArtifactType, input.Content, string(input.Metadata), input.UserID).Scan(&id)
	if err == nil {
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var existingIssueID uuid.UUID
	if err := s.db.QueryRow(ctx, `SELECT id, issue_id FROM issue_spec_artifacts
		WHERE organization_id = $1 AND repository_id = $2 AND change_key = $3
		AND artifact_type = $4 AND active FOR UPDATE`, s.scope.OrgID, s.scope.RepoID,
		input.ChangeKey, input.ArtifactType).Scan(&id, &existingIssueID); err != nil {
		return fmt.Errorf("load conflicting issue projection: %w", mapError(err))
	}
	if existingIssueID != input.IssueID {
		return ErrProjectionConflict
	}
	_, err = s.db.Exec(ctx, `UPDATE issue_spec_artifacts SET content = $4,
		metadata = $5::jsonb, created_by_user_id = $6,
		representation_version = representation_version + 1, updated_at = clock_timestamp()
		WHERE organization_id = $1 AND repository_id = $2 AND id = $3 AND issue_id = $7`,
		s.scope.OrgID, s.scope.RepoID, id, input.Content, string(input.Metadata), input.UserID, input.IssueID)
	return err
}

func (s RepoStore) ClearIssueProjection(ctx context.Context, issueID uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `UPDATE issue_spec_artifacts SET active = false,
		representation_version = representation_version + 1, updated_at = clock_timestamp()
		WHERE organization_id = $1 AND repository_id = $2 AND issue_id = $3 AND active`,
		s.scope.OrgID, s.scope.RepoID, issueID)
	if err != nil {
		return err
	}
	_ = tag
	return err
}

func (s RepoStore) ApplyTypedCommentProjection(ctx context.Context, input TypedCommentProjectionInput) error {
	if err := s.validate(); err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM issue_spec_typed_comments
		WHERE organization_id = $1 AND repository_id = $2 AND comment_id = $3
		AND comment_key <> $4`, s.scope.OrgID, s.scope.RepoID, input.CommentID, input.Key); err != nil {
		return err
	}
	var id uuid.UUID
	err := s.db.QueryRow(ctx, `INSERT INTO issue_spec_typed_comments
		(id, organization_id, repository_id, issue_id, comment_id, comment_type,
		 comment_key, body, metadata, created_by_user_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10)
		ON CONFLICT (organization_id, repository_id, comment_key) DO NOTHING
		RETURNING id`, uuid.New(), s.scope.OrgID, s.scope.RepoID,
		input.IssueID, input.CommentID, input.Type, input.Key, input.Body,
		string(input.Metadata), input.UserID).Scan(&id)
	if err == nil {
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("apply typed comment projection: %w", mapError(err))
	}
	var existingCommentID uuid.UUID
	if err := s.db.QueryRow(ctx, `SELECT id, comment_id FROM issue_spec_typed_comments
		WHERE organization_id = $1 AND repository_id = $2 AND comment_key = $3 FOR UPDATE`,
		s.scope.OrgID, s.scope.RepoID, input.Key).Scan(&id, &existingCommentID); err != nil {
		return fmt.Errorf("load conflicting typed comment projection: %w", mapError(err))
	}
	if existingCommentID != input.CommentID {
		return ErrProjectionConflict
	}
	_, err = s.db.Exec(ctx, `UPDATE issue_spec_typed_comments SET issue_id = $4,
		comment_type = $5, body = $6, metadata = $7::jsonb, created_by_user_id = $8,
		representation_version = representation_version + 1, updated_at = clock_timestamp()
		WHERE organization_id = $1 AND repository_id = $2 AND id = $3 AND comment_id = $9`,
		s.scope.OrgID, s.scope.RepoID, id, input.IssueID, input.Type, input.Body,
		string(input.Metadata), input.UserID, input.CommentID)
	return err
}

func (s RepoStore) ClearTypedCommentProjection(ctx context.Context, commentID uuid.UUID) error {
	_, err := s.db.Exec(ctx, `DELETE FROM issue_spec_typed_comments
		WHERE organization_id = $1 AND repository_id = $2 AND comment_id = $3`,
		s.scope.OrgID, s.scope.RepoID, commentID)
	return err
}

func (s RepoStore) RecordProjectionAnomaly(ctx context.Context, input ProjectionAnomalyInput) error {
	_, err := s.db.Exec(ctx, `INSERT INTO projection_anomalies
		(id, organization_id, repository_id, projection_name, source_type,
		 source_id, anomaly_key, details)
		VALUES ($1, $2, $3, 'issue-spec-marker', $4, $5, $6, $7::jsonb)
		ON CONFLICT (organization_id, repository_id, projection_name, source_type, source_id, anomaly_key)
		DO UPDATE SET details = EXCLUDED.details, observed_at = clock_timestamp(), resolved_at = NULL`,
		uuid.New(), s.scope.OrgID, s.scope.RepoID, input.SourceType,
		input.SourceID, input.Key, string(input.Details))
	return err
}

func (s RepoStore) ResolveProjectionAnomalies(ctx context.Context, sourceType string, sourceID uuid.UUID) error {
	_, err := s.db.Exec(ctx, `UPDATE projection_anomalies SET resolved_at = clock_timestamp()
		WHERE organization_id = $1 AND repository_id = $2 AND projection_name = 'issue-spec-marker'
		AND source_type = $3 AND source_id = $4 AND resolved_at IS NULL`,
		s.scope.OrgID, s.scope.RepoID, sourceType, sourceID)
	return err
}
