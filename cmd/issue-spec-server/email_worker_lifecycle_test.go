package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/server/config"
)

func TestOptionalEmailWorkerRequiresCapabilityAndRealPreparer(t *testing.T) {
	worker, err := composeEmailWorker(nil, config.MailSettings{}, nil, config.Config{})
	if err != nil || worker != nil {
		t.Fatalf("disabled email worker = %v/%v", worker, err)
	}
}

func TestWorkerGroupStopsClaimsAndDrainsEveryEnabledWorker(t *testing.T) {
	first, second := newLifecycleWorker(), newLifecycleWorker()
	ready := &readiness{}
	ready.worker.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	workers := []namedWorker{{name: "webhook delivery", worker: first}, {name: "email delivery", worker: second}}
	done := startWorkers(ctx, workers, ready)
	first.waitStarted(t)
	second.waitStarted(t)
	for _, item := range workers {
		item.worker.StopClaims()
	}
	waitCtx, waitCancel := context.WithTimeout(t.Context(), time.Second)
	defer waitCancel()
	if err := waitWorkers(waitCtx, done, cancel, len(workers)); err != nil {
		t.Fatal(err)
	}
	if first.stopCalls != 1 || second.stopCalls != 1 || ready.worker.Load() {
		t.Fatalf("workers stopped=%d/%d ready=%v", first.stopCalls, second.stopCalls, ready.worker.Load())
	}
}

func TestUnexpectedNamedWorkerExitFailsReadiness(t *testing.T) {
	want := errors.New("fixture worker failure")
	worker := newLifecycleWorker()
	worker.result = want
	worker.exitImmediately = true
	ready := &readiness{}
	ready.worker.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := startWorkers(ctx, []namedWorker{{name: "email delivery", worker: worker}}, ready)
	result := <-done
	if result.name != "email delivery" || !errors.Is(result.err, want) || ready.worker.Load() {
		t.Fatalf("exit = %+v ready=%v", result, ready.worker.Load())
	}
	if err := waitWorkers(t.Context(), done, cancel, 0); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerGroupCancellationIsBounded(t *testing.T) {
	worker := newLifecycleWorker()
	worker.ignoreStop = true
	ready := &readiness{}
	ready.worker.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	done := startWorkers(ctx, []namedWorker{{name: "stuck", worker: worker}}, ready)
	worker.waitStarted(t)
	waitCtx, waitCancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer waitCancel()
	err := waitWorkers(waitCtx, done, cancel, 1)
	if err == nil || (!errors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), "did not release")) {
		t.Fatalf("bounded wait error = %v", err)
	}
}

type lifecycleWorker struct {
	started         chan struct{}
	stop            chan struct{}
	stopOnce        sync.Once
	stopCalls       int
	result          error
	exitImmediately bool
	ignoreStop      bool
}

func newLifecycleWorker() *lifecycleWorker {
	return &lifecycleWorker{started: make(chan struct{}), stop: make(chan struct{})}
}

func (w *lifecycleWorker) Run(ctx context.Context) error {
	close(w.started)
	if w.exitImmediately {
		return w.result
	}
	if w.ignoreStop {
		<-ctx.Done()
		return nil
	}
	select {
	case <-w.stop:
		return w.result
	case <-ctx.Done():
		return nil
	}
}

func (w *lifecycleWorker) StopClaims() {
	w.stopOnce.Do(func() {
		w.stopCalls++
		close(w.stop)
	})
}

func (w *lifecycleWorker) waitStarted(t *testing.T) {
	t.Helper()
	select {
	case <-w.started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}
}
