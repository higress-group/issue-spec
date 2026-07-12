package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/codereview"
	"github.com/higress-group/issue-spec/internal/server/models"
)

// NativeOnboardingOperations is the credential-free metadata and authenticated
// repository registration surface consumed by self-hosted init. It remains
// separate from GitHub compatibility operations so init never infers a code
// provider from the issue server origin.
type NativeOnboardingOperations interface {
	GetNativeServerMetadata(context.Context) (NativeServerMetadata, error)
	EnsureNativeRepository(context.Context, string, NativeEnsureRepositoryInput) (NativeEnsureRepositoryResult, error)
	EnsureNativeActiveBinding(context.Context, models.RepoScope, NativeEnsureBindingInput) (NativeEnsureBindingResult, error)
}

type NativeServerMetadata struct {
	APIVersion       string                           `json:"api_version"`
	ServerInstanceID string                           `json:"server_instance_id"`
	APIURL           string                           `json:"api_url"`
	NativeAPIURL     string                           `json:"native_api_url"`
	WebURL           string                           `json:"web_url"`
	Transport        NativeTransport                  `json:"transport"`
	Providers        []codereview.ProviderDescription `json:"providers"`
}

type NativeTransport struct {
	Mode   string `json:"mode"`
	Secure bool   `json:"secure"`
}

type NativeEnsureRepositoryInput struct {
	Name          string `json:"name"`
	DisplayName   string `json:"display_name"`
	Description   string `json:"description,omitempty"`
	DefaultBranch string `json:"default_branch"`
}

type NativeEnsureRepositoryResult struct {
	Repository NativeRepositoryDetail `json:"repository"`
	Created    bool                   `json:"created"`
}

type NativeRepositoryDetail struct {
	ID                 string `json:"id"`
	OrganizationID     string `json:"organization_id"`
	Name               string `json:"name"`
	DisplayName        string `json:"display_name"`
	Visibility         string `json:"visibility"`
	DefaultBranch      string `json:"default_branch"`
	ContributionPolicy string `json:"contribution_policy"`
}

type NativeEnsureBindingInput struct {
	ProviderKey          string `json:"provider_key"`
	ExternalRepositoryID string `json:"external_repository_id"`
	CloneURL             string `json:"clone_url"`
	WebURL               string `json:"web_url"`
	DefaultBranch        string `json:"default_branch"`
}

type NativeEnsureBindingResult struct {
	Binding NativeBinding `json:"binding"`
	Created bool          `json:"created"`
}

func (c *Client) GetNativeServerMetadata(ctx context.Context) (NativeServerMetadata, error) {
	var result NativeServerMetadata
	_, err := c.doRunnerJSON(ctx, http.MethodGet, "/meta", nil, nil, ConditionalRequest{}, false, &result)
	if err != nil {
		return NativeServerMetadata{}, err
	}
	if result.APIVersion != "v1" || strings.TrimSpace(result.ServerInstanceID) == "" ||
		!absoluteHTTPURL(result.APIURL) || !absoluteHTTPURL(result.NativeAPIURL) || !absoluteHTTPURL(result.WebURL) ||
		strings.TrimSpace(result.Transport.Mode) == "" {
		return NativeServerMetadata{}, errors.New("native server metadata response is incomplete")
	}
	for i := range result.Providers {
		normalized, normalizeErr := result.Providers[i].Normalized(result.Providers[i].ProviderKey)
		if normalizeErr != nil {
			return NativeServerMetadata{}, fmt.Errorf("native server metadata provider: %w", normalizeErr)
		}
		result.Providers[i] = normalized
	}
	return result, nil
}

func (c *Client) EnsureNativeRepository(ctx context.Context, organizationID string, input NativeEnsureRepositoryInput) (NativeEnsureRepositoryResult, error) {
	organizationID = strings.TrimSpace(organizationID)
	if _, err := uuid.Parse(organizationID); err != nil || strings.TrimSpace(input.Name) == "" ||
		strings.TrimSpace(input.DisplayName) == "" || strings.TrimSpace(input.DefaultBranch) == "" {
		return NativeEnsureRepositoryResult{}, errors.New("native repository ensure input is invalid")
	}
	var result NativeEnsureRepositoryResult
	path := "/orgs/" + url.PathEscape(organizationID) + "/repos/ensure"
	_, err := c.doRunnerJSON(ctx, http.MethodPost, path, nil, input, ConditionalRequest{}, true, &result)
	if err != nil {
		return NativeEnsureRepositoryResult{}, err
	}
	if _, err := uuid.Parse(strings.TrimSpace(result.Repository.ID)); err != nil ||
		result.Repository.OrganizationID != organizationID || result.Repository.Name != strings.TrimSpace(input.Name) ||
		strings.TrimSpace(result.Repository.DefaultBranch) == "" {
		return NativeEnsureRepositoryResult{}, errors.New("native repository ensure response is incomplete")
	}
	return result, nil
}

func (c *Client) EnsureNativeActiveBinding(ctx context.Context, scope models.RepoScope, input NativeEnsureBindingInput) (NativeEnsureBindingResult, error) {
	if err := scope.Validate(); err != nil || strings.TrimSpace(input.ProviderKey) == "" ||
		strings.TrimSpace(input.ExternalRepositoryID) == "" || !absoluteHTTPURL(input.CloneURL) ||
		!absoluteHTTPURL(input.WebURL) || strings.TrimSpace(input.DefaultBranch) == "" {
		return NativeEnsureBindingResult{}, errors.New("native binding ensure input is invalid")
	}
	var result NativeEnsureBindingResult
	path := fmt.Sprintf("/orgs/%s/repos/%s/bindings/active", scope.OrgID, scope.RepoID)
	_, err := c.doRunnerJSON(ctx, http.MethodPut, path, nil, input, ConditionalRequest{}, true, &result)
	if err != nil {
		return NativeEnsureBindingResult{}, err
	}
	if _, err := uuid.Parse(strings.TrimSpace(result.Binding.ID)); err != nil || !result.Binding.Active ||
		result.Binding.ProviderKey != strings.TrimSpace(input.ProviderKey) ||
		result.Binding.ExternalRepositoryID != strings.TrimSpace(input.ExternalRepositoryID) {
		return NativeEnsureBindingResult{}, errors.New("native binding ensure response is incomplete")
	}
	return result, nil
}

func absoluteHTTPURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && parsed.IsAbs() && parsed.Host != "" && parsed.User == nil &&
		(parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.RawQuery == "" && parsed.Fragment == ""
}

var _ NativeOnboardingOperations = (*Client)(nil)
