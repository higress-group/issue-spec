package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/higress-group/issue-spec/internal/commentrunner/jobs"
)

type IntakeServer interface {
	Run(context.Context) error
	StopAccepting()
}

type ReconcileLoop interface{ Run(context.Context) error }

type JobDispatcher interface {
	Reconcile(context.Context) (jobs.ReconcileResult, error)
	RunJobsReady(context.Context, int) (jobs.Result, error)
	DrainCancellations(context.Context, int) (jobs.Result, error)
}

type RuntimeConfig struct {
	HTTP              IntakeServer
	Reconciler        ReconcileLoop
	Dispatcher        JobDispatcher
	MaxConcurrentJobs int
	DispatchIdleDelay time.Duration
	CancelIdleDelay   time.Duration
}

type Runtime struct{ config RuntimeConfig }

func NewRuntime(config RuntimeConfig) (*Runtime, error) {
	if config.HTTP == nil || config.Reconciler == nil || config.Dispatcher == nil {
		return nil, errors.New("runner serve runtime requires HTTP intake, reconciler, and dispatcher")
	}
	if config.MaxConcurrentJobs <= 0 {
		config.MaxConcurrentJobs = 1
	}
	if config.MaxConcurrentJobs > 32 {
		return nil, errors.New("runner serve runtime job concurrency exceeds safe bound")
	}
	if config.DispatchIdleDelay <= 0 {
		config.DispatchIdleDelay = 250 * time.Millisecond
	}
	if config.CancelIdleDelay <= 0 {
		config.CancelIdleDelay = 100 * time.Millisecond
	}
	return &Runtime{config: config}, nil
}

// Run owns the long-running runner serve lifecycle. Shutdown ordering is
// deliberate: reject new HTTP deliveries, stop/release reconciliation claims,
// then stop dispatcher work (whose credential broker revokes job leases), and
// only then return so the command may close the shared state store.
func (r *Runtime) Run(ctx context.Context) error {
	base := context.WithoutCancel(ctx)
	httpCtx, stopHTTP := context.WithCancel(base)
	reconcileCtx, stopReconcile := context.WithCancel(base)
	dispatchCtx, stopDispatch := context.WithCancel(base)
	defer stopHTTP()
	defer stopReconcile()
	defer stopDispatch()
	httpDone, reconcileDone, dispatchDone := make(chan error, 1), make(chan error, 1), make(chan error, 1)
	go func() { httpDone <- r.config.HTTP.Run(httpCtx) }()
	go func() { reconcileDone <- r.config.Reconciler.Run(reconcileCtx) }()
	go func() { dispatchDone <- r.runDispatcher(dispatchCtx) }()

	var trigger error
	triggerSource := "context"
	select {
	case <-ctx.Done():
	case trigger = <-httpDone:
		triggerSource = "http"
	case trigger = <-reconcileDone:
		triggerSource = "reconciler"
	case trigger = <-dispatchDone:
		triggerSource = "dispatcher"
	}

	r.config.HTTP.StopAccepting()
	stopHTTP()
	if triggerSource != "http" {
		trigger = errors.Join(trigger, componentError("http", <-httpDone))
	}
	stopReconcile()
	if triggerSource != "reconciler" {
		trigger = errors.Join(trigger, componentError("reconciler", <-reconcileDone))
	}
	stopDispatch()
	if triggerSource != "dispatcher" {
		trigger = errors.Join(trigger, componentError("dispatcher", <-dispatchDone))
	}
	if triggerSource == "context" {
		return nil
	}
	if trigger == nil {
		return fmt.Errorf("runner serve %s stopped unexpectedly", triggerSource)
	}
	return componentError(triggerSource, trigger)
}

func (r *Runtime) runDispatcher(ctx context.Context) error {
	if _, err := r.config.Dispatcher.Reconcile(ctx); err != nil {
		return err
	}
	workerCtx, stop := context.WithCancel(ctx)
	defer stop()
	jobsDone, cancellationsDone := make(chan error, 1), make(chan error, 1)
	go func() { jobsDone <- r.runJobLoop(workerCtx) }()
	go func() { cancellationsDone <- r.runCancellationLoop(workerCtx) }()
	var first error
	select {
	case <-ctx.Done():
	case first = <-jobsDone:
		jobsDone = nil
	case first = <-cancellationsDone:
		cancellationsDone = nil
	}
	stop()
	if jobsDone != nil {
		first = errors.Join(first, componentError("job-dispatch", <-jobsDone))
	}
	if cancellationsDone != nil {
		first = errors.Join(first, componentError("cancellation-drain", <-cancellationsDone))
	}
	if ctx.Err() != nil {
		return nil
	}
	return first
}

func (r *Runtime) runJobLoop(ctx context.Context) error {
	for {
		result, err := r.config.Dispatcher.RunJobsReady(ctx, r.config.MaxConcurrentJobs)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if jobs.IsOnlyTerminalJobFailures(err) {
				continue
			}
			return err
		}
		if result.Executed {
			continue
		}
		timer := time.NewTimer(r.config.DispatchIdleDelay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil
		case <-timer.C:
		}
	}
}

func (r *Runtime) runCancellationLoop(ctx context.Context) error {
	for {
		result, err := r.config.Dispatcher.DrainCancellations(ctx, 32)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if result.Executed {
			continue
		}
		timer := time.NewTimer(r.config.CancelIdleDelay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil
		case <-timer.C:
		}
	}
}

func componentError(name string, err error) error {
	if err == nil || errors.Is(err, context.Canceled) {
		return nil
	}
	return fmt.Errorf("runner serve %s: %w", name, err)
}
