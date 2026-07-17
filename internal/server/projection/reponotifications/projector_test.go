package reponotifications

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/emaildelivery"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/higress-group/issue-spec/internal/server/store"
)

type fakeRepository struct {
	scope      models.RepoScope
	artifact   bool
	actor      store.RepositoryNotificationCandidate
	candidates []store.RepositoryNotificationCandidate
}

func (r *fakeRepository) Scope() models.RepoScope { return r.scope }
func (r *fakeRepository) IssueHasActiveArtifactProjection(context.Context, uuid.UUID) (bool, error) {
	return r.artifact, nil
}
func (r *fakeRepository) RepositoryNotificationActor(context.Context, uuid.UUID) (store.RepositoryNotificationCandidate, error) {
	return r.actor, nil
}
func (r *fakeRepository) ManualRepositoryNotificationSubscribers(_ context.Context, exclude uuid.UUID) ([]store.RepositoryNotificationCandidate, error) {
	result := make([]store.RepositoryNotificationCandidate, 0, len(r.candidates))
	for _, candidate := range r.candidates {
		if candidate.UserID != exclude {
			result = append(result, candidate)
		}
	}
	return result, nil
}

type fakeQueue struct {
	items map[string]emaildelivery.EnqueueInput
	err   error
}

func (q *fakeQueue) Enqueue(_ context.Context, input emaildelivery.EnqueueInput) (emaildelivery.Delivery, bool, error) {
	if q.err != nil {
		return emaildelivery.Delivery{}, false, q.err
	}
	if q.items == nil {
		q.items = map[string]emaildelivery.EnqueueInput{}
	}
	key := string(input.Kind) + ":" + input.IdempotencyKey
	if _, exists := q.items[key]; exists {
		return emaildelivery.Delivery{}, false, nil
	}
	q.items[key] = input
	return emaildelivery.Delivery{ID: uuid.New()}, true, nil
}

func TestOrdinaryIssueProjectorExcludesActorAndRetriesIdempotently(t *testing.T) {
	actorID, recipientID := uuid.New(), uuid.New()
	repository, event := projectionFixture(actorID)
	repository.candidates = []store.RepositoryNotificationCandidate{
		{UserID: actorID, Login: "author"}, {UserID: recipientID, Login: "reader"},
	}
	projector, _ := NewOrdinaryIssueProjector(StoreSubscriberSelector{})
	queue := &fakeQueue{}
	result, err := projector.ProjectIssueCreated(t.Context(), repository, queue, event)
	if err != nil || result.Eligible != 1 || result.Inserted != 1 || len(queue.items) != 1 {
		t.Fatalf("first projection = %+v items=%d err=%v", result, len(queue.items), err)
	}
	result, err = projector.ProjectIssueCreated(t.Context(), repository, queue, event)
	if err != nil || result.Eligible != 1 || result.Inserted != 0 || len(queue.items) != 1 {
		t.Fatalf("retry projection = %+v items=%d err=%v", result, len(queue.items), err)
	}
	for _, input := range queue.items {
		if input.RecipientUserID != recipientID || input.IdempotencyKey != event.Issue.ID.String()+":"+recipientID.String() {
			t.Fatalf("enqueue input = %+v", input)
		}
	}
}

func TestOrdinaryIssueProjectorSkipsArtifactsAndLateSubscribers(t *testing.T) {
	actorID := uuid.New()
	repository, event := projectionFixture(actorID)
	projector, _ := NewOrdinaryIssueProjector(StoreSubscriberSelector{})
	queue := &fakeQueue{}
	result, err := projector.ProjectIssueCreated(t.Context(), repository, queue, event)
	if err != nil || result.Eligible != 0 || len(queue.items) != 0 {
		t.Fatalf("no subscribers = %+v/%v", result, err)
	}
	// Subscribing after this event does not replay it; only a future hook call
	// for a distinct newly created issue can observe the new subscriber.
	repository.candidates = []store.RepositoryNotificationCandidate{{UserID: uuid.New(), Login: "late"}}
	if len(queue.items) != 0 {
		t.Fatal("late subscription replayed an earlier issue")
	}
	repository.artifact = true
	result, err = projector.ProjectIssueCreated(t.Context(), repository, queue, event)
	if err != nil || !result.SkippedArtifact || len(queue.items) != 0 {
		t.Fatalf("artifact projection = %+v items=%d err=%v", result, len(queue.items), err)
	}
}

func TestOrdinaryIssueProjectorPropagatesEnqueueFailureForTransactionRollback(t *testing.T) {
	actorID := uuid.New()
	repository, event := projectionFixture(actorID)
	repository.candidates = []store.RepositoryNotificationCandidate{{UserID: uuid.New(), Login: "reader"}}
	projector, _ := NewOrdinaryIssueProjector(StoreSubscriberSelector{})
	want := errors.New("transaction write failed")
	if _, err := projector.ProjectIssueCreated(t.Context(), repository, &fakeQueue{err: want}, event); !errors.Is(err, want) {
		t.Fatalf("enqueue error = %v", err)
	}
}

func projectionFixture(actorID uuid.UUID) (*fakeRepository, IssueCreated) {
	scope := models.RepoScope{OrgID: uuid.New(), RepoID: uuid.New()}
	issue := models.Issue{ID: uuid.New(), Scope: scope, Number: 9, AuthorID: &actorID,
		Title: "ordinary issue", Body: "raw body", CreatedAt: time.Now().UTC()}
	repository := &fakeRepository{scope: scope, actor: store.RepositoryNotificationCandidate{
		UserID: actorID, Login: "author", DisplayName: "Author",
	}}
	event := IssueCreated{Repository: models.RepositoryResource{Scope: scope, Owner: "acme", Name: "widgets"},
		Issue: issue, ActorID: actorID}
	return repository, event
}
