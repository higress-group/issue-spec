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

func TestCompleteRejectsScopeSharedSignoffDirtyAndCommitShape(t *testing.T) {
	tests := []struct {
		name      string
		ownership []string
		shared    []string
		prepare   func(*testing.T, integrationFixture) string
		want      error
	}{
		{name: "scope escape", ownership: []string{"internal/**"}, prepare: func(t *testing.T, f integrationFixture) string {
			return commitWorkerFile(t, f, "README.md", "outside\n", true)
		}, want: ErrOwnershipViolation},
		{name: "shared touchpoint is not permission", ownership: []string{"internal/**"}, shared: []string{"go.mod"}, prepare: func(t *testing.T, f integrationFixture) string {
			return commitWorkerFile(t, f, "go.mod", "module changed\n", true)
		}, want: ErrOwnershipViolation},
		{name: "missing signoff", ownership: []string{"internal/**"}, prepare: func(t *testing.T, f integrationFixture) string {
			return commitWorkerFile(t, f, "internal/x.go", "package x\n", false)
		}, want: ErrInvalidWorkerResult},
		{name: "dirty worktree", ownership: []string{"internal/**"}, prepare: func(t *testing.T, f integrationFixture) string {
			result := commitWorkerFile(t, f, "internal/x.go", "package x\n", true)
			if err := os.WriteFile(filepath.Join(f.worktree, "dirty.txt"), []byte("dirty"), 0o600); err != nil {
				t.Fatal(err)
			}
			return result
		}, want: ErrInvalidWorkerResult},
		{name: "multiple commits", ownership: []string{"internal/**"}, prepare: func(t *testing.T, f integrationFixture) string {
			commitWorkerFile(t, f, "internal/a.go", "package internal\n", true)
			return commitWorkerFile(t, f, "internal/b.go", "package internal\n", true)
		}, want: ErrInvalidWorkerResult},
		{name: "empty commit", ownership: []string{"internal/**"}, prepare: func(t *testing.T, f integrationFixture) string {
			runGit(t, f.worktree, "commit", "--allow-empty", "-s", "-m", "empty")
			return gitOutput(t, f.worktree, "rev-parse", "HEAD")
		}, want: ErrInvalidWorkerResult},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newIntegrationFixture(t, test.ownership, test.shared)
			result := test.prepare(t, fixture)
			_, err := fixture.manager.Complete(context.Background(), CompleteRequest{WorkspaceID: fixture.lease.Portable.WorkspaceID, OwnerToken: fixture.lease.Owner.Token, ResultCommit: result})
			if !errors.Is(err, test.want) {
				t.Fatalf("Complete err=%v want %v", err, test.want)
			}
			stored, _, getErr := fixture.manager.Store.Get(context.Background(), fixture.lease.Portable.WorkspaceID)
			if getErr != nil || stored.Portable.State != StatePrepared || stored.Portable.ResultCommit != "" {
				t.Fatalf("failed completion changed lease: %+v err=%v", stored, getErr)
			}
		})
	}
}

func TestCompleteBareDescendantDiagnosticFailsClosed(t *testing.T) {
	fixture := newIntegrationFixture(t, []string{"internal"}, nil)
	result := commitWorkerFile(t, fixture, "internal/child.go", "package internal\n", true)
	_, err := fixture.manager.Complete(context.Background(), CompleteRequest{
		WorkspaceID: fixture.lease.Portable.WorkspaceID, OwnerToken: fixture.lease.Owner.Token, ResultCommit: result,
	})
	if !errors.Is(err, ErrOwnershipViolation) ||
		!strings.Contains(err.Error(), `descendants of bare declaration "internal"`) ||
		!strings.Contains(err.Error(), `declare "internal/**"`) ||
		!strings.Contains(err.Error(), "internal/child.go") {
		t.Fatalf("Complete descendant diagnostic=%v", err)
	}
	stored, found, getErr := fixture.manager.Store.Get(context.Background(), fixture.lease.Portable.WorkspaceID)
	if getErr != nil || !found || stored.Portable.State != StatePrepared || stored.Portable.ResultCommit != "" || stored.Portable.IntegrationSHA != "" {
		t.Fatalf("failed completion mutated lease: %+v found=%v err=%v", stored.Portable, found, getErr)
	}
	if got := gitOutput(t, fixture.repo, "rev-parse", "HEAD"); got != fixture.base {
		t.Fatalf("failed completion integrated commit: HEAD=%s base=%s", got, fixture.base)
	}
}

func TestCompleteAndIntegrateFailEarlyForLegacyOwnershipGrammar(t *testing.T) {
	for _, ownership := range [][]string{{"internal/?.go"}, {"internal/[ab].go"}} {
		t.Run(strings.ReplaceAll(ownership[0], "/", "_"), func(t *testing.T) {
			fixture := newIntegrationFixture(t, ownership, nil)
			_, err := fixture.manager.Complete(context.Background(), CompleteRequest{
				WorkspaceID: fixture.lease.Portable.WorkspaceID, OwnerToken: fixture.lease.Owner.Token, ResultCommit: "not-a-commit",
			})
			if !errors.Is(err, ErrInvalidWorkerResult) || !strings.Contains(err.Error(), "ownership") {
				t.Fatalf("Complete did not reject ownership before Git result validation: %v", err)
			}
			_, err = fixture.manager.Store.Update(context.Background(), fixture.lease.Portable.WorkspaceID, func(current *LocalLease) error {
				current.Portable.State = StateWorkerComplete
				current.Portable.ResultCommit = fixture.base
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = fixture.manager.Integrate(context.Background(), IntegrateRequest{
				WorkspaceID: fixture.lease.Portable.WorkspaceID, OwnerToken: fixture.lease.Owner.Token, ExpectedHead: fixture.base,
			})
			if !errors.Is(err, ErrWorkspaceConflict) || !strings.Contains(err.Error(), "invalid managed ownership") {
				t.Fatalf("Integrate did not reject legacy ownership before Git mutation: %v", err)
			}
		})
	}
}

func TestIntegrateSuccessRetryAndSafeCleanupPreserveUserWorktree(t *testing.T) {
	fixture := newIntegrationFixture(t, []string{"internal/**"}, nil)
	resultCommit := commitWorkerFile(t, fixture, "internal/x.go", "package internal\n", true)
	completed, err := fixture.manager.Complete(context.Background(), CompleteRequest{WorkspaceID: fixture.lease.Portable.WorkspaceID, OwnerToken: fixture.lease.Owner.Token, ResultCommit: resultCommit})
	if err != nil || completed.Lease.Portable.State != StateWorkerComplete {
		t.Fatalf("Complete=%+v err=%v", completed, err)
	}
	userPath := filepath.Join(t.TempDir(), "user-worktree")
	runGit(t, fixture.repo, "worktree", "add", "-b", "user-preserved", "--", userPath, fixture.base)

	integrated, err := fixture.manager.Integrate(context.Background(), IntegrateRequest{WorkspaceID: fixture.lease.Portable.WorkspaceID, OwnerToken: fixture.lease.Owner.Token, ExpectedHead: fixture.base})
	if err != nil || integrated.AlreadyIntegrated || integrated.Lease.Portable.State != StateIntegrated || integrated.IntegrationSHA == "" {
		t.Fatalf("Integrate=%+v err=%v", integrated, err)
	}
	if integrated.Lease.Portable.ResultCommit != resultCommit {
		t.Fatalf("integrate lost worker result: %+v", integrated.Lease.Portable)
	}
	retry, err := fixture.manager.Integrate(context.Background(), IntegrateRequest{WorkspaceID: fixture.lease.Portable.WorkspaceID, OwnerToken: fixture.lease.Owner.Token, ExpectedHead: fixture.base})
	if err != nil || !retry.AlreadyIntegrated || retry.IntegrationSHA != integrated.IntegrationSHA {
		t.Fatalf("retry=%+v err=%v", retry, err)
	}
	if got := gitOutput(t, fixture.repo, "rev-list", "--count", fixture.base+"..HEAD"); got != "1" {
		t.Fatalf("retry duplicated integration commit: %s", got)
	}
	cleaned, err := fixture.manager.Cleanup(context.Background(), fixture.lease.Portable.WorkspaceID, fixture.lease.Owner.Token)
	if err != nil || cleaned.Lease.Portable.State != StateCleaned ||
		cleaned.Lease.Portable.ResultCommit != resultCommit || cleaned.Lease.Portable.IntegrationSHA != integrated.IntegrationSHA {
		t.Fatalf("cleanup=%+v err=%v", cleaned, err)
	}
	stored, found, err := fixture.manager.Store.Get(context.Background(), fixture.lease.Portable.WorkspaceID)
	if err != nil || !found || stored.Portable.State != StateCleaned ||
		stored.Portable.ResultCommit != resultCommit || stored.Portable.IntegrationSHA != integrated.IntegrationSHA {
		t.Fatalf("stored cleanup evidence=%+v found=%v err=%v", stored.Portable, found, err)
	}
	if _, err := os.Stat(userPath); err != nil {
		t.Fatalf("user worktree was touched: %v", err)
	}
	if got := gitOutput(t, fixture.repo, "rev-parse", "refs/heads/"+fixture.lease.Portable.Branch); got != resultCommit {
		t.Fatalf("cleanup changed worker branch: %s", got)
	}
	markers := gitOutput(t, fixture.repo, "for-each-ref", "--format=%(refname)", "refs/issue-spec/process-workspaces/"+fixture.lease.Portable.WorkspaceID+"/")
	if markers != "" {
		t.Fatalf("cleanup left owned marker %q", markers)
	}
	if attempts := integrationAttemptRefs(t, fixture); attempts != "" {
		t.Fatalf("successful integration left attempt marker %q", attempts)
	}
}

func TestIntegratedRetryRejectsDifferentExpectedHeadBeforeAlreadyIntegrated(t *testing.T) {
	fixture := completedFixture(t, "internal/x.go", "package internal\n")
	integrated, err := fixture.manager.Integrate(context.Background(), IntegrateRequest{
		WorkspaceID: fixture.lease.Portable.WorkspaceID, OwnerToken: fixture.lease.Owner.Token, ExpectedHead: fixture.base,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = fixture.manager.Integrate(context.Background(), IntegrateRequest{
		WorkspaceID: fixture.lease.Portable.WorkspaceID, OwnerToken: fixture.lease.Owner.Token, ExpectedHead: integrated.IntegrationSHA,
	})
	if !errors.Is(err, ErrStaleIntegration) || !strings.Contains(err.Error(), "recorded integration") {
		t.Fatalf("integrated retry accepted different expected HEAD: %v", err)
	}
}

func TestCompleteRejectsMergeUnreachableAndResultReplacement(t *testing.T) {
	t.Run("merge commit", func(t *testing.T) {
		fixture := newIntegrationFixture(t, []string{"internal/**"}, nil)
		runGit(t, fixture.worktree, "checkout", "-b", "worker-side", fixture.base)
		commitWorkerFile(t, fixture, "internal/side.go", "package internal\n", true)
		runGit(t, fixture.worktree, "checkout", fixture.lease.Portable.Branch)
		commitWorkerFile(t, fixture, "internal/main.go", "package internal\n", true)
		runGit(t, fixture.worktree, "merge", "--no-ff", "worker-side", "-m", "merge worker commits", "--signoff")
		result := gitOutput(t, fixture.worktree, "rev-parse", "HEAD")
		_, err := fixture.manager.Complete(context.Background(), CompleteRequest{WorkspaceID: fixture.lease.Portable.WorkspaceID, OwnerToken: fixture.lease.Owner.Token, ResultCommit: result})
		if !errors.Is(err, ErrInvalidWorkerResult) {
			t.Fatalf("merge result accepted: %v", err)
		}
	})

	t.Run("unreachable object id", func(t *testing.T) {
		fixture := newIntegrationFixture(t, []string{"internal/**"}, nil)
		_, err := fixture.manager.Complete(context.Background(), CompleteRequest{WorkspaceID: fixture.lease.Portable.WorkspaceID, OwnerToken: fixture.lease.Owner.Token, ResultCommit: strings.Repeat("f", 40)})
		if !errors.Is(err, ErrInvalidWorkerResult) {
			t.Fatalf("unreachable result accepted: %v", err)
		}
	})

	t.Run("replace durable result", func(t *testing.T) {
		fixture := completedFixture(t, "internal/x.go", "package internal\n")
		_, err := fixture.manager.Complete(context.Background(), CompleteRequest{WorkspaceID: fixture.lease.Portable.WorkspaceID, OwnerToken: fixture.lease.Owner.Token, ResultCommit: strings.Repeat("e", 40)})
		if !errors.Is(err, ErrInvalidWorkerResult) || !strings.Contains(err.Error(), "cannot be replaced") {
			t.Fatalf("result replacement accepted: %v", err)
		}
	})
}

func TestIntegrateRejectsStaleHeadWithoutMutatingLease(t *testing.T) {
	fixture := completedFixture(t, "internal/x.go", "package internal\n")
	runGit(t, fixture.repo, "commit", "--allow-empty", "-m", "advance integration")
	advanced := gitOutput(t, fixture.repo, "rev-parse", "HEAD")
	_, err := fixture.manager.Integrate(context.Background(), IntegrateRequest{WorkspaceID: fixture.lease.Portable.WorkspaceID, OwnerToken: fixture.lease.Owner.Token, ExpectedHead: fixture.base})
	if !errors.Is(err, ErrStaleIntegration) {
		t.Fatalf("stale HEAD accepted: %v", err)
	}
	if got := gitOutput(t, fixture.repo, "rev-parse", "HEAD"); got != advanced {
		t.Fatalf("stale integration changed HEAD: %s", got)
	}
	stored, _, _ := fixture.manager.Store.Get(context.Background(), fixture.lease.Portable.WorkspaceID)
	if stored.Portable.State != StateWorkerComplete || stored.Portable.IntegrationSHA != "" {
		t.Fatalf("stale integration changed lease: %+v", stored)
	}
}

func TestIntegratePreMutationDriftPreservesExternalHeadAndWorkerResult(t *testing.T) {
	fixture := completedFixture(t, "internal/pre-race.go", "package internal\n")
	var externalHead string
	ctx := withIntegrationRaceHook(context.Background(), func(phase string) error {
		if phase != integrationHookBeforeMutation {
			return nil
		}
		runGit(t, fixture.repo, "commit", "--allow-empty", "-m", "external pre-mutation advance")
		externalHead = gitOutput(t, fixture.repo, "rev-parse", "HEAD")
		return nil
	})
	result, err := fixture.manager.Integrate(ctx, IntegrateRequest{WorkspaceID: fixture.lease.Portable.WorkspaceID, OwnerToken: fixture.lease.Owner.Token, ExpectedHead: fixture.base})
	if !errors.Is(err, ErrStaleIntegration) || result.Lease.Portable.State != StateConflicted {
		t.Fatalf("pre-mutation drift result=%+v err=%v", result, err)
	}
	if got := gitOutput(t, fixture.repo, "rev-parse", "HEAD"); got != externalHead {
		t.Fatalf("external HEAD changed: got %s want %s", got, externalHead)
	}
	if status := gitOutput(t, fixture.repo, "status", "--porcelain=v1"); status != "" {
		t.Fatalf("pre-mutation drift left dirty checkout: %q", status)
	}
	stored, _, _ := fixture.manager.Store.Get(context.Background(), fixture.lease.Portable.WorkspaceID)
	if stored.Portable.State != StateConflicted || stored.Portable.ResultCommit == "" || stored.Portable.IntegrationSHA != "" || integrationAttemptRefs(t, fixture) != "" {
		t.Fatalf("pre-mutation recovery lost retry evidence: %+v markers=%q", stored, integrationAttemptRefs(t, fixture))
	}
}

func TestIntegratePostMutationDriftPreservesExactExternalCommitAndUserWork(t *testing.T) {
	fixture := completedFixture(t, "internal/post-race.go", "package internal\n")
	externalWorktree := filepath.Join(t.TempDir(), "external-worktree")
	runGit(t, fixture.repo, "worktree", "add", "-b", "external-p026", externalWorktree, fixture.base)
	if err := os.WriteFile(filepath.Join(externalWorktree, "external-user.txt"), []byte("preserve me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, externalWorktree, "add", "external-user.txt")
	runGit(t, externalWorktree, "commit", "-m", "external user work")
	externalHead := gitOutput(t, externalWorktree, "rev-parse", "HEAD")
	runGit(t, fixture.repo, "worktree", "remove", externalWorktree)
	runGit(t, fixture.repo, "branch", "-D", "external-p026")
	var coordinatorHead string
	ctx := withIntegrationRaceHook(context.Background(), func(phase string) error {
		if phase != integrationHookAfterMutation {
			return nil
		}
		coordinatorHead = gitOutput(t, fixture.repo, "rev-parse", "HEAD")
		runGit(t, fixture.repo, "reset", "--hard", externalHead)
		return nil
	})
	result, err := fixture.manager.Integrate(ctx, IntegrateRequest{WorkspaceID: fixture.lease.Portable.WorkspaceID, OwnerToken: fixture.lease.Owner.Token, ExpectedHead: fixture.base})
	if !errors.Is(err, ErrStaleIntegration) || result.Lease.Portable.State != StateConflicted {
		t.Fatalf("post-mutation drift result=%+v err=%v", result, err)
	}
	if got := gitOutput(t, fixture.repo, "rev-parse", "HEAD"); got != externalHead || coordinatorHead == "" || coordinatorHead == externalHead {
		t.Fatalf("post-mutation HEAD got=%s external=%s coordinator=%s", got, externalHead, coordinatorHead)
	}
	if content, readErr := os.ReadFile(filepath.Join(fixture.repo, "external-user.txt")); readErr != nil || string(content) != "preserve me\n" {
		t.Fatalf("external user work was not preserved: content=%q err=%v", content, readErr)
	}
	if status := gitOutput(t, fixture.repo, "status", "--porcelain=v1"); status != "" {
		t.Fatalf("post-mutation drift left dirty checkout: %q", status)
	}
	containsCoordinator, predicateErr := fixture.manager.gitPredicate(context.Background(), fixture.repo, "merge-base", "--is-ancestor", coordinatorHead, externalHead)
	if predicateErr != nil || containsCoordinator {
		t.Fatalf("external HEAD retained coordinator commit: contains=%v err=%v", containsCoordinator, predicateErr)
	}
	stored, _, _ := fixture.manager.Store.Get(context.Background(), fixture.lease.Portable.WorkspaceID)
	if stored.Portable.State != StateConflicted || stored.Portable.ResultCommit == "" || stored.Portable.IntegrationSHA != "" || integrationAttemptRefs(t, fixture) != "" {
		t.Fatalf("post-mutation recovery lost retry evidence: %+v markers=%q", stored, integrationAttemptRefs(t, fixture))
	}
}

func TestIntegrateConcurrentDriftDuringCherryPickRemovesOnlyCoordinatorCommit(t *testing.T) {
	fixture := completedFixture(t, "internal/concurrent-race.go", "package internal\n")
	blocker := &blockingCherryPickRunner{GitRunner: ExecGitRunner{}, entered: make(chan struct{}), release: make(chan struct{})}
	fixture.manager.Runner = blocker
	type outcome struct {
		result IntegrationResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := fixture.manager.Integrate(context.Background(), IntegrateRequest{WorkspaceID: fixture.lease.Portable.WorkspaceID, OwnerToken: fixture.lease.Owner.Token, ExpectedHead: fixture.base})
		done <- outcome{result: result, err: err}
	}()
	select {
	case <-blocker.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for cherry-pick boundary")
	}
	runGit(t, fixture.repo, "commit", "--allow-empty", "-m", "external concurrent advance")
	externalHead := gitOutput(t, fixture.repo, "rev-parse", "HEAD")
	close(blocker.release)
	var got outcome
	select {
	case got = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for integration recovery")
	}
	if !errors.Is(got.err, ErrStaleIntegration) || got.result.Lease.Portable.State != StateConflicted {
		t.Fatalf("concurrent drift result=%+v err=%v", got.result, got.err)
	}
	if head := gitOutput(t, fixture.repo, "rev-parse", "HEAD"); head != externalHead {
		t.Fatalf("coordinator rollback changed external HEAD: got %s want %s", head, externalHead)
	}
	if status := gitOutput(t, fixture.repo, "status", "--porcelain=v1"); status != "" {
		t.Fatalf("coordinator rollback left dirty checkout: %q", status)
	}
	stored, _, _ := fixture.manager.Store.Get(context.Background(), fixture.lease.Portable.WorkspaceID)
	if stored.Portable.State != StateConflicted || stored.Portable.ResultCommit == "" || stored.Portable.IntegrationSHA != "" {
		t.Fatalf("concurrent drift lost worker result: %+v", stored)
	}
}

func TestIntegrateOntoAdvancedNonConflictingExpectedHead(t *testing.T) {
	fixture := completedFixture(t, "internal/x.go", "package internal\n")
	if err := os.WriteFile(filepath.Join(fixture.repo, "coordinator.txt"), []byte("dependency\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, fixture.repo, "add", "coordinator.txt")
	runGit(t, fixture.repo, "commit", "-m", "integrate dependency first")
	expected := gitOutput(t, fixture.repo, "rev-parse", "HEAD")
	result, err := fixture.manager.Integrate(context.Background(), IntegrateRequest{WorkspaceID: fixture.lease.Portable.WorkspaceID, OwnerToken: fixture.lease.Owner.Token, ExpectedHead: expected})
	if err != nil || result.Lease.Portable.State != StateIntegrated {
		t.Fatalf("advanced integration=%+v err=%v", result, err)
	}
	if parent := gitOutput(t, fixture.repo, "rev-parse", "HEAD^"); parent != expected {
		t.Fatalf("worker commit parent=%s want expected HEAD %s", parent, expected)
	}
}

func TestIntegrateConflictAbortsAndRestoresIntegration(t *testing.T) {
	fixture := newIntegrationFixture(t, []string{"README.md"}, nil)
	result := commitWorkerFile(t, fixture, "README.md", "worker\n", true)
	if _, err := fixture.manager.Complete(context.Background(), CompleteRequest{WorkspaceID: fixture.lease.Portable.WorkspaceID, OwnerToken: fixture.lease.Owner.Token, ResultCommit: result}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.repo, "README.md"), []byte("integration\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, fixture.repo, "add", "README.md")
	runGit(t, fixture.repo, "commit", "-m", "integration-side change")
	expected := gitOutput(t, fixture.repo, "rev-parse", "HEAD")

	resultState, err := fixture.manager.Integrate(context.Background(), IntegrateRequest{WorkspaceID: fixture.lease.Portable.WorkspaceID, OwnerToken: fixture.lease.Owner.Token, ExpectedHead: expected})
	if !errors.Is(err, ErrIntegrationConflict) || resultState.Lease.Portable.State != StateConflicted {
		t.Fatalf("conflict result=%+v err=%v", resultState, err)
	}
	if got := gitOutput(t, fixture.repo, "rev-parse", "HEAD"); got != expected {
		t.Fatalf("conflict abort changed HEAD: %s", got)
	}
	if status := gitOutput(t, fixture.repo, "status", "--porcelain=v1"); status != "" {
		t.Fatalf("conflict abort left dirty integration: %q", status)
	}
	cherryPickHead := gitOutput(t, fixture.repo, "rev-parse", "--git-path", "CHERRY_PICK_HEAD")
	if _, statErr := os.Stat(filepath.Join(fixture.repo, cherryPickHead)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("conflict abort left CHERRY_PICK_HEAD: %v", statErr)
	}
}

func TestConcurrentIntegrateUsesCoordinatorLockAndConverges(t *testing.T) {
	fixture := completedFixture(t, "internal/x.go", "package internal\n")
	blocker := &blockingCherryPickRunner{GitRunner: ExecGitRunner{}, entered: make(chan struct{}), release: make(chan struct{})}
	fixture.manager.Runner = blocker
	second, err := OpenManager(context.Background(), fixture.repo, fixture.manager.WorkspaceRoot, ManagerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	request := IntegrateRequest{WorkspaceID: fixture.lease.Portable.WorkspaceID, OwnerToken: fixture.lease.Owner.Token, ExpectedHead: fixture.base}
	results := make(chan struct {
		result IntegrationResult
		err    error
	}, 2)
	go func() {
		result, err := fixture.manager.Integrate(context.Background(), request)
		results <- struct {
			result IntegrationResult
			err    error
		}{result, err}
	}()
	<-blocker.entered
	go func() {
		result, err := second.Integrate(context.Background(), request)
		results <- struct {
			result IntegrationResult
			err    error
		}{result, err}
	}()
	select {
	case early := <-results:
		t.Fatalf("integration escaped coordinator lock: %+v err=%v", early.result, early.err)
	case <-time.After(100 * time.Millisecond):
	}
	close(blocker.release)
	var integrated, already int
	for range 2 {
		outcome := <-results
		if outcome.err != nil {
			t.Fatal(outcome.err)
		}
		if outcome.result.AlreadyIntegrated {
			already++
		} else {
			integrated++
		}
	}
	if integrated != 1 || already != 1 {
		t.Fatalf("concurrent outcomes integrated=%d already=%d", integrated, already)
	}
}

func TestIntegrateRetryRecognizesAppliedCommitAfterPublicationCrash(t *testing.T) {
	fixture := completedFixture(t, "internal/x.go", "package internal\n")
	crash := errors.New("injected crash after cherry-pick")
	fixture.manager.Runner = &failAfterAppliedCommitRunner{GitRunner: ExecGitRunner{}, failure: crash}
	request := IntegrateRequest{WorkspaceID: fixture.lease.Portable.WorkspaceID, OwnerToken: fixture.lease.Owner.Token, ExpectedHead: fixture.base}
	if _, err := fixture.manager.Integrate(context.Background(), request); !errors.Is(err, crash) {
		t.Fatalf("injected publication crash not observed: %v", err)
	}
	stored, _, _ := fixture.manager.Store.Get(context.Background(), fixture.lease.Portable.WorkspaceID)
	if stored.Portable.State != StateIntegrating || gitOutput(t, fixture.repo, "rev-parse", "HEAD") == fixture.base {
		t.Fatalf("crash window not established: %+v", stored)
	}
	if attempts := integrationAttemptRefs(t, fixture); attempts == "" {
		t.Fatal("publication crash did not retain durable attempt marker")
	}
	fixture.manager.Runner = ExecGitRunner{}
	recovered, err := fixture.manager.Integrate(context.Background(), request)
	if err != nil || !recovered.AlreadyIntegrated || recovered.Lease.Portable.State != StateIntegrated {
		t.Fatalf("retry did not recover applied commit: %+v err=%v", recovered, err)
	}
	if count := gitOutput(t, fixture.repo, "rev-list", "--count", fixture.base+"..HEAD"); count != "1" {
		t.Fatalf("recovery duplicated commit: %s", count)
	}
	if attempts := integrationAttemptRefs(t, fixture); attempts != "" {
		t.Fatalf("recovery left attempt marker %q", attempts)
	}
}

func TestIntegrateResumePublicationDriftPreservesExternalHeadAndWorkerResult(t *testing.T) {
	fixture := completedFixture(t, "internal/resume-race.go", "package internal\n")
	crash := errors.New("injected crash after cherry-pick")
	fixture.manager.Runner = &failAfterAppliedCommitRunner{GitRunner: ExecGitRunner{}, failure: crash}
	request := IntegrateRequest{WorkspaceID: fixture.lease.Portable.WorkspaceID, OwnerToken: fixture.lease.Owner.Token, ExpectedHead: fixture.base}
	if _, err := fixture.manager.Integrate(context.Background(), request); !errors.Is(err, crash) {
		t.Fatalf("injected publication crash not observed: %v", err)
	}
	coordinatorHead := gitOutput(t, fixture.repo, "rev-parse", "HEAD")
	externalWorktree := filepath.Join(t.TempDir(), "resume-external-worktree")
	runGit(t, fixture.repo, "worktree", "add", "-b", "external-resume-p026", externalWorktree, coordinatorHead)
	if err := os.WriteFile(filepath.Join(externalWorktree, "external-resume.txt"), []byte("preserve resume work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, externalWorktree, "add", "external-resume.txt")
	runGit(t, externalWorktree, "commit", "-m", "external resume publication advance")
	externalHead := gitOutput(t, externalWorktree, "rev-parse", "HEAD")
	runGit(t, fixture.repo, "worktree", "remove", externalWorktree)
	runGit(t, fixture.repo, "branch", "-D", "external-resume-p026")

	fixture.manager.Runner = ExecGitRunner{}
	ctx := withIntegrationRaceHook(context.Background(), func(phase string) error {
		if phase == integrationHookBeforeResumePublication {
			runGit(t, fixture.repo, "reset", "--hard", externalHead)
		}
		return nil
	})
	result, err := fixture.manager.Integrate(ctx, request)
	if !errors.Is(err, ErrStaleIntegration) || result.Lease.Portable.State != StateConflicted {
		t.Fatalf("resume publication drift result=%+v err=%v", result, err)
	}
	if got := gitOutput(t, fixture.repo, "rev-parse", "HEAD"); got != externalHead {
		t.Fatalf("resume recovery changed external HEAD: got %s want %s", got, externalHead)
	}
	if content, readErr := os.ReadFile(filepath.Join(fixture.repo, "external-resume.txt")); readErr != nil || string(content) != "preserve resume work\n" {
		t.Fatalf("external resume work was not preserved: content=%q err=%v", content, readErr)
	}
	if status := gitOutput(t, fixture.repo, "status", "--porcelain=v1"); status != "" {
		t.Fatalf("resume recovery left dirty checkout: %q", status)
	}
	stored, _, _ := fixture.manager.Store.Get(context.Background(), fixture.lease.Portable.WorkspaceID)
	if stored.Portable.State != StateConflicted || stored.Portable.ResultCommit == "" || stored.Portable.IntegrationSHA != "" || integrationAttemptRefs(t, fixture) != "" {
		t.Fatalf("resume recovery lost retry evidence: %+v markers=%q", stored, integrationAttemptRefs(t, fixture))
	}
}

func TestIntegrateRetryRejectsManualSamePatchWithoutAttemptMarker(t *testing.T) {
	fixture := completedFixture(t, "internal/x.go", "package internal\n")
	stored, err := fixture.manager.Store.Update(context.Background(), fixture.lease.Portable.WorkspaceID, func(current *LocalLease) error {
		current.Portable.State = StateIntegrating
		current.Integration = IntegrationState{ExpectedHead: fixture.base, ObservedHead: fixture.base, StartedAt: time.Now().UTC()}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, fixture.repo, "cherry-pick", stored.Portable.ResultCommit)
	if attempts := integrationAttemptRefs(t, fixture); attempts != "" {
		t.Fatalf("manual cherry-pick unexpectedly created attempt marker %q", attempts)
	}
	_, err = fixture.manager.Integrate(context.Background(), IntegrateRequest{
		WorkspaceID: fixture.lease.Portable.WorkspaceID, OwnerToken: fixture.lease.Owner.Token, ExpectedHead: fixture.base,
	})
	if !errors.Is(err, ErrStaleIntegration) || !strings.Contains(err.Error(), "durable attempt marker") {
		t.Fatalf("manual same-patch commit accepted as crash recovery: %v", err)
	}
	stillIntegrating, _, _ := fixture.manager.Store.Get(context.Background(), fixture.lease.Portable.WorkspaceID)
	if stillIntegrating.Portable.State != StateIntegrating || stillIntegrating.Portable.IntegrationSHA != "" {
		t.Fatalf("manual commit changed durable integration state: %+v", stillIntegrating)
	}
}

func TestIntegrateRetryRejectsMarkedSamePatchWithDifferentCommitIdentity(t *testing.T) {
	fixture := completedFixture(t, "internal/x.go", "package internal\n")
	stored, err := fixture.manager.Store.Update(context.Background(), fixture.lease.Portable.WorkspaceID, func(current *LocalLease) error {
		current.Portable.State = StateIntegrating
		current.Integration = IntegrationState{ExpectedHead: fixture.base, ObservedHead: fixture.base, StartedAt: time.Now().UTC()}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	markerRef := integrationAttemptRef(stored, fixture.base)
	runGit(t, fixture.repo, "update-ref", markerRef, fixture.base, strings.Repeat("0", len(fixture.base)))
	runGit(t, fixture.repo, "cherry-pick", "--no-commit", stored.Portable.ResultCommit)
	runGit(t, fixture.repo, "commit", "-s", "-m", "manual same patch with different identity")
	_, err = fixture.manager.Integrate(context.Background(), IntegrateRequest{
		WorkspaceID: fixture.lease.Portable.WorkspaceID, OwnerToken: fixture.lease.Owner.Token, ExpectedHead: fixture.base,
	})
	if !errors.Is(err, ErrStaleIntegration) || !strings.Contains(err.Error(), "not the recorded worker result") {
		t.Fatalf("marked same patch with changed identity accepted: %v", err)
	}
	if marker := gitOutput(t, fixture.repo, "rev-parse", markerRef); marker != fixture.base {
		t.Fatalf("failed recovery changed marker from expected HEAD: %s", marker)
	}
}

func TestIntegratedRetryRefusesToDeleteMarkerWithoutExactCASValue(t *testing.T) {
	fixture := completedFixture(t, "internal/x.go", "package internal\n")
	integrated, err := fixture.manager.Integrate(context.Background(), IntegrateRequest{
		WorkspaceID: fixture.lease.Portable.WorkspaceID, OwnerToken: fixture.lease.Owner.Token, ExpectedHead: fixture.base,
	})
	if err != nil {
		t.Fatal(err)
	}
	markerRef := integrationAttemptRef(integrated.Lease, fixture.base)
	runGit(t, fixture.repo, "update-ref", markerRef, fixture.base, strings.Repeat("0", len(fixture.base)))
	_, err = fixture.manager.Integrate(context.Background(), IntegrateRequest{
		WorkspaceID: fixture.lease.Portable.WorkspaceID, OwnerToken: fixture.lease.Owner.Token, ExpectedHead: fixture.base,
	})
	if !errors.Is(err, ErrWorkspaceConflict) || !strings.Contains(err.Error(), "marker disagrees") {
		t.Fatalf("retry deleted marker without exact integrated SHA: %v", err)
	}
	if marker := gitOutput(t, fixture.repo, "rev-parse", markerRef); marker != fixture.base {
		t.Fatalf("mismatched marker changed or deleted: %s", marker)
	}
}

type integrationFixture struct {
	repo, base, worktree string
	manager              *Manager
	lease                LocalLease
}

func newIntegrationFixture(t *testing.T, ownership, shared []string) integrationFixture {
	t.Helper()
	repo, base := newGitRepository(t)
	manager := openTestManager(t, repo)
	lease := testLease("ws-integration", "PROCESS-005", ModeWritable, "worker-integration", base, ownership)
	lease.Portable.SharedTouchpoints = shared
	prepared, err := manager.Prepare(context.Background(), PrepareRequest{Lease: lease})
	if err != nil {
		t.Fatal(err)
	}
	return integrationFixture{repo: repo, base: base, worktree: prepared.Lease.WorktreePath, manager: manager, lease: lease}
}

func completedFixture(t *testing.T, path, content string) integrationFixture {
	t.Helper()
	fixture := newIntegrationFixture(t, []string{"internal/**"}, nil)
	result := commitWorkerFile(t, fixture, path, content, true)
	if _, err := fixture.manager.Complete(context.Background(), CompleteRequest{WorkspaceID: fixture.lease.Portable.WorkspaceID, OwnerToken: fixture.lease.Owner.Token, ResultCommit: result}); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func commitWorkerFile(t *testing.T, fixture integrationFixture, name, content string, signoff bool) string {
	t.Helper()
	path := filepath.Join(fixture.worktree, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, fixture.worktree, "add", "--", name)
	if !signoff {
		tree := gitOutput(t, fixture.worktree, "write-tree")
		parent := gitOutput(t, fixture.worktree, "rev-parse", "HEAD")
		command := exec.Command("git", "commit-tree", tree, "-p", parent)
		command.Dir = fixture.worktree
		command.Stdin = strings.NewReader("worker change\n")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git commit-tree: %v: %s", err, output)
		}
		commit := strings.TrimSpace(string(output))
		runGit(t, fixture.worktree, "update-ref", "HEAD", commit, parent)
		runGit(t, fixture.worktree, "reset", "--hard", commit)
		return commit
	}
	args := []string{"commit"}
	args = append(args, "-s")
	args = append(args, "-m", "worker change")
	runGit(t, fixture.worktree, args...)
	return gitOutput(t, fixture.worktree, "rev-parse", "HEAD")
}

func integrationAttemptRefs(t *testing.T, fixture integrationFixture) string {
	t.Helper()
	return gitOutput(t, fixture.repo, "for-each-ref", "--format=%(refname)", "refs/issue-spec/process-integrations/"+fixture.lease.Portable.WorkspaceID+"/")
}

type blockingCherryPickRunner struct {
	GitRunner
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

type failAfterAppliedCommitRunner struct {
	GitRunner
	mu                sync.Mutex
	appliedCommitSeen bool
	failure           error
}

func (r *failAfterAppliedCommitRunner) Run(ctx context.Context, command GitCommand) (GitResult, error) {
	result, err := r.GitRunner.Run(ctx, command)
	if err != nil {
		return result, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(command.Args) > 0 && command.Args[0] == "cherry-pick" && (len(command.Args) < 2 || command.Args[1] != "--abort") {
		r.appliedCommitSeen = true
		return result, nil
	}
	if r.appliedCommitSeen && len(command.Args) == 2 && command.Args[0] == "rev-parse" && command.Args[1] == "HEAD" {
		return result, r.failure
	}
	return result, nil
}

func (r *blockingCherryPickRunner) Run(ctx context.Context, command GitCommand) (GitResult, error) {
	if len(command.Args) > 0 && command.Args[0] == "cherry-pick" && (len(command.Args) < 2 || command.Args[1] != "--abort") {
		r.once.Do(func() {
			close(r.entered)
			select {
			case <-r.release:
			case <-ctx.Done():
			}
		})
	}
	return r.GitRunner.Run(ctx, command)
}

func TestIntegrationErrorsAreGrepFriendly(t *testing.T) {
	for _, err := range []error{ErrInvalidWorkerResult, ErrStaleIntegration, ErrIntegrationConflict, ErrOwnershipViolation} {
		if strings.TrimSpace(err.Error()) == "" {
			t.Fatal(fmt.Errorf("empty integration error: %w", err))
		}
	}
}
