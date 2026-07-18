package reponotifications

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/emaildelivery"
	"github.com/higress-group/issue-spec/internal/server/notificationmail"
	"github.com/higress-group/issue-spec/internal/server/store"
)

func TestCompletedTransitionIsNarrow(t *testing.T) {
	for _, lifecycle := range []string{"active", "blocked", "closed"} {
		if !knownPriorLifecycle(lifecycle) {
			t.Fatalf("prior lifecycle %q rejected", lifecycle)
		}
	}
	for _, lifecycle := range []string{"", "completed", "edited", "proposal"} {
		if knownPriorLifecycle(lifecycle) {
			t.Fatalf("prior lifecycle %q accepted", lifecycle)
		}
	}
}

func TestArtifactMilestoneFactPreventsRetryAndLateSubscriberReplay(t *testing.T) {
	actorID, recipientID, lateID := uuid.New(), uuid.New(), uuid.New()
	repository, event := projectionFixture(actorID)
	repository.createdArtifact = store.CreatedArtifactNotification{
		ChangeKey: "email-flow", Milestone: store.NotificationMilestoneProposal,
	}
	repository.candidates = []store.RepositoryNotificationCandidate{{UserID: recipientID, Login: "reader"}}
	projector, err := NewMilestoneProjector(StoreSubscriberSelector{})
	if err != nil {
		t.Fatal(err)
	}
	queue := &fakeQueue{}
	result, err := projector.ProjectCreatedArtifact(t.Context(), repository, queue, event)
	if err != nil || !result.Handled || !result.FactNew || result.Eligible != 1 || result.Inserted != 1 {
		t.Fatalf("first projection = %+v err=%v", result, err)
	}
	if len(repository.milestoneInsertions) != 1 || len(queue.items) != 1 {
		t.Fatalf("facts=%d deliveries=%d", len(repository.milestoneInsertions), len(queue.items))
	}
	for _, input := range queue.items {
		if input.Kind != emaildelivery.KindChangeMilestone || input.RecipientUserID != recipientID ||
			input.MilestoneID == nil || input.IdempotencyKey != input.MilestoneID.String()+":"+recipientID.String() {
			t.Fatalf("enqueue input = %+v", input)
		}
		var snapshot notificationmail.MilestoneSnapshot
		if err := json.Unmarshal(input.Snapshot, &snapshot); err != nil ||
			snapshot.ChangeKey != "email-flow" || snapshot.Milestone != store.NotificationMilestoneProposal {
			t.Fatalf("snapshot = %+v err=%v", snapshot, err)
		}
	}

	repository.candidates = append(repository.candidates,
		store.RepositoryNotificationCandidate{UserID: lateID, Login: "late"})
	result, err = projector.ProjectCreatedArtifact(t.Context(), repository, queue, event)
	if err != nil || !result.Handled || result.FactNew || result.Eligible != 0 || result.Inserted != 0 {
		t.Fatalf("retry projection = %+v err=%v", result, err)
	}
	if len(repository.milestoneInsertions) != 1 || len(queue.items) != 1 {
		t.Fatalf("retry facts=%d deliveries=%d", len(repository.milestoneInsertions), len(queue.items))
	}
}

func TestCompletedMilestoneEmitsOnceAndIgnoresReentry(t *testing.T) {
	actorID, recipientID := uuid.New(), uuid.New()
	repository, event := projectionFixture(actorID)
	repository.candidates = []store.RepositoryNotificationCandidate{{UserID: recipientID, Login: "reader"}}
	projector, _ := NewMilestoneProjector(StoreSubscriberSelector{})
	queue := &fakeQueue{}
	event.Issue.UpdatedAt = time.Now().UTC()
	mutation := CompletedMutation{Repository: event.Repository, Issue: event.Issue, ActorID: actorID,
		Before: LifecycleState{ChangeKey: "email-flow", Lifecycle: "active"},
		After:  LifecycleState{ChangeKey: "email-flow", Lifecycle: "completed"}}
	result, err := projector.ProjectCompleted(t.Context(), repository, queue, mutation)
	if err != nil || !result.FactNew || result.Inserted != 1 {
		t.Fatalf("completed projection = %+v err=%v", result, err)
	}
	result, err = projector.ProjectCompleted(t.Context(), repository, queue, mutation)
	if err != nil || result.FactNew || result.Inserted != 0 || len(queue.items) != 1 {
		t.Fatalf("completed retry = %+v deliveries=%d err=%v", result, len(queue.items), err)
	}
	mutation.Before.Lifecycle = "completed"
	if result, err = projector.ProjectCompleted(t.Context(), repository, queue, mutation); err != nil || result.Handled {
		t.Fatalf("reentry projection = %+v err=%v", result, err)
	}
}
