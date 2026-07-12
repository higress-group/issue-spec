// Package permissions exposes the collaborator-permission compatibility route.
package permissions

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/higress-group/issue-spec/internal/server/api/github/codec"
	"github.com/higress-group/issue-spec/internal/server/api/github/conditional"
	"github.com/higress-group/issue-spec/internal/server/api/github/issues"
	"github.com/higress-group/issue-spec/internal/server/api/github/pagination"
	"github.com/higress-group/issue-spec/internal/server/api/routeset"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/higress-group/issue-spec/internal/server/store"
)

type Service struct {
	database   *store.Store
	authorizer issues.RepositoryAuthorizer
}

func NewService(database *store.Store, authorizer issues.RepositoryAuthorizer) (*Service, error) {
	if database == nil || authorizer == nil {
		return nil, errors.New("github permissions: store and authorizer are required")
	}
	return &Service{database: database, authorizer: authorizer}, nil
}

func (s *Service) Get(ctx context.Context, owner, repository, login string, caller authz.Subject) (models.RepositoryResource, models.ProtocolUser, authz.Permission, error) {
	resource, err := s.database.ResolveRepository(ctx, owner, repository)
	if err != nil {
		return models.RepositoryResource{}, models.ProtocolUser{}, authz.PermissionNone, err
	}
	callerDecision, err := s.authorizer.EvaluateRepository(ctx, caller, authz.RepositoryRequest{Scope: resource.Scope, Operation: authz.OperationRead})
	if err != nil {
		return models.RepositoryResource{}, models.ProtocolUser{}, authz.PermissionNone, err
	}
	if !callerDecision.Allowed {
		return models.RepositoryResource{}, models.ProtocolUser{}, authz.PermissionNone, &issues.DecisionError{Decision: callerDecision}
	}
	// The target identity is looked up only after the caller has passed tenant
	// visibility and credential-cap checks.
	user, err := s.database.ProtocolUserForRepository(ctx, resource.Scope, login)
	if err != nil {
		return models.RepositoryResource{}, models.ProtocolUser{}, authz.PermissionNone, err
	}
	target := authz.Authenticated(serverauth.Principal{User: serverauth.User{ID: user.ID, Login: user.Login, Status: user.Status}, Kind: serverauth.CredentialSession})
	decision, err := s.authorizer.EvaluateRepository(ctx, target, authz.RepositoryRequest{Scope: resource.Scope, Operation: authz.OperationRead})
	if err != nil {
		return models.RepositoryResource{}, models.ProtocolUser{}, authz.PermissionNone, err
	}
	return resource, user, decision.EffectivePermission, nil
}

type Dependencies struct {
	Service        *Service
	Presenter      codec.Presenter
	Authentication serverauth.Middleware
	Conditional    conditional.Policy
}

func NewRouteSet(deps Dependencies) (routeset.RouteSet, error) {
	if deps.Service == nil {
		return routeset.RouteSet{}, errors.New("github permissions: service is required")
	}
	authentication := issues.ConfigureCompatibilityAuthentication(deps.Authentication)
	h := handler{service: deps.Service, presenter: deps.Presenter, conditional: deps.Conditional}
	set := routeset.RouteSet{Name: "github-permissions", Routes: []routeset.Route{{
		Name: "github.collaborators.permission", Method: http.MethodGet,
		Pattern: "/repos/{owner}/{repo}/collaborators/{login}/permission",
		Handler: issues.WithRequestID(authentication.AuthenticateOptional(http.HandlerFunc(h.get))),
	}}}
	return set, set.Validate()
}

type handler struct {
	service     *Service
	presenter   codec.Presenter
	conditional conditional.Policy
}

func (h handler) get(w http.ResponseWriter, r *http.Request) {
	resource, user, permission, err := h.service.Get(r.Context(), r.PathValue("owner"), r.PathValue("repo"), r.PathValue("login"), issues.Subject(r))
	if err != nil {
		issues.WriteError(w, r, err)
		return
	}
	etag := pagination.StrongETag("collaborator-permission", resource.Scope.OrgID, resource.Scope.RepoID,
		user.ID, user.Login, permission)
	// Authority can change without mutating the user row. ETag captures the
	// evaluated permission; omit Last-Modified rather than emit a stale clock.
	if pagination.WriteNotModified(w, r, etag, time.Time{}, h.conditional.Rate()) {
		return
	}
	value := permission.String()
	issues.WriteJSON(w, http.StatusOK, h.presenter.PresentPermission(value, value,
		codec.UserView{StableID: user.ID.String(), Login: user.Login}))
}
