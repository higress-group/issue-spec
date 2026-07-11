package delivery

import (
	"context"
	"testing"
	"time"
)

func TestQuiesceBeforeRunStopsWithoutClaiming(t *testing.T) {
	service := &Service{config: Config{MaxConcurrency: 2, PollInterval: time.Millisecond}, quiesce: make(chan struct{})}
	service.StopClaims()
	service.Quiesce()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := service.Run(ctx); err != nil {
		t.Fatal(err)
	}
}
