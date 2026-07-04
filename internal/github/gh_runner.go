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
	"time"
)

type RunnerPreflightResult struct {
	Backend      BackendInfo            `json:"backend"`
	User         User                   `json:"user"`
	Subscription RepositorySubscription `json:"subscription"`
	Metadata     ResponseMetadata       `json:"metadata"`
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
	return b.wrapRunnerCommandError("CheckRunnerAuth", command, result, err, ResponseMetadata{})
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

func (b *GHBackend) GetRunnerUser(ctx context.Context) (User, ResponseMetadata, error) {
	var user User
	metadata, err := b.runRunnerJSON(ctx, ExternalCLIAPIRequest{
		Operation: "GetRunnerUser",
		Method:    http.MethodGet,
		Endpoint:  "/user",
	}, &user)
	return user, metadata, err
}

func (b *GHBackend) PollNotifications(ctx context.Context, opts NotificationListOptions) (NotificationListResult, error) {
	endpoint := opts.Page.CursorURL
	query := url.Values{}
	if endpoint == "" {
		endpoint = "/notifications"
		query.Set("per_page", strconv.Itoa(perPage(opts.Page.PerPage, defaultRunnerNotificationsPerPage)))
		addTimeQuery(query, "since", opts.Since)
		addTimeQuery(query, "before", opts.Before)
	}
	if opts.All {
		query.Set("all", "true")
	}
	if opts.Participating {
		query.Set("participating", "true")
	}
	var notifications []Notification
	metadata, err := b.runRunnerJSONPages(ctx, ExternalCLIAPIRequest{
		Operation: "PollNotifications",
		Method:    http.MethodGet,
		Endpoint:  endpoint,
		Headers:   conditionalHeaders(opts.ETag, opts.LastModified),
		Query:     query,
		Paginate:  true,
	}, "", &notifications)
	return NotificationListResult{Notifications: notifications, Metadata: metadata}, err
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

func (b *GHBackend) ListRepositoryIssueCommentsPage(ctx context.Context, repo string, opts CommentListOptions) (IssueCommentsResult, error) {
	endpoint := opts.Page.CursorURL
	query := url.Values{}
	if endpoint == "" {
		endpoint = "/repos/" + repo + "/issues/comments"
		query.Set("per_page", strconv.Itoa(perPage(opts.Page.PerPage, defaultRunnerCommentsPerPage)))
		addTimeQuery(query, "since", opts.Since)
	}
	var comments []Comment
	metadata, err := b.runRunnerJSONPages(ctx, ExternalCLIAPIRequest{
		Operation: "ListRepositoryIssueCommentsPage",
		Method:    http.MethodGet,
		Endpoint:  endpoint,
		Headers:   conditionalHeaders(opts.ETag, opts.LastModified),
		Query:     query,
		Paginate:  true,
	}, "", &comments)
	annotateCommentIssueNumbers(comments, 0)
	return IssueCommentsResult{Comments: comments, Metadata: metadata}, err
}

func (b *GHBackend) ListIssueCommentsPage(ctx context.Context, repo string, issueNumber int, opts CommentListOptions) (IssueCommentsResult, error) {
	endpoint := opts.Page.CursorURL
	query := url.Values{}
	if endpoint == "" {
		endpoint = fmt.Sprintf("/repos/%s/issues/%d/comments", repo, issueNumber)
		query.Set("per_page", strconv.Itoa(perPage(opts.Page.PerPage, defaultRunnerCommentsPerPage)))
		addTimeQuery(query, "since", opts.Since)
	}
	var comments []Comment
	metadata, err := b.runRunnerJSONPages(ctx, ExternalCLIAPIRequest{
		Operation: "ListIssueCommentsPage",
		Method:    http.MethodGet,
		Endpoint:  endpoint,
		Headers:   conditionalHeaders(opts.ETag, opts.LastModified),
		Query:     query,
		Paginate:  true,
	}, "", &comments)
	annotateCommentIssueNumbers(comments, issueNumber)
	return IssueCommentsResult{Comments: comments, Metadata: metadata}, err
}

func (b *GHBackend) GetIssueContext(ctx context.Context, repo string, issueNumber int, conditional ConditionalRequest) (IssueContextResult, error) {
	var issue Issue
	metadata, err := b.runRunnerJSON(ctx, ExternalCLIAPIRequest{
		Operation: "GetIssueContext",
		Method:    http.MethodGet,
		Endpoint:  fmt.Sprintf("/repos/%s/issues/%d", repo, issueNumber),
		Headers:   conditionalHeaders(conditional.ETag, conditional.LastModified),
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
	return CollaboratorPermissionResult{Permission: permission, CanWrite: permissionAllowsWrite(permission.Permission), Metadata: metadata}, err
}

func (b *GHBackend) CreateRunnerComment(ctx context.Context, repo string, issueNumber int, body string) (RunnerCommentResult, error) {
	var comment Comment
	metadata, err := b.runRunnerJSON(ctx, ExternalCLIAPIRequest{
		Operation: "CreateRunnerComment",
		Method:    http.MethodPost,
		Endpoint:  fmt.Sprintf("/repos/%s/issues/%d/comments", repo, issueNumber),
		Body:      map[string]string{"body": body},
	}, &comment)
	if comment.IssueNumber == 0 {
		comment.IssueNumber = issueNumber
	}
	return RunnerCommentResult{Comment: comment, Metadata: metadata}, err
}

func (b *GHBackend) UpdateRunnerComment(ctx context.Context, repo string, commentID int64, body string) (RunnerCommentResult, error) {
	var comment Comment
	metadata, err := b.runRunnerJSON(ctx, ExternalCLIAPIRequest{
		Operation: "UpdateRunnerComment",
		Method:    http.MethodPatch,
		Endpoint:  fmt.Sprintf("/repos/%s/issues/comments/%d", repo, commentID),
		Body:      map[string]string{"body": body},
	}, &comment)
	if comment.IssueNumber == 0 {
		comment.IssueNumber = issueNumberFromAPIURL(comment.IssueURL)
	}
	return RunnerCommentResult{Comment: comment, Metadata: metadata}, err
}

func (b *GHBackend) AddCommentReaction(ctx context.Context, repo string, commentID int64, content string) (RunnerReactionResult, error) {
	metadata, err := b.runRunnerJSON(ctx, ExternalCLIAPIRequest{
		Operation: "AddCommentReaction",
		Method:    http.MethodPost,
		Endpoint:  fmt.Sprintf("/repos/%s/issues/comments/%d/reactions", repo, commentID),
		Body:      map[string]string{"content": content},
	}, nil)
	return RunnerReactionResult{Metadata: metadata}, err
}

func (b *GHBackend) runRunnerJSON(ctx context.Context, request ExternalCLIAPIRequest, out any) (ResponseMetadata, error) {
	metadata, body, err := b.runIncludedAPI(ctx, request)
	if err != nil || metadata.NotModified || out == nil {
		return metadata, err
	}
	if err := DecodeCLIJSON(body, out); err != nil {
		return metadata, &GHRunnerError{Kind: GHRunnerErrorDecode, Operation: request.Operation, Host: b.Host, StatusCode: metadata.StatusCode, Err: err}
	}
	return metadata, nil
}

func (b *GHBackend) runRunnerJSONPages(ctx context.Context, request ExternalCLIAPIRequest, envelopeKey string, out any) (ResponseMetadata, error) {
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

func (b *GHBackend) runIncludedAPI(ctx context.Context, request ExternalCLIAPIRequest) (ResponseMetadata, []byte, error) {
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

func (b *GHBackend) wrapRunnerCommandError(operation string, command ExternalCLICommand, result ExternalCLIResult, runErr error, metadata ResponseMetadata) error {
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

func DecodeCLIHTTPResponse(data []byte) (ResponseMetadata, []byte, error) {
	normalized := bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	cursor := 0
	for cursor < len(normalized) && normalized[cursor] == '\n' {
		cursor++
	}
	if !bytes.HasPrefix(normalized[cursor:], []byte("HTTP/")) {
		return ResponseMetadata{}, data, nil
	}

	var metadata ResponseMetadata
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

func parseHTTPMetadata(headerBlock []byte) (ResponseMetadata, error) {
	lines := strings.Split(string(headerBlock), "\n")
	if len(lines) == 0 {
		return ResponseMetadata{}, fmt.Errorf("decode gh included response: empty header block")
	}
	statusCode := 0
	for _, field := range strings.Fields(lines[0]) {
		if code, err := strconv.Atoi(field); err == nil {
			statusCode = code
			break
		}
	}
	if statusCode == 0 {
		return ResponseMetadata{}, fmt.Errorf("decode gh included response: missing HTTP status in %q", lines[0])
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

func metadataFromHeaders(statusCode int, headers http.Header) ResponseMetadata {
	resetUnix := atoi64Header(headers, "X-RateLimit-Reset")
	var resetAt time.Time
	if resetUnix > 0 {
		resetAt = time.Unix(resetUnix, 0).UTC()
	}
	return ResponseMetadata{
		StatusCode:          statusCode,
		NotModified:         statusCode == http.StatusNotModified,
		Headers:             headers,
		PollIntervalSeconds: atoiHeader(headers, "X-Poll-Interval"),
		ETag:                headers.Get("ETag"),
		LastModified:        headers.Get("Last-Modified"),
		RateLimit: RateLimitMetadata{
			Limit:             atoiHeader(headers, "X-RateLimit-Limit"),
			Remaining:         atoiHeader(headers, "X-RateLimit-Remaining"),
			Used:              atoiHeader(headers, "X-RateLimit-Used"),
			ResetUnix:         resetUnix,
			ResetAt:           resetAt,
			Resource:          headers.Get("X-RateLimit-Resource"),
			RetryAfterSeconds: atoiHeader(headers, "Retry-After"),
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

func perPage(value, defaultValue int) int {
	if value <= 0 {
		return defaultValue
	}
	if value > 100 {
		return 100
	}
	return value
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
