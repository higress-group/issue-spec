package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultRunnerCommentsPerPage      = 100
	defaultRunnerNotificationsPerPage = 50
)

// RunnerOperations is the polling-runner GitHub API surface. It is separate
// from Operations because runner intake needs HTTP status/header metadata.
type RunnerOperations interface {
	PollNotifications(context.Context, NotificationListOptions) (NotificationListResult, error)
	GetRepositorySubscription(context.Context, string) (RepositorySubscriptionResult, error)
	GetIssueContext(context.Context, string, int, ConditionalRequest) (IssueContextResult, error)
	ListIssueCommentsPage(context.Context, string, int, CommentListOptions) (IssueCommentsResult, error)
	ListRepositoryIssueCommentsPage(context.Context, string, CommentListOptions) (IssueCommentsResult, error)
	ListCommentReactionsPage(context.Context, string, int64, RunnerPageOptions) (CommentReactionsResult, error)
	GetCollaboratorPermission(context.Context, string, string) (CollaboratorPermissionResult, error)
	CreateRunnerComment(context.Context, string, int, string) (RunnerCommentResult, error)
	UpdateRunnerComment(context.Context, string, int64, string) (RunnerCommentResult, error)
	AddCommentReaction(context.Context, string, int64, string) (RunnerReactionResult, error)
}

type ConditionalRequest struct {
	ETag         string
	LastModified string
}

type RunnerPageOptions struct {
	PerPage   int
	Page      int
	CursorURL string
}

type NotificationListOptions struct {
	ConditionalRequest
	Page          RunnerPageOptions
	Since         *time.Time
	Before        *time.Time
	All           bool
	Participating bool
}

type CommentListOptions struct {
	ConditionalRequest
	Page  RunnerPageOptions
	Since *time.Time
}

type ResponseMetadata struct {
	StatusCode          int
	Headers             http.Header
	ETag                string
	LastModified        string
	NotModified         bool
	PollIntervalSeconds int
	PollInterval        time.Duration
	RateLimit           RateLimitMetadata
	Pagination          PaginationMetadata
}

type RateLimitMetadata struct {
	Limit             int
	Remaining         int
	Used              int
	ResetUnix         int64
	ResetAt           time.Time
	Resource          string
	RetryAfterSeconds int
}

type PaginationMetadata struct {
	NextURL  string
	PrevURL  string
	FirstURL string
	LastURL  string
	NextPage int
	PrevPage int
	LastPage int
}

type NotificationListResult struct {
	Notifications []Notification
	Metadata      ResponseMetadata
}

type IssueContextResult struct {
	Issue    Issue
	Metadata ResponseMetadata
}

type IssueCommentsResult struct {
	Comments []Comment
	Metadata ResponseMetadata
}

type CommentReactionsResult struct {
	Reactions []Reaction
	Metadata  ResponseMetadata
}

type RepositorySubscriptionResult struct {
	Subscription RepositorySubscription
	Metadata     ResponseMetadata
}

type CollaboratorPermissionResult struct {
	Permission CollaboratorPermission
	CanWrite   bool
	Metadata   ResponseMetadata
}

type RunnerCommentResult struct {
	Comment  Comment
	Metadata ResponseMetadata
}

type RunnerReactionResult struct {
	Metadata ResponseMetadata
}

type Notification struct {
	ID         string              `json:"id"`
	Unread     bool                `json:"unread"`
	Reason     string              `json:"reason"`
	UpdatedAt  time.Time           `json:"updated_at"`
	LastReadAt *time.Time          `json:"last_read_at,omitempty"`
	Subject    NotificationSubject `json:"subject"`
	Repository Repository          `json:"repository"`
	URL        string              `json:"url"`
}

type NotificationSubject struct {
	Title            string `json:"title"`
	URL              string `json:"url"`
	LatestCommentURL string `json:"latest_comment_url"`
	Type             string `json:"type"`
}

type Repository struct {
	ID            int64  `json:"id"`
	FullName      string `json:"full_name"`
	HTMLURL       string `json:"html_url"`
	URL           string `json:"url"`
	DefaultBranch string `json:"default_branch"`
}

type RepositorySubscription struct {
	Subscribed    bool      `json:"subscribed"`
	Ignored       bool      `json:"ignored"`
	Reason        string    `json:"reason"`
	CreatedAt     time.Time `json:"created_at"`
	URL           string    `json:"url"`
	RepositoryURL string    `json:"repository_url"`
}

type CollaboratorPermission struct {
	Permission string `json:"permission"`
	RoleName   string `json:"role_name"`
	User       *User  `json:"user,omitempty"`
}

func (c *Client) PollNotifications(ctx context.Context, opts NotificationListOptions) (NotificationListResult, error) {
	path := opts.Page.CursorURL
	query := url.Values{}
	if path == "" {
		path = "/notifications"
		addPageQuery(query, opts.Page, defaultRunnerNotificationsPerPage)
		addTimeQuery(query, "since", opts.Since)
		addTimeQuery(query, "before", opts.Before)
		if opts.All {
			query.Set("all", "true")
		}
		if opts.Participating {
			query.Set("participating", "true")
		}
	}

	var notifications []Notification
	meta, err := c.doRunnerJSON(ctx, http.MethodGet, path, query, nil, opts.ConditionalRequest, true, &notifications)
	if err != nil {
		return NotificationListResult{Metadata: meta}, err
	}
	return NotificationListResult{Notifications: notifications, Metadata: meta}, nil
}

func (c *Client) GetRepositorySubscription(ctx context.Context, repo string) (RepositorySubscriptionResult, error) {
	var subscription RepositorySubscription
	meta, err := c.doRunnerJSON(ctx, http.MethodGet, "/repos/"+repo+"/subscription", nil, nil, ConditionalRequest{}, false, &subscription)
	if err != nil {
		return RepositorySubscriptionResult{Metadata: meta}, err
	}
	return RepositorySubscriptionResult{Subscription: subscription, Metadata: meta}, nil
}

func (c *Client) GetIssueContext(ctx context.Context, repo string, issueNumber int, conditional ConditionalRequest) (IssueContextResult, error) {
	var issue Issue
	meta, err := c.doRunnerJSON(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/issues/%d", repo, issueNumber), nil, nil, conditional, true, &issue)
	if err != nil {
		return IssueContextResult{Metadata: meta}, err
	}
	return IssueContextResult{Issue: issue, Metadata: meta}, nil
}

func (c *Client) ListIssueCommentsPage(ctx context.Context, repo string, issueNumber int, opts CommentListOptions) (IssueCommentsResult, error) {
	path := opts.Page.CursorURL
	query := url.Values{}
	if path == "" {
		path = fmt.Sprintf("/repos/%s/issues/%d/comments", repo, issueNumber)
		addPageQuery(query, opts.Page, defaultRunnerCommentsPerPage)
		addTimeQuery(query, "since", opts.Since)
	}

	var comments []Comment
	meta, err := c.doRunnerJSON(ctx, http.MethodGet, path, query, nil, opts.ConditionalRequest, true, &comments)
	if err != nil {
		return IssueCommentsResult{Metadata: meta}, err
	}
	annotateCommentIssueNumbers(comments, issueNumber)
	return IssueCommentsResult{Comments: comments, Metadata: meta}, nil
}

func (c *Client) ListRepositoryIssueCommentsPage(ctx context.Context, repo string, opts CommentListOptions) (IssueCommentsResult, error) {
	path := opts.Page.CursorURL
	query := url.Values{}
	if path == "" {
		path = "/repos/" + repo + "/issues/comments"
		addPageQuery(query, opts.Page, defaultRunnerCommentsPerPage)
		addTimeQuery(query, "since", opts.Since)
	}

	var comments []Comment
	meta, err := c.doRunnerJSON(ctx, http.MethodGet, path, query, nil, opts.ConditionalRequest, true, &comments)
	if err != nil {
		return IssueCommentsResult{Metadata: meta}, err
	}
	annotateCommentIssueNumbers(comments, 0)
	return IssueCommentsResult{Comments: comments, Metadata: meta}, nil
}

func (c *Client) ListCommentReactionsPage(ctx context.Context, repo string, commentID int64, page RunnerPageOptions) (CommentReactionsResult, error) {
	path := page.CursorURL
	query := url.Values{}
	if path == "" {
		path = fmt.Sprintf("/repos/%s/issues/comments/%d/reactions", repo, commentID)
		addPageQuery(query, page, defaultRunnerCommentsPerPage)
	}

	var reactions []Reaction
	meta, err := c.doRunnerJSON(ctx, http.MethodGet, path, query, nil, ConditionalRequest{}, false, &reactions)
	if err != nil {
		return CommentReactionsResult{Metadata: meta}, err
	}
	return CommentReactionsResult{Reactions: reactions, Metadata: meta}, nil
}

func (c *Client) GetCollaboratorPermission(ctx context.Context, repo, login string) (CollaboratorPermissionResult, error) {
	var permission CollaboratorPermission
	path := fmt.Sprintf("/repos/%s/collaborators/%s/permission", repo, url.PathEscape(login))
	meta, err := c.doRunnerJSON(ctx, http.MethodGet, path, nil, nil, ConditionalRequest{}, false, &permission)
	if err != nil {
		return CollaboratorPermissionResult{Metadata: meta}, err
	}
	return CollaboratorPermissionResult{
		Permission: permission,
		CanWrite:   permissionAllowsWrite(permission.Permission),
		Metadata:   meta,
	}, nil
}

func (c *Client) CreateRunnerComment(ctx context.Context, repo string, issueNumber int, body string) (RunnerCommentResult, error) {
	var comment Comment
	meta, err := c.doRunnerJSON(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/issues/%d/comments", repo, issueNumber), nil, map[string]string{"body": body}, ConditionalRequest{}, false, &comment)
	if err != nil {
		return RunnerCommentResult{Metadata: meta}, err
	}
	comment.IssueNumber = issueNumber
	return RunnerCommentResult{Comment: comment, Metadata: meta}, nil
}

func (c *Client) UpdateRunnerComment(ctx context.Context, repo string, commentID int64, body string) (RunnerCommentResult, error) {
	var comment Comment
	meta, err := c.doRunnerJSON(ctx, http.MethodPatch, fmt.Sprintf("/repos/%s/issues/comments/%d", repo, commentID), nil, map[string]string{"body": body}, ConditionalRequest{}, false, &comment)
	if err != nil {
		return RunnerCommentResult{Metadata: meta}, err
	}
	comments := []Comment{comment}
	annotateCommentIssueNumbers(comments, 0)
	comment = comments[0]
	return RunnerCommentResult{Comment: comment, Metadata: meta}, nil
}

func (c *Client) AddCommentReaction(ctx context.Context, repo string, commentID int64, content string) (RunnerReactionResult, error) {
	meta, err := c.doRunnerJSON(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/issues/comments/%d/reactions", repo, commentID), nil, map[string]string{"content": content}, ConditionalRequest{}, false, nil)
	return RunnerReactionResult{Metadata: meta}, err
}

func (c *Client) doRunnerJSON(ctx context.Context, method, path string, query url.Values, in any, conditional ConditionalRequest, allowNotModified bool, out any) (ResponseMetadata, error) {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return ResponseMetadata{}, err
		}
		body = bytes.NewReader(data)
	}

	endpoint, err := c.endpoint(endpointWithQuery(path, query))
	if err != nil {
		return ResponseMetadata{}, err
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return ResponseMetadata{}, err
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
	conditional.apply(req)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return ResponseMetadata{}, err
	}
	defer resp.Body.Close()

	meta := responseMetadata(resp)
	if resp.StatusCode == http.StatusNotModified && allowNotModified {
		meta.NotModified = true
		_, _ = io.Copy(io.Discard, resp.Body)
		return meta, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return meta, &APIError{Method: method, URL: endpoint, StatusCode: resp.StatusCode, Body: redactTokenValue(string(data), c.Token)}
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, resp.Body)
		return meta, nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return meta, fmt.Errorf("decode GitHub response from %s: %w", endpoint, err)
	}
	return meta, nil
}

func (c ConditionalRequest) apply(req *http.Request) {
	if strings.TrimSpace(c.ETag) != "" {
		req.Header.Set("If-None-Match", c.ETag)
	}
	if strings.TrimSpace(c.LastModified) != "" {
		req.Header.Set("If-Modified-Since", c.LastModified)
	}
}

func responseMetadata(resp *http.Response) ResponseMetadata {
	if resp == nil {
		return ResponseMetadata{}
	}
	headers := resp.Header.Clone()
	meta := ResponseMetadata{
		StatusCode:   resp.StatusCode,
		Headers:      headers,
		ETag:         headers.Get("ETag"),
		LastModified: headers.Get("Last-Modified"),
		RateLimit: RateLimitMetadata{
			Resource: headers.Get("X-RateLimit-Resource"),
		},
		Pagination: parsePagination(headers.Get("Link")),
	}
	if seconds, ok := parseHeaderInt(headers, "X-Poll-Interval"); ok {
		meta.PollIntervalSeconds = seconds
		meta.PollInterval = time.Duration(seconds) * time.Second
	}
	if value, ok := parseHeaderInt(headers, "X-RateLimit-Limit"); ok {
		meta.RateLimit.Limit = value
	}
	if value, ok := parseHeaderInt(headers, "X-RateLimit-Remaining"); ok {
		meta.RateLimit.Remaining = value
	}
	if value, ok := parseHeaderInt(headers, "X-RateLimit-Used"); ok {
		meta.RateLimit.Used = value
	}
	if value, ok := parseHeaderInt64(headers, "X-RateLimit-Reset"); ok {
		meta.RateLimit.ResetUnix = value
		meta.RateLimit.ResetAt = time.Unix(value, 0).UTC()
	}
	if value, ok := parseHeaderInt(headers, "Retry-After"); ok {
		meta.RateLimit.RetryAfterSeconds = value
	}
	return meta
}

func addPageQuery(query url.Values, page RunnerPageOptions, defaultPerPage int) {
	perPage := page.PerPage
	if perPage <= 0 {
		perPage = defaultPerPage
	}
	if perPage > 0 {
		query.Set("per_page", strconv.Itoa(perPage))
	}
	if page.Page > 0 {
		query.Set("page", strconv.Itoa(page.Page))
	}
}

func addTimeQuery(query url.Values, key string, value *time.Time) {
	if value == nil || value.IsZero() {
		return
	}
	query.Set(key, value.UTC().Format(time.RFC3339))
}

func annotateCommentIssueNumbers(comments []Comment, issueNumber int) {
	for i := range comments {
		if issueNumber > 0 {
			comments[i].IssueNumber = issueNumber
			continue
		}
		if comments[i].IssueURL == "" {
			continue
		}
		n, err := ParseIssueNumber(comments[i].IssueURL)
		if err == nil {
			comments[i].IssueNumber = n
		}
	}
}

func permissionAllowsWrite(permission string) bool {
	switch strings.ToLower(strings.TrimSpace(permission)) {
	case "admin", "maintain", "write":
		return true
	default:
		return false
	}
}

func parseHeaderInt(headers http.Header, name string) (int, bool) {
	value := strings.TrimSpace(headers.Get(name))
	if value == "" {
		return 0, false
	}
	n, err := strconv.Atoi(value)
	return n, err == nil
}

func parseHeaderInt64(headers http.Header, name string) (int64, bool) {
	value := strings.TrimSpace(headers.Get(name))
	if value == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(value, 10, 64)
	return n, err == nil
}

func parsePagination(link string) PaginationMetadata {
	var pagination PaginationMetadata
	for _, rawPart := range strings.Split(link, ",") {
		part := strings.TrimSpace(rawPart)
		if !strings.HasPrefix(part, "<") {
			continue
		}
		end := strings.Index(part, ">")
		if end < 0 {
			continue
		}
		linkURL := part[1:end]
		rel := parseLinkRel(part[end+1:])
		switch rel {
		case "next":
			pagination.NextURL = linkURL
			pagination.NextPage = parseLinkPage(linkURL)
		case "prev":
			pagination.PrevURL = linkURL
			pagination.PrevPage = parseLinkPage(linkURL)
		case "first":
			pagination.FirstURL = linkURL
		case "last":
			pagination.LastURL = linkURL
			pagination.LastPage = parseLinkPage(linkURL)
		}
	}
	return pagination
}

func parseLinkRel(attrs string) string {
	for _, attr := range strings.Split(attrs, ";") {
		attr = strings.TrimSpace(attr)
		if !strings.HasPrefix(attr, "rel=") {
			continue
		}
		return strings.Trim(strings.TrimPrefix(attr, "rel="), `"`)
	}
	return ""
}

func parseLinkPage(linkURL string) int {
	u, err := url.Parse(linkURL)
	if err != nil {
		return 0
	}
	page, err := strconv.Atoi(u.Query().Get("page"))
	if err != nil {
		return 0
	}
	return page
}

var _ RunnerOperations = (*Client)(nil)
