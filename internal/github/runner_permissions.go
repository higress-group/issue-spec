package github

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

type CollaboratorPermission struct {
	Permission string `json:"permission"`
	RoleName   string `json:"role_name,omitempty"`
	User       *User  `json:"user,omitempty"`
}

type PermissionOperations interface {
	GetCollaboratorPermission(context.Context, string, string) (CollaboratorPermission, error)
}

func (c *Client) GetCollaboratorPermission(ctx context.Context, repo, username string) (CollaboratorPermission, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return CollaboratorPermission{}, fmt.Errorf("collaborator username is empty")
	}
	var permission CollaboratorPermission
	err := c.doJSON(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/collaborators/%s/permission", repo, url.PathEscape(username)), nil, &permission)
	return permission, err
}

func (b *GHBackend) GetCollaboratorPermission(ctx context.Context, repo, username string) (CollaboratorPermission, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return CollaboratorPermission{}, fmt.Errorf("collaborator username is empty")
	}
	var permission CollaboratorPermission
	err := b.runAPIJSON(ctx, "GetCollaboratorPermission", http.MethodGet, fmt.Sprintf("/repos/%s/collaborators/%s/permission", repo, url.PathEscape(username)), nil, nil, &permission)
	return permission, err
}

var _ PermissionOperations = (*Client)(nil)
var _ PermissionOperations = (*GHBackend)(nil)
