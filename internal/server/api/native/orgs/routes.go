// Package orgs exposes organization and membership lifecycle routes.
package orgs

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	adminservice "github.com/higress-group/issue-spec/internal/server/admin"
	adminapi "github.com/higress-group/issue-spec/internal/server/api/native/admin"
	"github.com/higress-group/issue-spec/internal/server/api/routeset"
	"github.com/higress-group/issue-spec/internal/server/models"
)

type Dependencies struct {
	Service      *adminservice.Service
	Authorizer   adminservice.Authorizer
	Authenticate adminapi.Authenticate
}

func NewRouteSet(deps Dependencies) (routeset.RouteSet, error) {
	if deps.Service == nil {
		return routeset.RouteSet{}, errors.New("native orgs: service is required")
	}
	guard, err := adminapi.NewGuard(deps.Authorizer, deps.Authenticate)
	if err != nil {
		return routeset.RouteSet{}, err
	}
	h := handlers{service: deps.Service}
	site := func(action adminservice.Action) func(*http.Request) (adminservice.AuthorizationRequest, error) {
		return func(*http.Request) (adminservice.AuthorizationRequest, error) {
			return adminservice.AuthorizationRequest{Action: action}, nil
		}
	}
	org := func(action adminservice.Action) func(*http.Request) (adminservice.AuthorizationRequest, error) {
		return func(r *http.Request) (adminservice.AuthorizationRequest, error) {
			id, err := adminapi.ParsePathUUID(r, "org")
			return adminservice.AuthorizationRequest{Action: action, OrganizationID: id}, err
		}
	}
	set := routeset.RouteSet{Name: "native-organizations", Routes: []routeset.Route{
		{Name: "native.orgs.list", Method: http.MethodGet, Pattern: "/api/v1/orgs", Handler: guard.Protect(site(adminservice.ActionSiteAdmin), http.HandlerFunc(h.list))},
		{Name: "native.orgs.create", Method: http.MethodPost, Pattern: "/api/v1/orgs", Handler: guard.Protect(site(adminservice.ActionSiteAdmin), http.HandlerFunc(h.create))},
		{Name: "native.orgs.get", Method: http.MethodGet, Pattern: "/api/v1/orgs/{org}", Handler: guard.Protect(org(adminservice.ActionOrganizationRead), http.HandlerFunc(h.get))},
		{Name: "native.orgs.update", Method: http.MethodPatch, Pattern: "/api/v1/orgs/{org}", Handler: guard.Protect(org(adminservice.ActionOrganizationAdmin), http.HandlerFunc(h.update))},
		{Name: "native.orgs.archive", Method: http.MethodDelete, Pattern: "/api/v1/orgs/{org}", Handler: guard.Protect(org(adminservice.ActionOrganizationAdmin), http.HandlerFunc(h.archive))},
		{Name: "native.memberships.list", Method: http.MethodGet, Pattern: "/api/v1/orgs/{org}/memberships", Handler: guard.Protect(org(adminservice.ActionOrganizationRead), http.HandlerFunc(h.listMemberships))},
		{Name: "native.memberships.invite", Method: http.MethodPost, Pattern: "/api/v1/orgs/{org}/memberships", Handler: guard.Protect(org(adminservice.ActionOrganizationAdmin), http.HandlerFunc(h.inviteMembership))},
		{Name: "native.memberships.update", Method: http.MethodPatch, Pattern: "/api/v1/orgs/{org}/memberships/{membership}", Handler: guard.Protect(org(adminservice.ActionOrganizationAdmin), http.HandlerFunc(h.updateMembership))},
		{Name: "native.memberships.archive", Method: http.MethodDelete, Pattern: "/api/v1/orgs/{org}/memberships/{membership}", Handler: guard.Protect(org(adminservice.ActionOrganizationAdmin), http.HandlerFunc(h.archiveMembership))},
	}}
	return set, set.Validate()
}

type handlers struct{ service *adminservice.Service }

func (h handlers) list(w http.ResponseWriter, r *http.Request) {
	items, err := h.service.ListOrganizations(r.Context(), includeArchived(r))
	if err != nil {
		adminapi.WriteServiceError(w, err)
		return
	}
	adminapi.WriteJSON(w, http.StatusOK, map[string]any{"organizations": items})
}

func (h handlers) create(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Name           string                `json:"name"`
		DisplayName    string                `json:"display_name"`
		Description    string                `json:"description"`
		BasePermission models.BasePermission `json:"base_permission"`
	}
	if err := adminapi.DecodeJSON(w, r, &request); err != nil {
		adminapi.WriteProblem(w, http.StatusBadRequest, "invalid_json", "Invalid request body")
		return
	}
	organization, err := h.service.CreateOrganization(r.Context(), adminapi.Actor(r), adminservice.CreateOrganizationInput{
		Name: request.Name, DisplayName: request.DisplayName, Description: request.Description,
		BasePermission: request.BasePermission,
	})
	if err != nil {
		adminapi.WriteServiceError(w, err)
		return
	}
	adminapi.WriteJSON(w, http.StatusCreated, organization)
}

func (h handlers) get(w http.ResponseWriter, r *http.Request) {
	id, _ := adminapi.ParsePathUUID(r, "org")
	organization, err := h.service.GetOrganization(r.Context(), id, includeArchived(r))
	if err != nil {
		adminapi.WriteServiceError(w, err)
		return
	}
	adminapi.WriteJSON(w, http.StatusOK, organization)
}

func (h handlers) update(w http.ResponseWriter, r *http.Request) {
	id, _ := adminapi.ParsePathUUID(r, "org")
	var request struct {
		DisplayName     *string                `json:"display_name"`
		Description     *string                `json:"description"`
		BasePermission  *models.BasePermission `json:"base_permission"`
		ExpectedVersion int64                  `json:"expected_version"`
	}
	if err := adminapi.DecodeJSON(w, r, &request); err != nil {
		adminapi.WriteProblem(w, http.StatusBadRequest, "invalid_json", "Invalid request body")
		return
	}
	organization, err := h.service.UpdateOrganization(r.Context(), adminapi.Actor(r), id, adminservice.UpdateOrganizationInput{
		DisplayName: request.DisplayName, Description: request.Description,
		BasePermission: request.BasePermission, ExpectedVersion: request.ExpectedVersion,
	})
	if err != nil {
		adminapi.WriteServiceError(w, err)
		return
	}
	adminapi.WriteJSON(w, http.StatusOK, organization)
}

func (h handlers) archive(w http.ResponseWriter, r *http.Request) {
	id, _ := adminapi.ParsePathUUID(r, "org")
	version, err := expectedVersion(r)
	if err != nil {
		adminapi.WriteProblem(w, http.StatusUnprocessableEntity, "invalid_request", "Expected version is required")
		return
	}
	if err := h.service.ArchiveOrganization(r.Context(), adminapi.Actor(r), id, version); err != nil {
		adminapi.WriteServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h handlers) listMemberships(w http.ResponseWriter, r *http.Request) {
	orgID, _ := adminapi.ParsePathUUID(r, "org")
	items, err := h.service.ListMemberships(r.Context(), orgID, includeArchived(r))
	if err != nil {
		adminapi.WriteServiceError(w, err)
		return
	}
	adminapi.WriteJSON(w, http.StatusOK, map[string]any{"memberships": items})
}

func (h handlers) inviteMembership(w http.ResponseWriter, r *http.Request) {
	orgID, _ := adminapi.ParsePathUUID(r, "org")
	var request struct {
		UserID uuid.UUID `json:"user_id"`
		Role   string    `json:"role"`
	}
	if err := adminapi.DecodeJSON(w, r, &request); err != nil {
		adminapi.WriteProblem(w, http.StatusBadRequest, "invalid_json", "Invalid request body")
		return
	}
	membership, err := h.service.InviteMembership(r.Context(), adminapi.Actor(r), orgID,
		adminservice.InviteMembershipInput{UserID: request.UserID, Role: request.Role})
	if err != nil {
		adminapi.WriteServiceError(w, err)
		return
	}
	adminapi.WriteJSON(w, http.StatusCreated, membership)
}

func (h handlers) updateMembership(w http.ResponseWriter, r *http.Request) {
	orgID, _ := adminapi.ParsePathUUID(r, "org")
	membershipID, _ := adminapi.ParsePathUUID(r, "membership")
	var request struct {
		Role            string                 `json:"role"`
		State           models.MembershipState `json:"state"`
		ExpectedVersion int64                  `json:"expected_version"`
	}
	if err := adminapi.DecodeJSON(w, r, &request); err != nil {
		adminapi.WriteProblem(w, http.StatusBadRequest, "invalid_json", "Invalid request body")
		return
	}
	membership, err := h.service.UpdateMembership(r.Context(), adminapi.Actor(r), orgID, membershipID,
		adminservice.UpdateMembershipInput{Role: request.Role, State: request.State, ExpectedVersion: request.ExpectedVersion})
	if err != nil {
		adminapi.WriteServiceError(w, err)
		return
	}
	adminapi.WriteJSON(w, http.StatusOK, membership)
}

func (h handlers) archiveMembership(w http.ResponseWriter, r *http.Request) {
	orgID, _ := adminapi.ParsePathUUID(r, "org")
	membershipID, _ := adminapi.ParsePathUUID(r, "membership")
	version, err := expectedVersion(r)
	if err != nil {
		adminapi.WriteProblem(w, http.StatusUnprocessableEntity, "invalid_request", "Expected version is required")
		return
	}
	if err := h.service.ArchiveMembership(r.Context(), adminapi.Actor(r), orgID, membershipID, version); err != nil {
		adminapi.WriteServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func includeArchived(r *http.Request) bool { return r.URL.Query().Get("include_archived") == "true" }

func expectedVersion(r *http.Request) (int64, error) {
	value, err := strconv.ParseInt(r.URL.Query().Get("version"), 10, 64)
	if err != nil || value <= 0 {
		return 0, errors.New("invalid version")
	}
	return value, nil
}
