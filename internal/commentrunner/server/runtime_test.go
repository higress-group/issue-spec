package server

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/commentrunner/jobs"
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
	for !log.has("dispatcher-reconcile") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
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
func (f *fakeRuntimeDispatcher) RunReady(ctx context.Context, _ int) (jobs.Result, error) {
	<-ctx.Done()
	f.log.add("dispatcher-revoked")
	return jobs.Result{}, ctx.Err()
}
