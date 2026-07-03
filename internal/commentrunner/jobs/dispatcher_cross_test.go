package jobs_test

import (
	"context"
	"testing"

	"github.com/higress-group/issue-spec/internal/acpx"
	"github.com/higress-group/issue-spec/internal/commentrunner/jobs"
	"github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/commentrunner/testkit"
)

func TestRunNextPersistsUnsafeSandboxMarkersAndKeepsCallerContext(t *testing.T) {
	store := testkit.NewMemoryStore()
	now := testkit.Now
	if err := store.Update(context.Background(), func(st *state.RunnerState) error {
		_, _, err := st.CreateCommandJob(state.Job{
			ID:                    "job-unsafe",
			Repo:                  "o/r",
			IssueNumber:           30,
			CoordinatorKind:       "codex",
			Model:                 "gpt-5.5[xhigh]",
			SessionCreatorLogin:   "alice",
			TriggeringUserLogin:   "alice",
			TriggerCommentID:      701,
			CommandID:             "cmd-unsafe",
			CommandName:           "new",
			CommandPrompt:         "exercise unsafe marker propagation",
			CommandIdempotencyKey: "cmd-key-unsafe",
			StatusWritebackKey:    "status-unsafe",
			Status:                state.StatusQueued,
			CreatedAt:             now,
			FirstObservedComment: state.SeenComment{
				Repo:                          "o/r",
				IssueNumber:                   30,
				CommentID:                     701,
				HTMLURL:                       "https://github.com/o/r/issues/30#issuecomment-701",
				AuthorLogin:                   "alice",
				FirstObservedBodyHash:         "sha256:first",
				StatusWritebackIdempotencyKey: "status-unsafe",
			},
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	binding := testkit.WorkspaceBinding("ws-unsafe")
	coordinator := &testkit.Coordinator{NewResult: testkit.DispatchResult("ps-unsafe", "rec-unsafe", "turn-unsafe")}
	writebacks := &testkit.Writeback{Store: store}
	dispatcher := jobs.Dispatcher{
		Store:           store,
		Repositories:    testkit.RepoResolver{},
		Workspaces:      &testkit.Workspaces{Binding: binding},
		Sandbox:         &testkit.Sandbox{Env: jobs.ExecutionEnvironment{WorkingDirectory: binding.AcpxWorkingDirectory, Sandbox: state.SandboxMetadata{UnsafeNoSandbox: true, SandboxProvider: "none", FSBoundary: "disabled", Diagnostics: "unsafe no-sandbox mode explicitly selected"}, Runner: successfulAuthRunner{}}},
		Acpx:            &testkit.AcpxFactory{Coordinator: coordinator},
		Writeback:       writebacks,
		Clock:           testkit.Clock{Time: now},
		PublicSessionID: func() (string, error) { return "ps-unsafe", nil },
		TurnCorrelationID: func() (string, error) {
			return "turn-token-unsafe", nil
		},
		IssueSpecBinary: "issue-spec",
	}

	result, err := dispatcher.RunNext(context.Background())
	if err != nil {
		t.Fatalf("RunNext returned error: %v", err)
	}
	if !result.Executed || result.Status != state.StatusCompleted {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(coordinator.NewContextHasDeadline) != 1 || coordinator.NewContextHasDeadline[0] {
		t.Fatalf("dispatcher added a hard acpx dispatch deadline: %+v", coordinator.NewContextHasDeadline)
	}
	snapshot := store.Snapshot()
	job := snapshot.Jobs["job-unsafe"]
	if !job.Sandbox.UnsafeNoSandbox || job.Sandbox.SandboxProvider != "none" || job.Sandbox.FSBoundary != "disabled" {
		t.Fatalf("unsafe sandbox metadata not persisted on job: %+v", job.Sandbox)
	}
	if len(writebacks.Requests) == 0 {
		t.Fatal("expected status writeback requests")
	}
	final := writebacks.Requests[len(writebacks.Requests)-1].Job
	if !final.Sandbox.UnsafeNoSandbox || final.Sandbox.FSBoundary != "disabled" {
		t.Fatalf("unsafe sandbox metadata missing from writeback job snapshot: %+v", final.Sandbox)
	}
}

type successfulAuthRunner struct{}

func (successfulAuthRunner) Run(context.Context, acpx.Command) (acpx.CommandResult, error) {
	return acpx.CommandResult{
		Stdout: []byte(`{"ok":true,"auth":{"host":"github.com","source":"gh","user":"bot"},"backend":{"name":"gh","selection_source":"auto:gh"}}`),
	}, nil
}
