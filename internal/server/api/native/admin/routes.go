package admin

import (
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	adminservice "github.com/higress-group/issue-spec/internal/server/admin"
	"github.com/higress-group/issue-spec/internal/server/api/routeset"
)

type Dependencies struct {
	Service      *adminservice.Service
	Authorizer   adminservice.Authorizer
	Authenticate Authenticate
}

func NewRouteSet(deps Dependencies) (routeset.RouteSet, error) {
	if deps.Service == nil {
		return routeset.RouteSet{}, errors.New("native admin: service is required")
	}
	guard, err := NewGuard(deps.Authorizer, deps.Authenticate)
	if err != nil {
		return routeset.RouteSet{}, err
	}
	h := handlers{service: deps.Service}
	org := func(action adminservice.Action) func(*http.Request) (adminservice.AuthorizationRequest, error) {
		return func(r *http.Request) (adminservice.AuthorizationRequest, error) {
			orgID, err := ParsePathUUID(r, "org")
			return adminservice.AuthorizationRequest{Action: action, OrganizationID: orgID}, err
		}
	}
	target := func(r *http.Request) (adminservice.AuthorizationRequest, error) {
		orgID, err := ParsePathUUID(r, "org")
		if err != nil {
			return adminservice.AuthorizationRequest{}, err
		}
		userID, err := ParsePathUUID(r, "user")
		return adminservice.AuthorizationRequest{Action: adminservice.ActionCredentialAdmin,
			OrganizationID: orgID, TargetUserID: userID}, err
	}
	set := routeset.RouteSet{Name: "native-administration", Routes: []routeset.Route{
		{Name: "native.service_accounts.list", Method: http.MethodGet, Pattern: "/api/v1/orgs/{org}/service-accounts", Handler: guard.Protect(org(adminservice.ActionOrganizationAdmin), http.HandlerFunc(h.listServiceAccounts))},
		{Name: "native.service_accounts.create", Method: http.MethodPost, Pattern: "/api/v1/orgs/{org}/service-accounts", Handler: guard.Protect(org(adminservice.ActionOrganizationAdmin), http.HandlerFunc(h.createServiceAccount))},
		{Name: "native.service_accounts.disable", Method: http.MethodDelete, Pattern: "/api/v1/orgs/{org}/service-accounts/{account}", Handler: guard.Protect(org(adminservice.ActionOrganizationAdmin), http.HandlerFunc(h.disableServiceAccount))},
		{Name: "native.managed_pats.list", Method: http.MethodGet, Pattern: "/api/v1/orgs/{org}/users/{user}/pats", Handler: guard.Protect(target, http.HandlerFunc(h.listPATs))},
		{Name: "native.managed_pats.create", Method: http.MethodPost, Pattern: "/api/v1/orgs/{org}/users/{user}/pats", Handler: guard.Protect(target, http.HandlerFunc(h.createPAT))},
		{Name: "native.managed_pats.rotate", Method: http.MethodPost, Pattern: "/api/v1/orgs/{org}/pats/{pat}/rotate", Handler: guard.Protect(org(adminservice.ActionCredentialAdmin), http.HandlerFunc(h.rotatePAT))},
		{Name: "native.managed_pats.revoke", Method: http.MethodDelete, Pattern: "/api/v1/orgs/{org}/pats/{pat}", Handler: guard.Protect(org(adminservice.ActionCredentialAdmin), http.HandlerFunc(h.revokePAT))},
	}}
	return set, set.Validate()
}

type handlers struct{ service *adminservice.Service }

func (h handlers) listServiceAccounts(w http.ResponseWriter, r *http.Request) {
	orgID, _ := ParsePathUUID(r, "org")
	items, err := h.service.ListServiceAccounts(r.Context(), orgID, r.URL.Query().Get("include_disabled") == "true")
	if err != nil {
		WriteServiceError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"service_accounts": items})
}

func (h handlers) createServiceAccount(w http.ResponseWriter, r *http.Request) {
	orgID, _ := ParsePathUUID(r, "org")
	var request struct {
		Name string `json:"name"`
	}
	if err := DecodeJSON(w, r, &request); err != nil {
		WriteProblem(w, http.StatusBadRequest, "invalid_json", "Invalid request body")
		return
	}
	account, err := h.service.CreateServiceAccount(r.Context(), Actor(r), orgID,
		adminservice.CreateServiceAccountInput{Name: request.Name})
	if err != nil {
		WriteServiceError(w, err)
		return
	}
	WriteJSON(w, http.StatusCreated, account)
}

func (h handlers) disableServiceAccount(w http.ResponseWriter, r *http.Request) {
	orgID, _ := ParsePathUUID(r, "org")
	accountID, _ := ParsePathUUID(r, "account")
	if err := h.service.DisableServiceAccount(r.Context(), Actor(r), orgID, accountID); err != nil {
		WriteServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h handlers) listPATs(w http.ResponseWriter, r *http.Request) {
	orgID, _ := ParsePathUUID(r, "org")
	userID, _ := ParsePathUUID(r, "user")
	items, err := h.service.ListManagedPATs(r.Context(), orgID, userID)
	if err != nil {
		WriteServiceError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"tokens": items})
}

func (h handlers) createPAT(w http.ResponseWriter, r *http.Request) {
	orgID, _ := ParsePathUUID(r, "org")
	userID, _ := ParsePathUUID(r, "user")
	var request struct {
		Name          string      `json:"name"`
		Scopes        []string    `json:"scopes"`
		RepositoryIDs []uuid.UUID `json:"repository_ids"`
		ExpiresAt     *time.Time  `json:"expires_at"`
	}
	if err := DecodeJSON(w, r, &request); err != nil {
		WriteProblem(w, http.StatusBadRequest, "invalid_json", "Invalid request body")
		return
	}
	created, err := h.service.CreateManagedPAT(r.Context(), Actor(r), orgID, adminservice.CreateManagedPATInput{
		TargetUserID: userID, Name: request.Name, Scopes: request.Scopes,
		RepositoryIDs: request.RepositoryIDs, ExpiresAt: request.ExpiresAt,
	})
	if err != nil {
		WriteServiceError(w, err)
		return
	}
	WriteJSON(w, http.StatusCreated, created)
}

func (h handlers) rotatePAT(w http.ResponseWriter, r *http.Request) {
	orgID, _ := ParsePathUUID(r, "org")
	tokenID, _ := ParsePathUUID(r, "pat")
	created, err := h.service.RotateManagedPAT(r.Context(), Actor(r), orgID, tokenID)
	if err != nil {
		WriteServiceError(w, err)
		return
	}
	WriteJSON(w, http.StatusCreated, created)
}

func (h handlers) revokePAT(w http.ResponseWriter, r *http.Request) {
	orgID, _ := ParsePathUUID(r, "org")
	tokenID, _ := ParsePathUUID(r, "pat")
	if err := h.service.RevokeManagedPAT(r.Context(), Actor(r), orgID, tokenID); err != nil {
		WriteServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
