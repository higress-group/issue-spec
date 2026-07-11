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
	if reloaded.SchemaVersion != 3 || string(reloaded.Deliveries[first.DeliveryID].RawEnvelope) != string(first.RawEnvelope) {
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
