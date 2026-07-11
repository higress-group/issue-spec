// Package bindingsapi exposes credential-free source binding administration as
// a composable native RouteSet.
package bindingsapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"
	adminservice "github.com/higress-group/issue-spec/internal/server/admin"
	adminapi "github.com/higress-group/issue-spec/internal/server/api/native/admin"
	"github.com/higress-group/issue-spec/internal/server/api/routeset"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/bindings"
	"github.com/higress-group/issue-spec/internal/server/models"
)

type Service interface {
	ActiveBinding(context.Context, authz.Subject, models.RepoScope) (bindings.Binding, error)
	CreateBindingVersion(context.Context, authz.Subject, adminservice.Actor, models.RepoScope, bindings.CreateBindingVersionInput) (bindings.Binding, error)
	DeactivateBinding(context.Context, authz.Subject, adminservice.Actor, models.RepoScope) error
}

type Dependencies struct {
	Service      Service
	Authenticate adminapi.Authenticate
}

func NewRouteSet(deps Dependencies) (routeset.RouteSet, error) {
	if deps.Service == nil || deps.Authenticate == nil {
		return routeset.RouteSet{}, errors.New("native bindings: service and authentication are required")
	}
	h := handlers{service: deps.Service}
	protect := func(handler http.HandlerFunc) http.Handler {
		return adminapi.WithRequestID(deps.Authenticate(handler))
	}
	set := routeset.RouteSet{Name: "native-bindings", Routes: []routeset.Route{
		{Name: "native.bindings.active", Method: http.MethodGet, Pattern: "/api/v1/orgs/{org}/repos/{repo}/bindings/active", Handler: protect(h.active)},
		{Name: "native.bindings.create_version", Method: http.MethodPost, Pattern: "/api/v1/orgs/{org}/repos/{repo}/bindings", Handler: protect(h.create)},
		{Name: "native.bindings.deactivate", Method: http.MethodDelete, Pattern: "/api/v1/orgs/{org}/repos/{repo}/bindings/active", Handler: protect(h.deactivate)},
	}}
	return set, set.Validate()
}

func (h handlers) deactivate(w http.ResponseWriter, r *http.Request) {
	principal, scope, ok := requestScope(w, r)
	if !ok {
		return
	}
	if err := h.service.DeactivateBinding(r.Context(), authz.Authenticated(principal),
		adminservice.ActorFromPrincipal(principal, adminapi.RequestID(r)), scope); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type handlers struct{ service Service }

func (h handlers) active(w http.ResponseWriter, r *http.Request) {
	principal, scope, ok := requestScope(w, r)
	if !ok {
		return
	}
	item, err := h.service.ActiveBinding(r.Context(), authz.Authenticated(principal), scope)
	if err != nil {
		writeError(w, err)
		return
	}
	adminapi.WriteJSON(w, http.StatusOK, item)
}

func (h handlers) create(w http.ResponseWriter, r *http.Request) {
	principal, scope, ok := requestScope(w, r)
	if !ok {
		return
	}
	var input bindings.CreateBindingVersionInput
	if err := adminapi.DecodeJSON(w, r, &input); err != nil {
		adminapi.WriteProblem(w, http.StatusBadRequest, "invalid_json", "Invalid request body")
		return
	}
	item, err := h.service.CreateBindingVersion(r.Context(), authz.Authenticated(principal),
		adminservice.ActorFromPrincipal(principal, adminapi.RequestID(r)), scope, input)
	if err != nil {
		writeError(w, err)
		return
	}
	adminapi.WriteJSON(w, http.StatusCreated, item)
}

func requestScope(w http.ResponseWriter, r *http.Request) (serverauth.Principal, models.RepoScope, bool) {
	principal, ok := serverauth.PrincipalFromContext(r.Context())
	if !ok || principal.User.ID == uuid.Nil {
		adminapi.WriteProblem(w, http.StatusUnauthorized, "authentication_required", "Authentication required")
		return serverauth.Principal{}, models.RepoScope{}, false
	}
	orgID, orgErr := uuid.Parse(r.PathValue("org"))
	repoID, repoErr := uuid.Parse(r.PathValue("repo"))
	if orgErr != nil || repoErr != nil {
		adminapi.WriteProblem(w, http.StatusUnprocessableEntity, "invalid_request", "Invalid repository")
		return serverauth.Principal{}, models.RepoScope{}, false
	}
	return principal, models.RepoScope{OrgID: orgID, RepoID: repoID}, true
}

func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, adminservice.ErrNotFound):
		adminapi.WriteProblem(w, http.StatusNotFound, "not_found", "Not found")
	case errors.Is(err, adminservice.ErrForbidden):
		adminapi.WriteProblem(w, http.StatusForbidden, "forbidden", "Forbidden")
	case errors.Is(err, adminservice.ErrVersionConflict):
		adminapi.WriteProblem(w, http.StatusConflict, "version_conflict", "Resource version conflict")
	case errors.Is(err, adminservice.ErrConflict):
		adminapi.WriteProblem(w, http.StatusConflict, "conflict", "Resource conflict")
	case errors.Is(err, adminservice.ErrInvalidInput):
		adminapi.WriteProblem(w, http.StatusUnprocessableEntity, "invalid_request", "Invalid request")
	default:
		adminapi.WriteProblem(w, http.StatusInternalServerError, "internal_error", "Request failed")
	}
}
