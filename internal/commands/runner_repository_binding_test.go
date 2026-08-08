package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/commentrunner"
	"github.com/higress-group/issue-spec/internal/commentrunner/intake"
	"github.com/higress-group/issue-spec/internal/commentrunner/jobs"
	"github.com/higress-group/issue-spec/internal/commentrunner/repository"
	"github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/commentrunner/testkit"
	"github.com/higress-group/issue-spec/internal/github"
)

func TestRunnerPollRejectsInvalidGitHubMetadataBeforeIntake(t *testing.T) {
	backend := &runnerPhaseBackend{
		fakeGitHubBackend: fakeGitHubBackend{info: github.BackendInfo{Name: "rest", Kind: "rest", Host: "github.com"}},
		repositoryMetadata: github.Repository{ID: 7301, FullName: "attacker/other", CloneURL: "https://github.com/attacker/other.git",
			HTMLURL: "https://github.com/attacker/other", DefaultBranch: "main"},
	}
	var out, errOut bytes.Buffer
	app := newApp(strings.NewReader(""), &out, &errOut)
	app.runnerPreflight = func(ctx context.Context, cfg commentrunner.Config) commentrunner.PreflightReport {
		return commentrunner.RunPreflight(ctx, cfg, commentrunner.PreflightDependencies{
			SelectBackend: func(context.Context, string) (auth.GitHubBackendSelection, error) {
				return auth.GitHubBackendSelection{Name: auth.GitHubBackendNameREST, Kind: auth.GitHubBackendKindREST,
					Mode: auth.GitHubBackendModeREST, Host: "github.com", SelectionSource: "test"}, nil
			},
			OpenBackend: func(context.Context, auth.GitHubBackendSelection) (commentrunner.PreflightRunnerBackend, error) {
				return backend, nil
			},
			LookPath: func(name string) (string, error) { return "/test/bin/" + name, nil },
		})
	}
	app.runnerIntake = func(context.Context, commentrunner.Config, intake.Options) (intake.Result, error) {
		t.Fatal("invalid authenticated repository metadata reached command intake")
		return intake.Result{}, nil
	}

	code := app.runRunner(t.Context(), []string{"poll", "--repo", "o/r", "--runner", "bot", "--backend", "rest",
		"--state", filepath.Join(t.TempDir(), "state.json"), "--workspace-root", t.TempDir(), "--unsafe-no-sandbox", "--dry-run", "--json"})
	if code != 1 {
		t.Fatalf("exit code = %d, stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	var result runnerDryRunResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	check := runnerPreflightCheck(t, result.Preflight, "repository-binding:o/r")
	if result.Preflight.OK || check.Status != commentrunner.CheckError || !strings.Contains(check.Detail, "does not match configured repository") {
		t.Fatalf("invalid metadata was not reported before intake: %+v", result.Preflight)
	}
}

func TestBuildRunnerDispatcherPinsAuthenticatedGitHubMetadataBeforeWorkspace(t *testing.T) {
	metadata := github.Repository{ID: 7301, FullName: "o/r", CloneURL: "https://github.com/o/r.git",
		HTMLURL: "https://github.com/o/r", DefaultBranch: "main"}
	backend := &runnerPhaseBackend{
		fakeGitHubBackend:  fakeGitHubBackend{info: github.BackendInfo{Name: "rest", Kind: "rest", Host: "github.com"}},
		repositoryMetadata: metadata,
	}
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	app.selectRunnerBackend = func(context.Context, string, auth.GitHubBackendMode) (auth.GitHubBackendSelection, error) {
		return auth.GitHubBackendSelection{Name: auth.GitHubBackendNameREST, Kind: auth.GitHubBackendKindREST,
			Mode: auth.GitHubBackendModeREST, Host: "github.com", SelectionSource: "test"}, nil
	}
	app.newGitHubBackend = func(context.Context, auth.GitHubBackendSelection) (github.Backend, error) {
		return backend, nil
	}

	store := &runnerBindingStore{MemoryStore: testkit.NewMemoryStore()}
	cfg := runnerRepositoryBindingConfig(t)
	dispatcher, cleanup, err := app.buildRunnerDispatcher(t.Context(), cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	if cleanup != nil {
		t.Fatal("caller-provided state store unexpectedly produced cleanup")
	}

	workspaceRoot := filepath.Join(cfg.WorkspaceRoot, "ws-github-binding")
	if err := os.MkdirAll(workspaceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	binding := testkit.WorkspaceBinding("ws-github-binding")
	binding.Workspace.Path = workspaceRoot
	binding.AcpxWorkingDirectory = workspaceRoot
	binding.SandboxWorkspacePath = workspaceRoot
	workspaces := &testkit.Workspaces{Binding: binding}
	stopAfterWorkspace := errors.New("stop after workspace preparation")
	dispatcher.Workspaces = workspaces
	dispatcher.Sandbox = &testkit.Sandbox{Err: stopAfterWorkspace}
	dispatcher.Acpx = &testkit.AcpxFactory{}
	dispatcher.Artifacts = jobs.NoopArtifactProvider{}
	dispatcher.Writeback = &testkit.Writeback{Store: store.MemoryStore}
	dispatcher.Clock = testkit.Clock{Time: testkit.Now}
	dispatcher.PublicSessionID = func() (string, error) { return "ps-github-binding", nil }
	dispatcher.TurnCorrelationID = func() (string, error) { return "turn-github-binding", nil }

	if err := store.Update(t.Context(), func(st *state.RunnerState) error {
		_, _, err := st.CreateCommandJob(state.Job{ID: "job-3793793793790001", Repo: "o/r", IssueNumber: 379,
			CommandID: "cmd-github-binding", CommandName: "new", CommandPrompt: "implement", CommandIdempotencyKey: "github-binding",
			SessionCreatorLogin: "alice", TriggeringUserLogin: "alice", TriggerCommentID: 37901,
			Status: state.StatusQueued, CreatedAt: testkit.Now, FirstObservedComment: state.SeenComment{
				Repo: "o/r", IssueNumber: 379, CommentID: 37901, AuthorLogin: "alice", FirstObservedUpdatedAt: testkit.Now,
				FirstObservedBodyHash: "sha256:github-binding",
			}})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	result, err := dispatcher.RunNext(t.Context())
	if !errors.Is(err, stopAfterWorkspace) || result.Status != state.StatusFailed {
		t.Fatalf("RunNext result=%+v err=%v", result, err)
	}
	if backend.repositoryMetadataCalls != 1 || len(workspaces.PrepareNewRequests) != 1 {
		t.Fatalf("metadata calls=%d workspace requests=%d", backend.repositoryMetadataCalls, len(workspaces.PrepareNewRequests))
	}
	want, err := (repository.GitHubResolver{Metadata: backend}).ResolveRepository(t.Context(), "o/r")
	if err != nil {
		t.Fatal(err)
	}
	pinned := workspaces.PrepareNewRequests[0].RepositoryBinding
	job := store.Snapshot().Jobs["job-3793793793790001"]
	if !pinned.Equal(want.Binding) || !job.RepositoryBinding.Equal(want.Binding) || !job.DispatchIntent.RepositoryBinding.Equal(want.Binding) {
		t.Fatalf("authenticated metadata was not pinned before workspace: request=%+v job=%+v intent=%+v want=%+v",
			pinned, job.RepositoryBinding, job.DispatchIntent.RepositoryBinding, want.Binding)
	}
}

func TestBuildRunnerDispatcherUsesLiveMetadataForBindingDrift(t *testing.T) {
	backend := &runnerPhaseBackend{
		fakeGitHubBackend: fakeGitHubBackend{info: github.BackendInfo{Name: "rest", Kind: "rest", Host: "github.com"}},
		repositoryMetadata: github.Repository{ID: 7301, FullName: "o/r", CloneURL: "https://github.com/o/r.git",
			HTMLURL: "https://github.com/o/r", DefaultBranch: "main"},
	}
	app := newApp(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	app.selectRunnerBackend = func(context.Context, string, auth.GitHubBackendMode) (auth.GitHubBackendSelection, error) {
		return auth.GitHubBackendSelection{Name: auth.GitHubBackendNameREST, Kind: auth.GitHubBackendKindREST,
			Mode: auth.GitHubBackendModeREST, Host: "github.com", SelectionSource: "test"}, nil
	}
	app.newGitHubBackend = func(context.Context, auth.GitHubBackendSelection) (github.Backend, error) { return backend, nil }
	dispatcher, _, err := app.buildRunnerDispatcher(t.Context(), runnerRepositoryBindingConfig(t), &runnerBindingStore{MemoryStore: testkit.NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := dispatcher.Repositories.ResolveRepository(t.Context(), "o/r")
	if err != nil {
		t.Fatal(err)
	}
	backend.repositoryMetadata.DefaultBranch = "trunk"
	current, err := dispatcher.Repositories.ResolveRepository(t.Context(), "o/r")
	if err != nil {
		t.Fatal(err)
	}
	if backend.repositoryMetadataCalls != 2 || current.DefaultBranch != "trunk" ||
		repository.DiagnosticCode(repository.ValidatePinned(pinned.Binding, current.Binding)) != repository.DiagnosticBindingDrift {
		t.Fatalf("live dispatcher resolution did not detect drift: pinned=%+v current=%+v calls=%d", pinned, current, backend.repositoryMetadataCalls)
	}
}

type runnerBindingStore struct {
	*testkit.MemoryStore
}

func (s *runnerBindingStore) Close() error { return nil }

func runnerRepositoryBindingConfig(t *testing.T) commentrunner.Config {
	t.Helper()
	return commentrunner.Config{Hostname: "github.com", Repositories: []string{"o/r"}, RunnerIdentity: "bot",
		GitHubBackend: auth.GitHubBackendModeREST, StatePath: filepath.Join(t.TempDir(), "state.json"),
		PollInterval: commentrunner.NewDuration(time.Minute), FallbackInterval: commentrunner.NewDuration(time.Hour),
		MaxConcurrentJobs: 1, AcpxPath: "acpx", Agent: commentrunner.DefaultAgentConfig(), WorkspaceRoot: t.TempDir(),
		WorkspaceRetention: commentrunner.NewDuration(time.Hour), CancellationEnabled: true}.Normalized()
}
