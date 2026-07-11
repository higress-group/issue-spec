package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/models"
)

type NativeBindingOperations interface {
	GetNativeActiveBinding(context.Context, models.RepoScope) (NativeBinding, error)
}

type NativeBinding struct {
	ID                   string    `json:"id"`
	ProviderKey          string    `json:"provider_key"`
	ExternalRepositoryID string    `json:"external_repository_id"`
	CloneURL             string    `json:"clone_url"`
	WebURL               string    `json:"web_url"`
	DefaultBranch        string    `json:"default_branch"`
	Version              int64     `json:"version"`
	Active               bool      `json:"active"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

func (c *Client) GetNativeActiveBinding(ctx context.Context, scope models.RepoScope) (NativeBinding, error) {
	if err := scope.Validate(); err != nil {
		return NativeBinding{}, err
	}
	var result NativeBinding
	path := fmt.Sprintf("/orgs/%s/repos/%s/bindings/active", scope.OrgID, scope.RepoID)
	_, err := c.doRunnerJSON(ctx, http.MethodGet, path, nil, nil, ConditionalRequest{}, false, &result)
	if err != nil {
		return NativeBinding{}, err
	}
	if _, err := uuid.Parse(strings.TrimSpace(result.ID)); err != nil || result.Version <= 0 || !result.Active ||
		strings.TrimSpace(result.ProviderKey) == "" || strings.TrimSpace(result.ExternalRepositoryID) == "" ||
		strings.TrimSpace(result.CloneURL) == "" || strings.TrimSpace(result.WebURL) == "" || strings.TrimSpace(result.DefaultBranch) == "" {
		return NativeBinding{}, errors.New("native active binding response is incomplete")
	}
	return result, nil
}

var _ NativeBindingOperations = (*Client)(nil)
