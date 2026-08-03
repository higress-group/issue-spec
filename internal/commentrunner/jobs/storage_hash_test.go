package jobs

import (
	"testing"

	"github.com/higress-group/issue-spec/internal/commentrunner/storage"
)

// The dispatcher delegates physical identity to the storage package; this
// guards the delegation so the two can never drift and orphan on-disk
// runtimes/pools.
func TestDispatcherHashDelegatesToStorage(t *testing.T) {
	for _, wsPath := range []string{"/tmp/root/ws-1", "/var/lib/runner/ws-abc", "relative/ws-2"} {
		got, err := stableSessionRuntimeRoot(wsPath, "o/r", "ps-1")
		if err != nil {
			t.Fatalf("stableSessionRuntimeRoot(%q): %v", wsPath, err)
		}
		want, err := storage.SessionRuntimeRoot(wsPath, "o/r", "ps-1")
		if err != nil {
			t.Fatalf("SessionRuntimeRoot(%q): %v", wsPath, err)
		}
		if got != want {
			t.Fatalf("runtime root drift for %q: %q vs %q", wsPath, got, want)
		}
	}
}
