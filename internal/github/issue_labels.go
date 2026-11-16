package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

var ErrRepositoryLabelNotFound = errors.New("repository label not found")

func repositoryLabelPath(repo, name string) string {
	return "/repos/" + repo + "/labels/" + url.PathEscape(name)
}

func (c *Client) GetRepositoryLabel(ctx context.Context, repo, name string) (Label, error) {
	var label Label
	err := c.doJSON(ctx, http.MethodGet, repositoryLabelPath(repo, name), nil, &label)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
		return Label{}, ErrRepositoryLabelNotFound
	}
	return label, err
}

func (c *Client) UpdateRepositoryLabelDescription(ctx context.Context, repo, name, description string) (Label, error) {
	var label Label
	err := c.doJSON(ctx, http.MethodPatch, repositoryLabelPath(repo, name), map[string]string{"description": description}, &label)
	return label, err
}

func (c *Client) AddIssueLabels(ctx context.Context, repo string, issue int, names []string) ([]Label, error) {
	var labels []Label
	path := fmt.Sprintf("/repos/%s/issues/%d/labels", repo, issue)
	err := c.doJSON(ctx, http.MethodPost, path, map[string][]string{"labels": names}, &labels)
	return labels, err
}

func (b *GHBackend) GetRepositoryLabel(ctx context.Context, repo, name string) (Label, error) {
	var label Label
	metadata, err := b.runRunnerJSON(ctx, ExternalCLIAPIRequest{
		Operation: "GetRepositoryLabel",
		Method:    http.MethodGet,
		Endpoint:  repositoryLabelPath(repo, name),
	}, &label)
	if metadata.StatusCode == http.StatusNotFound {
		return Label{}, ErrRepositoryLabelNotFound
	}
	return label, err
}

func (b *GHBackend) UpdateRepositoryLabelDescription(ctx context.Context, repo, name, description string) (Label, error) {
	var label Label
	err := b.runJSON(ctx, ExternalCLIAPIRequest{
		Operation: "UpdateRepositoryLabelDescription",
		Method:    http.MethodPatch,
		Endpoint:  repositoryLabelPath(repo, name),
		Body:      map[string]string{"description": description},
	}, &label)
	return label, err
}

func (b *GHBackend) AddIssueLabels(ctx context.Context, repo string, issue int, names []string) ([]Label, error) {
	var labels []Label
	err := b.runJSON(ctx, ExternalCLIAPIRequest{
		Operation: "AddIssueLabels",
		Method:    http.MethodPost,
		Endpoint:  fmt.Sprintf("/repos/%s/issues/%d/labels", repo, issue),
		Body:      map[string][]string{"labels": names},
	}, &labels)
	return labels, err
}

var _ IssueLabelBackend = (*Client)(nil)
var _ IssueLabelBackend = (*GHBackend)(nil)
