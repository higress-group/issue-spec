// Package reponotifications projects only newly created ordinary issues into
// repository email deliveries. Change milestones remain a successor concern.
package reponotifications

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/emaildelivery"
	"github.com/higress-group/issue-spec/internal/server/models"
	notifications "github.com/higress-group/issue-spec/internal/server/reponotifications"
	"github.com/higress-group/issue-spec/internal/server/store"
)

type Enqueuer interface {
	Enqueue(context.Context, emaildelivery.EnqueueInput) (emaildelivery.Delivery, bool, error)
}

type Repository interface {
	Scope() models.RepoScope
	IssueHasActiveArtifactProjection(context.Context, uuid.UUID) (bool, error)
	RepositoryNotificationActor(context.Context, uuid.UUID) (store.RepositoryNotificationCandidate, error)
	ManualRepositoryNotificationSubscribers(context.Context, uuid.UUID) ([]store.RepositoryNotificationCandidate, error)
}

// SubscriberSelector is explicit so milestone projection can reuse the same
// current-subscriber eligibility boundary in PROCESS-005.
type SubscriberSelector interface {
	EligibleSubscribers(context.Context, Repository, uuid.UUID) ([]store.RepositoryNotificationCandidate, error)
}

type StoreSubscriberSelector struct{}

func (StoreSubscriberSelector) EligibleSubscribers(ctx context.Context, repository Repository, exclude uuid.UUID) ([]store.RepositoryNotificationCandidate, error) {
	return repository.ManualRepositoryNotificationSubscribers(ctx, exclude)
}

type IssueCreated struct {
	Repository models.RepositoryResource
	Issue      models.Issue
	ActorID    uuid.UUID
}

type Result struct {
	Eligible        int
	Inserted        int
	SkippedArtifact bool
}

// OrdinaryIssueProjector is the transaction-bound CreateIssue hook exposed to
// PROCESS-005. Invoke it after authoritative artifact projection and before
// commit, passing an emaildelivery.Store created from the same pgx transaction.
type OrdinaryIssueProjector struct{ subscribers SubscriberSelector }

func NewOrdinaryIssueProjector(subscribers SubscriberSelector) (*OrdinaryIssueProjector, error) {
	if subscribers == nil {
		return nil, notifications.ErrInvalid
	}
	return &OrdinaryIssueProjector{subscribers: subscribers}, nil
}

func (p *OrdinaryIssueProjector) ProjectIssueCreated(ctx context.Context, repository Repository, queue Enqueuer, event IssueCreated) (Result, error) {
	if p == nil || queue == nil || event.ActorID == uuid.Nil || event.Issue.ID == uuid.Nil ||
		event.Issue.Number <= 0 || event.Issue.AuthorID == nil || *event.Issue.AuthorID != event.ActorID ||
		event.Issue.Scope != repository.Scope() || event.Repository.Scope != repository.Scope() ||
		event.Issue.CreatedAt.IsZero() {
		return Result{}, notifications.ErrInvalid
	}
	artifact, err := repository.IssueHasActiveArtifactProjection(ctx, event.Issue.ID)
	if err != nil {
		return Result{}, err
	}
	if artifact {
		return Result{SkippedArtifact: true}, nil
	}
	actor, err := repository.RepositoryNotificationActor(ctx, event.ActorID)
	if err != nil {
		return Result{}, err
	}
	body, truncated := notifications.NormalizeIssueBody(event.Issue.Body)
	snapshot := notifications.IssueCreatedSnapshot{Version: 1, ActorLogin: actor.Login,
		ActorDisplayName: actor.DisplayName, RepositoryOwner: event.Repository.Owner,
		RepositoryName: event.Repository.Name, IssueID: event.Issue.ID, IssueNumber: event.Issue.Number,
		IssueTitle: event.Issue.Title, IssueBody: body, BodyTruncated: truncated,
		OccurredAt: event.Issue.CreatedAt.UTC()}
	if err := snapshot.Validate(); err != nil {
		return Result{}, err
	}
	renderSnapshot, err := json.Marshal(snapshot)
	if err != nil {
		return Result{}, err
	}
	subscribers, err := p.subscribers.EligibleSubscribers(ctx, repository, event.ActorID)
	if err != nil {
		return Result{}, err
	}
	result := Result{Eligible: len(subscribers)}
	for _, subscriber := range subscribers {
		if subscriber.UserID == uuid.Nil || subscriber.UserID == event.ActorID {
			return Result{}, errors.New("repository notifications: invalid subscriber selection")
		}
		issueID, orgID, repoID := event.Issue.ID, event.Issue.Scope.OrgID, event.Issue.Scope.RepoID
		_, inserted, err := queue.Enqueue(ctx, emaildelivery.EnqueueInput{
			Kind: emaildelivery.KindRepoIssueCreated, IdempotencyKey: issueID.String() + ":" + subscriber.UserID.String(),
			RecipientUserID: subscriber.UserID, OrganizationID: &orgID, RepositoryID: &repoID,
			IssueID: &issueID, Snapshot: renderSnapshot,
		})
		if err != nil {
			return Result{}, err
		}
		if inserted {
			result.Inserted++
		}
	}
	return result, nil
}
