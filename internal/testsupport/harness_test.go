package testsupport

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// newTestHarness builds a harness rooted at an isolated parent so tests never
// touch the real OS temp directory.
func newTestHarness(t *testing.T) *Harness {
	t.Helper()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("eval parent: %v", err)
	}
	h, err := New(Options{Parent: parent, Tier: "test"})
	if err != nil {
		t.Fatalf("new harness: %v", err)
	}
	return h
}

func markerExists(t *testing.T, root string) bool {
	t.Helper()
	_, err := os.Stat(markerPath(root))
	return err == nil
}

// Scenario: normal and failing tests leave no owned resources.
func TestAcquireAndCleanupLeavesNothing(t *testing.T) {
	h := newTestHarness(t)
	root := h.AcquireRoot(t)

	if !markerExists(t, root.Path) {
		t.Fatalf("expected marker at %s", root.Path)
	}
	if got := h.Summary().RootsCreated; got != 1 {
		t.Fatalf("roots_created = %d, want 1", got)
	}

	if err := h.CleanupRoot(root.Path); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if _, err := os.Stat(root.Path); !os.IsNotExist(err) {
		t.Fatalf("root still present after cleanup: %v", err)
	}
	s := h.Summary()
	if s.RootsCleaned != 1 {
		t.Fatalf("roots_cleaned = %d, want 1", s.RootsCleaned)
	}
	// A second cleanup of an already-removed root is a no-op error, not a panic.
	if err := h.CleanupRoot(root.Path); err == nil {
		t.Fatalf("expected error cleaning up missing root")
	}
}

// Scenario: normal and failing tests leave no owned resources (per-test
// t.Cleanup path via AcquireRoot).
func TestAcquireRootRegistersTestCleanup(t *testing.T) {
	h := newTestHarness(t)
	var rootPath string
	t.Run("sub", func(t *testing.T) {
		root := h.AcquireRoot(t)
		rootPath = root.Path
		if !markerExists(t, rootPath) {
			t.Fatalf("expected marker")
		}
	})
	if _, err := os.Stat(rootPath); !os.IsNotExist(err) {
		t.Fatalf("expected root removed by t.Cleanup, stat err = %v", err)
	}
}

// Scenario: timeout and cancellation terminate the process tree.
func TestSpawnTerminatedOnCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process-group termination contract is Unix-only; Windows uses fallback")
	}
	h := newTestHarness(t)
	ctx, cancel := context.WithCancel(context.Background())

	// Shell spawns a long-lived grandchild in the same process group.
	child, err := h.Spawn(ctx, Command{
		Path: "/bin/sh",
		Args: []string{"-c", "sleep 300 & sleep 300"},
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	pid := child.PID()
	if pid <= 0 || !pidAlive(pid) {
		t.Fatalf("child not alive after spawn (pid=%d)", pid)
	}

	cancel()
	if err := child.Wait(); err == nil {
		t.Fatalf("expected non-nil error from cancelled child")
	}

	deadline := time.Now().Add(3 * time.Second)
	for pidAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if pidAlive(pid) {
		t.Fatalf("child pid %d still alive after cancellation", pid)
	}
	if s := h.Summary(); s.ChildrenReaped != 1 {
		t.Fatalf("children_reaped = %d, want 1", s.ChildrenReaped)
	}
}

// Scenario: timeout terminates the process tree (context deadline path).
func TestSpawnTerminatedOnTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process-group termination contract is Unix-only; Windows uses fallback")
	}
	h := newTestHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	child, err := h.Spawn(ctx, Command{Path: "/bin/sh", Args: []string{"-c", "sleep 300"}})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	pid := child.PID()
	if err := child.Wait(); err == nil {
		t.Fatalf("expected timeout error")
	}
	deadline := time.Now().Add(3 * time.Second)
	for pidAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if pidAlive(pid) {
		t.Fatalf("child pid %d still alive after timeout", pid)
	}
}

// Scenario: uncatchable termination is recovered on the next run.
func TestRecoverStaleRemovesDeadRun(t *testing.T) {
	h := newTestHarness(t)

	// Simulate a root left by a previous boot: same parent, valid marker, but a
	// boot id that differs from the current one so the run is provably dead.
	stale := filepath.Join(h.parent, "issue-spec-test-stale")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatalf("mkdir stale: %v", err)
	}
	writeStaleMarker(t, stale, "424242-previous-boot-abc123", 424242, h.parent)

	report, err := h.RecoverStale()
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if len(report.Recovered) != 1 || report.Recovered[0] != stale {
		t.Fatalf("recovered = %+v, want [%s]", report.Recovered, stale)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale root not removed: %v", err)
	}
	if h.Summary().RootsRecovered != 1 {
		t.Fatalf("roots_recovered = %d, want 1", h.Summary().RootsRecovered)
	}
}

// Scenario: unsafe cleanup candidate fails closed (multiple sub-cases).
func TestCleanupFailsClosed(t *testing.T) {
	t.Run("missing marker", func(t *testing.T) {
		h := newTestHarness(t)
		dir := filepath.Join(h.parent, "no-marker")
		mustMkdir(t, dir)
		if err := h.removeValidated(dir, false); err == nil {
			t.Fatalf("expected error for missing marker")
		}
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("dir must be retained: %v", err)
		}
	})

	t.Run("malformed marker", func(t *testing.T) {
		h := newTestHarness(t)
		dir := filepath.Join(h.parent, "bad-marker")
		mustMkdir(t, dir)
		if err := os.WriteFile(markerPath(dir), []byte("{not json"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := h.removeValidated(dir, false); err == nil {
			t.Fatalf("expected error for malformed marker")
		}
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("dir must be retained: %v", err)
		}
	})

	t.Run("wrong schema version", func(t *testing.T) {
		h := newTestHarness(t)
		dir := filepath.Join(h.parent, "wrong-schema")
		mustMkdir(t, dir)
		writeRawMarker(t, dir, marker{SchemaVersion: 99, RunID: "1-boot-aa", PID: 1, Parent: h.parent})
		if err := h.removeValidated(dir, false); err == nil {
			t.Fatalf("expected error for wrong schema version")
		}
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("dir must be retained: %v", err)
		}
	})

	t.Run("out of parent", func(t *testing.T) {
		h := newTestHarness(t)
		outside := filepath.Join(t.TempDir(), "outside")
		mustMkdir(t, outside)
		writeStaleMarker(t, outside, "1-previous-boot-aa", 1, h.parent)
		if err := h.removeValidated(outside, true); err == nil {
			t.Fatalf("expected error for out-of-parent candidate")
		}
		if _, err := os.Stat(outside); err != nil {
			t.Fatalf("out-of-parent dir must be retained: %v", err)
		}
	})

	t.Run("symlink escape", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation is unreliable on Windows CI")
		}
		h := newTestHarness(t)
		realOutside := filepath.Join(t.TempDir(), "target")
		mustMkdir(t, realOutside)
		writeStaleMarker(t, realOutside, "1-previous-boot-aa", 1, h.parent)
		link := filepath.Join(h.parent, "escape")
		if err := os.Symlink(realOutside, link); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		if err := h.removeValidated(link, true); err == nil {
			t.Fatalf("expected error for symlink escape")
		}
		if _, err := os.Stat(realOutside); err != nil {
			t.Fatalf("symlink target must be retained: %v", err)
		}
	})

	t.Run("active run retained by recovery", func(t *testing.T) {
		h := newTestHarness(t)
		dir := filepath.Join(h.parent, "active")
		mustMkdir(t, dir)
		// Current pid + current boot id => provably alive => must be retained.
		writeRawMarker(t, dir, marker{
			SchemaVersion: markerSchemaVersion,
			RunID:         newAliveRunID(),
			PID:           os.Getpid(),
			CreatedAt:     time.Now().UTC().Format(time.RFC3339Nano),
			Parent:        h.parent,
		})
		report, err := h.RecoverStale()
		if err != nil {
			t.Fatalf("recover: %v", err)
		}
		if len(report.Recovered) != 0 {
			t.Fatalf("active run must not be recovered: %+v", report.Recovered)
		}
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("active run dir must be retained: %v", err)
		}
	})
}

// Scenario: full suite proves zero leaks.
func TestFinalizeReportsZeroLeaks(t *testing.T) {
	h := newTestHarness(t)

	root, err := h.acquireRoot()
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := h.CleanupRoot(root.Path); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	if runtime.GOOS != "windows" {
		child, err := h.Spawn(context.Background(), Command{Path: "/bin/sh", Args: []string{"-c", "exit 0"}})
		if err != nil {
			t.Fatalf("spawn: %v", err)
		}
		if err := child.Wait(); err != nil {
			t.Fatalf("wait: %v", err)
		}
	}

	h.Finalize()
	if err := h.AssertNoLeaks(); err != nil {
		t.Fatalf("expected zero leaks: %v", err)
	}
	s := h.Summary()
	if s.Leaks != 0 {
		t.Fatalf("leaks = %d, want 0", s.Leaks)
	}
	line := s.Line()
	if !strings.HasPrefix(line, summaryPrefix+" ") {
		t.Fatalf("summary line missing prefix: %q", line)
	}
	for _, want := range []string{"tier=test", "leaks=0", "roots_created=1", "roots_cleaned=1", "duration_ms="} {
		if !strings.Contains(line, want) {
			t.Fatalf("summary line %q missing %q", line, want)
		}
	}
}

// A leaked (uncleaned) root must be detected and force-cleaned by Finalize.
func TestFinalizeDetectsLeakedRoot(t *testing.T) {
	h := newTestHarness(t)
	root, err := h.acquireRoot()
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	h.Finalize()
	if err := h.AssertNoLeaks(); err == nil {
		t.Fatalf("expected leak detection")
	}
	if _, err := os.Stat(root.Path); !os.IsNotExist(err) {
		t.Fatalf("leaked root should be force-cleaned: %v", err)
	}
	if h.Summary().Leaks != 1 {
		t.Fatalf("leaks = %d, want 1", h.Summary().Leaks)
	}
}

func TestResolveParentPrefersEnvWhenValid(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	lookup := func(k string) (string, bool) {
		if k == parentEnvVar {
			return dir, true
		}
		return "", false
	}
	got, err := resolveParent(lookup)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != dir {
		t.Fatalf("resolveParent = %q, want %q", got, dir)
	}

	// Invalid (non-existent) env value falls back to os.TempDir.
	fallbackLookup := func(k string) (string, bool) {
		if k == parentEnvVar {
			return filepath.Join(dir, "does-not-exist"), true
		}
		return "", false
	}
	fb, err := resolveParent(fallbackLookup)
	if err != nil {
		t.Fatalf("resolve fallback: %v", err)
	}
	wantFallback, _ := filepath.EvalSymlinks(os.TempDir())
	if fb != wantFallback {
		t.Fatalf("fallback = %q, want %q", fb, wantFallback)
	}
}

func TestParseRunIDRoundTrip(t *testing.T) {
	id, err := newRunID()
	if err != nil {
		t.Fatalf("newRunID: %v", err)
	}
	parsed, ok := parseRunID(id)
	if !ok {
		t.Fatalf("parseRunID(%q) failed", id)
	}
	if parsed.pid != os.Getpid() {
		t.Fatalf("pid = %d, want %d", parsed.pid, os.Getpid())
	}
	if parsed.rand == "" {
		t.Fatalf("empty random component in %q", id)
	}
}

// --- helpers ---

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
}

func writeRawMarker(t *testing.T, root string, m marker) {
	t.Helper()
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal marker: %v", err)
	}
	if err := os.WriteFile(markerPath(root), data, 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
}

func writeStaleMarker(t *testing.T, root, runID string, pid int, parent string) {
	t.Helper()
	writeRawMarker(t, root, marker{
		SchemaVersion: markerSchemaVersion,
		RunID:         runID,
		PID:           pid,
		CreatedAt:     time.Now().UTC().Format(time.RFC3339Nano),
		Parent:        parent,
	})
}

// newAliveRunID builds a run id for the current pid and boot so runAlive reports
// it as alive.
func newAliveRunID() string {
	return newRunIDForBoot(bootID())
}

func newRunIDForBoot(boot string) string {
	return itoa(os.Getpid()) + "-" + boot + "-deadbeef"
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
