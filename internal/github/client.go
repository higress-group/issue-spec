package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	Host       string
	BaseURL    string
	Token      string
	HTTPClient HTTPDoer
}

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type APIError struct {
	Method     string
	URL        string
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	body := strings.TrimSpace(e.Body)
	if len(body) > 500 {
		body = body[:500] + "..."
	}
	if body == "" {
		return fmt.Sprintf("%s %s failed: HTTP %d", e.Method, e.URL, e.StatusCode)
	}
	return fmt.Sprintf("%s %s failed: HTTP %d: %s", e.Method, e.URL, e.StatusCode, body)
}

type User struct {
	Login string `json:"login"`
}

type Issue struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
	URL     string `json:"url"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	State   string `json:"state"`
}

type Comment struct {
	ID          int64     `json:"id"`
	HTMLURL     string    `json:"html_url"`
	URL         string    `json:"url"`
	IssueURL    string    `json:"issue_url,omitempty"`
	IssueNumber int       `json:"-"`
	Body        string    `json:"body"`
	User        *User     `json:"user,omitempty"`
	Reactions   Reactions `json:"reactions,omitempty"`
	CreatedAt   time.Time `json:"created_at,omitempty"`
	UpdatedAt   time.Time `json:"updated_at,omitempty"`
}

type Reactions struct {
	TotalCount int `json:"total_count"`
	Eyes       int `json:"eyes"`
}

type Reaction struct {
	ID        int64     `json:"id"`
	User      *User     `json:"user,omitempty"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at,omitempty"`
}

type LabelResult struct {
	Name    string `json:"name"`
	Created bool   `json:"created"`
	Skipped bool   `json:"skipped"`
}

type PullRequest struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
	State   string `json:"state"`
	Head    struct {
		SHA string `json:"sha"`
		Ref string `json:"ref"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

type CreatePullRequestOptions struct {
	Title string
	Head  string
	Base  string
	Body  string
	Draft bool
}

type UpdateIssueOptions struct {
	Title *string
	Body  *string
}

type PullRequestFile struct {
	Filename string `json:"filename"`
	Patch    string `json:"patch"`
}

type PullRequestReviewComment struct {
	ID          int64  `json:"id"`
	HTMLURL     string `json:"html_url"`
	URL         string `json:"url"`
	Body        string `json:"body"`
	Path        string `json:"path"`
	Line        int    `json:"line,omitempty"`
	Position    int    `json:"position,omitempty"`
	CommitID    string `json:"commit_id"`
	InReplyToID int64  `json:"in_reply_to_id,omitempty"`
	User        *User  `json:"user,omitempty"`
}

type CombinedStatus struct {
	State    string   `json:"state"`
	Statuses []Status `json:"statuses"`
}

type Status struct {
	Context     string `json:"context"`
	State       string `json:"state"`
	Description string `json:"description"`
	TargetURL   string `json:"target_url"`
}

type CheckRunsResponse struct {
	TotalCount int        `json:"total_count"`
	CheckRuns  []CheckRun `json:"check_runs"`
}

type CheckRun struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	Conclusion  string `json:"conclusion"`
	DetailsURL  string `json:"details_url"`
	HTMLURL     string `json:"html_url"`
	StartedAt   string `json:"started_at"`
	CompletedAt string `json:"completed_at"`
}

func NewClient(host, token string) *Client {
	host = normalizeHost(host)
	return newClientWithHTTPDoer(host, baseURL(host), token, nil)
}

func NewClientWithBaseURL(host, baseURL, token string, httpClient *http.Client) *Client {
	return newClientWithHTTPDoer(host, baseURL, token, httpClient)
}

func NewClientWithHTTPDoer(host, baseURL, token string, httpClient HTTPDoer) *Client {
	return newClientWithHTTPDoer(host, baseURL, token, httpClient)
}

func newClientWithHTTPDoer(host, baseURL, token string, httpClient HTTPDoer) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{Host: normalizeHost(host), BaseURL: strings.TrimRight(baseURL, "/"), Token: token, HTTPClient: httpClient}
}

func (c *Client) BackendInfo() BackendInfo {
	return BackendInfo{Name: "rest", Kind: "rest", Host: c.Host}
}

func (c *Client) GetUser(ctx context.Context) (User, []string, error) {
	var user User
	resp, err := c.do(ctx, http.MethodGet, "/user", nil, &user)
	if err != nil {
		return User{}, nil, err
	}
	return user, splitScopes(resp.Header.Get("X-OAuth-Scopes")), nil
}

func (c *Client) CreateIssue(ctx context.Context, repo, title, body string, labels []string) (Issue, error) {
	var issue Issue
	payload := map[string]any{"title": title, "body": body}
	if len(labels) > 0 {
		payload["labels"] = labels
	}
	err := c.doJSON(ctx, http.MethodPost, "/repos/"+repo+"/issues", payload, &issue)
	return issue, err
}

func (c *Client) GetIssue(ctx context.Context, repo string, issueNumber int) (Issue, error) {
	var issue Issue
	err := c.doJSON(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/issues/%d", repo, issueNumber), nil, &issue)
	return issue, err
}

func (c *Client) UpdateIssue(ctx context.Context, repo string, issueNumber int, opts UpdateIssueOptions) (Issue, error) {
	payload := map[string]any{}
	if opts.Title != nil {
		payload["title"] = *opts.Title
	}
	if opts.Body != nil {
		payload["body"] = *opts.Body
	}
	var issue Issue
	err := c.doJSON(ctx, http.MethodPatch, fmt.Sprintf("/repos/%s/issues/%d", repo, issueNumber), payload, &issue)
	return issue, err
}

func (c *Client) ListIssueComments(ctx context.Context, repo string, issueNumber int) ([]Comment, error) {
	var all []Comment
	for page := 1; ; page++ {
		var comments []Comment
		path := fmt.Sprintf("/repos/%s/issues/%d/comments?per_page=100&page=%d", repo, issueNumber, page)
		if err := c.doJSON(ctx, http.MethodGet, path, nil, &comments); err != nil {
			return nil, err
		}
		all = append(all, comments...)
		if len(comments) < 100 {
			break
		}
	}
	return all, nil
}

func (c *Client) CreateComment(ctx context.Context, repo string, issueNumber int, body string) (Comment, error) {
	var comment Comment
	err := c.doJSON(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/issues/%d/comments", repo, issueNumber), map[string]string{"body": body}, &comment)
	return comment, err
}

func (c *Client) UpdateComment(ctx context.Context, repo string, commentID int64, body string) (Comment, error) {
	var comment Comment
	err := c.doJSON(ctx, http.MethodPatch, fmt.Sprintf("/repos/%s/issues/comments/%d", repo, commentID), map[string]string{"body": body}, &comment)
	return comment, err
}

func (c *Client) CreateLabel(ctx context.Context, repo, name, color, description string) (LabelResult, error) {
	var out struct {
		Name string `json:"name"`
	}
	err := c.doJSON(ctx, http.MethodPost, "/repos/"+repo+"/labels", map[string]string{
		"name":        name,
		"color":       color,
		"description": description,
	}, &out)
	if err != nil {
		var apiErr *APIError
		if strings.Contains(err.Error(), "already_exists") || strings.Contains(err.Error(), "already exists") {
			return LabelResult{Name: name, Skipped: true}, nil
		}
		if ok := errorAsAPI(err, &apiErr); ok && apiErr.StatusCode == http.StatusUnprocessableEntity {
			return LabelResult{Name: name, Skipped: true}, nil
		}
		return LabelResult{}, err
	}
	if out.Name == "" {
		out.Name = name
	}
	return LabelResult{Name: out.Name, Created: true}, nil
}

func (c *Client) GetPullRequest(ctx context.Context, repo string, prNumber int) (PullRequest, error) {
	var pr PullRequest
	err := c.doJSON(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/pulls/%d", repo, prNumber), nil, &pr)
	return pr, err
}

func (c *Client) CreatePullRequest(ctx context.Context, repo string, opts CreatePullRequestOptions) (PullRequest, error) {
	var pr PullRequest
	err := c.doJSON(ctx, http.MethodPost, "/repos/"+repo+"/pulls", map[string]any{
		"title": opts.Title,
		"head":  opts.Head,
		"base":  opts.Base,
		"body":  opts.Body,
		"draft": opts.Draft,
	}, &pr)
	return pr, err
}

func (c *Client) ListPullRequestFiles(ctx context.Context, repo string, prNumber int) ([]PullRequestFile, error) {
	var all []PullRequestFile
	for page := 1; ; page++ {
		var files []PullRequestFile
		path := fmt.Sprintf("/repos/%s/pulls/%d/files?per_page=100&page=%d", repo, prNumber, page)
		if err := c.doJSON(ctx, http.MethodGet, path, nil, &files); err != nil {
			return nil, err
		}
		all = append(all, files...)
		if len(files) < 100 {
			break
		}
	}
	return all, nil
}

func (c *Client) ListPullRequestReviewComments(ctx context.Context, repo string, prNumber int) ([]PullRequestReviewComment, error) {
	var all []PullRequestReviewComment
	for page := 1; ; page++ {
		var comments []PullRequestReviewComment
		path := fmt.Sprintf("/repos/%s/pulls/%d/comments?per_page=100&page=%d", repo, prNumber, page)
		if err := c.doJSON(ctx, http.MethodGet, path, nil, &comments); err != nil {
			return nil, err
		}
		all = append(all, comments...)
		if len(comments) < 100 {
			break
		}
	}
	return all, nil
}

func (c *Client) CreatePullRequestReviewComment(ctx context.Context, repo string, prNumber int, body, commitID, path string, line int, side string) (PullRequestReviewComment, error) {
	var comment PullRequestReviewComment
	if side == "" {
		side = "RIGHT"
	}
	err := c.doJSON(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/pulls/%d/comments", repo, prNumber), map[string]any{
		"body":      body,
		"commit_id": commitID,
		"path":      path,
		"line":      line,
		"side":      side,
	}, &comment)
	return comment, err
}

func (c *Client) ReplyPullRequestReviewComment(ctx context.Context, repo string, prNumber int, commentID int64, body string) (PullRequestReviewComment, error) {
	var comment PullRequestReviewComment
	err := c.doJSON(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/pulls/%d/comments/%d/replies", repo, prNumber, commentID), map[string]string{
		"body": body,
	}, &comment)
	return comment, err
}

func (c *Client) GetCombinedStatus(ctx context.Context, repo, ref string) (CombinedStatus, error) {
	var status CombinedStatus
	err := c.doJSON(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/commits/%s/status", repo, url.PathEscape(ref)), nil, &status)
	return status, err
}

func (c *Client) ListCheckRuns(ctx context.Context, repo, ref string) ([]CheckRun, error) {
	var all []CheckRun
	for page := 1; ; page++ {
		var response CheckRunsResponse
		path := fmt.Sprintf("/repos/%s/commits/%s/check-runs?per_page=100&page=%d", repo, url.PathEscape(ref), page)
		if err := c.doJSON(ctx, http.MethodGet, path, nil, &response); err != nil {
			return nil, err
		}
		all = append(all, response.CheckRuns...)
		if len(response.CheckRuns) < 100 {
			break
		}
	}
	return all, nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, in any, out any) error {
	_, err := c.do(ctx, method, path, in, out)
	return err
}

func (c *Client) do(ctx context.Context, method, path string, in any, out any) (*http.Response, error) {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(data)
	}

	endpoint, err := c.endpoint(path)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "issue-spec")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return resp, &APIError{Method: method, URL: endpoint, StatusCode: resp.StatusCode, Body: redactTokenValue(string(data), c.Token)}
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		io.Copy(io.Discard, resp.Body)
		return resp, nil
	}
	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(out); err != nil {
		return resp, fmt.Errorf("decode GitHub response from %s: %w", endpoint, err)
	}
	return resp, nil
}

func (c *Client) endpoint(path string) (string, error) {
	base := strings.TrimRight(c.BaseURL, "/")
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path, nil
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return base + path, nil
}

func ParseRepo(repo string) (string, error) {
	repo = strings.TrimSpace(repo)
	if strings.Count(repo, "/") != 1 {
		return "", fmt.Errorf("repo must be owner/name, got %q", repo)
	}
	parts := strings.Split(repo, "/")
	if parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("repo must be owner/name, got %q", repo)
	}
	return url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1]), nil
}

func ParseIssueNumber(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, fmt.Errorf("issue number is empty")
	}
	if n, err := strconv.Atoi(value); err == nil && n > 0 {
		return n, nil
	}
	u, err := url.Parse(value)
	if err != nil {
		return 0, fmt.Errorf("parse issue URL %q: %w", value, err)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 4 {
		return 0, fmt.Errorf("issue URL must look like /owner/repo/issues/123, got %q", value)
	}
	if parts[len(parts)-2] != "issues" {
		return 0, fmt.Errorf("issue URL must contain /issues/<number>, got %q", value)
	}
	n, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("issue URL has invalid number %q", parts[len(parts)-1])
	}
	return n, nil
}

func splitScopes(header string) []string {
	if strings.TrimSpace(header) == "" {
		return nil
	}
	parts := strings.Split(header, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if scope := strings.TrimSpace(part); scope != "" {
			out = append(out, scope)
		}
	}
	return out
}

func normalizeHost(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return "github.com"
	}
	host = strings.TrimPrefix(host, "https://")
	host = strings.TrimPrefix(host, "http://")
	host = strings.TrimSuffix(host, "/")
	return host
}

func baseURL(host string) string {
	if override := strings.TrimSpace(os.Getenv("ISSUE_SPEC_API_URL")); override != "" {
		return strings.TrimRight(override, "/")
	}
	if host == "github.com" {
		return "https://api.github.com"
	}
	return "https://" + host + "/api/v3"
}

func redactTokenValue(value, token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return value
	}
	return strings.ReplaceAll(value, token, "[REDACTED]")
}

func errorAsAPI(err error, target **APIError) bool {
	for err != nil {
		if apiErr, ok := err.(*APIError); ok {
			*target = apiErr
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
