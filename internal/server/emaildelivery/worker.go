package emaildelivery

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
)

type Queue interface {
	ClaimOne(context.Context, time.Time, time.Duration) (*Claim, error)
	Succeed(context.Context, *Claim, time.Time) error
	Retry(context.Context, *Claim, time.Time, time.Time, ReasonCode) error
	Fail(context.Context, *Claim, time.Time, ReasonCode) error
	Suppress(context.Context, *Claim, time.Time, ReasonCode) error
}

type WorkerConfig struct {
	LeaseDuration  time.Duration
	PollInterval   time.Duration
	MaxConcurrency int
	Clock          func() time.Time
}

type Worker struct {
	queue      Queue
	preparer   Preparer
	sender     Sender
	config     WorkerConfig
	stopClaims chan struct{}
	stopOnce   sync.Once
}

func NewWorker(queue Queue, preparer Preparer, sender Sender, config WorkerConfig) (*Worker, error) {
	if queue == nil || preparer == nil || sender == nil {
		return nil, errors.New("email delivery: queue, preparer and sender are required")
	}
	if config.LeaseDuration <= 0 {
		config.LeaseDuration = DefaultLease
	}
	if config.PollInterval <= 0 {
		config.PollInterval = DefaultPoll
	}
	if config.MaxConcurrency <= 0 {
		config.MaxConcurrency = DefaultConcurrent
	}
	if config.MaxConcurrency > 64 {
		return nil, errors.New("email delivery: worker concurrency exceeds 64")
	}
	if config.Clock == nil {
		config.Clock = func() time.Time { return time.Now().UTC() }
	}
	return &Worker{queue: queue, preparer: preparer, sender: sender, config: config,
		stopClaims: make(chan struct{})}, nil
}

// StopClaims prevents new queue claims while allowing an already claimed send
// to finish inside the composition owner's bounded drain window.
func (w *Worker) StopClaims() {
	if w == nil {
		return
	}
	w.stopOnce.Do(func() { close(w.stopClaims) })
}

func (w *Worker) stopping() bool {
	select {
	case <-w.stopClaims:
		return true
	default:
		return false
	}
}

func (w *Worker) ProcessOne(ctx context.Context) error {
	claim, err := w.queue.ClaimOne(ctx, w.config.Clock(), w.config.LeaseDuration)
	if err != nil {
		return err
	}
	message, err := w.preparer.Prepare(ctx, claim.Delivery)
	if err == nil {
		if message.DeliveryID == uuid.Nil {
			message.DeliveryID = claim.ID
		}
		if message.DeliveryID != claim.ID {
			err = Permanent(ReasonInvalidMessage)
		}
		if message.OccurredAt.IsZero() {
			message.OccurredAt = claim.CreatedAt
		}
	}
	if err == nil {
		err = w.sender.Send(ctx, message)
	}
	completed := w.config.Clock()
	if err == nil {
		return w.queue.Succeed(ctx, claim, completed)
	}
	disposition := outcome(err)
	if disposition.Suppressed {
		return w.queue.Suppress(ctx, claim, completed, disposition.Reason)
	}
	if disposition.Retryable && claim.Attempts < MaxAttempts {
		return w.queue.Retry(ctx, claim, nextRetry(completed, claim.Attempts), completed, disposition.Reason)
	}
	return w.queue.Fail(ctx, claim, completed, disposition.Reason)
}

// Run starts a bounded number of workers and returns only on quiesce,
// cancellation, or an unexpected queue/finalization error.
func (w *Worker) Run(ctx context.Context) error {
	if ctx.Err() != nil {
		return nil
	}
	workerContext, cancel := context.WithCancel(ctx)
	defer cancel()
	errorsCh := make(chan error, w.config.MaxConcurrency)
	var workers sync.WaitGroup
	for range w.config.MaxConcurrency {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				if w.stopping() {
					return
				}
				err := w.ProcessOne(workerContext)
				if err != nil && !errors.Is(err, ErrNoWork) {
					if workerContext.Err() != nil {
						return
					}
					select {
					case errorsCh <- err:
						cancel()
					default:
					}
					return
				}
				if errors.Is(err, ErrNoWork) {
					timer := time.NewTimer(w.config.PollInterval)
					select {
					case <-workerContext.Done():
						timer.Stop()
						return
					case <-w.stopClaims:
						timer.Stop()
						return
					case <-timer.C:
					}
				}
			}
		}()
	}
	done := make(chan struct{})
	go func() {
		workers.Wait()
		close(done)
	}()
	select {
	case <-ctx.Done():
		cancel()
		<-done
		return nil
	case <-w.stopClaims:
		<-done
		return nil
	case err := <-errorsCh:
		cancel()
		<-done
		return err
	case <-done:
		select {
		case err := <-errorsCh:
			return err
		default:
			return nil
		}
	}
}

func nextRetry(now time.Time, attempt int) time.Time {
	delay := InitialBackoff
	for current := 1; current < attempt && delay < MaximumBackoff; current++ {
		if delay > MaximumBackoff/2 {
			delay = MaximumBackoff
			break
		}
		delay *= 2
	}
	if delay > MaximumBackoff {
		delay = MaximumBackoff
	}
	return now.Add(delay)
}
