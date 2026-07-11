// Package repos exposes organization-owned repository and collaborator routes.
package repos

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
		return routeset.RouteSet{}, errors.New("native repos: service is required")
	}
	guard, err := adminapi.NewGuard(deps.Authorizer, deps.Authenticate)
	if err != nil {
		return routeset.RouteSet{}, err
	}
	h := handlers{service: deps.Service}
	org := func(action adminservice.Action) func(*http.Request) (adminservice.AuthorizationRequest, error) {
		return func(r *http.Request) (adminservice.AuthorizationRequest, error) {
			orgID, err := adminapi.ParsePathUUID(r, "org")
			return adminservice.AuthorizationRequest{Action: action, OrganizationID: orgID}, err
		}
	}
	repo := func(action adminservice.Action) func(*http.Request) (adminservice.AuthorizationRequest, error) {
		return func(r *http.Request) (adminservice.AuthorizationRequest, error) {
			orgID, err := adminapi.ParsePathUUID(r, "org")
			if err != nil {
				return adminservice.AuthorizationRequest{}, err
			}
			repoID, err := adminapi.ParsePathUUID(r, "repo")
			return adminservice.AuthorizationRequest{Action: action, OrganizationID: orgID, RepositoryID: repoID}, err
		}
	}
	set := routeset.RouteSet{Name: "native-repositories", Routes: []routeset.Route{
		{Name: "native.repos.list", Method: http.MethodGet, Pattern: "/api/v1/orgs/{org}/repos", Handler: guard.Protect(org(adminservice.ActionOrganizationRead), http.HandlerFunc(h.list))},
		{Name: "native.repos.create", Method: http.MethodPost, Pattern: "/api/v1/orgs/{org}/repos", Handler: guard.Protect(org(adminservice.ActionOrganizationAdmin), http.HandlerFunc(h.create))},
		{Name: "native.repos.get", Method: http.MethodGet, Pattern: "/api/v1/orgs/{org}/repos/{repo}", Handler: guard.Protect(repo(adminservice.ActionRepositoryRead), http.HandlerFunc(h.get))},
		{Name: "native.repos.update", Method: http.MethodPatch, Pattern: "/api/v1/orgs/{org}/repos/{repo}", Handler: guard.Protect(repo(adminservice.ActionRepositoryAdmin), http.HandlerFunc(h.update))},
		{Name: "native.repos.archive", Method: http.MethodDelete, Pattern: "/api/v1/orgs/{org}/repos/{repo}", Handler: guard.Protect(repo(adminservice.ActionRepositoryAdmin), http.HandlerFunc(h.archive))},
		{Name: "native.collaborators.list", Method: http.MethodGet, Pattern: "/api/v1/orgs/{org}/repos/{repo}/collaborators", Handler: guard.Protect(repo(adminservice.ActionRepositoryRead), http.HandlerFunc(h.listCollaborators))},
		{Name: "native.collaborators.upsert", Method: http.MethodPost, Pattern: "/api/v1/orgs/{org}/repos/{repo}/collaborators", Handler: guard.Protect(repo(adminservice.ActionRepositoryAdmin), http.HandlerFunc(h.upsertCollaborator))},
		{Name: "native.collaborators.archive", Method: http.MethodDelete, Pattern: "/api/v1/orgs/{org}/repos/{repo}/collaborators/{collaborator}", Handler: guard.Protect(repo(adminservice.ActionRepositoryAdmin), http.HandlerFunc(h.archiveCollaborator))},
	}}
	return set, set.Validate()
}

type handlers struct{ service *adminservice.Service }

func (h handlers) list(w http.ResponseWriter, r *http.Request) {
	orgID, _ := adminapi.ParsePathUUID(r, "org")
	items, err := h.service.ListRepositories(r.Context(), orgID, includeArchived(r))
	if err != nil {
		adminapi.WriteServiceError(w, err)
		return
	}
	adminapi.WriteJSON(w, http.StatusOK, map[string]any{"repositories": items})
}

func (h handlers) create(w http.ResponseWriter, r *http.Request) {
	orgID, _ := adminapi.ParsePathUUID(r, "org")
	var request struct {
		Name               string                    `json:"name"`
		DisplayName        string                    `json:"display_name"`
		Description        string                    `json:"description"`
		Visibility         models.Visibility         `json:"visibility"`
		DefaultBranch      string                    `json:"default_branch"`
		ContributionPolicy models.ContributionPolicy `json:"contribution_policy"`
	}
	if err := adminapi.DecodeJSON(w, r, &request); err != nil {
		adminapi.WriteProblem(w, http.StatusBadRequest, "invalid_json", "Invalid request body")
		return
	}
	repository, err := h.service.CreateRepository(r.Context(), adminapi.Actor(r), orgID, adminservice.CreateRepositoryInput{
		Name: request.Name, DisplayName: request.DisplayName, Description: request.Description,
		Visibility: request.Visibility, DefaultBranch: request.DefaultBranch,
		ContributionPolicy: request.ContributionPolicy,
	})
	if err != nil {
		adminapi.WriteServiceError(w, err)
		return
	}
	adminapi.WriteJSON(w, http.StatusCreated, repository)
}

func (h handlers) get(w http.ResponseWriter, r *http.Request) {
	scope := pathScope(r)
	repository, err := h.service.GetRepository(r.Context(), scope, includeArchived(r))
	if err != nil {
		adminapi.WriteServiceError(w, err)
		return
	}
	adminapi.WriteJSON(w, http.StatusOK, repository)
}

func (h handlers) update(w http.ResponseWriter, r *http.Request) {
	var request struct {
		DisplayName        *string                    `json:"display_name"`
		Description        *string                    `json:"description"`
		Visibility         *models.Visibility         `json:"visibility"`
		DefaultBranch      *string                    `json:"default_branch"`
		ContributionPolicy *models.ContributionPolicy `json:"contribution_policy"`
		ExpectedVersion    int64                      `json:"expected_version"`
	}
	if err := adminapi.DecodeJSON(w, r, &request); err != nil {
		adminapi.WriteProblem(w, http.StatusBadRequest, "invalid_json", "Invalid request body")
		return
	}
	repository, err := h.service.UpdateRepository(r.Context(), adminapi.Actor(r), pathScope(r),
		adminservice.UpdateRepositoryInput{DisplayName: request.DisplayName, Description: request.Description,
			Visibility: request.Visibility, DefaultBranch: request.DefaultBranch,
			ContributionPolicy: request.ContributionPolicy, ExpectedVersion: request.ExpectedVersion})
	if err != nil {
		adminapi.WriteServiceError(w, err)
		return
	}
	adminapi.WriteJSON(w, http.StatusOK, repository)
}

func (h handlers) archive(w http.ResponseWriter, r *http.Request) {
	version, err := expectedVersion(r)
	if err != nil {
		adminapi.WriteProblem(w, http.StatusUnprocessableEntity, "invalid_request", "Expected version is required")
		return
	}
	if err := h.service.ArchiveRepository(r.Context(), adminapi.Actor(r), pathScope(r), version); err != nil {
		adminapi.WriteServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h handlers) listCollaborators(w http.ResponseWriter, r *http.Request) {
	items, err := h.service.ListCollaborators(r.Context(), pathScope(r), includeArchived(r))
	if err != nil {
		adminapi.WriteServiceError(w, err)
		return
	}
	adminapi.WriteJSON(w, http.StatusOK, map[string]any{"collaborators": collaboratorCollection(items)})
}

func collaboratorCollection(items []models.AdminCollaborator) []models.AdminCollaborator {
	if items == nil {
		return []models.AdminCollaborator{}
	}
	return items
}

func (h handlers) upsertCollaborator(w http.ResponseWriter, r *http.Request) {
	var request struct {
		UserID uuid.UUID `json:"user_id"`
		Role   string    `json:"role"`
	}
	if err := adminapi.DecodeJSON(w, r, &request); err != nil {
		adminapi.WriteProblem(w, http.StatusBadRequest, "invalid_json", "Invalid request body")
		return
	}
	collaborator, err := h.service.UpsertCollaborator(r.Context(), adminapi.Actor(r), pathScope(r),
		adminservice.UpsertCollaboratorInput{UserID: request.UserID, Role: request.Role})
	if err != nil {
		adminapi.WriteServiceError(w, err)
		return
	}
	adminapi.WriteJSON(w, http.StatusCreated, collaborator)
}

func (h handlers) archiveCollaborator(w http.ResponseWriter, r *http.Request) {
	id, _ := adminapi.ParsePathUUID(r, "collaborator")
	version, err := expectedVersion(r)
	if err != nil {
		adminapi.WriteProblem(w, http.StatusUnprocessableEntity, "invalid_request", "Expected version is required")
		return
	}
	if err := h.service.ArchiveCollaborator(r.Context(), adminapi.Actor(r), pathScope(r), id, version); err != nil {
		adminapi.WriteServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func pathScope(r *http.Request) models.RepoScope {
	orgID, _ := adminapi.ParsePathUUID(r, "org")
	repoID, _ := adminapi.ParsePathUUID(r, "repo")
	return models.RepoScope{OrgID: orgID, RepoID: repoID}
}

func includeArchived(r *http.Request) bool { return r.URL.Query().Get("include_archived") == "true" }

func expectedVersion(r *http.Request) (int64, error) {
	value, err := strconv.ParseInt(r.URL.Query().Get("version"), 10, 64)
	if err != nil || value <= 0 {
		return 0, errors.New("invalid version")
	}
	return value, nil
}
