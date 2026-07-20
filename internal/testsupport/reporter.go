package testsupport

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"
)

// summaryPrefix is the machine-parseable marker consumers grep for.
const summaryPrefix = "TEST-TIER-SUMMARY"

// Summary is a snapshot of a run's resource accounting.
type Summary struct {
	Tier            string
	Duration        time.Duration
	RootsCreated    int
	RootsCleaned    int
	RootsRecovered  int
	ChildrenSpawned int
	ChildrenReaped  int
	// Leaks is the number of current-run children and roots that remained live
	// before forced finalization (zero for a clean run).
	Leaks int
}

// Summary returns the current accounting snapshot.
func (h *Harness) Summary() Summary {
	h.mu.Lock()
	defer h.mu.Unlock()
	return Summary{
		Tier:            h.tier,
		Duration:        time.Since(h.startedAt),
		RootsCreated:    h.rootsCreated,
		RootsCleaned:    h.rootsCleaned,
		RootsRecovered:  h.rootsRecovered,
		ChildrenSpawned: h.childrenSpawned,
		ChildrenReaped:  h.childrenReaped,
		Leaks:           h.leaks,
	}
}

// Line renders the summary as a single machine-parseable key=value line.
func (s Summary) Line() string {
	return fmt.Sprintf(
		"%s tier=%s duration_ms=%d roots_created=%d roots_cleaned=%d roots_recovered=%d children_spawned=%d children_reaped=%d leaks=%d",
		summaryPrefix,
		s.Tier,
		s.Duration.Milliseconds(),
		s.RootsCreated,
		s.RootsCleaned,
		s.RootsRecovered,
		s.ChildrenSpawned,
		s.ChildrenReaped,
		s.Leaks,
	)
}

// PrintSummary writes the summary line to w followed by a newline.
func (h *Harness) PrintSummary(w io.Writer) {
	fmt.Fprintln(w, h.Summary().Line())
}

// Finalize forcibly terminates and reaps any current-run children that outlived
// their tests and cleans up any remaining current-run roots. It records the
// number of leftover resources as the run's leak count before cleaning them.
func (h *Harness) Finalize() {
	h.mu.Lock()
	children := make([]*Child, 0, len(h.liveChildren))
	for c := range h.liveChildren {
		children = append(children, c)
	}
	roots := make([]string, 0, len(h.liveRoots))
	for r := range h.liveRoots {
		roots = append(roots, r)
	}
	h.leaks = len(children) + len(roots)
	h.mu.Unlock()

	for _, c := range children {
		_ = terminateProcess(c.cmd)
		_ = c.cmd.Wait()
		h.reap(c)
	}
	for _, r := range roots {
		_ = h.CleanupRoot(r)
	}
}

// AssertNoLeaks returns an error when the run leaked current-run children or
// roots (i.e. Finalize had to force-clean anything).
func (h *Harness) AssertNoLeaks() error {
	h.mu.Lock()
	leaks := h.leaks
	h.mu.Unlock()
	if leaks != 0 {
		return fmt.Errorf("testsupport: %d current-run resource(s) leaked", leaks)
	}
	return nil
}

// RunMain is the TestMain helper. It recovers stale roots, runs the test suite,
// finalizes the run (terminating and reaping children and cleaning roots),
// asserts zero current-run leaks, and prints the machine-parseable summary line.
// It returns the process exit code the caller should pass to os.Exit.
//
// Typical usage:
//
//	func TestMain(m *testing.M) {
//	    os.Exit(testsupport.RunMain(m, "full"))
//	}
func RunMain(m *testing.M, tier string) int {
	h, err := New(Options{Tier: tier})
	if err != nil {
		fmt.Fprintln(os.Stderr, "testsupport: init:", err)
		return 1
	}
	if report, err := h.RecoverStale(); err != nil {
		fmt.Fprintln(os.Stderr, "testsupport: stale recovery:", err)
	} else {
		for _, retained := range report.Retained {
			fmt.Fprintf(os.Stderr, "testsupport: retained stale candidate %s: %s\n", retained.Path, retained.Reason)
		}
	}

	stop := h.installSignalHandler()
	defer stop()

	code := m.Run()

	h.Finalize()
	if err := h.AssertNoLeaks(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if code == 0 {
			code = 1
		}
	}
	h.PrintSummary(os.Stdout)
	return code
}

// installSignalHandler converts catchable SIGINT/SIGTERM into the same
// terminate-reap-clean path used on normal exit, then exits non-zero. The
// returned function removes the handler.
func (h *Harness) installSignalHandler() func() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		select {
		case <-ch:
			h.Finalize()
			h.PrintSummary(os.Stdout)
			os.Exit(1)
		case <-done:
		}
	}()
	return func() {
		signal.Stop(ch)
		close(done)
	}
}
