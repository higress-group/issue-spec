package codec_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	client "github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/server/api/github/codec"
	"github.com/higress-group/issue-spec/internal/server/publicurl"
)

func presenter(t *testing.T) codec.Presenter {
	t.Helper()
	origins, err := publicurl.New("https://api.issues.test", "https://issues.test", nil)
	if err != nil {
		t.Fatal(err)
	}
	return codec.Presenter{Origins: origins}
}

func TestStableNumericIDIsJavaScriptSafeAndDeterministic(t *testing.T) {
	// This UUID produced 2702158502842464763 with the legacy 63-bit mask. A
	// browser rounded that value while constructing the comment reaction URL.
	const stable = "60ea2b7a-854d-417d-9d83-a0262f4a13bb"
	const want int64 = 9005925674908155

	got := codec.StableNumericID(stable)
	if got != want {
		t.Fatalf("StableNumericID(%q) = %d, want %d", stable, got, want)
	}
	if got <= 0 || got > codec.MaxSafeNumericID {
		t.Fatalf("StableNumericID(%q) = %d, outside JavaScript safe range", stable, got)
	}

	payload, err := json.Marshal(map[string]int64{"id": got})
	if err != nil {
		t.Fatal(err)
	}
	var browserValue map[string]float64
	if err := json.Unmarshal(payload, &browserValue); err != nil {
		t.Fatal(err)
	}
	if browserValue["id"] != float64(got) || int64(browserValue["id"]) != got {
		t.Fatalf("JSON number lost precision: encoded=%s decoded=%.0f want=%d", payload, browserValue["id"], got)
	}
}

func TestPresenterMatchesExistingGitHubClientFieldsAndPreservesRawBody(t *testing.T) {
	p := presenter(t)
	rawBody := "<!-- issue-spec:type=PROCESS id=PROCESS-006 version=1 -->\n中文  \\ slash\n"
	comment := p.PresentComment(codec.CommentView{
		StableID: "a3b", Owner: "higress-group", Repository: "issue-spec", IssueNumber: 162,
		Body: rawBody, Author: codec.UserView{StableID: "u1", Login: "alice"},
		CreatedAt: time.Date(2026, 7, 3, 1, 2, 3, 0, time.UTC), UpdatedAt: time.Date(2026, 7, 3, 1, 2, 4, 0, time.UTC),
		Reactions: codec.Reactions{TotalCount: 2, Eyes: 2},
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/higress-group/issue-spec/issues/162/comments" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode([]codec.Comment{comment})
	}))
	defer server.Close()
	githubClient := client.NewClientWithBaseURL("issues.test", server.URL, "", server.Client())
	comments, err := githubClient.ListIssueComments(context.Background(), "higress-group/issue-spec", 162)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 || comments[0].Body != rawBody || comments[0].User.Login != "alice" || comments[0].Reactions.Eyes != 2 {
		t.Fatalf("decoded comment = %+v", comments)
	}
	if comments[0].IssueURL != "https://api.issues.test/repos/higress-group/issue-spec/issues/162" {
		t.Fatalf("issue_url = %q", comments[0].IssueURL)
	}
	if !strings.Contains(comments[0].HTMLURL, "#issuecomment-") {
		t.Fatalf("html_url fragment missing: %q", comments[0].HTMLURL)
	}
}

func TestDTOsRoundTripThroughExistingGitHubRunnerClient(t *testing.T) {
	p := presenter(t)
	now := time.Date(2026, 7, 3, 1, 2, 3, 0, time.UTC)
	user := p.PresentUser(codec.UserView{StableID: "u1", Login: "alice"})
	issue := p.PresentIssue(codec.IssueView{
		StableID: "i1", Owner: "o", Repository: "r", Number: 7, State: "open", Title: "title", Body: "raw",
		Author: codec.UserView{StableID: "u1", Login: "alice"}, CreatedAt: now, UpdatedAt: now,
	})
	reaction := p.PresentReaction(codec.ReactionView{StableID: "reaction", Author: codec.UserView{StableID: "u1", Login: "alice"}, Content: "eyes", CreatedAt: now})
	permission := p.PresentPermission("write", "write", codec.UserView{StableID: "u1", Login: "alice"})
	subscription := codec.Subscription{Subscribed: true, CreatedAt: now, URL: "https://api.issues.test/repos/o/r/subscription", RepositoryURL: "https://api.issues.test/repos/o/r"}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/user":
			_ = json.NewEncoder(w).Encode(user)
		case "/repos/o/r/issues/7":
			_ = json.NewEncoder(w).Encode(issue)
		case "/repos/o/r/issues/comments/11/reactions":
			_ = json.NewEncoder(w).Encode([]codec.Reaction{reaction})
		case "/repos/o/r/collaborators/alice/permission":
			_ = json.NewEncoder(w).Encode(permission)
		case "/repos/o/r/subscription":
			_ = json.NewEncoder(w).Encode(subscription)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	githubClient := client.NewClientWithBaseURL("issues.test", server.URL, "", server.Client())
	gotUser, _, err := githubClient.GetUser(context.Background())
	if err != nil || gotUser.Login != "alice" {
		t.Fatalf("user = %+v err=%v", gotUser, err)
	}
	gotIssue, err := githubClient.GetIssue(context.Background(), "o/r", 7)
	if err != nil || gotIssue.Number != 7 || gotIssue.Body != "raw" || gotIssue.HTMLURL == "" || gotIssue.URL == "" {
		t.Fatalf("issue = %+v err=%v", gotIssue, err)
	}
	gotReactions, err := githubClient.ListCommentReactionsPage(context.Background(), "o/r", 11, client.RunnerPageOptions{})
	if err != nil || len(gotReactions.Reactions) != 1 || gotReactions.Reactions[0].Content != "eyes" || gotReactions.Reactions[0].User.Login != "alice" {
		t.Fatalf("reactions = %+v err=%v", gotReactions, err)
	}
	gotPermission, err := githubClient.GetCollaboratorPermission(context.Background(), "o/r", "alice")
	if err != nil || gotPermission.Permission.Permission != "write" || !gotPermission.CanWrite || gotPermission.Permission.User.Login != "alice" {
		t.Fatalf("permission = %+v err=%v", gotPermission, err)
	}
	gotSubscription, err := githubClient.GetRepositorySubscription(context.Background(), "o/r")
	if err != nil || !gotSubscription.Subscription.Subscribed || gotSubscription.Subscription.RepositoryURL == "" {
		t.Fatalf("subscription = %+v err=%v", gotSubscription, err)
	}
}

func TestIssuePresenterUsesOnlyConfiguredOrigins(t *testing.T) {
	p := presenter(t)
	issue := p.PresentIssue(codec.IssueView{
		StableID: "issue", Owner: "o", Repository: "r", Number: 7, State: "open", Title: "title", Body: "body",
		Author: codec.UserView{StableID: "user", Login: "alice"}, CreatedAt: time.Now(), UpdatedAt: time.Now(),
		Labels: []codec.LabelView{{StableID: "label", Owner: "o", Repository: "r", Name: "needs triage", Color: "#aabbcc"}},
	})
	data, err := json.Marshal(issue)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "evil") || !strings.Contains(issue.URL, "api.issues.test") || !strings.Contains(issue.HTMLURL, "issues.test") {
		t.Fatalf("issue URLs = %+v", issue)
	}
}

func TestInputDecodeAndValidation(t *testing.T) {
	body := "<!-- marker -->\n任意 UTF-8  \n"
	payload, _ := json.Marshal(map[string]any{"title": "spec", "body": body, "future_field": true})
	var input codec.CreateIssueInput
	if err := codec.DecodeJSON(bytes.NewReader(payload), &input); err != nil {
		t.Fatal(err)
	}
	if input.Body != body || len(input.Validate()) != 0 {
		t.Fatalf("input = %+v violations=%+v", input, input.Validate())
	}
	invalid := []any{
		codec.CreateIssueInput{Title: " "},
		codec.UpdateIssueInput{State: ptr("merged")},
		codec.CreateLabelInput{Name: "label", Color: "not-color"},
		codec.ReactionInput{Content: "thumbsup"},
	}
	for _, candidate := range invalid {
		var violations []codec.Violation
		switch value := candidate.(type) {
		case codec.CreateIssueInput:
			violations = value.Validate()
		case codec.UpdateIssueInput:
			violations = value.Validate()
		case codec.CreateLabelInput:
			violations = value.Validate()
		case codec.ReactionInput:
			violations = value.Validate()
		}
		if len(violations) == 0 {
			t.Fatalf("expected violation for %#v", candidate)
		}
	}
}

func TestSelfHostedCapabilitiesExcludeCodeHostAndNotifications(t *testing.T) {
	capabilities := codec.SelfHostedCapabilities()
	if !capabilities.Issues || !capabilities.RunnerServe {
		t.Fatalf("missing supported capabilities: %+v", capabilities)
	}
	if capabilities.Notifications || capabilities.PullRequests || capabilities.Reviews || capabilities.CommitStatuses || capabilities.CheckRuns {
		t.Fatalf("unsupported capability advertised: %+v", capabilities)
	}
}

func ptr(value string) *string { return &value }
