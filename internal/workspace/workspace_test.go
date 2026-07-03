package workspace

import (
	"context"
	"errors"
	"os"
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
	})
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(root, "ws-1")
	if binding.Workspace.Path != wantPath || binding.AcpxWorkingDirectory != wantPath || binding.SandboxWorkspacePath != wantPath {
		t.Fatalf("unexpected workspace paths: %+v", binding)
	}
	if binding.Workspace.Branch != "issue-spec-ws-1" || binding.Workspace.Ref != "main" || binding.Workspace.CheckoutSHA != "abc123" {
		t.Fatalf("unexpected git metadata: %+v", binding.Workspace)
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
	})
	if err == nil || !strings.Contains(err.Error(), "unsafe workspace id") {
		t.Fatalf("expected unsafe id error, got %v", err)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("git should not run for unsafe id: %#v", runner.commands)
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

func TestWorkspaceLocksAreExclusiveAndRecoverStale(t *testing.T) {
	root := t.TempDir()
	now := time.Unix(3000, 0).UTC()
	tokens := []string{"token-1", "token-2"}
	manager := Manager{
		Root:      root,
		Retention: time.Hour,
		Now:       func() time.Time { return now },
		TokenFunc: func() (string, error) {
			token := tokens[0]
			tokens = tokens[1:]
			return token, nil
		},
	}

	first, err := manager.AcquireLock(context.Background(), LockRequest{Repo: "o/r", PublicSessionID: "ps-1", JobID: "job-1", WorkspaceID: "ws-1", StaleAfter: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.AcquireLock(context.Background(), LockRequest{Repo: "o/r", PublicSessionID: "ps-1", JobID: "job-2", WorkspaceID: "ws-1", StaleAfter: time.Hour})
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("expected held lock, got %v", err)
	}
	now = now.Add(2 * time.Hour)
	recovered, err := manager.AcquireLock(context.Background(), LockRequest{Repo: "o/r", PublicSessionID: "ps-1", JobID: "job-3", WorkspaceID: "ws-1", StaleAfter: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if recovered.StaleRecoveredAt.IsZero() || recovered.WorkspaceLockToken != "token-2" {
		t.Fatalf("expected stale recovery metadata: %+v", recovered)
	}
	if err := manager.ReleaseLock(first); err == nil {
		t.Fatal("expected stale first token release to fail")
	}
	if err := manager.ReleaseLock(recovered); err != nil {
		t.Fatal(err)
	}
}

func TestCleanupHonorsActiveDirtyAndRetentionDecisions(t *testing.T) {
	root := t.TempDir()
	now := time.Unix(4000, 0).UTC()
	activePath := mkdirWorkspace(t, root, "active")
	recentPath := mkdirWorkspace(t, root, "recent")
	dirtyPath := mkdirWorkspace(t, root, "dirty")
	expiredPath := mkdirWorkspace(t, root, "expired")
	manager := Manager{Root: root, Retention: time.Hour, Now: func() time.Time { return now }}

	workspaces := []state.WorkspaceMetadata{
		{ID: "active", Path: activePath, Repo: "o/r", LastUsedAt: now.Add(-3 * time.Hour)},
		{ID: "recent", Path: recentPath, Repo: "o/r", LastUsedAt: now.Add(-30 * time.Minute)},
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
		"active":  "kept:active",
		"recent":  "kept:within_retention",
		"dirty":   "kept:dirty_or_uncertain",
		"expired": "removed:expired",
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
}

type fakeGitRunner struct {
	t        *testing.T
	commands []Command
	topLevel string
	remote   string
	branch   string
	head     string
}

func (r *fakeGitRunner) Run(_ context.Context, command Command) (Result, error) {
	r.commands = append(r.commands, command)
	switch command.Args[0] {
	case "clone":
		return Result{}, os.MkdirAll(command.Args[len(command.Args)-1], 0o700)
	case "checkout":
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

func first(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
