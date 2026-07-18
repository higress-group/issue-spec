package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestProfileExpiryWorkerRunsAndStopsWithWorkerGroupContract(t *testing.T) {
	service := &countingExpirer{}
	worker, err := newProfileExpiryWorker(service, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- worker.Run(t.Context()) }()
	deadline := time.After(time.Second)
	for service.calls.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("expiry did not run")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	worker.StopClaims()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

type countingExpirer struct{ calls atomic.Int64 }

func (e *countingExpirer) Expire(context.Context, int) (int, error) {
	e.calls.Add(1)
	return 0, nil
}
