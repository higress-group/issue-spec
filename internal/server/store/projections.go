package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/models"
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

// TypedCommentAuthority is the bounded, transaction-authoritative join used by
// trusted typed-comment services. It deliberately exposes only the stored
// comment and projection identity needed to revalidate one exact typed key.
type TypedCommentAuthority struct {
	CommentID             uuid.UUID
	CompatibilityID       int64
	IssueNumber           int64
	Type                  string
	Key                   string
	Body                  string
	RepresentationVersion int64
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

// TypedAnswerObservation carries one projected ANSWER comment with the
// provider facts effective-answer resolution needs. ActorLogin is display
// identity only; ordering and validity come from the other fields.
type TypedAnswerObservation struct {
	CompatibilityID       int64
	ActorLogin            string
	Body                  string
	RepresentationVersion int64
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

type ProjectionAnomalyInput struct {
	SourceType string
	SourceID   uuid.UUID
	Key        string
	Details    json.RawMessage
}

// NotificationSnapshot is the transaction-authoritative classification and
// presentation input captured after projection and before outbox insertion.
type NotificationSnapshot struct {
	OrganizationName        string
	OrganizationDisplayName string
	RepositoryName          string
	RepositoryDisplayName   string
	RepositoryVisibility    string
	Issue                   models.IssueSnapshot
	IssueKind               string
	CommentTyped            bool
	ActorLogin              string
	ActorDisplayName        string
	ActorServiceAccount     bool
}

func (s RepoStore) NotificationSnapshot(ctx context.Context, issueNumber int64,
	commentID *uuid.UUID, actorID uuid.UUID) (NotificationSnapshot, error) {
	if err := s.validate(); err != nil || issueNumber < 1 || actorID == uuid.Nil {
		return NotificationSnapshot{}, ErrInvalidInput
	}
	issue, err := s.IssueSnapshotByNumber(ctx, issueNumber)
	if err != nil {
		return NotificationSnapshot{}, err
	}
	result := NotificationSnapshot{Issue: issue, IssueKind: "ordinary"}
	if err := s.db.QueryRow(ctx, `SELECT organization.name, organization.display_name,
		repository.name, repository.display_name, repository.visibility, actor.login,
		COALESCE(actor.nickname, actor.display_name),
		EXISTS (SELECT 1 FROM service_accounts account
			WHERE account.organization_id = repository.organization_id
			AND account.user_id = actor.id AND account.disabled_at IS NULL)
		FROM repos repository JOIN orgs organization ON organization.id = repository.organization_id
		JOIN users actor ON actor.id = $3
		WHERE repository.organization_id = $1 AND repository.id = $2`,
		s.scope.OrgID, s.scope.RepoID, actorID).Scan(&result.OrganizationName,
		&result.OrganizationDisplayName, &result.RepositoryName,
		&result.RepositoryDisplayName, &result.RepositoryVisibility, &result.ActorLogin,
		&result.ActorDisplayName,
		&result.ActorServiceAccount); err != nil {
		return NotificationSnapshot{}, mapError(err)
	}
	var kind string
	err = s.db.QueryRow(ctx, `SELECT artifact_type FROM issue_spec_artifacts
		WHERE organization_id = $1 AND repository_id = $2 AND issue_id = $3 AND active
		ORDER BY updated_at DESC, id DESC LIMIT 1`, s.scope.OrgID, s.scope.RepoID, issue.Issue.ID).Scan(&kind)
	if err == nil && (kind == "proposal" || kind == "design" || kind == "implement") {
		result.IssueKind = kind
	} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return NotificationSnapshot{}, err
	}
	if commentID != nil {
		var typed bool
		if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM issue_spec_typed_comments typed
			JOIN comments comment ON comment.organization_id = typed.organization_id
			AND comment.repository_id = typed.repository_id AND comment.id = typed.comment_id
			WHERE typed.organization_id = $1 AND typed.repository_id = $2
			AND typed.comment_id = $3 AND typed.created_by_user_id = comment.author_id)`,
			s.scope.OrgID, s.scope.RepoID, *commentID).Scan(&typed); err != nil {
			return NotificationSnapshot{}, err
		}
		result.CommentTyped = typed
	}
	return result, nil
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

// TypedCommentAuthorityByKey resolves one projected typed comment by its
// repository-unique key without scanning the ordinary comment timeline. A
// locking read must run inside the caller's mutation transaction so validation
// and any dependent write share the same authoritative snapshot.
func (s RepoStore) TypedCommentAuthorityByKey(ctx context.Context, issueNumber int64,
	key, commentType string, lock bool) (TypedCommentAuthority, error) {
	key, commentType = strings.TrimSpace(key), strings.ToUpper(strings.TrimSpace(commentType))
	if err := s.validate(); err != nil || issueNumber <= 0 || key == "" || commentType == "" ||
		(lock && !s.inTx) {
		return TypedCommentAuthority{}, ErrInvalidInput
	}
	query := `SELECT c.id, c.compatibility_id, i.number, typed.comment_type,
		typed.comment_key, c.body, c.representation_version, c.created_at, c.updated_at
		FROM issue_spec_typed_comments typed
		JOIN comments c ON c.organization_id = typed.organization_id
			AND c.repository_id = typed.repository_id AND c.id = typed.comment_id
		JOIN issues i ON i.organization_id = c.organization_id
			AND i.repository_id = c.repository_id AND i.id = c.issue_id
		WHERE typed.organization_id = $1 AND typed.repository_id = $2
			AND i.number = $3 AND typed.comment_key = $4 AND typed.comment_type = $5`
	if lock {
		query += ` FOR UPDATE OF typed, c`
	}
	var result TypedCommentAuthority
	err := s.db.QueryRow(ctx, query, s.scope.OrgID, s.scope.RepoID, issueNumber, key, commentType).
		Scan(&result.CommentID, &result.CompatibilityID, &result.IssueNumber, &result.Type,
			&result.Key, &result.Body, &result.RepresentationVersion, &result.CreatedAt, &result.UpdatedAt)
	if err != nil {
		return TypedCommentAuthority{}, fmt.Errorf("load typed comment authority: %w", mapError(err))
	}
	return result, nil
}

// TypedAnswerObservationsByIssue lists every projected ANSWER comment on one
// issue so the caller can run canonical effective-answer resolution. Rows are
// ordered deterministically; validity filtering stays in the model layer.
func (s RepoStore) TypedAnswerObservationsByIssue(ctx context.Context, issueNumber int64) ([]TypedAnswerObservation, error) {
	if err := s.validate(); err != nil || issueNumber <= 0 {
		return nil, ErrInvalidInput
	}
	rows, err := s.db.Query(ctx, `SELECT c.compatibility_id, COALESCE(u.login, 'ghost'),
		c.body, c.representation_version, c.created_at, c.updated_at
		FROM issue_spec_typed_comments typed
		JOIN comments c ON c.organization_id = typed.organization_id
			AND c.repository_id = typed.repository_id AND c.id = typed.comment_id
		JOIN issues i ON i.organization_id = c.organization_id
			AND i.repository_id = c.repository_id AND i.id = c.issue_id
		LEFT JOIN users u ON u.id = c.author_id
		WHERE typed.organization_id = $1 AND typed.repository_id = $2
			AND i.number = $3 AND typed.comment_type = 'ANSWER'
		ORDER BY c.created_at, c.compatibility_id`, s.scope.OrgID, s.scope.RepoID, issueNumber)
	if err != nil {
		return nil, fmt.Errorf("load typed answer observations: %w", mapError(err))
	}
	defer rows.Close()
	result := make([]TypedAnswerObservation, 0)
	for rows.Next() {
		var item TypedAnswerObservation
		if err := rows.Scan(&item.CompatibilityID, &item.ActorLogin, &item.Body,
			&item.RepresentationVersion, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan typed answer observation: %w", mapError(err))
		}
		result = append(result, item)
	}
	return result, rows.Err()
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
