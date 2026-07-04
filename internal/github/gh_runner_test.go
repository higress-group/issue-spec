package github

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestGHRunnerPollNotificationsUsesIncludedConditionalAPI(t *testing.T) {
	runner := &sequenceCLIRunner{results: []ExternalCLIResult{{
		Stdout: []byte(string(includedHTTP(http.StatusOK, map[string]string{"ETag": `"page1"`}, `[
			{"id":"n1","unread":true,"reason":"mention","subject":{"type":"Issue","url":"https://api.github.com/repos/o/r/issues/7","latest_comment_url":"https://api.github.com/repos/o/r/issues/comments/101"},"repository":{"full_name":"o/r"}}
		]`)) + "\n" + string(includedHTTP(http.StatusOK, map[string]string{
			"ETag":                  `"notif-etag"`,
			"Last-Modified":         "Fri, 03 Jul 2026 10:00:00 GMT",
			"X-Poll-Interval":       "60",
			"X-RateLimit-Remaining": "4998",
			"X-RateLimit-Reset":     "1783066200",
			"Retry-After":           "7",
		}, `[
			{"id":"n2","unread":true,"reason":"mention","subject":{"type":"PullRequest","url":"https://api.github.com/repos/o/r/issues/8"},"repository":{"full_name":"o/r"}}
		]`))),
	}}}
	backend := newTestGHBackend(t, "ghe.example.com", runner)
	since := time.Date(2026, 7, 3, 10, 0, 0, 0, time.UTC)

	result, err := backend.PollNotifications(context.Background(), NotificationListOptions{
		ConditionalRequest: ConditionalRequest{ETag: `"old"`, LastModified: "Fri, 03 Jul 2026 09:00:00 GMT"},
		All:                true,
		Since:              &since,
		Page:               RunnerPageOptions{PerPage: 50},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Notifications) != 2 || result.Notifications[0].Subject.Type != "Issue" || result.Notifications[1].Subject.Type != "PullRequest" || result.Notifications[0].Repository.FullName != "o/r" {
		t.Fatalf("notifications = %+v", result.Notifications)
	}
	if result.Metadata.StatusCode != http.StatusOK || result.Metadata.PollIntervalSeconds != 60 || result.Metadata.RateLimit.Remaining != 4998 {
		t.Fatalf("metadata = %+v", result.Metadata)
	}
	if result.Metadata.RateLimit.ResetAt != time.Unix(1783066200, 0).UTC() {
		t.Fatalf("rate limit reset = %+v", result.Metadata.RateLimit)
	}
	if result.Metadata.RateLimit.RetryAfterSeconds != 7 {
		t.Fatalf("retry after = %+v", result.Metadata.RateLimit)
	}
	wantArgs := []string{
		"api",
		"--method", http.MethodGet,
		"--header", githubAPIVersion,
		"--header", "If-Modified-Since: Fri, 03 Jul 2026 09:00:00 GMT",
		"--header", `If-None-Match: "old"`,
		"--hostname", "ghe.example.com",
		"--paginate",
		"--include",
		"/notifications?all=true&per_page=50&since=2026-07-03T10%3A00%3A00Z",
	}
	if !reflect.DeepEqual(runner.commands[0].Args, wantArgs) {
		t.Fatalf("args = %#v, want %#v", runner.commands[0].Args, wantArgs)
	}
}

func TestGHRunnerConditionalHeadersIgnoreEmptyQuotedETag(t *testing.T) {
	headers := conditionalHeaders(`""`, "Sat, 04 Jul 2026 10:53:22 GMT")
	if got := headers.Get("If-None-Match"); got != "" {
		t.Fatalf("If-None-Match = %q, want omitted", got)
	}
	if got := headers.Get("If-Modified-Since"); got != "Sat, 04 Jul 2026 10:53:22 GMT" {
		t.Fatalf("If-Modified-Since = %q", got)
	}
	meta := metadataFromHeaders(http.StatusOK, http.Header{"Etag": []string{`""`}})
	if meta.ETag != "" {
		t.Fatalf("metadata ETag = %q, want empty", meta.ETag)
	}
}

func TestGHRunnerTreats304AsNoChange(t *testing.T) {
	runner := &sequenceCLIRunner{
		results: []ExternalCLIResult{{
			Stdout:   includedHTTP(http.StatusNotModified, map[string]string{"ETag": `"same"`, "X-Poll-Interval": "90"}, ""),
			ExitCode: 1,
		}},
		errs: []error{errors.New("exit status 1")},
	}
	backend := newTestGHBackend(t, "github.com", runner)

	result, err := backend.PollNotifications(context.Background(), NotificationListOptions{ConditionalRequest: ConditionalRequest{ETag: `"same"`}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Metadata.NotModified || result.Metadata.StatusCode != http.StatusNotModified || result.Metadata.PollIntervalSeconds != 90 {
		t.Fatalf("metadata = %+v", result.Metadata)
	}
	if len(result.Notifications) != 0 {
		t.Fatalf("notifications = %+v, want none", result.Notifications)
	}
}
func TestGHRunnerCommentListingAndWritePrimitives(t *testing.T) {
	runner := &sequenceCLIRunner{results: []ExternalCLIResult{
		{Stdout: includedHTTP(http.StatusOK, nil, `[
			{"id":101,"issue_url":"https://api.github.com/repos/o/r/issues/7","body":"one"},
			{"id":102,"issue_url":"https://api.github.com/repos/o/r/issues/8","body":"two"}
		]`)},
		{Stdout: includedHTTP(http.StatusOK, nil, `[{"id":201,"body":"issue comment"}]`)},
		{Stdout: includedHTTP(http.StatusCreated, nil, `{"id":301,"issue_url":"https://api.github.com/repos/o/r/issues/7","body":"created"}`)},
		{Stdout: includedHTTP(http.StatusOK, nil, `{"id":301,"issue_url":"https://api.github.com/repos/o/r/issues/7","body":"updated"}`)},
		{Stdout: includedHTTP(http.StatusCreated, map[string]string{"X-RateLimit-Remaining": "40"}, `{"id":401,"content":"eyes"}`)},
		{Stdout: includedHTTP(http.StatusOK, nil, `[{"id":401,"content":"eyes","user":{"login":"bot"}}]`)},
	}}
	backend := newTestGHBackend(t, "github.com", runner)

	since := time.Date(2026, 7, 3, 10, 0, 0, 0, time.UTC)
	repoComments, err := backend.ListRepositoryIssueCommentsPage(context.Background(), "o/r", CommentListOptions{Since: &since})
	if err != nil {
		t.Fatal(err)
	}
	if len(repoComments.Comments) != 2 || repoComments.Comments[0].IssueNumber != 7 || repoComments.Comments[1].IssueNumber != 8 {
		t.Fatalf("repo comments = %+v", repoComments.Comments)
	}
	wantArgs := []string{"api", "--method", http.MethodGet, "--header", githubAPIVersion, "--paginate", "--include", "/repos/o/r/issues/comments?per_page=100&since=2026-07-03T10%3A00%3A00Z"}
	if !reflect.DeepEqual(runner.commands[0].Args, wantArgs) {
		t.Fatalf("repo comments args = %#v, want %#v", runner.commands[0].Args, wantArgs)
	}

	issueComments, err := backend.ListIssueCommentsPage(context.Background(), "o/r", 7, CommentListOptions{Page: RunnerPageOptions{PerPage: 25}})
	if err != nil {
		t.Fatal(err)
	}
	if len(issueComments.Comments) != 1 || issueComments.Comments[0].IssueNumber != 7 {
		t.Fatalf("issue comments = %+v", issueComments.Comments)
	}
	wantArgs = []string{"api", "--method", http.MethodGet, "--header", githubAPIVersion, "--paginate", "--include", "/repos/o/r/issues/7/comments?per_page=25"}
	if !reflect.DeepEqual(runner.commands[1].Args, wantArgs) {
		t.Fatalf("issue comments args = %#v, want %#v", runner.commands[1].Args, wantArgs)
	}
	created, err := backend.CreateRunnerComment(context.Background(), "o/r", 7, "created")
	if err != nil {
		t.Fatal(err)
	}
	if created.Comment.ID != 301 || created.Comment.IssueNumber != 7 || created.Metadata.StatusCode != http.StatusCreated {
		t.Fatalf("created = %+v", created)
	}
	wantArgs = []string{"api", "--method", http.MethodPost, "--header", githubAPIVersion, "--include", "--input", "-", "/repos/o/r/issues/7/comments"}
	if !reflect.DeepEqual(runner.commands[2].Args, wantArgs) {
		t.Fatalf("create args = %#v, want %#v", runner.commands[2].Args, wantArgs)
	}
	assertJSONBody(t, runner.commands[2].Stdin, map[string]any{"body": "created"})

	updated, err := backend.UpdateRunnerComment(context.Background(), "o/r", 301, "updated")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Comment.Body != "updated" || updated.Comment.IssueNumber != 7 {
		t.Fatalf("updated = %+v", updated)
	}
	wantArgs = []string{"api", "--method", http.MethodPatch, "--header", githubAPIVersion, "--include", "--input", "-", "/repos/o/r/issues/comments/301"}
	if !reflect.DeepEqual(runner.commands[3].Args, wantArgs) {
		t.Fatalf("update args = %#v, want %#v", runner.commands[3].Args, wantArgs)
	}
	assertJSONBody(t, runner.commands[3].Stdin, map[string]any{"body": "updated"})

	reaction, err := backend.AddCommentReaction(context.Background(), "o/r", 301, "eyes")
	if err != nil {
		t.Fatal(err)
	}
	if reaction.Metadata.StatusCode != http.StatusCreated || reaction.Metadata.RateLimit.Remaining != 40 {
		t.Fatalf("reaction metadata = %+v", reaction.Metadata)
	}
	wantArgs = []string{"api", "--method", http.MethodPost, "--header", githubAPIVersion, "--include", "--input", "-", "/repos/o/r/issues/comments/301/reactions"}
	if !reflect.DeepEqual(runner.commands[4].Args, wantArgs) {
		t.Fatalf("reaction args = %#v, want %#v", runner.commands[4].Args, wantArgs)
	}
	assertJSONBody(t, runner.commands[4].Stdin, map[string]any{"content": "eyes"})

	reactions, err := backend.ListCommentReactionsPage(context.Background(), "o/r", 301, RunnerPageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(reactions.Reactions) != 1 || reactions.Reactions[0].User.Login != "bot" || reactions.Reactions[0].Content != "eyes" {
		t.Fatalf("listed reactions = %+v", reactions.Reactions)
	}
	wantArgs = []string{"api", "--method", http.MethodGet, "--header", githubAPIVersion, "--paginate", "--include", "/repos/o/r/issues/comments/301/reactions?per_page=100"}
	if !reflect.DeepEqual(runner.commands[5].Args, wantArgs) {
		t.Fatalf("reaction list args = %#v, want %#v", runner.commands[5].Args, wantArgs)
	}
}

func TestGHRunnerIssueContextPermissionAndPreflight(t *testing.T) {
	runner := &sequenceCLIRunner{results: []ExternalCLIResult{
		{Stdout: includedHTTP(http.StatusOK, nil, `{"number":7,"html_url":"https://github.com/o/r/issues/7","title":"Issue","state":"open"}`)},
		{Stdout: includedHTTP(http.StatusOK, nil, `{"permission":"maintain","role_name":"maintain","user":{"login":"octocat"}}`)},
		{},
		{Stdout: includedHTTP(http.StatusOK, nil, `{"login":"runner"}`)},
		{Stdout: includedHTTP(http.StatusOK, nil, `{"subscribed":true,"ignored":false,"reason":"subscribed"}`)},
	}}
	backend := newTestGHBackend(t, "github.com", runner)

	issue, err := backend.GetIssueContext(context.Background(), "o/r", 7, ConditionalRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if issue.Issue.Number != 7 || issue.Issue.Title != "Issue" {
		t.Fatalf("issue = %+v", issue)
	}

	permission, err := backend.GetCollaboratorPermission(context.Background(), "o/r", "octo cat")
	if err != nil {
		t.Fatal(err)
	}
	if !permission.CanWrite || permission.Permission.User.Login != "octocat" {
		t.Fatalf("permission = %+v", permission)
	}
	wantArgs := []string{"api", "--method", http.MethodGet, "--header", githubAPIVersion, "--include", "/repos/o/r/collaborators/octo%20cat/permission"}
	if !reflect.DeepEqual(runner.commands[1].Args, wantArgs) {
		t.Fatalf("permission args = %#v, want %#v", runner.commands[1].Args, wantArgs)
	}

	preflight, err := backend.CheckRunnerPreflight(context.Background(), "o/r")
	if err != nil {
		t.Fatal(err)
	}
	if preflight.User.Login != "runner" || !preflight.Subscription.Subscribed || preflight.Backend.Name != "gh" {
		t.Fatalf("preflight = %+v", preflight)
	}
	wantAuthArgs := []string{"auth", "status", "--active"}
	if !reflect.DeepEqual(runner.commands[2].Args, wantAuthArgs) {
		t.Fatalf("auth args = %#v, want %#v", runner.commands[2].Args, wantAuthArgs)
	}
}

func TestGHRunnerErrorClassificationAndRedaction(t *testing.T) {
	t.Run("missing gh", func(t *testing.T) {
		runner := &recordingCLIRunner{
			result: ExternalCLIResult{ExitCode: -1},
			err:    errors.New(`exec: "gh": executable file not found in $PATH`),
		}
		backend := newTestGHBackend(t, "github.com", runner)
		err := backend.CheckRunnerAuth(context.Background())
		if !IsGHRunnerErrorKind(err, GHRunnerErrorMissingCLI) {
			t.Fatalf("err = %v, want missing gh", err)
		}
	})

	t.Run("auth api", func(t *testing.T) {
		runner := &recordingCLIRunner{
			result: ExternalCLIResult{
				Stdout:   includedHTTP(http.StatusUnauthorized, nil, `{"message":"requires authentication"}`),
				ExitCode: 1,
			},
			err: errors.New("exit status 1"),
		}
		backend := newTestGHBackend(t, "github.com", runner)
		_, err := backend.PollNotifications(context.Background(), NotificationListOptions{})
		if !IsGHRunnerErrorKind(err, GHRunnerErrorAuth) {
			t.Fatalf("err = %v, want auth classification", err)
		}
	})

	t.Run("api redaction", func(t *testing.T) {
		secret := "ghp_secret"
		runner := &recordingCLIRunner{
			result: ExternalCLIResult{
				Stdout:   includedHTTP(http.StatusInternalServerError, nil, `{"message":"server failed"}`),
				Stderr:   []byte("token " + secret + " rejected"),
				ExitCode: 1,
			},
			err: errors.New("exit status 1"),
		}
		backend, err := NewGHBackend(GHBackendOptions{
			Host: "ghe.example.com",
			CLIOptions: GHCLIOptions{
				Runner:   runner,
				Redactor: NewExternalCLIRedactor(secret),
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = backend.PollNotifications(context.Background(), NotificationListOptions{})
		if !IsGHRunnerErrorKind(err, GHRunnerErrorAPI) {
			t.Fatalf("err = %v, want api classification", err)
		}
		message := err.Error()
		if strings.Contains(message, secret) {
			t.Fatalf("error leaked secret: %s", message)
		}
		for _, want := range []string{"HTTP 500", "operation PollNotifications", "[REDACTED]", "rejected"} {
			if !strings.Contains(message, want) {
				t.Fatalf("error %q missing %q", message, want)
			}
		}
	})
}

func TestGHRunnerPreflightRejectsUnwatchedRepository(t *testing.T) {
	runner := &sequenceCLIRunner{results: []ExternalCLIResult{
		{},
		{Stdout: includedHTTP(http.StatusOK, nil, `{"login":"runner"}`)},
		{Stdout: includedHTTP(http.StatusOK, nil, `{"subscribed":false,"ignored":false}`)},
	}}
	backend := newTestGHBackend(t, "github.com", runner)

	_, err := backend.CheckRunnerPreflight(context.Background(), "o/r")
	if !IsGHRunnerErrorKind(err, GHRunnerErrorPreflight) {
		t.Fatalf("err = %v, want preflight classification", err)
	}
}

func includedHTTP(status int, headers map[string]string, body string) []byte {
	var b strings.Builder
	b.WriteString("HTTP/2.0 ")
	b.WriteString(strconv.Itoa(status))
	if text := http.StatusText(status); text != "" {
		b.WriteByte(' ')
		b.WriteString(text)
	}
	b.WriteByte('\n')
	for key, value := range headers {
		b.WriteString(key)
		b.WriteString(": ")
		b.WriteString(value)
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	b.WriteString(body)
	return []byte(b.String())
}
