package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

var (
	ErrRepositoryLabelNotFound          = errors.New("repository label not found")
	ErrRepositoryLabelLookupUnsupported = errors.New("repository label lookup is unsupported")
)

func repositoryLabelPath(repo, name string) string {
	return "/repos/" + repo + "/labels/" + url.PathEscape(name)
}

func (c *Client) GetRepositoryLabel(ctx context.Context, repo, name string) (Label, error) {
	var label Label
	err := c.doJSON(ctx, http.MethodGet, repositoryLabelPath(repo, name), nil, &label)
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case http.StatusNotFound:
			return Label{}, ErrRepositoryLabelNotFound
		case http.StatusMethodNotAllowed:
			return Label{}, ErrRepositoryLabelLookupUnsupported
		}
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

// FeedbackPromotion is the outcome of one policy-bound priority decision.
type FeedbackPromotion struct {
	Label  string `json:"label"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// FeedbackPromotionBackend is the self-hosted repository's scoped feedback mutation.
type FeedbackPromotionBackend interface {
	PromoteFeedback(context.Context, string, int, string) (FeedbackPromotion, error)
}

func (c *Client) PromoteFeedback(ctx context.Context, repo string, issue int, policyRef string) (FeedbackPromotion, error) {
	var result FeedbackPromotion
	path := fmt.Sprintf("/repos/%s/issues/%d/feedback/promotion", repo, issue)
	err := c.doJSON(ctx, http.MethodPost, path, map[string]string{"policy_ref": policyRef}, &result)
	return result, err
}

var _ FeedbackPromotionBackend = (*Client)(nil)
var _ IssueLabelBackend = (*Client)(nil)
var _ IssueLabelBackend = (*GHBackend)(nil)
