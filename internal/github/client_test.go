package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientCreatesAndListsComments(t *testing.T) {
	var createdBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer token" {
			t.Fatalf("authorization header = %q", got)
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/issues/1/comments":
			var req map[string]string
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			createdBody = req["body"]
			json.NewEncoder(w).Encode(Comment{ID: 10, HTMLURL: "https://github.com/o/r/issues/1#issuecomment-10", URL: serverURL(r) + "/repos/o/r/issues/comments/10", Body: createdBody})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/issues/1/comments":
			if r.URL.Query().Get("per_page") != "100" {
				t.Fatalf("missing pagination query: %s", r.URL.RawQuery)
			}
			json.NewEncoder(w).Encode([]Comment{{ID: 10, HTMLURL: "https://github.com/o/r/issues/1#issuecomment-10", Body: createdBody}})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL("github.com", server.URL, "token", server.Client())
	created, err := client.CreateComment(context.Background(), "o/r", 1, "body")
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != 10 || createdBody != "body" {
		t.Fatalf("unexpected create result: %+v body=%q", created, createdBody)
	}
	comments, err := client.ListIssueComments(context.Background(), "o/r", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 || comments[0].ID != 10 {
		t.Fatalf("unexpected comments: %+v", comments)
	}
}

func TestClientUpdatesIssue(t *testing.T) {
	title := "new title"
	body := "new body"
	tests := []struct {
		name      string
		opts      UpdateIssueOptions
		wantTitle bool
		wantBody  bool
	}{
		{name: "title and body", opts: UpdateIssueOptions{Title: &title, Body: &body}, wantTitle: true, wantBody: true},
		{name: "title only", opts: UpdateIssueOptions{Title: &title}, wantTitle: true},
		{name: "body only", opts: UpdateIssueOptions{Body: &body}, wantBody: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var payload map[string]string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.Header.Get("Authorization"); got != "Bearer token" {
					t.Fatalf("authorization header = %q", got)
				}
				if r.Method != http.MethodPatch || r.URL.Path != "/repos/o/r/issues/5" {
					t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				json.NewEncoder(w).Encode(Issue{Number: 5, HTMLURL: "https://github.com/o/r/issues/5", Title: payload["title"], Body: payload["body"]})
			}))
			defer server.Close()

			client := NewClientWithBaseURL("github.com", server.URL, "token", server.Client())
			updated, err := client.UpdateIssue(context.Background(), "o/r", 5, tt.opts)
			if err != nil {
				t.Fatal(err)
			}
			if updated.Number != 5 {
				t.Fatalf("unexpected update result: %+v", updated)
			}
			if _, ok := payload["title"]; ok != tt.wantTitle {
				t.Fatalf("title payload presence = %v, want %v in %#v", ok, tt.wantTitle, payload)
			}
			if _, ok := payload["body"]; ok != tt.wantBody {
				t.Fatalf("body payload presence = %v, want %v in %#v", ok, tt.wantBody, payload)
			}
		})
	}
}

func TestClientAPIErrorRedactsTokenInResponseBody(t *testing.T) {
	const secret = "rest-api-error-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+secret {
			t.Fatalf("authorization header = %q", got)
		}
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"message":"upstream echoed ` + secret + `"}`))
	}))
	defer server.Close()

	client := NewClientWithBaseURL("github.com", server.URL, secret, server.Client())
	_, _, err := client.GetUser(context.Background())
	if err == nil {
		t.Fatal("GetUser succeeded, want API error")
	}
	var apiErr *APIError
	if !errorAsAPI(err, &apiErr) {
		t.Fatalf("error %T is not APIError: %v", err, err)
	}
	if strings.Contains(apiErr.Body, secret) || strings.Contains(err.Error(), secret) {
		t.Fatalf("API error leaked token: body=%q error=%q", apiErr.Body, err.Error())
	}
	if !strings.Contains(apiErr.Body, "[REDACTED]") || !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("API error missing redaction marker: body=%q error=%q", apiErr.Body, err.Error())
	}
}

func TestParseIssueNumberFromURL(t *testing.T) {
	n, err := ParseIssueNumber("https://github.com/o/r/issues/123")
	if err != nil {
		t.Fatal(err)
	}
	if n != 123 {
		t.Fatalf("number = %d", n)
	}
}

func TestRunnerRESTMetadataAndPagination(t *testing.T) {
	var notificationCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/notifications":
			notificationCalls++
			if got := r.Header.Get("If-None-Match"); notificationCalls == 2 && got != `"etag-1"` {
				t.Fatalf("if-none-match = %q", got)
			}
			if notificationCalls == 1 {
				w.Header().Set("ETag", `"etag-1"`)
				w.Header().Set("X-Poll-Interval", "60")
				w.Header().Set("X-RateLimit-Limit", "5000")
				w.Header().Set("X-RateLimit-Remaining", "4999")
				w.Header().Set("X-RateLimit-Reset", "123")
				json.NewEncoder(w).Encode([]Notification{{ID: "n1", Unread: true}, {ID: "n2", Unread: true}})
				return
			}
			w.WriteHeader(http.StatusNotModified)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/subscription":
			http.NotFound(w, r)
		case r.Method == http.MethodPut && r.URL.Path == "/repos/o/r/subscription":
			var req map[string]any
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			if req["subscribed"] != true {
				t.Fatalf("subscription request = %#v", req)
			}
			w.Header().Set("X-Poll-Interval", "45")
			w.Header().Set("X-RateLimit-Remaining", "4988")
			json.NewEncoder(w).Encode(Subscription{Subscribed: true, Ignored: false})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/issues/comments":
			if r.URL.Query().Get("since") == "" {
				t.Fatalf("missing since query: %s", r.URL.RawQuery)
			}
			json.NewEncoder(w).Encode([]RepositoryIssueComment{{Comment: Comment{ID: 7, Body: "repo comment"}, IssueNumber: 9, IssueURL: "https://github.com/o/r/issues/9"}})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/collaborators/alice/permission":
			w.Header().Set("X-RateLimit-Remaining", "4987")
			json.NewEncoder(w).Encode(Permission{Permission: "write"})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/issues/9/comments":
			json.NewEncoder(w).Encode([]Comment{{ID: 99, Body: "thread comment"}})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL("github.com", server.URL, "token", server.Client())
	notes, meta, err := client.ListNotifications(context.Background(), NotificationListOptions{ETag: `"etag-1"`})
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 2 || meta.PollInterval != "60" || meta.RateLimitRemaining != 4999 {
		t.Fatalf("notifications = %#v meta=%+v", notes, meta)
	}
	if notes[0].ID != "n1" {
		t.Fatalf("notifications = %#v", notes)
	}
	_, meta, err = client.ListNotifications(context.Background(), NotificationListOptions{ETag: `"etag-1"`})
	if err != nil {
		t.Fatal(err)
	}
	if !meta.NotModified || meta.StatusCode != http.StatusNotModified {
		t.Fatalf("not modified meta = %+v", meta)
	}
	sub, subMeta, err := client.WatchRepository(context.Background(), "o/r")
	if err != nil {
		t.Fatal(err)
	}
	if !sub.Subscribed || subMeta.PollInterval != "45" || subMeta.RateLimitRemaining != 4988 {
		t.Fatalf("subscription = %+v meta=%+v", sub, subMeta)
	}
	threadComments, _, err := client.GetNotificationComments(context.Background(), server.URL+"/repos/o/r/issues/9", NotificationCommentOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(threadComments) != 1 || threadComments[0].ID != 99 {
		t.Fatalf("thread comments = %#v", threadComments)
	}
	since := time.Unix(1, 0)
	repoComments, _, err := client.ListRepositoryIssueComments(context.Background(), "o/r", RepositoryIssueCommentOptions{Since: &since})
	if err != nil {
		t.Fatal(err)
	}
	if len(repoComments) != 1 || repoComments[0].IssueNumber != 9 || repoComments[0].IssueURL == "" {
		t.Fatalf("repo comments = %#v", repoComments)
	}
	perm, permMeta, err := client.GetCollaboratorPermission(context.Background(), "o/r", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if perm.Permission != "write" || permMeta.RateLimitRemaining != 4987 {
		t.Fatalf("permission = %+v meta=%+v", perm, permMeta)
	}
}

func serverURL(r *http.Request) string {
	return "http://" + strings.TrimSuffix(r.Host, "/")
}
