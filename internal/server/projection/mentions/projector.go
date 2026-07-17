package mentions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/emaildelivery"
	"github.com/higress-group/issue-spec/internal/server/mentionmail"
	"github.com/higress-group/issue-spec/internal/server/models"
	serverstore "github.com/higress-group/issue-spec/internal/server/store"
)

type CommentStore interface {
	MentionContext(context.Context, uuid.UUID, uuid.UUID) (serverstore.MentionCommentContext, error)
	ResolveMentionIdentities(context.Context, []string) ([]serverstore.MentionIdentity, error)
	SyncCommentMentions(context.Context, serverstore.MentionSyncInput) ([]uuid.UUID, error)
}

// Eligibility is intentionally injected at the transaction boundary. The P5
// composition adapter must capture the open comment transaction and use the
// authoritative identity-based repository read evaluator.
type Eligibility interface {
	CanReadRepository(context.Context, uuid.UUID, models.RepoScope) (bool, error)
}

type Enqueuer interface {
	Enqueue(context.Context, emaildelivery.EnqueueInput) (emaildelivery.Delivery, bool, error)
}

type Projector struct {
	parser      *Parser
	eligibility Eligibility
}

func NewProjector(eligibility Eligibility) (*Projector, error) {
	if eligibility == nil {
		return nil, errors.New("mention projection: eligibility is required")
	}
	return &Projector{parser: NewParser(), eligibility: eligibility}, nil
}

// CommentMutation is the narrow hook P5 calls after comment persistence and
// before commit. It contains identity/version only; all message fields and raw
// Markdown are reloaded through the transaction-bound store.
type CommentMutation struct {
	Scope                 models.RepoScope
	CommentID             uuid.UUID
	ActorUserID           uuid.UUID
	RepresentationVersion int64
}

func (p *Projector) ProjectComment(ctx context.Context, repository CommentStore, queue Enqueuer, input CommentMutation) error {
	if p == nil || p.parser == nil || p.eligibility == nil || repository == nil || queue == nil ||
		input.Scope.Validate() != nil || input.CommentID == uuid.Nil || input.ActorUserID == uuid.Nil ||
		input.RepresentationVersion <= 0 {
		return errors.New("mention projection: invalid comment mutation")
	}
	comment, err := repository.MentionContext(ctx, input.CommentID, input.ActorUserID)
	if err != nil {
		return err
	}
	if comment.RepresentationVersion != input.RepresentationVersion {
		return errors.New("mention projection: comment version changed")
	}
	identities, err := repository.ResolveMentionIdentities(ctx, p.parser.Logins([]byte(comment.Body)))
	if err != nil {
		return err
	}
	mentionedIDs := make([]uuid.UUID, 0, len(identities))
	byID := make(map[uuid.UUID]serverstore.MentionIdentity, len(identities))
	for _, identity := range identities {
		mentionedIDs = append(mentionedIDs, identity.UserID)
		byID[identity.UserID] = identity
	}
	firstSeen, err := repository.SyncCommentMentions(ctx, serverstore.MentionSyncInput{
		CommentID: input.CommentID, IssueID: comment.IssueID,
		RepresentationVersion: input.RepresentationVersion, MentionedUserIDs: mentionedIDs,
	})
	if err != nil {
		return err
	}
	snapshot := mentionmail.Snapshot{Version: mentionmail.SnapshotVersion,
		ActorLogin: comment.ActorLogin, ActorDisplayName: comment.ActorDisplayName,
		Organization: comment.Organization, Repository: comment.Repository,
		IssueNumber: comment.IssueNumber, IssueTitle: comment.IssueTitle,
		CommentID: comment.CommentID, CommentNumericID: comment.CompatibilityID,
		Excerpt: boundedExcerpt(comment.Body), OccurredAt: comment.OccurredAt,
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("mention projection: encode snapshot: %w", err)
	}
	for _, userID := range firstSeen {
		identity, exists := byID[userID]
		if !exists || userID == input.ActorUserID || !identity.NotificationEligible {
			continue
		}
		allowed, err := p.eligibility.CanReadRepository(ctx, userID, input.Scope)
		if err != nil {
			return fmt.Errorf("mention projection: evaluate recipient: %w", err)
		}
		if !allowed {
			continue
		}
		commentID, orgID, repoID := comment.CommentID, input.Scope.OrgID, input.Scope.RepoID
		_, _, err = queue.Enqueue(ctx, emaildelivery.EnqueueInput{Kind: emaildelivery.KindMention,
			IdempotencyKey: commentID.String() + ":" + userID.String(), RecipientUserID: userID,
			OrganizationID: &orgID, RepositoryID: &repoID, CommentID: &commentID, Snapshot: encoded})
		if err != nil {
			return fmt.Errorf("mention projection: enqueue: %w", err)
		}
	}
	return nil
}

func boundedExcerpt(body string) string {
	value := strings.Join(strings.Fields(body), " ")
	runes := []rune(value)
	if len(runes) <= mentionmail.MaxExcerptRunes {
		return value
	}
	return string(runes[:mentionmail.MaxExcerptRunes-1]) + "…"
}
