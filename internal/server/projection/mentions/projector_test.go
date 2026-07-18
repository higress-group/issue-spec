package mentions

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/emaildelivery"
	"github.com/higress-group/issue-spec/internal/server/mentionmail"
	"github.com/higress-group/issue-spec/internal/server/models"
	serverstore "github.com/higress-group/issue-spec/internal/server/store"
)

func TestProjectorPersistsFirstSeenAndEnqueuesOnlyEligibleNewRecipients(t *testing.T) {
	actorID, aliceID, noMailID, deniedID, bobID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	commentID, issueID := uuid.New(), uuid.New()
	scope := models.RepoScope{OrgID: uuid.New(), RepoID: uuid.New()}
	store := &projectionStore{context: serverstore.MentionCommentContext{CommentID: commentID,
		IssueID: issueID, Body: "@Alice @alice @actor @no-mail @denied @unknown",
		RepresentationVersion: 1, CompatibilityID: 42, OccurredAt: time.Now().UTC(),
		IssueNumber: 17, IssueTitle: "Mention behavior", Organization: "acme", Repository: "widgets",
		ActorLogin: "actor", ActorDisplayName: "Actor",
	}, identities: map[string]serverstore.MentionIdentity{
		"alice":   {UserID: aliceID, Login: "alice", NotificationEligible: true},
		"actor":   {UserID: actorID, Login: "actor", NotificationEligible: true},
		"no-mail": {UserID: noMailID, Login: "no-mail"},
		"denied":  {UserID: deniedID, Login: "denied", NotificationEligible: true},
		"bob":     {UserID: bobID, Login: "bob", NotificationEligible: true},
	}}
	eligibility := &projectionEligibility{allowed: map[uuid.UUID]bool{aliceID: true, bobID: true}}
	projector, err := NewProjector(eligibility)
	if err != nil {
		t.Fatal(err)
	}
	queue := &projectionQueue{}
	input := CommentMutation{Scope: scope, CommentID: commentID, ActorUserID: actorID, RepresentationVersion: 1}
	if err := projector.ProjectComment(t.Context(), store, queue, input); err != nil {
		t.Fatal(err)
	}
	if len(queue.inputs) != 1 || queue.inputs[0].RecipientUserID != aliceID ||
		queue.inputs[0].Kind != emaildelivery.KindMention {
		t.Fatalf("deliveries = %+v", queue.inputs)
	}
	var snapshot mentionmail.Snapshot
	if err := json.Unmarshal(queue.inputs[0].Snapshot, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.CommentID != commentID || snapshot.CommentNumericID != 42 || snapshot.Excerpt == "" {
		t.Fatalf("snapshot = %+v", snapshot)
	}

	// Removal updates presence but does not erase first-seen identity. Re-adding
	// the same recipient therefore cannot enqueue again; a genuinely new user can.
	store.context.Body, store.context.RepresentationVersion = "mention removed", 2
	input.RepresentationVersion = 2
	if err := projector.ProjectComment(t.Context(), store, queue, input); err != nil {
		t.Fatal(err)
	}
	store.context.Body, store.context.RepresentationVersion = "@alice and @bob", 3
	input.RepresentationVersion = 3
	if err := projector.ProjectComment(t.Context(), store, queue, input); err != nil {
		t.Fatal(err)
	}
	if len(queue.inputs) != 2 || queue.inputs[1].RecipientUserID != bobID {
		t.Fatalf("edit deliveries = %+v", queue.inputs)
	}
}

func TestProjectorPropagatesEligibilityAndQueueFailures(t *testing.T) {
	userID := uuid.New()
	store := minimalProjectionStore(userID)
	scope := models.RepoScope{OrgID: uuid.New(), RepoID: uuid.New()}
	projector, _ := NewProjector(&projectionEligibility{err: errors.New("policy unavailable")})
	err := projector.ProjectComment(t.Context(), store, &projectionQueue{}, CommentMutation{Scope: scope,
		CommentID: store.context.CommentID, ActorUserID: uuid.New(), RepresentationVersion: 1})
	if err == nil || !strings.Contains(err.Error(), "evaluate recipient") {
		t.Fatalf("eligibility error = %v", err)
	}
	store = minimalProjectionStore(userID)
	projector, _ = NewProjector(&projectionEligibility{allowed: map[uuid.UUID]bool{userID: true}})
	err = projector.ProjectComment(t.Context(), store, &projectionQueue{err: errors.New("queue unavailable")}, CommentMutation{Scope: scope,
		CommentID: store.context.CommentID, ActorUserID: uuid.New(), RepresentationVersion: 1})
	if err == nil || !strings.Contains(err.Error(), "enqueue") {
		t.Fatalf("queue error = %v", err)
	}
}

type projectionStore struct {
	context    serverstore.MentionCommentContext
	identities map[string]serverstore.MentionIdentity
	seen       map[uuid.UUID]bool
}

func (s *projectionStore) MentionContext(context.Context, uuid.UUID, uuid.UUID) (serverstore.MentionCommentContext, error) {
	return s.context, nil
}

func (s *projectionStore) ResolveMentionIdentities(_ context.Context, logins []string) ([]serverstore.MentionIdentity, error) {
	result := make([]serverstore.MentionIdentity, 0, len(logins))
	for _, login := range logins {
		if identity, ok := s.identities[login]; ok {
			result = append(result, identity)
		}
	}
	return result, nil
}

func (s *projectionStore) SyncCommentMentions(_ context.Context, input serverstore.MentionSyncInput) ([]uuid.UUID, error) {
	if s.seen == nil {
		s.seen = make(map[uuid.UUID]bool)
	}
	var first []uuid.UUID
	for _, userID := range input.MentionedUserIDs {
		if !s.seen[userID] {
			s.seen[userID] = true
			first = append(first, userID)
		}
	}
	return first, nil
}

type projectionEligibility struct {
	allowed map[uuid.UUID]bool
	err     error
}

func (e *projectionEligibility) CanReadRepository(_ context.Context, userID uuid.UUID, _ models.RepoScope) (bool, error) {
	return e.allowed[userID], e.err
}

type projectionQueue struct {
	inputs []emaildelivery.EnqueueInput
	err    error
}

func (q *projectionQueue) Enqueue(_ context.Context, input emaildelivery.EnqueueInput) (emaildelivery.Delivery, bool, error) {
	q.inputs = append(q.inputs, input)
	return emaildelivery.Delivery{}, true, q.err
}

func minimalProjectionStore(userID uuid.UUID) *projectionStore {
	commentID := uuid.New()
	return &projectionStore{context: serverstore.MentionCommentContext{CommentID: commentID,
		IssueID: uuid.New(), Body: "@person", RepresentationVersion: 1, CompatibilityID: 1,
		OccurredAt: time.Now().UTC(), IssueNumber: 1, IssueTitle: "Issue", Organization: "acme",
		Repository: "widgets", ActorLogin: "actor", ActorDisplayName: "Actor"},
		identities: map[string]serverstore.MentionIdentity{"person": {
			UserID: userID, Login: "person", NotificationEligible: true,
		}}}
}
