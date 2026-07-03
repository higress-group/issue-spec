package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/acpx"
	runnercontext "github.com/higress-group/issue-spec/internal/commentrunner/context"
	"github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/commentrunner/writeback"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/sandbox"
	"github.com/higress-group/issue-spec/internal/workspace"
)

func TestRunNextNewCreatesSessionMappingAndCompletionWriteback(t *testing.T) {
	store := newMemoryStore()
	now := time.Date(2026, 7, 3, 10, 0, 0, 0, time.UTC)
	seedQueuedJob(t, store, state.Job{
		ID:                    "job-new",
		Repo:                  "o/r",
		IssueNumber:           30,
		CoordinatorKind:       "codex",
		Model:                 "gpt-5.5[xhigh]",
		SessionCreatorLogin:   "alice",
		TriggeringUserLogin:   "alice",
		TriggerCommentID:      101,
		CommandID:             "cmd-new",
		CommandName:           "new",
		CommandPrompt:         "implement TASK-016",
		CommandIdempotencyKey: "cmd-key-new",
		StatusWritebackKey:    "status-new",
		Status:                state.StatusQueued,
		CreatedAt:             now,
		FirstObservedComment: state.SeenComment{
			Repo:                          "o/r",
			IssueNumber:                   30,
			CommentID:                     101,
			HTMLURL:                       "https://github.com/o/r/issues/30#issuecomment-101",
			AuthorLogin:                   "alice",
			FirstObservedUpdatedAt:        now,
			FirstObservedBodyHash:         "sha256:first",
			StatusWritebackIdempotencyKey: "status-new",
		},
	})
	workspaces := &fakeWorkspaces{binding: testBinding("ws-new")}
	writebacks := &fakeWriteback{}
	coordinator := &fakeCoordinator{newResult: dispatchResult("ps-new", "rec-new", "turn-new", completedSummary())}
	dispatcher := testDispatcher(store, workspaces, coordinator, writebacks, now)
	dispatcher.PublicSessionID = func() (string, error) { return "ps-new", nil }
	dispatcher.TurnCorrelationID = func() (string, error) { return "turn-token-new", nil }

	result, err := dispatcher.RunNext(context.Background())
	if err != nil {
		t.Fatalf("RunNext returned error: %v", err)
	}
	if !result.Executed || result.JobID != "job-new" || result.Status != state.StatusCompleted {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(coordinator.newPrompts) != 1 || !strings.Contains(coordinator.newPrompts[0], "implement TASK-016") || !strings.Contains(coordinator.newPrompts[0], `"source_label": "authorized_command"`) {
		t.Fatalf("new prompt missing context bundle: %q", first(coordinator.newPrompts))
	}
	if len(coordinator.resumePrompts) != 0 {
		t.Fatalf("unexpected resume dispatch: %+v", coordinator.resumePrompts)
	}
	assertWritebackStatuses(t, writebacks, state.StatusRunning, state.StatusCompleted)

	st := loadState(t, store)
	job := st.Jobs["job-new"]
	if job.Status != state.StatusCompleted || job.PublicSessionID != "ps-new" || job.AcpxRecordID != "rec-new" {
		t.Fatalf("job metadata not completed: %+v", job)
	}
	if job.ContextBundle.Hash == "" || job.DispatchIntent.TurnCorrelationToken != "turn-token-new" || job.DispatchIntent.StatusCommentID != 9001 {
		t.Fatalf("dispatch intent/context not persisted: %+v context=%+v", job.DispatchIntent, job.ContextBundle)
	}
	if len(job.CLIDirect) != 1 || job.CLIDirect[0].CommandName != "issue-spec comment upsert" {
		t.Fatalf("CLI-direct provenance missing: %+v", job.CLIDirect)
	}
	session, ok := st.GetPublicSession("o/r", "ps-new")
	if !ok || session.AcpxRecordID != "rec-new" || session.Workspace.ID != "ws-new" || session.LastJobID != "job-new" {
		t.Fatalf("public session mapping missing: %+v ok=%v", session, ok)
	}
	if _, ok := st.GetWorkspace("ws-new"); !ok {
		t.Fatalf("workspace metadata was not indexed: %+v", st.Workspaces)
	}
	if !workspaces.released {
		t.Fatal("workspace lock was not released")
	}
}

func TestRunNextResumeReusesSessionMappingAndWorkspace(t *testing.T) {
	store := newMemoryStore()
	now := time.Date(2026, 7, 3, 11, 0, 0, 0, time.UTC)
	resumeWorkspace := state.WorkspaceMetadata{ID: "ws-existing", Path: "/tmp/ws-existing", Repo: "o/r", CloneURL: "https://github.com/o/r.git", Branch: "issue-spec-ws-existing"}
	seedState(t, store, func(st *state.RunnerState) error {
		if err := st.UpsertWorkspace(resumeWorkspace); err != nil {
			return err
		}
		if err := st.UpsertPublicSession(state.PublicSession{
			Repo:            "o/r",
			PublicSessionID: "ps-existing",
			IssueNumber:     30,
			AcpxRecordID:    "rec-existing",
			CreatorLogin:    "alice",
			Status:          state.StatusCompleted,
			Workspace:       resumeWorkspace,
			Queue:           state.SessionQueue{AcceptedSequence: 3},
			CreatedAt:       now.Add(-time.Hour),
			LastUsedAt:      now.Add(-time.Minute),
		}); err != nil {
			return err
		}
		_, _, err := st.CreateCommandJob(state.Job{
			ID:                    "job-resume",
			Repo:                  "o/r",
			IssueNumber:           30,
			PublicSessionID:       "ps-existing",
			CoordinatorKind:       "codex",
			Model:                 "gpt-5.5[xhigh]",
			SessionCreatorLogin:   "alice",
			TriggeringUserLogin:   "bob",
			TriggerCommentID:      202,
			CommandID:             "cmd-resume",
			CommandName:           "resume",
			CommandPrompt:         "continue TASK-016",
			CommandIdempotencyKey: "cmd-key-resume",
			StatusWritebackKey:    "status-resume",
			Status:                state.StatusQueued,
			CreatedAt:             now,
			FirstObservedComment: state.SeenComment{
				Repo:                  "o/r",
				IssueNumber:           30,
				CommentID:             202,
				HTMLURL:               "https://github.com/o/r/issues/30#issuecomment-202",
				AuthorLogin:           "bob",
				FirstObservedBodyHash: "sha256:resume",
			},
		})
		return err
	})
	workspaces := &fakeWorkspaces{binding: workspace.Binding{Workspace: resumeWorkspace, AcpxWorkingDirectory: resumeWorkspace.Path, SandboxWorkspacePath: resumeWorkspace.Path}}
	writebacks := &fakeWriteback{}
	coordinator := &fakeCoordinator{resumeResult: dispatchResult("ps-existing", "rec-existing", "turn-resume", completedSummary())}
	dispatcher := testDispatcher(store, workspaces, coordinator, writebacks, now)
	dispatcher.TurnCorrelationID = func() (string, error) { return "turn-token-resume", nil }

	result, err := dispatcher.RunNext(context.Background())
	if err != nil {
		t.Fatalf("RunNext returned error: %v", err)
	}
	if result.Status != state.StatusCompleted {
		t.Fatalf("unexpected result: %+v", result)
	}
	if workspaces.prepareNewCalled {
		t.Fatal("resume should not prepare a new workspace")
	}
	if !workspaces.resolveResumeCalled {
		t.Fatal("resume did not resolve stored workspace")
	}
	if len(coordinator.resumePrompts) != 1 || !strings.Contains(coordinator.resumePrompts[0], `"public_session_id": "ps-existing"`) {
		t.Fatalf("resume prompt missing session context: %q", first(coordinator.resumePrompts))
	}
	assertWritebackStatuses(t, writebacks, state.StatusRunning, state.StatusCompleted)

	st := loadState(t, store)
	job := st.Jobs["job-resume"]
	if job.Status != state.StatusCompleted || job.AcpxRecordID != "rec-existing" || job.TriggeringUserLogin != "bob" || job.SessionCreatorLogin != "alice" {
		t.Fatalf("resume job metadata wrong: %+v", job)
	}
	if job.DispatchIntent.TurnSequence != 4 {
		t.Fatalf("turn sequence = %d, want 4", job.DispatchIntent.TurnSequence)
	}
	session, ok := st.GetPublicSession("o/r", "ps-existing")
	if !ok || session.LastJobID != "job-resume" || session.Lock.OwnerJobID != "" || len(session.Queue.PendingJobIDs) != 0 {
		t.Fatalf("resume session not updated/released: %+v ok=%v", session, ok)
	}
}

func TestRunNextNewAndResumeUseSameStableRuntimeHome(t *testing.T) {
	store := newMemoryStore()
	now := time.Date(2026, 7, 3, 11, 30, 0, 0, time.UTC)
	workspacePath := filepath.Join(t.TempDir(), "workspace")
	binding := workspace.Binding{
		Workspace:            state.WorkspaceMetadata{ID: "ws-stable", Path: workspacePath, Repo: "o/r", CloneURL: "https://github.com/o/r.git", Branch: "issue-spec-ws-stable"},
		AcpxWorkingDirectory: workspacePath,
		SandboxWorkspacePath: workspacePath,
	}
	seedQueuedJob(t, store, state.Job{
		ID:                    "job-stable-new",
		Repo:                  "o/r",
		IssueNumber:           30,
		CoordinatorKind:       "codex",
		Model:                 "gpt-5.5[xhigh]",
		SessionCreatorLogin:   "alice",
		TriggeringUserLogin:   "alice",
		TriggerCommentID:      211,
		CommandID:             "cmd-stable-new",
		CommandName:           "new",
		CommandPrompt:         "start stable runtime",
		CommandIdempotencyKey: "cmd-key-stable-new",
		StatusWritebackKey:    "status-stable-new",
		Status:                state.StatusQueued,
		CreatedAt:             now,
		FirstObservedComment: state.SeenComment{
			Repo:                          "o/r",
			IssueNumber:                   30,
			CommentID:                     211,
			HTMLURL:                       "https://github.com/o/r/issues/30#issuecomment-211",
			AuthorLogin:                   "alice",
			FirstObservedBodyHash:         "sha256:stable-new",
			StatusWritebackIdempotencyKey: "status-stable-new",
		},
	})
	workspaces := &fakeWorkspaces{binding: binding}
	sandbox := &fakeSandbox{}
	writebacks := &fakeWriteback{}
	coordinator := &fakeCoordinator{
		newResult:    dispatchResult("ps-stable", "rec-stable", "turn-new", completedSummary()),
		resumeResult: dispatchResult("ps-stable", "rec-stable", "turn-resume", completedSummary()),
	}
	dispatcher := testDispatcher(store, workspaces, coordinator, writebacks, now)
	dispatcher.Sandbox = sandbox
	dispatcher.PublicSessionID = func() (string, error) { return "ps-stable", nil }

	if result, err := dispatcher.RunNext(context.Background()); err != nil || result.Status != state.StatusCompleted {
		t.Fatalf("new RunNext result=%+v err=%v", result, err)
	}
	seedQueuedJob(t, store, state.Job{
		ID:                    "job-stable-resume",
		Repo:                  "o/r",
		IssueNumber:           30,
		PublicSessionID:       "ps-stable",
		CoordinatorKind:       "codex",
		Model:                 "gpt-5.5[xhigh]",
		SessionCreatorLogin:   "alice",
		TriggeringUserLogin:   "bob",
		TriggerCommentID:      212,
		CommandID:             "cmd-stable-resume",
		CommandName:           "resume",
		CommandPrompt:         "continue stable runtime",
		CommandIdempotencyKey: "cmd-key-stable-resume",
		StatusWritebackKey:    "status-stable-resume",
		Status:                state.StatusQueued,
		CreatedAt:             now.Add(time.Minute),
		FirstObservedComment: state.SeenComment{
			Repo:                  "o/r",
			IssueNumber:           30,
			CommentID:             212,
			HTMLURL:               "https://github.com/o/r/issues/30#issuecomment-212",
			AuthorLogin:           "bob",
			FirstObservedBodyHash: "sha256:stable-resume",
		},
	})
	if result, err := dispatcher.RunNext(context.Background()); err != nil || result.Status != state.StatusCompleted {
		t.Fatalf("resume RunNext result=%+v err=%v", result, err)
	}
	if len(sandbox.requests) != 2 {
		t.Fatalf("sandbox request count = %d, want 2", len(sandbox.requests))
	}
	newReq, resumeReq := sandbox.requests[0], sandbox.requests[1]
	if newReq.RuntimeHome == "" || newReq.RuntimeHome != resumeReq.RuntimeHome {
		t.Fatalf("runtime HOME not stable: new=%q resume=%q", newReq.RuntimeHome, resumeReq.RuntimeHome)
	}
	if filepath.Join(newReq.RuntimeHome, ".acpx", "sessions", "index.json") != filepath.Join(resumeReq.RuntimeHome, ".acpx", "sessions", "index.json") {
		t.Fatalf("acpx session index path changed: new=%q resume=%q", newReq.RuntimeHome, resumeReq.RuntimeHome)
	}
	wantRoot, err := stableSessionRuntimeRoot(workspacePath, "o/r", "ps-stable")
	if err != nil {
		t.Fatal(err)
	}
	if newReq.RuntimeHome != filepath.Join(wantRoot, "home") || newReq.RuntimeGHConfigDir != filepath.Join(wantRoot, "gh") || newReq.RuntimeXDGConfigHome != filepath.Join(wantRoot, "xdg") || newReq.RuntimeCodexHome != filepath.Join(wantRoot, "codex") {
		t.Fatalf("runtime paths not under stable root %s: %+v", wantRoot, newReq)
	}
	if !strings.HasPrefix(newReq.RuntimeHome, filepath.Join(workspacePath, ".sessions")+string(os.PathSeparator)) {
		t.Fatalf("runtime HOME %q is not under workspace .sessions", newReq.RuntimeHome)
	}
}

func TestStableSessionRuntimePathsSeparatePublicSessions(t *testing.T) {
	workspacePath := filepath.Join(t.TempDir(), "workspace")
	left, err := stableSessionRuntimePaths(workspacePath, "o/r", "ps-left")
	if err != nil {
		t.Fatal(err)
	}
	right, err := stableSessionRuntimePaths(workspacePath, "o/r", "ps-right")
	if err != nil {
		t.Fatal(err)
	}
	if left.home == right.home || filepath.Join(left.home, ".acpx", "sessions", "index.json") == filepath.Join(right.home, ".acpx", "sessions", "index.json") {
		t.Fatalf("different public sessions share a runtime HOME: left=%q right=%q", left.home, right.home)
	}
	if left.ghConfigDir == right.ghConfigDir || left.codexHome == right.codexHome {
		t.Fatalf("different public sessions share runtime config dirs: left=%+v right=%+v", left, right)
	}
}

func TestRunNextFailurePersistsFailedStateAndBoundedError(t *testing.T) {
	store := newMemoryStore()
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	seedQueuedJob(t, store, state.Job{
		ID:                    "job-fail",
		Repo:                  "o/r",
		IssueNumber:           30,
		SessionCreatorLogin:   "alice",
		TriggeringUserLogin:   "alice",
		TriggerCommentID:      303,
		CommandID:             "cmd-fail",
		CommandName:           "new",
		CommandPrompt:         "do work",
		CommandIdempotencyKey: "cmd-key-fail",
		StatusWritebackKey:    "status-fail",
		Status:                state.StatusQueued,
		CreatedAt:             now,
	})
	longErr := errors.New(strings.Repeat("x", 2000))
	workspaces := &fakeWorkspaces{err: longErr}
	writebacks := &fakeWriteback{}
	coordinator := &fakeCoordinator{}
	dispatcher := testDispatcher(store, workspaces, coordinator, writebacks, now)
	dispatcher.PublicSessionID = func() (string, error) { return "ps-fail", nil }

	result, err := dispatcher.RunNext(context.Background())
	if err == nil {
		t.Fatal("RunNext succeeded, want workspace error")
	}
	if result.Status != state.StatusFailed || len([]byte(result.Error)) > 1024 {
		t.Fatalf("failure result not bounded: %+v", result)
	}
	if len(coordinator.newPrompts) != 0 || len(coordinator.resumePrompts) != 0 {
		t.Fatal("acpx should not run after workspace failure")
	}
	assertWritebackStatuses(t, writebacks, state.StatusFailed)

	job := loadState(t, store).Jobs["job-fail"]
	if job.Status != state.StatusFailed || len(job.Diagnostics) != 1 || len([]byte(job.Diagnostics[0])) > 1024 {
		t.Fatalf("failed lifecycle state not safely persisted: %+v", job)
	}
}

func TestRunNextFailsBeforeAcpxWhenChildAuthProbeFails(t *testing.T) {
	store := newMemoryStore()
	now := time.Date(2026, 7, 3, 12, 5, 0, 0, time.UTC)
	seedQueuedJob(t, store, state.Job{
		ID:                    "job-auth-fail",
		Repo:                  "o/r",
		IssueNumber:           30,
		SessionCreatorLogin:   "alice",
		TriggeringUserLogin:   "alice",
		TriggerCommentID:      313,
		CommandID:             "cmd-auth-fail",
		CommandName:           "new",
		CommandPrompt:         "do work",
		CommandIdempotencyKey: "cmd-key-auth-fail",
		StatusWritebackKey:    "status-auth-fail",
		Status:                state.StatusQueued,
		CreatedAt:             now,
	})
	probe := &fakeAuthProbeRunner{result: acpx.CommandResult{
		Stdout: []byte(`{"ok":false,"host":"github.com","error":"gh backend token invalid","backend":{"name":"gh","selection_source":"auto:gh"}}`),
	}}
	workspaces := &fakeWorkspaces{binding: testBinding("ws-auth-fail")}
	writebacks := &fakeWriteback{}
	coordinator := &fakeCoordinator{newResult: dispatchResult("ps-auth-fail", "rec-auth-fail", "turn-auth-fail", completedSummary())}
	dispatcher := testDispatcher(store, workspaces, coordinator, writebacks, now)
	dispatcher.Sandbox = &fakeSandbox{env: ExecutionEnvironment{
		WorkingDirectory: "/workspace",
		Sandbox:          state.SandboxMetadata{SandboxProvider: "none", FSBoundary: "disabled"},
		Runner:           probe,
	}}
	dispatcher.PublicSessionID = func() (string, error) { return "ps-auth-fail", nil }

	result, err := dispatcher.RunNext(context.Background())
	if err == nil {
		t.Fatal("RunNext succeeded, want child auth failure")
	}
	if result.Status != state.StatusFailed || !strings.Contains(result.Error, "pre-acpx child auth probe failed") || !strings.Contains(result.Error, "backend=gh") {
		t.Fatalf("unexpected result: %+v err=%v", result, err)
	}
	if len(coordinator.newPrompts) != 0 || len(coordinator.resumePrompts) != 0 {
		t.Fatalf("acpx ran despite auth preflight failure: new=%d resume=%d", len(coordinator.newPrompts), len(coordinator.resumePrompts))
	}
	assertWritebackStatuses(t, writebacks, state.StatusFailed)
	if writebacks.requests[0].Phase != "child-auth" || writebacks.requests[0].Err == nil {
		t.Fatalf("auth failure was not visible through failed writeback: %+v", writebacks.requests[0])
	}
	job := loadState(t, store).Jobs["job-auth-fail"]
	if job.Status != state.StatusFailed || len(job.Diagnostics) != 1 || !strings.Contains(job.Diagnostics[0], "child-auth:") {
		t.Fatalf("auth failure was not persisted as controlled child-auth failure: %+v", job)
	}
	if len(probe.commands) != 1 || probe.commands[0].Binary != "issue-spec" || strings.Join(probe.commands[0].Args, " ") != "auth status --json" {
		t.Fatalf("unexpected auth probe command: %+v", probe.commands)
	}
}

func TestRunNextAuthProbeSuccessUsesConfiguredIssueSpecBinaryAndAllowsAcpxDispatch(t *testing.T) {
	store := newMemoryStore()
	now := time.Date(2026, 7, 3, 12, 10, 0, 0, time.UTC)
	seedQueuedJob(t, store, state.Job{
		ID:                    "job-auth-ok",
		Repo:                  "o/r",
		IssueNumber:           30,
		SessionCreatorLogin:   "alice",
		TriggeringUserLogin:   "alice",
		TriggerCommentID:      323,
		CommandID:             "cmd-auth-ok",
		CommandName:           "new",
		CommandPrompt:         "do work",
		CommandIdempotencyKey: "cmd-key-auth-ok",
		StatusWritebackKey:    "status-auth-ok",
		Status:                state.StatusQueued,
		CreatedAt:             now,
	})
	probe := &fakeAuthProbeRunner{}
	workspaces := &fakeWorkspaces{binding: testBinding("ws-auth-ok")}
	writebacks := &fakeWriteback{}
	coordinator := &fakeCoordinator{newResult: dispatchResult("ps-auth-ok", "rec-auth-ok", "turn-auth-ok", completedSummary())}
	dispatcher := testDispatcher(store, workspaces, coordinator, writebacks, now)
	dispatcher.Sandbox = &fakeSandbox{env: ExecutionEnvironment{
		WorkingDirectory: "/workspace",
		Sandbox:          state.SandboxMetadata{SandboxProvider: "none", FSBoundary: "disabled"},
		Runner:           probe,
	}}
	dispatcher.PublicSessionID = func() (string, error) { return "ps-auth-ok", nil }
	dispatcher.IssueSpecBinary = "/tmp/issue-spec-runner-e2e-001/bin/issue-spec"

	result, err := dispatcher.RunNext(context.Background())
	if err != nil {
		t.Fatalf("RunNext returned error: %v", err)
	}
	if result.Status != state.StatusCompleted || len(coordinator.newPrompts) != 1 {
		t.Fatalf("auth success did not allow dispatch: result=%+v prompts=%d", result, len(coordinator.newPrompts))
	}
	if len(probe.commands) != 1 || probe.commands[0].Binary != "/tmp/issue-spec-runner-e2e-001/bin/issue-spec" || strings.Join(probe.commands[0].Args, " ") != "auth status --json" {
		t.Fatalf("unexpected auth probe command: %+v", probe.commands)
	}
	assertWritebackStatuses(t, writebacks, state.StatusRunning, state.StatusCompleted)
}

func TestRunNextSummaryFailurePersistsSessionMapping(t *testing.T) {
	store := newMemoryStore()
	now := time.Date(2026, 7, 3, 12, 15, 0, 0, time.UTC)
	seedQueuedJob(t, store, state.Job{
		ID:                    "job-summary-fail",
		Repo:                  "o/r",
		IssueNumber:           30,
		SessionCreatorLogin:   "alice",
		TriggeringUserLogin:   "alice",
		TriggerCommentID:      333,
		CommandID:             "cmd-summary-fail",
		CommandName:           "new",
		CommandPrompt:         "do work",
		CommandIdempotencyKey: "cmd-key-summary-fail",
		StatusWritebackKey:    "status-summary-fail",
		Status:                state.StatusQueued,
		CreatedAt:             now,
	})
	workspaces := &fakeWorkspaces{binding: testBinding("ws-summary-fail")}
	writebacks := &fakeWriteback{}
	partial := dispatchResult("ps-summary-fail", "rec-summary-fail", "turn-summary-fail", runnercontext.CoordinatorSummary{})
	partial.Output = acpx.TurnOutput{RawStdout: "assistant output without coordinator summary"}
	coordinator := &fakeCoordinator{
		newErr: &acpx.PartialDispatchError{
			Result: partial,
			Err:    &acpx.OutputSummaryError{Err: acpx.ErrSummaryNotFound},
		},
	}
	dispatcher := testDispatcher(store, workspaces, coordinator, writebacks, now)
	dispatcher.PublicSessionID = func() (string, error) { return "ps-summary-fail", nil }
	dispatcher.TurnCorrelationID = func() (string, error) { return "turn-token-summary-fail", nil }

	result, err := dispatcher.RunNext(context.Background())
	if !errors.Is(err, acpx.ErrSummaryNotFound) {
		t.Fatalf("RunNext error = %v, want ErrSummaryNotFound", err)
	}
	if result.Status != state.StatusFailed || result.JobID != "job-summary-fail" {
		t.Fatalf("unexpected result: %+v", result)
	}
	assertWritebackStatuses(t, writebacks, state.StatusRunning, state.StatusFailed)

	st := loadState(t, store)
	job := st.Jobs["job-summary-fail"]
	if job.Status != state.StatusFailed || job.PublicSessionID != "ps-summary-fail" || job.AcpxRecordID != "rec-summary-fail" {
		t.Fatalf("failed job did not retain dispatch metadata: %+v", job)
	}
	if job.CoordinatorSummary != "" || len(job.CLIDirect) != 0 {
		t.Fatalf("invalid summary should not be persisted as provenance: summary=%q cli=%+v", job.CoordinatorSummary, job.CLIDirect)
	}
	session, ok := st.GetPublicSession("o/r", "ps-summary-fail")
	if !ok || session.Status != state.StatusFailed || session.AcpxRecordID != "rec-summary-fail" || session.Workspace.ID != "ws-summary-fail" || session.LastJobID != "job-summary-fail" || session.Lock.OwnerJobID != "" {
		t.Fatalf("public session mapping missing after summary failure: %+v ok=%v", session, ok)
	}
	if _, ok := st.GetWorkspace("ws-summary-fail"); !ok {
		t.Fatalf("workspace metadata was not indexed: %+v", st.Workspaces)
	}
	if !workspaces.released {
		t.Fatal("workspace lock was not released")
	}
}

func TestRunNextSkipsLockedQueuedJobAndDispatchesLaterSession(t *testing.T) {
	store := newMemoryStore()
	now := time.Date(2026, 7, 3, 12, 30, 0, 0, time.UTC)
	resumeWorkspace := state.WorkspaceMetadata{ID: "ws-locked", Path: "/tmp/ws-locked", Repo: "o/r", CloneURL: "https://github.com/o/r.git", Branch: "issue-spec-ws-locked"}
	seedState(t, store, func(st *state.RunnerState) error {
		if err := st.UpsertWorkspace(resumeWorkspace); err != nil {
			return err
		}
		if err := st.UpsertPublicSession(state.PublicSession{
			Repo:            "o/r",
			PublicSessionID: "ps-locked",
			IssueNumber:     30,
			AcpxRecordID:    "rec-locked",
			CreatorLogin:    "alice",
			Status:          state.StatusRunning,
			Workspace:       resumeWorkspace,
			Lock:            state.SessionLock{OwnerJobID: "job-active"},
			CreatedAt:       now.Add(-time.Hour),
		}); err != nil {
			return err
		}
		if _, _, err := st.CreateCommandJob(state.Job{
			ID:                    "job-locked-old",
			Repo:                  "o/r",
			IssueNumber:           30,
			PublicSessionID:       "ps-locked",
			CoordinatorKind:       "codex",
			Model:                 "gpt-5.5[xhigh]",
			TriggeringUserLogin:   "bob",
			TriggerCommentID:      401,
			CommandID:             "cmd-locked-old",
			CommandName:           "resume",
			CommandPrompt:         "continue locked work",
			CommandIdempotencyKey: "cmd-key-locked-old",
			StatusWritebackKey:    "status-locked-old",
			Status:                state.StatusQueued,
			CreatedAt:             now.Add(-time.Minute),
		}); err != nil {
			return err
		}
		_, _, err := st.CreateCommandJob(state.Job{
			ID:                    "job-new-later",
			Repo:                  "o/r",
			IssueNumber:           30,
			CoordinatorKind:       "codex",
			Model:                 "gpt-5.5[xhigh]",
			SessionCreatorLogin:   "carol",
			TriggeringUserLogin:   "carol",
			TriggerCommentID:      402,
			CommandID:             "cmd-new-later",
			CommandName:           "new",
			CommandPrompt:         "start independent work",
			CommandIdempotencyKey: "cmd-key-new-later",
			StatusWritebackKey:    "status-new-later",
			Status:                state.StatusQueued,
			CreatedAt:             now,
		})
		return err
	})
	workspaces := &fakeWorkspaces{
		binding:      testBinding("ws-new-later"),
		lockedJobIDs: map[string]bool{"job-locked-old": true},
	}
	writebacks := &fakeWriteback{}
	coordinator := &fakeCoordinator{newResult: dispatchResult("ps-new-later", "rec-new-later", "turn-new-later", completedSummary())}
	dispatcher := testDispatcher(store, workspaces, coordinator, writebacks, now)
	dispatcher.PublicSessionID = func() (string, error) { return "ps-new-later", nil }

	result, err := dispatcher.RunNext(context.Background())
	if err != nil {
		t.Fatalf("RunNext returned error: %v", err)
	}
	if !result.Executed || result.JobID != "job-new-later" || result.Status != state.StatusCompleted {
		t.Fatalf("later runnable job was not dispatched: %+v", result)
	}
	if len(coordinator.resumePrompts) != 0 || len(coordinator.newPrompts) != 1 {
		t.Fatalf("unexpected coordinator dispatches: new=%d resume=%d", len(coordinator.newPrompts), len(coordinator.resumePrompts))
	}
	assertWritebackStatuses(t, writebacks, state.StatusRunning, state.StatusCompleted)
	st := loadState(t, store)
	if st.Jobs["job-locked-old"].Status != state.StatusQueued {
		t.Fatalf("locked old job should stay queued: %+v", st.Jobs["job-locked-old"])
	}
	if st.Jobs["job-new-later"].Status != state.StatusCompleted {
		t.Fatalf("later job not completed: %+v", st.Jobs["job-new-later"])
	}
}

func TestSandboxRunnerMirrorsHostGHAuthIntoSandboxConfigDir(t *testing.T) {
	temp := t.TempDir()
	hostGH := filepath.Join(temp, "host-gh")
	workspacePath := filepath.Join(temp, "workspace")
	if err := os.MkdirAll(hostGH, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspacePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hostGH, "hosts.yml"), []byte("github.com:\n  oauth_token: test\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	env, err := (SandboxRunner{Config: sandbox.Config{UnsafeNoSandbox: true, HostGHConfigDir: hostGH}}).Prepare(context.Background(), SandboxRequest{
		WorkspacePath:        workspacePath,
		AcpxWorkingDirectory: workspacePath,
		AcpxBinary:           "acpx",
	})
	if err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}
	sandboxGH := env.Sandbox.TempPaths["GH_CONFIG_DIR"]
	if sandboxGH == "" {
		t.Fatalf("sandbox GH_CONFIG_DIR not recorded: %+v", env.Sandbox)
	}
	if _, err := os.Stat(filepath.Join(sandboxGH, "hosts.yml")); err != nil {
		t.Fatalf("host gh auth was not mirrored into %s: %v", sandboxGH, err)
	}
}

func TestSandboxRunnerUsesRequestRuntimePaths(t *testing.T) {
	temp := t.TempDir()
	hostGH := filepath.Join(temp, "host-gh")
	workspacePath := filepath.Join(temp, "workspace")
	runtimeRoot := filepath.Join(workspacePath, ".sessions", "runtime")
	if err := os.MkdirAll(hostGH, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspacePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hostGH, "hosts.yml"), []byte("github.com:\n  oauth_token: test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	req := SandboxRequest{
		WorkspacePath:        workspacePath,
		AcpxWorkingDirectory: workspacePath,
		AcpxBinary:           "acpx",
		RuntimeHome:          filepath.Join(runtimeRoot, "home"),
		RuntimeGHConfigDir:   filepath.Join(runtimeRoot, "gh"),
		RuntimeXDGConfigHome: filepath.Join(runtimeRoot, "xdg"),
		RuntimeCodexHome:     filepath.Join(runtimeRoot, "codex"),
	}
	runner := SandboxRunner{Config: sandbox.Config{UnsafeNoSandbox: true, HostGHConfigDir: hostGH}}
	first, err := runner.Prepare(context.Background(), req)
	if err != nil {
		t.Fatalf("first Prepare returned error: %v", err)
	}
	second, err := runner.Prepare(context.Background(), req)
	if err != nil {
		t.Fatalf("second Prepare returned error: %v", err)
	}
	for name, want := range map[string]string{
		"HOME":            req.RuntimeHome,
		"GH_CONFIG_DIR":   req.RuntimeGHConfigDir,
		"XDG_CONFIG_HOME": req.RuntimeXDGConfigHome,
		"CODEX_HOME":      req.RuntimeCodexHome,
	} {
		if got := first.Sandbox.TempPaths[name]; got != want {
			t.Fatalf("first %s = %q, want %q", name, got, want)
		}
		if got := second.Sandbox.TempPaths[name]; got != want {
			t.Fatalf("second %s = %q, want %q", name, got, want)
		}
	}
	firstIndex := filepath.Join(first.Sandbox.TempPaths["HOME"], ".acpx", "sessions", "index.json")
	secondIndex := filepath.Join(second.Sandbox.TempPaths["HOME"], ".acpx", "sessions", "index.json")
	if firstIndex != secondIndex {
		t.Fatalf("acpx named session index path changed: first=%q second=%q", firstIndex, secondIndex)
	}
}

func TestSandboxRunnerRefreshesExistingRuntimeGHConfig(t *testing.T) {
	temp := t.TempDir()
	hostGH := filepath.Join(temp, "host-gh")
	runtimeGH := filepath.Join(temp, "workspace", ".sessions", "runtime", "gh")
	workspacePath := filepath.Join(temp, "workspace")
	if err := os.MkdirAll(hostGH, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(runtimeGH, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspacePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hostGH, "hosts.yml"), []byte("github.com:\n  oauth_token: host\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stale := []byte("github.com:\n  oauth_token: stale-runtime\n")
	if err := os.WriteFile(filepath.Join(runtimeGH, "hosts.yml"), stale, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := (SandboxRunner{Config: sandbox.Config{UnsafeNoSandbox: true, HostGHConfigDir: hostGH}}).Prepare(context.Background(), SandboxRequest{
		WorkspacePath:        workspacePath,
		AcpxWorkingDirectory: workspacePath,
		AcpxBinary:           "acpx",
		RuntimeHome:          filepath.Join(temp, "workspace", ".sessions", "runtime", "home"),
		RuntimeGHConfigDir:   runtimeGH,
		RuntimeXDGConfigHome: filepath.Join(temp, "workspace", ".sessions", "runtime", "xdg"),
		RuntimeCodexHome:     filepath.Join(temp, "workspace", ".sessions", "runtime", "codex"),
	})
	if err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(runtimeGH, "hosts.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "github.com:\n  oauth_token: host\n" {
		t.Fatalf("runtime gh hosts.yml was not refreshed from host config: %q", got)
	}
}

func TestSandboxRunnerFailsFastWhenHostGHAuthMissing(t *testing.T) {
	temp := t.TempDir()
	workspacePath := filepath.Join(temp, "workspace")
	if err := os.MkdirAll(workspacePath, 0o700); err != nil {
		t.Fatal(err)
	}

	_, err := (SandboxRunner{Config: sandbox.Config{
		UnsafeNoSandbox: true,
		HostGHConfigDir: filepath.Join(temp, "missing-gh"),
	}}).Prepare(context.Background(), SandboxRequest{
		WorkspacePath:        workspacePath,
		AcpxWorkingDirectory: workspacePath,
		AcpxBinary:           "acpx",
	})
	if err == nil || !strings.Contains(err.Error(), "sandbox gh auth unavailable") || !strings.Contains(err.Error(), "sandbox GH_CONFIG_DIR") {
		t.Fatalf("Prepare error = %v, want exact sandbox auth path failure", err)
	}
}

func TestSandboxedRunnerPreservesAdapterCommandEnv(t *testing.T) {
	temp := t.TempDir()
	cfg := sandbox.Config{
		UnsafeNoSandbox:   true,
		WorkspacePath:     temp,
		TempHome:          filepath.Join(temp, "home"),
		TempGHConfigDir:   filepath.Join(temp, "gh"),
		TempXDGConfigHome: filepath.Join(temp, "xdg"),
		HostEnv:           []string{"PATH=/usr/bin", "UNLISTED_HOST=value", "GH_TOKEN=host-secret", "GITHUB_TOKEN=github-secret", "ISSUE_SPEC_TOKEN=issue-secret"},
		EnvAllowlist:      []string{"PATH"},
	}
	for _, dir := range []string{cfg.TempHome, cfg.TempGHConfigDir, cfg.TempXDGConfigHome} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	runner := &recordingSandboxRunner{}
	_, err := (sandboxedRunner{cfg: cfg, deps: sandbox.Dependencies{Runner: runner}}).Run(context.Background(), acpx.Command{
		Binary: "acpx",
		Env: []string{
			"PATH=/custom/bin",
			"ACPX_CLAUDE_INCLUDE_USER_SETTINGS=1",
			"UNLISTED_HOST=value",
			"GH_TOKEN=command-secret",
			"GITHUB_TOKEN=command-github-secret",
			"ISSUE_SPEC_TOKEN=command-issue-secret",
		},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	env := envEntriesMap(runner.command.Env)
	if env["ACPX_CLAUDE_INCLUDE_USER_SETTINGS"] != "1" || env["PATH"] != "/custom/bin" {
		t.Fatalf("trusted adapter env was not preserved: %v", runner.command.Env)
	}
	for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN", "ISSUE_SPEC_TOKEN"} {
		if _, ok := env[name]; ok {
			t.Fatalf("token env %s leaked into sandbox command: %v", name, runner.command.Env)
		}
	}
	if _, ok := env["UNLISTED_HOST"]; ok {
		t.Fatalf("unchanged host env leaked into sandbox command: %v", runner.command.Env)
	}
}

func testDispatcher(store *memoryStore, workspaces *fakeWorkspaces, coordinator *fakeCoordinator, writebacks *fakeWriteback, now time.Time) *Dispatcher {
	writebacks.store = store
	return &Dispatcher{
		Store:             store,
		Repositories:      fakeRepoResolver{},
		Workspaces:        workspaces,
		Sandbox:           &fakeSandbox{},
		Acpx:              fakeAcpxFactory{coordinator: coordinator},
		Writeback:         writebacks,
		Clock:             fixedClock(now),
		PublicSessionID:   func() (string, error) { return "ps-generated", nil },
		TurnCorrelationID: func() (string, error) { return "turn-generated", nil },
		IssueSpecBinary:   "issue-spec",
	}
}

func seedQueuedJob(t *testing.T, store *memoryStore, job state.Job) {
	t.Helper()
	seedState(t, store, func(st *state.RunnerState) error {
		_, _, err := st.CreateCommandJob(job)
		return err
	})
}

func seedState(t *testing.T, store *memoryStore, mutate func(*state.RunnerState) error) {
	t.Helper()
	if err := store.Update(context.Background(), mutate); err != nil {
		t.Fatal(err)
	}
}

func loadState(t *testing.T, store *memoryStore) state.RunnerState {
	t.Helper()
	st, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func assertWritebackStatuses(t *testing.T, writebacks *fakeWriteback, want ...state.LifecycleStatus) {
	t.Helper()
	if len(writebacks.requests) != len(want) {
		t.Fatalf("writeback count = %d, want %d: %+v", len(writebacks.requests), len(want), writebacks.requests)
	}
	for i, status := range want {
		if writebacks.requests[i].Status != status {
			t.Fatalf("writeback %d status = %s, want %s", i, writebacks.requests[i].Status, status)
		}
	}
}

func testBinding(id string) workspace.Binding {
	path := "/tmp/" + id
	return workspace.Binding{
		Workspace:            state.WorkspaceMetadata{ID: id, Path: path, Repo: "o/r", CloneURL: "https://github.com/o/r.git", Branch: "issue-spec-" + id, Ref: "main"},
		AcpxWorkingDirectory: path,
		SandboxWorkspacePath: path,
	}
}

func completedSummary() runnercontext.CoordinatorSummary {
	return runnercontext.CoordinatorSummary{
		Status: "completed",
		Artifacts: []runnercontext.WorkflowArtifact{{
			Kind:   "typed_comment",
			ID:     "PROCESS-012",
			URL:    "https://github.com/o/r/issues/30#issuecomment-1",
			Action: "updated",
		}},
		Commands: []runnercontext.CLICommandSummary{{
			Name:          "issue-spec comment upsert",
			ExitCode:      0,
			ArtifactID:    "PROCESS-012",
			ArtifactURL:   "https://github.com/o/r/issues/30#issuecomment-1",
			StdoutSummary: "updated PROCESS-012",
		}},
		Processes: []runnercontext.ProcessEvidence{{ProcessID: "PROCESS-012", TaskID: "TASK-016", Status: "done", Evidence: "tests passed"}},
	}
}

func dispatchResult(publicID, recordID, turnID string, summary runnercontext.CoordinatorSummary) acpx.DispatchResult {
	return acpx.DispatchResult{
		PublicSessionID: publicID,
		Metadata: acpx.Metadata{
			StableRecordID:    recordID,
			TrueSessionID:     "true-" + recordID,
			ProviderSessionID: "provider-" + recordID,
			LastTurnID:        turnID,
		},
		Output: acpx.TurnOutput{
			ReplyText:    "done",
			Summary:      summary,
			SummaryFound: true,
		},
	}
}

type memoryStore struct {
	state state.RunnerState
}

func newMemoryStore() *memoryStore {
	return &memoryStore{state: state.NewState()}
}

func (s *memoryStore) Load(context.Context) (state.RunnerState, error) {
	data, err := json.Marshal(s.state)
	if err != nil {
		return state.RunnerState{}, err
	}
	var out state.RunnerState
	if err := json.Unmarshal(data, &out); err != nil {
		return state.RunnerState{}, err
	}
	out.Normalize()
	return out, nil
}

func (s *memoryStore) Update(_ context.Context, mutate func(*state.RunnerState) error) error {
	if mutate == nil {
		return errors.New("mutate is required")
	}
	next, err := s.Load(context.Background())
	if err != nil {
		return err
	}
	if err := mutate(&next); err != nil {
		return err
	}
	next.Normalize()
	s.state = next
	return nil
}

type fakeRepoResolver struct{}

func (fakeRepoResolver) ResolveRepository(context.Context, string) (RepositoryInfo, error) {
	return RepositoryInfo{Repo: "o/r", CloneURL: "https://github.com/o/r.git", DefaultBranch: "main"}, nil
}

type fakeWorkspaces struct {
	binding             workspace.Binding
	err                 error
	lockedJobIDs        map[string]bool
	prepareNewCalled    bool
	resolveResumeCalled bool
	released            bool
}

func (f *fakeWorkspaces) PrepareNew(context.Context, workspace.NewRequest) (workspace.Binding, error) {
	f.prepareNewCalled = true
	if f.err != nil {
		return workspace.Binding{}, f.err
	}
	return f.binding, nil
}

func (f *fakeWorkspaces) ResolveResume(context.Context, workspace.ResumeRequest) (workspace.Binding, error) {
	f.resolveResumeCalled = true
	if f.err != nil {
		return workspace.Binding{}, f.err
	}
	return f.binding, nil
}

func (f *fakeWorkspaces) AcquireLock(_ context.Context, req workspace.LockRequest) (state.SessionLock, error) {
	if f.lockedJobIDs[req.JobID] {
		return state.SessionLock{}, workspace.ErrLocked
	}
	return state.SessionLock{OwnerJobID: req.JobID, WorkspaceLockToken: "token", WorkspaceLockPath: "/tmp/lock"}, nil
}

func (f *fakeWorkspaces) ReleaseLock(state.SessionLock) error {
	f.released = true
	return nil
}

type fakeSandbox struct {
	requests []SandboxRequest
	env      ExecutionEnvironment
	err      error
}

func (f *fakeSandbox) Prepare(_ context.Context, req SandboxRequest) (ExecutionEnvironment, error) {
	f.requests = append(f.requests, req)
	if f.err != nil {
		return ExecutionEnvironment{}, f.err
	}
	if f.env.WorkingDirectory != "" || f.env.Runner != nil || f.env.Sandbox.SandboxProvider != "" {
		env := f.env
		if env.Runner == nil {
			env.Runner = &fakeAuthProbeRunner{}
		}
		return env, nil
	}
	return ExecutionEnvironment{
		WorkingDirectory: "/workspace",
		Sandbox:          state.SandboxMetadata{Enabled: true, SandboxProvider: "bubblewrap", FSBoundary: "workspace"},
		Runner:           &fakeAuthProbeRunner{},
	}, nil
}

type fakeAcpxFactory struct {
	coordinator *fakeCoordinator
}

func (f fakeAcpxFactory) NewCoordinator(ExecutionEnvironment) (Coordinator, error) {
	return f.coordinator, nil
}

type fakeCoordinator struct {
	newResult     acpx.DispatchResult
	resumeResult  acpx.DispatchResult
	newErr        error
	resumeErr     error
	newPrompts    []string
	resumePrompts []string
}

func (f *fakeCoordinator) NewSession(_ context.Context, req acpx.NewSessionRequest) (acpx.DispatchResult, error) {
	f.newPrompts = append(f.newPrompts, req.Prompt)
	return f.newResult, f.newErr
}

func (f *fakeCoordinator) Resume(_ context.Context, req acpx.ResumeRequest) (acpx.DispatchResult, error) {
	f.resumePrompts = append(f.resumePrompts, req.Prompt)
	return f.resumeResult, f.resumeErr
}

type fakeWriteback struct {
	store    *memoryStore
	requests []writeback.Request
}

func (f *fakeWriteback) Write(_ context.Context, req writeback.Request) (writeback.Result, error) {
	f.requests = append(f.requests, req)
	id := req.Job.StatusCommentID
	if id == 0 {
		id = int64(9000 + len(f.requests))
	}
	url := "https://github.com/o/r/issues/30#issuecomment-9001"
	if f.store != nil {
		_ = f.store.Update(context.Background(), func(st *state.RunnerState) error {
			job := st.Jobs[req.Job.ID]
			job.StatusWritebackKey = req.Job.StatusWritebackKey
			job.StatusCommentID = id
			job.StatusCommentURL = url
			job.DispatchIntent.StatusCommentID = id
			return st.UpsertJob(job)
		})
	}
	return writeback.Result{Comment: github.Comment{ID: id, HTMLURL: url}}, nil
}

type fixedClock time.Time

func (c fixedClock) Now() time.Time { return time.Time(c) }

type recordingSandboxRunner struct {
	command sandbox.Command
}

func (r *recordingSandboxRunner) Run(_ context.Context, command sandbox.Command) (sandbox.Result, error) {
	r.command = command
	return sandbox.Result{}, nil
}

type fakeAuthProbeRunner struct {
	commands []acpx.Command
	result   acpx.CommandResult
	err      error
}

func (r *fakeAuthProbeRunner) Run(_ context.Context, command acpx.Command) (acpx.CommandResult, error) {
	r.commands = append(r.commands, command)
	result := r.result
	if result.Stdout == nil && result.Stderr == nil && result.ExitCode == 0 && r.err == nil {
		result.Stdout = []byte(`{"ok":true,"auth":{"host":"github.com","source":"gh","user":"bot"},"backend":{"name":"gh","selection_source":"auto:gh"}}`)
	}
	return result, r.err
}

func envEntriesMap(entries []string) map[string]string {
	out := map[string]string{}
	for _, entry := range entries {
		name, value, ok := strings.Cut(entry, "=")
		if ok {
			out[name] = value
		}
	}
	return out
}

func first(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}
