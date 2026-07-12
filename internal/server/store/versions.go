package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/jackc/pgx/v5"
)

type RepoCollection string

const (
	RepoCollectionIssues        RepoCollection = "issues"
	RepoCollectionComments      RepoCollection = "comments"
	RepoCollectionLabels        RepoCollection = "labels"
	RepoCollectionArtifacts     RepoCollection = "artifacts"
	RepoCollectionWebhooks      RepoCollection = "webhooks"
	RepoCollectionReactions     RepoCollection = "reactions"
	RepoCollectionBindings      RepoCollection = "bindings"
	RepoCollectionReferences    RepoCollection = "references"
	RepoCollectionEvidence      RepoCollection = "evidence"
	RepoCollectionCollaborators RepoCollection = "collaborators"
	RepoCollectionSubscriptions RepoCollection = "subscriptions"
)

// BumpCollectionVersion is a compare-and-swap primitive for collection ETags.
func (s RepoStore) BumpCollectionVersion(ctx context.Context, collection RepoCollection, expected int64) (int64, error) {
	if err := s.validate(); err != nil {
		return 0, err
	}
	column, ok := collectionColumn(collection)
	if !ok {
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

// IncrementCollectionVersions atomically advances each named repository
// collection and returns the resulting values in the same order. It is used
// inside mutation transactions where a caller does not have an external CAS
// token but still needs lossless concurrent invalidation.
func (s RepoStore) IncrementCollectionVersions(ctx context.Context, collections ...RepoCollection) ([]int64, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	if len(collections) == 0 {
		return nil, fmt.Errorf("%w: at least one collection is required", ErrInvalidInput)
	}
	columns := make([]string, len(collections))
	seen := map[string]struct{}{}
	for index, collection := range collections {
		var ok bool
		columns[index], ok = collectionColumn(collection)
		if !ok {
			return nil, fmt.Errorf("%w: unknown repository collection %q", ErrInvalidInput, collection)
		}
		if _, exists := seen[columns[index]]; exists {
			return nil, fmt.Errorf("%w: duplicate repository collection %q", ErrInvalidInput, collection)
		}
		seen[columns[index]] = struct{}{}
	}
	assignments := make([]string, len(columns))
	for index, column := range columns {
		assignments[index] = column + " = " + column + " + 1"
	}
	query := `UPDATE repos SET ` + strings.Join(assignments, ", ") + `, updated_at = clock_timestamp()
		WHERE organization_id = $1 AND id = $2 RETURNING ` + strings.Join(columns, ", ")
	values := make([]int64, len(columns))
	destinations := make([]any, len(values))
	for index := range values {
		destinations[index] = &values[index]
	}
	if err := s.db.QueryRow(ctx, query, s.scope.OrgID, s.scope.RepoID).Scan(destinations...); err != nil {
		return nil, fmt.Errorf("increment repository collections: %w", mapError(err))
	}
	return values, nil
}

func collectionColumn(collection RepoCollection) (string, bool) {
	switch collection {
	case RepoCollectionIssues:
		return "issues_collection_version", true
	case RepoCollectionComments:
		return "comments_collection_version", true
	case RepoCollectionLabels:
		return "labels_collection_version", true
	case RepoCollectionArtifacts:
		return "artifacts_collection_version", true
	case RepoCollectionWebhooks:
		return "webhooks_collection_version", true
	case RepoCollectionReactions:
		return "reactions_collection_version", true
	case RepoCollectionBindings:
		return "bindings_collection_version", true
	case RepoCollectionReferences:
		return "references_collection_version", true
	case RepoCollectionEvidence:
		return "evidence_collection_version", true
	case RepoCollectionCollaborators:
		return "collaborators_collection_version", true
	case RepoCollectionSubscriptions:
		return "subscriptions_collection_version", true
	default:
		return "", false
	}
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
	if !s.inTx {
		if s.root == nil {
			return models.Issue{}, errors.New("store: UpdateIssueCAS requires a store- or transaction-backed repository scope")
		}
		var updated models.Issue
		err := s.root.WithinTx(ctx, func(tx *Tx) error {
			var err error
			updated, err = tx.ScopedRepo(s.scope).UpdateIssueCAS(ctx, number, expected, input)
			return err
		})
		return updated, err
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
