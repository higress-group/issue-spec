package server

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/commentrunner/jobs"
	"github.com/higress-group/issue-spec/internal/commentrunner/state"
)

func TestRuntimeStartsReconcileAndShutsDownInOwnershipOrder(t *testing.T) {
	log := &runtimeLog{}
	http := &fakeRuntimeHTTP{log: log}
	reconciler := &fakeRuntimeReconciler{log: log}
	dispatcher := &fakeRuntimeDispatcher{log: log}
	runtime, err := NewRuntime(RuntimeConfig{HTTP: http, Reconciler: reconciler, Dispatcher: dispatcher,
		MaxConcurrentJobs: 2, DispatchIdleDelay: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- runtime.Run(ctx) }()
	deadline := time.Now().Add(time.Second)
	for (!log.has("dispatcher-reconcile") || !log.has("dispatcher-cancellation-drain")) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !log.has("dispatcher-cancellation-drain") {
		t.Fatalf("runtime did not start cancellation drain: %v", log.entries())
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	entries := log.entries()
	assertRuntimeOrder(t, entries, "http-stop-accepting", "http-stopped")
	assertRuntimeOrder(t, entries, "http-stopped", "reconciler-released")
	assertRuntimeOrder(t, entries, "reconciler-released", "dispatcher-revoked")
}

func TestRuntimeJobLoopSurvivesBenignCancelledDispatchCompletion(t *testing.T) {
	log := &runtimeLog{}
	dispatcher := &benignCancellationRuntimeDispatcher{secondCall: make(chan struct{})}
	runtime, err := NewRuntime(RuntimeConfig{
		HTTP: &fakeRuntimeHTTP{log: log}, Reconciler: &fakeRuntimeReconciler{log: log}, Dispatcher: dispatcher,
		MaxConcurrentJobs: 1, DispatchIdleDelay: time.Millisecond, CancelIdleDelay: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- runtime.Run(ctx) }()
	select {
	case <-dispatcher.secondCall:
		// The job loop consumed the benign cancellation result and entered its
		// next iteration instead of terminating runner serve.
	case err := <-done:
		t.Fatalf("runner serve exited after benign cancellation: %v", err)
	case <-time.After(time.Second):
		t.Fatal("runner job loop did not continue after benign cancellation")
	}
	select {
	case err := <-done:
		t.Fatalf("runner serve exited before shutdown: %v", err)
	default:
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

type runtimeLog struct {
	mu    sync.Mutex
	items []string
}

func (l *runtimeLog) add(item string) { l.mu.Lock(); l.items = append(l.items, item); l.mu.Unlock() }
func (l *runtimeLog) entries() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.items...)
}
func (l *runtimeLog) has(item string) bool {
	for _, entry := range l.entries() {
		if entry == item {
			return true
		}
	}
	return false
}

func assertRuntimeOrder(t *testing.T, entries []string, before, after string) {
	t.Helper()
	positions := map[string]int{}
	found := map[string]bool{}
	for index, entry := range entries {
		positions[entry] = index
		found[entry] = true
	}
	if !found[before] || !found[after] || positions[before] >= positions[after] {
		t.Fatalf("runtime order %s before %s not preserved: %v", before, after, entries)
	}
}

type fakeRuntimeHTTP struct{ log *runtimeLog }

func (f *fakeRuntimeHTTP) Run(ctx context.Context) error {
	<-ctx.Done()
	f.log.add("http-stopped")
	return nil
}
func (f *fakeRuntimeHTTP) StopAccepting() { f.log.add("http-stop-accepting") }

type fakeRuntimeReconciler struct{ log *runtimeLog }

func (f *fakeRuntimeReconciler) Run(ctx context.Context) error {
	<-ctx.Done()
	f.log.add("reconciler-released")
	return nil
}

type fakeRuntimeDispatcher struct{ log *runtimeLog }

func (f *fakeRuntimeDispatcher) Reconcile(context.Context) (jobs.ReconcileResult, error) {
	f.log.add("dispatcher-reconcile")
	return jobs.ReconcileResult{}, nil
}
func (f *fakeRuntimeDispatcher) RunJobsReady(ctx context.Context, _ int) (jobs.Result, error) {
	<-ctx.Done()
	f.log.add("dispatcher-revoked")
	return jobs.Result{}, ctx.Err()
}
func (f *fakeRuntimeDispatcher) DrainCancellations(context.Context, int) (jobs.Result, error) {
	f.log.add("dispatcher-cancellation-drain")
	return jobs.Result{Reason: "no queued cancellations"}, nil
}

type benignCancellationRuntimeDispatcher struct {
	mu         sync.Mutex
	calls      int
	secondCall chan struct{}
	once       sync.Once
}

func (*benignCancellationRuntimeDispatcher) Reconcile(context.Context) (jobs.ReconcileResult, error) {
	return jobs.ReconcileResult{}, nil
}

func (f *benignCancellationRuntimeDispatcher) RunJobsReady(ctx context.Context, _ int) (jobs.Result, error) {
	f.mu.Lock()
	f.calls++
	call := f.calls
	f.mu.Unlock()
	if call == 1 {
		return jobs.Result{Executed: true, JobID: "job-cancelled", Status: state.StatusCancelled, Reason: "cancelled_during_dispatch"}, nil
	}
	f.once.Do(func() { close(f.secondCall) })
	<-ctx.Done()
	return jobs.Result{}, ctx.Err()
}

func (*benignCancellationRuntimeDispatcher) DrainCancellations(ctx context.Context, _ int) (jobs.Result, error) {
	select {
	case <-ctx.Done():
		return jobs.Result{}, ctx.Err()
	case <-time.After(time.Millisecond):
		return jobs.Result{Reason: "no queued cancellations"}, nil
	}
}
