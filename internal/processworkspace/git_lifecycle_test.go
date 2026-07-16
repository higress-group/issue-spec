package processworkspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestManagerPreparesParallelWritableAndDetachedSnapshot(t *testing.T) {
	repo, base := newGitRepository(t)
	manager := openTestManager(t, repo)
	first := testLease("ws-a", "PROCESS-001", ModeWritable, "process-a", base, []string{"internal/a/**"})
	second := testLease("ws-b", "PROCESS-002", ModeWritable, "process-b", base, []string{"internal/b/**"})

	a, err := manager.Prepare(context.Background(), PrepareRequest{Lease: first})
	if err != nil {
		t.Fatal(err)
	}
	b, err := manager.Prepare(context.Background(), PrepareRequest{Lease: second})
	if err != nil {
		t.Fatal(err)
	}
	if !a.Registered || !b.Registered || a.Lease.WorktreePath == b.Lease.WorktreePath || a.Branch == b.Branch {
		t.Fatalf("parallel leases are not isolated: a=%+v b=%+v", a, b)
	}
	if a.Branch != "refs/heads/process-a" || b.Branch != "refs/heads/process-b" {
		t.Fatalf("branches are not full refs: %q %q", a.Branch, b.Branch)
	}

	snapshot := testLease("ws-review", "PROCESS-003", ModeSnapshot, "", base, nil)
	review, err := manager.Prepare(context.Background(), PrepareRequest{Lease: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	if !review.Registered || review.Branch != "" || review.Head != base {
		t.Fatalf("snapshot is not detached at exact SHA: %+v", review)
	}
	if got := gitOutput(t, repo, "rev-parse", "HEAD"); got != base {
		t.Fatalf("integration HEAD changed: %s", got)
	}
}

func TestReconcileRecoversReservationAndPostAddCrashWindows(t *testing.T) {
	repo, base := newGitRepository(t)
	manager := openTestManager(t, repo)
	for index, afterAdd := range []bool{false, true} {
		id := "ws-crash-" + string(rune('a'+index))
		lease := testLease(id, "PROCESS-00"+string(rune('4'+index)), ModeWritable, "crash-"+id, base, []string{"internal/" + id + "/**"})
		lease.IntegrationRoot = manager.IntegrationRoot
		if _, err := manager.Store.Create(context.Background(), lease); err != nil {
			t.Fatal(err)
		}
		path, err := manager.workspacePath(id)
		if err != nil {
			t.Fatal(err)
		}
		if afterAdd {
			if err := manager.addWorktree(context.Background(), lease, path); err != nil {
				t.Fatal(err)
			}
		}
		inspection, err := manager.Reconcile(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if inspection.Lease.Portable.State != StatePrepared || !inspection.Registered || inspection.Head != base {
			t.Fatalf("crash window not recovered: %+v", inspection)
		}
	}
}

func TestCleanupFailsClosedForDirtyOrMismatchedAndPreservesUserWorktree(t *testing.T) {
	repo, base := newGitRepository(t)
	manager := openTestManager(t, repo)
	userPath := filepath.Join(t.TempDir(), "user-worktree")
	runGit(t, repo, "worktree", "add", "-b", "user-owned", "--", userPath, base)

	lease := testLease("ws-clean", "PROCESS-006", ModeWritable, "managed-clean", base, []string{"internal/**"})
	prepared, err := manager.Prepare(context.Background(), PrepareRequest{Lease: lease})
	if err != nil {
		t.Fatal(err)
	}
	dirtyFile := filepath.Join(prepared.Lease.WorktreePath, "dirty.txt")
	if err := os.WriteFile(dirtyFile, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Cleanup(context.Background(), lease.Portable.WorkspaceID, lease.Owner.Token); !errors.Is(err, ErrWorkspaceDirty) {
		t.Fatalf("dirty cleanup err=%v", err)
	}
	if _, err := os.Stat(dirtyFile); err != nil {
		t.Fatalf("dirty file was touched: %v", err)
	}
	if err := os.Remove(dirtyFile); err != nil {
		t.Fatal(err)
	}
	cleaned, err := manager.Cleanup(context.Background(), lease.Portable.WorkspaceID, lease.Owner.Token)
	if err != nil {
		t.Fatal(err)
	}
	if cleaned.Lease.Portable.State != StateCleaned {
		t.Fatalf("cleanup state=%s", cleaned.Lease.Portable.State)
	}
	if markers := gitOutput(t, repo, "for-each-ref", "--format=%(refname)", "refs/issue-spec/process-workspaces/ws-clean/"); markers != "" {
		t.Fatalf("cleanup left ownership marker %q", markers)
	}
	if got := gitOutput(t, repo, "rev-parse", "refs/heads/managed-clean"); got != base {
		t.Fatalf("cleanup modified or deleted process branch: %s", got)
	}
	if _, err := os.Stat(userPath); err != nil {
		t.Fatalf("user worktree was removed: %v", err)
	}
	if _, err := manager.Cleanup(context.Background(), lease.Portable.WorkspaceID, lease.Owner.Token); err != nil {
		t.Fatalf("idempotent cleanup failed: %v", err)
	}

	mismatch := testLease("ws-mismatch", "PROCESS-007", ModeWritable, "managed-mismatch", base, []string{"internal/**"})
	prepared, err = manager.Prepare(context.Background(), PrepareRequest{Lease: mismatch})
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, prepared.Lease.WorktreePath, "checkout", "-b", "unexpected")
	inspection, err := manager.Cleanup(context.Background(), mismatch.Portable.WorkspaceID, mismatch.Owner.Token)
	if !errors.Is(err, ErrWorkspaceConflict) || !inspection.Present {
		t.Fatalf("branch mismatch cleanup=%+v err=%v", inspection, err)
	}
	if _, err := os.Stat(prepared.Lease.WorktreePath); err != nil {
		t.Fatalf("mismatched worktree was removed: %v", err)
	}
}

func TestCleanupRecoversPostAddPreAssignmentCrash(t *testing.T) {
	repo, base := newGitRepository(t)
	manager := openTestManager(t, repo)
	lease := testLease("ws-cleanup-crash", "PROCESS-010", ModeWritable, "cleanup-crash", base, []string{"internal/cleanup/**"})
	lease.IntegrationRoot = manager.IntegrationRoot
	if _, err := manager.Store.Create(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	path, err := manager.workspacePath(lease.Portable.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.addWorktree(context.Background(), lease, path); err != nil {
		t.Fatal(err)
	}
	cleaned, err := manager.Cleanup(context.Background(), lease.Portable.WorkspaceID, lease.Owner.Token)
	if err != nil {
		t.Fatal(err)
	}
	if cleaned.Lease.Portable.State != StateCleaned {
		t.Fatalf("cleanup state=%s", cleaned.Lease.Portable.State)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphaned worktree still exists: %v", err)
	}
	for _, worktree := range gitOutputLines(t, repo, "worktree", "list", "--porcelain") {
		if strings.TrimPrefix(worktree, "worktree ") == path {
			t.Fatalf("orphaned worktree is still registered: %s", path)
		}
	}
}

func TestReconcileFailsClosedWhenActiveWorktreeDisappears(t *testing.T) {
	repo, base := newGitRepository(t)
	manager := openTestManager(t, repo)
	lease := testLease("ws-missing", "PROCESS-011", ModeWritable, "missing-worktree", base, []string{"internal/missing/**"})
	prepared, err := manager.Prepare(context.Background(), PrepareRequest{Lease: lease})
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "worktree", "remove", "--", prepared.Lease.WorktreePath)
	inspection, err := manager.Reconcile(context.Background(), lease.Portable.WorkspaceID)
	if !errors.Is(err, ErrWorkspaceConflict) || inspection.Present || inspection.Registered {
		t.Fatalf("missing active worktree reconcile=%+v err=%v", inspection, err)
	}
}

func TestPrepareRejectsDirtyIntegrationAndSymlinkCollision(t *testing.T) {
	repo, base := newGitRepository(t)
	manager := openTestManager(t, repo)
	if err := os.WriteFile(filepath.Join(repo, "untracked"), []byte("dirty"), 0o600); err != nil {
		t.Fatal(err)
	}
	lease := testLease("ws-dirty-root", "PROCESS-008", ModeWritable, "dirty-root", base, []string{"internal/**"})
	if _, err := manager.Prepare(context.Background(), PrepareRequest{Lease: lease}); !errors.Is(err, ErrWorkspaceDirty) {
		t.Fatalf("dirty integration accepted: %v", err)
	}
	if err := os.Remove(filepath.Join(repo, "untracked")); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	target, err := manager.workspacePath("ws-symlink")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, target); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	symlinkLease := testLease("ws-symlink", "PROCESS-009", ModeWritable, "symlink", base, []string{"internal/**"})
	if _, err := manager.Prepare(context.Background(), PrepareRequest{Lease: symlinkLease}); !errors.Is(err, ErrWorkspaceConflict) {
		t.Fatalf("symlink collision accepted: %v", err)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("outside symlink target was modified: %v", entries)
	}
}

func TestPrepareDoesNotClaimPreExistingUserBranchAtReservedBase(t *testing.T) {
	repo, base := newGitRepository(t)
	manager := openTestManager(t, repo)
	runGit(t, repo, "branch", "user-reserved-name", base)
	lease := testLease("ws-user-branch", "PROCESS-012", ModeWritable, "user-reserved-name", base, []string{"internal/user/**"})

	_, err := manager.Prepare(context.Background(), PrepareRequest{Lease: lease})
	if !errors.Is(err, ErrWorkspaceConflict) {
		t.Fatalf("pre-existing user branch was accepted: %v", err)
	}
	if got := gitOutput(t, repo, "rev-parse", "refs/heads/user-reserved-name"); got != base {
		t.Fatalf("user branch changed: got %s want %s", got, base)
	}
	if markers := gitOutput(t, repo, "for-each-ref", "--format=%(refname)", "refs/issue-spec/process-workspaces/ws-user-branch/"); markers != "" {
		t.Fatalf("failed reservation left ownership marker %q", markers)
	}
	path, pathErr := manager.workspacePath(lease.Portable.WorkspaceID)
	if pathErr != nil {
		t.Fatal(pathErr)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed reservation modified workspace path: %v", statErr)
	}
}

func TestPrepareRevalidatesIntegrationHeadAfterWorktreeAdd(t *testing.T) {
	repo, base := newGitRepository(t)
	runner := &afterWorktreeAddRunner{GitRunner: ExecGitRunner{}, after: func() error {
		command := exec.Command("git", "commit", "--allow-empty", "-m", "advance integration during prepare")
		command.Dir = repo
		output, err := command.CombinedOutput()
		if err != nil {
			return fmt.Errorf("advance integration HEAD: %w: %s", err, output)
		}
		return nil
	}}
	manager, err := OpenManager(context.Background(), repo, filepath.Join(t.TempDir(), "managed"), ManagerOptions{Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	lease := testLease("ws-head-race", "PROCESS-013", ModeWritable, "head-race", base, []string{"internal/race/**"})

	_, err = manager.Prepare(context.Background(), PrepareRequest{Lease: lease})
	if !errors.Is(err, ErrWorkspaceConflict) {
		t.Fatalf("stale integration HEAD was accepted: %v", err)
	}
	stored, found, getErr := manager.Store.Get(context.Background(), lease.Portable.WorkspaceID)
	if getErr != nil || !found {
		t.Fatalf("reserved lease missing: found=%v err=%v", found, getErr)
	}
	if stored.Portable.State != StatePreparing || stored.WorktreePath != "" {
		t.Fatalf("stale prepare became active: %+v", stored)
	}
	if got := gitOutput(t, repo, "rev-parse", "HEAD"); got == base {
		t.Fatal("race runner did not advance integration HEAD")
	}
}

func TestPrepareConflictsWhenIntegrationAdvancesAfterFinalHeadRead(t *testing.T) {
	repo, base := newGitRepository(t)
	runner := &afterFinalHeadReadRunner{GitRunner: ExecGitRunner{}, after: func() error {
		command := exec.Command("git", "commit", "--allow-empty", "-m", "advance after final validation read")
		command.Dir = repo
		output, err := command.CombinedOutput()
		if err != nil {
			return fmt.Errorf("advance integration HEAD after read: %w: %s", err, output)
		}
		return nil
	}}
	manager, err := OpenManager(context.Background(), repo, filepath.Join(t.TempDir(), "managed"), ManagerOptions{Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	lease := testLease("ws-post-read-race", "PROCESS-015", ModeWritable, "post-read-race", base, []string{"internal/post-read/**"})

	inspection, err := manager.Prepare(context.Background(), PrepareRequest{Lease: lease})
	if !errors.Is(err, ErrWorkspaceConflict) {
		t.Fatalf("post-read integration drift was published: inspection=%+v err=%v", inspection, err)
	}
	stored, found, getErr := manager.Store.Get(context.Background(), lease.Portable.WorkspaceID)
	if getErr != nil || !found {
		t.Fatalf("reserved lease missing: found=%v err=%v", found, getErr)
	}
	if stored.Portable.State != StateConflicted || inspection.Lease.Portable.State != StateConflicted {
		t.Fatalf("post-read drift did not atomically persist conflicted: stored=%+v inspection=%+v", stored, inspection)
	}
}

func TestManagersSerializeOnStableIntegrationLock(t *testing.T) {
	repo, _ := newGitRepository(t)
	first := openTestManager(t, repo)
	second := openTestManager(t, repo)
	enteredFirst := make(chan struct{})
	releaseFirst := make(chan struct{})
	enteredSecond := make(chan struct{})
	errorsOut := make(chan error, 2)
	go func() {
		_, err := first.withIntegrationLock(context.Background(), func() (Inspection, error) {
			close(enteredFirst)
			<-releaseFirst
			return Inspection{}, nil
		})
		errorsOut <- err
	}()
	<-enteredFirst
	go func() {
		_, err := second.withIntegrationLock(context.Background(), func() (Inspection, error) {
			close(enteredSecond)
			return Inspection{}, nil
		})
		errorsOut <- err
	}()
	select {
	case <-enteredSecond:
		t.Fatal("second manager entered the integration boundary concurrently")
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseFirst)
	select {
	case <-enteredSecond:
	case <-time.After(2 * time.Second):
		t.Fatal("second manager did not acquire the released integration lock")
	}
	for range 2 {
		if err := <-errorsOut; err != nil {
			t.Fatal(err)
		}
	}
}

func TestIntegrationLockIsStableAndReleasedAfterActionError(t *testing.T) {
	repo, _ := newGitRepository(t)
	first := openTestManager(t, repo)
	second := openTestManager(t, repo)
	before, err := os.Stat(first.IntegrationLockPath)
	if err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("integration action failed")
	if _, err := first.withIntegrationLock(context.Background(), func() (Inspection, error) {
		return Inspection{}, sentinel
	}); !errors.Is(err, sentinel) {
		t.Fatalf("action error was not preserved: %v", err)
	}
	if _, err := second.withIntegrationLock(context.Background(), func() (Inspection, error) {
		return Inspection{}, nil
	}); err != nil {
		t.Fatalf("second manager could not acquire lock after error: %v", err)
	}
	after, err := os.Stat(first.IntegrationLockPath)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("integration coordination lock file was replaced or unlinked")
	}
}

func TestConcurrentPrepareOfSameReservationConverges(t *testing.T) {
	repo, base := newGitRepository(t)
	manager := openTestManager(t, repo)
	lease := testLease("ws-concurrent", "PROCESS-014", ModeWritable, "concurrent-reservation", base, []string{"internal/concurrent/**"})
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			inspection, err := manager.Prepare(context.Background(), PrepareRequest{Lease: lease})
			if err == nil && (!inspection.Registered || inspection.Lease.Portable.State != StatePrepared) {
				err = fmt.Errorf("unexpected converged inspection: %+v", inspection)
			}
			results <- err
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	if markers := gitOutputLines(t, repo, "for-each-ref", "--format=%(refname)", "refs/issue-spec/process-workspaces/ws-concurrent/"); len(markers) != 1 || markers[0] == "" {
		t.Fatalf("concurrent reservation markers = %v", markers)
	}
}

func TestReconcileRejectsWritableHeadDriftByLifecycleState(t *testing.T) {
	states := []LifecycleState{StatePreparing, StatePrepared, StateWorkerComplete, StateIntegrating, StateIntegrated}
	for index, state := range states {
		t.Run(string(state), func(t *testing.T) {
			repo, base := newGitRepository(t)
			manager := openTestManager(t, repo)
			id := fmt.Sprintf("ws-state-%d", index)
			lease := testLease(id, fmt.Sprintf("PROCESS-%03d", 20+index), ModeWritable, "state-"+string(state), base, []string{"internal/state/**"})
			var worktreePath string
			if state == StatePreparing {
				lease.IntegrationRoot = manager.IntegrationRoot
				if _, err := manager.Store.Create(context.Background(), lease); err != nil {
					t.Fatal(err)
				}
				worktreePath, _ = manager.workspacePath(id)
				if err := manager.addWorktree(context.Background(), lease, worktreePath); err != nil {
					t.Fatal(err)
				}
			} else {
				prepared, err := manager.Prepare(context.Background(), PrepareRequest{Lease: lease})
				if err != nil {
					t.Fatal(err)
				}
				worktreePath = prepared.Lease.WorktreePath
			}

			if state == StateWorkerComplete || state == StateIntegrating || state == StateIntegrated {
				runGit(t, worktreePath, "commit", "--allow-empty", "-m", "worker result")
				resultCommit := gitOutput(t, worktreePath, "rev-parse", "HEAD")
				if _, err := manager.Store.Update(context.Background(), id, func(current *LocalLease) error {
					current.Portable.State = StateWorkerComplete
					current.Portable.ResultCommit = resultCommit
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				if state == StateIntegrating || state == StateIntegrated {
					if _, err := manager.Store.Update(context.Background(), id, func(current *LocalLease) error {
						current.Portable.State = StateIntegrating
						return nil
					}); err != nil {
						t.Fatal(err)
					}
				}
				if state == StateIntegrated {
					if _, err := manager.Store.Update(context.Background(), id, func(current *LocalLease) error {
						current.Portable.State = StateIntegrated
						current.Portable.IntegrationSHA = base
						return nil
					}); err != nil {
						t.Fatal(err)
					}
				}
			}
			runGit(t, worktreePath, "commit", "--allow-empty", "-m", "unexpected clean commit")

			inspection, err := manager.Reconcile(context.Background(), id)
			if !errors.Is(err, ErrWorkspaceConflict) {
				t.Fatalf("HEAD drift in state %s was accepted: inspection=%+v err=%v", state, inspection, err)
			}
			if state == StatePreparing {
				if inspection.Lease.Portable.State != StatePreparing {
					t.Fatalf("preparing crash recovery changed state to %s", inspection.Lease.Portable.State)
				}
			} else if inspection.Lease.Portable.State != StateConflicted {
				t.Fatalf("HEAD drift was not persisted as conflicted: %+v", inspection.Lease)
			}
		})
	}
}

type afterWorktreeAddRunner struct {
	GitRunner
	once  sync.Once
	after func() error
}

type afterFinalHeadReadRunner struct {
	GitRunner
	mu        sync.Mutex
	headReads int
	after     func() error
}

func (r *afterFinalHeadReadRunner) Run(ctx context.Context, command GitCommand) (GitResult, error) {
	result, err := r.GitRunner.Run(ctx, command)
	if err != nil || len(command.Args) != 2 || command.Args[0] != "rev-parse" || command.Args[1] != "HEAD" {
		return result, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.headReads++
	if r.headReads == 2 && r.after != nil {
		if afterErr := r.after(); afterErr != nil {
			return result, afterErr
		}
	}
	return result, nil
}

func (r *afterWorktreeAddRunner) Run(ctx context.Context, command GitCommand) (GitResult, error) {
	result, err := r.GitRunner.Run(ctx, command)
	if err != nil || len(command.Args) < 2 || command.Args[0] != "worktree" || command.Args[1] != "add" {
		return result, err
	}
	var afterErr error
	r.once.Do(func() {
		if r.after != nil {
			afterErr = r.after()
		}
	})
	if afterErr != nil {
		return result, afterErr
	}
	return result, nil
}

func openTestManager(t *testing.T, repo string) *Manager {
	t.Helper()
	manager, err := OpenManager(context.Background(), repo, filepath.Join(t.TempDir(), "managed"), ManagerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func testLease(id, processID string, mode WorkspaceMode, branch, base string, ownership []string) LocalLease {
	now := time.Now().UTC()
	class := ExecutionChangeBearing
	detached := ""
	if mode == ModeSnapshot {
		class = ExecutionReview
		detached = base
	}
	return LocalLease{Portable: PortableLease{SchemaVersion: LeaseSchemaVersion, WorkspaceID: id, Repository: "o/r", ProcessID: processID,
		ExecutionClass: class, Mode: mode, BaseSHA: base, Branch: branch, DetachedRevision: detached, WriteOwnership: ownership,
		RuntimeNamespace: id, State: StatePreparing, CreatedAt: now, UpdatedAt: now},
		Owner: LeaseOwner{CoordinatorID: "coordinator", Token: "token-" + id, AcquiredAt: now}, LocalRevision: 1}
}

func newGitRepository(t *testing.T) (string, string) {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "integration")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-b", "main")
	runGit(t, repo, "config", "user.name", "Test User")
	runGit(t, repo, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "base")
	return repo, gitOutput(t, repo, "rev-parse", "HEAD")
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(output))
}

func gitOutputLines(t *testing.T, dir string, args ...string) []string {
	t.Helper()
	return strings.Split(gitOutput(t, dir, args...), "\n")
}
