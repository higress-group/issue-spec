package jobs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/acpx"
	"github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/commentrunner/storage"
	"github.com/higress-group/issue-spec/internal/workspace"
)

// sharedLayoutIdentity is the dispatcher runtime identity used by the shared
// layout tests: builtin GitHub profile shape (empty realm).
func sharedLayoutIdentity() RuntimeIdentity {
	return RuntimeIdentity{Hostname: "github.com", Runner: "runner-shared"}
}

// sharedLayoutBinding builds a session-clone binding rooted below root so the
// runner workspace root (the parent of the clone) is root itself.
func sharedLayoutBinding(t *testing.T, root, id, repo string) workspace.Binding {
	t.Helper()
	path := filepath.Join(root, id)
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return workspace.Binding{
		Workspace:            state.WorkspaceMetadata{ID: id, Path: path, Repo: repo, CloneURL: "https://github.com/" + repo + ".git", Branch: "issue-spec-" + id, Ref: "main", RepositoryBinding: sharedLayoutRepoBinding(repo)},
		AcpxWorkingDirectory: path,
		SandboxWorkspacePath: path,
	}
}

func sharedLayoutRepoBinding(repo string) state.RepositoryBindingSnapshot {
	binding := testRepositoryBinding()
	binding.IssueRepositoryKey = repo
	binding.ExternalRepositoryID = repo
	binding.CloneURL = "https://github.com/" + repo + ".git"
	binding.WebURL = "https://github.com/" + repo
	return binding
}

// perRepoResolver resolves the requested repo verbatim so one dispatcher can
// dispatch jobs of different repos into different runtime scopes.
type perRepoResolver struct{}

func (perRepoResolver) ResolveRepository(_ context.Context, repo string) (RepositoryInfo, error) {
	binding := sharedLayoutRepoBinding(repo)
	return RepositoryInfo{Repo: repo, CloneURL: binding.CloneURL, DefaultBranch: "main", Ref: "main", Binding: binding}, nil
}

func seedSharedLayoutJob(t *testing.T, store *memoryStore, id, repo string, now time.Time) {
	t.Helper()
	seedQueuedJob(t, store, state.Job{
		ID:                    id,
		Repo:                  repo,
		IssueNumber:           30,
		SessionCreatorLogin:   "alice",
		TriggeringUserLogin:   "alice",
		TriggerCommentID:      42,
		CommandID:             "cmd-" + id,
		CommandName:           "new",
		CommandPrompt:         "implement",
		CommandIdempotencyKey: "key-" + id,
		StatusWritebackKey:    "status-" + id,
		CreatedAt:             now,
		FirstObservedComment: state.SeenComment{
			Repo: repo, IssueNumber: 30, CommentID: 42, AuthorLogin: "alice",
			HTMLURL: "https://github.com/" + repo + "/issues/30#issuecomment-42",
		},
	})
}

func newSharedLayoutDispatcher(store *memoryStore, workspaces *fakeWorkspaces, coordinator *fakeCoordinator, fake *fakeStorage, now time.Time) (*Dispatcher, *fakeSandbox) {
	dispatcher := testDispatcher(store, workspaces, coordinator, &fakeWriteback{}, now)
	dispatcher.Repositories = perRepoResolver{}
	dispatcher.Storage = fake
	dispatcher.RuntimeIdentity = sharedLayoutIdentity()
	return dispatcher, dispatcher.Sandbox.(*fakeSandbox)
}

// TestSharedLayoutDispatchSharesRuntimeHomeAcrossSessions is the core shared
// layout contract: two sessions of one repo on one runner identity dispatch
// onto the identical runner-scoped runtime HOME while each job receives its
// own disposable scratch, and no legacy .sessions runtime is created.
func TestSharedLayoutDispatchSharesRuntimeHomeAcrossSessions(t *testing.T) {
	store := newMemoryStore()
	now := time.Date(2026, 7, 6, 9, 0, 0, 0, time.UTC)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspaces := &fakeWorkspaces{bindings: map[string]workspace.Binding{
		"job-aaaaaaaaaaaaaaaa": sharedLayoutBinding(t, root, "ws-shared-a", "o/r"),
		"job-bbbbbbbbbbbbbbbb": sharedLayoutBinding(t, root, "ws-shared-b", "o/r"),
	}}
	coordinator := &fakeCoordinator{newResult: dispatchResult("ps-first", "rec-first", "turn-first", completedSummary())}
	fake := &fakeStorage{}
	dispatcher, sandbox := newSharedLayoutDispatcher(store, workspaces, coordinator, fake, now)

	seedSharedLayoutJob(t, store, "job-aaaaaaaaaaaaaaaa", "o/r", now)
	result, err := dispatcher.RunNext(context.Background())
	if err != nil || !result.Executed || result.Status != state.StatusCompleted {
		t.Fatalf("first dispatch result=%+v err=%v", result, err)
	}

	dispatcher.PublicSessionID = func() (string, error) { return "ps-second", nil }
	coordinator.newResult = dispatchResult("ps-second", "rec-second", "turn-second", completedSummary())
	seedSharedLayoutJob(t, store, "job-bbbbbbbbbbbbbbbb", "o/r", now.Add(time.Minute))
	result, err = dispatcher.RunNext(context.Background())
	if err != nil || !result.Executed || result.Status != state.StatusCompleted {
		t.Fatalf("second dispatch result=%+v err=%v", result, err)
	}

	if len(sandbox.requests) != 2 {
		t.Fatalf("sandbox requests = %d, want 2", len(sandbox.requests))
	}
	first, second := sandbox.requests[0], sandbox.requests[1]

	scope := sharedLayoutIdentity().scope("o/r")
	homeRoot, err := storage.RuntimeHomeRoot(root, scope)
	if err != nil {
		t.Fatal(err)
	}
	wantHome := filepath.Join(homeRoot, "home")
	if first.RuntimeHome != wantHome || second.RuntimeHome != wantHome {
		t.Fatalf("shared runtime home mismatch: first=%q second=%q want=%q", first.RuntimeHome, second.RuntimeHome, wantHome)
	}
	for _, req := range []SandboxRequest{first, second} {
		if req.RuntimeGHConfigDir != filepath.Join(homeRoot, "gh") ||
			req.RuntimeXDGConfigHome != filepath.Join(homeRoot, "xdg") ||
			req.RuntimeCodexHome != filepath.Join(homeRoot, "codex") ||
			req.RuntimeAcpxDir != filepath.Join(homeRoot, "acpx-runtime") {
			t.Fatalf("runtime home subdirs do not anchor at the shared root %q: %+v", homeRoot, req)
		}
	}

	scratchRootA, err := storage.JobScratchRoot(root, "job-aaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	scratchRootB, err := storage.JobScratchRoot(root, "job-bbbbbbbbbbbbbbbb")
	if err != nil {
		t.Fatal(err)
	}
	if first.JobTmpDir != filepath.Join(scratchRootA, "tmp") || second.JobTmpDir != filepath.Join(scratchRootB, "tmp") {
		t.Fatalf("job tmp dirs are not the per-job scratch: first=%q second=%q", first.JobTmpDir, second.JobTmpDir)
	}
	if first.JobTmpDir == second.JobTmpDir || first.JobGoTmpDir == second.JobGoTmpDir ||
		first.JobXDGDataHome == second.JobXDGDataHome || first.JobXDGStateHome == second.JobXDGStateHome {
		t.Fatalf("jobs of one repo must receive distinct scratch: first=%+v second=%+v", first, second)
	}
	for _, dir := range []string{first.JobTmpDir, first.JobGoTmpDir, first.JobXDGDataHome, first.JobXDGStateHome} {
		if info, statErr := os.Stat(dir); statErr != nil || !info.IsDir() {
			t.Fatalf("scratch dir %q not prepared: info=%v err=%v", dir, info, statErr)
		}
	}

	// Recording order and shape: shared home + process pool + job scratch per
	// dispatch; the legacy session runtime record is never used.
	if len(fake.recordCalls) != 0 {
		t.Fatalf("legacy RecordSessionResources must not run on the shared layout: %+v", fake.recordCalls)
	}
	if len(fake.homeCalls) != 2 || len(fake.poolCalls) != 2 || len(fake.scratchCalls) != 2 {
		t.Fatalf("shared layout recording calls: home=%d pool=%d scratch=%d, want 2 each",
			len(fake.homeCalls), len(fake.poolCalls), len(fake.scratchCalls))
	}
	if fake.homeCalls[0].scope != scope || fake.homeCalls[1].scope != scope {
		t.Fatalf("runtime home scopes = %+v, want %+v", fake.homeCalls, scope)
	}
	if fake.scratchCalls[0].jobID != "job-aaaaaaaaaaaaaaaa" || fake.scratchCalls[1].jobID != "job-bbbbbbbbbbbbbbbb" ||
		fake.scratchCalls[0].path != scratchRootA || fake.scratchCalls[1].path != scratchRootB {
		t.Fatalf("scratch recordings = %+v", fake.scratchCalls)
	}

	// Both jobs completed: both scratch trees are completed and removed.
	if len(fake.completeCalls) != 2 {
		t.Fatalf("complete calls = %+v, want 2", fake.completeCalls)
	}

	// The shared layout never materializes the legacy per-session runtime root.
	if _, err := os.Lstat(filepath.Join(root, ".sessions")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy .sessions root must not exist on the shared layout: %v", err)
	}
}

// TestSharedLayoutRuntimeHomeDiffersAcrossRepos proves the runtime HOME is
// scoped per repo: a job of another repo on the same runner identity receives
// a different shared home below the same .runner-home root.
func TestSharedLayoutRuntimeHomeDiffersAcrossRepos(t *testing.T) {
	store := newMemoryStore()
	now := time.Date(2026, 7, 6, 9, 30, 0, 0, time.UTC)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspaces := &fakeWorkspaces{bindings: map[string]workspace.Binding{
		"job-aaaaaaaaaaaaaaaa": sharedLayoutBinding(t, root, "ws-repo-a", "o/r"),
		"job-cccccccccccccccc": sharedLayoutBinding(t, root, "ws-repo-c", "o/r2"),
	}}
	coordinator := &fakeCoordinator{newResult: dispatchResult("ps-repo", "rec-repo", "turn-repo", completedSummary())}
	dispatcher, sandbox := newSharedLayoutDispatcher(store, workspaces, coordinator, &fakeStorage{}, now)

	seedSharedLayoutJob(t, store, "job-aaaaaaaaaaaaaaaa", "o/r", now)
	if result, err := dispatcher.RunNext(context.Background()); err != nil || !result.Executed {
		t.Fatalf("first repo dispatch result=%+v err=%v", result, err)
	}
	dispatcher.PublicSessionID = func() (string, error) { return "ps-repo-two", nil }
	seedSharedLayoutJob(t, store, "job-cccccccccccccccc", "o/r2", now.Add(time.Minute))
	if result, err := dispatcher.RunNext(context.Background()); err != nil || !result.Executed {
		t.Fatalf("second repo dispatch result=%+v err=%v", result, err)
	}

	if len(sandbox.requests) != 2 {
		t.Fatalf("sandbox requests = %d, want 2", len(sandbox.requests))
	}
	first, second := sandbox.requests[0], sandbox.requests[1]
	if first.RuntimeHome == "" || first.RuntimeHome == second.RuntimeHome {
		t.Fatalf("different repos must not share a runtime home: first=%q second=%q", first.RuntimeHome, second.RuntimeHome)
	}
	wantSecond, err := storage.RuntimeHomeRoot(root, sharedLayoutIdentity().scope("o/r2"))
	if err != nil {
		t.Fatal(err)
	}
	if second.RuntimeHome != filepath.Join(wantSecond, "home") {
		t.Fatalf("second repo home = %q, want %q", second.RuntimeHome, filepath.Join(wantSecond, "home"))
	}
	if filepath.Dir(filepath.Dir(first.RuntimeHome)) != filepath.Join(root, storage.RunnerHomesDirName) ||
		filepath.Dir(filepath.Dir(second.RuntimeHome)) != filepath.Join(root, storage.RunnerHomesDirName) {
		t.Fatalf("both homes must anchor below %q: first=%q second=%q", storage.RunnerHomesDirName, first.RuntimeHome, second.RuntimeHome)
	}
}

// TestZeroRuntimeIdentityKeepsLegacySessionLayout pins the zero-identity
// behavior: existing wiring without a runner runtime identity keeps the
// per-session .sessions/<hash> runtime and receives no job scratch.
func TestZeroRuntimeIdentityKeepsLegacySessionLayout(t *testing.T) {
	store := newMemoryStore()
	now := time.Date(2026, 7, 6, 10, 0, 0, 0, time.UTC)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspaces := &fakeWorkspaces{binding: sharedLayoutBinding(t, root, "ws-legacy", "o/r")}
	coordinator := &fakeCoordinator{newResult: dispatchResult("ps-legacy", "rec-legacy", "turn-legacy", completedSummary())}
	fake := &fakeStorage{}
	dispatcher := testDispatcher(store, workspaces, coordinator, &fakeWriteback{}, now)
	dispatcher.Storage = fake
	sandbox := dispatcher.Sandbox.(*fakeSandbox)

	seedSharedLayoutJob(t, store, "job-legacy-shared", "o/r", now)
	result, err := dispatcher.RunNext(context.Background())
	if err != nil || !result.Executed || result.Status != state.StatusCompleted {
		t.Fatalf("legacy dispatch result=%+v err=%v", result, err)
	}
	if len(sandbox.requests) != 1 {
		t.Fatalf("sandbox requests = %d, want 1", len(sandbox.requests))
	}
	req := sandbox.requests[0]
	legacyRoot, err := storage.SessionRuntimeRoot(filepath.Join(root, "ws-legacy"), "o/r", "ps-generated")
	if err != nil {
		t.Fatal(err)
	}
	if req.RuntimeHome != filepath.Join(legacyRoot, "home") {
		t.Fatalf("legacy runtime home = %q, want %q", req.RuntimeHome, filepath.Join(legacyRoot, "home"))
	}
	if !strings.Contains(req.RuntimeHome, string(filepath.Separator)+storage.SessionsDirName+string(filepath.Separator)) {
		t.Fatalf("legacy runtime home must live below .sessions: %q", req.RuntimeHome)
	}
	if req.JobTmpDir != "" || req.JobGoTmpDir != "" || req.JobXDGDataHome != "" || req.JobXDGStateHome != "" {
		t.Fatalf("legacy layout must not receive job scratch: %+v", req)
	}
	if len(fake.recordCalls) != 1 || len(fake.homeCalls) != 0 || len(fake.scratchCalls) != 0 || len(fake.poolCalls) != 0 {
		t.Fatalf("legacy recording must use RecordSessionResources only: record=%d home=%d pool=%d scratch=%d",
			len(fake.recordCalls), len(fake.homeCalls), len(fake.poolCalls), len(fake.scratchCalls))
	}
	if len(fake.completeCalls) != 0 {
		t.Fatalf("legacy layout has no scratch to complete: %+v", fake.completeCalls)
	}
	if _, err := os.Lstat(filepath.Join(root, storage.RunnerHomesDirName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("shared runner-home root must not exist on the legacy layout: %v", err)
	}
}

// TestSharedLayoutRecordingFailureBlocksDispatchBeforeSandbox proves the
// fail-closed recording order: a runtime home, process pool, or job scratch
// recording failure fails the dispatch before the sandbox ever sees the
// runtime.
func TestSharedLayoutRecordingFailureBlocksDispatchBeforeSandbox(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*fakeStorage)
		assert    func(t *testing.T, fake *fakeStorage)
	}{
		{
			name:      "runtime home",
			configure: func(f *fakeStorage) { f.homeErr = errors.New("sidecar home write failed") },
			assert: func(t *testing.T, fake *fakeStorage) {
				t.Helper()
				if len(fake.homeCalls) != 1 || len(fake.poolCalls) != 0 || len(fake.scratchCalls) != 0 {
					t.Fatalf("home failure must stop before pool/scratch recording: home=%d pool=%d scratch=%d",
						len(fake.homeCalls), len(fake.poolCalls), len(fake.scratchCalls))
				}
			},
		},
		{
			name:      "process pool",
			configure: func(f *fakeStorage) { f.poolErr = errors.New("sidecar pool write failed") },
			assert: func(t *testing.T, fake *fakeStorage) {
				t.Helper()
				if len(fake.homeCalls) != 1 || len(fake.poolCalls) != 1 || len(fake.scratchCalls) != 0 {
					t.Fatalf("pool failure must stop before scratch recording: home=%d pool=%d scratch=%d",
						len(fake.homeCalls), len(fake.poolCalls), len(fake.scratchCalls))
				}
			},
		},
		{
			name:      "job scratch",
			configure: func(f *fakeStorage) { f.scratchErr = errors.New("sidecar scratch write failed") },
			assert: func(t *testing.T, fake *fakeStorage) {
				t.Helper()
				if len(fake.homeCalls) != 1 || len(fake.poolCalls) != 1 || len(fake.scratchCalls) != 1 {
					t.Fatalf("scratch failure follows home+pool recording: home=%d pool=%d scratch=%d",
						len(fake.homeCalls), len(fake.poolCalls), len(fake.scratchCalls))
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newMemoryStore()
			now := time.Date(2026, 7, 6, 10, 30, 0, 0, time.UTC)
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			workspaces := &fakeWorkspaces{binding: sharedLayoutBinding(t, root, "ws-failclosed", "o/r")}
			coordinator := &fakeCoordinator{newResult: dispatchResult("ps-failclosed", "rec-failclosed", "turn-failclosed", completedSummary())}
			fake := &fakeStorage{}
			tt.configure(fake)
			dispatcher, sandbox := newSharedLayoutDispatcher(store, workspaces, coordinator, fake, now)

			seedSharedLayoutJob(t, store, "job-dddddddddddddddd", "o/r", now)
			result, err := dispatcher.RunNext(context.Background())
			if err == nil {
				t.Fatalf("recording failure must fail the dispatch: %+v", result)
			}
			if got := loadState(t, store).Jobs["job-dddddddddddddddd"].Status; got != state.StatusFailed {
				t.Fatalf("recording failure must fail the job fail-closed, got %q", got)
			}
			if len(sandbox.requests) != 0 {
				t.Fatalf("sandbox Prepare must not run when recording fails: %+v", sandbox.requests)
			}
			tt.assert(t, fake)
		})
	}
}

// TestSharedLayoutTerminalTransitionsCompleteJobScratch covers every terminal
// path that must retire the job's disposable scratch: successful completion,
// dispatch failure, cancellation before dispatch, and confirmed cancellation
// of a running job.
func TestSharedLayoutTerminalTransitionsCompleteJobScratch(t *testing.T) {
	now := time.Date(2026, 7, 6, 11, 0, 0, 0, time.UTC)

	t.Run("success", func(t *testing.T) {
		store := newMemoryStore()
		root, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		workspaces := &fakeWorkspaces{binding: sharedLayoutBinding(t, root, "ws-terminal-ok", "o/r")}
		coordinator := &fakeCoordinator{newResult: dispatchResult("ps-terminal-ok", "rec-terminal-ok", "turn-terminal-ok", completedSummary())}
		fake := &fakeStorage{}
		dispatcher, _ := newSharedLayoutDispatcher(store, workspaces, coordinator, fake, now)

		seedSharedLayoutJob(t, store, "job-eeeeeeeeeeeeeeee", "o/r", now)
		result, err := dispatcher.RunNext(context.Background())
		if err != nil || !result.Executed || result.Status != state.StatusCompleted {
			t.Fatalf("dispatch result=%+v err=%v", result, err)
		}
		if len(fake.completeCalls) != 1 || fake.completeCalls[0].jobID != "job-eeeeeeeeeeeeeeee" || fake.completeCalls[0].repo != "o/r" {
			t.Fatalf("successful terminal completion must complete the job scratch exactly once: %+v", fake.completeCalls)
		}
	})

	t.Run("dispatch failure", func(t *testing.T) {
		store := newMemoryStore()
		root, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		workspaces := &fakeWorkspaces{binding: sharedLayoutBinding(t, root, "ws-terminal-fail", "o/r")}
		coordinator := &fakeCoordinator{newErr: errors.New("coordinator exploded")}
		fake := &fakeStorage{}
		dispatcher, _ := newSharedLayoutDispatcher(store, workspaces, coordinator, fake, now)

		seedSharedLayoutJob(t, store, "job-ffffffffffffffff", "o/r", now)
		if _, err := dispatcher.RunNext(context.Background()); err == nil {
			t.Fatalf("terminal coordinator failure must surface an error")
		}
		if got := loadState(t, store).Jobs["job-ffffffffffffffff"].Status; got != state.StatusFailed {
			t.Fatalf("job status = %q, want failed", got)
		}
		if len(fake.completeCalls) != 1 || fake.completeCalls[0].jobID != "job-ffffffffffffffff" {
			t.Fatalf("failed terminal completion must complete the job scratch exactly once: %+v", fake.completeCalls)
		}
	})

	t.Run("cancel queued", func(t *testing.T) {
		store := newMemoryStore()
		root, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		workspaceMeta := sharedLayoutBinding(t, root, "ws-cancel-queued", "o/r").Workspace
		seedState(t, store, func(st *state.RunnerState) error {
			if err := st.UpsertWorkspace(workspaceMeta); err != nil {
				return err
			}
			if err := st.UpsertPublicSession(state.PublicSession{
				Repo: "o/r", PublicSessionID: "ps-cancel-queued", IssueNumber: 30, CreatorLogin: "alice",
				AcpxRecordID: "rec-cancel-queued",
				Status:       state.StatusCompleted, Workspace: workspaceMeta, RepositoryBinding: sharedLayoutRepoBinding("o/r"),
				Queue:     state.SessionQueue{AcceptedSequence: 1, PendingJobIDs: []string{"job-1111111111111111"}},
				CreatedAt: now, LastUsedAt: now,
			}); err != nil {
				return err
			}
			if err := st.UpsertJob(state.Job{
				ID: "job-1111111111111111", Repo: "o/r", IssueNumber: 30, PublicSessionID: "ps-cancel-queued",
				CommandName: "resume", CommandPrompt: "queued behind", CommandIdempotencyKey: "key-cancel-queued",
				Status: state.StatusQueued, CreatedAt: now, Workspace: workspaceMeta,
				RepositoryBinding: sharedLayoutRepoBinding("o/r"),
			}); err != nil {
				return err
			}
			return st.UpsertCancellation(state.Cancellation{
				ID: "cancel-queued-scratch", IdempotencyKey: "cancel-key-queued-scratch", Repo: "o/r", IssueNumber: 30,
				TriggerCommentID: 909, CancelingUserLogin: "bob", TargetPublicSessionID: "ps-cancel-queued",
				Status: state.StatusQueued, CreatedAt: now,
			})
		})
		fake := &fakeStorage{}
		dispatcher, _ := newSharedLayoutDispatcher(store, &fakeWorkspaces{}, &fakeCoordinator{}, fake, now)

		result, err := dispatcher.DrainCancellations(context.Background(), 0)
		if err != nil {
			t.Fatalf("DrainCancellations: %v", err)
		}
		if !result.Executed || result.JobID != "job-1111111111111111" || result.Status != state.StatusCancelled {
			t.Fatalf("cancel result = %+v", result)
		}
		if len(fake.completeCalls) != 1 || fake.completeCalls[0].jobID != "job-1111111111111111" {
			t.Fatalf("queued cancellation must complete the job scratch: %+v", fake.completeCalls)
		}
	})

	t.Run("cancel running", func(t *testing.T) {
		store := newMemoryStore()
		root, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		workspaceMeta := sharedLayoutBinding(t, root, "ws-cancel-running", "o/r").Workspace
		seedState(t, store, func(st *state.RunnerState) error {
			if err := st.UpsertWorkspace(workspaceMeta); err != nil {
				return err
			}
			if err := st.UpsertJob(state.Job{
				ID: "job-2222222222222222", Repo: "o/r", IssueNumber: 30, PublicSessionID: "ps-cancel-running",
				CoordinatorKind: "codex", SessionCreatorLogin: "alice", TriggeringUserLogin: "alice",
				TriggerCommentID: 910, StatusWritebackKey: "status-cancel-running",
				Status: state.StatusRunning, CreatedAt: now, UpdatedAt: now, Workspace: workspaceMeta,
				RepositoryBinding: sharedLayoutRepoBinding("o/r"),
				DispatchIntent:    state.DispatchIntent{RunnerJobID: "job-2222222222222222", PublicSessionID: "ps-cancel-running"},
			}); err != nil {
				return err
			}
			if err := st.UpsertPublicSession(state.PublicSession{
				Repo: "o/r", PublicSessionID: "ps-cancel-running", IssueNumber: 30, CreatorLogin: "alice",
				Status: state.StatusRunning, Workspace: workspaceMeta, RepositoryBinding: sharedLayoutRepoBinding("o/r"),
				CreatedAt: now, LastUsedAt: now, LastJobID: "job-2222222222222222",
			}); err != nil {
				return err
			}
			return st.UpsertCancellation(state.Cancellation{
				ID: "cancel-running-scratch", IdempotencyKey: "cancel-key-running-scratch", Repo: "o/r", IssueNumber: 30,
				TriggerCommentID: 911, CancelingUserLogin: "bob", TargetPublicSessionID: "ps-cancel-running",
				Status: state.StatusQueued, CreatedAt: now,
			})
		})
		fake := &fakeStorage{}
		coordinator := &fakeCancelCoordinator{cancelResult: acpx.CancelResult{Confirmed: true, Diagnostics: "cancelled by acpx"}}
		dispatcher, _ := newSharedLayoutDispatcher(store, &fakeWorkspaces{}, &fakeCoordinator{}, fake, now)
		dispatcher.Acpx = staticAcpxFactory{coordinator: coordinator}

		result, err := dispatcher.DrainCancellations(context.Background(), 0)
		if err != nil {
			t.Fatalf("DrainCancellations: %v", err)
		}
		if !result.Executed || result.JobID != "job-2222222222222222" || result.Status != state.StatusCancelled {
			t.Fatalf("cancel result = %+v", result)
		}
		if coordinator.cancelCalls != 1 {
			t.Fatalf("acpx cancel calls = %d, want 1", coordinator.cancelCalls)
		}
		if len(fake.completeCalls) != 1 || fake.completeCalls[0].jobID != "job-2222222222222222" {
			t.Fatalf("confirmed cancellation must complete the job scratch: %+v", fake.completeCalls)
		}
		// Restart-style coordinator rebuild re-records the shared home and the
		// session process pool, never a legacy session runtime.
		if len(fake.homeCalls) != 1 || len(fake.poolCalls) != 1 || len(fake.recordCalls) != 0 {
			t.Fatalf("cancel coordinator rebuild recording: home=%d pool=%d record=%d",
				len(fake.homeCalls), len(fake.poolCalls), len(fake.recordCalls))
		}
	})
}

// TestSharedHomeMirrorsStayAtomicUnderConcurrentRefresh hammers the shared
// runtime home the way concurrent jobs do: two gh mirrors and two codex
// mirrors loop into the same shared directories while a reader continuously
// validates that every observed file is one complete generation, never a
// partial or torn write.
func TestSharedHomeMirrorsStayAtomicUnderConcurrentRefresh(t *testing.T) {
	root := t.TempDir()
	ghDest := filepath.Join(root, "shared-home", "gh")
	codexDest := filepath.Join(root, "shared-home", "codex")

	generation := func(i int) []byte {
		header := fmt.Sprintf("%08d", i)
		return []byte(header + "\n" + strings.Repeat(header+"|", 500))
	}
	writeHostAtomic := func(dir, name string, data []byte) error {
		tmp := filepath.Join(dir, ".host-write-"+name)
		if err := os.WriteFile(tmp, data, 0o600); err != nil {
			return err
		}
		return os.Rename(tmp, filepath.Join(dir, name))
	}
	validate := func(data []byte) error {
		header, rest, found := strings.Cut(string(data), "\n")
		if !found {
			return fmt.Errorf("torn mirror: header without payload (len=%d)", len(data))
		}
		if rest != strings.Repeat(header+"|", 500) {
			return fmt.Errorf("torn mirror: payload of generation %q is incomplete", header)
		}
		return nil
	}

	errCh := make(chan error, 16)
	writersDone := make(chan struct{})
	var wg sync.WaitGroup
	for g := 0; g < 2; g++ {
		source := filepath.Join(root, fmt.Sprintf("host-gh-%d", g))
		if err := os.MkdirAll(source, 0o700); err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func(source string, offset int) {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				if err := writeHostAtomic(source, "hosts.yml", generation(i*2+offset)); err != nil {
					errCh <- err
					return
				}
				if err := copyGHConfigDir(source, ghDest); err != nil {
					errCh <- fmt.Errorf("gh mirror: %w", err)
					return
				}
			}
		}(source, g)
	}
	for g := 0; g < 2; g++ {
		source := filepath.Join(root, fmt.Sprintf("host-codex-%d", g))
		if err := os.MkdirAll(source, 0o700); err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func(source string, offset int) {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				if err := writeHostAtomic(source, "auth.json", generation(i*2+offset)); err != nil {
					errCh <- err
					return
				}
				if err := copyLimitedCodexConfig(source, codexDest); err != nil {
					errCh <- fmt.Errorf("codex mirror: %w", err)
					return
				}
			}
		}(source, g)
	}
	go func() {
		wg.Wait()
		close(writersDone)
	}()

	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			select {
			case <-writersDone:
				return
			default:
			}
			for _, path := range []string{filepath.Join(ghDest, "hosts.yml"), filepath.Join(codexDest, "auth.json")} {
				data, err := os.ReadFile(path)
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				if err != nil {
					errCh <- fmt.Errorf("read %s: %w", path, err)
					return
				}
				if err := validate(data); err != nil {
					errCh <- fmt.Errorf("%s: %w", path, err)
					return
				}
			}
		}
	}()
	<-readerDone

	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
	// The final state must be complete generations of both mirrors.
	for _, path := range []string{filepath.Join(ghDest, "hosts.yml"), filepath.Join(codexDest, "auth.json")} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("final mirror %s: %v", path, err)
		}
		if err := validate(data); err != nil {
			t.Fatalf("final mirror %s: %v", path, err)
		}
	}
}

// TestSharedHomeMirrorSkipsUnchangedContent proves steady-state refreshes do
// not churn the shared home: a second mirror of byte-identical host config
// performs no write, so the installed file keeps its identity.
func TestSharedHomeMirrorSkipsUnchangedContent(t *testing.T) {
	root := t.TempDir()
	ghSource := filepath.Join(root, "host-gh")
	codexSource := filepath.Join(root, "host-codex")
	for _, dir := range []string{ghSource, codexSource} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(ghSource, "hosts.yml"), []byte("github.com:\n  oauth_token: x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexSource, "auth.json"), []byte(`{"token":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ghDest := filepath.Join(root, "shared-home", "gh")
	codexDest := filepath.Join(root, "shared-home", "codex")

	mirror := func() {
		t.Helper()
		if err := copyGHConfigDir(ghSource, ghDest); err != nil {
			t.Fatalf("gh mirror: %v", err)
		}
		if err := copyLimitedCodexConfig(codexSource, codexDest); err != nil {
			t.Fatalf("codex mirror: %v", err)
		}
	}
	mirror()
	statFile := func(path string) os.FileInfo {
		t.Helper()
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		return info
	}
	ghBefore := statFile(filepath.Join(ghDest, "hosts.yml"))
	codexBefore := statFile(filepath.Join(codexDest, "auth.json"))

	mirror()
	ghAfter := statFile(filepath.Join(ghDest, "hosts.yml"))
	codexAfter := statFile(filepath.Join(codexDest, "auth.json"))
	if !os.SameFile(ghBefore, ghAfter) || !os.SameFile(codexBefore, codexAfter) {
		t.Fatalf("unchanged mirror must not rewrite the shared home files")
	}
	if !ghAfter.ModTime().Equal(ghBefore.ModTime()) || !codexAfter.ModTime().Equal(codexBefore.ModTime()) {
		t.Fatalf("unchanged mirror must not touch modification times")
	}
	if ghAfter.Mode().Perm() != 0o600 || codexAfter.Mode().Perm() != 0o600 {
		t.Fatalf("mirrored files must stay private: gh=%v codex=%v", ghAfter.Mode(), codexAfter.Mode())
	}
}
