// Package referencesapi exposes mutable, namespaced external references.
package referencesapi

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
	ListReferences(context.Context, authz.Subject, models.RepoScope, uuid.UUID) ([]bindings.Reference, error)
	UpsertReference(context.Context, authz.Subject, adminservice.Actor, models.RepoScope, bindings.UpsertReferenceInput) (bindings.Reference, error)
	DeleteReference(context.Context, authz.Subject, adminservice.Actor, models.RepoScope, uuid.UUID, uuid.UUID) error
}

type Dependencies struct {
	Service      Service
	Authenticate adminapi.Authenticate
}

func NewRouteSet(deps Dependencies) (routeset.RouteSet, error) {
	if deps.Service == nil || deps.Authenticate == nil {
		return routeset.RouteSet{}, errors.New("native references: service and authentication are required")
	}
	h := handlers{service: deps.Service}
	protect := func(handler http.HandlerFunc) http.Handler { return adminapi.WithRequestID(deps.Authenticate(handler)) }
	set := routeset.RouteSet{Name: "native-references", Routes: []routeset.Route{
		{Name: "native.references.list", Method: http.MethodGet, Pattern: "/api/v1/orgs/{org}/repos/{repo}/issues/{issue}/references", Handler: protect(h.list)},
		{Name: "native.references.upsert", Method: http.MethodPut, Pattern: "/api/v1/orgs/{org}/repos/{repo}/issues/{issue}/references", Handler: protect(h.upsert)},
		{Name: "native.references.delete", Method: http.MethodDelete, Pattern: "/api/v1/orgs/{org}/repos/{repo}/issues/{issue}/references/{reference}", Handler: protect(h.delete)},
	}}
	return set, set.Validate()
}

func (h handlers) delete(w http.ResponseWriter, r *http.Request) {
	principal, scope, issueID, ok := requestScope(w, r)
	if !ok {
		return
	}
	referenceID, err := uuid.Parse(r.PathValue("reference"))
	if err != nil {
		adminapi.WriteProblem(w, http.StatusUnprocessableEntity, "invalid_request", "Invalid reference")
		return
	}
	if err := h.service.DeleteReference(r.Context(), authz.Authenticated(principal),
		adminservice.ActorFromPrincipal(principal, adminapi.RequestID(r)), scope, issueID, referenceID); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type handlers struct{ service Service }

func (h handlers) list(w http.ResponseWriter, r *http.Request) {
	principal, scope, issueID, ok := requestScope(w, r)
	if !ok {
		return
	}
	items, err := h.service.ListReferences(r.Context(), authz.Authenticated(principal), scope, issueID)
	if err != nil {
		writeError(w, err)
		return
	}
	if items == nil {
		items = []bindings.Reference{}
	}
	adminapi.WriteJSON(w, http.StatusOK, map[string]any{"references": items})
}

func (h handlers) upsert(w http.ResponseWriter, r *http.Request) {
	principal, scope, issueID, ok := requestScope(w, r)
	if !ok {
		return
	}
	var input bindings.UpsertReferenceInput
	if err := adminapi.DecodeJSON(w, r, &input); err != nil {
		adminapi.WriteProblem(w, http.StatusBadRequest, "invalid_json", "Invalid request body")
		return
	}
	if input.IssueID != uuid.Nil && input.IssueID != issueID {
		adminapi.WriteProblem(w, http.StatusUnprocessableEntity, "invalid_request", "Issue mismatch")
		return
	}
	input.IssueID = issueID
	item, err := h.service.UpsertReference(r.Context(), authz.Authenticated(principal),
		adminservice.ActorFromPrincipal(principal, adminapi.RequestID(r)), scope, input)
	if err != nil {
		writeError(w, err)
		return
	}
	adminapi.WriteJSON(w, http.StatusOK, item)
}

func requestScope(w http.ResponseWriter, r *http.Request) (serverauth.Principal, models.RepoScope, uuid.UUID, bool) {
	principal, ok := serverauth.PrincipalFromContext(r.Context())
	if !ok || principal.User.ID == uuid.Nil {
		adminapi.WriteProblem(w, http.StatusUnauthorized, "authentication_required", "Authentication required")
		return serverauth.Principal{}, models.RepoScope{}, uuid.Nil, false
	}
	orgID, orgErr := uuid.Parse(r.PathValue("org"))
	repoID, repoErr := uuid.Parse(r.PathValue("repo"))
	issueID, issueErr := uuid.Parse(r.PathValue("issue"))
	if orgErr != nil || repoErr != nil || issueErr != nil {
		adminapi.WriteProblem(w, http.StatusUnprocessableEntity, "invalid_request", "Invalid repository or issue")
		return serverauth.Principal{}, models.RepoScope{}, uuid.Nil, false
	}
	return principal, models.RepoScope{OrgID: orgID, RepoID: repoID}, issueID, true
}

func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, adminservice.ErrNotFound):
		adminapi.WriteProblem(w, http.StatusNotFound, "not_found", "Not found")
	case errors.Is(err, adminservice.ErrForbidden):
		adminapi.WriteProblem(w, http.StatusForbidden, "forbidden", "Forbidden")
	case errors.Is(err, adminservice.ErrConflict):
		adminapi.WriteProblem(w, http.StatusConflict, "conflict", "Resource conflict")
	case errors.Is(err, adminservice.ErrInvalidInput):
		adminapi.WriteProblem(w, http.StatusUnprocessableEntity, "invalid_request", "Invalid request")
	default:
		adminapi.WriteProblem(w, http.StatusInternalServerError, "internal_error", "Request failed")
	}
}
