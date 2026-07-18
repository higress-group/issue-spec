package emaildelivery

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestWorkerRetryFailureAndSuppressionDisposition(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	tests := []struct {
		name       string
		attempts   int
		prepareErr error
		sendErr    error
		want       State
		wantReason ReasonCode
		wantNext   time.Time
	}{
		{name: "retry", attempts: 2, sendErr: Retryable(ReasonSMTPTimeout), want: StatePending,
			wantReason: ReasonSMTPTimeout, wantNext: now.Add(2 * time.Minute)},
		{name: "fifth attempt fails", attempts: 5, sendErr: Retryable(ReasonSMTPAmbiguous), want: StateFailed,
			wantReason: ReasonSMTPAmbiguous},
		{name: "policy suppresses", attempts: 1, prepareErr: Suppressed(ReasonPolicyUnavailable),
			want: StateSuppressed, wantReason: ReasonPolicyUnavailable},
		{name: "terminal failure", attempts: 1, sendErr: Permanent(ReasonSMTPAuthentication),
			want: StateFailed, wantReason: ReasonSMTPAuthentication},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			claim := testClaim(test.attempts, now)
			queue := &fakeQueue{claims: []*Claim{claim}}
			worker, err := NewWorker(queue, prepareFunc(func(context.Context, Delivery) (Message, error) {
				if test.prepareErr != nil {
					return Message{}, test.prepareErr
				}
				return Message{DeliveryID: claim.ID, To: "person@example.test", Subject: "Notice",
					Body: "body", OccurredAt: now}, nil
			}), senderFunc(func(context.Context, Message) error { return test.sendErr }), WorkerConfig{Clock: func() time.Time { return now }})
			if err != nil {
				t.Fatal(err)
			}
			if err := worker.ProcessOne(t.Context()); err != nil {
				t.Fatal(err)
			}
			if queue.finalState != test.want || queue.reason != test.wantReason {
				t.Fatalf("final = %s/%s, want %s/%s", queue.finalState, queue.reason, test.want, test.wantReason)
			}
			if !test.wantNext.IsZero() && !queue.next.Equal(test.wantNext) {
				t.Fatalf("retry = %s, want %s", queue.next, test.wantNext)
			}
		})
	}
}

func TestWorkerSuccessfulSendAndUnknownErrorsStayRedacted(t *testing.T) {
	now := time.Now().UTC()
	claim := testClaim(1, now)
	queue := &fakeQueue{claims: []*Claim{claim}}
	worker, err := NewWorker(queue, prepareFunc(func(context.Context, Delivery) (Message, error) {
		return Message{To: "person@example.test", Subject: "Notice", Body: "body", OccurredAt: now}, nil
	}), senderFunc(func(context.Context, Message) error { return nil }), WorkerConfig{Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.ProcessOne(t.Context()); err != nil || queue.finalState != StateSucceeded {
		t.Fatalf("success = %s, %v", queue.finalState, err)
	}

	claim = testClaim(1, now)
	queue = &fakeQueue{claims: []*Claim{claim}}
	worker, _ = NewWorker(queue, prepareFunc(func(context.Context, Delivery) (Message, error) {
		return Message{}, errors.New("private body and person@example.test must not persist")
	}), senderFunc(func(context.Context, Message) error { return nil }), WorkerConfig{Clock: func() time.Time { return now }})
	if err := worker.ProcessOne(t.Context()); err != nil {
		t.Fatal(err)
	}
	if queue.reason != ReasonPreparationUnavailable || queue.finalState != StatePending {
		t.Fatalf("unknown error disposition = %s/%s", queue.finalState, queue.reason)
	}
}

func TestWorkerStopClaimsDrainsInFlightSend(t *testing.T) {
	now := time.Now().UTC()
	queue := &fakeQueue{claims: []*Claim{testClaim(1, now)}}
	entered, release := make(chan struct{}), make(chan struct{})
	worker, err := NewWorker(queue, prepareFunc(func(context.Context, Delivery) (Message, error) {
		return Message{To: "person@example.test", Subject: "Notice", Body: "body", OccurredAt: now}, nil
	}), senderFunc(func(ctx context.Context, _ Message) error {
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}), WorkerConfig{MaxConcurrency: 1, PollInterval: time.Millisecond, Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- worker.Run(context.Background()) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("worker did not enter sender")
	}
	worker.StopClaims()
	select {
	case err := <-done:
		t.Fatalf("worker did not drain in-flight send: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if queue.finalState != StateSucceeded {
		t.Fatalf("final state = %s", queue.finalState)
	}
}

func TestWorkerStoppedBeforeRunDoesNotClaim(t *testing.T) {
	queue := &fakeQueue{}
	worker, err := NewWorker(queue, prepareFunc(func(context.Context, Delivery) (Message, error) {
		return Message{}, nil
	}), senderFunc(func(context.Context, Message) error { return nil }), WorkerConfig{MaxConcurrency: 2})
	if err != nil {
		t.Fatal(err)
	}
	worker.StopClaims()
	worker.StopClaims()
	if err := worker.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if queue.claimCalls != 0 {
		t.Fatalf("claims = %d", queue.claimCalls)
	}
}

func TestDesignExactKindsStatesAndEnqueueReferences(t *testing.T) {
	kinds := []Kind{KindVerification, KindMention, KindRepoIssueCreated, KindChangeMilestone}
	for _, kind := range kinds {
		if !kind.Valid() {
			t.Fatalf("kind %q is not valid", kind)
		}
	}
	for _, kind := range []Kind{"repository", "milestone", "webhook", ""} {
		if kind.Valid() {
			t.Fatalf("unexpected kind %q", kind)
		}
	}
	states := []State{StatePending, StateDelivering, StateSucceeded, StateFailed, StateSuppressed}
	if len(states) != 5 {
		t.Fatal("design state set changed")
	}
	user, org, repo, request, comment, issue, milestone := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	inputs := []EnqueueInput{
		{Kind: KindVerification, IdempotencyKey: request.String(), RecipientUserID: user, VerificationRequestID: &request, Snapshot: []byte(`{"request":"one"}`)},
		{Kind: KindMention, IdempotencyKey: comment.String() + ":" + user.String(), RecipientUserID: user, OrganizationID: &org, RepositoryID: &repo, CommentID: &comment, Snapshot: []byte(`{"comment":"one"}`)},
		{Kind: KindRepoIssueCreated, IdempotencyKey: issue.String() + ":" + user.String(), RecipientUserID: user, OrganizationID: &org, RepositoryID: &repo, IssueID: &issue, Snapshot: []byte(`{"issue":"one"}`)},
		{Kind: KindChangeMilestone, IdempotencyKey: milestone.String() + ":" + user.String(), RecipientUserID: user, OrganizationID: &org, RepositoryID: &repo, MilestoneID: &milestone, Snapshot: []byte(`{"milestone":"one"}`)},
	}
	for _, input := range inputs {
		if _, err := input.validate(); err != nil {
			t.Fatalf("%s input: %v", input.Kind, err)
		}
		first, _ := StableDeliveryID(input.Kind, input.IdempotencyKey)
		second, _ := StableDeliveryID(input.Kind, input.IdempotencyKey)
		if first == uuid.Nil || first != second {
			t.Fatalf("%s stable id = %s/%s", input.Kind, first, second)
		}
	}
}

type prepareFunc func(context.Context, Delivery) (Message, error)

func (f prepareFunc) Prepare(ctx context.Context, delivery Delivery) (Message, error) {
	return f(ctx, delivery)
}

type senderFunc func(context.Context, Message) error

func (f senderFunc) Send(ctx context.Context, message Message) error { return f(ctx, message) }

type fakeQueue struct {
	mu          sync.Mutex
	claims      []*Claim
	claimCalls  int
	finalState  State
	reason      ReasonCode
	next        time.Time
	completedAt time.Time
}

func (q *fakeQueue) ClaimOne(context.Context, time.Time, time.Duration) (*Claim, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.claimCalls++
	if len(q.claims) == 0 {
		return nil, ErrNoWork
	}
	claim := q.claims[0]
	q.claims = q.claims[1:]
	return claim, nil
}

func (q *fakeQueue) Succeed(_ context.Context, _ *Claim, at time.Time) error {
	q.record(StateSucceeded, "", time.Time{}, at)
	return nil
}

func (q *fakeQueue) Retry(_ context.Context, _ *Claim, next, at time.Time, reason ReasonCode) error {
	q.record(StatePending, reason, next, at)
	return nil
}

func (q *fakeQueue) Fail(_ context.Context, _ *Claim, at time.Time, reason ReasonCode) error {
	q.record(StateFailed, reason, time.Time{}, at)
	return nil
}

func (q *fakeQueue) Suppress(_ context.Context, _ *Claim, at time.Time, reason ReasonCode) error {
	q.record(StateSuppressed, reason, time.Time{}, at)
	return nil
}

func (q *fakeQueue) record(state State, reason ReasonCode, next, at time.Time) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.finalState, q.reason, q.next, q.completedAt = state, reason, next, at
}

func testClaim(attempts int, now time.Time) *Claim {
	id := uuid.New()
	return &Claim{Delivery: Delivery{ID: id, Kind: KindMention, IdempotencyKey: id.String(),
		RecipientUserID: uuid.New(), Snapshot: []byte(`{"message":"one"}`), State: StateDelivering,
		Attempts: attempts, CreatedAt: now.Add(-time.Minute), RepresentationVersion: 2}, LeaseVersion: 2}
}
