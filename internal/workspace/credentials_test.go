package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/commentrunner/state"
)

func TestPrepareNewFailsClosedWhenCloneCredentialCleanupFails(t *testing.T) {
	runner := &credentialRunner{}
	manager := Manager{Root: t.TempDir(), Retention: time.Hour, Runner: runner}
	_, err := manager.PrepareNew(context.Background(), credentialRequest(cleanupHook{cleanupErr: errors.New("revoke failed")}))
	if err == nil || !strings.Contains(err.Error(), "credential cleanup") {
		t.Fatalf("PrepareNew error = %v", err)
	}
	if len(runner.commands) != 1 || strings.Join(runner.commands[0].Args, " ")[:5] != "clone" {
		t.Fatalf("commands after cleanup failure = %+v", runner.commands)
	}
	if strings.Contains(strings.Join(runner.commands[0].Env, "\n"), "secret-value") {
		t.Fatalf("secret value entered git env: %+v", runner.commands[0].Env)
	}
}

func TestPrepareNewJoinsCloneAndCredentialCleanupFailures(t *testing.T) {
	runner := &credentialRunner{cloneErr: errors.New("clone failed")}
	manager := Manager{Root: t.TempDir(), Retention: time.Hour, Runner: runner}
	_, err := manager.PrepareNew(context.Background(), credentialRequest(cleanupHook{cleanupErr: errors.New("revoke failed")}))
	if err == nil || !strings.Contains(err.Error(), "clone failed") || !strings.Contains(err.Error(), "revoke failed") {
		t.Fatalf("PrepareNew error = %v", err)
	}
}

func TestPrepareNewPersistsCredentialCloneURLAndResumeAcceptsPinnedBindingURL(t *testing.T) {
	runner := &credentialRunner{}
	manager := Manager{Root: t.TempDir(), Retention: time.Hour, Runner: runner,
		IDFunc: func(NewRequest) (string, error) { return "workspace-ssh", nil }}
	request := credentialRequest(cloneURLHook{cloneURL: "git@code.example:acme/widgets.git"})
	prepared, err := manager.PrepareNew(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if got := prepared.Workspace.CloneURL; got != "git@code.example:acme/widgets.git" {
		t.Fatalf("workspace clone URL = %q", got)
	}
	if got := strings.Join(runner.commands[0].Args, " "); !strings.Contains(got, "git@code.example:acme/widgets.git") {
		t.Fatalf("clone command = %q", got)
	}
	// ResolveResume receives the authoritative HTTPS binding URL from the
	// dispatcher, but verifies the actual SSH origin stored in workspace state.
	runner.remoteURL = "git@code.example:acme/widgets.git"
	if err := os.MkdirAll(prepared.Workspace.Path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ResolveResume(context.Background(), ResumeRequest{Repo: request.Repo,
		CloneURL: request.RepositoryBinding.CloneURL, Workspace: prepared.Workspace}); err != nil {
		t.Fatal(err)
	}
}

func TestResolveResumeRejectsGitTransportModeSwitchInBothDirections(t *testing.T) {
	const (
		httpsURL = "https://code.example/acme/widgets.git"
		sshURL   = "git@code.example:acme/widgets.git"
	)
	for _, test := range []struct {
		name, stored, current string
	}{
		{name: "HTTPS to host SSH", stored: httpsURL, current: sshURL},
		{name: "host SSH to HTTPS", stored: sshURL, current: httpsURL},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			workspacePath := filepath.Join(root, "workspace-mode-switch")
			if err := os.Mkdir(workspacePath, 0o700); err != nil {
				t.Fatal(err)
			}
			runner := &credentialRunner{remoteURL: test.stored}
			manager := Manager{Root: root, Retention: time.Hour, Runner: runner}
			request := credentialRequest(nil)
			metadata := state.WorkspaceMetadata{ID: "workspace-mode-switch", Path: workspacePath, Repo: request.Repo,
				CloneURL: test.stored, Branch: "issue-spec-test", RepositoryBinding: request.RepositoryBinding}
			_, err := manager.ResolveResume(context.Background(), ResumeRequest{Repo: request.Repo,
				CloneURL: httpsURL, ExpectedCloneURL: test.current, Workspace: metadata})
			if !errors.Is(err, ErrWorkspaceTransportMismatch) || !strings.Contains(err.Error(), "start a new session") {
				t.Fatalf("ResolveResume error = %v", err)
			}
		})
	}
}

func credentialRequest(hook CredentialExecutionHook) NewRequest {
	return NewRequest{Repo: "acme/widgets", CloneURL: "https://code.example/acme/widgets.git", DefaultBranch: "main",
		PublicSessionID: "session-1", JobID: "job-1", Credentials: hook,
		RepositoryBinding: state.RepositoryBindingSnapshot{Source: "server", IssueRepositoryKey: "acme/widgets",
			BindingID: uuid.NewString(), Version: 1, ProviderKey: "github", ExternalRepositoryID: "acme/widgets",
			CloneURL: "https://code.example/acme/widgets.git", WebURL: "https://code.example/acme/widgets", DefaultBranch: "main"}}
}

type cleanupHook struct{ cleanupErr error }

func (h cleanupHook) BeforeGit(context.Context, string, string) (GitCredential, error) {
	return GitCredential{Env: []string{"GIT_ASKPASS=/private/helper"}, Cleanup: func() error { return h.cleanupErr }}, nil
}

type cloneURLHook struct{ cloneURL string }

func (h cloneURLHook) BeforeGit(context.Context, string, string) (GitCredential, error) {
	return GitCredential{CloneURL: h.cloneURL}, nil
}

type credentialRunner struct {
	commands  []Command
	cloneErr  error
	remoteURL string
}

func (r *credentialRunner) Run(_ context.Context, command Command) (Result, error) {
	r.commands = append(r.commands, command)
	if len(command.Args) > 0 && command.Args[0] == "clone" && r.cloneErr != nil {
		return Result{ExitCode: -1}, r.cloneErr
	}
	if len(command.Args) >= 2 && command.Args[0] == "rev-parse" && command.Args[1] == "HEAD" {
		return Result{Stdout: []byte(strings.Repeat("a", 40) + "\n")}, nil
	}
	if len(command.Args) >= 3 && command.Args[0] == "rev-parse" && command.Args[1] == "--abbrev-ref" {
		return Result{Stdout: []byte("issue-spec-test\n")}, nil
	}
	if len(command.Args) >= 2 && command.Args[0] == "rev-parse" && command.Args[1] == "--show-toplevel" {
		return Result{Stdout: []byte(command.Dir + "\n")}, nil
	}
	if len(command.Args) >= 3 && command.Args[0] == "remote" && command.Args[1] == "get-url" {
		return Result{Stdout: []byte(r.remoteURL + "\n")}, nil
	}
	return Result{}, nil
}
