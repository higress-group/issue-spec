// Package deliveries exposes authorized webhook delivery inspection and
// manual redelivery without exposing credential material.
package deliveries

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"
	adminapi "github.com/higress-group/issue-spec/internal/server/api/native/admin"
	"github.com/higress-group/issue-spec/internal/server/api/routeset"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/events/delivery"
	"github.com/higress-group/issue-spec/internal/server/models"
)

type Dependencies struct {
	Service      Service
	Authenticate adminapi.Authenticate
}

type Service interface {
	List(context.Context, authz.Subject, models.RepoScope) ([]delivery.Delivery, error)
	Get(context.Context, authz.Subject, models.RepoScope, uuid.UUID) (delivery.Detail, error)
	Redeliver(context.Context, delivery.Actor, authz.Subject, models.RepoScope, uuid.UUID) (delivery.Delivery, error)
}

func NewRouteSet(deps Dependencies) (routeset.RouteSet, error) {
	if deps.Service == nil || deps.Authenticate == nil {
		return routeset.RouteSet{}, errors.New("native deliveries: service and authentication are required")
	}
	h := handlers{service: deps.Service}
	protect := func(next http.HandlerFunc) http.Handler {
		return adminapi.WithRequestID(deps.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			principal, ok := serverauth.PrincipalFromContext(r.Context())
			if !ok || principal.User.ID == uuid.Nil {
				adminapi.WriteProblem(w, http.StatusUnauthorized, "authentication_required", "Authentication required")
				return
			}
			next(w, r)
		})))
	}
	set := routeset.RouteSet{Name: "native-webhook-deliveries", Routes: []routeset.Route{
		{Name: "native.deliveries.list", Method: http.MethodGet, Pattern: "/api/v1/orgs/{org}/repos/{repo}/deliveries", Handler: protect(h.list)},
		{Name: "native.deliveries.get", Method: http.MethodGet, Pattern: "/api/v1/orgs/{org}/repos/{repo}/deliveries/{delivery}", Handler: protect(h.get)},
		{Name: "native.deliveries.redeliver", Method: http.MethodPost, Pattern: "/api/v1/orgs/{org}/repos/{repo}/deliveries/{delivery}/redeliver", Handler: protect(h.redeliver)},
	}}
	return set, set.Validate()
}

type handlers struct{ service Service }

func (h handlers) list(w http.ResponseWriter, r *http.Request) {
	scope, ok := pathScope(w, r)
	if !ok {
		return
	}
	items, err := h.service.List(r.Context(), subject(r), scope)
	if err != nil {
		writeError(w, err)
		return
	}
	adminapi.WriteJSON(w, http.StatusOK, map[string]any{"deliveries": items})
}

func (h handlers) get(w http.ResponseWriter, r *http.Request) {
	scope, id, ok := pathDelivery(w, r)
	if !ok {
		return
	}
	item, err := h.service.Get(r.Context(), subject(r), scope, id)
	if err != nil {
		writeError(w, err)
		return
	}
	adminapi.WriteJSON(w, http.StatusOK, item)
}

func (h handlers) redeliver(w http.ResponseWriter, r *http.Request) {
	scope, id, ok := pathDelivery(w, r)
	if !ok {
		return
	}
	principal, _ := serverauth.PrincipalFromContext(r.Context())
	item, err := h.service.Redeliver(r.Context(), delivery.ActorFromPrincipal(principal, adminapi.RequestID(r)), subject(r), scope, id)
	if err != nil {
		writeError(w, err)
		return
	}
	adminapi.WriteJSON(w, http.StatusAccepted, item)
}

func pathScope(w http.ResponseWriter, r *http.Request) (models.RepoScope, bool) {
	orgID, err := uuid.Parse(r.PathValue("org"))
	if err != nil {
		adminapi.WriteProblem(w, http.StatusUnprocessableEntity, "invalid_request", "Invalid organization id")
		return models.RepoScope{}, false
	}
	repoID, err := uuid.Parse(r.PathValue("repo"))
	if err != nil {
		adminapi.WriteProblem(w, http.StatusUnprocessableEntity, "invalid_request", "Invalid repository id")
		return models.RepoScope{}, false
	}
	return models.RepoScope{OrgID: orgID, RepoID: repoID}, true
}

func pathDelivery(w http.ResponseWriter, r *http.Request) (models.RepoScope, uuid.UUID, bool) {
	scope, ok := pathScope(w, r)
	if !ok {
		return models.RepoScope{}, uuid.Nil, false
	}
	id, err := uuid.Parse(r.PathValue("delivery"))
	if err != nil {
		adminapi.WriteProblem(w, http.StatusUnprocessableEntity, "invalid_request", "Invalid delivery id")
		return models.RepoScope{}, uuid.Nil, false
	}
	return scope, id, true
}

func subject(r *http.Request) authz.Subject {
	principal, _ := serverauth.PrincipalFromContext(r.Context())
	return authz.Authenticated(principal)
}

func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, delivery.ErrInvalid):
		adminapi.WriteProblem(w, http.StatusUnprocessableEntity, "invalid_request", "Invalid delivery request")
	case errors.Is(err, delivery.ErrNotFound):
		adminapi.WriteProblem(w, http.StatusNotFound, "not_found", "Resource not found")
	case errors.Is(err, delivery.ErrForbidden):
		adminapi.WriteProblem(w, http.StatusForbidden, "forbidden", "Forbidden")
	default:
		adminapi.WriteProblem(w, http.StatusInternalServerError, "internal_error", "Request failed")
	}
}
