package commands

import (
	"bytes"
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

	"github.com/higress-group/issue-spec/internal/acpx"
	"github.com/higress-group/issue-spec/internal/commentrunner/jobs"
	"github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/commentrunner/testkit"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/processworkspace"
	runnerworkspace "github.com/higress-group/issue-spec/internal/workspace"
)

func TestRunnerNativeProcessActorLifecycleUsesOneACPXCoordinator(t *testing.T) {
	skipWithoutRealGit(t)
	sessionClone, base := workspaceGitRepository(t)
	canonicalSessionClone, err := filepath.EvalSymlinks(sessionClone)
	if err != nil {
		t.Fatal(err)
	}
	binding := testkit.WorkspaceBinding("ws-native-actor")
	binding.Workspace.Path = canonicalSessionClone
	binding.AcpxWorkingDirectory = canonicalSessionClone
	binding.SandboxWorkspacePath = canonicalSessionClone

	store := testkit.NewMemoryStore()
	if err := store.Update(t.Context(), func(st *state.RunnerState) error {
		_, _, err := st.CreateCommandJob(state.Job{
			ID: "job-native-actor", Repo: "o/r", IssueNumber: 177, CoordinatorKind: "codex", Model: "gpt-5.5[xhigh]",
			SessionCreatorLogin: "alice", TriggeringUserLogin: "alice", TriggerCommentID: 17701,
			CommandID: "cmd-native-actor", CommandName: "new", CommandPrompt: "run native child lifecycle",
			CommandIdempotencyKey: "cmd-key-native-actor", StatusWritebackKey: "status-native-actor",
			Status: state.StatusQueued, CreatedAt: testkit.Now,
			FirstObservedComment: state.SeenComment{Repo: "o/r", IssueNumber: 177, CommentID: 17701,
				HTMLURL: "https://github.com/o/r/issues/177#issuecomment-17701", AuthorLogin: "alice",
				FirstObservedBodyHash: "sha256:native-actor", StatusWritebackIdempotencyKey: "status-native-actor"},
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	backend := newWorkspaceCASBackend(workspaceProcessBody(t, model.ProcessExecutionChangeBearing))
	executor := &gitNativeChildExecutor{}
	coordinator := &connectedActorCoordinator{sessionClone: canonicalSessionClone, base: base, backend: backend, executor: executor}
	factory := &connectedActorFactory{coordinator: coordinator}
	coordinator.factory = factory
	sandbox := &connectedActorSandbox{t: t}
	coordinator.sandbox = sandbox
	dispatcher := jobs.Dispatcher{
		Store: store, Repositories: testkit.RepoResolver{}, Workspaces: &testkit.Workspaces{Binding: binding},
		Sandbox: sandbox, Acpx: factory, Writeback: &testkit.Writeback{Store: store}, Clock: testkit.Clock{Time: testkit.Now},
		PublicSessionID:   func() (string, error) { return "ps-native-actor", nil },
		TurnCorrelationID: func() (string, error) { return "turn-token-native-actor", nil },
		IssueSpecBinary:   "issue-spec",
	}

	result, err := dispatcher.RunNext(t.Context())
	if err != nil {
		t.Fatalf("connected runner lifecycle: %v", err)
	}
	if !result.Executed || result.Status != state.StatusCompleted {
		t.Fatalf("connected runner result=%+v", result)
	}
	if len(sandbox.requests) != 1 || sandbox.requests[0].WorkspacePath != canonicalSessionClone || sandbox.requests[0].AcpxWorkingDirectory != canonicalSessionClone {
		t.Fatalf("runner sandbox did not keep coordinator in session clone: %+v", sandbox.requests)
	}
	if factory.calls != 1 || len(factory.environments) != 1 || factory.environments[0].WorkingDirectory != canonicalSessionClone {
		t.Fatalf("runner ACPX factory calls=%d environments=%+v", factory.calls, factory.environments)
	}
	if coordinator.newCalls != 1 || coordinator.resumeCalls != 0 || coordinator.factoryCallsBeforeChild != 1 || coordinator.factoryCallsAfterChild != 1 {
		t.Fatalf("runner created nested ACPX dispatch: coordinator=%+v factory calls=%d", coordinator, factory.calls)
	}
	if executor.calls != 1 || len(executor.workingDirectories) != 1 || executor.workingDirectories[0] != coordinator.prepared.WorktreePath {
		t.Fatalf("native child executor calls=%d cwd=%v prepared=%q", executor.calls, executor.workingDirectories, coordinator.prepared.WorktreePath)
	}
	if coordinator.handoff.ResultCommit == "" || !coordinator.handoff.Test.Passed || coordinator.handoff.Test.Command != "git diff --cached --check" ||
		!strings.Contains(coordinator.handoff.Summary, coordinator.handoff.ResultCommit) || !strings.Contains(coordinator.handoff.Summary, coordinator.handoff.Test.Command) {
		t.Fatalf("native child structured handoff was not derived from execution: %+v", coordinator.handoff)
	}
	if got := workspaceGitOutput(t, canonicalSessionClone, "rev-parse", coordinator.handoff.ResultCommit+"^"); got != base {
		t.Fatalf("native child commit parent=%s want=%s", got, base)
	}
	if got := workspaceGitOutput(t, canonicalSessionClone, "rev-list", "--count", base+".."+coordinator.handoff.ResultCommit); got != "1" {
		t.Fatalf("native child commit count=%s want exactly one", got)
	}
	if coordinator.headBeforeChild != base || coordinator.headAfterChild != base || coordinator.headAfterComplete != base ||
		coordinator.headAfterIntegrate != coordinator.integrated.IntegrationSHA || coordinator.headAfterCleanup != coordinator.integrated.IntegrationSHA {
		t.Fatalf("session clone changed outside integration: before=%s after-child=%s after-complete=%s after-integrate=%s after-cleanup=%s result=%s",
			coordinator.headBeforeChild, coordinator.headAfterChild, coordinator.headAfterComplete, coordinator.headAfterIntegrate, coordinator.headAfterCleanup, coordinator.integrated.IntegrationSHA)
	}
	if _, err := os.Stat(coordinator.prepared.WorktreePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("coordinator cleanup left native child worktree: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(canonicalSessionClone, "internal", "commands", "native-actor.txt")); err != nil || string(data) != "native child result\n" {
		t.Fatalf("integrated native child result data=%q err=%v", data, err)
	}
	job := store.Snapshot().Jobs["job-native-actor"]
	if job.Acpx.CWD != canonicalSessionClone {
		t.Fatalf("persisted ACPX cwd=%q want session clone %q", job.Acpx.CWD, canonicalSessionClone)
	}
}

// TestWorkspacePrepareOverlappingOwnershipIsAdvisory proves the end-to-end CLI
// semantic: two real workspaces from different PROCESSes with overlapping
// declared write scope both prepare successfully, the second one carrying an
// advisory naming the first, and inspect reports the symmetric advisory.
func TestWorkspacePrepareOverlappingOwnershipIsAdvisory(t *testing.T) {
	skipWithoutRealGit(t)
	repo, base := workspaceGitRepository(t)
	workspaceRoot := filepath.Join(t.TempDir(), "managed")

	firstBackend := newWorkspaceCASBackend(workspaceProcessBodyFor(t, "PROCESS-101", model.ProcessExecutionChangeBearing, []string{"internal/shared/**"}))
	first, firstOut, firstErrOut := transitionAppWithError(firstBackend)
	firstCode := first.runWorkflowWorkspace(context.Background(), []string{"prepare", "--repo", "o/r", "--issue", "177", "--process", "PROCESS-101",
		"--integration-root", repo, "--workspace-root", workspaceRoot, "--owner-token", "owner-a", "--base", base, "--json"})
	if firstCode != 0 {
		t.Fatalf("first prepare code=%d out=%s err=%s", firstCode, firstOut.String(), firstErrOut.String())
	}
	firstResult := decodeWorkspaceResult(t, firstOut)
	if !firstResult.OK || len(firstResult.OwnershipAdvisories) != 0 {
		t.Fatalf("first prepare result=%+v", firstResult)
	}

	secondBackend := newWorkspaceCASBackend(workspaceProcessBodyFor(t, "PROCESS-102", model.ProcessExecutionChangeBearing, []string{"internal/shared/file.go"}))
	second, secondOut, secondErrOut := transitionAppWithError(secondBackend)
	secondCode := second.runWorkflowWorkspace(context.Background(), []string{"prepare", "--repo", "o/r", "--issue", "178", "--process", "PROCESS-102",
		"--integration-root", repo, "--workspace-root", workspaceRoot, "--owner-token", "owner-b", "--base", base, "--json"})
	if secondCode != 0 {
		t.Fatalf("overlapping prepare must stay successful, code=%d out=%s err=%s", secondCode, secondOut.String(), secondErrOut.String())
	}
	secondResult := decodeWorkspaceResult(t, secondOut)
	if !secondResult.OK || secondResult.Code != "" || secondResult.State != processworkspace.StatePrepared {
		t.Fatalf("overlapping prepare result=%+v", secondResult)
	}
	want := []processworkspace.OverlapAdvisory{{WorkspaceID: "ws-process-101", ProcessID: "PROCESS-101",
		Overlaps: []processworkspace.OverlapAdvisoryEntry{{Entry: "internal/shared/file.go", PeerEntry: "internal/shared/**"}}}}
	if !reflect.DeepEqual(secondResult.OwnershipAdvisories, want) {
		t.Fatalf("prepare advisories=%+v want %+v", secondResult.OwnershipAdvisories, want)
	}

	inspectBackend := newWorkspaceCASBackend(workspaceProcessBodyFor(t, "PROCESS-101", model.ProcessExecutionChangeBearing, []string{"internal/shared/**"}))
	inspect, inspectOut, inspectErrOut := transitionAppWithError(inspectBackend)
	inspectCode := inspect.runWorkflowWorkspace(context.Background(), []string{"inspect", "--repo", "o/r", "--issue", "177", "--process", "PROCESS-101",
		"--integration-root", repo, "--workspace-root", workspaceRoot})
	if inspectCode != 0 {
		t.Fatalf("inspect code=%d out=%s err=%s", inspectCode, inspectOut.String(), inspectErrOut.String())
	}
	output := inspectOut.String()
	if !strings.Contains(output, "ownership advisory: workspace ws-process-102 (process PROCESS-102) declares overlapping write scope (internal/shared/** vs internal/shared/file.go)") {
		t.Fatalf("inspect output missing advisory line: %s", output)
	}
	if !strings.Contains(output, "may require merge-conflict resolution at integration time") {
		t.Fatalf("inspect advisory line omits the integration-time consequence: %s", output)
	}
}

type connectedActorSandbox struct {
	t        *testing.T
	requests []jobs.SandboxRequest
}

func (s *connectedActorSandbox) Prepare(_ context.Context, request jobs.SandboxRequest) (jobs.ExecutionEnvironment, error) {
	s.requests = append(s.requests, request)
	for _, name := range []string{runnerworkspace.ProcessIntegrationRootEnv, runnerworkspace.ProcessWorkspaceRootEnv} {
		value := request.ExtraEnv[name]
		if value == "" {
			return jobs.ExecutionEnvironment{}, fmt.Errorf("runner-owned %s is empty", name)
		}
		s.t.Setenv(name, value)
	}
	return jobs.ExecutionEnvironment{WorkingDirectory: request.AcpxWorkingDirectory,
		Sandbox: state.SandboxMetadata{UnsafeNoSandbox: true, SandboxProvider: "none", FSBoundary: "disabled"}, Runner: connectedActorAuthRunner{}}, nil
}

type connectedActorAuthRunner struct{}

func (connectedActorAuthRunner) Run(context.Context, acpx.Command) (acpx.CommandResult, error) {
	return acpx.CommandResult{Stdout: []byte(`{"ok":true,"auth":{"host":"github.com","source":"gh","user":"bot"},"backend":{"name":"gh","selection_source":"auto:gh"}}`)}, nil
}

type connectedActorFactory struct {
	coordinator  *connectedActorCoordinator
	calls        int
	environments []jobs.ExecutionEnvironment
}

func (f *connectedActorFactory) NewCoordinator(environment jobs.ExecutionEnvironment) (jobs.Coordinator, error) {
	f.calls++
	f.environments = append(f.environments, environment)
	if f.calls != 1 {
		return nil, fmt.Errorf("nested ACPX coordinator factory call %d", f.calls)
	}
	f.coordinator.environment = environment
	return f.coordinator, nil
}

type connectedActorCoordinator struct {
	sessionClone            string
	base                    string
	backend                 *workspaceCASBackend
	executor                nativeChildExecutor
	factory                 *connectedActorFactory
	sandbox                 *connectedActorSandbox
	environment             jobs.ExecutionEnvironment
	newCalls                int
	resumeCalls             int
	factoryCallsBeforeChild int
	factoryCallsAfterChild  int
	prepared                workspaceCommandResult
	integrated              workspaceCommandResult
	handoff                 nativeChildHandoff
	headBeforeChild         string
	headAfterChild          string
	headAfterComplete       string
	headAfterIntegrate      string
	headAfterCleanup        string
}

func (c *connectedActorCoordinator) NewSession(ctx context.Context, _ acpx.NewSessionRequest) (acpx.DispatchResult, error) {
	c.newCalls++
	if c.sandbox == nil || len(c.sandbox.requests) != 1 || c.sandbox.requests[0].WorkspacePath != c.sessionClone ||
		c.sandbox.requests[0].AcpxWorkingDirectory != c.sessionClone {
		return acpx.DispatchResult{}, fmt.Errorf("coordinator observed invalid sandbox request: %+v", c.sandbox)
	}
	if c.environment.WorkingDirectory != c.sessionClone {
		return acpx.DispatchResult{}, fmt.Errorf("ACPX cwd=%q want session clone %q", c.environment.WorkingDirectory, c.sessionClone)
	}
	if err := c.runLifecycle(ctx); err != nil {
		return acpx.DispatchResult{}, err
	}
	result := testkit.DispatchResult("ps-native-actor", "rec-native-actor", "turn-native-actor")
	result.Metadata.CWD = c.environment.WorkingDirectory
	return result, nil
}

func (c *connectedActorCoordinator) Resume(context.Context, acpx.ResumeRequest) (acpx.DispatchResult, error) {
	c.resumeCalls++
	return acpx.DispatchResult{}, errors.New("unexpected nested/resume ACPX dispatch")
}

func (c *connectedActorCoordinator) runLifecycle(ctx context.Context) error {
	owner := "native-actor-owner"
	baseArgs := []string{"--repo", "o/r", "--issue", "177", "--process", "PROCESS-004", "--owner-token", owner}
	prepared, err := runConnectedWorkspaceCommand(ctx, c.backend, append(append([]string{"prepare"}, baseArgs...), "--base", c.base, "--json"))
	if err != nil {
		return err
	}
	c.prepared = prepared
	processRoot := os.Getenv(runnerworkspace.ProcessWorkspaceRootEnv)
	if !connectedPathWithinRoot(prepared.WorktreePath, processRoot) || prepared.WorktreePath == c.sessionClone {
		return fmt.Errorf("native child worktree %q is outside session pool %q", prepared.WorktreePath, processRoot)
	}
	c.headBeforeChild, err = connectedGitOutput(ctx, c.sessionClone, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	c.factoryCallsBeforeChild = c.factory.calls
	c.handoff, err = c.executor.Execute(ctx, nativeChildRequest{WorkingDirectory: prepared.WorktreePath, BaseCommit: c.base})
	if err != nil {
		return err
	}
	c.factoryCallsAfterChild = c.factory.calls
	c.headAfterChild, err = connectedGitOutput(ctx, c.sessionClone, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	completed, err := runConnectedWorkspaceCommand(ctx, c.backend, append(append([]string{"complete"}, baseArgs...), "--result-commit", c.handoff.ResultCommit, "--json"))
	if err != nil {
		return err
	}
	if completed.State != processworkspace.StateWorkerComplete || completed.ResultCommit != c.handoff.ResultCommit {
		return fmt.Errorf("complete rejected native handoff: %+v", completed)
	}
	c.headAfterComplete, err = connectedGitOutput(ctx, c.sessionClone, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	c.integrated, err = runConnectedWorkspaceCommand(ctx, c.backend, append(append([]string{"integrate"}, baseArgs...), "--expected-head", c.base, "--json"))
	if err != nil {
		return err
	}
	c.headAfterIntegrate, err = connectedGitOutput(ctx, c.sessionClone, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	cleaned, err := runConnectedWorkspaceCommand(ctx, c.backend, append(append([]string{"cleanup"}, baseArgs...), "--json"))
	if err != nil {
		return err
	}
	if cleaned.State != processworkspace.StateCleaned {
		return fmt.Errorf("cleanup state=%s", cleaned.State)
	}
	c.headAfterCleanup, err = connectedGitOutput(ctx, c.sessionClone, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	return nil
}

func runConnectedWorkspaceCommand(ctx context.Context, backend *workspaceCASBackend, args []string) (workspaceCommandResult, error) {
	app, out, errOut := transitionAppWithError(backend)
	if code := app.runWorkflowWorkspace(ctx, args); code != 0 {
		return workspaceCommandResult{}, fmt.Errorf("workspace %s code=%d out=%s err=%s", args[0], code, out.String(), errOut.String())
	}
	var result workspaceCommandResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		return result, fmt.Errorf("decode workspace %s: %w: %s", args[0], err, out.String())
	}
	return result, nil
}

type nativeChildExecutor interface {
	Execute(context.Context, nativeChildRequest) (nativeChildHandoff, error)
}

type nativeChildRequest struct {
	WorkingDirectory string
	BaseCommit       string
}

type nativeChildTestResult struct {
	Command string
	Passed  bool
	Output  string
}

type nativeChildHandoff struct {
	ResultCommit string
	Test         nativeChildTestResult
	Summary      string
}

type gitNativeChildExecutor struct {
	calls              int
	workingDirectories []string
}

func (e *gitNativeChildExecutor) Execute(ctx context.Context, request nativeChildRequest) (nativeChildHandoff, error) {
	e.calls++
	e.workingDirectories = append(e.workingDirectories, request.WorkingDirectory)
	resultPath := filepath.Join(request.WorkingDirectory, "internal", "commands", "native-actor.txt")
	if err := os.MkdirAll(filepath.Dir(resultPath), 0o700); err != nil {
		return nativeChildHandoff{}, err
	}
	if err := os.WriteFile(resultPath, []byte("native child result\n"), 0o600); err != nil {
		return nativeChildHandoff{}, err
	}
	if _, err := connectedGitOutput(ctx, request.WorkingDirectory, "add", "internal/commands/native-actor.txt"); err != nil {
		return nativeChildHandoff{}, err
	}
	testResult := nativeChildTestResult{Command: "git diff --cached --check"}
	output, err := connectedGitOutput(ctx, request.WorkingDirectory, "diff", "--cached", "--check")
	testResult.Output = output
	testResult.Passed = err == nil
	if err != nil {
		return nativeChildHandoff{Test: testResult}, err
	}
	if _, err := connectedGitOutput(ctx, request.WorkingDirectory, "commit", "-s", "-m", "test: native process child handoff"); err != nil {
		return nativeChildHandoff{Test: testResult}, err
	}
	commit, err := connectedGitOutput(ctx, request.WorkingDirectory, "rev-parse", "HEAD")
	if err != nil {
		return nativeChildHandoff{Test: testResult}, err
	}
	count, err := connectedGitOutput(ctx, request.WorkingDirectory, "rev-list", "--count", request.BaseCommit+".."+commit)
	if err != nil || count != "1" {
		return nativeChildHandoff{ResultCommit: commit, Test: testResult}, fmt.Errorf("native child commit count=%q err=%v", count, err)
	}
	return nativeChildHandoff{ResultCommit: commit, Test: testResult,
		Summary: fmt.Sprintf("native child commit %s ready after %s passed", commit, testResult.Command)}, nil
}

func connectedGitOutput(ctx context.Context, directory string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = directory
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return strings.TrimSpace(stdout.String()), fmt.Errorf("git %s in %s: %w: %s", strings.Join(args, " "), directory, err, stderr.String())
	}
	return strings.TrimSpace(stdout.String()), nil
}

func connectedPathWithinRoot(path, root string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}
