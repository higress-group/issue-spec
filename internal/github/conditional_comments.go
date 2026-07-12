package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// GetCommentRepresentation discovers strict self-hosted CAS support and the
// exact caller-visible representation identity. GitHub and older servers omit
// the capability header and therefore fail closed before mutation.
func (c *Client) GetCommentRepresentation(ctx context.Context, repo string, commentID int64) (CommentRepresentation, error) {
	if commentID <= 0 {
		return CommentRepresentation{}, fmt.Errorf("comment id must be positive")
	}
	var comment Comment
	resp, err := c.doCommentRequest(ctx, http.MethodGet,
		fmt.Sprintf("/repos/%s/issues/comments/%d", repo, commentID), nil, nil, &comment)
	if err != nil {
		return CommentRepresentation{}, err
	}
	if resp.Header.Get(HeaderConditionalCommentMutation) != ConditionalCommentMutationVersion {
		return CommentRepresentation{}, ErrConditionalCommentMutationUnsupported
	}
	version, err := positiveRepresentationVersion(resp.Header)
	if err != nil {
		return CommentRepresentation{}, err
	}
	return CommentRepresentation{Comment: comment, RepresentationVersion: version,
		ETag: resp.Header.Get("ETag"), Guarantee: CommentMutationStrictConditional}, nil
}

// UpdateCommentConditional performs capability discovery before sending the
// mutation, then supplies the caller's expected representation version. The
// server remains authoritative for the race between observation and PATCH.
func (c *Client) UpdateCommentConditional(ctx context.Context, repo string, commentID, expected int64, body string) (CommentRepresentation, error) {
	if expected <= 0 {
		return CommentRepresentation{}, fmt.Errorf("expected representation version must be positive")
	}
	observed, err := c.GetCommentRepresentation(ctx, repo, commentID)
	if err != nil {
		return CommentRepresentation{}, err
	}
	if observed.RepresentationVersion != expected {
		return CommentRepresentation{}, &CommentMutationConflictError{Expected: expected, Current: observed.RepresentationVersion}
	}
	var comment Comment
	headers := http.Header{HeaderExpectedRepresentationVersion: {strconv.FormatInt(expected, 10)}}
	resp, err := c.doCommentRequest(ctx, http.MethodPatch,
		fmt.Sprintf("/repos/%s/issues/comments/%d", repo, commentID), headers, map[string]string{"body": body}, &comment)
	if err != nil {
		var apiErr *APIError
		if errorAsAPI(err, &apiErr) && (apiErr.StatusCode == http.StatusConflict || apiErr.StatusCode == http.StatusPreconditionFailed) {
			current, _ := strconv.ParseInt(strings.TrimSpace(resp.Header.Get(HeaderRepresentationVersion)), 10, 64)
			return CommentRepresentation{}, &CommentMutationConflictError{Expected: expected, Current: current}
		}
		return CommentRepresentation{}, err
	}
	version, err := positiveRepresentationVersion(resp.Header)
	if err != nil {
		return CommentRepresentation{}, err
	}
	if resp.Header.Get(HeaderConditionalCommentMutation) != ConditionalCommentMutationVersion {
		return CommentRepresentation{}, errorsUnsupportedAfterMutation()
	}
	return CommentRepresentation{Comment: comment, RepresentationVersion: version,
		ETag: resp.Header.Get("ETag"), Guarantee: CommentMutationStrictConditional}, nil
}

func errorsUnsupportedAfterMutation() error {
	// Capability discovery already passed before PATCH. Reaching this branch
	// means a non-conforming intermediary stripped the response contract.
	return fmt.Errorf("%w: mutation response omitted capability confirmation", ErrConditionalCommentMutationUnsupported)
}

func positiveRepresentationVersion(header http.Header) (int64, error) {
	value, err := strconv.ParseInt(strings.TrimSpace(header.Get(HeaderRepresentationVersion)), 10, 64)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("comment response omitted a valid representation version")
	}
	return value, nil
}

func (c *Client) doCommentRequest(ctx context.Context, method, path string, headers http.Header, input, output any) (*http.Response, error) {
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
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
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	for name, values := range headers {
		for _, value := range values {
			req.Header.Add(name, value)
		}
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
	if output == nil || resp.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp, nil
	}
	if err := json.NewDecoder(resp.Body).Decode(output); err != nil {
		return resp, fmt.Errorf("decode GitHub response from %s: %w", endpoint, err)
	}
	return resp, nil
}

var _ ConditionalCommentBackend = (*Client)(nil)
