package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

var ErrTypedCommentIDSequenceExhausted = errors.New("store: typed comment ID sequence exhausted")

// AllocateIssueScopedTypedCommentID returns the first available canonical ID
// for one Issue and type. The owning Issue row is the allocation lock, so the
// caller must use a transaction and create the projection before committing.
func (s RepoStore) AllocateIssueScopedTypedCommentID(ctx context.Context, issueNumber int64,
	commentType string) (string, error) {
	commentType = strings.ToUpper(strings.TrimSpace(commentType))
	if err := s.validate(); err != nil || !s.inTx || issueNumber <= 0 || commentType == "" {
		return "", ErrInvalidInput
	}
	for _, char := range commentType {
		if char < 'A' || char > 'Z' {
			return "", ErrInvalidInput
		}
	}

	var issueID uuid.UUID
	if err := s.db.QueryRow(ctx, `SELECT id FROM issues
		WHERE organization_id = $1 AND repository_id = $2 AND number = $3
		FOR UPDATE`, s.scope.OrgID, s.scope.RepoID, issueNumber).Scan(&issueID); err != nil {
		return "", fmt.Errorf("lock issue for typed comment ID allocation: %w", mapError(err))
	}

	prefix := fmt.Sprintf("%s-%d", commentType, issueNumber)
	rows, err := s.db.Query(ctx, `SELECT comment_key FROM issue_spec_typed_comments
		WHERE organization_id = $1 AND repository_id = $2
			AND issue_id = $3 AND comment_type = $4`, s.scope.OrgID, s.scope.RepoID, issueID, commentType)
	if err != nil {
		return "", fmt.Errorf("list typed comment IDs for allocation: %w", mapError(err))
	}
	defer rows.Close()

	used := [1000]bool{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return "", fmt.Errorf("scan typed comment ID for allocation: %w", mapError(err))
		}
		if len(key) != len(prefix)+3 {
			continue
		}
		sequence, err := strconv.Atoi(key[len(prefix):])
		if err == nil && sequence > 0 && sequence < len(used) {
			used[sequence] = true
		}
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("iterate typed comment IDs for allocation: %w", mapError(err))
	}
	for sequence := 1; sequence < len(used); sequence++ {
		if !used[sequence] {
			return fmt.Sprintf("%s%03d", prefix, sequence), nil
		}
	}
	return "", ErrTypedCommentIDSequenceExhausted
}
