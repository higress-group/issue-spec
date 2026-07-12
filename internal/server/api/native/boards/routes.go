// Package boardsapi exposes permission-filtered change board read models.
package boardsapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	adminservice "github.com/higress-group/issue-spec/internal/server/admin"
	"github.com/higress-group/issue-spec/internal/server/api/github/pagination"
	adminapi "github.com/higress-group/issue-spec/internal/server/api/native/admin"
	"github.com/higress-group/issue-spec/internal/server/api/routeset"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/changes"
	"github.com/higress-group/issue-spec/internal/server/models"
)

type Service interface {
	RepositoryBoard(context.Context, authz.Subject, models.RepoScope, changes.ListOptions) (changes.BoardPage, error)
	OrganizationBoard(context.Context, authz.Subject, models.OrgScope, changes.ListOptions) (changes.BoardPage, error)
	Change(context.Context, authz.Subject, models.RepoScope, string) (changes.ChangeCard, string, time.Time, error)
	IssueRelationships(context.Context, authz.Subject, string, string, int64) (changes.IssueRelationships, error)
}

type Dependencies struct {
	Service              Service
	Authenticate         adminapi.Authenticate
	AuthenticateOptional adminapi.Authenticate
}

func NewRouteSet(deps Dependencies) (routeset.RouteSet, error) {
	if deps.Service == nil || deps.Authenticate == nil || deps.AuthenticateOptional == nil {
		return routeset.RouteSet{}, errors.New("native boards: service and authentication are required")
	}
	h := handlers{service: deps.Service}
	protect := func(authenticate adminapi.Authenticate, handler http.HandlerFunc) http.Handler {
		protected := authenticate(handler)
		return adminapi.WithRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			protected.ServeHTTP(w, r)
		}))
	}
	set := routeset.RouteSet{Name: "native-change-boards", Routes: []routeset.Route{
		{Name: "native.boards.organization.list", Method: http.MethodGet, Pattern: "/api/v1/orgs/{org}/changes", Handler: protect(deps.Authenticate, h.organizationList)},
		{Name: "native.boards.organization.query", Method: http.MethodPost, Pattern: "/api/v1/orgs/{org}/changes/query", Handler: protect(deps.Authenticate, h.organizationQuery)},
		{Name: "native.boards.repository.list", Method: http.MethodGet, Pattern: "/api/v1/orgs/{org}/repos/{repo}/changes", Handler: protect(deps.AuthenticateOptional, h.repositoryList)},
		{Name: "native.boards.repository.query", Method: http.MethodPost, Pattern: "/api/v1/orgs/{org}/repos/{repo}/changes/query", Handler: protect(deps.Authenticate, h.repositoryQuery)},
		{Name: "native.boards.repository.detail", Method: http.MethodGet, Pattern: "/api/v1/orgs/{org}/repos/{repo}/changes/{change...}", Handler: protect(deps.AuthenticateOptional, h.detail)},
		{Name: "native.issues.relationships", Method: http.MethodGet, Pattern: "/api/v1/context/repos/{owner}/{repo}/issues/{number}/relationships", Handler: protect(deps.AuthenticateOptional, h.issueRelationships)},
	}}
	return set, set.Validate()
}

type handlers struct{ service Service }

func (h handlers) repositoryList(w http.ResponseWriter, r *http.Request) {
	subject, scope, ok := repositoryScope(w, r, false)
	if !ok {
		return
	}
	options, ok := queryOptions(w, r)
	if !ok {
		return
	}
	page, err := h.service.RepositoryBoard(r.Context(), subject, scope, options)
	if err != nil {
		writeError(w, err)
		return
	}
	writeBoard(w, r, page)
}

func (h handlers) repositoryQuery(w http.ResponseWriter, r *http.Request) {
	subject, scope, ok := repositoryScope(w, r, true)
	if !ok {
		return
	}
	var options changes.ListOptions
	if err := adminapi.DecodeJSON(w, r, &options); err != nil {
		problem(w, http.StatusBadRequest, "invalid_json", "Invalid request body")
		return
	}
	page, err := h.service.RepositoryBoard(r.Context(), subject, scope, options)
	if err != nil {
		writeError(w, err)
		return
	}
	writeBoard(w, r, page)
}

func (h handlers) organizationList(w http.ResponseWriter, r *http.Request) {
	principal, scope, ok := organizationScope(w, r)
	if !ok {
		return
	}
	options, ok := queryOptions(w, r)
	if !ok {
		return
	}
	page, err := h.service.OrganizationBoard(r.Context(), authz.Authenticated(principal), scope, options)
	if err != nil {
		writeError(w, err)
		return
	}
	writeBoard(w, r, page)
}

func (h handlers) organizationQuery(w http.ResponseWriter, r *http.Request) {
	principal, scope, ok := organizationScope(w, r)
	if !ok {
		return
	}
	var options changes.ListOptions
	if err := adminapi.DecodeJSON(w, r, &options); err != nil {
		problem(w, http.StatusBadRequest, "invalid_json", "Invalid request body")
		return
	}
	page, err := h.service.OrganizationBoard(r.Context(), authz.Authenticated(principal), scope, options)
	if err != nil {
		writeError(w, err)
		return
	}
	writeBoard(w, r, page)
}

func (h handlers) detail(w http.ResponseWriter, r *http.Request) {
	subject, scope, ok := repositoryScope(w, r, false)
	if !ok {
		return
	}
	if len(r.URL.Query()) != 0 {
		problem(w, http.StatusUnprocessableEntity, "invalid_request", "Detail query parameters are not supported")
		return
	}
	card, validator, modified, err := h.service.Change(r.Context(), subject, scope, r.PathValue("change"))
	if err != nil {
		writeError(w, err)
		return
	}
	if notModified(w, r, validator, modified) {
		return
	}
	adminapi.WriteJSON(w, http.StatusOK, card)
}

func (h handlers) issueRelationships(w http.ResponseWriter, r *http.Request) {
	if len(r.URL.Query()) != 0 {
		problem(w, http.StatusUnprocessableEntity, "invalid_request", "Relationship query parameters are not supported")
		return
	}
	number, err := strconv.ParseInt(r.PathValue("number"), 10, 64)
	if err != nil || number <= 0 {
		problem(w, http.StatusUnprocessableEntity, "invalid_request", "Invalid issue number")
		return
	}
	subject := authz.Anonymous()
	if presented, ok := serverauth.PrincipalFromContext(r.Context()); ok && presented.User.ID != uuid.Nil {
		subject = authz.Authenticated(presented)
	}
	projection, err := h.service.IssueRelationships(r.Context(), subject, r.PathValue("owner"), r.PathValue("repo"), number)
	if err != nil {
		writeError(w, err)
		return
	}
	if notModified(w, r, projection.Validator, projection.LastModified) {
		return
	}
	adminapi.WriteJSON(w, http.StatusOK, projection)
}

func repositoryScope(w http.ResponseWriter, r *http.Request, requireAuthentication bool) (authz.Subject, models.RepoScope, bool) {
	subject := authz.Anonymous()
	if presented, ok := serverauth.PrincipalFromContext(r.Context()); ok && presented.User.ID != uuid.Nil {
		subject = authz.Authenticated(presented)
	} else if requireAuthentication {
		problem(w, http.StatusUnauthorized, "authentication_required", "Authentication required")
		return authz.Subject{}, models.RepoScope{}, false
	}
	orgID, orgErr := uuid.Parse(r.PathValue("org"))
	repoID, repoErr := uuid.Parse(r.PathValue("repo"))
	if orgErr != nil || repoErr != nil {
		problem(w, http.StatusUnprocessableEntity, "invalid_request", "Invalid repository")
		return authz.Subject{}, models.RepoScope{}, false
	}
	return subject, models.RepoScope{OrgID: orgID, RepoID: repoID}, true
}

func organizationScope(w http.ResponseWriter, r *http.Request) (serverauth.Principal, models.OrgScope, bool) {
	principal, ok := principal(w, r)
	if !ok {
		return serverauth.Principal{}, models.OrgScope{}, false
	}
	orgID, err := uuid.Parse(r.PathValue("org"))
	if err != nil {
		problem(w, http.StatusUnprocessableEntity, "invalid_request", "Invalid organization")
		return serverauth.Principal{}, models.OrgScope{}, false
	}
	return principal, models.OrgScope{OrgID: orgID}, true
}

func principal(w http.ResponseWriter, r *http.Request) (serverauth.Principal, bool) {
	principal, ok := serverauth.PrincipalFromContext(r.Context())
	if !ok || principal.User.ID == uuid.Nil {
		problem(w, http.StatusUnauthorized, "authentication_required", "Authentication required")
		return serverauth.Principal{}, false
	}
	return principal, true
}

func queryOptions(w http.ResponseWriter, r *http.Request) (changes.ListOptions, bool) {
	allowed := map[string]bool{"stage": true, "lifecycle": true, "anomaly": true, "page": true, "per_page": true}
	for key, values := range r.URL.Query() {
		if !allowed[key] || len(values) != 1 {
			problem(w, http.StatusUnprocessableEntity, "invalid_request", "Invalid board query")
			return changes.ListOptions{}, false
		}
	}
	page, err := (pagination.Parser{DefaultPerPage: 20, MaximumPerPage: 100}).Parse(r.URL.Query())
	if err != nil {
		problem(w, http.StatusUnprocessableEntity, "invalid_request", "Invalid pagination")
		return changes.ListOptions{}, false
	}
	options := changes.ListOptions{Stage: changes.Stage(strings.TrimSpace(r.URL.Query().Get("stage"))),
		Lifecycle: changes.Lifecycle(strings.TrimSpace(r.URL.Query().Get("lifecycle"))), Anomaly: strings.TrimSpace(r.URL.Query().Get("anomaly"))}
	options.Page, options.PerPage = page.Page, page.PerPage
	return options, true
}

func writeBoard(w http.ResponseWriter, r *http.Request, page changes.BoardPage) {
	if notModified(w, r, page.Validator, page.LastModified) {
		return
	}
	adminapi.WriteJSON(w, http.StatusOK, page)
}

func notModified(w http.ResponseWriter, r *http.Request, validator string, modified time.Time) bool {
	w.Header().Set("Cache-Control", "no-store")
	pagination.SetConditionalHeaders(w.Header(), validator, modified)
	if pagination.NotModified(r, validator, modified) {
		w.WriteHeader(http.StatusNotModified)
		return true
	}
	return false
}

func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, adminservice.ErrNotFound):
		problem(w, http.StatusNotFound, "not_found", "Not found")
	case errors.Is(err, adminservice.ErrForbidden):
		problem(w, http.StatusForbidden, "forbidden", "Forbidden")
	case errors.Is(err, adminservice.ErrInvalidInput):
		problem(w, http.StatusUnprocessableEntity, "invalid_request", "Invalid request")
	default:
		problem(w, http.StatusInternalServerError, "internal_error", "Request failed")
	}
}

func problem(w http.ResponseWriter, status int, code, title string) {
	w.Header().Set("Cache-Control", "no-store")
	adminapi.WriteProblem(w, status, code, title)
}
