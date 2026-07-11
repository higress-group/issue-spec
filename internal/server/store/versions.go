package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/jackc/pgx/v5"
)

type RepoCollection string

const (
	RepoCollectionIssues    RepoCollection = "issues"
	RepoCollectionLabels    RepoCollection = "labels"
	RepoCollectionArtifacts RepoCollection = "artifacts"
	RepoCollectionWebhooks  RepoCollection = "webhooks"
)

// BumpCollectionVersion is a compare-and-swap primitive for collection ETags.
func (s RepoStore) BumpCollectionVersion(ctx context.Context, collection RepoCollection, expected int64) (int64, error) {
	if err := s.validate(); err != nil {
		return 0, err
	}
	var column string
	switch collection {
	case RepoCollectionIssues:
		column = "issues_collection_version"
	case RepoCollectionLabels:
		column = "labels_collection_version"
	case RepoCollectionArtifacts:
		column = "artifacts_collection_version"
	case RepoCollectionWebhooks:
		column = "webhooks_collection_version"
	default:
		return 0, fmt.Errorf("%w: unknown repository collection %q", ErrInvalidInput, collection)
	}
	var version int64
	err := s.db.QueryRow(ctx, `UPDATE repos SET `+column+` = `+column+` + 1, updated_at = clock_timestamp()
		WHERE organization_id = $1 AND id = $2 AND `+column+` = $3
		RETURNING `+column, s.scope.OrgID, s.scope.RepoID, expected).Scan(&version)
	if err == nil {
		return version, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("bump repository %s version: %w", collection, mapError(err))
	}
	return 0, s.classifyRepoCASMiss(ctx)
}

// UpdateIssueCAS changes mutable issue fields only when the representation
// version matches, then advances that version exactly once.
func (s RepoStore) UpdateIssueCAS(ctx context.Context, number, expected int64, input models.IssueUpdate) (models.Issue, error) {
	if err := s.validate(); err != nil {
		return models.Issue{}, err
	}
	if number <= 0 || expected <= 0 || (input.State != models.IssueStateOpen && input.State != models.IssueStateClosed) {
		return models.Issue{}, fmt.Errorf("%w: positive issue number/version and valid state are required", ErrInvalidInput)
	}
	row := s.db.QueryRow(ctx, `UPDATE issues
		SET title = $4,
			body = $5,
			state = $6,
			closed_at = CASE WHEN $6 = 'closed' THEN COALESCE(closed_at, clock_timestamp()) ELSE NULL END,
			representation_version = representation_version + 1,
			updated_at = clock_timestamp()
		WHERE organization_id = $1 AND repository_id = $2 AND number = $3
			AND representation_version = $7
		RETURNING `+issueColumns,
		s.scope.OrgID, s.scope.RepoID, number, input.Title, input.Body, input.State, expected)
	issue, err := scanIssue(row)
	if err == nil {
		return issue, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return models.Issue{}, fmt.Errorf("update issue %d: %w", number, mapError(err))
	}
	var exists bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM issues WHERE organization_id = $1 AND repository_id = $2 AND number = $3
	)`, s.scope.OrgID, s.scope.RepoID, number).Scan(&exists); err != nil {
		return models.Issue{}, fmt.Errorf("classify issue update: %w", err)
	}
	if !exists {
		return models.Issue{}, ErrNotFound
	}
	return models.Issue{}, ErrVersionConflict
}

func (s RepoStore) classifyRepoCASMiss(ctx context.Context) error {
	var exists bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM repos WHERE organization_id = $1 AND id = $2
	)`, s.scope.OrgID, s.scope.RepoID).Scan(&exists); err != nil {
		return fmt.Errorf("classify repository version update: %w", err)
	}
	if !exists {
		return ErrNotFound
	}
	return ErrVersionConflict
}
