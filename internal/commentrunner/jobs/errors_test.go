package jobs

import (
	"errors"
	"fmt"
	"testing"
)

func TestIsOnlyTerminalJobFailures(t *testing.T) {
	firstCause := errors.New("prepare workspace")
	secondCause := errors.New("load issue context")
	first := terminalJobFailure(firstCause)
	second := fmt.Errorf("dispatch: %w", terminalJobFailure(secondCause))

	if !IsOnlyTerminalJobFailures(first) || !errors.Is(first, firstCause) {
		t.Fatalf("single terminal job failure was not classified or did not preserve its cause: %v", first)
	}
	if !IsOnlyTerminalJobFailures(errors.Join(first, second)) {
		t.Fatal("joined terminal job failures were not classified as job-scoped")
	}
	if IsOnlyTerminalJobFailures(errors.Join(first, errors.New("state store unavailable"))) {
		t.Fatal("mixed job and infrastructure failures were classified as job-scoped")
	}
}
