package state

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFileStoreCreateUpdateListReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runner-state.json")
	now := time.Unix(1000, 0).UTC()

	store, err := OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	err = store.Update(context.Background(), func(st *RunnerState) error {
		seen := SeenComment{
			Repo:                     "o/r",
			IssueNumber:              30,
			CommentID:                101,
			AuthorLogin:              "alice",
			FirstObservedAt:          now,
			FirstObservedBodyHash:    "sha256:command",
			ProducedCommandCandidate: true,
			CommandIdempotencyKey:    "cmd:o/r:101",
		}
		job, created, err := st.CreateCommandJob(Job{
			ID:                    "job-1",
			Repo:                  "o/r",
			IssueNumber:           30,
			PublicSessionID:       "ps-1",
			AcpxRecordID:          "acpx-record-1",
			CoordinatorKind:       "native-codex",
			Model:                 "gpt-5.5",
			SessionCreatorLogin:   "alice",
			TriggeringUserLogin:   "alice",
			TriggerCommentID:      101,
			CommandName:           "new",
			CommandIdempotencyKey: "cmd:o/r:101",
			StatusWritebackKey:    "status:o/r:101",
			Status:                StatusQueued,
			CreatedAt:             now,
			FirstObservedComment:  seen,
			ContextBundle:         ContextBundleProvenance{SchemaVersion: 1, Hash: "sha256:bundle", PromptBytes: 12},
			DispatchIntent:        DispatchIntent{RunnerJobID: "job-1", PublicSessionID: "ps-1", AcpxRecordID: "acpx-record-1", ContextBundleHash: "sha256:bundle"},
			Workspace:             WorkspaceMetadata{ID: "ws-1", Path: "/work/o-r-ps-1", Repo: "o/r", CloneURL: "https://github.com/o/r.git", Branch: "main"},
			Sandbox:               SandboxMetadata{Enabled: true, SandboxProvider: "bwrap", FSBoundary: "workspace"},
			Acpx:                  AcpxMetadata{StableRecordID: "acpx-record-1", TrueSessionID: "session-1"},
			CLIDirect:             []CLIDirectProvenance{{CommandName: "issue-spec", Backend: "gh", ExitCode: 0, StdoutSummary: "updated PROCESS"}},
		})
		if err != nil {
			return err
		}
		if !created || job.ID != "job-1" {
			t.Fatalf("unexpected create result: job=%+v created=%v", job, created)
		}
		if err := st.UpsertPublicSession(PublicSession{
			Repo:            "o/r",
			PublicSessionID: "ps-1",
			IssueNumber:     30,
			AcpxRecordID:    "acpx-record-1",
			CreatorLogin:    "alice",
			Status:          StatusRunning,
			Workspace:       WorkspaceMetadata{ID: "ws-1", Path: "/work/o-r-ps-1", Repo: "o/r", CloneURL: "https://github.com/o/r.git", Branch: "main"},
			Queue:           SessionQueue{PendingJobIDs: []string{"job-1"}, AcceptedSequence: 1},
			Lock:            SessionLock{OwnerJobID: "job-1", WorkspaceLockToken: "lock-token"},
		}); err != nil {
			return err
		}
		st.Workspaces["ws-1"] = WorkspaceMetadata{ID: "ws-1", Path: "/work/o-r-ps-1", Repo: "o/r"}
		st.Repositories["github.com/o/r"] = RepositoryState{
			Host:                "github.com",
			Repo:                "o/r",
			Backend:             "gh",
			NotificationCursor:  CursorState{Resource: "notifications", ETag: `"etag"`, XPollIntervalSeconds: 60},
			IssueCommentCursors: map[string]CursorState{"30": {LastSeenID: 101}},
			RateLimit:           RateLimitState{Remaining: 4999},
		}
		return st.UpsertStatusWriteback(StatusWriteback{
			IdempotencyKey:   "status:o/r:101",
			JobID:            "job-1",
			Repo:             "o/r",
			IssueNumber:      30,
			TriggerCommentID: 101,
			CommentID:        501,
			Status:           StatusQueued,
			UpdatedAt:        now,
		})
	})
	if err != nil {
		t.Fatal(err)
	}

	loaded, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.ListJobs(); len(got) != 1 || got[0].ID != "job-1" {
		t.Fatalf("unexpected jobs: %+v", got)
	}
	if job, ok := loaded.FindCommandJob("cmd:o/r:101"); !ok || job.Status != StatusQueued {
		t.Fatalf("missing idempotent job lookup: %+v ok=%v", job, ok)
	}
	if session, ok := loaded.GetPublicSession("o/r", "ps-1"); !ok || session.AcpxRecordID != "acpx-record-1" || session.Lock.OwnerJobID != "job-1" {
		t.Fatalf("missing public session: %+v ok=%v", session, ok)
	}
	if writeback, ok := loaded.FindStatusWriteback("status:o/r:101"); !ok || writeback.CommentID != 501 {
		t.Fatalf("missing status writeback: %+v ok=%v", writeback, ok)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "seen_comments") {
		t.Fatalf("main state JSON retained seen_comments:\n%s", string(data))
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reloaded, err := reopened.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if job, ok := reloaded.FindCommandJob("cmd:o/r:101"); !ok || job.FirstObservedComment.CommentID != 101 {
		t.Fatalf("job provenance did not reload: job=%+v ok=%v", job, ok)
	}
}

func TestLoadFileStripsLegacySeenCommentsOnSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runner-state.json")
	legacy := `{
  "schema_version": 1,
  "seen_comments": {
    "o/r#123": {
      "repo": "o/r",
      "comment_id": 123,
      "first_observed_body_hash": "sha256:legacy"
    }
  },
  "jobs": {}
}
`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveFile(path, state); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "seen_comments") {
		t.Fatalf("legacy seen_comments was retained after save:\n%s", string(data))
	}
}

func TestLoadFileStripsLegacyHeartbeatFieldsOnSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runner-state.json")
	legacy := `{
  "schema_version": 1,
  "public_sessions": {
    "o/r#ps-legacy": {
      "repo": "o/r",
      "public_session_id": "ps-legacy",
      "acpx_record_id": "rec-legacy",
      "status": "running",
      "queue": {
        "pending_job_ids": ["job-legacy"],
        "accepted_sequence": 7,
        "heartbeat_at": "2026-07-03T10:00:00Z"
      },
      "lock": {
        "owner_job_id": "job-legacy",
        "acquired_at": "2026-07-03T10:00:00Z",
        "heartbeat_at": "2026-07-03T10:01:00Z",
        "workspace_lock_token": "token-legacy",
        "workspace_lock_path": "/tmp/lock-legacy",
        "stale_recovered_at": "2026-07-03T10:02:00Z"
      }
    }
  },
  "jobs": {}
}
`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	session, ok := state.GetPublicSession("o/r", "ps-legacy")
	if !ok || session.Queue.AcceptedSequence != 7 || session.Lock.OwnerJobID != "job-legacy" || session.Lock.StaleRecoveredAt.IsZero() {
		t.Fatalf("legacy session did not load: %+v ok=%v", session, ok)
	}
	if err := SaveFile(path, state); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "heartbeat_at") {
		t.Fatalf("legacy heartbeat_at was retained after save:\n%s", string(data))
	}
}

func TestMissingAndCorruptFileBehavior(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")
	state, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if state.SchemaVersion != SchemaVersion || state.Jobs == nil || len(state.Jobs) != 0 {
		t.Fatalf("missing file did not return empty normalized state: %+v", state)
	}

	corruptPath := filepath.Join(t.TempDir(), "corrupt.json")
	if err := os.WriteFile(corruptPath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = LoadFile(corruptPath)
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("expected corrupt error, got %v", err)
	}
	if err == nil || !errors.As(err, new(*CorruptStateError)) {
		t.Fatalf("expected typed corrupt diagnostic, got %T", err)
	}
}

func TestLockContention(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	first, err := OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	second, err := OpenFileStore(path)
	if err == nil {
		second.Close()
		t.Fatal("expected second open to fail while lock is held")
	}
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("expected lock error, got %v", err)
	}
}

func TestFileStoreRecoversStaleLockFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	lockPath := path + ".lock"
	if err := os.WriteFile(lockPath, []byte("pid=1\ncreated_at=2000-01-01T00:00:00Z\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenFileStore(path)
	if err != nil {
		t.Fatalf("stale lock file blocked open: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected lock file to be removed on close, got %v", err)
	}
}

func TestIdempotencyLookupSurvivesDuplicateAndTerminalStates(t *testing.T) {
	state := NewState()
	job, created, err := state.CreateCommandJob(Job{ID: "job-1", Repo: "o/r", CommandIdempotencyKey: "cmd:1", Status: StatusRunning})
	if err != nil || !created {
		t.Fatalf("job create failed: job=%+v created=%v err=%v", job, created, err)
	}
	duplicate, created, err := state.CreateCommandJob(Job{ID: "job-2", Repo: "o/r", CommandIdempotencyKey: "cmd:1", Status: StatusQueued})
	if err != nil || created || duplicate.ID != "job-1" {
		t.Fatalf("duplicate command did not return existing job: job=%+v created=%v err=%v", duplicate, created, err)
	}
	if _, err := state.UpdateJobStatus("job-1", StatusCompleted, time.Unix(10, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	if found, ok := state.FindCommandJob("cmd:1"); !ok || found.Status != StatusCompleted {
		t.Fatalf("terminal job missing from idempotency lookup: %+v ok=%v", found, ok)
	}
	if err := state.UpsertCancellation(Cancellation{ID: "cancel-1", IdempotencyKey: "cancel:1", TargetJobID: "job-1", Status: StatusCancelled}); err != nil {
		t.Fatal(err)
	}
	if cancel, ok := state.FindCancellation("cancel:1"); !ok || cancel.TargetJobID != "job-1" {
		t.Fatalf("missing cancellation lookup: %+v ok=%v", cancel, ok)
	}
	if err := state.UpsertStatusWriteback(StatusWriteback{IdempotencyKey: "status:1", JobID: "job-1", Status: StatusCompleted}); err != nil {
		t.Fatal(err)
	}
	if writeback, ok := state.FindStatusWriteback("status:1"); !ok || writeback.Status != StatusCompleted {
		t.Fatalf("missing status lookup: %+v ok=%v", writeback, ok)
	}
}

func TestLifecycleTransitionsAndReconciliationAPI(t *testing.T) {
	state := NewState()
	if _, _, err := state.CreateCommandJob(Job{ID: "job-1", CommandIdempotencyKey: "cmd:1", Status: StatusQueued}); err != nil {
		t.Fatal(err)
	}
	if _, err := state.UpdateJobStatus("job-1", StatusDispatched, time.Unix(1, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	if jobs := state.JobsForReconciliation(); len(jobs) != 1 || jobs[0].ID != "job-1" {
		t.Fatalf("expected dispatched job to need reconciliation: %+v", jobs)
	}
	if _, err := state.UpdateJobStatus("job-1", StatusRunning, time.Unix(2, 0).UTC(), "started"); err != nil {
		t.Fatal(err)
	}
	if jobs := state.JobsForReconciliation(); len(jobs) != 1 || jobs[0].Status != StatusRunning {
		t.Fatalf("expected running job to need reconciliation: %+v", jobs)
	}
	if _, err := state.UpdateJobStatus("job-1", StatusCompleted, time.Unix(3, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	if jobs := state.JobsForReconciliation(); len(jobs) != 0 {
		t.Fatalf("terminal job should not need reconciliation: %+v", jobs)
	}
	if _, err := state.UpdateJobStatus("job-1", StatusRunning, time.Unix(4, 0).UTC()); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("expected invalid transition error, got %v", err)
	}
}

func TestWorkspaceMetadataHelpers(t *testing.T) {
	state := NewState()
	now := time.Unix(20, 0).UTC()
	workspace := WorkspaceMetadata{
		ID:              "ws-1",
		Path:            "/work/ws-1",
		Repo:            "o/r",
		CloneURL:        "https://github.com/o/r.git",
		Branch:          "issue-spec-ws-1",
		Ref:             "main",
		CheckoutSHA:     "abc123",
		RetentionPolicy: "delete_after_last_used=1h0m0s",
		CleanupAfter:    now.Add(time.Hour),
	}
	if err := state.UpsertWorkspace(workspace); err != nil {
		t.Fatal(err)
	}
	if got, ok := state.GetWorkspace("ws-1"); !ok || got.Path != "/work/ws-1" {
		t.Fatalf("workspace not indexed: %+v ok=%v", got, ok)
	}
	if err := state.UpsertWorkspace(WorkspaceMetadata{ID: "missing"}); err == nil {
		t.Fatal("expected incomplete workspace metadata to be rejected")
	}
}

func TestSchemaFriendlyZeroValues(t *testing.T) {
	var state RunnerState
	state.Normalize()
	if state.SchemaVersion != SchemaVersion {
		t.Fatalf("schema version = %d", state.SchemaVersion)
	}
	if state.Repositories == nil || state.Jobs == nil || state.Idempotency.CommandJobs == nil {
		t.Fatalf("normalize did not initialize maps: %+v", state)
	}
	if err := state.UpsertJob(Job{ID: "job-zero"}); err != nil {
		t.Fatal(err)
	}
	if state.Jobs["job-zero"].Status != StatusQueued {
		t.Fatalf("zero-value job did not default to queued: %+v", state.Jobs["job-zero"])
	}
	path := filepath.Join(t.TempDir(), "zero.json")
	if err := SaveFile(path, state); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Idempotency.CommandJobs == nil || reloaded.StatusWritebacks == nil {
		t.Fatalf("reloaded state was not normalized: %+v", reloaded)
	}
}
