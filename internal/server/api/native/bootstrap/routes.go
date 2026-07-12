// Package bootstrap exposes the one-time bootstrap status and claim RouteSet.
package bootstrap

import (
	"net/http"

	"github.com/google/uuid"
	adminservice "github.com/higress-group/issue-spec/internal/server/admin"
	adminapi "github.com/higress-group/issue-spec/internal/server/api/native/admin"
	"github.com/higress-group/issue-spec/internal/server/api/routeset"
)

type Dependencies struct{ Service *adminservice.Service }

func NewRouteSet(deps Dependencies) (routeset.RouteSet, error) {
	if deps.Service == nil {
		return routeset.RouteSet{}, adminservice.ErrInvalidInput
	}
	h := handlers{service: deps.Service}
	set := routeset.RouteSet{Name: "native-bootstrap", Routes: []routeset.Route{
		{Name: "native.bootstrap.status", Method: http.MethodGet, Pattern: "/api/v1/bootstrap", Handler: adminapi.WithRequestID(http.HandlerFunc(h.status))},
		{Name: "native.bootstrap.claim", Method: http.MethodPost, Pattern: "/api/v1/bootstrap/claim", Handler: adminapi.WithRequestID(http.HandlerFunc(h.claim))},
	}}
	return set, set.Validate()
}

type handlers struct{ service *adminservice.Service }

func (h handlers) status(w http.ResponseWriter, r *http.Request) {
	status, err := h.service.BootstrapStatus(r.Context())
	if err != nil {
		adminapi.WriteServiceError(w, err)
		return
	}
	adminapi.WriteJSON(w, http.StatusOK, status)
}

func (h handlers) claim(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Secret      string  `json:"secret"`
		UserID      *string `json:"user_id"`
		Login       string  `json:"login"`
		DisplayName string  `json:"display_name"`
		Email       *string `json:"email"`
	}
	if err := adminapi.DecodeJSON(w, r, &request); err != nil {
		adminapi.WriteProblem(w, http.StatusBadRequest, "invalid_json", "Invalid request body")
		return
	}
	input := adminservice.BootstrapClaimInput{Secret: request.Secret, Login: request.Login,
		DisplayName: request.DisplayName, Email: request.Email, RequestID: adminapi.RequestID(r)}
	if request.UserID != nil {
		id, err := uuid.Parse(*request.UserID)
		if err != nil {
			adminapi.WriteProblem(w, http.StatusUnprocessableEntity, "invalid_request", "Invalid request")
			return
		}
		input.UserID = &id
	}
	result, err := h.service.ClaimBootstrap(r.Context(), input)
	if err != nil {
		adminapi.WriteServiceError(w, err)
		return
	}
	adminapi.WriteJSON(w, http.StatusCreated, result)
}
