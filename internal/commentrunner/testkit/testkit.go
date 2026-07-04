package testkit

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/higress-group/issue-spec/internal/acpx"
	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/commentrunner"
	runnercontext "github.com/higress-group/issue-spec/internal/commentrunner/context"
	"github.com/higress-group/issue-spec/internal/commentrunner/jobs"
	crstate "github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/commentrunner/writeback"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/workspace"
)

var Now = time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)

type Clock struct {
	Time time.Time
}

func (c Clock) Now() time.Time {
	if c.Time.IsZero() {
		return Now
	}
	return c.Time
}

type MemoryStore struct {
	mu    sync.Mutex
	State crstate.RunnerState
	Saves int
}

func NewMemoryStore(initial ...crstate.RunnerState) *MemoryStore {
	st := crstate.NewState()
	if len(initial) > 0 {
		st = initial[0]
		st.Normalize()
	}
	return &MemoryStore{State: st}
}

func (s *MemoryStore) Load(context.Context) (crstate.RunnerState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneState(s.State)
}

func (s *MemoryStore) Save(_ context.Context, st crstate.RunnerState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next, err := cloneState(st)
	if err != nil {
		return err
	}
	s.State = next
	s.Saves++
	return nil
}

func (s *MemoryStore) Update(_ context.Context, mutate func(*crstate.RunnerState) error) error {
	if mutate == nil {
		return errors.New("mutate is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next, err := cloneState(s.State)
	if err != nil {
		return err
	}
	if err := mutate(&next); err != nil {
		return err
	}
	next.Normalize()
	s.State = next
	return nil
}

func (s *MemoryStore) Snapshot() crstate.RunnerState {
	st, err := s.Load(context.Background())
	if err != nil {
		panic(err)
	}
	return st
}

func cloneState(st crstate.RunnerState) (crstate.RunnerState, error) {
	st.Normalize()
	data, err := json.Marshal(st)
	if err != nil {
		return crstate.RunnerState{}, err
	}
	var out crstate.RunnerState
	if err := json.Unmarshal(data, &out); err != nil {
		return crstate.RunnerState{}, err
	}
	out.Normalize()
	return out, nil
}

type RunnerBackend struct {
	User                     github.User
	Scopes                   []string
	Permissions              map[string]string
	DefaultPermission        string
	Notifications            github.NotificationListResult
	IssueComments            map[int]github.IssueCommentsResult
	RepositoryComments       github.IssueCommentsResult
	IssueContext             github.IssueContextResult
	Subscription             github.RepositorySubscriptionResult
	NotificationOptions      []github.NotificationListOptions
	IssueCommentOptions      []github.CommentListOptions
	RepositoryCommentOptions []github.CommentListOptions
	CollaboratorLookups      []string
	CreatedRunnerComments    []github.Comment
	UpdatedRunnerComments    []github.Comment
	CommentReactions         []CommentReaction
	PermissionLookupErrFor   string
	NotificationErr          error
	IssueCommentsErr         error
	RepositoryCommentsErr    error
	CreateRunnerCommentErr   error
	UpdateRunnerCommentErr   error
	AddCommentReactionErr    error
	NextRunnerCommentID      int64
}

type CommentReaction struct {
	Repo      string
	CommentID int64
	Content   string
}

func (b *RunnerBackend) GetUser(context.Context) (github.User, []string, error) {
	if b.User.Login == "" {
		b.User.Login = "bot"
	}
	return b.User, append([]string(nil), b.Scopes...), nil
}

func (b *RunnerBackend) PollNotifications(_ context.Context, opts github.NotificationListOptions) (github.NotificationListResult, error) {
	b.NotificationOptions = append(b.NotificationOptions, opts)
	return b.Notifications, b.NotificationErr
}

func (b *RunnerBackend) ListIssueCommentsPage(_ context.Context, _ string, issue int, opts github.CommentListOptions) (github.IssueCommentsResult, error) {
	b.IssueCommentOptions = append(b.IssueCommentOptions, opts)
	if b.IssueCommentsErr != nil {
		return github.IssueCommentsResult{}, b.IssueCommentsErr
	}
	return b.IssueComments[issue], nil
}

func (b *RunnerBackend) ListRepositoryIssueCommentsPage(_ context.Context, _ string, opts github.CommentListOptions) (github.IssueCommentsResult, error) {
	b.RepositoryCommentOptions = append(b.RepositoryCommentOptions, opts)
	return b.RepositoryComments, b.RepositoryCommentsErr
}

func (b *RunnerBackend) GetCollaboratorPermission(_ context.Context, repo, login string) (github.CollaboratorPermissionResult, error) {
	b.CollaboratorLookups = append(b.CollaboratorLookups, repo+"#"+login)
	if login == b.PermissionLookupErrFor {
		return github.CollaboratorPermissionResult{}, errors.New("permission lookup failed")
	}
	permission := b.Permissions[login]
	if permission == "" {
		permission = b.DefaultPermission
	}
	if permission == "" {
		permission = "read"
	}
	return github.CollaboratorPermissionResult{
		Permission: github.CollaboratorPermission{Permission: permission},
		CanWrite:   permission == "write" || permission == "maintain" || permission == "admin",
	}, nil
}

func (b *RunnerBackend) GetRepositorySubscription(context.Context, string) (github.RepositorySubscriptionResult, error) {
	return b.Subscription, nil
}

func (b *RunnerBackend) GetIssueContext(context.Context, string, int, github.ConditionalRequest) (github.IssueContextResult, error) {
	return b.IssueContext, nil
}

func (b *RunnerBackend) CreateRunnerComment(_ context.Context, repo string, issue int, body string) (github.RunnerCommentResult, error) {
	if b.CreateRunnerCommentErr != nil {
		return github.RunnerCommentResult{}, b.CreateRunnerCommentErr
	}
	id := b.NextRunnerCommentID
	if id == 0 {
		id = 9000 + int64(len(b.CreatedRunnerComments)+1)
	}
	b.NextRunnerCommentID = id + 1
	comment := github.Comment{
		ID:          id,
		HTMLURL:     "https://github.com/" + repo + "/issues/" + strconv.Itoa(issue) + "#issuecomment-" + strconv.FormatInt(id, 10),
		URL:         "https://api.github.com/repos/" + repo + "/issues/comments/" + strconv.FormatInt(id, 10),
		IssueNumber: issue,
		Body:        body,
	}
	b.CreatedRunnerComments = append(b.CreatedRunnerComments, comment)
	return github.RunnerCommentResult{Comment: comment, Metadata: Metadata(http.StatusCreated, "", 0)}, nil
}

func (b *RunnerBackend) UpdateRunnerComment(_ context.Context, repo string, commentID int64, body string) (github.RunnerCommentResult, error) {
	if b.UpdateRunnerCommentErr != nil {
		return github.RunnerCommentResult{}, b.UpdateRunnerCommentErr
	}
	comment := github.Comment{
		ID:      commentID,
		HTMLURL: "https://github.com/" + repo + "/issues/comments/" + strconv.FormatInt(commentID, 10),
		URL:     "https://api.github.com/repos/" + repo + "/issues/comments/" + strconv.FormatInt(commentID, 10),
		Body:    body,
	}
	b.UpdatedRunnerComments = append(b.UpdatedRunnerComments, comment)
	return github.RunnerCommentResult{Comment: comment, Metadata: Metadata(http.StatusOK, "", 0)}, nil
}

func (b *RunnerBackend) AddCommentReaction(_ context.Context, repo string, commentID int64, content string) (github.RunnerReactionResult, error) {
	b.CommentReactions = append(b.CommentReactions, CommentReaction{Repo: repo, CommentID: commentID, Content: content})
	if b.AddCommentReactionErr != nil {
		return github.RunnerReactionResult{}, b.AddCommentReactionErr
	}
	return github.RunnerReactionResult{Metadata: Metadata(http.StatusCreated, "", 0)}, nil
}

func RunnerConfig() commentrunner.Config {
	return commentrunner.Config{
		Hostname:            "github.com",
		Repositories:        []string{"o/r"},
		RunnerIdentity:      "bot",
		GitHubBackend:       auth.GitHubBackendModeGH,
		StatePath:           "/tmp/runner-state.json",
		PollInterval:        commentrunner.NewDuration(time.Minute),
		FallbackInterval:    commentrunner.NewDuration(5 * time.Minute),
		MaxConcurrentJobs:   1,
		AcpxPath:            "acpx",
		Agent:               commentrunner.DefaultAgentConfig(),
		WorkspaceRoot:       "/tmp/workspaces",
		WorkspaceRetention:  commentrunner.NewDuration(time.Hour),
		CancellationEnabled: true,
	}.Normalized()
}

func CommandComment(id int64, issue int, login, body string) github.Comment {
	return github.Comment{
		ID:          id,
		HTMLURL:     "https://github.com/o/r/issues/" + strconv.Itoa(issue) + "#issuecomment-" + strconv.FormatInt(id, 10),
		URL:         "https://api.github.com/repos/o/r/issues/comments/" + strconv.FormatInt(id, 10),
		IssueURL:    "https://api.github.com/repos/o/r/issues/" + strconv.Itoa(issue),
		IssueNumber: issue,
		Body:        body,
		User:        &github.User{Login: login},
		CreatedAt:   Now.Add(-time.Hour),
		UpdatedAt:   Now.Add(-time.Minute),
	}
}

func Notification(issue int) github.Notification {
	return github.Notification{
		ID:         "n-" + strconv.Itoa(issue),
		Repository: github.Repository{FullName: "o/r"},
		Subject:    github.NotificationSubject{URL: "https://api.github.com/repos/o/r/issues/" + strconv.Itoa(issue), Type: "Issue"},
	}
}

func Metadata(status int, etag string, pollInterval int) github.ResponseMetadata {
	return github.ResponseMetadata{
		StatusCode:          status,
		ETag:                etag,
		NotModified:         status == http.StatusNotModified,
		PollIntervalSeconds: pollInterval,
	}
}

type RepoResolver struct {
	Info jobs.RepositoryInfo
	Err  error
}

func (r RepoResolver) ResolveRepository(context.Context, string) (jobs.RepositoryInfo, error) {
	if r.Err != nil {
		return jobs.RepositoryInfo{}, r.Err
	}
	if r.Info.Repo == "" {
		return jobs.RepositoryInfo{Repo: "o/r", CloneURL: "https://github.com/o/r.git", DefaultBranch: "main"}, nil
	}
	return r.Info, nil
}

type Workspaces struct {
	Binding             workspace.Binding
	Err                 error
	LockErr             error
	ReleaseErr          error
	PrepareNewRequests  []workspace.NewRequest
	ResolveResumeReqs   []workspace.ResumeRequest
	AcquireLockRequests []workspace.LockRequest
	ReleasedLocks       []crstate.SessionLock
}

func (w *Workspaces) PrepareNew(_ context.Context, req workspace.NewRequest) (workspace.Binding, error) {
	w.PrepareNewRequests = append(w.PrepareNewRequests, req)
	if w.Err != nil {
		return workspace.Binding{}, w.Err
	}
	return w.Binding, nil
}

func (w *Workspaces) ResolveResume(_ context.Context, req workspace.ResumeRequest) (workspace.Binding, error) {
	w.ResolveResumeReqs = append(w.ResolveResumeReqs, req)
	if w.Err != nil {
		return workspace.Binding{}, w.Err
	}
	return w.Binding, nil
}

func (w *Workspaces) AcquireLock(_ context.Context, req workspace.LockRequest) (crstate.SessionLock, error) {
	w.AcquireLockRequests = append(w.AcquireLockRequests, req)
	if w.LockErr != nil {
		return crstate.SessionLock{}, w.LockErr
	}
	return crstate.SessionLock{OwnerJobID: req.JobID, WorkspaceLockToken: "token-" + req.JobID, WorkspaceLockPath: "/tmp/lock-" + req.JobID}, nil
}

func (w *Workspaces) ReleaseLock(lock crstate.SessionLock) error {
	w.ReleasedLocks = append(w.ReleasedLocks, lock)
	return w.ReleaseErr
}

func WorkspaceBinding(id string) workspace.Binding {
	path := "/tmp/" + id
	return workspace.Binding{
		Workspace:            crstate.WorkspaceMetadata{ID: id, Path: path, Repo: "o/r", CloneURL: "https://github.com/o/r.git", Branch: "issue-spec-" + id, Ref: "main"},
		AcpxWorkingDirectory: path,
		SandboxWorkspacePath: path,
	}
}

type Sandbox struct {
	Env      jobs.ExecutionEnvironment
	Err      error
	Requests []jobs.SandboxRequest
}

func (s *Sandbox) Prepare(_ context.Context, req jobs.SandboxRequest) (jobs.ExecutionEnvironment, error) {
	s.Requests = append(s.Requests, req)
	if s.Env.WorkingDirectory == "" {
		s.Env.WorkingDirectory = req.AcpxWorkingDirectory
	}
	return s.Env, s.Err
}

type AcpxFactory struct {
	Coordinator *Coordinator
	Err         error
	Envs        []jobs.ExecutionEnvironment
}

func (f *AcpxFactory) NewCoordinator(env jobs.ExecutionEnvironment) (jobs.Coordinator, error) {
	f.Envs = append(f.Envs, env)
	if f.Err != nil {
		return nil, f.Err
	}
	if f.Coordinator == nil {
		f.Coordinator = &Coordinator{}
	}
	return f.Coordinator, nil
}

type Coordinator struct {
	NewResult                acpx.DispatchResult
	ResumeResult             acpx.DispatchResult
	NewErr                   error
	ResumeErr                error
	NewRequests              []acpx.NewSessionRequest
	ResumeRequests           []acpx.ResumeRequest
	NewContextHasDeadline    []bool
	ResumeContextHasDeadline []bool
}

func (c *Coordinator) NewSession(ctx context.Context, req acpx.NewSessionRequest) (acpx.DispatchResult, error) {
	c.NewRequests = append(c.NewRequests, req)
	_, hasDeadline := ctx.Deadline()
	c.NewContextHasDeadline = append(c.NewContextHasDeadline, hasDeadline)
	if c.NewErr != nil {
		return acpx.DispatchResult{}, c.NewErr
	}
	return c.NewResult, nil
}

func (c *Coordinator) Resume(ctx context.Context, req acpx.ResumeRequest) (acpx.DispatchResult, error) {
	c.ResumeRequests = append(c.ResumeRequests, req)
	_, hasDeadline := ctx.Deadline()
	c.ResumeContextHasDeadline = append(c.ResumeContextHasDeadline, hasDeadline)
	if c.ResumeErr != nil {
		return acpx.DispatchResult{}, c.ResumeErr
	}
	return c.ResumeResult, nil
}

func DispatchResult(publicID, recordID, turnID string) acpx.DispatchResult {
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
			Summary:      CompletedSummary(),
			SummaryFound: true,
		},
	}
}

func CompletedSummary() runnercontext.CoordinatorSummary {
	return runnercontext.CoordinatorSummary{
		Status: "completed",
		Artifacts: []runnercontext.WorkflowArtifact{{
			Kind:   "typed_comment",
			ID:     "PROCESS-014",
			URL:    "https://github.com/o/r/issues/30#issuecomment-1",
			Action: "updated",
		}},
		Commands: []runnercontext.CLICommandSummary{{
			Name:          "issue-spec comment upsert",
			ExitCode:      0,
			ArtifactID:    "PROCESS-014",
			ArtifactURL:   "https://github.com/o/r/issues/30#issuecomment-1",
			StdoutSummary: "updated PROCESS-014",
		}},
		Processes: []runnercontext.ProcessEvidence{{ProcessID: "PROCESS-014", TaskID: "TASK-019", Status: "done", Evidence: "tests passed"}},
	}
}

type Writeback struct {
	Store         *MemoryStore
	Err           error
	Requests      []writeback.Request
	NextCommentID int64
}

func (w *Writeback) Write(_ context.Context, req writeback.Request) (writeback.Result, error) {
	w.Requests = append(w.Requests, req)
	if w.Err != nil {
		return writeback.Result{}, w.Err
	}
	id := req.Job.StatusCommentID
	if id == 0 {
		id = w.NextCommentID
		if id == 0 {
			id = 9000 + int64(len(w.Requests))
		}
		w.NextCommentID = id + 1
	}
	url := "https://github.com/o/r/issues/" + strconv.Itoa(req.Job.IssueNumber) + "#issuecomment-" + strconv.FormatInt(id, 10)
	if w.Store != nil {
		_ = w.Store.Update(context.Background(), func(st *crstate.RunnerState) error {
			job := st.Jobs[req.Job.ID]
			job.StatusCommentID = id
			job.StatusCommentURL = url
			job.DispatchIntent.StatusCommentID = id
			return st.UpsertJob(job)
		})
	}
	return writeback.Result{Comment: github.Comment{ID: id, HTMLURL: url}}, nil
}
