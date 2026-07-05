package writeback

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/templates"
)

func TestWritebackCreatesThenUpdatesTrackedComment(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(100, 0).UTC()
	store := newMemoryStore()
	job := testJob()
	mustUpdate(t, store, func(st *state.RunnerState) error { return st.UpsertJob(job) })
	ops := &fakeRunnerOps{createID: 501}
	service := &Service{GitHub: ops, Store: store, Clock: func() time.Time { return now }}

	created, err := service.Write(ctx, Request{Job: job, Status: state.StatusQueued, Phase: "queued"})
	if err != nil {
		t.Fatal(err)
	}
	if !created.Created || created.Comment.ID != 501 || ops.creates != 1 {
		t.Fatalf("create result=%+v creates=%d", created, ops.creates)
	}
	if !strings.Contains(created.Body, "| Status | `queued` |") ||
		!strings.Contains(created.Body, "| Phase | `queued` |") ||
		!strings.Contains(created.Body, "| Public session | `s_123` |") {
		t.Fatalf("created body missing concise status fields:\n%s", created.Body)
	}
	if strings.Contains(created.Body, "| Runner job |") || strings.Contains(created.Body, "| Trigger comment |") {
		t.Fatalf("created body leaked runner metadata:\n%s", created.Body)
	}

	updated, err := service.Write(ctx, Request{Job: job, Status: state.StatusRunning, Phase: "running"})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Created || !updated.Updated || ops.creates != 1 || ops.updates != 1 || ops.lastUpdateID != 501 {
		t.Fatalf("update result=%+v creates=%d updates=%d updateID=%d", updated, ops.creates, ops.updates, ops.lastUpdateID)
	}
	loaded := store.snapshot()
	writeback, ok := loaded.FindStatusWriteback(job.StatusWritebackKey)
	if !ok || writeback.CommentID != 501 || writeback.Status != state.StatusRunning || writeback.UpdatedAt != now {
		t.Fatalf("writeback not persisted: %+v ok=%v", writeback, ok)
	}
	storedJob := loaded.Jobs[job.ID]
	if storedJob.StatusCommentID != 501 || storedJob.DispatchIntent.StatusCommentID != 501 {
		t.Fatalf("job status comment id not integrated: %+v", storedJob)
	}
}

func TestWritebackRecoversMarkedCommentBeforeCreate(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	job := testJob()
	job.StatusCommentID = 0
	mustUpdate(t, store, func(st *state.RunnerState) error { return st.UpsertJob(job) })
	existingBody, err := templates.RenderRunnerStatusComment(templates.RunnerStatusComment{
		Marker: templates.RunnerStatusMarker{
			SchemaVersion:      templates.RunnerStatusMarkerSchemaVersion,
			StatusWritebackKey: job.StatusWritebackKey,
			JobID:              job.ID,
			PublicSessionID:    job.PublicSessionID,
			TriggerCommentID:   job.TriggerCommentID,
		},
		Status: "queued",
	})
	if err != nil {
		t.Fatal(err)
	}
	ops := &fakeRunnerOps{
		createID: 900,
		comments: []github.Comment{{
			ID:      777,
			HTMLURL: "https://github.com/o/r/issues/30#issuecomment-777",
			Body:    existingBody,
		}},
	}
	service := &Service{GitHub: ops, Store: store, Clock: func() time.Time { return time.Unix(200, 0).UTC() }}

	result, err := service.Write(ctx, Request{Job: job, Status: state.StatusCompleted, Phase: "completed"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Recovered || result.Created || ops.creates != 0 || ops.updates != 1 || ops.lastUpdateID != 777 {
		t.Fatalf("expected recovered update: result=%+v creates=%d updates=%d updateID=%d", result, ops.creates, ops.updates, ops.lastUpdateID)
	}
	loaded := store.snapshot()
	writeback, ok := loaded.FindStatusWriteback(job.StatusWritebackKey)
	if !ok || writeback.CommentID != 777 || writeback.URL == "" {
		t.Fatalf("recovered id not persisted: %+v ok=%v", writeback, ok)
	}
}

func TestWritebackRecordsBackendFailureWithoutDuplicateCreate(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	job := testJob()
	ops := &fakeRunnerOps{
		createID:  501,
		createErr: errors.New("HTTP 403 secondary rate limit"),
		createMeta: github.ResponseMetadata{
			StatusCode: http.StatusForbidden,
			RateLimit:  github.RateLimitMetadata{RetryAfterSeconds: 3},
		},
	}
	service := &Service{GitHub: ops, Store: store, Clock: func() time.Time { return time.Unix(300, 0).UTC() }}

	result, err := service.Write(ctx, Request{Job: job, Status: state.StatusFailed, Phase: "create-status"})
	if err == nil {
		t.Fatal("expected create error")
	}
	if result.Metadata.RateLimit.RetryAfterSeconds != 3 || ops.creates != 1 {
		t.Fatalf("metadata/result not preserved: result=%+v creates=%d", result, ops.creates)
	}
	loaded := store.snapshot()
	writeback, ok := loaded.FindStatusWriteback(job.StatusWritebackKey)
	if !ok || !strings.Contains(writeback.LastError, "secondary rate limit") || writeback.CommentID != 0 {
		t.Fatalf("failure not persisted safely: %+v ok=%v", writeback, ok)
	}
}

func TestWritebackParsesCoordinatorReplyBody(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	job := testJob()
	ops := &fakeRunnerOps{createID: 501}
	service := &Service{GitHub: ops, Store: store, Clock: func() time.Time { return time.Unix(400, 0).UTC() }}
	reply := "done\n```issue_spec_coordinator_summary\n" + `{
  "status": "completed",
  "artifacts": [{"kind": "typed_comment", "id": "PROCESS-001", "url": "https://github.com/o/r/issues/30#issuecomment-1", "action": "updated"}],
  "commands": [{"name": "issue-spec comment upsert", "exit_code": 0, "artifact_id": "PROCESS-001"}],
  "children": [{"id": "child-1", "role": "worker", "process_id": "PROCESS-001", "status": "done"}]
}` + "\n```"

	result, err := service.Write(ctx, Request{Job: job, Status: state.StatusCompleted, CoordinatorReplyBody: reply})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"## Result",
		"Completed the requested command.",
		"updated typed_comment PROCESS-001: https://github.com/o/r/issues/30#issuecomment-1",
	} {
		if !strings.Contains(result.Body, want) {
			t.Fatalf("coordinator result missing %q:\n%s", want, result.Body)
		}
	}
	for _, forbidden := range []string{"Coordinator CLI command", "Child provenance", "issue-spec comment upsert", "child-1"} {
		if strings.Contains(result.Body, forbidden) {
			t.Fatalf("coordinator details leaked %q:\n%s", forbidden, result.Body)
		}
	}
}

func testJob() state.Job {
	return state.Job{
		ID:                    "job-1",
		Repo:                  "o/r",
		IssueNumber:           30,
		PublicSessionID:       "s_123",
		CoordinatorKind:       "native-codex",
		Model:                 "gpt-5.5",
		SessionCreatorLogin:   "alice",
		TriggeringUserLogin:   "bob",
		TriggerCommentID:      101,
		CommandIdempotencyKey: "cmd:o/r:101",
		StatusWritebackKey:    "status:o/r:101",
		Status:                state.StatusQueued,
		Sandbox:               state.SandboxMetadata{SandboxProvider: "bwrap", FSBoundary: "workspace"},
		CLIDirect: []state.CLIDirectProvenance{{
			CommandName:   "issue-spec comment upsert",
			ExitCode:      0,
			StdoutSummary: "updated PROCESS",
			ArtifactRefs:  []state.ArtifactRef{{ID: "PROCESS-001", URL: "https://github.com/o/r/issues/30#issuecomment-1"}},
		}},
	}
}

type memoryStore struct {
	mu    sync.Mutex
	state state.RunnerState
}

func newMemoryStore() *memoryStore {
	return &memoryStore{state: state.NewState()}
}

func (s *memoryStore) Update(ctx context.Context, mutate func(*state.RunnerState) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := mutate(&s.state); err != nil {
		return err
	}
	s.state.Normalize()
	return nil
}

func (s *memoryStore) snapshot() state.RunnerState {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Normalize()
	return s.state
}

func mustUpdate(t *testing.T, store *memoryStore, mutate func(*state.RunnerState) error) {
	t.Helper()
	if err := store.Update(context.Background(), mutate); err != nil {
		t.Fatal(err)
	}
}

type fakeRunnerOps struct {
	comments     []github.Comment
	createID     int64
	createErr    error
	updateErr    error
	listErr      error
	createMeta   github.ResponseMetadata
	updateMeta   github.ResponseMetadata
	creates      int
	updates      int
	lists        int
	lastCreate   string
	lastUpdate   string
	lastUpdateID int64
}

func (f *fakeRunnerOps) ListIssueCommentsPage(context.Context, string, int, github.CommentListOptions) (github.IssueCommentsResult, error) {
	f.lists++
	if f.listErr != nil {
		return github.IssueCommentsResult{}, f.listErr
	}
	return github.IssueCommentsResult{Comments: f.comments}, nil
}

func (f *fakeRunnerOps) CreateRunnerComment(_ context.Context, _ string, issueNumber int, body string) (github.RunnerCommentResult, error) {
	f.creates++
	f.lastCreate = body
	if f.createErr != nil {
		return github.RunnerCommentResult{Metadata: f.createMeta}, f.createErr
	}
	id := f.createID
	if id == 0 {
		id = 1
	}
	return github.RunnerCommentResult{
		Comment:  github.Comment{ID: id, IssueNumber: issueNumber, HTMLURL: "https://github.com/o/r/issues/30#issuecomment-501", Body: body},
		Metadata: f.createMeta,
	}, nil
}

func (f *fakeRunnerOps) UpdateRunnerComment(_ context.Context, _ string, commentID int64, body string) (github.RunnerCommentResult, error) {
	f.updates++
	f.lastUpdateID = commentID
	f.lastUpdate = body
	if f.updateErr != nil {
		return github.RunnerCommentResult{Metadata: f.updateMeta}, f.updateErr
	}
	return github.RunnerCommentResult{
		Comment:  github.Comment{ID: commentID, IssueNumber: 30, HTMLURL: "https://github.com/o/r/issues/30#issuecomment-501", Body: body},
		Metadata: f.updateMeta,
	}, nil
}

func (f *fakeRunnerOps) AddCommentReaction(context.Context, string, int64, string) (github.RunnerReactionResult, error) {
	return github.RunnerReactionResult{}, nil
}

func (f *fakeRunnerOps) PollNotifications(context.Context, github.NotificationListOptions) (github.NotificationListResult, error) {
	return github.NotificationListResult{}, nil
}

func (f *fakeRunnerOps) GetRepositorySubscription(context.Context, string) (github.RepositorySubscriptionResult, error) {
	return github.RepositorySubscriptionResult{}, nil
}

func (f *fakeRunnerOps) GetIssueContext(context.Context, string, int, github.ConditionalRequest) (github.IssueContextResult, error) {
	return github.IssueContextResult{}, nil
}

func (f *fakeRunnerOps) ListRepositoryIssueCommentsPage(context.Context, string, github.CommentListOptions) (github.IssueCommentsResult, error) {
	return github.IssueCommentsResult{}, nil
}

func (f *fakeRunnerOps) ListCommentReactionsPage(context.Context, string, int64, github.RunnerPageOptions) (github.CommentReactionsResult, error) {
	return github.CommentReactionsResult{}, nil
}

func (f *fakeRunnerOps) GetCollaboratorPermission(context.Context, string, string) (github.CollaboratorPermissionResult, error) {
	return github.CollaboratorPermissionResult{}, nil
}

var _ github.RunnerOperations = (*fakeRunnerOps)(nil)
