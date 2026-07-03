package github

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type RunnerOperations interface {
	CheckRunnerAuth(context.Context) error
	CheckRunnerPreflight(context.Context, string) (RunnerPreflightResult, error)
	PollNotifications(context.Context, NotificationPollOptions) (NotificationPollResult, error)
	GetRepositorySubscription(context.Context, string) (RepositorySubscriptionResult, error)
	ListRepositoryIssueComments(context.Context, string, RepositoryIssueCommentsOptions) (IssueCommentsResult, error)
	ListRunnerIssueComments(context.Context, string, int, IssueCommentsOptions) (IssueCommentsResult, error)
	GetRunnerIssue(context.Context, string, int) (IssueContextResult, error)
	GetCollaboratorPermission(context.Context, string, string) (CollaboratorPermissionResult, error)
	CreateRunnerIssueComment(context.Context, string, int, string) (IssueCommentResult, error)
	UpdateRunnerIssueComment(context.Context, string, int64, string) (IssueCommentResult, error)
}

type RunnerResponseMetadata struct {
	StatusCode          int             `json:"status_code"`
	NotModified         bool            `json:"not_modified,omitempty"`
	Headers             http.Header     `json:"headers,omitempty"`
	PollIntervalSeconds int             `json:"poll_interval_seconds,omitempty"`
	ETag                string          `json:"etag,omitempty"`
	LastModified        string          `json:"last_modified,omitempty"`
	RateLimit           RunnerRateLimit `json:"rate_limit,omitempty"`
}

type RunnerRateLimit struct {
	Limit     int    `json:"limit,omitempty"`
	Remaining int    `json:"remaining,omitempty"`
	Used      int    `json:"used,omitempty"`
	Reset     int64  `json:"reset,omitempty"`
	Resource  string `json:"resource,omitempty"`
}

type NotificationPollOptions struct {
	All           bool
	Participating bool
	Since         string
	Before        string
	ETag          string
	LastModified  string
	PerPage       int
}

type NotificationPollResult struct {
	Notifications []Notification         `json:"notifications"`
	Metadata      RunnerResponseMetadata `json:"metadata"`
}

type Notification struct {
	ID         string              `json:"id"`
	Unread     bool                `json:"unread"`
	Reason     string              `json:"reason"`
	UpdatedAt  string              `json:"updated_at"`
	LastReadAt string              `json:"last_read_at"`
	URL        string              `json:"url"`
	Subject    NotificationSubject `json:"subject"`
	Repository RepositorySummary   `json:"repository"`
}

type NotificationSubject struct {
	Title            string `json:"title"`
	URL              string `json:"url"`
	LatestCommentURL string `json:"latest_comment_url"`
	Type             string `json:"type"`
}

type RepositorySummary struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	HTMLURL       string `json:"html_url"`
	DefaultBranch string `json:"default_branch"`
	Owner         *User  `json:"owner,omitempty"`
}

type RepositorySubscription struct {
	Subscribed bool   `json:"subscribed"`
	Ignored    bool   `json:"ignored"`
	Reason     string `json:"reason"`
	CreatedAt  string `json:"created_at"`
	URL        string `json:"url"`
}

type RepositorySubscriptionResult struct {
	Subscription RepositorySubscription `json:"subscription"`
	Metadata     RunnerResponseMetadata `json:"metadata"`
}

type RepositoryIssueCommentsOptions struct {
	Since        string
	ETag         string
	LastModified string
	PerPage      int
}

type IssueCommentsOptions struct {
	ETag         string
	LastModified string
	PerPage      int
}

type IssueCommentsResult struct {
	Comments []RunnerIssueComment   `json:"comments"`
	Metadata RunnerResponseMetadata `json:"metadata"`
}

type RunnerIssueComment struct {
	ID          int64  `json:"id"`
	HTMLURL     string `json:"html_url"`
	URL         string `json:"url"`
	IssueURL    string `json:"issue_url"`
	IssueNumber int    `json:"issue_number,omitempty"`
	Body        string `json:"body"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	User        *User  `json:"user,omitempty"`
}

type IssueCommentResult struct {
	Comment  RunnerIssueComment     `json:"comment"`
	Metadata RunnerResponseMetadata `json:"metadata"`
}

type IssueContextResult struct {
	Issue    Issue                  `json:"issue"`
	Metadata RunnerResponseMetadata `json:"metadata"`
}

type CollaboratorPermission struct {
	Permission string `json:"permission"`
	RoleName   string `json:"role_name"`
	User       *User  `json:"user,omitempty"`
}

func (p CollaboratorPermission) AllowsWrite() bool {
	switch strings.ToLower(strings.TrimSpace(p.Permission)) {
	case "write", "maintain", "admin":
		return true
	default:
		return false
	}
}

type CollaboratorPermissionResult struct {
	Permission CollaboratorPermission `json:"permission"`
	Metadata   RunnerResponseMetadata `json:"metadata"`
}

type RunnerPreflightResult struct {
	Backend      BackendInfo            `json:"backend"`
	User         User                   `json:"user"`
	Subscription RepositorySubscription `json:"subscription"`
	Metadata     RunnerResponseMetadata `json:"metadata"`
}

type GHRunnerErrorKind string

const (
	GHRunnerErrorMissingCLI GHRunnerErrorKind = "missing_gh"
	GHRunnerErrorAuth       GHRunnerErrorKind = "auth"
	GHRunnerErrorAPI        GHRunnerErrorKind = "api"
	GHRunnerErrorDecode     GHRunnerErrorKind = "decode"
	GHRunnerErrorPreflight  GHRunnerErrorKind = "preflight"
	GHRunnerErrorCommand    GHRunnerErrorKind = "command"
)

type GHRunnerError struct {
	Kind       GHRunnerErrorKind
	Operation  string
	Host       string
	StatusCode int
	Err        error
}

func (e *GHRunnerError) Error() string {
	parts := []string{fmt.Sprintf("gh runner %s error", e.Kind)}
	if e.Operation != "" {
		parts = append(parts, "operation "+e.Operation)
	}
	if e.Host != "" {
		parts = append(parts, "host "+e.Host)
	}
	if e.StatusCode != 0 {
		parts = append(parts, fmt.Sprintf("HTTP %d", e.StatusCode))
	}
	if e.Err != nil {
		parts = append(parts, e.Err.Error())
	}
	return strings.Join(parts, ": ")
}

func (e *GHRunnerError) Unwrap() error {
	return e.Err
}

func IsGHRunnerErrorKind(err error, kind GHRunnerErrorKind) bool {
	var runnerErr *GHRunnerError
	return errors.As(err, &runnerErr) && runnerErr.Kind == kind
}

func (b *GHBackend) CheckRunnerAuth(ctx context.Context) error {
	result, command, err := b.cli.runAuthRaw(ctx, b.Host, "runner auth status", []string{"auth", "status", "--active"})
	if err == nil && result.ExitCode == 0 {
		return nil
	}
	return b.wrapRunnerCommandError("CheckRunnerAuth", command, result, err, RunnerResponseMetadata{})
}

func (b *GHBackend) CheckRunnerPreflight(ctx context.Context, repo string) (RunnerPreflightResult, error) {
	out := RunnerPreflightResult{Backend: b.BackendInfo()}
	if err := b.CheckRunnerAuth(ctx); err != nil {
		return out, err
	}
	user, metadata, err := b.GetRunnerUser(ctx)
	if err != nil {
		return out, err
	}
	out.User = user
	out.Metadata = metadata
	subscription, err := b.GetRepositorySubscription(ctx, repo)
	if err != nil {
		var runnerErr *GHRunnerError
		if errors.As(err, &runnerErr) && runnerErr.StatusCode == http.StatusNotFound {
			return out, &GHRunnerError{Kind: GHRunnerErrorPreflight, Operation: "CheckRunnerPreflight", Host: b.Host, StatusCode: runnerErr.StatusCode, Err: err}
		}
		return out, err
	}
	out.Subscription = subscription.Subscription
	out.Metadata = subscription.Metadata
	if !subscription.Subscription.Subscribed || subscription.Subscription.Ignored {
		return out, &GHRunnerError{
			Kind:      GHRunnerErrorPreflight,
			Operation: "CheckRunnerPreflight",
			Host:      b.Host,
			Err:       fmt.Errorf("repository %s is not watched with active notifications", repo),
		}
	}
	return out, nil
}

func (b *GHBackend) GetRunnerUser(ctx context.Context) (User, RunnerResponseMetadata, error) {
	var user User
	metadata, err := b.runRunnerJSON(ctx, ExternalCLIAPIRequest{
		Operation: "GetRunnerUser",
		Method:    http.MethodGet,
		Endpoint:  "/user",
	}, &user)
	return user, metadata, err
}

func (b *GHBackend) PollNotifications(ctx context.Context, opts NotificationPollOptions) (NotificationPollResult, error) {
	query := url.Values{"per_page": {strconv.Itoa(perPage(opts.PerPage))}}
	if opts.All {
		query.Set("all", "true")
	}
	if opts.Participating {
		query.Set("participating", "true")
	}
	if strings.TrimSpace(opts.Since) != "" {
		query.Set("since", strings.TrimSpace(opts.Since))
	}
	if strings.TrimSpace(opts.Before) != "" {
		query.Set("before", strings.TrimSpace(opts.Before))
	}
	var notifications []Notification
	metadata, err := b.runRunnerJSONPages(ctx, ExternalCLIAPIRequest{
		Operation: "PollNotifications",
		Method:    http.MethodGet,
		Endpoint:  "/notifications",
		Headers:   conditionalHeaders(opts.ETag, opts.LastModified),
		Query:     query,
		Paginate:  true,
	}, "", &notifications)
	return NotificationPollResult{Notifications: notifications, Metadata: metadata}, err
}

func (b *GHBackend) GetRepositorySubscription(ctx context.Context, repo string) (RepositorySubscriptionResult, error) {
	var subscription RepositorySubscription
	metadata, err := b.runRunnerJSON(ctx, ExternalCLIAPIRequest{
		Operation: "GetRepositorySubscription",
		Method:    http.MethodGet,
		Endpoint:  "/repos/" + repo + "/subscription",
	}, &subscription)
	return RepositorySubscriptionResult{Subscription: subscription, Metadata: metadata}, err
}

func (b *GHBackend) ListRepositoryIssueComments(ctx context.Context, repo string, opts RepositoryIssueCommentsOptions) (IssueCommentsResult, error) {
	query := url.Values{"per_page": {strconv.Itoa(perPage(opts.PerPage))}}
	if strings.TrimSpace(opts.Since) != "" {
		query.Set("since", strings.TrimSpace(opts.Since))
	}
	var comments []RunnerIssueComment
	metadata, err := b.runRunnerJSONPages(ctx, ExternalCLIAPIRequest{
		Operation: "ListRepositoryIssueComments",
		Method:    http.MethodGet,
		Endpoint:  "/repos/" + repo + "/issues/comments",
		Headers:   conditionalHeaders(opts.ETag, opts.LastModified),
		Query:     query,
		Paginate:  true,
	}, "", &comments)
	populateIssueNumbers(comments)
	return IssueCommentsResult{Comments: comments, Metadata: metadata}, err
}

func (b *GHBackend) ListRunnerIssueComments(ctx context.Context, repo string, issueNumber int, opts IssueCommentsOptions) (IssueCommentsResult, error) {
	var comments []RunnerIssueComment
	metadata, err := b.runRunnerJSONPages(ctx, ExternalCLIAPIRequest{
		Operation: "ListRunnerIssueComments",
		Method:    http.MethodGet,
		Endpoint:  fmt.Sprintf("/repos/%s/issues/%d/comments", repo, issueNumber),
		Headers:   conditionalHeaders(opts.ETag, opts.LastModified),
		Query:     url.Values{"per_page": {strconv.Itoa(perPage(opts.PerPage))}},
		Paginate:  true,
	}, "", &comments)
	for i := range comments {
		if comments[i].IssueNumber == 0 {
			comments[i].IssueNumber = issueNumber
		}
	}
	return IssueCommentsResult{Comments: comments, Metadata: metadata}, err
}

func (b *GHBackend) GetRunnerIssue(ctx context.Context, repo string, issueNumber int) (IssueContextResult, error) {
	var issue Issue
	metadata, err := b.runRunnerJSON(ctx, ExternalCLIAPIRequest{
		Operation: "GetRunnerIssue",
		Method:    http.MethodGet,
		Endpoint:  fmt.Sprintf("/repos/%s/issues/%d", repo, issueNumber),
	}, &issue)
	return IssueContextResult{Issue: issue, Metadata: metadata}, err
}

func (b *GHBackend) GetCollaboratorPermission(ctx context.Context, repo, login string) (CollaboratorPermissionResult, error) {
	var permission CollaboratorPermission
	metadata, err := b.runRunnerJSON(ctx, ExternalCLIAPIRequest{
		Operation: "GetCollaboratorPermission",
		Method:    http.MethodGet,
		Endpoint:  fmt.Sprintf("/repos/%s/collaborators/%s/permission", repo, url.PathEscape(login)),
	}, &permission)
	return CollaboratorPermissionResult{Permission: permission, Metadata: metadata}, err
}

func (b *GHBackend) CreateRunnerIssueComment(ctx context.Context, repo string, issueNumber int, body string) (IssueCommentResult, error) {
	var comment RunnerIssueComment
	metadata, err := b.runRunnerJSON(ctx, ExternalCLIAPIRequest{
		Operation: "CreateRunnerIssueComment",
		Method:    http.MethodPost,
		Endpoint:  fmt.Sprintf("/repos/%s/issues/%d/comments", repo, issueNumber),
		Body:      map[string]string{"body": body},
	}, &comment)
	if comment.IssueNumber == 0 {
		comment.IssueNumber = issueNumber
	}
	return IssueCommentResult{Comment: comment, Metadata: metadata}, err
}

func (b *GHBackend) UpdateRunnerIssueComment(ctx context.Context, repo string, commentID int64, body string) (IssueCommentResult, error) {
	var comment RunnerIssueComment
	metadata, err := b.runRunnerJSON(ctx, ExternalCLIAPIRequest{
		Operation: "UpdateRunnerIssueComment",
		Method:    http.MethodPatch,
		Endpoint:  fmt.Sprintf("/repos/%s/issues/comments/%d", repo, commentID),
		Body:      map[string]string{"body": body},
	}, &comment)
	if comment.IssueNumber == 0 {
		comment.IssueNumber = issueNumberFromAPIURL(comment.IssueURL)
	}
	return IssueCommentResult{Comment: comment, Metadata: metadata}, err
}

func (b *GHBackend) runRunnerJSON(ctx context.Context, request ExternalCLIAPIRequest, out any) (RunnerResponseMetadata, error) {
	metadata, body, err := b.runIncludedAPI(ctx, request)
	if err != nil || metadata.NotModified || out == nil {
		return metadata, err
	}
	if err := DecodeCLIJSON(body, out); err != nil {
		return metadata, &GHRunnerError{Kind: GHRunnerErrorDecode, Operation: request.Operation, Host: b.Host, StatusCode: metadata.StatusCode, Err: err}
	}
	return metadata, nil
}

func (b *GHBackend) runRunnerJSONPages(ctx context.Context, request ExternalCLIAPIRequest, envelopeKey string, out any) (RunnerResponseMetadata, error) {
	metadata, body, err := b.runIncludedAPI(ctx, request)
	if err != nil || metadata.NotModified || out == nil {
		return metadata, err
	}
	var decodeErr error
	if strings.TrimSpace(envelopeKey) != "" {
		decodeErr = DecodeCLIJSONEnvelopePageStream(body, envelopeKey, out)
	} else {
		decodeErr = DecodeCLIJSONPageStream(body, out)
	}
	if decodeErr != nil {
		return metadata, &GHRunnerError{Kind: GHRunnerErrorDecode, Operation: request.Operation, Host: b.Host, StatusCode: metadata.StatusCode, Err: decodeErr}
	}
	return metadata, nil
}

func (b *GHBackend) runIncludedAPI(ctx context.Context, request ExternalCLIAPIRequest) (RunnerResponseMetadata, []byte, error) {
	request.Include = true
	result, command, runErr := b.cli.runAPIRaw(ctx, b.Host, request)
	metadata, body, parseErr := DecodeCLIHTTPResponse(result.Stdout)
	if parseErr != nil && result.ExitCode == 0 && runErr == nil {
		return metadata, nil, &GHRunnerError{Kind: GHRunnerErrorDecode, Operation: request.Operation, Host: b.Host, Err: parseErr}
	}
	if metadata.StatusCode == http.StatusNotModified {
		metadata.NotModified = true
		return metadata, nil, nil
	}
	if result.ExitCode != 0 || runErr != nil {
		return metadata, body, b.wrapRunnerCommandError(request.Operation, command, result, runErr, metadata)
	}
	if parseErr != nil {
		return metadata, nil, &GHRunnerError{Kind: GHRunnerErrorDecode, Operation: request.Operation, Host: b.Host, Err: parseErr}
	}
	if metadata.StatusCode >= 400 {
		return metadata, body, &GHRunnerError{
			Kind:       GHRunnerErrorAPI,
			Operation:  request.Operation,
			Host:       b.Host,
			StatusCode: metadata.StatusCode,
			Err:        fmt.Errorf("gh api returned HTTP %d", metadata.StatusCode),
		}
	}
	return metadata, body, nil
}

func (b *GHBackend) wrapRunnerCommandError(operation string, command ExternalCLICommand, result ExternalCLIResult, runErr error, metadata RunnerResponseMetadata) error {
	kind := GHRunnerErrorCommand
	if isMissingCLIError(result, runErr) {
		kind = GHRunnerErrorMissingCLI
	} else if metadata.StatusCode == http.StatusUnauthorized || result.ExitCode == 4 || strings.Contains(strings.ToLower(command.Operation), "auth") {
		kind = GHRunnerErrorAuth
	} else if metadata.StatusCode != 0 {
		kind = GHRunnerErrorAPI
	}
	err := runErr
	if command.Binary != "" || len(command.Args) > 0 || result.ExitCode != 0 {
		err = b.cli.commandError(command, result, runErr)
	}
	return &GHRunnerError{Kind: kind, Operation: operation, Host: b.Host, StatusCode: metadata.StatusCode, Err: err}
}

func isMissingCLIError(result ExternalCLIResult, err error) bool {
	if result.ExitCode == -1 {
		return true
	}
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "executable file not found") || strings.Contains(message, "no such file or directory")
}

func DecodeCLIHTTPResponse(data []byte) (RunnerResponseMetadata, []byte, error) {
	normalized := bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	cursor := 0
	for cursor < len(normalized) && normalized[cursor] == '\n' {
		cursor++
	}
	if !bytes.HasPrefix(normalized[cursor:], []byte("HTTP/")) {
		return RunnerResponseMetadata{}, data, nil
	}

	var metadata RunnerResponseMetadata
	var bodies bytes.Buffer
	for cursor < len(normalized) {
		for cursor < len(normalized) && normalized[cursor] == '\n' {
			cursor++
		}
		if cursor >= len(normalized) {
			break
		}
		if !bytes.HasPrefix(normalized[cursor:], []byte("HTTP/")) {
			part := bytes.TrimSpace(normalized[cursor:])
			if len(part) > 0 {
				if bodies.Len() > 0 {
					bodies.WriteByte('\n')
				}
				bodies.Write(part)
			}
			break
		}
		headerEnd := bytes.Index(normalized[cursor:], []byte("\n\n"))
		if headerEnd < 0 {
			return metadata, nil, fmt.Errorf("decode gh included response: missing header terminator")
		}
		headerBlock := normalized[cursor : cursor+headerEnd]
		pageMetadata, err := parseHTTPMetadata(headerBlock)
		if err != nil {
			return metadata, nil, err
		}
		metadata = pageMetadata
		bodyStart := cursor + headerEnd + 2
		next := findNextHTTPBlock(normalized, bodyStart)
		part := bytes.TrimSpace(normalized[bodyStart:next])
		if len(part) > 0 {
			if bodies.Len() > 0 {
				bodies.WriteByte('\n')
			}
			bodies.Write(part)
		}
		cursor = next
	}
	return metadata, bodies.Bytes(), nil
}

func parseHTTPMetadata(headerBlock []byte) (RunnerResponseMetadata, error) {
	lines := strings.Split(string(headerBlock), "\n")
	if len(lines) == 0 {
		return RunnerResponseMetadata{}, fmt.Errorf("decode gh included response: empty header block")
	}
	statusCode := 0
	for _, field := range strings.Fields(lines[0]) {
		if code, err := strconv.Atoi(field); err == nil {
			statusCode = code
			break
		}
	}
	if statusCode == 0 {
		return RunnerResponseMetadata{}, fmt.Errorf("decode gh included response: missing HTTP status in %q", lines[0])
	}
	headers := http.Header{}
	for _, line := range lines[1:] {
		key, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(key) == "" {
			continue
		}
		headers.Add(strings.TrimSpace(key), strings.TrimSpace(value))
	}
	return metadataFromHeaders(statusCode, headers), nil
}

func metadataFromHeaders(statusCode int, headers http.Header) RunnerResponseMetadata {
	return RunnerResponseMetadata{
		StatusCode:          statusCode,
		NotModified:         statusCode == http.StatusNotModified,
		Headers:             headers,
		PollIntervalSeconds: atoiHeader(headers, "X-Poll-Interval"),
		ETag:                headers.Get("ETag"),
		LastModified:        headers.Get("Last-Modified"),
		RateLimit: RunnerRateLimit{
			Limit:     atoiHeader(headers, "X-RateLimit-Limit"),
			Remaining: atoiHeader(headers, "X-RateLimit-Remaining"),
			Used:      atoiHeader(headers, "X-RateLimit-Used"),
			Reset:     atoi64Header(headers, "X-RateLimit-Reset"),
			Resource:  headers.Get("X-RateLimit-Resource"),
		},
	}
}

func findNextHTTPBlock(data []byte, start int) int {
	offset := start
	for {
		idx := bytes.Index(data[offset:], []byte("\nHTTP/"))
		if idx < 0 {
			return len(data)
		}
		candidate := offset + idx + 1
		if looksLikeHTTPStatus(data[candidate:]) {
			return candidate
		}
		offset = candidate + len("HTTP/")
	}
}

func looksLikeHTTPStatus(data []byte) bool {
	if !bytes.HasPrefix(data, []byte("HTTP/")) {
		return false
	}
	lineEnd := bytes.IndexByte(data, '\n')
	if lineEnd < 0 {
		lineEnd = len(data)
	}
	for _, field := range strings.Fields(string(data[:lineEnd])) {
		if code, err := strconv.Atoi(field); err == nil && code >= 100 && code <= 599 {
			return true
		}
	}
	return false
}

func conditionalHeaders(etag, lastModified string) http.Header {
	headers := http.Header{}
	if strings.TrimSpace(etag) != "" {
		headers.Set("If-None-Match", strings.TrimSpace(etag))
	}
	if strings.TrimSpace(lastModified) != "" {
		headers.Set("If-Modified-Since", strings.TrimSpace(lastModified))
	}
	return headers
}

func perPage(value int) int {
	if value <= 0 {
		return 100
	}
	if value > 100 {
		return 100
	}
	return value
}

func populateIssueNumbers(comments []RunnerIssueComment) {
	for i := range comments {
		if comments[i].IssueNumber == 0 {
			comments[i].IssueNumber = issueNumberFromAPIURL(comments[i].IssueURL)
		}
	}
}

func issueNumberFromAPIURL(rawURL string) int {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return 0
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for i := 0; i < len(parts)-1; i++ {
		if parts[i] != "issues" {
			continue
		}
		number, err := strconv.Atoi(parts[i+1])
		if err == nil && number > 0 {
			return number
		}
	}
	return 0
}

func atoiHeader(headers http.Header, name string) int {
	value, _ := strconv.Atoi(strings.TrimSpace(headers.Get(name)))
	return value
}

func atoi64Header(headers http.Header, name string) int64 {
	value, _ := strconv.ParseInt(strings.TrimSpace(headers.Get(name)), 10, 64)
	return value
}

var _ RunnerOperations = (*GHBackend)(nil)
