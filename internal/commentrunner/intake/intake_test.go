package intake

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/auth"
	"github.com/higress-group/issue-spec/internal/commentrunner"
	crstate "github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/github"
)

var testNow = time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)

type fixedClock struct{}

func (fixedClock) Now() time.Time { return testNow }

type fakeStore struct {
	state crstate.RunnerState
	saves int
}

func (s *fakeStore) Load(context.Context) (crstate.RunnerState, error) {
	s.state.Normalize()
	return s.state, nil
}

func (s *fakeStore) Save(_ context.Context, st crstate.RunnerState) error {
	st.Normalize()
	s.state = st
	s.saves++
	return nil
}

type fakeBackend struct {
	user                   github.User
	permissions            map[string]string
	notifications          github.NotificationListResult
	issueComments          map[int]github.IssueCommentsResult
	repoComments           github.IssueCommentsResult
	notificationOpts       []github.NotificationListOptions
	issueCommentOpts       []github.CommentListOptions
	repoCommentOpts        []github.CommentListOptions
	collaboratorLookups    []string
	permissionLookupErrFor string
}

func (b *fakeBackend) GetUser(context.Context) (github.User, []string, error) {
	if b.user.Login == "" {
		b.user.Login = "bot"
	}
	return b.user, nil, nil
}

func (b *fakeBackend) PollNotifications(_ context.Context, opts github.NotificationListOptions) (github.NotificationListResult, error) {
	b.notificationOpts = append(b.notificationOpts, opts)
	return b.notifications, nil
}

func (b *fakeBackend) ListIssueCommentsPage(_ context.Context, _ string, issue int, opts github.CommentListOptions) (github.IssueCommentsResult, error) {
	b.issueCommentOpts = append(b.issueCommentOpts, opts)
	return b.issueComments[issue], nil
}

func (b *fakeBackend) ListRepositoryIssueCommentsPage(_ context.Context, _ string, opts github.CommentListOptions) (github.IssueCommentsResult, error) {
	b.repoCommentOpts = append(b.repoCommentOpts, opts)
	return b.repoComments, nil
}

func (b *fakeBackend) GetCollaboratorPermission(_ context.Context, repo, login string) (github.CollaboratorPermissionResult, error) {
	b.collaboratorLookups = append(b.collaboratorLookups, repo+"#"+login)
	if login == b.permissionLookupErrFor {
		return github.CollaboratorPermissionResult{}, errors.New("permission lookup failed")
	}
	permission := b.permissions[login]
	if permission == "" {
		permission = "read"
	}
	return github.CollaboratorPermissionResult{
		Permission: github.CollaboratorPermission{Permission: permission},
		CanWrite:   permission == "write" || permission == "maintain" || permission == "admin",
	}, nil
}

func (b *fakeBackend) GetRepositorySubscription(context.Context, string) (github.RepositorySubscriptionResult, error) {
	return github.RepositorySubscriptionResult{}, nil
}

func (b *fakeBackend) GetIssueContext(context.Context, string, int, github.ConditionalRequest) (github.IssueContextResult, error) {
	return github.IssueContextResult{}, nil
}

func (b *fakeBackend) CreateRunnerComment(context.Context, string, int, string) (github.RunnerCommentResult, error) {
	return github.RunnerCommentResult{}, nil
}

func (b *fakeBackend) UpdateRunnerComment(context.Context, string, int64, string) (github.RunnerCommentResult, error) {
	return github.RunnerCommentResult{}, nil
}

func TestRunOnceDeduplicatesNotificationAndFallbackDelivery(t *testing.T) {
	comment := commandComment(101, 7, "alice", "/new fix the flaky test")
	backend := &fakeBackend{
		user:        github.User{Login: "bot"},
		permissions: map[string]string{"alice": "write"},
		notifications: github.NotificationListResult{
			Notifications: []github.Notification{notification(7)},
			Metadata:      meta(http.StatusOK, `"notes-v1"`, 60),
		},
		issueComments: map[int]github.IssueCommentsResult{
			7: {Comments: []github.Comment{comment}, Metadata: meta(http.StatusOK, `"thread-v1"`, 0)},
		},
		repoComments: github.IssueCommentsResult{Comments: []github.Comment{comment}, Metadata: meta(http.StatusOK, `"repo-v1"`, 0)},
	}
	store := &fakeStore{state: crstate.NewState()}

	result, err := RunOnce(context.Background(), testConfig(), backend, store, testOptions("alice"))
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || len(result.Jobs) != 1 || !result.Jobs[0].Created {
		t.Fatalf("result jobs = %+v diagnostics=%+v", result.Jobs, result.Diagnostics)
	}
	if len(store.state.Jobs) != 1 {
		t.Fatalf("stored jobs = %d, want 1", len(store.state.Jobs))
	}
	if !hasStatus(result.Commands, CommandStatusDuplicate) {
		t.Fatalf("duplicate delivery was not reported: %+v", result.Commands)
	}
	if store.state.Repositories["o/r"].NotificationCursor.ETag != `"notes-v1"` {
		t.Fatalf("notification cursor not persisted: %+v", store.state.Repositories["o/r"].NotificationCursor)
	}
}

func TestRunOnceFallbackRecoversCommentMissingFromNotifications(t *testing.T) {
	backend := &fakeBackend{
		user:          github.User{Login: "bot"},
		permissions:   map[string]string{"bot": "admin"},
		notifications: github.NotificationListResult{Metadata: meta(http.StatusNotModified, `"notes-v1"`, 90)},
		repoComments:  github.IssueCommentsResult{Comments: []github.Comment{commandComment(102, 9, "bot", "/new self-authored command")}, Metadata: meta(http.StatusOK, `"repo-v2"`, 0)},
	}
	store := &fakeStore{state: crstate.NewState()}

	result, err := RunOnce(context.Background(), testConfig(), backend, store, testOptions("bot"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Jobs) != 1 || result.Jobs[0].Repo != "o/r" || result.Jobs[0].Issue != 9 {
		t.Fatalf("fallback did not create expected job: %+v", result.Jobs)
	}
	if len(backend.issueCommentOpts) != 0 {
		t.Fatalf("notification comments fetched despite no notification: %+v", backend.issueCommentOpts)
	}
}

func TestRunOnceRejectsUnauthorizedAndMalformedCommands(t *testing.T) {
	backend := &fakeBackend{
		user:          github.User{Login: "bot"},
		permissions:   map[string]string{"bob": "read", "alice": "write"},
		notifications: github.NotificationListResult{Metadata: meta(http.StatusNotModified, `"notes"`, 60)},
		repoComments: github.IssueCommentsResult{Comments: []github.Comment{
			commandComment(201, 4, "bob", "/new unauthorized"),
			commandComment(202, 4, "alice", "/resume bad/id continue"),
		}},
	}
	store := &fakeStore{state: crstate.NewState()}

	result, err := RunOnce(context.Background(), testConfig(), backend, store, testOptions("bob", "alice"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Jobs) != 0 || len(store.state.Jobs) != 0 {
		t.Fatalf("unexpected dispatchable jobs: result=%+v state=%+v", result.Jobs, store.state.Jobs)
	}
	if !hasStatus(result.Commands, CommandStatusUnauthorized) || !hasStatus(result.Commands, CommandStatusRejected) {
		t.Fatalf("expected unauthorized and malformed reports: %+v", result.Commands)
	}
}

func TestRunOnceCreatesResumeCandidateForKnownSession(t *testing.T) {
	st := crstate.NewState()
	st.PublicSessions[crstate.PublicSessionKey("o/r", "sess-1")] = crstate.PublicSession{
		Repo:            "o/r",
		PublicSessionID: "sess-1",
		AcpxRecordID:    "record-1",
	}
	backend := &fakeBackend{
		user:          github.User{Login: "bot"},
		permissions:   map[string]string{"alice": "maintain"},
		notifications: github.NotificationListResult{Metadata: meta(http.StatusNotModified, `"notes"`, 60)},
		repoComments:  github.IssueCommentsResult{Comments: []github.Comment{commandComment(301, 6, "alice", "/resume sess-1 continue work")}},
	}
	store := &fakeStore{state: st}

	result, err := RunOnce(context.Background(), testConfig(), backend, store, testOptions("alice"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Jobs) != 1 || result.Jobs[0].Verb != commentrunner.VerbResume || result.Jobs[0].PublicSessionID != "sess-1" {
		t.Fatalf("resume candidate not queued: %+v", result.Jobs)
	}
}

func TestRunOnceDryRunReportsWithoutSavingState(t *testing.T) {
	backend := &fakeBackend{
		user:          github.User{Login: "bot"},
		permissions:   map[string]string{"alice": "write"},
		notifications: github.NotificationListResult{Metadata: meta(http.StatusNotModified, `"notes"`, 60)},
		repoComments:  github.IssueCommentsResult{Comments: []github.Comment{commandComment(401, 5, "alice", "/new dry run")}},
	}
	store := &fakeStore{state: crstate.NewState()}

	opts := testOptions("alice")
	opts.DryRun = true
	result, err := RunOnce(context.Background(), testConfig(), backend, store, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !result.DryRun || len(result.Jobs) != 1 {
		t.Fatalf("dry-run result missing job report: %+v", result)
	}
	if store.saves != 0 || len(store.state.Jobs) != 0 {
		t.Fatalf("dry-run saved state: saves=%d jobs=%d", store.saves, len(store.state.Jobs))
	}
}

func TestRunOnceUsesConditionalCursorsAndRateLimitNextStep(t *testing.T) {
	st := crstate.NewState()
	st.Repositories["o/r"] = crstate.RepositoryState{
		Repo:               "o/r",
		NotificationCursor: crstate.CursorState{ETag: `"old-notes"`},
		NotificationThreadCursors: map[string]crstate.CursorState{
			"8": {ETag: `"old-thread"`},
		},
	}
	resetAt := testNow.Add(3 * time.Minute)
	backend := &fakeBackend{
		user:        github.User{Login: "bot"},
		permissions: map[string]string{"alice": "write"},
		notifications: github.NotificationListResult{
			Notifications: []github.Notification{notification(8)},
			Metadata: github.ResponseMetadata{
				StatusCode:          http.StatusOK,
				ETag:                `"new-notes"`,
				PollIntervalSeconds: 120,
				RateLimit:           github.RateLimitMetadata{Remaining: 0, ResetAt: resetAt},
			},
		},
		issueComments: map[int]github.IssueCommentsResult{
			8: {Comments: []github.Comment{commandComment(501, 8, "alice", "/new cursor test")}, Metadata: meta(http.StatusOK, `"new-thread"`, 0)},
		},
		repoComments: github.IssueCommentsResult{Metadata: meta(http.StatusNotModified, `"repo"`, 0)},
	}
	store := &fakeStore{state: st}

	result, err := RunOnce(context.Background(), testConfig(), backend, store, testOptions("alice"))
	if err != nil {
		t.Fatal(err)
	}
	if got := backend.notificationOpts[0].ETag; got != `"old-notes"` {
		t.Fatalf("notification ETag = %q", got)
	}
	if got := backend.issueCommentOpts[0].ETag; got != `"old-thread"` {
		t.Fatalf("thread ETag = %q", got)
	}
	if result.Next.PollAt != resetAt {
		t.Fatalf("next poll = %s, want rate reset %s", result.Next.PollAt, resetAt)
	}
	if store.state.Repositories["o/r"].NotificationThreadCursors["8"].ETag != `"new-thread"` {
		t.Fatalf("thread cursor not saved: %+v", store.state.Repositories["o/r"].NotificationThreadCursors["8"])
	}
}

func testConfig() commentrunner.Config {
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

func testOptions(users ...string) Options {
	return Options{
		Clock: fixedClock{},
		AuthorizationPolicy: commentrunner.AuthorizationPolicy{
			RunnerLogin:  "bot",
			AllowedUsers: users,
		},
	}
}

func commandComment(id int64, issue int, login, body string) github.Comment {
	return github.Comment{
		ID:          id,
		HTMLURL:     "https://github.com/o/r/issues/" + strconv.Itoa(issue) + "#issuecomment-" + strconv.FormatInt(id, 10),
		URL:         "https://api.github.com/repos/o/r/issues/comments/" + strconv.FormatInt(id, 10),
		IssueURL:    "https://api.github.com/repos/o/r/issues/" + strconv.Itoa(issue),
		IssueNumber: issue,
		Body:        body,
		User:        &github.User{Login: login},
		CreatedAt:   testNow.Add(-time.Hour),
		UpdatedAt:   testNow.Add(-time.Minute),
	}
}

func notification(issue int) github.Notification {
	return github.Notification{
		ID:         "n-" + strconv.Itoa(issue),
		Repository: github.Repository{FullName: "o/r"},
		Subject:    github.NotificationSubject{URL: "https://api.github.com/repos/o/r/issues/" + strconv.Itoa(issue), Type: "Issue"},
	}
}

func meta(status int, etag string, pollInterval int) github.ResponseMetadata {
	return github.ResponseMetadata{
		StatusCode:          status,
		ETag:                etag,
		NotModified:         status == http.StatusNotModified,
		PollIntervalSeconds: pollInterval,
	}
}

func hasStatus(reports []CommandReport, status string) bool {
	for _, report := range reports {
		if report.Status == status {
			return true
		}
	}
	return false
}
