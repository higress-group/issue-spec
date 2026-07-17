package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const MaxMentionCandidates = 10

var ErrMentionCallerIneligible = errors.New("store: mention caller is not an active human")

// MentionCandidate is the complete public directory projection. Email,
// membership and repository authority deliberately never cross this boundary.
type MentionCandidate struct {
	Login       string `json:"login"`
	DisplayName string `json:"display_name"`
}

// MentionIdentity is an exact immutable-login resolution. The boolean says
// only whether a verified notification binding exists; the private address is
// never exposed to mention projection or its snapshots.
type MentionIdentity struct {
	UserID               uuid.UUID
	Login                string
	DisplayName          string
	NotificationEligible bool
}

type MentionCommentContext struct {
	CommentID             uuid.UUID
	IssueID               uuid.UUID
	Body                  string
	RepresentationVersion int64
	CompatibilityID       int64
	OccurredAt            time.Time
	IssueNumber           int64
	IssueTitle            string
	Organization          string
	Repository            string
	ActorLogin            string
	ActorDisplayName      string
}

type MentionSyncInput struct {
	CommentID             uuid.UUID
	IssueID               uuid.UUID
	RepresentationVersion int64
	MentionedUserIDs      []uuid.UUID
}

// MentionCandidates performs one bounded, site-wide prefix lookup for an
// authenticated active human. Candidate authority for the current repository
// is intentionally irrelevant to discovery.
func (s *Store) MentionCandidates(ctx context.Context, callerID uuid.UUID, prefix string, limit int) ([]MentionCandidate, error) {
	prefix = strings.TrimSpace(prefix)
	if s == nil || s.pool == nil || callerID == uuid.Nil || prefix == "" || !utf8.ValidString(prefix) ||
		utf8.RuneCountInString(prefix) > 64 || limit <= 0 || limit > MaxMentionCandidates {
		return nil, ErrInvalidInput
	}
	var eligible bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM users caller WHERE caller.id = $1 AND caller.status = 'active'
		AND NOT EXISTS (SELECT 1 FROM service_accounts account WHERE account.user_id = caller.id)
	)`, callerID).Scan(&eligible); err != nil {
		return nil, fmt.Errorf("mention candidates: validate caller: %w", err)
	}
	if !eligible {
		return nil, ErrMentionCallerIneligible
	}
	likePrefix := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(prefix) + "%"
	rows, err := s.pool.Query(ctx, `SELECT candidate.login,
		COALESCE(candidate.nickname, candidate.display_name, candidate.login)
		FROM users candidate
		WHERE candidate.status = 'active'
		AND NOT EXISTS (SELECT 1 FROM service_accounts account WHERE account.user_id = candidate.id)
		AND (candidate.login_key LIKE lower($2) ESCAPE '\'
			OR lower(COALESCE(candidate.nickname, candidate.display_name, candidate.login)) LIKE lower($2) ESCAPE '\')
		ORDER BY CASE
			WHEN candidate.login_key = lower($1) THEN 0
			WHEN candidate.login_key LIKE lower($2) ESCAPE '\' THEN 1
			ELSE 2 END,
			candidate.login_key, candidate.id
		LIMIT $3`, prefix, likePrefix, limit)
	if err != nil {
		return nil, fmt.Errorf("mention candidates: search: %w", err)
	}
	defer rows.Close()
	result := make([]MentionCandidate, 0, limit)
	for rows.Next() {
		var candidate MentionCandidate
		if err := rows.Scan(&candidate.Login, &candidate.DisplayName); err != nil {
			return nil, fmt.Errorf("mention candidates: scan: %w", err)
		}
		result = append(result, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mention candidates: iterate: %w", err)
	}
	return result, nil
}

// MentionContext reloads transaction-authoritative comment, issue, repository
// and actor fields used by the projection and bounded delivery snapshot.
func (s RepoStore) MentionContext(ctx context.Context, commentID, actorID uuid.UUID) (MentionCommentContext, error) {
	if err := s.validate(); err != nil || commentID == uuid.Nil || actorID == uuid.Nil {
		return MentionCommentContext{}, ErrInvalidInput
	}
	var result MentionCommentContext
	err := s.db.QueryRow(ctx, `SELECT c.id, c.issue_id, c.body, c.representation_version,
		c.compatibility_id, c.updated_at, i.number, i.title, o.name, r.name,
		actor.login, COALESCE(actor.nickname, actor.display_name, actor.login)
		FROM comments c
		JOIN issues i ON i.organization_id = c.organization_id
			AND i.repository_id = c.repository_id AND i.id = c.issue_id
		JOIN repos r ON r.organization_id = c.organization_id AND r.id = c.repository_id
		JOIN orgs o ON o.id = c.organization_id
		JOIN users actor ON actor.id = $4
		WHERE c.organization_id = $1 AND c.repository_id = $2 AND c.id = $3`,
		s.scope.OrgID, s.scope.RepoID, commentID, actorID).Scan(
		&result.CommentID, &result.IssueID, &result.Body, &result.RepresentationVersion,
		&result.CompatibilityID, &result.OccurredAt, &result.IssueNumber, &result.IssueTitle,
		&result.Organization, &result.Repository, &result.ActorLogin, &result.ActorDisplayName)
	if err != nil {
		return MentionCommentContext{}, fmt.Errorf("mention context: %w", mapError(err))
	}
	return result, nil
}

// ResolveMentionIdentities performs exact immutable-login resolution, locks
// the selected active-human user rows for the mutation, and returns no private
// notification address.
func (s RepoStore) ResolveMentionIdentities(ctx context.Context, logins []string) ([]MentionIdentity, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	if len(logins) == 0 {
		return []MentionIdentity{}, nil
	}
	rows, err := s.db.Query(ctx, `SELECT u.id, u.login,
		COALESCE(u.nickname, u.display_name, u.login),
		(u.notification_email IS NOT NULL AND u.notification_email_verified_at IS NOT NULL)
		FROM users u
		WHERE u.login_key = ANY($1::text[]) AND u.status = 'active'
		AND NOT EXISTS (SELECT 1 FROM service_accounts account WHERE account.user_id = u.id)
		ORDER BY u.login_key, u.id FOR SHARE OF u`, logins)
	if err != nil {
		return nil, fmt.Errorf("resolve mention identities: %w", err)
	}
	defer rows.Close()
	result := make([]MentionIdentity, 0, len(logins))
	for rows.Next() {
		var identity MentionIdentity
		if err := rows.Scan(&identity.UserID, &identity.Login, &identity.DisplayName,
			&identity.NotificationEligible); err != nil {
			return nil, fmt.Errorf("scan mention identity: %w", err)
		}
		result = append(result, identity)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate mention identities: %w", err)
	}
	return result, nil
}

// SyncCommentMentions updates current presence while preserving the unique
// first-seen comment/user fact forever. Only rows inserted by this revision
// are returned, so remove/re-add and retries cannot become new deliveries.
func (s RepoStore) SyncCommentMentions(ctx context.Context, input MentionSyncInput) ([]uuid.UUID, error) {
	if err := s.validate(); err != nil || !s.inTx || input.CommentID == uuid.Nil ||
		input.IssueID == uuid.Nil || input.RepresentationVersion <= 0 {
		return nil, ErrInvalidInput
	}
	rows, err := s.db.Query(ctx, `WITH current_mentions AS (
		SELECT unnest($6::uuid[]) AS user_id
	), updated AS (
		UPDATE comment_mentions mention SET
			present = EXISTS (SELECT 1 FROM current_mentions current WHERE current.user_id = mention.mentioned_user_id),
			last_seen_representation_version = $5,
			last_seen_at = clock_timestamp(), updated_at = clock_timestamp()
		WHERE mention.organization_id = $1 AND mention.repository_id = $2 AND mention.comment_id = $4
		RETURNING mention.mentioned_user_id
	), inserted AS (
		INSERT INTO comment_mentions (
			organization_id, repository_id, issue_id, comment_id, mentioned_user_id,
			first_seen_representation_version, last_seen_representation_version, present)
		SELECT $1, $2, $3, $4, current.user_id, $5, $5, true FROM current_mentions current
		ON CONFLICT (organization_id, repository_id, comment_id, mentioned_user_id) DO NOTHING
		RETURNING mentioned_user_id
	)
	SELECT mentioned_user_id FROM inserted ORDER BY mentioned_user_id`, s.scope.OrgID, s.scope.RepoID,
		input.IssueID, input.CommentID, input.RepresentationVersion, input.MentionedUserIDs)
	if err != nil {
		return nil, fmt.Errorf("sync comment mentions: %w", err)
	}
	defer rows.Close()
	var inserted []uuid.UUID
	for rows.Next() {
		var userID uuid.UUID
		if err := rows.Scan(&userID); err != nil {
			return nil, fmt.Errorf("scan inserted mention: %w", err)
		}
		inserted = append(inserted, userID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate inserted mentions: %w", err)
	}
	return inserted, nil
}
