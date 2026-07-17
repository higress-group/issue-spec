package reponotifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/emaildelivery"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/higress-group/issue-spec/internal/server/notificationmail"
	notifications "github.com/higress-group/issue-spec/internal/server/reponotifications"
	"github.com/higress-group/issue-spec/internal/server/store"
)

type MilestoneRepository interface {
	Repository
	CreatedIssueArtifactNotification(context.Context, uuid.UUID) (store.CreatedArtifactNotification, bool, error)
	InsertNotificationMilestone(context.Context, store.NotificationMilestoneInput) (store.NotificationMilestoneFact, bool, error)
}

type MilestoneProjector struct{ subscribers SubscriberSelector }

func NewMilestoneProjector(subscribers SubscriberSelector) (*MilestoneProjector, error) {
	if subscribers == nil {
		return nil, notifications.ErrInvalid
	}
	return &MilestoneProjector{subscribers: subscribers}, nil
}

type MilestoneResult struct {
	Handled  bool
	FactNew  bool
	Eligible int
	Inserted int
}

// ProjectCreatedArtifact handles proposal/design/implement only when the new
// issue already has an authoritative active artifact projection. Callers run
// the ordinary projector only when Handled is false.
func (p *MilestoneProjector) ProjectCreatedArtifact(ctx context.Context, repository MilestoneRepository,
	queue Enqueuer, event IssueCreated) (MilestoneResult, error) {
	if err := validateMilestoneEvent(repository, queue, event, true); err != nil {
		return MilestoneResult{}, err
	}
	artifact, handled, err := repository.CreatedIssueArtifactNotification(ctx, event.Issue.ID)
	if err != nil || !handled {
		return MilestoneResult{Handled: handled}, err
	}
	fact, inserted, err := repository.InsertNotificationMilestone(ctx, store.NotificationMilestoneInput{
		ID: uuid.New(), ChangeKey: artifact.ChangeKey, Milestone: artifact.Milestone,
		TriggeringIssueID: event.Issue.ID, ActorUserID: event.ActorID, OccurredAt: event.Issue.CreatedAt.UTC(),
	})
	if err != nil || !inserted {
		return MilestoneResult{Handled: true, FactNew: inserted}, err
	}
	body, truncated := notifications.NormalizeIssueBody(event.Issue.Body)
	snapshot := notificationmail.MilestoneSnapshot{Version: notificationmail.SnapshotVersion,
		RepositoryOwner: event.Repository.Owner, RepositoryName: event.Repository.Name,
		ChangeKey: artifact.ChangeKey, Milestone: artifact.Milestone, IssueID: event.Issue.ID,
		IssueNumber: event.Issue.Number, IssueTitle: event.Issue.Title, Content: body,
		ContentTruncated: truncated, OccurredAt: event.Issue.CreatedAt.UTC()}
	result, err := p.fanout(ctx, repository, queue, event.ActorID, fact, snapshot)
	result.Handled, result.FactNew = true, true
	return result, err
}

type LifecycleState struct {
	ChangeKey string
	Lifecycle string
}

type CompletedMutation struct {
	Repository models.RepositoryResource
	Issue      models.Issue
	Comment    *models.CommentSnapshot
	ActorID    uuid.UUID
	Before     LifecycleState
	After      LifecycleState
}

// ProjectCompleted accepts only a known lifecycle for the same change moving
// from non-completed to completed. Empty/new keys and edit-time artifact
// conversions are silent rather than becoming a general transition engine.
func (p *MilestoneProjector) ProjectCompleted(ctx context.Context, repository MilestoneRepository,
	queue Enqueuer, mutation CompletedMutation) (MilestoneResult, error) {
	event := IssueCreated{Repository: mutation.Repository, Issue: mutation.Issue, ActorID: mutation.ActorID}
	if err := validateMilestoneEvent(repository, queue, event, false); err != nil {
		return MilestoneResult{}, err
	}
	beforeKey := strings.ToLower(strings.TrimSpace(mutation.Before.ChangeKey))
	afterKey := strings.ToLower(strings.TrimSpace(mutation.After.ChangeKey))
	if beforeKey == "" || beforeKey != afterKey || mutation.Before.Lifecycle == "completed" ||
		mutation.After.Lifecycle != "completed" || !knownPriorLifecycle(mutation.Before.Lifecycle) {
		return MilestoneResult{}, nil
	}
	occurredAt := mutation.Issue.UpdatedAt.UTC()
	var commentID *uuid.UUID
	if mutation.Comment != nil {
		id := mutation.Comment.Comment.ID
		commentID = &id
		occurredAt = mutation.Comment.Comment.UpdatedAt.UTC()
	}
	fact, inserted, err := repository.InsertNotificationMilestone(ctx, store.NotificationMilestoneInput{
		ID: uuid.New(), ChangeKey: afterKey, Milestone: store.NotificationMilestoneCompleted,
		TriggeringIssueID: mutation.Issue.ID, TriggeringCommentID: commentID,
		ActorUserID: mutation.ActorID, OccurredAt: occurredAt,
	})
	if err != nil || !inserted {
		return MilestoneResult{Handled: true, FactNew: inserted}, err
	}
	snapshot := notificationmail.MilestoneSnapshot{Version: notificationmail.SnapshotVersion,
		RepositoryOwner: mutation.Repository.Owner, RepositoryName: mutation.Repository.Name,
		ChangeKey: afterKey, Milestone: store.NotificationMilestoneCompleted,
		IssueID: mutation.Issue.ID, OccurredAt: occurredAt}
	result, err := p.fanout(ctx, repository, queue, mutation.ActorID, fact, snapshot)
	result.Handled, result.FactNew = true, true
	return result, err
}

func (p *MilestoneProjector) fanout(ctx context.Context, repository MilestoneRepository, queue Enqueuer,
	actorID uuid.UUID, fact store.NotificationMilestoneFact, snapshot notificationmail.MilestoneSnapshot) (MilestoneResult, error) {
	actor, err := repository.RepositoryNotificationActor(ctx, actorID)
	if err != nil {
		return MilestoneResult{}, err
	}
	snapshot.ActorLogin, snapshot.ActorDisplayName = actor.Login, actor.DisplayName
	if err := snapshot.Validate(); err != nil {
		return MilestoneResult{}, err
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return MilestoneResult{}, fmt.Errorf("repository notifications: encode milestone snapshot: %w", err)
	}
	subscribers, err := p.subscribers.EligibleSubscribers(ctx, repository, actorID)
	if err != nil {
		return MilestoneResult{}, err
	}
	result := MilestoneResult{Eligible: len(subscribers)}
	for _, subscriber := range subscribers {
		if subscriber.UserID == uuid.Nil || subscriber.UserID == actorID {
			return MilestoneResult{}, errors.New("repository notifications: invalid milestone subscriber")
		}
		orgID, repoID, milestoneID := repository.Scope().OrgID, repository.Scope().RepoID, fact.ID
		_, inserted, err := queue.Enqueue(ctx, emaildelivery.EnqueueInput{
			Kind:            emaildelivery.KindChangeMilestone,
			IdempotencyKey:  milestoneID.String() + ":" + subscriber.UserID.String(),
			RecipientUserID: subscriber.UserID, OrganizationID: &orgID, RepositoryID: &repoID,
			MilestoneID: &milestoneID, Snapshot: encoded,
		})
		if err != nil {
			return MilestoneResult{}, fmt.Errorf("repository notifications: enqueue milestone: %w", err)
		}
		if inserted {
			result.Inserted++
		}
	}
	return result, nil
}

func validateMilestoneEvent(repository Repository, queue Enqueuer, event IssueCreated, requireAuthor bool) error {
	if repository == nil || queue == nil || event.ActorID == uuid.Nil || event.Issue.ID == uuid.Nil ||
		event.Issue.Number <= 0 ||
		event.Issue.Scope != repository.Scope() || event.Repository.Scope != repository.Scope() ||
		event.Issue.CreatedAt.IsZero() {
		return notifications.ErrInvalid
	}
	if requireAuthor && (event.Issue.AuthorID == nil || *event.Issue.AuthorID != event.ActorID) {
		return notifications.ErrInvalid
	}
	return nil
}

func knownPriorLifecycle(value string) bool {
	return value == "active" || value == "blocked" || value == "closed"
}
