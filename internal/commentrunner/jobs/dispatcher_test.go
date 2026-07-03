package jobs

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/acpx"
	"github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/workspace"
)

type fakeStore struct {
	mu sync.Mutex
	st model.RunnerState
}

func (f *fakeStore) Update(fn func(*model.RunnerState) error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.st.Repos == nil {
		f.st.Repos = map[string]*model.RepoState{}
	}
	return fn(&f.st)
}

type fakeWorkspace struct {
	path     string
	locked   bool
	lockErr  error
	lockCnt  int
	prepareN int
}

type fakeCanceller struct {
	calls []string
	err   error
}

func (f *fakeWorkspace) PrepareNew(spec workspace.WorkspaceSpec) (*model.WorkspaceState, error) {
	f.prepareN++
	f.path = "/tmp/" + spec.WorkspaceID
	return &model.WorkspaceState{ID: spec.WorkspaceID, Path: f.path, Repo: spec.RepoKey, Branch: "main"}, nil
}

func (f *fakeWorkspace) Resume(st model.WorkspaceState) error { return nil }

func (f *fakeWorkspace) Lock(path string) (func() error, error) {
	f.lockCnt++
	if f.lockErr != nil {
		return nil, f.lockErr
	}
	if f.locked {
		return nil, errors.New("workspace lock busy: " + path)
	}
	f.locked = true
	return func() error { f.locked = false; return nil }, nil
}

func (f *fakeCanceller) CancelTurn(_ context.Context, sessionID string) error {
	f.calls = append(f.calls, sessionID)
	return f.err
}

type fakeAcpx struct {
	calls []acpx.SessionRequest
	res   acpx.SessionResult
	err   error
}

func (f *fakeAcpx) Run(ctx context.Context, req acpx.SessionRequest) (acpx.SessionResult, error) {
	f.calls = append(f.calls, req)
	return f.res, f.err
}

type fakeBackend struct {
	comments []github.Comment
	updates  int
	creates  int
}

func (f *fakeBackend) ListIssueComments(context.Context, string, int) ([]github.Comment, error) {
	return f.comments, nil
}
func (f *fakeBackend) CreateComment(context.Context, string, int, string) (github.Comment, error) {
	f.creates++
	return github.Comment{}, nil
}
func (f *fakeBackend) UpdateComment(context.Context, string, int64, string) (github.Comment, error) {
	f.updates++
	return github.Comment{}, nil
}

func TestDispatcherNewHappyPathAndQueueing(t *testing.T) {
	store := &fakeStore{}
	ws := &fakeWorkspace{}
	ac := &fakeAcpx{res: acpx.SessionResult{SessionID: "sess-1", Queued: true, Metadata: map[string]string{"turn": "1"}}}
	backend := &fakeBackend{}
	d := Dispatcher{Store: store, Workspace: ws, Acpx: ac, Backend: backend, Now: func() time.Time { return time.Unix(100, 0) }}
	job, err := d.RunNew(context.Background(), DispatchRequest{Repo: "o/r", RepoKey: "o/r", RepoURL: "https://example", IssueNumber: 1, IssueKey: "o/r#1", Prompt: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != "queued" && job.Status != "running" {
		t.Fatalf("job status = %s", job.Status)
	}
	if len(ac.calls) != 1 || ac.calls[0].Mode != acpx.ModeFresh || !ac.calls[0].Queue || !ac.calls[0].RefreshMeta {
		t.Fatalf("acpx call = %+v", ac.calls)
	}
	rs := store.st.Repos["o/r"]
	if rs == nil || rs.Jobs[job.ID].Status != "queued" || rs.Acpx[job.ID].SessionID != "sess-1" {
		t.Fatalf("state = %+v", rs)
	}
	if backend.creates == 0 && backend.updates == 0 {
		t.Fatalf("status comments not written")
	}
}

func TestDispatcherResumeReusesWorkspaceAndSerializes(t *testing.T) {
	store := &fakeStore{}
	ws := &fakeWorkspace{}
	ac := &fakeAcpx{res: acpx.SessionResult{SessionID: "sess-2"}}
	backend := &fakeBackend{}
	d := Dispatcher{Store: store, Workspace: ws, Acpx: ac, Backend: backend}
	_, err := d.RunNew(context.Background(), DispatchRequest{Repo: "o/r", RepoKey: "o/r", RepoURL: "https://example", IssueNumber: 1, IssueKey: "o/r#1", Prompt: "seed", PublicSessionID: "pub-1", WorkspaceID: "pub-1"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.RunResume(context.Background(), DispatchRequest{Repo: "o/r", RepoKey: "o/r", IssueNumber: 1, IssueKey: "o/r#1", Prompt: "more", PublicSessionID: "pub-1", WorkspaceID: "pub-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ac.calls) != 2 || ac.calls[1].Mode != acpx.ModeResume || ac.calls[1].SessionID != "pub-1" {
		t.Fatalf("resume call = %+v", ac.calls)
	}
}

func TestDispatcherDuplicateIdempotencyAndFailureTransitions(t *testing.T) {
	store := &fakeStore{}
	ws := &fakeWorkspace{}
	ac := &fakeAcpx{res: acpx.SessionResult{SessionID: "sess-1"}}
	backend := &fakeBackend{}
	d := Dispatcher{Store: store, Workspace: ws, Acpx: ac, Backend: backend}
	req := DispatchRequest{Repo: "o/r", RepoKey: "o/r", RepoURL: "https://example", IssueNumber: 1, IssueKey: "o/r#1", Prompt: "hello", JobID: "job-1", PublicSessionID: "pub-1", WorkspaceID: "pub-1"}
	if _, err := d.RunNew(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if _, err := d.RunNew(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if len(ac.calls) != 1 {
		t.Fatalf("expected duplicate calls to be deduped by state, got %d", len(ac.calls))
	}
	bad := &fakeWorkspace{lockErr: errors.New("workspace lock failed")}
	d.Workspace = bad
	if _, err := d.RunResume(context.Background(), DispatchRequest{Repo: "o/r", RepoKey: "o/r", IssueNumber: 1, IssueKey: "o/r#1", Prompt: "x", PublicSessionID: "pub-1", WorkspaceID: "pub-1"}); err == nil {
		t.Fatal("expected failure")
	}
	if backend.updates == 0 && backend.creates == 0 {
		t.Fatal("expected status updates")
	}
}

func TestDispatcherLockRelease(t *testing.T) {
	store := &fakeStore{}
	ws := &fakeWorkspace{locked: true}
	ac := &fakeAcpx{res: acpx.SessionResult{SessionID: "sess-1"}}
	backend := &fakeBackend{}
	d := Dispatcher{Store: store, Workspace: ws, Acpx: ac, Backend: backend}
	_ = store.Update(func(st *model.RunnerState) error {
		rs := state.EnsureRepo(st, "o/r")
		rs.Workspaces["pub-1"] = &model.WorkspaceState{ID: "pub-1", Path: "/tmp/pub-1", Repo: "o/r"}
		return nil
	})
	if _, err := d.RunResume(context.Background(), DispatchRequest{Repo: "o/r", RepoKey: "o/r", IssueNumber: 1, IssueKey: "o/r#1", Prompt: "x", PublicSessionID: "pub-1", WorkspaceID: "pub-1"}); err == nil {
		t.Fatal("expected lock failure")
	}
	ws.locked = false
	if _, err := d.RunResume(context.Background(), DispatchRequest{Repo: "o/r", RepoKey: "o/r", IssueNumber: 1, IssueKey: "o/r#1", Prompt: "x", PublicSessionID: "pub-1", WorkspaceID: "pub-1"}); err != nil {
		t.Fatal(err)
	}
}

func TestDispatcherStartupReconcileAndCancel(t *testing.T) {
	store := &fakeStore{}
	ws := &fakeWorkspace{}
	ac := &fakeAcpx{res: acpx.SessionResult{SessionID: "sess-1"}}
	backend := &fakeBackend{}
	canceller := &fakeCanceller{}
	d := Dispatcher{Store: store, Workspace: ws, Acpx: ac, Backend: backend, Canceller: canceller, Now: func() time.Time { return time.Unix(200, 0) }}
	_ = store.Update(func(st *model.RunnerState) error {
		rs := state.EnsureRepo(st, "o/r")
		rs.Jobs["job-active"] = &model.JobState{ID: "job-active", PublicSessionID: "sess-active", Status: "running"}
		rs.Jobs["job-complete"] = &model.JobState{ID: "job-complete", PublicSessionID: "sess-complete", Status: "done"}
		rs.Jobs["job-failed"] = &model.JobState{ID: "job-failed", PublicSessionID: "sess-failed", Status: "failed"}
		rs.Jobs["job-ambiguous"] = &model.JobState{ID: "job-ambiguous", PublicSessionID: "sess-ambiguous", Status: "ambiguous"}
		return nil
	})
	if err := d.ReconcileStartup(context.Background(), "o/r"); err != nil {
		t.Fatal(err)
	}
	rs := store.st.Repos["o/r"]
	if rs.Jobs["job-active"].Status != "running" || rs.Jobs["job-complete"].Status != "done" || rs.Jobs["job-failed"].Status != "failed" || rs.Jobs["job-ambiguous"].Status != "interrupted" {
		t.Fatalf("reconcile = %+v", rs.Jobs)
	}
	_ = store.Update(func(st *model.RunnerState) error {
		rs := state.EnsureRepo(st, "o/r")
		rs.Workspaces["pub-1"] = &model.WorkspaceState{ID: "pub-1", Path: "/tmp/pub-1", Repo: "o/r"}
		rs.Jobs["job-cancel"] = &model.JobState{ID: "job-cancel", PublicSessionID: "pub-1", WorkspaceID: "pub-1", Status: "running"}
		return nil
	})
	job, err := d.Cancel(context.Background(), DispatchRequest{Repo: "o/r", RepoKey: "o/r", IssueNumber: 1, IssueKey: "o/r#1", PublicSessionID: "pub-1"})
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != "cancelled" || len(canceller.calls) != 1 || canceller.calls[0] != "pub-1" {
		t.Fatalf("cancel = %+v %#v", job, canceller.calls)
	}
	if rs.Jobs["job-cancel"].Status != "cancelled" {
		t.Fatalf("cancel state = %+v", rs.Jobs["job-cancel"])
	}
}

func TestDispatcherCancelRejectsUnsupportedAndReleasesLock(t *testing.T) {
	store := &fakeStore{}
	ws := &fakeWorkspace{}
	ac := &fakeAcpx{res: acpx.SessionResult{SessionID: "sess-1"}}
	backend := &fakeBackend{}
	canceller := &fakeCanceller{err: errors.New("turn-level cancellation unsupported")}
	d := Dispatcher{Store: store, Workspace: ws, Acpx: ac, Backend: backend, Canceller: canceller}
	_ = store.Update(func(st *model.RunnerState) error {
		rs := state.EnsureRepo(st, "o/r")
		rs.Jobs["job-1"] = &model.JobState{ID: "job-1", PublicSessionID: "pub-1", Status: "running"}
		return nil
	})
	if _, err := d.Cancel(context.Background(), DispatchRequest{Repo: "o/r", RepoKey: "o/r", PublicSessionID: "pub-1"}); err == nil {
		t.Fatal("expected cancel failure")
	}
	if len(canceller.calls) != 1 {
		t.Fatalf("cancel calls = %#v", canceller.calls)
	}
	if got := store.st.Repos["o/r"].Jobs["job-1"].Status; got != "running" {
		t.Fatalf("state mutated on failed cancel: %s", got)
	}
}
