package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/commentrunner/state"
)

func TestPrepareNewClonesChecksOutBranchAndReturnsMetadata(t *testing.T) {
	root := t.TempDir()
	now := time.Unix(1000, 0).UTC()
	runner := &fakeGitRunner{t: t, branch: "issue-spec-ws-1", head: "abc123"}
	manager := Manager{
		Root:      root,
		Retention: time.Hour,
		Runner:    runner,
		Now:       func() time.Time { return now },
		IDFunc: func(NewRequest) (string, error) {
			return "ws-1", nil
		},
	}

	binding, err := manager.PrepareNew(context.Background(), NewRequest{
		Repo:            "o/r",
		CloneURL:        "https://github.com/o/r.git",
		DefaultBranch:   "main",
		PublicSessionID: "ps-1",
		JobID:           "job-1",
		RepositoryBinding: state.RepositoryBindingSnapshot{Source: "operator", IssueRepositoryKey: "o/r",
			BindingID: "mapping-1", Version: 3, ProviderKey: "github", ExternalRepositoryID: "o/r",
			CloneURL: "https://github.com/o/r.git", WebURL: "https://github.com/o/r", DefaultBranch: "main"},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantPath, err := canonicalPath(filepath.Join(root, "ws-1"))
	if err != nil {
		t.Fatal(err)
	}
	if binding.Workspace.Path != wantPath || binding.AcpxWorkingDirectory != wantPath || binding.SandboxWorkspacePath != wantPath {
		t.Fatalf("unexpected workspace paths: %+v", binding)
	}
	if binding.Workspace.Branch != "issue-spec-ws-1" || binding.Workspace.Ref != "main" || binding.Workspace.CheckoutSHA != "abc123" {
		t.Fatalf("unexpected git metadata: %+v", binding.Workspace)
	}
	if !binding.Workspace.RepositoryBinding.Complete() || binding.Workspace.RepositoryBinding.BindingID != "mapping-1" {
		t.Fatalf("repository binding snapshot was not copied: %+v", binding.Workspace.RepositoryBinding)
	}
	if !binding.Workspace.CleanupAfter.Equal(now.Add(time.Hour)) || binding.Workspace.RetentionPolicy == "" {
		t.Fatalf("missing retention metadata: %+v", binding.Workspace)
	}
	got := runner.argStrings()
	want := []string{
		"clone -- https://github.com/o/r.git " + wantPath,
		"checkout --force main",
		"checkout -B issue-spec-ws-1",
		"rev-parse HEAD",
		"rev-parse --abbrev-ref HEAD",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("git commands = %#v, want %#v", got, want)
	}
}

func TestPrepareNewConfiguresRepoLocalGitAuthor(t *testing.T) {
	root := t.TempDir()
	runner := &fakeGitRunner{t: t, branch: "issue-spec-ws-author", head: "abc123"}
	manager := Manager{Root: root, Retention: time.Hour, Runner: runner,
		GitAuthorName: "Issue Spec Runner", GitAuthorEmail: "runner@example.test",
		IDFunc: func(NewRequest) (string, error) { return "ws-author", nil }}

	_, err := manager.PrepareNew(t.Context(), NewRequest{Repo: "o/r", CloneURL: "https://github.com/o/r.git",
		DefaultBranch: "main", PublicSessionID: "ps-author", JobID: "job-author", RepositoryBinding: testRepositoryBinding()})
	if err != nil {
		t.Fatal(err)
	}
	wantPath, err := canonicalPath(filepath.Join(root, "ws-author"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"clone -- https://github.com/o/r.git " + wantPath,
		"config --local user.name Issue Spec Runner",
		"config --local user.email runner@example.test",
		"checkout --force main",
		"checkout -B issue-spec-ws-author",
		"rev-parse HEAD",
		"rev-parse --abbrev-ref HEAD",
	}
	if got := runner.argStrings(); !reflect.DeepEqual(got, want) {
		t.Fatalf("git commands = %#v, want %#v", got, want)
	}
}

func TestRepoLocalGitAuthorCommitsWithoutGlobalOrSystemConfig(t *testing.T) {
	origin := filepath.Join(t.TempDir(), "origin.git")
	seed := t.TempDir()
	runGitCommand(t, "", nil, "init", "--bare", "--initial-branch=main", origin)
	runGitCommand(t, seed, nil, "init", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("seed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitCommand(t, seed, nil, "add", "README.md")
	runGitCommand(t, seed, nil, "-c", "user.name=Seed", "-c", "user.email=seed@example.test", "commit", "-m", "seed")
	runGitCommand(t, seed, nil, "remote", "add", "origin", origin)
	runGitCommand(t, seed, nil, "push", "origin", "main")

	manager := Manager{Root: t.TempDir(), Retention: time.Hour,
		GitAuthorName: "Issue Spec Runner", GitAuthorEmail: "runner@example.test",
		IDFunc: func(NewRequest) (string, error) { return "ws-real-author", nil }}
	binding, err := manager.PrepareNew(t.Context(), NewRequest{Repo: "o/r", CloneURL: origin, DefaultBranch: "main",
		PublicSessionID: "ps-real-author", JobID: "job-real-author", RepositoryBinding: testRepositoryBindingForClone(origin)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binding.Workspace.Path, "README.md"), []byte("seed\nrunner\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	isolatedGitEnv := []string{"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "HOME=" + t.TempDir()}
	runGitCommand(t, binding.Workspace.Path, isolatedGitEnv, "add", "README.md")
	runGitCommand(t, binding.Workspace.Path, isolatedGitEnv, "commit", "-m", "runner commit")
	author := runGitCommand(t, binding.Workspace.Path, isolatedGitEnv, "show", "-s", "--format=%an <%ae>", "HEAD")
	if strings.TrimSpace(author) != "Issue Spec Runner <runner@example.test>" {
		t.Fatalf("commit author = %q", author)
	}
}

func TestPrepareNewRejectsUnsafeWorkspaceIDBeforeGit(t *testing.T) {
	runner := &fakeGitRunner{t: t}
	manager := Manager{Root: t.TempDir(), Retention: time.Hour, Runner: runner}
	_, err := manager.PrepareNew(context.Background(), NewRequest{
		Repo:            "o/r",
		CloneURL:        "https://github.com/o/r.git",
		DefaultBranch:   "main",
		WorkspaceID:     "../escape",
		PublicSessionID: "ps-1",
		JobID:           "job-1",
		RepositoryBinding: state.RepositoryBindingSnapshot{Source: "operator", IssueRepositoryKey: "o/r",
			BindingID: "mapping-1", Version: 1, ProviderKey: "github", ExternalRepositoryID: "o/r",
			CloneURL: "https://github.com/o/r.git", WebURL: "https://github.com/o/r", DefaultBranch: "main"},
	})
	if err == nil || !strings.Contains(err.Error(), "unsafe workspace id") {
		t.Fatalf("expected unsafe id error, got %v", err)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("git should not run for unsafe id: %#v", runner.commands)
	}
}

func TestPrepareNewRejectsOptionLikeDefaultBranchBeforeGit(t *testing.T) {
	runner := &fakeGitRunner{t: t}
	manager := Manager{Root: t.TempDir(), Retention: time.Hour, Runner: runner,
		IDFunc: func(NewRequest) (string, error) { return "ws-option", nil }}
	_, err := manager.PrepareNew(context.Background(), NewRequest{
		Repo: "o/r", CloneURL: "https://github.com/o/r.git", DefaultBranch: "--upload-pack=evil",
		PublicSessionID: "ps-1", JobID: "job-1", RepositoryBinding: state.RepositoryBindingSnapshot{
			Source: "operator", IssueRepositoryKey: "o/r", BindingID: "mapping-1", Version: 1,
			ProviderKey: "github", ExternalRepositoryID: "o/r", CloneURL: "https://github.com/o/r.git",
			WebURL: "https://github.com/o/r", DefaultBranch: "--upload-pack=evil",
		},
	})
	if err == nil {
		t.Fatal("PrepareNew accepted option-like default branch")
	}
	if got := runner.argStrings(); len(got) != 0 {
		t.Fatalf("git ran before ref rejection: %+v", got)
	}
}

func TestResolveResumeValidatesStoredWorkspaceAndRefreshesRetention(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "ws-1")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(2000, 0).UTC()
	runner := &fakeGitRunner{t: t, topLevel: path, remote: "https://github.com/o/r.git", branch: "issue-spec-ws-1"}
	manager := Manager{Root: root, Retention: 2 * time.Hour, Runner: runner, Now: func() time.Time { return now }}

	binding, err := manager.ResolveResume(context.Background(), ResumeRequest{
		Repo:     "o/r",
		CloneURL: "https://github.com/o/r.git",
		Workspace: state.WorkspaceMetadata{
			ID:       "ws-1",
			Path:     path,
			Repo:     "o/r",
			CloneURL: "https://github.com/o/r.git",
			Branch:   "issue-spec-ws-1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !binding.Workspace.LastUsedAt.Equal(now) || !binding.Workspace.CleanupAfter.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("resume did not refresh timestamps: %+v", binding.Workspace)
	}

	_, err = manager.ResolveResume(context.Background(), ResumeRequest{Workspace: state.WorkspaceMetadata{ID: "ws-x", Path: filepath.Join(root, "..", "outside")}})
	if err == nil || !strings.Contains(err.Error(), "escapes workspace root") {
		t.Fatalf("expected path escape rejection, got %v", err)
	}
}

func TestResolveResumeAcceptsRealpathWorkspaceUnderSymlinkedRoot(t *testing.T) {
	realRoot := t.TempDir()
	linkParent := t.TempDir()
	linkRoot := filepath.Join(linkParent, "workspaces")
	if err := os.Symlink(realRoot, linkRoot); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	pathViaLink := filepath.Join(linkRoot, "ws-1")
	if err := os.MkdirAll(pathViaLink, 0o700); err != nil {
		t.Fatal(err)
	}
	realPath, err := filepath.EvalSymlinks(pathViaLink)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(2100, 0).UTC()
	runner := &fakeGitRunner{t: t, topLevel: realPath, remote: "https://github.com/o/r.git", branch: "issue-spec-ws-1"}
	manager := Manager{Root: linkRoot, Retention: 2 * time.Hour, Runner: runner, Now: func() time.Time { return now }}

	binding, err := manager.ResolveResume(context.Background(), ResumeRequest{
		Repo:     "o/r",
		CloneURL: "https://github.com/o/r.git",
		Workspace: state.WorkspaceMetadata{
			ID:       "ws-1",
			Path:     realPath,
			Repo:     "o/r",
			CloneURL: "https://github.com/o/r.git",
			Branch:   "issue-spec-ws-1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if binding.Workspace.Path != realPath || binding.AcpxWorkingDirectory != realPath || binding.SandboxWorkspacePath != realPath {
		t.Fatalf("resume did not preserve canonical workspace path: %+v", binding)
	}
}

func TestWorkspaceLocksAreExclusive(t *testing.T) {
	root := t.TempDir()
	now := time.Unix(3000, 0).UTC()
	manager := Manager{
		Root:      root,
		Retention: time.Hour,
		Now:       func() time.Time { return now },
		TokenFunc: fixedTokens(t, "token-1"),
	}

	first, err := manager.AcquireLock(context.Background(), LockRequest{Repo: "o/r", PublicSessionID: "ps-1", JobID: "job-1", WorkspaceID: "ws-1"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.AcquireLock(context.Background(), LockRequest{Repo: "o/r", PublicSessionID: "ps-1", JobID: "job-2", WorkspaceID: "ws-1"})
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("expected held lock, got %v", err)
	}
	if err := manager.ReleaseLock(first); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceLockRecoversUnlockedStaleFile(t *testing.T) {
	root := t.TempDir()
	now := time.Unix(3100, 0).UTC()
	lockPath, err := lockPath(root, "o/r", "ps-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		t.Fatal(err)
	}
	stale := lockRecord{
		Repo:            "o/r",
		PublicSessionID: "ps-1",
		JobID:           "job-stale",
		WorkspaceID:     "ws-1",
		Token:           "token-stale",
		ProcessID:       12345,
		AcquiredAt:      now.Add(-2 * time.Hour),
	}
	writeTestLockRecord(t, lockPath, stale)
	manager := Manager{
		Root:      root,
		Retention: time.Hour,
		Now:       func() time.Time { return now },
		TokenFunc: fixedTokens(t, "token-recovered"),
	}

	recovered, err := manager.AcquireLock(context.Background(), LockRequest{Repo: "o/r", PublicSessionID: "ps-1", JobID: "job-recovered", WorkspaceID: "ws-1"})
	if err != nil {
		t.Fatal(err)
	}
	if recovered.StaleRecoveredAt.IsZero() || recovered.WorkspaceLockToken != "token-recovered" {
		t.Fatalf("expected stale recovery metadata: %+v", recovered)
	}
	record, err := readLock(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if record.JobID != "job-recovered" || record.Token != "token-recovered" || record.ProcessID != os.Getpid() {
		t.Fatalf("stale lock was not replaced by current owner: %+v", record)
	}
	if err := manager.ReleaseLock(state.SessionLock{OwnerJobID: "job-stale", WorkspaceLockToken: "token-stale", WorkspaceLockPath: lockPath}); err == nil {
		t.Fatal("expected stale first token release to fail")
	}
	if err := manager.ReleaseLock(recovered); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceStaleRecoveryDoesNotDeleteCurrentLock(t *testing.T) {
	root := t.TempDir()
	now := time.Unix(3200, 0).UTC()
	manager := Manager{
		Root:      root,
		Retention: time.Hour,
		Now:       func() time.Time { return now },
		TokenFunc: fixedTokens(t, "token-current"),
	}
	current, err := manager.AcquireLock(context.Background(), LockRequest{Repo: "o/r", PublicSessionID: "ps-1", JobID: "job-current", WorkspaceID: "ws-1"})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Hour)
	_, err = manager.AcquireLock(context.Background(), LockRequest{Repo: "o/r", PublicSessionID: "ps-1", JobID: "job-next", WorkspaceID: "ws-1"})
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("expected current lock to remain held, got %v", err)
	}
	if err := manager.ReleaseLock(state.SessionLock{OwnerJobID: "job-stale", WorkspaceLockToken: "token-stale", WorkspaceLockPath: current.WorkspaceLockPath}); err == nil {
		t.Fatal("expected stale token release to fail")
	}
	record, err := readLock(current.WorkspaceLockPath)
	if err != nil {
		t.Fatal(err)
	}
	if record.JobID != "job-current" || record.Token != "token-current" {
		t.Fatalf("current lock was modified or removed: %+v", record)
	}
	if err := manager.ReleaseLock(current); err != nil {
		t.Fatal(err)
	}
}

func TestCleanupHonorsActiveDirtyAndRetentionDecisions(t *testing.T) {
	root := t.TempDir()
	now := time.Unix(4000, 0).UTC()
	activePath := mkdirWorkspace(t, root, "active")
	recentPath := mkdirWorkspace(t, root, "recent")
	staleDeadlinePath := mkdirWorkspace(t, root, "stale-deadline")
	dirtyPath := mkdirWorkspace(t, root, "dirty")
	expiredPath := mkdirWorkspace(t, root, "expired")
	manager := Manager{Root: root, Retention: time.Hour, Now: func() time.Time { return now }, Runner: &fakeGitRunner{t: t}}

	workspaces := []state.WorkspaceMetadata{
		{ID: "active", Path: activePath, Repo: "o/r", LastUsedAt: now.Add(-3 * time.Hour)},
		{ID: "recent", Path: recentPath, Repo: "o/r", LastUsedAt: now.Add(-30 * time.Minute)},
		{ID: "stale-deadline", Path: staleDeadlinePath, Repo: "o/r", LastUsedAt: now.Add(-30 * time.Minute), CleanupAfter: now.Add(-time.Hour)},
		{ID: "dirty", Path: dirtyPath, Repo: "o/r", LastUsedAt: now.Add(-3 * time.Hour), Dirty: true},
		{ID: "expired", Path: expiredPath, Repo: "o/r", LastUsedAt: now.Add(-3 * time.Hour)},
	}
	results, err := manager.Cleanup(context.Background(), CleanupRequest{Workspaces: workspaces, ActiveIDs: map[string]bool{"active": true}})
	if err != nil {
		t.Fatal(err)
	}
	actions := map[string]string{}
	for _, result := range results {
		actions[result.WorkspaceID] = result.Action + ":" + result.Reason
	}
	want := map[string]string{
		"active":         "kept:active",
		"recent":         "kept:within_retention",
		"stale-deadline": "kept:within_retention",
		"dirty":          "kept:dirty_or_uncertain",
		"expired":        "removed:expired",
	}
	if !reflect.DeepEqual(actions, want) {
		t.Fatalf("cleanup decisions = %#v, want %#v", actions, want)
	}
	if _, err := os.Stat(expiredPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired workspace still exists or unexpected stat error: %v", err)
	}
	if _, err := os.Stat(recentPath); err != nil {
		t.Fatalf("recent workspace removed: %v", err)
	}
	if _, err := os.Stat(staleDeadlinePath); err != nil {
		t.Fatalf("recent workspace with stale cleanup deadline removed: %v", err)
	}
}

func TestCleanupKeepsSessionCloneUntilLinkedWorktreesAreGone(t *testing.T) {
	root := t.TempDir()
	now := time.Unix(5000, 0).UTC()
	expired := mkdirWorkspace(t, root, "expired")
	linked := filepath.Join(root, "linked-process")
	runner := &fakeGitRunner{t: t, worktrees: []string{expired, linked}}
	manager := Manager{Root: root, Retention: time.Hour, Now: func() time.Time { return now }, Runner: runner}
	results, err := manager.Cleanup(context.Background(), CleanupRequest{Workspaces: []state.WorkspaceMetadata{{ID: "expired", Path: expired, LastUsedAt: now.Add(-2 * time.Hour)}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Action != "kept" || results[0].Reason != "linked_worktrees" {
		t.Fatalf("cleanup with linked worktree = %+v", results)
	}
	if _, err := os.Stat(expired); err != nil {
		t.Fatalf("session clone was removed with linked worktree: %v", err)
	}
}

func TestCleanupFailsClosedWhenWorktreeInspectionFailsOrOmitsSessionClone(t *testing.T) {
	for _, tc := range []struct {
		name   string
		runner func(*testing.T, string) Runner
	}{
		{name: "command failure", runner: func(t *testing.T, _ string) Runner {
			return &fakeGitRunner{t: t, worktreeErr: errors.New("inspection failed")}
		}},
		{name: "session clone omitted", runner: func(t *testing.T, root string) Runner {
			return &fakeGitRunner{t: t, worktrees: []string{filepath.Join(root, "other")}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			now := time.Unix(6000, 0).UTC()
			expired := mkdirWorkspace(t, root, "expired")
			manager := Manager{Root: root, Retention: time.Hour, Now: func() time.Time { return now }, Runner: tc.runner(t, root)}
			results, err := manager.Cleanup(context.Background(), CleanupRequest{Workspaces: []state.WorkspaceMetadata{{ID: "expired", Path: expired, LastUsedAt: now.Add(-2 * time.Hour)}}})
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != 1 || results[0].Action != "kept" || results[0].Reason != "worktree_inspection_failed" {
				t.Fatalf("fail-closed cleanup = %+v", results)
			}
			if _, err := os.Stat(expired); err != nil {
				t.Fatalf("session clone was removed after inspection failure: %v", err)
			}
		})
	}
}

type fakeGitRunner struct {
	t           *testing.T
	commands    []Command
	topLevel    string
	remote      string
	branch      string
	head        string
	worktrees   []string
	worktreeErr error
}

func testRepositoryBinding() state.RepositoryBindingSnapshot {
	return testRepositoryBindingForClone("https://github.com/o/r.git")
}

func testRepositoryBindingForClone(cloneURL string) state.RepositoryBindingSnapshot {
	return state.RepositoryBindingSnapshot{Source: "operator", IssueRepositoryKey: "o/r", BindingID: "mapping-test",
		Version: 1, ProviderKey: "git", ExternalRepositoryID: "o/r", CloneURL: cloneURL,
		WebURL: "https://code.example.test/o/r", DefaultBranch: "main"}
}

func runGitCommand(t *testing.T, dir string, extraEnv []string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	if len(extraEnv) > 0 {
		replaced := map[string]bool{}
		for _, entry := range extraEnv {
			name, _, ok := strings.Cut(entry, "=")
			if ok {
				replaced[name] = true
			}
		}
		for _, entry := range os.Environ() {
			name, _, ok := strings.Cut(entry, "=")
			if ok && !replaced[name] {
				cmd.Env = append(cmd.Env, entry)
			}
		}
		cmd.Env = append(cmd.Env, extraEnv...)
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func (r *fakeGitRunner) Run(_ context.Context, command Command) (Result, error) {
	r.commands = append(r.commands, command)
	switch command.Args[0] {
	case "clone":
		return Result{}, os.MkdirAll(command.Args[len(command.Args)-1], 0o700)
	case "checkout":
		return Result{}, nil
	case "config":
		return Result{}, nil
	case "rev-parse":
		if reflect.DeepEqual(command.Args, []string{"rev-parse", "HEAD"}) {
			return Result{Stdout: []byte(first(r.head, "abc") + "\n")}, nil
		}
		if reflect.DeepEqual(command.Args, []string{"rev-parse", "--abbrev-ref", "HEAD"}) {
			return Result{Stdout: []byte(first(r.branch, "main") + "\n")}, nil
		}
		if reflect.DeepEqual(command.Args, []string{"rev-parse", "--show-toplevel"}) {
			return Result{Stdout: []byte(first(r.topLevel, command.Dir) + "\n")}, nil
		}
	case "remote":
		if reflect.DeepEqual(command.Args, []string{"remote", "get-url", "origin"}) {
			return Result{Stdout: []byte(first(r.remote, "https://github.com/o/r.git") + "\n")}, nil
		}
	case "worktree":
		if r.worktreeErr != nil {
			return Result{ExitCode: 1, Stderr: []byte(r.worktreeErr.Error())}, r.worktreeErr
		}
		paths := r.worktrees
		if len(paths) == 0 {
			paths = []string{command.Dir}
		}
		var output strings.Builder
		for _, path := range paths {
			fmt.Fprintf(&output, "worktree %s\nHEAD abc\nbranch refs/heads/main\n\n", path)
		}
		return Result{Stdout: []byte(output.String())}, nil
	}
	r.t.Fatalf("unexpected git command: %#v", command)
	return Result{ExitCode: 1}, nil
}

func (r *fakeGitRunner) argStrings() []string {
	out := make([]string, 0, len(r.commands))
	for _, command := range r.commands {
		out = append(out, strings.Join(command.Args, " "))
	}
	return out
}

func mkdirWorkspace(t *testing.T, root, id string) string {
	t.Helper()
	path := filepath.Join(root, id)
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func fixedTokens(t *testing.T, tokens ...string) func() (string, error) {
	t.Helper()
	return func() (string, error) {
		if len(tokens) == 0 {
			t.Fatal("unexpected token request")
		}
		token := tokens[0]
		tokens = tokens[1:]
		return token, nil
	}
}

func writeTestLockRecord(t *testing.T, path string, record lockRecord) {
	t.Helper()
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func first(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
