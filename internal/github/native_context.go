package github

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// NativeContextOperations resolves trusted self-hosted repository UUID scopes
// from an origin-bound profile PAT. It is deliberately separate from
// RunnerOperations so runner serve never gains notification dependencies.
type NativeContextOperations interface {
	GetNativeContext(context.Context) (NativeContext, error)
	ListNativeContextRepositories(context.Context, string) (NativeRepositoriesContext, error)
}

type NativeContext struct {
	User          NativeContextUser           `json:"user"`
	Credential    NativeContextCredential     `json:"credential"`
	Organizations []NativeOrganizationContext `json:"organizations"`
}

type NativeContextUser struct {
	ID    string `json:"id"`
	Login string `json:"login"`
}

type NativeContextCredential struct {
	Kind                 string   `json:"kind"`
	Scopes               []string `json:"scopes"`
	RepositoryRestricted bool     `json:"repository_restricted"`
}

type NativeOrganizationContext struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type NativeRepositoriesContext struct {
	Repositories []NativeRepositoryContext `json:"repositories"`
}

type NativeRepositoryContext struct {
	Repository NativeRepositorySummary `json:"repository"`
}

type NativeRepositorySummary struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id"`
	Name           string `json:"name"`
}

func (c *Client) GetNativeContext(ctx context.Context) (NativeContext, error) {
	var result NativeContext
	_, err := c.doRunnerJSON(ctx, http.MethodGet, "/context", nil, nil, ConditionalRequest{}, false, &result)
	if err != nil {
		return NativeContext{}, err
	}
	return result, nil
}

func (c *Client) ListNativeContextRepositories(ctx context.Context, organizationID string) (NativeRepositoriesContext, error) {
	organizationID = strings.TrimSpace(organizationID)
	if organizationID == "" || strings.ContainsAny(organizationID, "/\\\r\n\t") {
		return NativeRepositoriesContext{}, errors.New("organization id is invalid")
	}
	var result NativeRepositoriesContext
	path := "/context/orgs/" + url.PathEscape(organizationID) + "/repos"
	_, err := c.doRunnerJSON(ctx, http.MethodGet, path, nil, nil, ConditionalRequest{}, false, &result)
	if err != nil {
		return NativeRepositoriesContext{}, err
	}
	return result, nil
}

var _ NativeContextOperations = (*Client)(nil)
