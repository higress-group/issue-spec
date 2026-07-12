package workspace

import (
	"context"
	"errors"
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

type credentialRunner struct {
	commands []Command
	cloneErr error
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
	return Result{}, nil
}
