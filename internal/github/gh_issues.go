package github

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

func (b *GHBackend) GetUser(ctx context.Context) (User, []string, error) {
	var user User
	metadata, err := b.runRunnerJSON(ctx, ExternalCLIAPIRequest{
		Operation: "GetUser",
		Method:    http.MethodGet,
		Endpoint:  "/user",
	}, &user)
	if err != nil {
		return User{}, nil, err
	}
	return user, splitScopes(metadata.Headers.Get("X-OAuth-Scopes")), nil
}

func (b *GHBackend) CreateIssue(ctx context.Context, repo, title, body string, labels []string) (Issue, error) {
	payload := map[string]any{"title": title, "body": body}
	if len(labels) > 0 {
		payload["labels"] = labels
	}
	var issue Issue
	err := b.runJSON(ctx, ExternalCLIAPIRequest{
		Operation: "CreateIssue",
		Method:    http.MethodPost,
		Endpoint:  "/repos/" + repo + "/issues",
		Body:      payload,
	}, &issue)
	return issue, err
}

func (b *GHBackend) GetIssue(ctx context.Context, repo string, issueNumber int) (Issue, error) {
	var issue Issue
	err := b.runJSON(ctx, ExternalCLIAPIRequest{
		Operation: "GetIssue",
		Method:    http.MethodGet,
		Endpoint:  fmt.Sprintf("/repos/%s/issues/%d", repo, issueNumber),
	}, &issue)
	return issue, err
}

func (b *GHBackend) ListIssues(ctx context.Context, repo string, opts ListIssueOptions) ([]Issue, error) {
	var issues []Issue
	if err := b.runPagedJSON(ctx, ExternalCLIAPIRequest{
		Operation: "ListIssues",
		Method:    http.MethodGet,
		Endpoint:  "/repos/" + repo + "/issues",
		Query:     url.Values{"per_page": {"100"}, "state": {opts.State}},
		Paginate:  true,
	}, &issues); err != nil {
		return nil, err
	}
	return issues, nil
}

func (b *GHBackend) UpdateIssue(ctx context.Context, repo string, issueNumber int, opts UpdateIssueOptions) (Issue, error) {
	payload := map[string]any{}
	if opts.Title != nil {
		payload["title"] = *opts.Title
	}
	if opts.Body != nil {
		payload["body"] = *opts.Body
	}
	if opts.State != nil {
		payload["state"] = *opts.State
	}
	var issue Issue
	err := b.runJSON(ctx, ExternalCLIAPIRequest{
		Operation: "UpdateIssue",
		Method:    http.MethodPatch,
		Endpoint:  fmt.Sprintf("/repos/%s/issues/%d", repo, issueNumber),
		Body:      payload,
	}, &issue)
	return issue, err
}

func (b *GHBackend) ListIssueComments(ctx context.Context, repo string, issueNumber int) ([]Comment, error) {
	var comments []Comment
	if err := b.runPagedJSON(ctx, ExternalCLIAPIRequest{
		Operation: "ListIssueComments",
		Method:    http.MethodGet,
		Endpoint:  fmt.Sprintf("/repos/%s/issues/%d/comments", repo, issueNumber),
		Query:     url.Values{"per_page": {"100"}},
		Paginate:  true,
	}, &comments); err != nil {
		return nil, err
	}
	return comments, nil
}

// ObserveIssueComment resolves a provider comment locator without enumerating
// the issue timeline. The response version is optional because github.com does
// not currently expose the self-hosted representation-version header.
func (b *GHBackend) ObserveIssueComment(ctx context.Context, repo string, commentID int64) (IssueCommentObservation, error) {
	if commentID <= 0 {
		return IssueCommentObservation{}, fmt.Errorf("comment id must be positive")
	}
	var comment Comment
	metadata, err := b.runRunnerJSON(ctx, ExternalCLIAPIRequest{
		Operation: "ObserveIssueComment",
		Method:    http.MethodGet,
		Endpoint:  fmt.Sprintf("/repos/%s/issues/comments/%d", repo, commentID),
	}, &comment)
	if err != nil {
		return IssueCommentObservation{}, err
	}
	comments := []Comment{comment}
	annotateCommentIssueNumbers(comments, 0)
	version, _ := parseHeaderInt64(metadata.Headers, HeaderRepresentationVersion)
	return IssueCommentObservation{Comment: comments[0], RepresentationVersion: version}, nil
}

func (b *GHBackend) CreateComment(ctx context.Context, repo string, issueNumber int, body string) (Comment, error) {
	var comment Comment
	err := b.runJSON(ctx, ExternalCLIAPIRequest{
		Operation: "CreateComment",
		Method:    http.MethodPost,
		Endpoint:  fmt.Sprintf("/repos/%s/issues/%d/comments", repo, issueNumber),
		Body:      map[string]string{"body": body},
	}, &comment)
	return comment, err
}

func (b *GHBackend) UpdateComment(ctx context.Context, repo string, commentID int64, body string) (Comment, error) {
	var comment Comment
	err := b.runJSON(ctx, ExternalCLIAPIRequest{
		Operation: "UpdateComment",
		Method:    http.MethodPatch,
		Endpoint:  fmt.Sprintf("/repos/%s/issues/comments/%d", repo, commentID),
		Body:      map[string]string{"body": body},
	}, &comment)
	return comment, err
}

func (b *GHBackend) CreateLabel(ctx context.Context, repo, name, color, description string) (LabelResult, error) {
	var out struct {
		Name string `json:"name"`
	}
	err := b.runJSON(ctx, ExternalCLIAPIRequest{
		Operation: "CreateLabel",
		Method:    http.MethodPost,
		Endpoint:  "/repos/" + repo + "/labels",
		Body: map[string]string{
			"name":        name,
			"color":       color,
			"description": description,
		},
	}, &out)
	if err != nil {
		message := strings.ToLower(err.Error())
		if strings.Contains(message, "already_exists") || strings.Contains(message, "already exists") {
			return LabelResult{Name: name, Skipped: true}, nil
		}
		return LabelResult{}, err
	}
	if out.Name == "" {
		out.Name = name
	}
	return LabelResult{Name: out.Name, Created: true}, nil
}

func (b *GHBackend) runJSON(ctx context.Context, request ExternalCLIAPIRequest, out any) error {
	result, err := b.cli.RunAPI(ctx, b.Host, request)
	if err != nil {
		return err
	}
	return DecodeCLIJSON(result.Stdout, out)
}

func (b *GHBackend) runPagedJSON(ctx context.Context, request ExternalCLIAPIRequest, out any) error {
	result, err := b.cli.RunAPI(ctx, b.Host, request)
	if err != nil {
		return err
	}
	return DecodeCLIJSONPageStream(result.Stdout, out)
}
