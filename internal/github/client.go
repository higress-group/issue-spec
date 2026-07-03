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
	HTTPClient *http.Client
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
	ID      int64  `json:"id"`
	HTMLURL string `json:"html_url"`
	URL     string `json:"url"`
	Body    string `json:"body"`
	User    *User  `json:"user,omitempty"`
}

type Notification struct {
	ID        string    `json:"id"`
	Unread    bool      `json:"unread"`
	Reason    string    `json:"reason"`
	UpdatedAt time.Time `json:"updated_at"`
	URL       string    `json:"url"`
	Subject   struct {
		Title            string `json:"title"`
		URL              string `json:"url"`
		LatestCommentURL string `json:"latest_comment_url"`
		Type             string `json:"type"`
	} `json:"subject"`
}

type Subscription struct {
	Subscribed bool `json:"subscribed"`
	Ignored    bool `json:"ignored"`
}

type Permission struct {
	Permission string `json:"permission"`
}

type RepositoryIssueComment struct {
	Comment
	IssueNumber int    `json:"issue_number"`
	IssueURL    string `json:"issue_url"`
}

type ResponseMetadata struct {
	StatusCode         int
	Headers            http.Header
	ETag               string
	LastModified       string
	PollInterval       string
	RateLimitLimit     int
	RateLimitRemaining int
	RateLimitReset     string
	NotModified        bool
}

type NotificationListOptions struct {
	All           bool
	Participating bool
	Since         *time.Time
	Before        *time.Time
	PerPage       int
	Page          int
	ETag          string
	LastModified  string
}

type NotificationCommentOptions struct {
	ETag         string
	LastModified string
}

type RepositoryIssueCommentOptions struct {
	Since   *time.Time
	PerPage int
	Page    int
}

func (o NotificationListOptions) query() url.Values {
	q := url.Values{}
	if o.All {
		q.Set("all", "true")
	}
	if o.Participating {
		q.Set("participating", "true")
	}
	if o.Since != nil {
		q.Set("since", o.Since.UTC().Format(time.RFC3339))
	}
	if o.Before != nil {
		q.Set("before", o.Before.UTC().Format(time.RFC3339))
	}
	if o.PerPage > 0 {
		q.Set("per_page", strconv.Itoa(o.PerPage))
	}
	if o.Page > 0 {
		q.Set("page", strconv.Itoa(o.Page))
	}
	return q
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
	return &Client{
		Host:       host,
		BaseURL:    baseURL(host),
		Token:      token,
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
	}
}

func NewClientWithBaseURL(host, baseURL, token string, httpClient *http.Client) *Client {
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

func (c *Client) ListNotifications(ctx context.Context, opts NotificationListOptions) ([]Notification, ResponseMetadata, error) {
	return c.listNotificationsPages(ctx, opts)
}

func (c *Client) WatchRepository(ctx context.Context, repo string) (Subscription, ResponseMetadata, error) {
	var sub Subscription
	var meta ResponseMetadata
	resp, err := c.do(ctx, http.MethodPut, "/repos/"+repo+"/subscription", map[string]any{"subscribed": true}, &sub)
	if err != nil {
		return Subscription{}, meta, err
	}
	meta = responseMetadata(resp)
	return sub, meta, nil
}

func (c *Client) GetNotificationComments(ctx context.Context, threadURL string, opts NotificationCommentOptions) ([]Comment, ResponseMetadata, error) {
	var all []Comment
	meta, err := c.listCommentPages(ctx, threadURL+"/comments", nil, opts.ETag, opts.LastModified, &all)
	return all, meta, err
}

func (c *Client) ListRepositoryIssueComments(ctx context.Context, repo string, opts RepositoryIssueCommentOptions) ([]RepositoryIssueComment, ResponseMetadata, error) {
	var all []RepositoryIssueComment
	query := url.Values{}
	if opts.PerPage > 0 {
		query.Set("per_page", strconv.Itoa(opts.PerPage))
	} else {
		query.Set("per_page", "100")
	}
	if opts.Page > 0 {
		query.Set("page", strconv.Itoa(opts.Page))
	}
	if opts.Since != nil {
		query.Set("since", opts.Since.UTC().Format(time.RFC3339))
	}
	meta, err := c.listIssueCommentPages(ctx, "/repos/"+repo+"/issues/comments", query, &all)
	return all, meta, err
}

func (c *Client) GetCollaboratorPermission(ctx context.Context, repo, user string) (Permission, ResponseMetadata, error) {
	var perm Permission
	var meta ResponseMetadata
	resp, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/collaborators/%s/permission", repo, url.PathEscape(user)), nil, &perm)
	if err != nil {
		return Permission{}, meta, err
	}
	meta = responseMetadata(resp)
	return perm, meta, nil
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

func (c *Client) listNotificationsPages(ctx context.Context, opts NotificationListOptions) ([]Notification, ResponseMetadata, error) {
	query := opts.query()
	meta := ResponseMetadata{}
	var all []Notification
	for page := 1; ; page++ {
		q := cloneQuery(query)
		if q.Get("per_page") == "" {
			q.Set("per_page", "100")
		}
		if q.Get("page") == "" {
			q.Set("page", strconv.Itoa(page))
		}
		var pageItems []Notification
		resp, err := c.doConditional(ctx, http.MethodGet, withQuery("/notifications", q), nil, &pageItems, opts.ETag, opts.LastModified)
		if resp != nil {
			meta = responseMetadata(resp)
			if meta.NotModified {
				return all, meta, nil
			}
		}
		if err != nil {
			return nil, meta, err
		}
		if len(pageItems) == 0 {
			return all, meta, nil
		}
		all = append(all, pageItems...)
		if len(pageItems) < 100 {
			return all, meta, nil
		}
	}
}

func (c *Client) listCommentPages(ctx context.Context, path string, query url.Values, etag, lastModified string, out *[]Comment) (ResponseMetadata, error) {
	meta := ResponseMetadata{}
	for page := 1; ; page++ {
		q := cloneQuery(query)
		if q.Get("per_page") == "" {
			q.Set("per_page", "100")
		}
		if q.Get("page") == "" {
			q.Set("page", strconv.Itoa(page))
		}
		var items []Comment
		resp, err := c.doConditional(ctx, http.MethodGet, withQuery(path, q), nil, &items, etag, lastModified)
		if resp != nil {
			meta = responseMetadata(resp)
			if meta.NotModified {
				return meta, nil
			}
		}
		if err != nil {
			return meta, err
		}
		*out = append(*out, items...)
		if len(items) < 100 {
			return meta, nil
		}
	}
}

func (c *Client) listIssueCommentPages(ctx context.Context, path string, query url.Values, out *[]RepositoryIssueComment) (ResponseMetadata, error) {
	meta := ResponseMetadata{}
	for page := 1; ; page++ {
		q := cloneQuery(query)
		if q.Get("per_page") == "" {
			q.Set("per_page", "100")
		}
		if q.Get("page") == "" {
			q.Set("page", strconv.Itoa(page))
		}
		var items []RepositoryIssueComment
		resp, err := c.doConditional(ctx, http.MethodGet, withQuery(path, q), nil, &items, "", "")
		if resp != nil {
			meta = responseMetadata(resp)
		}
		if err != nil {
			return meta, err
		}
		*out = append(*out, items...)
		if len(items) < 100 {
			return meta, nil
		}
	}
}

func (c *Client) doConditional(ctx context.Context, method, path string, in any, out any, etag, lastModified string) (*http.Response, error) {
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
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if lastModified != "" {
		req.Header.Set("If-Modified-Since", lastModified)
	}
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
	if resp.StatusCode == http.StatusNotModified {
		return resp, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return resp, &APIError{Method: method, URL: endpoint, StatusCode: resp.StatusCode, Body: redactTokenValue(string(data), c.Token)}
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp, fmt.Errorf("decode GitHub response from %s: %w", endpoint, err)
		}
	} else {
		io.Copy(io.Discard, resp.Body)
	}
	return resp, nil
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

	if resp.StatusCode == http.StatusNotModified {
		return resp, nil
	}
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

func responseMetadata(resp *http.Response) ResponseMetadata {
	meta := ResponseMetadata{StatusCode: resp.StatusCode, Headers: resp.Header.Clone()}
	meta.ETag = resp.Header.Get("ETag")
	meta.LastModified = resp.Header.Get("Last-Modified")
	meta.PollInterval = resp.Header.Get("X-Poll-Interval")
	meta.RateLimitLimit, _ = strconv.Atoi(resp.Header.Get("X-RateLimit-Limit"))
	meta.RateLimitRemaining, _ = strconv.Atoi(resp.Header.Get("X-RateLimit-Remaining"))
	meta.RateLimitReset = resp.Header.Get("X-RateLimit-Reset")
	meta.NotModified = resp.StatusCode == http.StatusNotModified
	return meta
}

func cloneQuery(query url.Values) url.Values {
	out := url.Values{}
	for k, v := range query {
		out[k] = append([]string(nil), v...)
	}
	return out
}

func withQuery(path string, query url.Values) string {
	if len(query) == 0 {
		return path
	}
	return path + "?" + query.Encode()
}

func pageSize(v url.Values) int {
	if n, _ := strconv.Atoi(v.Get("per_page")); n > 0 {
		return n
	}
	return 100
}

func lenValue(out any) int {
	switch v := out.(type) {
	case *[]Notification:
		return len(*v)
	case *[]Comment:
		return len(*v)
	case *[]RepositoryIssueComment:
		return len(*v)
	default:
		return 0
	}
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
