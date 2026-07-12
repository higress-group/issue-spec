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
	ErrConflict               = errors.New("webhook delivery identity conflict")
	ErrCapacity               = errors.New("webhook delivery queue capacity exceeded")
	ErrNoPending              = errors.New("no pending webhook delivery")
	ErrLeaseLost              = errors.New("webhook delivery lease lost")
	ErrInvalid                = errors.New("invalid webhook delivery")
	ErrDecisionRequired       = errors.New("webhook delivery durable decision required")
	ErrDecisionConflict       = errors.New("webhook delivery durable decision conflict")
	ErrAcknowledgementPending = errors.New("webhook delivery acknowledgement pending")
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

// DurableDecision links an immutable delivery to the durable control-plane
// record created from its authoritative comment revision. AckRequired keeps
// the delivery non-terminal until the corresponding remote acknowledgement is
// observed or written successfully.
type DurableDecision struct {
	Outcome               state.DeliveryOutcome
	JobID                 string
	CancellationID        string
	StatusWritebackKey    string
	AckRequired           bool
	AuthoritativeRevision int64
}

// DecisionMutation must be a pure local state mutation. It runs while the
// StateStore update is locked and must never perform network or filesystem
// side effects outside the store transaction.
type DecisionMutation func(*state.RunnerState) error

// DeliveryQueue is the PROCESS-022 handoff. Implementations must preserve
// claim atomicity and lease fencing; callers must not parse RawEnvelope before
// a successful claim.
type DeliveryQueue interface {
	Claim(context.Context, string, time.Duration, time.Time) (state.WebhookDelivery, error)
	RecordDecision(context.Context, string, string, string, time.Time, DurableDecision, DecisionMutation) (state.WebhookDelivery, error)
	MarkAcknowledged(context.Context, string, string, string, time.Time) error
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

// RecordDecision performs the command/cancellation/rejection mutation and
// delivery linkage in one lease-fenced StateStore.Update. Callers must perform
// remote eyes/status acknowledgements only after this method succeeds.
func (q *Queue) RecordDecision(ctx context.Context, deliveryID, owner, leaseToken string, now time.Time,
	decision DurableDecision, mutate DecisionMutation) (state.WebhookDelivery, error) {
	if err := validateDecision(decision); err != nil {
		return state.WebhookDelivery{}, err
	}
	deliveryID, owner, leaseToken = strings.TrimSpace(deliveryID), strings.TrimSpace(owner), strings.TrimSpace(leaseToken)
	if deliveryID == "" || owner == "" || leaseToken == "" || now.IsZero() {
		return state.WebhookDelivery{}, ErrInvalid
	}
	var recorded state.WebhookDelivery
	lost, conflict := false, false
	err := q.store.Update(ctx, func(current *state.RunnerState) error {
		current.Normalize()
		delivery, ok := current.Deliveries[deliveryID]
		if !claimActive(delivery, ok, owner, leaseToken, now) {
			lost = true
			return nil
		}
		if delivery.Outcome != "" {
			if !decisionMatches(delivery, decision) {
				conflict = true
				return nil
			}
			if err := validateDecisionLink(current, delivery); err != nil {
				return err
			}
			recorded = delivery
			return nil
		}
		if mutate != nil {
			if err := mutate(current); err != nil {
				return err
			}
		}
		delivery.Outcome = decision.Outcome
		delivery.JobID = strings.TrimSpace(decision.JobID)
		delivery.CancellationID = strings.TrimSpace(decision.CancellationID)
		delivery.StatusWritebackKey = strings.TrimSpace(decision.StatusWritebackKey)
		delivery.AckPending = decision.AckRequired
		delivery.AckCompletedAt = time.Time{}
		delivery.AuthoritativeRevision = decision.AuthoritativeRevision
		if err := validateDecisionLink(current, delivery); err != nil {
			return err
		}
		current.Deliveries[deliveryID] = delivery
		recorded = delivery
		return nil
	})
	if err != nil {
		return state.WebhookDelivery{}, err
	}
	if lost {
		return state.WebhookDelivery{}, ErrLeaseLost
	}
	if conflict {
		return state.WebhookDelivery{}, ErrDecisionConflict
	}
	return recorded, nil
}

func (q *Queue) MarkAcknowledged(ctx context.Context, deliveryID, owner, leaseToken string, now time.Time) error {
	deliveryID, owner, leaseToken = strings.TrimSpace(deliveryID), strings.TrimSpace(owner), strings.TrimSpace(leaseToken)
	if deliveryID == "" || owner == "" || leaseToken == "" || now.IsZero() {
		return ErrInvalid
	}
	lost, decisionMissing := false, false
	err := q.store.Update(ctx, func(current *state.RunnerState) error {
		delivery, ok := current.Deliveries[deliveryID]
		if !claimActive(delivery, ok, owner, leaseToken, now) {
			lost = true
			return nil
		}
		if delivery.Outcome == "" {
			decisionMissing = true
			return nil
		}
		if !delivery.AckPending {
			if delivery.AckCompletedAt.IsZero() {
				return ErrInvalid
			}
			return nil
		}
		delivery.AckPending = false
		delivery.AckCompletedAt = now
		current.Deliveries[deliveryID] = delivery
		return nil
	})
	if err != nil {
		return err
	}
	if lost {
		return ErrLeaseLost
	}
	if decisionMissing {
		return ErrDecisionRequired
	}
	return nil
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
	lost, decisionMissing, ackPending := false, false, false
	err := q.store.Update(ctx, func(current *state.RunnerState) error {
		delivery, ok := current.Deliveries[deliveryID]
		if !claimActive(delivery, ok, owner, leaseToken, now) {
			lost = true
			return nil
		}
		if next == state.DeliveryCompleted {
			if delivery.Outcome == "" {
				decisionMissing = true
				return nil
			}
			if err := validateDecisionLink(current, delivery); err != nil {
				return err
			}
			if delivery.AckPending || (ackRequiredOutcome(delivery.Outcome) && delivery.AckCompletedAt.IsZero()) {
				ackPending = true
				return nil
			}
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
	if decisionMissing {
		return ErrDecisionRequired
	}
	if ackPending {
		return ErrAcknowledgementPending
	}
	return nil
}

func claimActive(delivery state.WebhookDelivery, ok bool, owner, leaseToken string, now time.Time) bool {
	return ok && delivery.Status == state.DeliveryProcessing && delivery.LeaseOwner == owner &&
		delivery.LeaseToken == leaseToken && delivery.LeaseUntil.After(now)
}

func validateDecision(decision DurableDecision) error {
	decision.JobID = strings.TrimSpace(decision.JobID)
	decision.CancellationID = strings.TrimSpace(decision.CancellationID)
	decision.StatusWritebackKey = strings.TrimSpace(decision.StatusWritebackKey)
	if !decision.Outcome.Valid() || decision.AuthoritativeRevision <= 0 {
		return ErrInvalid
	}
	switch decision.Outcome {
	case state.DeliveryOutcomeJob:
		if decision.JobID == "" || decision.CancellationID != "" || decision.StatusWritebackKey != "" || !decision.AckRequired {
			return ErrInvalid
		}
	case state.DeliveryOutcomeCancellation:
		if decision.CancellationID == "" || decision.JobID != "" || decision.StatusWritebackKey != "" || !decision.AckRequired {
			return ErrInvalid
		}
	case state.DeliveryOutcomeRejected:
		if decision.StatusWritebackKey == "" || decision.JobID != "" || decision.CancellationID != "" || !decision.AckRequired {
			return ErrInvalid
		}
	case state.DeliveryOutcomeIgnored, state.DeliveryOutcomeSuperseded:
		if decision.JobID != "" || decision.CancellationID != "" || decision.StatusWritebackKey != "" || decision.AckRequired {
			return ErrInvalid
		}
	}
	return nil
}

func validateDecisionLink(current *state.RunnerState, delivery state.WebhookDelivery) error {
	switch delivery.Outcome {
	case state.DeliveryOutcomeJob:
		if _, ok := current.Jobs[delivery.JobID]; !ok {
			return fmt.Errorf("%w: linked job is missing", ErrInvalid)
		}
	case state.DeliveryOutcomeCancellation:
		if _, ok := current.Cancellations[delivery.CancellationID]; !ok {
			return fmt.Errorf("%w: linked cancellation is missing", ErrInvalid)
		}
	case state.DeliveryOutcomeRejected:
		if _, ok := current.StatusWritebacks[delivery.StatusWritebackKey]; !ok {
			return fmt.Errorf("%w: linked rejection writeback is missing", ErrInvalid)
		}
	case state.DeliveryOutcomeIgnored, state.DeliveryOutcomeSuperseded:
		if delivery.AckPending || !delivery.AckCompletedAt.IsZero() {
			return fmt.Errorf("%w: acknowledgement is forbidden for %s outcome", ErrInvalid, delivery.Outcome)
		}
	default:
		return fmt.Errorf("%w: invalid delivery outcome", ErrInvalid)
	}
	if ackRequiredOutcome(delivery.Outcome) && delivery.AckPending != delivery.AckCompletedAt.IsZero() {
		return fmt.Errorf("%w: acknowledgement state is inconsistent", ErrInvalid)
	}
	return nil
}

func ackRequiredOutcome(outcome state.DeliveryOutcome) bool {
	switch outcome {
	case state.DeliveryOutcomeJob, state.DeliveryOutcomeCancellation, state.DeliveryOutcomeRejected:
		return true
	default:
		return false
	}
}

func decisionMatches(delivery state.WebhookDelivery, decision DurableDecision) bool {
	return delivery.Outcome == decision.Outcome && delivery.JobID == strings.TrimSpace(decision.JobID) &&
		delivery.CancellationID == strings.TrimSpace(decision.CancellationID) &&
		delivery.StatusWritebackKey == strings.TrimSpace(decision.StatusWritebackKey) &&
		delivery.AuthoritativeRevision == decision.AuthoritativeRevision
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
