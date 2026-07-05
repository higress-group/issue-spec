package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
	state := "closed"
	tests := []struct {
		name      string
		opts      UpdateIssueOptions
		wantTitle bool
		wantBody  bool
		wantState bool
	}{
		{name: "title and body", opts: UpdateIssueOptions{Title: &title, Body: &body}, wantTitle: true, wantBody: true},
		{name: "title only", opts: UpdateIssueOptions{Title: &title}, wantTitle: true},
		{name: "body only", opts: UpdateIssueOptions{Body: &body}, wantBody: true},
		{name: "state only", opts: UpdateIssueOptions{State: &state}, wantState: true},
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
			if _, ok := payload["state"]; ok != tt.wantState {
				t.Fatalf("state payload presence = %v, want %v in %#v", ok, tt.wantState, payload)
			}
		})
	}
}

func TestClientUpdatesPullRequestBody(t *testing.T) {
	body := "updated PR body"
	var payload map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer token" {
			t.Fatalf("authorization header = %q", got)
		}
		if r.Method != http.MethodPatch || r.URL.Path != "/repos/o/r/pulls/7" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		json.NewEncoder(w).Encode(PullRequest{Number: 7, HTMLURL: "https://github.com/o/r/pull/7", Body: payload["body"]})
	}))
	defer server.Close()

	client := NewClientWithBaseURL("github.com", server.URL, "token", server.Client())
	updated, err := client.UpdatePullRequest(context.Background(), "o/r", 7, UpdatePullRequestOptions{Body: &body})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Number != 7 || updated.Body != body {
		t.Fatalf("unexpected update result: %+v", updated)
	}
	if payload["body"] != body || len(payload) != 1 {
		t.Fatalf("payload = %#v, want only body", payload)
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

func serverURL(r *http.Request) string {
	return "http://" + strings.TrimSuffix(r.Host, "/")
}
