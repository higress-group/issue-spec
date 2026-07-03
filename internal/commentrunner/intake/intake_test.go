package intake

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/github"
)

type fakeBackend struct {
	notifications  []github.Notification
	notesMeta      github.ResponseMetadata
	threadComments []github.Comment
	threadMeta     github.ResponseMetadata
	repoComments   []github.RepositoryIssueComment
	repoMeta       github.ResponseMetadata
	notifETag      string
	threadETag     string
}

func (f *fakeBackend) GetUser(context.Context) (github.User, []string, error) {
	return github.User{}, nil, nil
}
func (f *fakeBackend) CreateIssue(context.Context, string, string, string, []string) (github.Issue, error) {
	return github.Issue{}, nil
}
func (f *fakeBackend) GetIssue(context.Context, string, int) (github.Issue, error) {
	return github.Issue{}, nil
}
func (f *fakeBackend) UpdateIssue(context.Context, string, int, github.UpdateIssueOptions) (github.Issue, error) {
	return github.Issue{}, nil
}
func (f *fakeBackend) ListIssueComments(context.Context, string, int) ([]github.Comment, error) {
	return nil, nil
}
func (f *fakeBackend) CreateComment(context.Context, string, int, string) (github.Comment, error) {
	return github.Comment{}, nil
}
func (f *fakeBackend) UpdateComment(context.Context, string, int64, string) (github.Comment, error) {
	return github.Comment{}, nil
}
func (f *fakeBackend) CollaboratorPermission(context.Context, string, string) (string, error) {
	return "write", nil
}
func (f *fakeBackend) CreateLabel(context.Context, string, string, string, string) (github.LabelResult, error) {
	return github.LabelResult{}, nil
}
func (f *fakeBackend) GetPullRequest(context.Context, string, int) (github.PullRequest, error) {
	return github.PullRequest{}, nil
}
func (f *fakeBackend) CreatePullRequest(context.Context, string, github.CreatePullRequestOptions) (github.PullRequest, error) {
	return github.PullRequest{}, nil
}
func (f *fakeBackend) ListPullRequestFiles(context.Context, string, int) ([]github.PullRequestFile, error) {
	return nil, nil
}
func (f *fakeBackend) ListPullRequestReviewComments(context.Context, string, int) ([]github.PullRequestReviewComment, error) {
	return nil, nil
}
func (f *fakeBackend) CreatePullRequestReviewComment(context.Context, string, int, string, string, string, int, string) (github.PullRequestReviewComment, error) {
	return github.PullRequestReviewComment{}, nil
}
func (f *fakeBackend) ReplyPullRequestReviewComment(context.Context, string, int, int64, string) (github.PullRequestReviewComment, error) {
	return github.PullRequestReviewComment{}, nil
}
func (f *fakeBackend) GetCombinedStatus(context.Context, string, string) (github.CombinedStatus, error) {
	return github.CombinedStatus{}, nil
}
func (f *fakeBackend) ListCheckRuns(context.Context, string, string) ([]github.CheckRun, error) {
	return nil, nil
}
func (f *fakeBackend) ListNotifications(context.Context, github.NotificationListOptions) ([]github.Notification, github.ResponseMetadata, error) {
	return f.notifications, f.notesMeta, nil
}
func (f *fakeBackend) WatchRepository(context.Context, string) (github.Subscription, github.ResponseMetadata, error) {
	return github.Subscription{}, github.ResponseMetadata{}, nil
}
func (f *fakeBackend) GetNotificationComments(context.Context, string, github.NotificationCommentOptions) ([]github.Comment, github.ResponseMetadata, error) {
	if f.threadMeta.NotModified {
		return nil, f.threadMeta, nil
	}
	return f.threadComments, f.threadMeta, nil
}
func (f *fakeBackend) ListRepositoryIssueComments(context.Context, string, github.RepositoryIssueCommentOptions) ([]github.RepositoryIssueComment, github.ResponseMetadata, error) {
	return f.repoComments, f.repoMeta, nil
}
func (f *fakeBackend) GetCollaboratorPermission(context.Context, string, string) (github.Permission, github.ResponseMetadata, error) {
	return github.Permission{Permission: "write"}, github.ResponseMetadata{}, nil
}

type staticAllowlist map[string]bool

func (s staticAllowlist) Allowed(login string) bool { return s[login] }

type staticPerms string

func (s staticPerms) CollaboratorPermission(context.Context, string, string) (string, error) {
	return string(s), nil
}

func notificationURL(u string) github.Notification {
	var n github.Notification
	n.Subject.URL = u
	return n
}

func TestPollUsesValidatorsAnd304(t *testing.T) {
	dir := t.TempDir()
	store := state.New(filepath.Join(dir, "state.json"))
	now := time.Unix(100, 0).UTC()
	b := &fakeBackend{
		notesMeta:  github.ResponseMetadata{ETag: `"n1"`},
		threadMeta: github.ResponseMetadata{NotModified: true},
	}
	r := Runner{Backend: b, Store: store, Allowlist: staticAllowlist{"alice": true}, Perms: staticPerms("write"), Now: func() time.Time { return now }}
	got, err := r.Poll(context.Background(), "o/r")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Candidates) != 0 {
		t.Fatalf("candidates = %+v", got.Candidates)
	}
	st, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if st.Repos["o/r"].Validators.NotificationsETag != `"n1"` {
		t.Fatalf("validators = %+v", st.Repos["o/r"].Validators)
	}
}

func TestPollCreatesCandidatesAndDedupes(t *testing.T) {
	dir := t.TempDir()
	store := state.New(filepath.Join(dir, "state.json"))
	now := time.Unix(100, 0).UTC()
	body := "/new build it"
	b := &fakeBackend{
		notifications:  []github.Notification{notificationURL("https://github.com/o/r/issues/7")},
		threadComments: []github.Comment{{ID: 11, HTMLURL: "https://github.com/o/r/issues/7#issuecomment-11", Body: body, User: &github.User{Login: "alice"}}},
		repoComments:   []github.RepositoryIssueComment{{Comment: github.Comment{ID: 22, HTMLURL: "https://github.com/o/r/issues/8#issuecomment-22", Body: "/cancel sess-1", User: &github.User{Login: "alice"}}, IssueNumber: 8}},
	}
	r := Runner{Backend: b, Store: store, Allowlist: staticAllowlist{"alice": true}, Perms: staticPerms("write"), Now: func() time.Time { return now }}
	got, err := r.Poll(context.Background(), "o/r")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Candidates) != 2 {
		t.Fatalf("candidates = %+v", got.Candidates)
	}
	if got.Candidates[0].Kind != CandidateJob || got.Candidates[1].Kind != CandidateCancel {
		t.Fatalf("candidates = %+v", got.Candidates)
	}
	again, err := r.Poll(context.Background(), "o/r")
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Candidates) != 0 {
		t.Fatalf("duplicate candidates = %+v", again.Candidates)
	}
}

func TestPollFallsBackToRepositoryComments(t *testing.T) {
	dir := t.TempDir()
	store := state.New(filepath.Join(dir, "state.json"))
	now := time.Unix(100, 0).UTC()
	b := &fakeBackend{
		notifications: []github.Notification{notificationURL("https://github.com/o/r/issues/7")},
		threadMeta:    github.ResponseMetadata{NotModified: true},
		repoComments:  []github.RepositoryIssueComment{{Comment: github.Comment{ID: 33, HTMLURL: "https://github.com/o/r/issues/9#issuecomment-33", Body: "/new fallback", User: &github.User{Login: "alice"}}, IssueNumber: 9}},
	}
	r := Runner{Backend: b, Store: store, Allowlist: staticAllowlist{"alice": true}, Perms: staticPerms("write"), Now: func() time.Time { return now }}
	got, err := r.Poll(context.Background(), "o/r")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Candidates) != 1 || got.Candidates[0].IssueNumber != 9 {
		t.Fatalf("fallback candidates = %+v", got.Candidates)
	}
}

func TestPollRejectsDuplicateEditedCommentById(t *testing.T) {
	dir := t.TempDir()
	store := state.New(filepath.Join(dir, "state.json"))
	now := time.Unix(100, 0).UTC()
	b := &fakeBackend{
		notifications:  []github.Notification{notificationURL("https://github.com/o/r/issues/7")},
		threadComments: []github.Comment{{ID: 44, HTMLURL: "https://github.com/o/r/issues/7#issuecomment-44", Body: "/new first", User: &github.User{Login: "alice"}}},
	}
	r := Runner{Backend: b, Store: store, Allowlist: staticAllowlist{"alice": true}, Perms: staticPerms("write"), Now: func() time.Time { return now }}
	if _, err := r.Poll(context.Background(), "o/r"); err != nil {
		t.Fatal(err)
	}
	b.threadComments[0].Body = "/new edited"
	got, err := r.Poll(context.Background(), "o/r")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Candidates) != 0 {
		t.Fatalf("edited comment reran: %+v", got.Candidates)
	}
}

func TestPollPersistsPollIntervalMetadata(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := state.New(path)
	now := time.Unix(100, 0).UTC()
	b := &fakeBackend{
		notesMeta: github.ResponseMetadata{ETag: `"n2"`, PollInterval: "60"},
	}
	r := Runner{Backend: b, Store: store, Allowlist: staticAllowlist{"alice": true}, Perms: staticPerms("write"), Now: func() time.Time { return now }}
	if _, err := r.Poll(context.Background(), "o/r"); err != nil {
		t.Fatal(err)
	}
	st, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if st.Repos["o/r"].Validators.NotificationsETag != `"n2"` {
		t.Fatalf("validators = %+v", st.Repos["o/r"].Validators)
	}
	if st.Polling.PollInterval != "60" {
		t.Fatalf("polling = %+v", st.Polling)
	}
}
