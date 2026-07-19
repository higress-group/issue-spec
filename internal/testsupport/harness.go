package testsupport

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"
)

// Options configures a Harness. All fields are optional; zero values select the
// production-like defaults (env-resolved parent, generated run id, "default"
// tier label).
type Options struct {
	// Parent overrides the configured parent directory. When empty it is
	// resolved from ISSUE_SPEC_TEST_TMP or the process temp directory.
	Parent string
	// Tier labels the run in the emitted TEST-TIER-SUMMARY line.
	Tier string
	// RunID overrides the generated run identity. Intended for tests.
	RunID string
	// Lookup overrides environment resolution. Intended for tests.
	Lookup func(string) (string, bool)
}

// Harness owns a single integration run's temporary roots and child processes.
// It is safe for concurrent use.
type Harness struct {
	parent    string
	runID     string
	tier      string
	startedAt time.Time

	mu              sync.Mutex
	liveRoots       map[string]struct{}
	liveChildren    map[*Child]struct{}
	rootsCreated    int
	rootsCleaned    int
	rootsRecovered  int
	childrenSpawned int
	childrenReaped  int
	leaks           int
}

// New builds a Harness with the given options.
func New(opts Options) (*Harness, error) {
	parent := opts.Parent
	if parent == "" {
		resolved, err := resolveParent(opts.Lookup)
		if err != nil {
			return nil, fmt.Errorf("resolve configured parent: %w", err)
		}
		parent = resolved
	}
	runID := opts.RunID
	if runID == "" {
		generated, err := newRunID()
		if err != nil {
			return nil, err
		}
		runID = generated
	}
	tier := opts.Tier
	if tier == "" {
		tier = "default"
	}
	return &Harness{
		parent:       parent,
		runID:        runID,
		tier:         tier,
		startedAt:    time.Now(),
		liveRoots:    make(map[string]struct{}),
		liveChildren: make(map[*Child]struct{}),
	}, nil
}

// Parent returns the canonical configured parent directory.
func (h *Harness) Parent() string { return h.parent }

// RunID returns this run's identity token.
func (h *Harness) RunID() string { return h.runID }

// Root is a validated, owned run-scoped directory.
type Root struct {
	// Path is the canonical (EvalSymlinks-resolved) path of the root.
	Path string
}

// AcquireRoot creates one unique run-scoped root under the configured parent,
// writes the ownership marker, and registers per-test cleanup. It fails the test
// on any error.
func (h *Harness) AcquireRoot(t testing.TB) *Root {
	t.Helper()
	root, err := h.acquireRoot()
	if err != nil {
		t.Fatalf("testsupport: acquire root: %v", err)
	}
	t.Cleanup(func() {
		if !h.rootTracked(root.Path) {
			// Already cleaned explicitly by the test; nothing to do.
			return
		}
		if err := h.CleanupRoot(root.Path); err != nil {
			t.Errorf("testsupport: cleanup root %s: %v", root.Path, err)
		}
	})
	return root
}

// acquireRoot performs the root creation without a testing.TB, for reuse and
// direct testing.
func (h *Harness) acquireRoot() (*Root, error) {
	dir, err := os.MkdirTemp(h.parent, "issue-spec-test-*")
	if err != nil {
		return nil, fmt.Errorf("create run root: %w", err)
	}
	real, err := containedRealPath(h.parent, dir)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("validate run root: %w", err)
	}
	m := marker{
		SchemaVersion: markerSchemaVersion,
		RunID:         h.runID,
		PID:           os.Getpid(),
		CreatedAt:     time.Now().UTC().Format(time.RFC3339Nano),
		Parent:        h.parent,
	}
	if err := writeMarker(real, m); err != nil {
		_ = os.RemoveAll(real)
		return nil, err
	}
	h.mu.Lock()
	h.liveRoots[real] = struct{}{}
	h.rootsCreated++
	h.mu.Unlock()
	return &Root{Path: real}, nil
}

// CleanupRoot validates and removes a current-run owned root. It fails closed:
// the directory is only removed when its marker is present, canonical, and the
// path is contained within the configured parent with no symlink escape.
func (h *Harness) CleanupRoot(path string) error {
	if err := h.removeValidated(path, false); err != nil {
		return err
	}
	h.mu.Lock()
	if _, ok := h.liveRoots[path]; ok {
		delete(h.liveRoots, path)
		h.rootsCleaned++
	}
	h.mu.Unlock()
	return nil
}

// rootTracked reports whether path is still a live current-run root.
func (h *Harness) rootTracked(path string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.liveRoots[path]
	return ok
}

// removeValidated is the shared fail-closed removal path. When requireDead is
// true the owning run must be provably not alive (used by stale recovery); for a
// current-run cleanup the caller passes false.
func (h *Harness) removeValidated(path string, requireDead bool) error {
	m, err := readMarker(path)
	if err != nil {
		return err
	}
	real, err := containedRealPath(h.parent, path)
	if err != nil {
		return err
	}
	if requireDead && runAlive(m) {
		return fmt.Errorf("%w: run %s pid %d", errRunActive, m.RunID, m.PID)
	}
	if err := os.RemoveAll(real); err != nil {
		return fmt.Errorf("remove validated root %s: %w", real, err)
	}
	return nil
}

// Command describes a child process to spawn under the run's process group.
type Command struct {
	Path string
	Args []string
	Dir  string
	Env  []string
}

// Child is a spawned process tracked by the harness.
type Child struct {
	cmd    *exec.Cmd
	h      *Harness
	reaped bool
}

// Spawn starts a child process attached to the run's process-lifecycle boundary.
// The provided context terminates the whole process group on cancellation or
// timeout.
func (h *Harness) Spawn(ctx context.Context, c Command) (*Child, error) {
	cmd := exec.CommandContext(ctx, c.Path, c.Args...)
	cmd.Dir = c.Dir
	if c.Env != nil {
		cmd.Env = c.Env
	}
	configureProcAttr(cmd)
	cmd.Cancel = func() error { return terminateProcess(cmd) }
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("spawn %s: %w", c.Path, err)
	}
	child := &Child{cmd: cmd, h: h}
	h.mu.Lock()
	h.liveChildren[child] = struct{}{}
	h.childrenSpawned++
	h.mu.Unlock()
	return child, nil
}

// PID returns the child's process id, or 0 if it has not started.
func (c *Child) PID() int {
	if c.cmd.Process != nil {
		return c.cmd.Process.Pid
	}
	return 0
}

// Wait waits for the child to exit, reaping it and updating harness accounting.
func (c *Child) Wait() error {
	err := c.cmd.Wait()
	c.h.reap(c)
	return err
}

// Terminate signals the child's process group for shutdown.
func (c *Child) Terminate() error {
	return terminateProcess(c.cmd)
}

// reap records that a child has been fully reaped exactly once.
func (h *Harness) reap(c *Child) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c.reaped {
		return
	}
	if _, ok := h.liveChildren[c]; ok {
		delete(h.liveChildren, c)
		h.childrenReaped++
		c.reaped = true
	}
}
