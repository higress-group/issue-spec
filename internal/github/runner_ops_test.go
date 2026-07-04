package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRunnerPollNotificationsHandles304AndMetadata(t *testing.T) {
	since := time.Date(2026, 7, 3, 8, 9, 10, 0, time.UTC)
	var requested bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = true
		if r.Method != http.MethodGet || r.URL.Path != "/notifications" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
		if got := r.Header.Get("If-None-Match"); got != `"etag-1"` {
			t.Fatalf("If-None-Match = %q", got)
		}
		if got := r.Header.Get("If-Modified-Since"); got != "Fri, 03 Jul 2026 08:00:00 GMT" {
			t.Fatalf("If-Modified-Since = %q", got)
		}
		if got := r.URL.Query().Get("since"); got != since.Format(time.RFC3339) {
			t.Fatalf("since = %q", got)
		}
		if got := r.URL.Query().Get("per_page"); got != "50" {
			t.Fatalf("per_page = %q", got)
		}
		if got := r.URL.Query().Get("all"); got != "true" {
			t.Fatalf("all = %q", got)
		}
		w.Header().Set("ETag", `"etag-2"`)
		w.Header().Set("Last-Modified", "Fri, 03 Jul 2026 08:10:00 GMT")
		w.Header().Set("X-Poll-Interval", "60")
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "4999")
		w.Header().Set("X-RateLimit-Used", "1")
		w.Header().Set("X-RateLimit-Reset", "1783066200")
		w.Header().Set("X-RateLimit-Resource", "core")
		w.WriteHeader(http.StatusNotModified)
	}))
	defer server.Close()

	client := NewClientWithBaseURL("github.com", server.URL, "token", server.Client())
	result, err := client.PollNotifications(context.Background(), NotificationListOptions{
		ConditionalRequest: ConditionalRequest{ETag: `"etag-1"`, LastModified: "Fri, 03 Jul 2026 08:00:00 GMT"},
		Since:              &since,
		All:                true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !requested {
		t.Fatal("server was not requested")
	}
	if !result.Metadata.NotModified || result.Metadata.StatusCode != http.StatusNotModified {
		t.Fatalf("metadata = %+v, want normal 304 no-change", result.Metadata)
	}
	if result.Metadata.ETag != `"etag-2"` || result.Metadata.LastModified == "" {
		t.Fatalf("conditional metadata = %+v", result.Metadata)
	}
	if result.Metadata.PollIntervalSeconds != 60 || result.Metadata.PollInterval != time.Minute {
		t.Fatalf("poll interval = %d/%s", result.Metadata.PollIntervalSeconds, result.Metadata.PollInterval)
	}
	if result.Metadata.RateLimit.Limit != 5000 || result.Metadata.RateLimit.Remaining != 4999 || result.Metadata.RateLimit.Resource != "core" {
		t.Fatalf("rate limit = %+v", result.Metadata.RateLimit)
	}
}

func TestRunnerRepositoryIssueCommentsFallbackPaginationAndCursor(t *testing.T) {
	since := time.Date(2026, 7, 3, 9, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer token" {
			t.Fatalf("authorization = %q", got)
		}
		if r.Method != http.MethodGet || r.URL.Path != "/repos/o/r/issues/comments" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
		switch r.URL.Query().Get("page") {
		case "1", "":
			if got := r.URL.Query().Get("since"); got != since.Format(time.RFC3339) {
				t.Fatalf("since = %q", got)
			}
			if got := r.URL.Query().Get("per_page"); got != "2" {
				t.Fatalf("per_page = %q", got)
			}
			w.Header().Set("ETag", `"comments-1"`)
			w.Header().Set("Link", `<`+serverURL(r)+`/repos/o/r/issues/comments?per_page=2&page=2>; rel="next", <`+serverURL(r)+`/repos/o/r/issues/comments?per_page=2&page=3>; rel="last"`)
			w.Header().Set("X-RateLimit-Remaining", "42")
			json.NewEncoder(w).Encode([]Comment{
				{ID: 11, IssueURL: serverURL(r) + "/repos/o/r/issues/7", HTMLURL: "https://github.com/o/r/issues/7#issuecomment-11", Body: "/new one", UpdatedAt: since},
				{ID: 12, IssueURL: serverURL(r) + "/repos/o/r/issues/8", HTMLURL: "https://github.com/o/r/issues/8#issuecomment-12", Body: "/new two", UpdatedAt: since},
			})
		case "2":
			json.NewEncoder(w).Encode([]Comment{
				{ID: 13, IssueURL: serverURL(r) + "/repos/o/r/issues/9", HTMLURL: "https://github.com/o/r/issues/9#issuecomment-13", Body: "/new three", UpdatedAt: since},
			})
		default:
			t.Fatalf("unexpected page query %q", r.URL.RawQuery)
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL("github.com", server.URL, "token", server.Client())
	first, err := client.ListRepositoryIssueCommentsPage(context.Background(), "o/r", CommentListOptions{
		Page:  RunnerPageOptions{PerPage: 2},
		Since: &since,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Comments) != 2 || first.Comments[0].IssueNumber != 7 || first.Comments[1].IssueNumber != 8 {
		t.Fatalf("first comments = %+v", first.Comments)
	}
	if first.Metadata.ETag != `"comments-1"` || first.Metadata.RateLimit.Remaining != 42 {
		t.Fatalf("metadata = %+v", first.Metadata)
	}
	if first.Metadata.Pagination.NextURL == "" || first.Metadata.Pagination.NextPage != 2 || first.Metadata.Pagination.LastPage != 3 {
		t.Fatalf("pagination = %+v", first.Metadata.Pagination)
	}

	second, err := client.ListRepositoryIssueCommentsPage(context.Background(), "o/r", CommentListOptions{
		Page: RunnerPageOptions{CursorURL: first.Metadata.Pagination.NextURL},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Comments) != 1 || second.Comments[0].IssueNumber != 9 {
		t.Fatalf("second comments = %+v", second.Comments)
	}
}

func TestRunnerIssueContextAndPerIssueCommentsUseConditionals(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/issues/5":
			if got := r.Header.Get("If-None-Match"); got != `"issue-etag"` {
				t.Fatalf("issue If-None-Match = %q", got)
			}
			w.Header().Set("ETag", `"issue-etag-2"`)
			json.NewEncoder(w).Encode(Issue{Number: 5, HTMLURL: "https://github.com/o/r/issues/5", Title: "issue"})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/issues/5/comments":
			if got := r.Header.Get("If-Modified-Since"); got != "Fri, 03 Jul 2026 09:00:00 GMT" {
				t.Fatalf("comments If-Modified-Since = %q", got)
			}
			if got := r.URL.Query().Get("per_page"); got != "100" {
				t.Fatalf("per_page = %q", got)
			}
			json.NewEncoder(w).Encode([]Comment{{ID: 20, Body: "comment"}})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL("github.com", server.URL, "", server.Client())
	issue, err := client.GetIssueContext(context.Background(), "o/r", 5, ConditionalRequest{ETag: `"issue-etag"`})
	if err != nil {
		t.Fatal(err)
	}
	if issue.Issue.Number != 5 || issue.Metadata.ETag != `"issue-etag-2"` {
		t.Fatalf("issue result = %+v", issue)
	}
	comments, err := client.ListIssueCommentsPage(context.Background(), "o/r", 5, CommentListOptions{
		ConditionalRequest: ConditionalRequest{LastModified: "Fri, 03 Jul 2026 09:00:00 GMT"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(comments.Comments) != 1 || comments.Comments[0].IssueNumber != 5 {
		t.Fatalf("comments = %+v", comments.Comments)
	}
}

func TestRunnerSubscriptionPermissionAndWriteMapping(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/subscription":
			json.NewEncoder(w).Encode(RepositorySubscription{Subscribed: true, Ignored: false, Reason: "subscribed"})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/collaborators/octocat/permission":
			json.NewEncoder(w).Encode(CollaboratorPermission{Permission: "maintain", RoleName: "maintain", User: &User{Login: "octocat"}})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL("github.com", server.URL, "", server.Client())
	subscription, err := client.GetRepositorySubscription(context.Background(), "o/r")
	if err != nil {
		t.Fatal(err)
	}
	if !subscription.Subscription.Subscribed || subscription.Subscription.Ignored {
		t.Fatalf("subscription = %+v", subscription.Subscription)
	}

	permission, err := client.GetCollaboratorPermission(context.Background(), "o/r", "octocat")
	if err != nil {
		t.Fatal(err)
	}
	if !permission.CanWrite || permission.Permission.Permission != "maintain" {
		t.Fatalf("permission = %+v", permission)
	}
	for _, value := range []string{"write", "maintain", "admin"} {
		if !permissionAllowsWrite(value) {
			t.Fatalf("%q should be write-equivalent", value)
		}
	}
	for _, value := range []string{"read", "triage", "none", ""} {
		if permissionAllowsWrite(value) {
			t.Fatalf("%q should not be write-equivalent", value)
		}
	}
}

func TestRunnerCommentCreateUpdateReturnMetadata(t *testing.T) {
	var createdBody, updatedBody, reactionContent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/issues/5/comments":
			var payload map[string]string
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			createdBody = payload["body"]
			w.Header().Set("ETag", `"created"`)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(Comment{ID: 30, HTMLURL: "https://github.com/o/r/issues/5#issuecomment-30", Body: createdBody})
		case r.Method == http.MethodPatch && r.URL.Path == "/repos/o/r/issues/comments/30":
			var payload map[string]string
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			updatedBody = payload["body"]
			w.Header().Set("Retry-After", "3")
			json.NewEncoder(w).Encode(Comment{ID: 30, IssueURL: serverURL(r) + "/repos/o/r/issues/5", HTMLURL: "https://github.com/o/r/issues/5#issuecomment-30", Body: updatedBody})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/issues/comments/30/reactions":
			var payload map[string]string
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			reactionContent = payload["content"]
			w.Header().Set("X-RateLimit-Remaining", "41")
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, `{"id":1,"content":"eyes"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/issues/comments/30/reactions":
			if got := r.URL.Query().Get("per_page"); got != "100" {
				t.Fatalf("reaction per_page = %q", got)
			}
			w.Header().Set("X-RateLimit-Remaining", "40")
			json.NewEncoder(w).Encode([]Reaction{{ID: 1, User: &User{Login: "bot"}, Content: "eyes"}})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL("github.com", server.URL, "", server.Client())
	created, err := client.CreateRunnerComment(context.Background(), "o/r", 5, "queued")
	if err != nil {
		t.Fatal(err)
	}
	if created.Comment.ID != 30 || created.Comment.IssueNumber != 5 || createdBody != "queued" {
		t.Fatalf("created = %+v body=%q", created, createdBody)
	}
	if created.Metadata.StatusCode != http.StatusCreated || created.Metadata.ETag != `"created"` {
		t.Fatalf("created metadata = %+v", created.Metadata)
	}

	updated, err := client.UpdateRunnerComment(context.Background(), "o/r", 30, "completed")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Comment.ID != 30 || updated.Comment.IssueNumber != 5 || updatedBody != "completed" {
		t.Fatalf("updated = %+v body=%q", updated, updatedBody)
	}
	if updated.Metadata.RateLimit.RetryAfterSeconds != 3 {
		t.Fatalf("updated metadata = %+v", updated.Metadata)
	}

	reaction, err := client.AddCommentReaction(context.Background(), "o/r", 30, "eyes")
	if err != nil {
		t.Fatal(err)
	}
	if reactionContent != "eyes" {
		t.Fatalf("reaction content = %q", reactionContent)
	}
	if reaction.Metadata.StatusCode != http.StatusCreated || reaction.Metadata.RateLimit.Remaining != 41 {
		t.Fatalf("reaction metadata = %+v", reaction.Metadata)
	}

	reactions, err := client.ListCommentReactionsPage(context.Background(), "o/r", 30, RunnerPageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(reactions.Reactions) != 1 || reactions.Reactions[0].User.Login != "bot" || reactions.Reactions[0].Content != "eyes" {
		t.Fatalf("listed reactions = %+v", reactions.Reactions)
	}
	if reactions.Metadata.RateLimit.Remaining != 40 {
		t.Fatalf("listed reactions metadata = %+v", reactions.Metadata)
	}
}

func TestRunnerRESTClientSupportsFakeHTTPDoer(t *testing.T) {
	client := NewClientWithHTTPDoer("github.com", "https://api.example.test", "", roundTripDoer(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://api.example.test/repos/o/r/issues/1" {
			t.Fatalf("url = %s", r.URL.String())
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Etag": []string{`"issue"`}},
			Body:       io.NopCloser(strings.NewReader(`{"number":1,"html_url":"https://github.com/o/r/issues/1","title":"from fake doer"}`)),
			Request:    r,
		}, nil
	}))

	result, err := client.GetIssueContext(context.Background(), "o/r", 1, ConditionalRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Issue.Number != 1 || result.Metadata.ETag != `"issue"` {
		t.Fatalf("result = %+v", result)
	}
}

type roundTripDoer func(*http.Request) (*http.Response, error)

func (d roundTripDoer) Do(req *http.Request) (*http.Response, error) {
	return d(req)
}
