// Package mentionmail reloads mention recipients and repository authority at
// send time and renders the bounded plain-text mention message.
package mentionmail

import (
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/models"
)

const (
	SnapshotVersion = 1
	MaxExcerptRunes = 500
)

var ErrInvalid = errors.New("mention mail: invalid input")

// Snapshot contains no recipient address and is safe to persist in the shared
// delivery queue. Excerpt is bounded before construction.
type Snapshot struct {
	Version          int       `json:"version"`
	ActorLogin       string    `json:"actor_login"`
	ActorDisplayName string    `json:"actor_display_name"`
	Organization     string    `json:"organization"`
	Repository       string    `json:"repository"`
	IssueNumber      int64     `json:"issue_number"`
	IssueTitle       string    `json:"issue_title"`
	CommentID        uuid.UUID `json:"comment_id"`
	CommentNumericID int64     `json:"comment_numeric_id"`
	Excerpt          string    `json:"excerpt"`
	OccurredAt       time.Time `json:"occurred_at"`
}

func (s Snapshot) Validate(scope models.RepoScope, commentID uuid.UUID) error {
	if err := scope.Validate(); err != nil || s.Version != SnapshotVersion || commentID == uuid.Nil ||
		s.CommentID != commentID || strings.TrimSpace(s.ActorLogin) == "" ||
		strings.TrimSpace(s.Organization) == "" || strings.TrimSpace(s.Repository) == "" ||
		s.IssueNumber <= 0 || s.CommentNumericID <= 0 || strings.TrimSpace(s.IssueTitle) == "" ||
		s.OccurredAt.IsZero() || len([]rune(s.Excerpt)) > MaxExcerptRunes {
		return ErrInvalid
	}
	return nil
}
