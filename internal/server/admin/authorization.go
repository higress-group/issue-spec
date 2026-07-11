package admin

import (
	"context"

	"github.com/google/uuid"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
)

type Action string

const (
	ActionSiteAdmin         Action = "site.admin"
	ActionOrganizationRead  Action = "organization.read"
	ActionOrganizationAdmin Action = "organization.admin"
	ActionRepositoryRead    Action = "repository.read"
	ActionRepositoryAdmin   Action = "repository.admin"
	ActionCredentialAdmin   Action = "credential.admin"
)

type AuthorizationRequest struct {
	Action         Action
	OrganizationID uuid.UUID
	RepositoryID   uuid.UUID
	TargetUserID   uuid.UUID
}

type Authorizer interface {
	Authorize(context.Context, serverauth.Principal, AuthorizationRequest) error
}

type AuthorizerFunc func(context.Context, serverauth.Principal, AuthorizationRequest) error

func (f AuthorizerFunc) Authorize(ctx context.Context, principal serverauth.Principal, request AuthorizationRequest) error {
	if f == nil {
		return ErrForbidden
	}
	return f(ctx, principal, request)
}

type DenyAllAuthorizer struct{}

func (DenyAllAuthorizer) Authorize(context.Context, serverauth.Principal, AuthorizationRequest) error {
	return ErrForbidden
}
