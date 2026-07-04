package intake

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
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

type fakeReaction struct {
	repo      string
	commentID int64
	content   string
}

type fakeBackend struct {
	user                   github.User
	permissions            map[string]string
	issueStates            map[string]string
	notifications          github.NotificationListResult
	notificationErr        error
	issueComments          map[int]github.IssueCommentsResult
	repoComments           github.IssueCommentsResult
	listIssueCommentsPage  func(int, github.CommentListOptions) (github.IssueCommentsResult, error)
	listedReactions        map[int64]github.CommentReactionsResult
	listReactionsPage      func(int64, github.RunnerPageOptions) (github.CommentReactionsResult, error)
	notificationOpts       []github.NotificationListOptions
	issueCommentOpts       []github.CommentListOptions
	repoCommentOpts        []github.CommentListOptions
	issueContextLookups    []string
	reactionListLookups    []int64
	collaboratorLookups    []string
	permissionLookupErrFor string
	createdRunnerComments  []github.Comment
	updatedRunnerComments  []github.Comment
	commentReactions       []fakeReaction
	nextRunnerCommentID    int64
	addReactionErr         error
}

func (b *fakeBackend) GetUser(context.Context) (github.User, []string, error) {
	if b.user.Login == "" {
		b.user.Login = "bot"
	}
	return b.user, nil, nil
}

func (b *fakeBackend) PollNotifications(_ context.Context, opts github.NotificationListOptions) (github.NotificationListResult, error) {
	b.notificationOpts = append(b.notificationOpts, opts)
	return b.notifications, b.notificationErr
}

func (b *fakeBackend) ListIssueCommentsPage(_ context.Context, _ string, issue int, opts github.CommentListOptions) (github.IssueCommentsResult, error) {
	b.issueCommentOpts = append(b.issueCommentOpts, opts)
	if b.listIssueCommentsPage != nil {
		return b.listIssueCommentsPage(issue, opts)
	}
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

func (b *fakeBackend) GetIssueContext(_ context.Context, repo string, issue int, _ github.ConditionalRequest) (github.IssueContextResult, error) {
	b.issueContextLookups = append(b.issueContextLookups, repo+"#"+strconv.Itoa(issue))
	state := b.issueStates[repo+"#"+strconv.Itoa(issue)]
	if state == "" {
		state = "open"
	}
	return github.IssueContextResult{Issue: github.Issue{Number: issue, State: state}}, nil
}

func (b *fakeBackend) ListCommentReactionsPage(_ context.Context, _ string, commentID int64, page github.RunnerPageOptions) (github.CommentReactionsResult, error) {
	b.reactionListLookups = append(b.reactionListLookups, commentID)
	if b.listReactionsPage != nil {
		return b.listReactionsPage(commentID, page)
	}
	return b.listedReactions[commentID], nil
}

func (b *fakeBackend) CreateRunnerComment(_ context.Context, repo string, issue int, body string) (github.RunnerCommentResult, error) {
	id := b.nextRunnerCommentID
	if id == 0 {
		id = 9000 + int64(len(b.createdRunnerComments)+1)
	}
	b.nextRunnerCommentID = id + 1
	comment := github.Comment{
		ID:          id,
		HTMLURL:     "https://github.com/" + repo + "/issues/" + strconv.Itoa(issue) + "#issuecomment-" + strconv.FormatInt(id, 10),
		IssueNumber: issue,
		Body:        body,
	}
	b.createdRunnerComments = append(b.createdRunnerComments, comment)
	return github.RunnerCommentResult{Comment: comment}, nil
}

func (b *fakeBackend) UpdateRunnerComment(_ context.Context, repo string, commentID int64, body string) (github.RunnerCommentResult, error) {
	comment := github.Comment{
		ID:      commentID,
		HTMLURL: "https://github.com/" + repo + "/issues/0#issuecomment-" + strconv.FormatInt(commentID, 10),
		Body:    body,
	}
	b.updatedRunnerComments = append(b.updatedRunnerComments, comment)
	return github.RunnerCommentResult{Comment: comment}, nil
}

func (b *fakeBackend) AddCommentReaction(_ context.Context, repo string, commentID int64, content string) (github.RunnerReactionResult, error) {
	b.commentReactions = append(b.commentReactions, fakeReaction{repo: repo, commentID: commentID, content: content})
	if b.addReactionErr != nil {
		return github.RunnerReactionResult{}, b.addReactionErr
	}
	return github.RunnerReactionResult{Metadata: meta(http.StatusCreated, "", 0)}, nil
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
	if len(backend.commentReactions) != 1 || backend.commentReactions[0].commentID != 101 || backend.commentReactions[0].content != queuedJobReactionContent {
		t.Fatalf("queued job reaction = %+v, want one eyes reaction on trigger comment", backend.commentReactions)
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

func TestRunOnceSkipsClosedIssueNotificationThread(t *testing.T) {
	st := crstate.NewState()
	st.Repositories["o/r"] = crstate.RepositoryState{
		Repo: "o/r",
		FallbackCadence: crstate.FallbackCadence{
			Enabled:         true,
			IntervalSeconds: 300,
			NextPollAt:      testNow.Add(time.Hour),
		},
	}
	backend := &fakeBackend{
		user:        github.User{Login: "bot"},
		permissions: map[string]string{"alice": "write"},
		issueStates: map[string]string{"o/r#7": "closed"},
		notifications: github.NotificationListResult{
			Notifications: []github.Notification{notification(7)},
			Metadata:      meta(http.StatusOK, `"notes-v1"`, 60),
		},
		issueComments: map[int]github.IssueCommentsResult{
			7: {Comments: []github.Comment{commandComment(103, 7, "alice", "/new should not run")}},
		},
	}
	store := &fakeStore{state: st}

	result, err := RunOnce(context.Background(), testConfig(), backend, store, testOptions("alice"))
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || len(result.Jobs) != 0 || len(result.Commands) != 0 {
		t.Fatalf("closed notification thread should not produce commands: ok=%v jobs=%+v commands=%+v diagnostics=%+v", result.OK, result.Jobs, result.Commands, result.Diagnostics)
	}
	if len(backend.issueCommentOpts) != 0 {
		t.Fatalf("closed notification thread fetched comments: %+v", backend.issueCommentOpts)
	}
	if len(backend.collaboratorLookups) != 0 || len(backend.commentReactions) != 0 {
		t.Fatalf("closed notification thread performed command side effects: lookups=%+v reactions=%+v", backend.collaboratorLookups, backend.commentReactions)
	}
}

func TestRunOnceSkipsClosedIssueRepositoryFallbackComment(t *testing.T) {
	backend := &fakeBackend{
		user:          github.User{Login: "bot"},
		permissions:   map[string]string{"alice": "write"},
		issueStates:   map[string]string{"o/r#7": "closed"},
		notifications: github.NotificationListResult{Metadata: meta(http.StatusNotModified, `"notes"`, 60)},
		repoComments:  github.IssueCommentsResult{Comments: []github.Comment{commandComment(104, 7, "alice", "/new should not run")}},
	}
	store := &fakeStore{state: crstate.NewState()}

	result, err := RunOnce(context.Background(), testConfig(), backend, store, testOptions("alice"))
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || len(result.Jobs) != 0 || len(result.Commands) != 0 {
		t.Fatalf("closed fallback comment should not produce commands: ok=%v jobs=%+v commands=%+v diagnostics=%+v", result.OK, result.Jobs, result.Commands, result.Diagnostics)
	}
	if len(backend.collaboratorLookups) != 0 || len(backend.commentReactions) != 0 {
		t.Fatalf("closed fallback comment performed command side effects: lookups=%+v reactions=%+v", backend.collaboratorLookups, backend.commentReactions)
	}
	if got := store.state.Repositories["o/r"].RepositoryCommentCursor.LastSeenID; got != 104 {
		t.Fatalf("closed fallback cursor LastSeenID = %d, want 104", got)
	}
}

func TestRunOnceSkipsCommandAlreadyAckedByRunnerEyes(t *testing.T) {
	comment := commandComment(105, 7, "alice", "/new already handled")
	comment.Reactions = github.Reactions{TotalCount: 1, Eyes: 1}
	backend := &fakeBackend{
		user:          github.User{Login: "bot"},
		permissions:   map[string]string{"alice": "write"},
		notifications: github.NotificationListResult{Metadata: meta(http.StatusNotModified, `"notes"`, 60)},
		repoComments:  github.IssueCommentsResult{Comments: []github.Comment{comment}},
		listedReactions: map[int64]github.CommentReactionsResult{
			105: {Reactions: []github.Reaction{{User: &github.User{Login: "bot"}, Content: "eyes"}}},
		},
	}
	store := &fakeStore{state: crstate.NewState()}

	result, err := RunOnce(context.Background(), testConfig(), backend, store, testOptions("alice"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Jobs) != 0 || len(store.state.Jobs) != 0 {
		t.Fatalf("remote-acked command should not queue: result=%+v state=%+v", result.Jobs, store.state.Jobs)
	}
	if len(backend.reactionListLookups) != 1 || backend.reactionListLookups[0] != 105 {
		t.Fatalf("reaction lookup = %+v, want comment 105", backend.reactionListLookups)
	}
	if len(backend.collaboratorLookups) != 0 || len(backend.commentReactions) != 0 {
		t.Fatalf("remote-acked command performed queue side effects: lookups=%+v reactions=%+v", backend.collaboratorLookups, backend.commentReactions)
	}
	if !hasReason(result.Commands, "remote_runner_ack") {
		t.Fatalf("remote ack duplicate was not reported: %+v", result.Commands)
	}
}

func TestRunOnceOtherUserEyesDoesNotBlockQueue(t *testing.T) {
	comment := commandComment(106, 7, "alice", "/new handle despite other eyes")
	comment.Reactions = github.Reactions{TotalCount: 1, Eyes: 1}
	backend := &fakeBackend{
		user:          github.User{Login: "bot"},
		permissions:   map[string]string{"alice": "write"},
		notifications: github.NotificationListResult{Metadata: meta(http.StatusNotModified, `"notes"`, 60)},
		repoComments:  github.IssueCommentsResult{Comments: []github.Comment{comment}},
		listedReactions: map[int64]github.CommentReactionsResult{
			106: {Reactions: []github.Reaction{{User: &github.User{Login: "octocat"}, Content: "eyes"}}},
		},
	}
	store := &fakeStore{state: crstate.NewState()}

	result, err := RunOnce(context.Background(), testConfig(), backend, store, testOptions("alice"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Jobs) != 1 || len(store.state.Jobs) != 1 {
		t.Fatalf("other user's eyes should not block queue: result=%+v state=%+v", result.Jobs, store.state.Jobs)
	}
	if len(backend.reactionListLookups) != 1 || backend.reactionListLookups[0] != 106 {
		t.Fatalf("reaction lookup = %+v, want comment 106", backend.reactionListLookups)
	}
	if len(backend.commentReactions) != 1 || backend.commentReactions[0].commentID != 106 {
		t.Fatalf("queued job reaction = %+v, want eyes on comment 106", backend.commentReactions)
	}
}

func TestRunOnceNoReactionsUsesOriginalQueuePath(t *testing.T) {
	backend := &fakeBackend{
		user:          github.User{Login: "bot"},
		permissions:   map[string]string{"alice": "write"},
		notifications: github.NotificationListResult{Metadata: meta(http.StatusNotModified, `"notes"`, 60)},
		repoComments:  github.IssueCommentsResult{Comments: []github.Comment{commandComment(107, 7, "alice", "/new normal path")}},
		listedReactions: map[int64]github.CommentReactionsResult{
			107: {Reactions: []github.Reaction{{User: &github.User{Login: "bot"}, Content: "eyes"}}},
		},
	}
	store := &fakeStore{state: crstate.NewState()}

	result, err := RunOnce(context.Background(), testConfig(), backend, store, testOptions("alice"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Jobs) != 1 || len(store.state.Jobs) != 1 {
		t.Fatalf("comment without reaction summary should queue normally: result=%+v state=%+v", result.Jobs, store.state.Jobs)
	}
	if len(backend.reactionListLookups) != 0 {
		t.Fatalf("reaction API should not be queried without reaction summary: %+v", backend.reactionListLookups)
	}
	if len(backend.commentReactions) != 1 || backend.commentReactions[0].commentID != 107 {
		t.Fatalf("queued job reaction = %+v, want eyes on comment 107", backend.commentReactions)
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
	if len(result.Jobs) != 0 {
		t.Fatalf("unexpected dispatchable jobs: result=%+v state=%+v", result.Jobs, store.state.Jobs)
	}
	if len(backend.commentReactions) != 0 {
		t.Fatalf("rejected commands should not add reactions: %+v", backend.commentReactions)
	}
	if !hasStatus(result.Commands, CommandStatusUnauthorized) || !hasStatus(result.Commands, CommandStatusRejected) {
		t.Fatalf("expected unauthorized and malformed reports: %+v", result.Commands)
	}
	if len(backend.createdRunnerComments) != 2 {
		t.Fatalf("rejected commands should create durable status comments: %+v", backend.createdRunnerComments)
	}
	for _, job := range store.state.Jobs {
		if job.Status != crstate.StatusRejected || job.StatusWritebackKey == "" {
			t.Fatalf("rejected command job not persisted safely: %+v", job)
		}
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

func TestRunOnceQueuesCancelForActiveJobWithoutPublicSessionMapping(t *testing.T) {
	st := crstate.NewState()
	if err := st.UpsertJob(crstate.Job{
		ID:                  "job-running-new",
		Repo:                "o/r",
		IssueNumber:         30,
		PublicSessionID:     "ps-live",
		TriggeringUserLogin: "alice",
		TriggerCommentID:    900,
		CommandName:         "new",
		Status:              crstate.StatusRunning,
		CreatedAt:           testNow.Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	backend := &fakeBackend{
		user:          github.User{Login: "bot"},
		permissions:   map[string]string{"alice": "write"},
		notifications: github.NotificationListResult{Metadata: meta(http.StatusNotModified, `"notes"`, 60)},
		repoComments:  github.IssueCommentsResult{Comments: []github.Comment{commandComment(302, 30, "alice", "/cancel ps-live")}},
	}
	store := &fakeStore{state: st}

	result, err := RunOnce(context.Background(), testConfig(), backend, store, testOptions("alice"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Cancellations) != 1 || result.Cancellations[0].PublicSessionID != "ps-live" {
		t.Fatalf("active in-flight /new cancellation not queued: %+v commands=%+v", result.Cancellations, result.Commands)
	}
	cancel := firstCancellation(store.state.Cancellations)
	if cancel.TargetJobID != "job-running-new" || cancel.Status != crstate.StatusQueued {
		t.Fatalf("cancellation did not retain active target: %+v", cancel)
	}
	if len(backend.createdRunnerComments) != 0 {
		t.Fatalf("authorized cancellation should not create rejected writeback: %+v", backend.createdRunnerComments)
	}
	if len(backend.commentReactions) != 0 {
		t.Fatalf("authorized cancellation should not add queued job reaction: %+v", backend.commentReactions)
	}
}

func TestRunOnceRejectedWritebackDoesNotEchoUnauthorizedPrompt(t *testing.T) {
	secretPrompt := strings.Repeat("secret-token-", 80)
	backend := &fakeBackend{
		user:          github.User{Login: "bot"},
		permissions:   map[string]string{"mallory": "read"},
		notifications: github.NotificationListResult{Metadata: meta(http.StatusNotModified, `"notes"`, 60)},
		repoComments:  github.IssueCommentsResult{Comments: []github.Comment{commandComment(303, 4, "mallory", "/new "+secretPrompt)}},
	}
	store := &fakeStore{state: crstate.NewState()}

	result, err := RunOnce(context.Background(), testConfig(), backend, store, testOptions("mallory"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Jobs) != 0 || !hasStatus(result.Commands, CommandStatusUnauthorized) {
		t.Fatalf("unauthorized command should be rejected without a queued job: %+v", result)
	}
	if len(backend.createdRunnerComments) != 1 {
		t.Fatalf("expected one rejected writeback, got %+v", backend.createdRunnerComments)
	}
	body := backend.createdRunnerComments[0].Body
	if !strings.Contains(body, "| Status | `rejected` |") || !strings.Contains(body, "command unauthorized") {
		t.Fatalf("rejected writeback missing status/diagnostic:\n%s", body)
	}
	if strings.Contains(body, secretPrompt) || strings.Contains(body, "secret-token-secret-token-secret-token") {
		t.Fatalf("rejected writeback echoed unauthorized prompt:\n%s", body)
	}
}

func TestRunOnceWritesRejectedStatusForUnknownSessionAndDisabledCancellation(t *testing.T) {
	st := crstate.NewState()
	if err := st.UpsertPublicSession(crstate.PublicSession{
		Repo:            "o/r",
		PublicSessionID: "ps-known",
		IssueNumber:     4,
		AcpxRecordID:    "rec-known",
		Status:          crstate.StatusRunning,
	}); err != nil {
		t.Fatal(err)
	}
	backend := &fakeBackend{
		user:          github.User{Login: "bot"},
		permissions:   map[string]string{"alice": "write"},
		notifications: github.NotificationListResult{Metadata: meta(http.StatusNotModified, `"notes"`, 60)},
		repoComments: github.IssueCommentsResult{Comments: []github.Comment{
			commandComment(304, 4, "alice", "/resume ps-missing continue"),
			commandComment(305, 4, "alice", "/cancel ps-known"),
		}},
	}
	cfg := testConfig()
	cfg.CancellationEnabled = false
	store := &fakeStore{state: st}

	result, err := RunOnce(context.Background(), cfg, backend, store, testOptions("alice"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Jobs) != 0 || len(result.Cancellations) != 0 {
		t.Fatalf("rejected commands should not queue work: jobs=%+v cancellations=%+v", result.Jobs, result.Cancellations)
	}
	if len(backend.commentReactions) != 0 {
		t.Fatalf("unknown session or rejected cancel should not add reactions: %+v", backend.commentReactions)
	}
	if len(backend.createdRunnerComments) != 2 {
		t.Fatalf("expected rejected writebacks for unknown session and disabled cancellation: %+v", backend.createdRunnerComments)
	}
	if !strings.Contains(backend.createdRunnerComments[0].Body, "unknown-session") || !strings.Contains(backend.createdRunnerComments[1].Body, "cancellation-disabled") {
		t.Fatalf("rejected writebacks missing phases:\n--- first ---\n%s\n--- second ---\n%s", backend.createdRunnerComments[0].Body, backend.createdRunnerComments[1].Body)
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
	if len(backend.commentReactions) != 0 {
		t.Fatalf("dry-run should not add reactions: %+v", backend.commentReactions)
	}
	if store.saves != 0 || len(store.state.Jobs) != 0 {
		t.Fatalf("dry-run saved state: saves=%d jobs=%d", store.saves, len(store.state.Jobs))
	}
}

func TestRunOnceDoesNotStoreIgnoredCommentAsSeen(t *testing.T) {
	backend := &fakeBackend{
		user:          github.User{Login: "bot"},
		permissions:   map[string]string{"alice": "write"},
		notifications: github.NotificationListResult{Metadata: meta(http.StatusNotModified, `"notes"`, 60)},
		repoComments:  github.IssueCommentsResult{Comments: []github.Comment{commandComment(403, 5, "alice", "ordinary discussion")}},
	}
	store := &fakeStore{state: crstate.NewState()}

	result, err := RunOnce(context.Background(), testConfig(), backend, store, testOptions("alice"))
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || len(result.Jobs) != 0 || !hasStatus(result.Commands, CommandStatusIgnored) {
		t.Fatalf("ignored comment result = jobs=%+v commands=%+v diagnostics=%+v", result.Jobs, result.Commands, result.Diagnostics)
	}
	if len(store.state.Jobs) != 0 || len(store.state.Cancellations) != 0 {
		t.Fatalf("ignored comment created durable command state: jobs=%+v cancellations=%+v", store.state.Jobs, store.state.Cancellations)
	}
}

func TestRunOnceDuplicateCommandUsesStableIdempotencyKey(t *testing.T) {
	comment := commandComment(404, 5, "alice", "/new duplicate")
	backend := &fakeBackend{
		user:          github.User{Login: "bot"},
		permissions:   map[string]string{"alice": "write"},
		notifications: github.NotificationListResult{Metadata: meta(http.StatusNotModified, `"notes"`, 60)},
		repoComments:  github.IssueCommentsResult{Comments: []github.Comment{comment}},
	}
	store := &fakeStore{state: crstate.NewState()}

	first, err := RunOnce(context.Background(), testConfig(), backend, store, testOptions("alice"))
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Jobs) != 1 || !first.Jobs[0].Created {
		t.Fatalf("first run did not queue job: %+v", first.Jobs)
	}

	repoState := store.state.Repositories["o/r"]
	repoState.FallbackCadence.NextPollAt = testNow.Add(-time.Minute)
	store.state.Repositories["o/r"] = repoState
	backend.repoComments = github.IssueCommentsResult{Comments: []github.Comment{comment}}
	second, err := RunOnce(context.Background(), testConfig(), backend, store, testOptions("alice"))
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Jobs) != 0 || len(store.state.Jobs) != 1 || !hasReason(second.Commands, "idempotency_key_exists") {
		t.Fatalf("second run did not use command idempotency duplicate: jobs=%+v stored=%+v commands=%+v", second.Jobs, store.state.Jobs, second.Commands)
	}
}

func TestRunOnceIgnoredCommentEditedIntoCommandQueues(t *testing.T) {
	ignored := commandComment(405, 5, "alice", "ordinary discussion")
	accepted := ignored
	accepted.Body = "/new now a command"
	accepted.UpdatedAt = accepted.UpdatedAt.Add(time.Minute)
	backend := &fakeBackend{
		user:          github.User{Login: "bot"},
		permissions:   map[string]string{"alice": "write"},
		notifications: github.NotificationListResult{Metadata: meta(http.StatusNotModified, `"notes"`, 60)},
		repoComments:  github.IssueCommentsResult{Comments: []github.Comment{ignored}},
	}
	store := &fakeStore{state: crstate.NewState()}

	first, err := RunOnce(context.Background(), testConfig(), backend, store, testOptions("alice"))
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Jobs) != 0 || !hasStatus(first.Commands, CommandStatusIgnored) {
		t.Fatalf("first run should ignore comment: jobs=%+v commands=%+v", first.Jobs, first.Commands)
	}
	repoState := store.state.Repositories["o/r"]
	repoState.FallbackCadence.NextPollAt = testNow.Add(-time.Minute)
	store.state.Repositories["o/r"] = repoState
	backend.repoComments = github.IssueCommentsResult{Comments: []github.Comment{accepted}}

	second, err := RunOnce(context.Background(), testConfig(), backend, store, testOptions("alice"))
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Jobs) != 1 || !second.Jobs[0].Created {
		t.Fatalf("edited ignored comment did not queue: jobs=%+v commands=%+v", second.Jobs, second.Commands)
	}
}

func TestRunOnceMalformedCommandEditedIntoValidCommandQueues(t *testing.T) {
	malformed := commandComment(406, 5, "alice", "/new")
	accepted := malformed
	accepted.Body = "/new now valid"
	accepted.UpdatedAt = accepted.UpdatedAt.Add(time.Minute)
	backend := &fakeBackend{
		user:          github.User{Login: "bot"},
		permissions:   map[string]string{"alice": "write"},
		notifications: github.NotificationListResult{Metadata: meta(http.StatusNotModified, `"notes"`, 60)},
		repoComments:  github.IssueCommentsResult{Comments: []github.Comment{malformed}},
	}
	store := &fakeStore{state: crstate.NewState()}

	first, err := RunOnce(context.Background(), testConfig(), backend, store, testOptions("alice"))
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Jobs) != 0 || !hasStatus(first.Commands, CommandStatusRejected) {
		t.Fatalf("first run should reject malformed command: jobs=%+v commands=%+v", first.Jobs, first.Commands)
	}
	repoState := store.state.Repositories["o/r"]
	repoState.FallbackCadence.NextPollAt = testNow.Add(-time.Minute)
	store.state.Repositories["o/r"] = repoState
	backend.repoComments = github.IssueCommentsResult{Comments: []github.Comment{accepted}}

	second, err := RunOnce(context.Background(), testConfig(), backend, store, testOptions("alice"))
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Jobs) != 1 || !second.Jobs[0].Created {
		t.Fatalf("edited malformed command did not queue: jobs=%+v commands=%+v", second.Jobs, second.Commands)
	}
}

func TestRunOnceReactionFailureDoesNotBlockQueuedJob(t *testing.T) {
	backend := &fakeBackend{
		user:           github.User{Login: "bot"},
		permissions:    map[string]string{"alice": "write"},
		notifications:  github.NotificationListResult{Metadata: meta(http.StatusNotModified, `"notes"`, 60)},
		repoComments:   github.IssueCommentsResult{Comments: []github.Comment{commandComment(402, 5, "alice", "/new keep going")}},
		addReactionErr: errors.New("reaction failed"),
	}
	store := &fakeStore{state: crstate.NewState()}

	result, err := RunOnce(context.Background(), testConfig(), backend, store, testOptions("alice"))
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || len(result.Jobs) != 1 || len(store.state.Jobs) != 1 {
		t.Fatalf("reaction failure blocked queue: ok=%v jobs=%+v stored=%+v", result.OK, result.Jobs, store.state.Jobs)
	}
	if len(backend.commentReactions) != 1 || backend.commentReactions[0].commentID != 402 {
		t.Fatalf("reaction attempt = %+v, want one attempt", backend.commentReactions)
	}
	if len(result.Diagnostics) != 1 || !strings.Contains(result.Diagnostics[0].Message, "queued job reaction: reaction failed") {
		t.Fatalf("reaction failure diagnostic = %+v", result.Diagnostics)
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
	notificationCursor := store.state.Repositories["o/r"].NotificationCursor
	if notificationCursor.ETag != `"new-notes"` || notificationCursor.XPollIntervalSeconds != 120 || notificationCursor.LastSuccessfulPollAt != testNow {
		t.Fatalf("notification cursor not saved from success metadata: %+v", notificationCursor)
	}
	if notificationCursor.RateLimit.Remaining != 0 || !notificationCursor.RateLimit.ResetAt.Equal(resetAt) {
		t.Fatalf("notification rate limit not saved: %+v", notificationCursor.RateLimit)
	}
	if store.state.Repositories["o/r"].NotificationThreadCursors["8"].ETag != `"new-thread"` {
		t.Fatalf("thread cursor not saved: %+v", store.state.Repositories["o/r"].NotificationThreadCursors["8"])
	}
}

func TestRunOnceNotificationErrorRateLimitResetDefersNextPoll(t *testing.T) {
	st := crstate.NewState()
	st.Repositories["o/r"] = crstate.RepositoryState{
		Repo: "o/r",
		FallbackCadence: crstate.FallbackCadence{
			Enabled:         true,
			IntervalSeconds: 300,
			NextPollAt:      testNow.Add(time.Hour),
		},
	}
	resetAt := testNow.Add(15 * time.Minute)
	backend := &fakeBackend{
		user:            github.User{Login: "bot"},
		notificationErr: errors.New("notifications forbidden"),
		notifications: github.NotificationListResult{Metadata: github.ResponseMetadata{
			StatusCode: http.StatusForbidden,
			RateLimit:  github.RateLimitMetadata{Remaining: 0, ResetAt: resetAt},
		}},
	}
	store := &fakeStore{state: st}

	result, err := RunOnce(context.Background(), testConfig(), backend, store, testOptions("alice"))
	if err != nil {
		t.Fatal(err)
	}
	if result.OK {
		t.Fatalf("result OK = true, want notification diagnostic: %+v", result)
	}
	if result.Next.PollAt != resetAt {
		t.Fatalf("next poll = %s, want rate reset %s", result.Next.PollAt, resetAt)
	}
	cursor := store.state.Repositories["o/r"].NotificationCursor
	if cursor.LastStatusCode != http.StatusForbidden || !cursor.RateLimit.ResetAt.Equal(resetAt) || cursor.RateLimit.Remaining != 0 {
		t.Fatalf("notification error metadata not persisted: %+v", cursor)
	}
	if !cursor.LastSuccessfulPollAt.IsZero() {
		t.Fatalf("error metadata marked as successful poll: %+v", cursor)
	}
}

func TestRunOnceNotificationErrorRetryAfterDefersNextPoll(t *testing.T) {
	st := crstate.NewState()
	st.Repositories["o/r"] = crstate.RepositoryState{
		Repo: "o/r",
		FallbackCadence: crstate.FallbackCadence{
			Enabled:         true,
			IntervalSeconds: 300,
			NextPollAt:      testNow.Add(time.Hour),
		},
	}
	retryAfter := 2 * time.Minute
	backend := &fakeBackend{
		user:            github.User{Login: "bot"},
		notificationErr: errors.New("notifications throttled"),
		notifications: github.NotificationListResult{Metadata: github.ResponseMetadata{
			StatusCode: http.StatusTooManyRequests,
			RateLimit:  github.RateLimitMetadata{RetryAfterSeconds: int(retryAfter.Seconds())},
		}},
	}
	store := &fakeStore{state: st}

	result, err := RunOnce(context.Background(), testConfig(), backend, store, testOptions("alice"))
	if err != nil {
		t.Fatal(err)
	}
	if result.OK {
		t.Fatalf("result OK = true, want notification diagnostic: %+v", result)
	}
	if result.Next.PollAfter != retryAfter || result.Next.PollAt != testNow.Add(retryAfter) {
		t.Fatalf("next poll = after %s at %s, want Retry-After %s", result.Next.PollAfter, result.Next.PollAt, retryAfter)
	}
	cursor := store.state.Repositories["o/r"].NotificationCursor
	if cursor.LastStatusCode != http.StatusTooManyRequests || cursor.RateLimit.RetryAfterSeconds != int(retryAfter.Seconds()) {
		t.Fatalf("notification Retry-After metadata not persisted: %+v", cursor)
	}
}

func TestRunOnceDoesNotAdvanceThreadCursorAfterPartialPageError(t *testing.T) {
	const nextURL = "https://api.github.test/repos/o/r/issues/8/comments?per_page=100&page=2"
	st := crstate.NewState()
	st.Repositories["o/r"] = crstate.RepositoryState{
		Repo: "o/r",
		NotificationThreadCursors: map[string]crstate.CursorState{
			"8": {},
		},
		FallbackCadence: crstate.FallbackCadence{
			Enabled:         true,
			IntervalSeconds: 300,
			NextPollAt:      testNow.Add(time.Hour),
		},
	}
	backend := &fakeBackend{
		user:        github.User{Login: "bot"},
		permissions: map[string]string{"alice": "write"},
		notifications: github.NotificationListResult{
			Notifications: []github.Notification{notification(8)},
			Metadata:      meta(http.StatusOK, `"notes-v1"`, 0),
		},
		repoComments: github.IssueCommentsResult{Metadata: meta(http.StatusNotModified, `"repo"`, 0)},
	}
	calls := 0
	backend.listIssueCommentsPage = func(issue int, opts github.CommentListOptions) (github.IssueCommentsResult, error) {
		if issue != 8 {
			t.Fatalf("issue = %d, want 8", issue)
		}
		calls++
		switch calls {
		case 1:
			if opts.Page.CursorURL != "" {
				t.Fatalf("first page CursorURL = %q", opts.Page.CursorURL)
			}
			return github.IssueCommentsResult{
				Metadata: github.ResponseMetadata{
					StatusCode: http.StatusOK,
					ETag:       `"thread-page-1"`,
					Pagination: github.PaginationMetadata{NextURL: nextURL},
				},
			}, nil
		case 2:
			if opts.Page.CursorURL != nextURL {
				t.Fatalf("second page CursorURL = %q, want %q", opts.Page.CursorURL, nextURL)
			}
			return github.IssueCommentsResult{}, errors.New("temporary page 2 failure")
		default:
			t.Fatalf("unexpected issue comments call %d", calls)
			return github.IssueCommentsResult{}, nil
		}
	}
	store := &fakeStore{state: st}

	result, err := RunOnce(context.Background(), testConfig(), backend, store, testOptions("alice"))
	if err != nil {
		t.Fatal(err)
	}
	if result.OK || len(result.Diagnostics) != 1 {
		t.Fatalf("partial page error should be diagnostic failure: ok=%v diagnostics=%+v", result.OK, result.Diagnostics)
	}
	cursor := store.state.Repositories["o/r"].NotificationThreadCursors["8"]
	if cursor.ETag != "" || cursor.Cursor != "" {
		t.Fatalf("partial thread cursor was advanced: %+v", cursor)
	}
}

func TestRunOncePreservesThreadPaginationAfterMetadataBearingPageError(t *testing.T) {
	const nextURL = "https://api.github.test/repos/o/r/issues/8/comments?per_page=100&page=2"
	retryAfter := 2 * time.Minute
	resetAt := testNow.Add(90 * time.Second)
	st := crstate.NewState()
	st.Repositories["o/r"] = crstate.RepositoryState{
		Repo: "o/r",
		NotificationThreadCursors: map[string]crstate.CursorState{
			"8": {},
		},
		FallbackCadence: crstate.FallbackCadence{
			Enabled:         true,
			IntervalSeconds: 300,
			NextPollAt:      testNow.Add(time.Hour),
		},
	}
	backend := &fakeBackend{
		user:        github.User{Login: "bot"},
		permissions: map[string]string{"alice": "write"},
		notifications: github.NotificationListResult{
			Notifications: []github.Notification{notification(8)},
			Metadata:      meta(http.StatusOK, `"notes-v1"`, 0),
		},
		repoComments: github.IssueCommentsResult{Metadata: meta(http.StatusNotModified, `"repo"`, 0)},
	}
	calls := 0
	backend.listIssueCommentsPage = func(issue int, opts github.CommentListOptions) (github.IssueCommentsResult, error) {
		if issue != 8 {
			t.Fatalf("issue = %d, want 8", issue)
		}
		calls++
		switch calls {
		case 1:
			if opts.Page.CursorURL != "" {
				t.Fatalf("first page CursorURL = %q", opts.Page.CursorURL)
			}
			return github.IssueCommentsResult{
				Metadata: github.ResponseMetadata{
					StatusCode: http.StatusOK,
					ETag:       `"thread-page-1"`,
					Pagination: github.PaginationMetadata{NextURL: nextURL},
				},
			}, nil
		case 2:
			if opts.Page.CursorURL != nextURL {
				t.Fatalf("second page CursorURL = %q, want %q", opts.Page.CursorURL, nextURL)
			}
			return github.IssueCommentsResult{
				Metadata: github.ResponseMetadata{
					StatusCode:          http.StatusTooManyRequests,
					ETag:                `"error-etag"`,
					PollIntervalSeconds: 90,
					RateLimit: github.RateLimitMetadata{
						Remaining:         0,
						ResetAt:           resetAt,
						RetryAfterSeconds: int(retryAfter.Seconds()),
					},
				},
			}, errors.New("temporary page 2 throttling")
		default:
			t.Fatalf("unexpected issue comments call %d", calls)
			return github.IssueCommentsResult{}, nil
		}
	}
	store := &fakeStore{state: st}

	result, err := RunOnce(context.Background(), testConfig(), backend, store, testOptions("alice"))
	if err != nil {
		t.Fatal(err)
	}
	if result.OK || len(result.Diagnostics) != 1 {
		t.Fatalf("metadata-bearing page error should be diagnostic failure: ok=%v diagnostics=%+v", result.OK, result.Diagnostics)
	}
	cursor := store.state.Repositories["o/r"].NotificationThreadCursors["8"]
	if cursor.ETag != `"thread-page-1"` || cursor.Cursor != nextURL {
		t.Fatalf("thread pagination cursor not preserved after error: %+v", cursor)
	}
	if cursor.LastStatusCode != http.StatusTooManyRequests || cursor.XPollIntervalSeconds != 90 || cursor.RateLimit.RetryAfterSeconds != int(retryAfter.Seconds()) || !cursor.RateLimit.ResetAt.Equal(resetAt) {
		t.Fatalf("thread error metadata not persisted: %+v", cursor)
	}
	if !cursor.LastSuccessfulPollAt.IsZero() {
		t.Fatalf("thread error marked as successful poll: %+v", cursor)
	}
	if result.Next.PollAfter != retryAfter || result.Next.PollAt != testNow.Add(retryAfter) {
		t.Fatalf("next poll = after %s at %s, want Retry-After %s", result.Next.PollAfter, result.Next.PollAt, retryAfter)
	}
}

func TestRunOnceResumesThreadPaginationAfterMetadataBearingPageError(t *testing.T) {
	const nextURL = "https://api.github.test/repos/o/r/issues/8/comments?per_page=100&page=2"
	retryAfter := 2 * time.Minute
	st := crstate.NewState()
	st.Repositories["o/r"] = crstate.RepositoryState{
		Repo: "o/r",
		NotificationThreadCursors: map[string]crstate.CursorState{
			"8": {},
		},
		FallbackCadence: crstate.FallbackCadence{
			Enabled:         true,
			IntervalSeconds: 300,
			NextPollAt:      testNow.Add(time.Hour),
		},
	}
	backend := &fakeBackend{
		user:        github.User{Login: "bot"},
		permissions: map[string]string{"alice": "write"},
		notifications: github.NotificationListResult{
			Notifications: []github.Notification{notification(8)},
			Metadata:      meta(http.StatusOK, `"notes-v1"`, 0),
		},
		repoComments: github.IssueCommentsResult{Metadata: meta(http.StatusNotModified, `"repo"`, 0)},
	}
	calls := 0
	backend.listIssueCommentsPage = func(issue int, opts github.CommentListOptions) (github.IssueCommentsResult, error) {
		if issue != 8 {
			t.Fatalf("issue = %d, want 8", issue)
		}
		calls++
		switch calls {
		case 1:
			if opts.Page.CursorURL != "" {
				t.Fatalf("first page CursorURL = %q", opts.Page.CursorURL)
			}
			return github.IssueCommentsResult{
				Metadata: github.ResponseMetadata{
					StatusCode: http.StatusOK,
					ETag:       `"thread-page-1"`,
					Pagination: github.PaginationMetadata{NextURL: nextURL},
				},
			}, nil
		case 2:
			if opts.Page.CursorURL != nextURL {
				t.Fatalf("second page CursorURL = %q, want %q", opts.Page.CursorURL, nextURL)
			}
			return github.IssueCommentsResult{
				Metadata: github.ResponseMetadata{
					StatusCode:          http.StatusServiceUnavailable,
					ETag:                `"error-etag"`,
					PollIntervalSeconds: 90,
					RateLimit: github.RateLimitMetadata{
						RetryAfterSeconds: int(retryAfter.Seconds()),
					},
				},
			}, errors.New("temporary page 2 outage")
		case 3:
			if opts.Page.CursorURL != "" {
				t.Fatalf("resumed first page CursorURL = %q", opts.Page.CursorURL)
			}
			if opts.ETag != `"thread-page-1"` {
				t.Fatalf("resumed first page ETag = %q, want page-1 ETag", opts.ETag)
			}
			return github.IssueCommentsResult{Metadata: meta(http.StatusNotModified, `"thread-page-1"`, 0)}, nil
		case 4:
			if opts.Page.CursorURL != nextURL {
				t.Fatalf("resumed second page CursorURL = %q, want %q", opts.Page.CursorURL, nextURL)
			}
			return github.IssueCommentsResult{
				Comments: []github.Comment{commandComment(602, 8, "alice", "/new from page two")},
				Metadata: meta(http.StatusOK, `"thread-complete"`, 0),
			}, nil
		default:
			t.Fatalf("unexpected issue comments call %d", calls)
			return github.IssueCommentsResult{}, nil
		}
	}
	store := &fakeStore{state: st}

	first, err := RunOnce(context.Background(), testConfig(), backend, store, testOptions("alice"))
	if err != nil {
		t.Fatal(err)
	}
	if first.OK || len(first.Diagnostics) != 1 {
		t.Fatalf("metadata-bearing page error should be diagnostic failure: ok=%v diagnostics=%+v", first.OK, first.Diagnostics)
	}
	cursor := store.state.Repositories["o/r"].NotificationThreadCursors["8"]
	if cursor.ETag != `"thread-page-1"` || cursor.Cursor != nextURL {
		t.Fatalf("thread pagination cursor not preserved after first run: %+v", cursor)
	}

	second, err := RunOnce(context.Background(), testConfig(), backend, store, testOptions("alice"))
	if err != nil {
		t.Fatal(err)
	}
	if !second.OK || len(second.Jobs) != 1 || second.Jobs[0].TriggerComment != 602 {
		t.Fatalf("page 2 command was not queued after resume: ok=%v jobs=%+v diagnostics=%+v", second.OK, second.Jobs, second.Diagnostics)
	}
	if calls != 4 {
		t.Fatalf("issue comment calls = %d, want 4", calls)
	}
	cursor = store.state.Repositories["o/r"].NotificationThreadCursors["8"]
	if cursor.ETag != `"thread-complete"` || cursor.Cursor != "" {
		t.Fatalf("completed thread cursor = %+v", cursor)
	}
}

func TestRunOnceContinuesThreadPaginationAfterPageOneNotModifiedWithPendingCursor(t *testing.T) {
	const nextURL = "https://api.github.test/repos/o/r/issues/8/comments?per_page=100&page=2"
	st := crstate.NewState()
	st.Repositories["o/r"] = crstate.RepositoryState{
		Repo: "o/r",
		NotificationThreadCursors: map[string]crstate.CursorState{
			"8": {
				Resource: "issue-comments:o/r#8",
				ETag:     `"thread-page-1"`,
				Cursor:   nextURL,
			},
		},
		FallbackCadence: crstate.FallbackCadence{
			Enabled:         true,
			IntervalSeconds: 300,
			NextPollAt:      testNow.Add(time.Hour),
		},
	}
	backend := &fakeBackend{
		user:        github.User{Login: "bot"},
		permissions: map[string]string{"alice": "write"},
		notifications: github.NotificationListResult{
			Notifications: []github.Notification{notification(8)},
			Metadata:      meta(http.StatusOK, `"notes-v1"`, 0),
		},
		repoComments: github.IssueCommentsResult{Metadata: meta(http.StatusNotModified, `"repo"`, 0)},
	}
	calls := 0
	backend.listIssueCommentsPage = func(issue int, opts github.CommentListOptions) (github.IssueCommentsResult, error) {
		if issue != 8 {
			t.Fatalf("issue = %d, want 8", issue)
		}
		calls++
		switch calls {
		case 1:
			if opts.Page.CursorURL != "" {
				t.Fatalf("first page CursorURL = %q", opts.Page.CursorURL)
			}
			if opts.ETag != `"thread-page-1"` {
				t.Fatalf("first page ETag = %q, want page-1 cursor", opts.ETag)
			}
			return github.IssueCommentsResult{Metadata: meta(http.StatusNotModified, `"thread-page-1"`, 0)}, nil
		case 2:
			if opts.Page.CursorURL != nextURL {
				t.Fatalf("second page CursorURL = %q, want %q", opts.Page.CursorURL, nextURL)
			}
			return github.IssueCommentsResult{
				Comments: []github.Comment{commandComment(602, 8, "alice", "/new from page two")},
				Metadata: meta(http.StatusOK, `"thread-complete"`, 0),
			}, nil
		default:
			t.Fatalf("unexpected issue comments call %d", calls)
			return github.IssueCommentsResult{}, nil
		}
	}
	store := &fakeStore{state: st}

	result, err := RunOnce(context.Background(), testConfig(), backend, store, testOptions("alice"))
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || len(result.Jobs) != 1 || result.Jobs[0].TriggerComment != 602 {
		t.Fatalf("page 2 command was not queued: ok=%v jobs=%+v diagnostics=%+v", result.OK, result.Jobs, result.Diagnostics)
	}
	if calls != 2 {
		t.Fatalf("issue comment calls = %d, want 2", calls)
	}
	cursor := store.state.Repositories["o/r"].NotificationThreadCursors["8"]
	if cursor.ETag != `"thread-complete"` || cursor.Cursor != "" {
		t.Fatalf("completed thread cursor = %+v", cursor)
	}
}

func TestComputeNextStepUsesBackoffMetadataFromAllCursors(t *testing.T) {
	cfg := testConfig()
	st := crstate.NewState()
	st.Repositories["o/r"] = crstate.RepositoryState{
		Repo:               "o/r",
		NotificationCursor: crstate.CursorState{XPollIntervalSeconds: 120},
		RepositoryCommentCursor: crstate.CursorState{
			RateLimit: crstate.RateLimitState{Remaining: 0, ResetAt: testNow.Add(4 * time.Minute)},
		},
		NotificationThreadCursors: map[string]crstate.CursorState{
			"8": {
				LastPollAt: testNow,
				RateLimit:  crstate.RateLimitState{RetryAfterSeconds: int((7 * time.Minute).Seconds())},
			},
		},
		IssueCommentCursors: map[string]crstate.CursorState{
			"9": {RateLimit: crstate.RateLimitState{Remaining: 0, ResetAt: testNow.Add(6 * time.Minute)}},
		},
	}

	next := computeNextStep(cfg, st, testNow)
	want := testNow.Add(7 * time.Minute)
	if next.PollAt != want || next.RateLimitResetAt != (time.Time{}) {
		t.Fatalf("next step = %+v, want poll at Retry-After %s without reset winner", next, want)
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

func hasReason(reports []CommandReport, reason string) bool {
	for _, report := range reports {
		if report.Reason == reason {
			return true
		}
	}
	return false
}

func firstCancellation(cancellations map[string]crstate.Cancellation) crstate.Cancellation {
	for _, cancellation := range cancellations {
		return cancellation
	}
	return crstate.Cancellation{}
}
