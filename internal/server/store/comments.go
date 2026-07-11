package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/jackc/pgx/v5"
)

const commentColumns = `
	id, organization_id, repository_id, issue_id, author_id, body,
	representation_version, reactions_collection_version, created_at, updated_at`

const qualifiedCommentColumns = `
	c.id, c.organization_id, c.repository_id, c.issue_id, c.author_id, c.body,
	c.representation_version, c.reactions_collection_version, c.created_at, c.updated_at`

func (s RepoStore) CreateComment(ctx context.Context, input models.NewComment) (models.CommentSnapshot, error) {
	if err := s.validate(); err != nil || input.IssueNumber <= 0 {
		return models.CommentSnapshot{}, ErrInvalidInput
	}
	if input.ID == uuid.Nil {
		input.ID = uuid.New()
	}
	if !s.inTx {
		if s.root == nil {
			return models.CommentSnapshot{}, errors.New("store: CreateComment requires a store- or transaction-backed repository scope")
		}
		var result models.CommentSnapshot
		err := s.root.WithinTx(ctx, func(tx *Tx) error {
			var err error
			result, err = tx.ScopedRepo(s.scope).CreateComment(ctx, input)
			return err
		})
		return result, err
	}
	var issueID uuid.UUID
	if err := s.db.QueryRow(ctx, `SELECT id FROM issues WHERE organization_id = $1
		AND repository_id = $2 AND number = $3 FOR UPDATE`, s.scope.OrgID, s.scope.RepoID,
		input.IssueNumber).Scan(&issueID); err != nil {
		return models.CommentSnapshot{}, fmt.Errorf("load comment issue: %w", mapError(err))
	}
	row := s.db.QueryRow(ctx, `INSERT INTO comments
		(id, organization_id, repository_id, issue_id, author_id, body)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING `+commentColumns,
		input.ID, s.scope.OrgID, s.scope.RepoID, issueID, input.AuthorID, input.Body)
	comment, err := scanComment(row)
	if err != nil {
		return models.CommentSnapshot{}, fmt.Errorf("insert comment: %w", mapError(err))
	}
	if _, err := s.db.Exec(ctx, `UPDATE issues SET
		comments_collection_version = comments_collection_version + 1,
		updated_at = clock_timestamp()
		WHERE organization_id = $1 AND repository_id = $2 AND id = $3`,
		s.scope.OrgID, s.scope.RepoID, issueID); err != nil {
		return models.CommentSnapshot{}, fmt.Errorf("bump issue comment versions: %w", err)
	}
	if _, err := s.IncrementCollectionVersions(ctx, RepoCollectionIssues, RepoCollectionComments); err != nil {
		return models.CommentSnapshot{}, err
	}
	login, err := s.authorLogin(ctx, comment.AuthorID)
	if err != nil {
		return models.CommentSnapshot{}, err
	}
	return models.CommentSnapshot{Comment: comment, IssueNumber: input.IssueNumber, AuthorLogin: login}, nil
}

func (s RepoStore) CommentByCompatibilityID(ctx context.Context, compatibilityID int64) (models.CommentSnapshot, error) {
	if err := s.validate(); err != nil || compatibilityID <= 0 {
		return models.CommentSnapshot{}, ErrInvalidInput
	}
	row := s.db.QueryRow(ctx, `SELECT `+qualifiedCommentColumns+`, i.number, COALESCE(u.login, 'ghost')
		FROM comments c JOIN issues i ON i.organization_id = c.organization_id
		AND i.repository_id = c.repository_id AND i.id = c.issue_id
		LEFT JOIN users u ON u.id = c.author_id
		WHERE c.organization_id = $1 AND c.repository_id = $2 AND c.compatibility_id = $3`,
		s.scope.OrgID, s.scope.RepoID, compatibilityID)
	result, err := scanCommentSnapshot(row)
	if err != nil {
		return models.CommentSnapshot{}, fmt.Errorf("get comment: %w", mapError(err))
	}
	return result, nil
}

func (s RepoStore) UpdateCommentCAS(ctx context.Context, compatibilityID, expected int64, body string) (models.CommentSnapshot, error) {
	if err := s.validate(); err != nil || compatibilityID <= 0 || expected <= 0 {
		return models.CommentSnapshot{}, ErrInvalidInput
	}
	if !s.inTx {
		if s.root == nil {
			return models.CommentSnapshot{}, errors.New("store: UpdateCommentCAS requires a store- or transaction-backed repository scope")
		}
		var result models.CommentSnapshot
		err := s.root.WithinTx(ctx, func(tx *Tx) error {
			var err error
			result, err = tx.ScopedRepo(s.scope).UpdateCommentCAS(ctx, compatibilityID, expected, body)
			return err
		})
		return result, err
	}
	row := s.db.QueryRow(ctx, `UPDATE comments c SET body = $4,
		representation_version = c.representation_version + 1, updated_at = clock_timestamp()
		FROM issues i WHERE c.organization_id = $1 AND c.repository_id = $2
		AND c.compatibility_id = $3 AND c.representation_version = $5
		AND i.organization_id = c.organization_id AND i.repository_id = c.repository_id AND i.id = c.issue_id
		RETURNING `+qualifiedCommentColumns+`, i.number,
		COALESCE((SELECT u.login FROM users u WHERE u.id = c.author_id), 'ghost')`,
		s.scope.OrgID, s.scope.RepoID, compatibilityID, body, expected)
	result, err := scanCommentSnapshot(row)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return models.CommentSnapshot{}, fmt.Errorf("update comment: %w", mapError(err))
		}
		var exists bool
		var version int64
		err = s.db.QueryRow(ctx, `SELECT true, representation_version FROM comments
			WHERE organization_id = $1 AND repository_id = $2 AND compatibility_id = $3`,
			s.scope.OrgID, s.scope.RepoID, compatibilityID).Scan(&exists, &version)
		if errors.Is(err, pgx.ErrNoRows) {
			return models.CommentSnapshot{}, ErrNotFound
		}
		if err != nil {
			return models.CommentSnapshot{}, err
		}
		return models.CommentSnapshot{}, ErrVersionConflict
	}
	if _, err := s.db.Exec(ctx, `UPDATE issues SET comments_collection_version = comments_collection_version + 1,
		updated_at = clock_timestamp() WHERE organization_id = $1 AND repository_id = $2 AND id = $3`,
		s.scope.OrgID, s.scope.RepoID, result.Comment.IssueID); err != nil {
		return models.CommentSnapshot{}, err
	}
	if _, err := s.IncrementCollectionVersions(ctx, RepoCollectionIssues, RepoCollectionComments); err != nil {
		return models.CommentSnapshot{}, err
	}
	return result, nil
}

func (s RepoStore) ListComments(ctx context.Context, options models.CommentListOptions) (models.CommentPage, error) {
	if err := s.validate(); err != nil || options.Page < 1 || options.PerPage < 1 {
		return models.CommentPage{}, ErrInvalidInput
	}
	clauses := []string{"c.organization_id = $1", "c.repository_id = $2"}
	args := []any{s.scope.OrgID, s.scope.RepoID}
	if options.IssueNumber != nil {
		if *options.IssueNumber <= 0 {
			return models.CommentPage{}, ErrInvalidInput
		}
		args = append(args, *options.IssueNumber)
		clauses = append(clauses, fmt.Sprintf("i.number = $%d", len(args)))
	}
	if options.Since != nil {
		args = append(args, options.Since.UTC())
		clauses = append(clauses, fmt.Sprintf("c.updated_at >= $%d", len(args)))
	}
	where := strings.Join(clauses, " AND ")
	var page models.CommentPage
	if err := s.db.QueryRow(ctx, `SELECT count(*), COALESCE(max(c.updated_at), to_timestamp(0))
		FROM comments c JOIN issues i ON i.organization_id = c.organization_id
		AND i.repository_id = c.repository_id AND i.id = c.issue_id WHERE `+where, args...).
		Scan(&page.Total, &page.LastModified); err != nil {
		return models.CommentPage{}, fmt.Errorf("count comments: %w", err)
	}
	args = append(args, options.PerPage, (options.Page-1)*options.PerPage)
	rows, err := s.db.Query(ctx, `SELECT `+qualifiedCommentColumns+`, i.number, COALESCE(u.login, 'ghost')
		FROM comments c JOIN issues i ON i.organization_id = c.organization_id
		AND i.repository_id = c.repository_id AND i.id = c.issue_id
		LEFT JOIN users u ON u.id = c.author_id WHERE `+where+
		` ORDER BY c.created_at, c.id LIMIT $`+fmt.Sprint(len(args)-1)+` OFFSET $`+fmt.Sprint(len(args)), args...)
	if err != nil {
		return models.CommentPage{}, fmt.Errorf("list comments: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		item, err := scanCommentSnapshot(rows)
		if err != nil {
			return models.CommentPage{}, err
		}
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		return models.CommentPage{}, err
	}
	if options.IssueNumber != nil {
		if err := s.db.QueryRow(ctx, `SELECT comments_collection_version FROM issues
			WHERE organization_id = $1 AND repository_id = $2 AND number = $3`,
			s.scope.OrgID, s.scope.RepoID, *options.IssueNumber).Scan(&page.CollectionVersion); err != nil {
			return models.CommentPage{}, fmt.Errorf("load issue comments version: %w", mapError(err))
		}
	} else if err := s.db.QueryRow(ctx, `SELECT comments_collection_version FROM repos
		WHERE organization_id = $1 AND id = $2`, s.scope.OrgID, s.scope.RepoID).
		Scan(&page.CollectionVersion); err != nil {
		return models.CommentPage{}, fmt.Errorf("load repository comments version: %w", mapError(err))
	}
	return page, nil
}

func (s RepoStore) authorLogin(ctx context.Context, userID *uuid.UUID) (string, error) {
	if userID == nil {
		return "ghost", nil
	}
	var login string
	if err := s.db.QueryRow(ctx, `SELECT login FROM users WHERE id = $1`, *userID).Scan(&login); err != nil {
		return "", fmt.Errorf("load author login: %w", mapError(err))
	}
	return login, nil
}

func scanComment(row rowScanner) (models.Comment, error) {
	var comment models.Comment
	err := row.Scan(&comment.ID, &comment.Scope.OrgID, &comment.Scope.RepoID, &comment.IssueID,
		&comment.AuthorID, &comment.Body, &comment.RepresentationVersion,
		&comment.ReactionsCollectionVersion, &comment.CreatedAt, &comment.UpdatedAt)
	return comment, err
}

func scanCommentSnapshot(row rowScanner) (models.CommentSnapshot, error) {
	var result models.CommentSnapshot
	err := row.Scan(&result.Comment.ID, &result.Comment.Scope.OrgID, &result.Comment.Scope.RepoID,
		&result.Comment.IssueID, &result.Comment.AuthorID, &result.Comment.Body,
		&result.Comment.RepresentationVersion, &result.Comment.ReactionsCollectionVersion,
		&result.Comment.CreatedAt, &result.Comment.UpdatedAt, &result.IssueNumber, &result.AuthorLogin)
	return result, err
}
