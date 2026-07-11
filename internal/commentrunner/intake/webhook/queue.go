// Package webhook implements the durable, untrusted webhook intake boundary
// used by self-hosted runners. Parsing commands and creating jobs belongs to
// PROCESS-022; this package only persists and leases immutable deliveries.
package webhook

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/commentrunner/state"
)

var (
	ErrConflict  = errors.New("webhook delivery identity conflict")
	ErrCapacity  = errors.New("webhook delivery queue capacity exceeded")
	ErrNoPending = errors.New("no pending webhook delivery")
	ErrLeaseLost = errors.New("webhook delivery lease lost")
	ErrInvalid   = errors.New("invalid webhook delivery")
)

type QueueConfig struct {
	MaxActiveDeliveries int
	MaxItemBytes        int64
	MaxTotalBytes       int64
}

type Queue struct {
	store  state.StateStore
	config QueueConfig
}

type Acceptance struct {
	Delivery  state.WebhookDelivery
	Duplicate bool
}

// DeliveryQueue is the PROCESS-022 handoff. Implementations must preserve
// claim atomicity and lease fencing; callers must not parse RawEnvelope before
// a successful claim.
type DeliveryQueue interface {
	Claim(context.Context, string, time.Duration, time.Time) (state.WebhookDelivery, error)
	Release(context.Context, string, string, string, time.Time) error
	Complete(context.Context, string, string, string, time.Time) error
	Fail(context.Context, string, string, string, time.Time, string) error
}

func NewQueue(store state.StateStore, config QueueConfig) (*Queue, error) {
	if store == nil {
		return nil, errors.New("webhook queue: state store is required")
	}
	if config.MaxActiveDeliveries <= 0 {
		config.MaxActiveDeliveries = 1000
	}
	if config.MaxItemBytes <= 0 {
		config.MaxItemBytes = 1 << 20
	}
	if config.MaxTotalBytes <= 0 {
		config.MaxTotalBytes = 16 << 20
	}
	if config.MaxActiveDeliveries > 10000 || config.MaxItemBytes > 16<<20 ||
		config.MaxTotalBytes > 256<<20 || config.MaxTotalBytes < config.MaxItemBytes {
		return nil, errors.New("webhook queue: limits exceed safe bounds or total bytes do not cover one item")
	}
	return &Queue{store: store, config: config}, nil
}

func (q *Queue) Accept(ctx context.Context, delivery state.WebhookDelivery) (Acceptance, error) {
	if err := validateDelivery(delivery, q.config.MaxItemBytes); err != nil {
		return Acceptance{}, err
	}
	var result Acceptance
	var conflict, capacity bool
	err := q.store.Update(ctx, func(current *state.RunnerState) error {
		current.Normalize()
		if existing, ok := current.Deliveries[delivery.DeliveryID]; ok {
			if existing.BodySHA256 == delivery.BodySHA256 && existing.EventID == delivery.EventID &&
				existing.SubscriptionID == delivery.SubscriptionID {
				result = Acceptance{Delivery: existing, Duplicate: true}
				return nil
			}
			existing.ConflictCount++
			existing.LastConflictAt = delivery.ReceivedAt
			current.Deliveries[delivery.DeliveryID] = existing
			conflict = true
			return nil
		}
		active, totalBytes := 0, int64(0)
		for _, existing := range current.Deliveries {
			if existing.Status == state.DeliveryPending || existing.Status == state.DeliveryProcessing {
				active++
				totalBytes += int64(len(existing.RawEnvelope))
			}
		}
		if active >= q.config.MaxActiveDeliveries || totalBytes+int64(len(delivery.RawEnvelope)) > q.config.MaxTotalBytes {
			capacity = true
			return nil
		}
		delivery.RawEnvelope = append([]byte(nil), delivery.RawEnvelope...)
		delivery.Status = state.DeliveryPending
		current.Deliveries[delivery.DeliveryID] = delivery
		result = Acceptance{Delivery: delivery}
		return nil
	})
	if err != nil {
		return Acceptance{}, fmt.Errorf("persist webhook delivery: %w", err)
	}
	if conflict {
		return Acceptance{}, ErrConflict
	}
	if capacity {
		return Acceptance{}, ErrCapacity
	}
	return result, nil
}

func (q *Queue) Claim(ctx context.Context, owner string, lease time.Duration, now time.Time) (state.WebhookDelivery, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" || lease <= 0 || now.IsZero() {
		return state.WebhookDelivery{}, ErrInvalid
	}
	var claimed state.WebhookDelivery
	found := false
	err := q.store.Update(ctx, func(current *state.RunnerState) error {
		current.Normalize()
		candidates := make([]state.WebhookDelivery, 0)
		for id, delivery := range current.Deliveries {
			if delivery.Status == state.DeliveryProcessing && !delivery.LeaseUntil.After(now) {
				delivery.Status = state.DeliveryPending
				delivery.LeaseOwner = ""
				delivery.LeaseToken = ""
				delivery.LeaseUntil = time.Time{}
				current.Deliveries[id] = delivery
			}
			if delivery.Status == state.DeliveryPending {
				candidates = append(candidates, delivery)
			}
		}
		sort.Slice(candidates, func(i, j int) bool {
			if candidates[i].ReceivedAt.Equal(candidates[j].ReceivedAt) {
				return candidates[i].DeliveryID < candidates[j].DeliveryID
			}
			return candidates[i].ReceivedAt.Before(candidates[j].ReceivedAt)
		})
		if len(candidates) == 0 {
			return nil
		}
		claimed = candidates[0]
		claimed.Status = state.DeliveryProcessing
		claimed.LeaseOwner = owner
		claimed.LeaseToken = uuid.NewString()
		claimed.LeaseUntil = now.Add(lease)
		claimed.Attempt++
		current.Deliveries[claimed.DeliveryID] = claimed
		found = true
		return nil
	})
	if err != nil {
		return state.WebhookDelivery{}, err
	}
	if !found {
		return state.WebhookDelivery{}, ErrNoPending
	}
	claimed.RawEnvelope = append([]byte(nil), claimed.RawEnvelope...)
	return claimed, nil
}

func (q *Queue) Release(ctx context.Context, deliveryID, owner, leaseToken string, now time.Time) error {
	return q.finish(ctx, deliveryID, owner, leaseToken, now, state.DeliveryPending, "")
}

func (q *Queue) Complete(ctx context.Context, deliveryID, owner, leaseToken string, now time.Time) error {
	return q.finish(ctx, deliveryID, owner, leaseToken, now, state.DeliveryCompleted, "")
}

func (q *Queue) Fail(ctx context.Context, deliveryID, owner, leaseToken string, now time.Time, safeDiagnostic string) error {
	return q.finish(ctx, deliveryID, owner, leaseToken, now, state.DeliveryFailed, safeDiagnostic)
}

func (q *Queue) finish(ctx context.Context, deliveryID, owner, leaseToken string, now time.Time,
	next state.DeliveryStatus, diagnostic string) error {
	deliveryID, owner, leaseToken = strings.TrimSpace(deliveryID), strings.TrimSpace(owner), strings.TrimSpace(leaseToken)
	if deliveryID == "" || owner == "" || leaseToken == "" || now.IsZero() {
		return ErrInvalid
	}
	lost := false
	err := q.store.Update(ctx, func(current *state.RunnerState) error {
		delivery, ok := current.Deliveries[deliveryID]
		if !ok || delivery.Status != state.DeliveryProcessing || delivery.LeaseOwner != owner ||
			delivery.LeaseToken != leaseToken || !delivery.LeaseUntil.After(now) {
			lost = true
			return nil
		}
		delivery.Status = next
		delivery.LeaseOwner = ""
		delivery.LeaseToken = ""
		delivery.LeaseUntil = time.Time{}
		if next.Terminal() {
			delivery.CompletedAt = now
		}
		if len(diagnostic) > 256 {
			diagnostic = diagnostic[:256]
		}
		delivery.LastError = safeDiagnosticCode(diagnostic)
		current.Deliveries[deliveryID] = delivery
		return nil
	})
	if err != nil {
		return err
	}
	if lost {
		return ErrLeaseLost
	}
	return nil
}

func safeDiagnosticCode(value string) string {
	switch strings.TrimSpace(value) {
	case "invalid_envelope", "unauthorized_command", "processing_failed", "writeback_failed", "binding_unavailable":
		return strings.TrimSpace(value)
	case "":
		return ""
	default:
		return "processing_failed"
	}
}

func validateDelivery(delivery state.WebhookDelivery, maxBytes int64) error {
	if strings.TrimSpace(delivery.DeliveryID) == "" || strings.TrimSpace(delivery.EventID) == "" ||
		strings.TrimSpace(delivery.SubscriptionID) == "" || delivery.ReceivedAt.IsZero() ||
		len(delivery.RawEnvelope) == 0 || int64(len(delivery.RawEnvelope)) > maxBytes {
		return ErrInvalid
	}
	for _, id := range []string{delivery.DeliveryID, delivery.EventID, delivery.SubscriptionID} {
		parsed, err := uuid.Parse(id)
		if err != nil || parsed == uuid.Nil {
			return ErrInvalid
		}
	}
	digest := sha256.Sum256(delivery.RawEnvelope)
	if !strings.EqualFold(delivery.BodySHA256, hex.EncodeToString(digest[:])) {
		return ErrInvalid
	}
	return nil
}
