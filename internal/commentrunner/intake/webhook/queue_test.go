package webhook

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/commentrunner/state"
)

func TestQueuePersistsDeduplicatesConflictsAndRestarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store, err := state.OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	queue, _ := NewQueue(store, QueueConfig{MaxActiveDeliveries: 2, MaxItemBytes: 1024, MaxTotalBytes: 1024})
	now := time.Now().UTC()
	first := testDelivery("delivery-1", []byte(`{"event_id":"one"}`), now)
	if result, err := queue.Accept(t.Context(), first); err != nil || result.Duplicate {
		t.Fatalf("first acceptance=%+v err=%v", result, err)
	}
	if result, err := queue.Accept(t.Context(), first); err != nil || !result.Duplicate {
		t.Fatalf("duplicate acceptance=%+v err=%v", result, err)
	}
	conflict := testDelivery(first.DeliveryID, []byte(`{"event_id":"other"}`), now.Add(time.Second))
	if _, err := queue.Accept(t.Context(), conflict); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict error=%v", err)
	}
	loaded, _ := store.Load(t.Context())
	if loaded.Deliveries[first.DeliveryID].ConflictCount != 1 ||
		string(loaded.Deliveries[first.DeliveryID].RawEnvelope) != string(first.RawEnvelope) {
		t.Fatalf("conflict mutated authority: %+v", loaded.Deliveries[first.DeliveryID])
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := state.OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reloaded, err := reopened.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.SchemaVersion != state.SchemaVersion || string(reloaded.Deliveries[first.DeliveryID].RawEnvelope) != string(first.RawEnvelope) {
		t.Fatalf("delivery did not survive restart: %+v", reloaded.Deliveries[first.DeliveryID])
	}
}

func TestQueueCapacityCountsItemsAndTotalBytesButAllowsReplay(t *testing.T) {
	store, _ := state.OpenFileStore(filepath.Join(t.TempDir(), "state.json"))
	defer store.Close()
	queue, _ := NewQueue(store, QueueConfig{MaxActiveDeliveries: 1, MaxItemBytes: 32, MaxTotalBytes: 32})
	now := time.Unix(1000, 0).UTC()
	first := testDelivery("delivery-1", []byte("1234567890"), now)
	if _, err := queue.Accept(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	if result, err := queue.Accept(t.Context(), first); err != nil || !result.Duplicate {
		t.Fatalf("replay at cap=%+v err=%v", result, err)
	}
	if _, err := queue.Accept(t.Context(), testDelivery("delivery-2", []byte("x"), now)); !errors.Is(err, ErrCapacity) {
		t.Fatalf("item cap error=%v", err)
	}
	store2, _ := state.OpenFileStore(filepath.Join(t.TempDir(), "state.json"))
	defer store2.Close()
	bytesQueue, _ := NewQueue(store2, QueueConfig{MaxActiveDeliveries: 10, MaxItemBytes: 16, MaxTotalBytes: 16})
	if _, err := bytesQueue.Accept(t.Context(), testDelivery("delivery-a", []byte("1234567890"), now)); err != nil {
		t.Fatal(err)
	}
	if _, err := bytesQueue.Accept(t.Context(), testDelivery("delivery-b", []byte("1234567"), now)); !errors.Is(err, ErrCapacity) {
		t.Fatalf("total byte cap error=%v", err)
	}
}

func TestQueueLeaseFencingRecoveryAndDualClaimants(t *testing.T) {
	store, _ := state.OpenFileStore(filepath.Join(t.TempDir(), "state.json"))
	defer store.Close()
	queue, _ := NewQueue(store, QueueConfig{MaxItemBytes: 1024, MaxTotalBytes: 4096})
	now := time.Now().UTC()
	for index, id := range []string{"delivery-1", "delivery-2"} {
		if _, err := queue.Accept(t.Context(), testDelivery(id, []byte(id), now.Add(time.Duration(index)*time.Second))); err != nil {
			t.Fatal(err)
		}
	}
	var claims [2]state.WebhookDelivery
	var errs [2]error
	var wait sync.WaitGroup
	for index := range 2 {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			claims[index], errs[index] = queue.Claim(context.Background(), "worker-"+string(rune('a'+index)), time.Minute, now)
		}(index)
	}
	wait.Wait()
	if errs[0] != nil || errs[1] != nil || claims[0].DeliveryID == claims[1].DeliveryID ||
		claims[0].LeaseToken == "" || claims[1].LeaseToken == "" {
		t.Fatalf("dual claims=%+v/%+v errors=%v/%v", claims[0], claims[1], errs[0], errs[1])
	}

	// An expired lease cannot finish even before another claimant recovers it.
	if err := queue.Complete(t.Context(), claims[0].DeliveryID, claims[0].LeaseOwner,
		claims[0].LeaseToken, claims[0].LeaseUntil); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("expired lease completion error=%v", err)
	}
	// Recover using the same owner to prove the token, not owner text, fences a stale worker.
	reclaimed, err := queue.Claim(t.Context(), claims[0].LeaseOwner, time.Minute, claims[0].LeaseUntil)
	if err != nil || reclaimed.LeaseToken == claims[0].LeaseToken {
		t.Fatalf("reclaimed=%+v err=%v", reclaimed, err)
	}
	if err := queue.Complete(t.Context(), claims[0].DeliveryID, claims[0].LeaseOwner,
		claims[0].LeaseToken, claims[0].LeaseUntil.Add(time.Second)); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale same-owner token completion error=%v", err)
	}
	if _, err := queue.RecordDecision(t.Context(), reclaimed.DeliveryID, reclaimed.LeaseOwner,
		reclaimed.LeaseToken, reclaimed.LeaseUntil.Add(-2*time.Second), DurableDecision{
			Outcome: state.DeliveryOutcomeIgnored, AuthoritativeRevision: 1,
		}, nil); err != nil {
		t.Fatalf("record ignored decision: %v", err)
	}
	if err := queue.Complete(t.Context(), reclaimed.DeliveryID, reclaimed.LeaseOwner,
		reclaimed.LeaseToken, reclaimed.LeaseUntil.Add(-time.Second)); err != nil {
		t.Fatalf("current lease completion error=%v", err)
	}
	loaded, _ := store.Load(t.Context())
	delivery, ok := loaded.Deliveries[reclaimed.DeliveryID]
	if !ok || delivery.Status != state.DeliveryCompleted || len(delivery.RawEnvelope) != 0 {
		t.Fatalf("terminal delivery not compacted: %+v", delivery)
	}
}

func TestQueueDurableDecisionPrecedesAcknowledgementAndCompletion(t *testing.T) {
	store, _ := state.OpenFileStore(filepath.Join(t.TempDir(), "state.json"))
	defer store.Close()
	queue, _ := NewQueue(store, QueueConfig{MaxItemBytes: 1024, MaxTotalBytes: 4096})
	now := time.Now().UTC()
	delivery := testDelivery("delivery-decision", []byte("decision"), now)
	if _, err := queue.Accept(t.Context(), delivery); err != nil {
		t.Fatal(err)
	}
	claim, err := queue.Claim(t.Context(), "worker", time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.Complete(t.Context(), claim.DeliveryID, claim.LeaseOwner, claim.LeaseToken, now); !errors.Is(err, ErrDecisionRequired) {
		t.Fatalf("completion before decision error=%v", err)
	}
	decision := DurableDecision{Outcome: state.DeliveryOutcomeJob, JobID: "job-1", AckRequired: true, AuthoritativeRevision: 3}
	recorded, err := queue.RecordDecision(t.Context(), claim.DeliveryID, claim.LeaseOwner, claim.LeaseToken, now, decision,
		func(current *state.RunnerState) error {
			_, _, err := current.CreateCommandJob(state.Job{ID: "job-1", Repo: "o/r", Status: state.StatusQueued,
				CommandIdempotencyKey: "command-1"})
			return err
		})
	if err != nil || recorded.JobID != "job-1" || !recorded.AckPending {
		t.Fatalf("recorded=%+v err=%v", recorded, err)
	}
	if err := queue.Complete(t.Context(), claim.DeliveryID, claim.LeaseOwner, claim.LeaseToken, now); !errors.Is(err, ErrAcknowledgementPending) {
		t.Fatalf("completion before ack error=%v", err)
	}
	if err := queue.MarkAcknowledged(t.Context(), claim.DeliveryID, claim.LeaseOwner, claim.LeaseToken, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := queue.MarkAcknowledged(t.Context(), claim.DeliveryID, claim.LeaseOwner, claim.LeaseToken, now.Add(2*time.Second)); err != nil {
		t.Fatalf("idempotent ack: %v", err)
	}
	if err := queue.Complete(t.Context(), claim.DeliveryID, claim.LeaseOwner, claim.LeaseToken, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	loaded, _ := store.Load(t.Context())
	got := loaded.Deliveries[claim.DeliveryID]
	if got.Status != state.DeliveryCompleted || got.AckPending || got.AckCompletedAt.IsZero() || got.JobID != "job-1" || len(loaded.Jobs) != 1 {
		t.Fatalf("durable decision not preserved: delivery=%+v jobs=%+v", got, loaded.Jobs)
	}
}

func TestQueueDecisionAndJobAreAtomicWhenSaveFails(t *testing.T) {
	base, _ := state.OpenFileStore(filepath.Join(t.TempDir(), "state.json"))
	defer base.Close()
	failing := &failUpdateStore{StateStore: base}
	queue, _ := NewQueue(failing, QueueConfig{MaxItemBytes: 1024, MaxTotalBytes: 4096})
	now := time.Now().UTC()
	delivery := testDelivery("delivery-atomic", []byte("atomic"), now)
	if _, err := queue.Accept(t.Context(), delivery); err != nil {
		t.Fatal(err)
	}
	claim, err := queue.Claim(t.Context(), "worker", time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	failing.failNext = true
	decision := DurableDecision{Outcome: state.DeliveryOutcomeJob, JobID: "job-atomic", AckRequired: true, AuthoritativeRevision: 1}
	mutation := func(current *state.RunnerState) error {
		_, _, err := current.CreateCommandJob(state.Job{ID: "job-atomic", Repo: "o/r", CommandIdempotencyKey: "command-atomic"})
		return err
	}
	if _, err := queue.RecordDecision(t.Context(), claim.DeliveryID, claim.LeaseOwner, claim.LeaseToken, now, decision, mutation); err == nil {
		t.Fatal("expected injected save failure")
	}
	loaded, _ := base.Load(t.Context())
	if len(loaded.Jobs) != 0 || loaded.Deliveries[claim.DeliveryID].Outcome != "" {
		t.Fatalf("failed atomic update leaked state: %+v", loaded)
	}
	if _, err := queue.RecordDecision(t.Context(), claim.DeliveryID, claim.LeaseOwner, claim.LeaseToken, now, decision, mutation); err != nil {
		t.Fatalf("retry decision: %v", err)
	}
	// Replaying the same decision must not create a second job.
	if _, err := queue.RecordDecision(t.Context(), claim.DeliveryID, claim.LeaseOwner, claim.LeaseToken, now, decision, mutation); err != nil {
		t.Fatalf("duplicate decision: %v", err)
	}
	loaded, _ = base.Load(t.Context())
	if len(loaded.Jobs) != 1 || loaded.Deliveries[claim.DeliveryID].JobID != "job-atomic" {
		t.Fatalf("retry did not converge: %+v", loaded)
	}
}

func TestQueueCompletionFailsClosedOnCorruptAcknowledgementState(t *testing.T) {
	for _, test := range []struct {
		name     string
		decision DurableDecision
		mutate   DecisionMutation
		corrupt  func(*state.WebhookDelivery)
	}{
		{
			name: "required ack timestamp missing",
			decision: DurableDecision{Outcome: state.DeliveryOutcomeJob, JobID: "job-corrupt",
				AckRequired: true, AuthoritativeRevision: 1},
			mutate: func(current *state.RunnerState) error {
				return current.UpsertJob(state.Job{ID: "job-corrupt", CommandIdempotencyKey: "cmd-corrupt"})
			},
			corrupt: func(delivery *state.WebhookDelivery) { delivery.AckPending = false },
		},
		{
			name:     "ignored outcome has ack residue",
			decision: DurableDecision{Outcome: state.DeliveryOutcomeIgnored, AuthoritativeRevision: 1},
			corrupt:  func(delivery *state.WebhookDelivery) { delivery.AckCompletedAt = time.Now().UTC() },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, _ := state.OpenFileStore(filepath.Join(t.TempDir(), "state.json"))
			defer store.Close()
			queue, _ := NewQueue(store, QueueConfig{MaxItemBytes: 1024, MaxTotalBytes: 4096})
			now := time.Now().UTC()
			delivery := testDelivery("delivery-"+test.name, []byte(test.name), now)
			_, _ = queue.Accept(t.Context(), delivery)
			claim, _ := queue.Claim(t.Context(), "worker", time.Minute, now)
			if _, err := queue.RecordDecision(t.Context(), claim.DeliveryID, claim.LeaseOwner, claim.LeaseToken,
				now, test.decision, test.mutate); err != nil {
				t.Fatal(err)
			}
			if err := store.Update(t.Context(), func(current *state.RunnerState) error {
				item := current.Deliveries[claim.DeliveryID]
				test.corrupt(&item)
				current.Deliveries[claim.DeliveryID] = item
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := queue.Complete(t.Context(), claim.DeliveryID, claim.LeaseOwner, claim.LeaseToken,
				now.Add(time.Second)); !errors.Is(err, ErrInvalid) {
				t.Fatalf("corrupt completion error=%v", err)
			}
		})
	}
}

type failUpdateStore struct {
	state.StateStore
	failNext bool
}

func (s *failUpdateStore) Update(ctx context.Context, mutate func(*state.RunnerState) error) error {
	if !s.failNext {
		return s.StateStore.Update(ctx, mutate)
	}
	s.failNext = false
	current, err := s.StateStore.Load(ctx)
	if err != nil {
		return err
	}
	if err := mutate(&current); err != nil {
		return err
	}
	return errors.New("injected save failure")
}

func testDelivery(id string, body []byte, received time.Time) state.WebhookDelivery {
	namespace := uuid.MustParse("77777777-7777-4777-8777-777777777777")
	deliveryID := id
	if _, err := uuid.Parse(deliveryID); err != nil {
		deliveryID = uuid.NewSHA1(namespace, []byte(id)).String()
	}
	digest := sha256.Sum256(body)
	return state.WebhookDelivery{DeliveryID: deliveryID, EventID: uuid.NewSHA1(namespace, []byte("event-1")).String(),
		SubscriptionID: uuid.NewSHA1(namespace, []byte("subscription-1")).String(),
		BodySHA256:     hex.EncodeToString(digest[:]), RawEnvelope: append([]byte(nil), body...),
		ReceivedAt: received, Status: state.DeliveryPending}
}
