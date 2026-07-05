package github

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

func (b *GHBackend) GetPullRequest(ctx context.Context, repo string, prNumber int) (PullRequest, error) {
	var pr PullRequest
	err := b.runAPIJSON(ctx, "GetPullRequest", http.MethodGet, fmt.Sprintf("/repos/%s/pulls/%d", repo, prNumber), nil, nil, &pr)
	return pr, err
}

func (b *GHBackend) CreatePullRequest(ctx context.Context, repo string, opts CreatePullRequestOptions) (PullRequest, error) {
	var pr PullRequest
	err := b.runAPIJSON(ctx, "CreatePullRequest", http.MethodPost, "/repos/"+repo+"/pulls", nil, map[string]any{
		"title": opts.Title,
		"head":  opts.Head,
		"base":  opts.Base,
		"body":  opts.Body,
		"draft": opts.Draft,
	}, &pr)
	return pr, err
}

func (b *GHBackend) UpdatePullRequest(ctx context.Context, repo string, prNumber int, opts UpdatePullRequestOptions) (PullRequest, error) {
	payload := map[string]any{}
	if opts.Body != nil {
		payload["body"] = *opts.Body
	}
	var pr PullRequest
	err := b.runAPIJSON(ctx, "UpdatePullRequest", http.MethodPatch, fmt.Sprintf("/repos/%s/pulls/%d", repo, prNumber), nil, payload, &pr)
	return pr, err
}

func (b *GHBackend) ListPullRequestFiles(ctx context.Context, repo string, prNumber int) ([]PullRequestFile, error) {
	var files []PullRequestFile
	err := b.runAPIJSONPages(ctx, "ListPullRequestFiles", http.MethodGet, fmt.Sprintf("/repos/%s/pulls/%d/files", repo, prNumber), paginationQuery(), nil, "", &files)
	return files, err
}

func (b *GHBackend) ListPullRequestReviewComments(ctx context.Context, repo string, prNumber int) ([]PullRequestReviewComment, error) {
	var comments []PullRequestReviewComment
	err := b.runAPIJSONPages(ctx, "ListPullRequestReviewComments", http.MethodGet, fmt.Sprintf("/repos/%s/pulls/%d/comments", repo, prNumber), paginationQuery(), nil, "", &comments)
	return comments, err
}

func (b *GHBackend) CreatePullRequestReviewComment(ctx context.Context, repo string, prNumber int, body, commitID, path string, line int, side string) (PullRequestReviewComment, error) {
	if strings.TrimSpace(side) == "" {
		side = "RIGHT"
	}
	var comment PullRequestReviewComment
	err := b.runAPIJSON(ctx, "CreatePullRequestReviewComment", http.MethodPost, fmt.Sprintf("/repos/%s/pulls/%d/comments", repo, prNumber), nil, map[string]any{
		"body":      body,
		"commit_id": commitID,
		"path":      path,
		"line":      line,
		"side":      side,
	}, &comment)
	return comment, err
}

func (b *GHBackend) ReplyPullRequestReviewComment(ctx context.Context, repo string, prNumber int, commentID int64, body string) (PullRequestReviewComment, error) {
	var comment PullRequestReviewComment
	err := b.runAPIJSON(ctx, "ReplyPullRequestReviewComment", http.MethodPost, fmt.Sprintf("/repos/%s/pulls/%d/comments/%d/replies", repo, prNumber, commentID), nil, map[string]string{
		"body": body,
	}, &comment)
	return comment, err
}

func (b *GHBackend) GetCombinedStatus(ctx context.Context, repo, ref string) (CombinedStatus, error) {
	var status CombinedStatus
	err := b.runAPIJSON(ctx, "GetCombinedStatus", http.MethodGet, fmt.Sprintf("/repos/%s/commits/%s/status", repo, url.PathEscape(ref)), nil, nil, &status)
	return status, err
}

func (b *GHBackend) ListCheckRuns(ctx context.Context, repo, ref string) ([]CheckRun, error) {
	var runs []CheckRun
	err := b.runAPIJSONPages(ctx, "ListCheckRuns", http.MethodGet, fmt.Sprintf("/repos/%s/commits/%s/check-runs", repo, url.PathEscape(ref)), paginationQuery(), nil, "check_runs", &runs)
	return runs, err
}

func (b *GHBackend) runAPIJSON(ctx context.Context, operation, method, endpoint string, query url.Values, body any, out any) error {
	result, err := b.cli.RunAPI(ctx, b.Host, ExternalCLIAPIRequest{
		Operation: operation,
		Method:    method,
		Endpoint:  endpoint,
		Query:     query,
		Body:      body,
	})
	if err != nil {
		return err
	}
	return DecodeCLIJSON(result.Stdout, out)
}

func (b *GHBackend) runAPIJSONPages(ctx context.Context, operation, method, endpoint string, query url.Values, body any, envelopeKey string, out any) error {
	result, err := b.cli.RunAPI(ctx, b.Host, ExternalCLIAPIRequest{
		Operation: operation,
		Method:    method,
		Endpoint:  endpoint,
		Query:     query,
		Body:      body,
		Paginate:  true,
	})
	if err != nil {
		return err
	}
	if envelopeKey != "" {
		return DecodeCLIJSONEnvelopePageStream(result.Stdout, envelopeKey, out)
	}
	return DecodeCLIJSONPageStream(result.Stdout, out)
}

func paginationQuery() url.Values {
	return url.Values{"per_page": {"100"}}
}
