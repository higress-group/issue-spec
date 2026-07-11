package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/models"
)

const issueColumns = `
	id, organization_id, repository_id, number, author_id, title, body, state,
	representation_version, comments_collection_version, labels_collection_version,
	bindings_collection_version, references_collection_version, evidence_collection_version,
	created_at, updated_at, closed_at`

// AllocateIssueNumber atomically advances the repository-local sequence. When
// called through Tx.Repo it participates in the caller's transaction.
func (s RepoStore) AllocateIssueNumber(ctx context.Context) (int64, error) {
	if err := s.validate(); err != nil {
		return 0, err
	}
	var number int64
	err := s.db.QueryRow(ctx, `
		UPDATE repos
		SET next_issue_number = next_issue_number + 1
		WHERE organization_id = $1 AND id = $2
		RETURNING next_issue_number - 1`, s.scope.OrgID, s.scope.RepoID).Scan(&number)
	if err != nil {
		return 0, fmt.Errorf("allocate issue number: %w", mapError(err))
	}
	return number, nil
}

// CreateIssue allocates the per-repository number and inserts the issue in one
// transaction. A caller already in a transaction keeps that same transaction.
func (s RepoStore) CreateIssue(ctx context.Context, input models.NewIssue) (models.Issue, error) {
	if err := s.validate(); err != nil {
		return models.Issue{}, err
	}
	if input.ID == uuid.Nil {
		input.ID = uuid.New()
	}
	if s.inTx {
		return s.createIssue(ctx, input)
	}
	if s.root == nil {
		return models.Issue{}, errors.New("store: CreateIssue requires a store- or transaction-backed repository scope")
	}
	var issue models.Issue
	err := s.root.WithinTx(ctx, func(tx *Tx) error {
		var err error
		issue, err = tx.ScopedRepo(s.scope).createIssue(ctx, input)
		return err
	})
	return issue, err
}

func (s RepoStore) createIssue(ctx context.Context, input models.NewIssue) (models.Issue, error) {
	number, err := s.AllocateIssueNumber(ctx)
	if err != nil {
		return models.Issue{}, err
	}
	row := s.db.QueryRow(ctx, `
		INSERT INTO issues (
			id, organization_id, repository_id, number, author_id, title, body
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING `+issueColumns,
		input.ID, s.scope.OrgID, s.scope.RepoID, number, input.AuthorID, input.Title, input.Body)
	issue, err := scanIssue(row)
	if err != nil {
		return models.Issue{}, fmt.Errorf("insert issue: %w", mapError(err))
	}
	return issue, nil
}

func (s RepoStore) IssueByNumber(ctx context.Context, number int64) (models.Issue, error) {
	if err := s.validate(); err != nil {
		return models.Issue{}, err
	}
	row := s.db.QueryRow(ctx, `SELECT `+issueColumns+`
		FROM issues
		WHERE organization_id = $1 AND repository_id = $2 AND number = $3`,
		s.scope.OrgID, s.scope.RepoID, number)
	issue, err := scanIssue(row)
	if err != nil {
		return models.Issue{}, fmt.Errorf("get issue %d: %w", number, mapError(err))
	}
	return issue, nil
}

type rowScanner interface {
	Scan(...any) error
}

func scanIssue(row rowScanner) (models.Issue, error) {
	var issue models.Issue
	err := row.Scan(
		&issue.ID,
		&issue.Scope.OrgID,
		&issue.Scope.RepoID,
		&issue.Number,
		&issue.AuthorID,
		&issue.Title,
		&issue.Body,
		&issue.State,
		&issue.RepresentationVersion,
		&issue.CommentsCollectionVersion,
		&issue.LabelsCollectionVersion,
		&issue.BindingsCollectionVersion,
		&issue.ReferencesCollectionVersion,
		&issue.EvidenceCollectionVersion,
		&issue.CreatedAt,
		&issue.UpdatedAt,
		&issue.ClosedAt,
	)
	return issue, err
}
