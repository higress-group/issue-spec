package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/jackc/pgx/v5"
)

const reactionColumns = `cr.id, cr.compatibility_id, cr.organization_id, cr.repository_id,
	cr.issue_id, cr.comment_id, cr.user_id, COALESCE(u.login, 'ghost'),
	cr.identity_key, cr.reaction_key, cr.created_at`

func (s RepoStore) ListCommentReactions(ctx context.Context, commentCompatibilityID int64, page, perPage int) (models.ReactionPage, error) {
	if err := s.validate(); err != nil || commentCompatibilityID <= 0 || page < 1 || perPage < 1 {
		return models.ReactionPage{}, ErrInvalidInput
	}
	var commentID uuid.UUID
	var result models.ReactionPage
	if err := s.db.QueryRow(ctx, `SELECT id, reactions_collection_version, updated_at FROM comments
		WHERE organization_id = $1 AND repository_id = $2 AND compatibility_id = $3`,
		s.scope.OrgID, s.scope.RepoID, commentCompatibilityID).
		Scan(&commentID, &result.CollectionVersion, &result.LastModified); err != nil {
		return models.ReactionPage{}, fmt.Errorf("load reaction comment: %w", mapError(err))
	}
	result.CommentID = commentID
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM comment_reactions WHERE organization_id = $1
		AND repository_id = $2 AND comment_id = $3`, s.scope.OrgID, s.scope.RepoID, commentID).
		Scan(&result.Total); err != nil {
		return models.ReactionPage{}, fmt.Errorf("count reactions: %w", err)
	}
	rows, err := s.db.Query(ctx, `SELECT `+reactionColumns+` FROM comment_reactions cr
		LEFT JOIN users u ON u.id = cr.user_id WHERE cr.organization_id = $1
		AND cr.repository_id = $2 AND cr.comment_id = $3
		ORDER BY cr.created_at, cr.id LIMIT $4 OFFSET $5`, s.scope.OrgID, s.scope.RepoID,
		commentID, perPage, (page-1)*perPage)
	if err != nil {
		return models.ReactionPage{}, fmt.Errorf("list reactions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		reaction, err := scanReaction(rows)
		if err != nil {
			return models.ReactionPage{}, err
		}
		result.Items = append(result.Items, reaction)
	}
	return result, rows.Err()
}

func (s RepoStore) AddCommentReaction(ctx context.Context, commentCompatibilityID int64, userID uuid.UUID, content string) (models.ReactionMutation, error) {
	if err := s.requireMutationTx(); err != nil || commentCompatibilityID <= 0 || userID == uuid.Nil {
		return models.ReactionMutation{}, ErrInvalidInput
	}
	comment, err := s.CommentByCompatibilityID(ctx, commentCompatibilityID)
	if err != nil {
		return models.ReactionMutation{}, err
	}
	identityKey := "user:" + userID.String()
	row := s.db.QueryRow(ctx, `INSERT INTO comment_reactions
		(id, organization_id, repository_id, issue_id, comment_id, user_id, identity_key, reaction_key)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (organization_id, repository_id, comment_id, identity_key, reaction_key)
		DO NOTHING RETURNING id`, uuid.New(), s.scope.OrgID, s.scope.RepoID,
		comment.Comment.IssueID, comment.Comment.ID, userID, identityKey, content)
	var insertedID uuid.UUID
	err = row.Scan(&insertedID)
	created := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return models.ReactionMutation{}, fmt.Errorf("create reaction: %w", mapError(err))
	}
	var reaction models.CommentReaction
	if created {
		reaction, err = s.reactionByID(ctx, insertedID, false)
	} else {
		err = s.db.QueryRow(ctx, `SELECT `+reactionColumns+` FROM comment_reactions cr
			LEFT JOIN users u ON u.id = cr.user_id WHERE cr.organization_id = $1
			AND cr.repository_id = $2 AND cr.comment_id = $3 AND cr.identity_key = $4
			AND cr.reaction_key = $5`, s.scope.OrgID, s.scope.RepoID, comment.Comment.ID,
			identityKey, content).Scan(reactionDestinations(&reaction)...)
	}
	if err != nil {
		return models.ReactionMutation{}, fmt.Errorf("load reaction: %w", mapError(err))
	}
	if created {
		comment, err = s.bumpCommentReactionVersions(ctx, comment.Comment.ID)
		if err != nil {
			return models.ReactionMutation{}, err
		}
	}
	comment.Reactions, err = s.ReactionSummary(ctx, comment.Comment.ID)
	return models.ReactionMutation{Reaction: reaction, Comment: comment, Created: created}, err
}

func (s RepoStore) ReactionByCompatibilityID(ctx context.Context, compatibilityID int64, forUpdate bool) (models.CommentReaction, error) {
	if err := s.validate(); err != nil || compatibilityID <= 0 {
		return models.CommentReaction{}, ErrInvalidInput
	}
	var id uuid.UUID
	if err := s.db.QueryRow(ctx, `SELECT id FROM comment_reactions WHERE organization_id = $1
		AND repository_id = $2 AND compatibility_id = $3`, s.scope.OrgID, s.scope.RepoID, compatibilityID).Scan(&id); err != nil {
		return models.CommentReaction{}, fmt.Errorf("resolve reaction: %w", mapError(err))
	}
	return s.reactionByID(ctx, id, forUpdate)
}

func (s RepoStore) DeleteCommentReaction(ctx context.Context, compatibilityID int64) (models.CommentSnapshot, error) {
	if err := s.requireMutationTx(); err != nil || compatibilityID <= 0 {
		return models.CommentSnapshot{}, ErrInvalidInput
	}
	reaction, err := s.ReactionByCompatibilityID(ctx, compatibilityID, true)
	if err != nil {
		return models.CommentSnapshot{}, err
	}
	tag, err := s.db.Exec(ctx, `DELETE FROM comment_reactions WHERE organization_id = $1
		AND repository_id = $2 AND id = $3`, s.scope.OrgID, s.scope.RepoID, reaction.ID)
	if err != nil {
		return models.CommentSnapshot{}, fmt.Errorf("delete reaction: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return models.CommentSnapshot{}, ErrNotFound
	}
	comment, err := s.bumpCommentReactionVersions(ctx, reaction.CommentID)
	if err != nil {
		return models.CommentSnapshot{}, err
	}
	comment.Reactions, err = s.ReactionSummary(ctx, reaction.CommentID)
	return comment, err
}

func (s RepoStore) reactionByID(ctx context.Context, id uuid.UUID, forUpdate bool) (models.CommentReaction, error) {
	query := `SELECT ` + reactionColumns + ` FROM comment_reactions cr
		LEFT JOIN users u ON u.id = cr.user_id WHERE cr.organization_id = $1
		AND cr.repository_id = $2 AND cr.id = $3`
	if forUpdate {
		query += ` FOR UPDATE OF cr`
	}
	var reaction models.CommentReaction
	if err := s.db.QueryRow(ctx, query, s.scope.OrgID, s.scope.RepoID, id).
		Scan(reactionDestinations(&reaction)...); err != nil {
		return models.CommentReaction{}, fmt.Errorf("get reaction: %w", mapError(err))
	}
	return reaction, nil
}

func (s RepoStore) bumpCommentReactionVersions(ctx context.Context, commentID uuid.UUID) (models.CommentSnapshot, error) {
	row := s.db.QueryRow(ctx, `UPDATE comments c SET
		representation_version = c.representation_version + 1,
		reactions_collection_version = c.reactions_collection_version + 1,
		updated_at = clock_timestamp()
		FROM issues i WHERE c.organization_id = $1 AND c.repository_id = $2 AND c.id = $3
		AND i.organization_id = c.organization_id AND i.repository_id = c.repository_id AND i.id = c.issue_id
		RETURNING `+qualifiedCommentColumns+`, i.number,
		COALESCE((SELECT u.login FROM users u WHERE u.id = c.author_id), 'ghost'),
		COALESCE((SELECT COALESCE(u.nickname, u.display_name, u.login) FROM users u WHERE u.id = c.author_id), 'ghost')`,
		s.scope.OrgID, s.scope.RepoID, commentID)
	comment, err := scanCommentSnapshot(row)
	if err != nil {
		return models.CommentSnapshot{}, fmt.Errorf("bump comment reaction versions: %w", mapError(err))
	}
	if _, err := s.db.Exec(ctx, `UPDATE issues SET comments_collection_version = comments_collection_version + 1,
		updated_at = clock_timestamp() WHERE organization_id = $1 AND repository_id = $2 AND id = $3`,
		s.scope.OrgID, s.scope.RepoID, comment.Comment.IssueID); err != nil {
		return models.CommentSnapshot{}, err
	}
	if _, err := s.IncrementCollectionVersions(ctx, RepoCollectionIssues, RepoCollectionComments, RepoCollectionReactions); err != nil {
		return models.CommentSnapshot{}, err
	}
	return comment, nil
}

func (s RepoStore) PopulateCommentReactionSummaries(ctx context.Context, comments []models.CommentSnapshot) error {
	if len(comments) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(comments))
	indexes := make(map[uuid.UUID]int, len(comments))
	for index := range comments {
		ids[index] = comments[index].Comment.ID
		indexes[ids[index]] = index
	}
	rows, err := s.db.Query(ctx, reactionSummaryQuery+` AND comment_id = ANY($3::uuid[])
		GROUP BY comment_id`, s.scope.OrgID, s.scope.RepoID, ids)
	if err != nil {
		return fmt.Errorf("load reaction summaries: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var summary models.ReactionSummary
		destinations := append([]any{&id}, reactionSummaryDestinations(&summary)...)
		if err := rows.Scan(destinations...); err != nil {
			return err
		}
		if index, ok := indexes[id]; ok {
			comments[index].Reactions = summary
		}
	}
	return rows.Err()
}

func (s RepoStore) ReactionSummary(ctx context.Context, commentID uuid.UUID) (models.ReactionSummary, error) {
	var summary models.ReactionSummary
	err := s.db.QueryRow(ctx, reactionSummaryQuery+` AND comment_id = $3 GROUP BY comment_id`,
		s.scope.OrgID, s.scope.RepoID, commentID).Scan(append([]any{new(uuid.UUID)}, reactionSummaryDestinations(&summary)...)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return summary, nil
	}
	return summary, err
}

const reactionSummaryQuery = `SELECT comment_id, count(*),
	count(*) FILTER (WHERE reaction_key = '+1'), count(*) FILTER (WHERE reaction_key = '-1'),
	count(*) FILTER (WHERE reaction_key = 'laugh'), count(*) FILTER (WHERE reaction_key = 'hooray'),
	count(*) FILTER (WHERE reaction_key = 'confused'), count(*) FILTER (WHERE reaction_key = 'heart'),
	count(*) FILTER (WHERE reaction_key = 'rocket'), count(*) FILTER (WHERE reaction_key = 'eyes')
	FROM comment_reactions WHERE organization_id = $1 AND repository_id = $2`

func reactionSummaryDestinations(summary *models.ReactionSummary) []any {
	return []any{&summary.TotalCount, &summary.PlusOne, &summary.MinusOne, &summary.Laugh,
		&summary.Hooray, &summary.Confused, &summary.Heart, &summary.Rocket, &summary.Eyes}
}

func scanReaction(row rowScanner) (models.CommentReaction, error) {
	var reaction models.CommentReaction
	err := row.Scan(reactionDestinations(&reaction)...)
	return reaction, err
}

func reactionDestinations(reaction *models.CommentReaction) []any {
	return []any{&reaction.ID, &reaction.CompatibilityID, &reaction.Scope.OrgID,
		&reaction.Scope.RepoID, &reaction.IssueID, &reaction.CommentID, &reaction.UserID,
		&reaction.AuthorLogin, &reaction.IdentityKey, &reaction.ReactionKey, &reaction.CreatedAt}
}
