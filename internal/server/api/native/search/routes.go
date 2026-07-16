// Package searchapi exposes permission-filtered issue and comment search.
package searchapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/uuid"
	adminservice "github.com/higress-group/issue-spec/internal/server/admin"
	adminapi "github.com/higress-group/issue-spec/internal/server/api/native/admin"
	"github.com/higress-group/issue-spec/internal/server/api/routeset"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/models"
	searchservice "github.com/higress-group/issue-spec/internal/server/search"
)

type Service interface {
	Repository(context.Context, authz.Subject, models.RepoScope, searchservice.Options) (searchservice.Page, error)
	Organization(context.Context, authz.Subject, models.OrgScope, searchservice.Options) (searchservice.Page, error)
	ContextRepository(context.Context, authz.Subject, string, string, searchservice.Options) (searchservice.Page, error)
}

type Dependencies struct {
	Service              Service
	Authenticate         adminapi.Authenticate
	AuthenticateOptional adminapi.Authenticate
	WebOrigin            string
}

func NewRouteSet(deps Dependencies) (routeset.RouteSet, error) {
	if deps.Service == nil || deps.Authenticate == nil || deps.AuthenticateOptional == nil || strings.TrimSpace(deps.WebOrigin) == "" {
		return routeset.RouteSet{}, errors.New("native search: service, authentication, and web origin are required")
	}
	h := handlers{service: deps.Service, webOrigin: strings.TrimRight(deps.WebOrigin, "/")}
	protect := func(authenticate adminapi.Authenticate, handler http.HandlerFunc) http.Handler {
		protected := authenticate(handler)
		return adminapi.WithRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			protected.ServeHTTP(w, r)
		}))
	}
	set := routeset.RouteSet{Name: "native-search", Routes: []routeset.Route{
		{Name: "native.search.organization.issues", Method: http.MethodGet, Pattern: "/api/v1/orgs/{org}/search/issues", Handler: protect(deps.Authenticate, h.organization)},
		{Name: "native.search.repository.issues", Method: http.MethodGet, Pattern: "/api/v1/orgs/{org}/repos/{repo}/search/issues", Handler: protect(deps.AuthenticateOptional, h.repository)},
		{Name: "native.search.context.issues", Method: http.MethodGet, Pattern: "/api/v1/context/repos/{owner}/{repo}/search/issues", Handler: protect(deps.AuthenticateOptional, h.contextRepository)},
	}}
	return set, set.Validate()
}

type handlers struct {
	service   Service
	webOrigin string
}

func (h handlers) repository(w http.ResponseWriter, r *http.Request) {
	orgID, orgErr := uuid.Parse(r.PathValue("org"))
	repoID, repoErr := uuid.Parse(r.PathValue("repo"))
	if orgErr != nil || repoErr != nil {
		problem(w, http.StatusUnprocessableEntity, "invalid_request", "Invalid repository")
		return
	}
	options, ok := parseOptions(w, r)
	if !ok {
		return
	}
	page, err := h.service.Repository(r.Context(), subject(r), models.RepoScope{OrgID: orgID, RepoID: repoID}, options)
	h.writePage(w, page, err)
}

func (h handlers) organization(w http.ResponseWriter, r *http.Request) {
	orgID, err := uuid.Parse(r.PathValue("org"))
	if err != nil {
		problem(w, http.StatusUnprocessableEntity, "invalid_request", "Invalid organization")
		return
	}
	options, ok := parseOptions(w, r)
	if !ok {
		return
	}
	page, searchErr := h.service.Organization(r.Context(), subject(r), models.OrgScope{OrgID: orgID}, options)
	h.writePage(w, page, searchErr)
}

func (h handlers) contextRepository(w http.ResponseWriter, r *http.Request) {
	options, ok := parseOptions(w, r)
	if !ok {
		return
	}
	page, err := h.service.ContextRepository(r.Context(), subject(r), r.PathValue("owner"), r.PathValue("repo"), options)
	h.writePage(w, page, err)
}

func subject(r *http.Request) authz.Subject {
	if principal, ok := serverauth.PrincipalFromContext(r.Context()); ok && principal.User.ID != uuid.Nil {
		return authz.Authenticated(principal)
	}
	return authz.Anonymous()
}

func parseOptions(w http.ResponseWriter, r *http.Request) (searchservice.Options, bool) {
	allowed := map[string]bool{"q": true, "state": true, "source": true, "stage": true, "page": true, "per_page": true}
	for key, values := range r.URL.Query() {
		if !allowed[key] || len(values) != 1 {
			problem(w, http.StatusUnprocessableEntity, "invalid_request", "Invalid search query")
			return searchservice.Options{}, false
		}
	}
	page, perPage := 0, 0
	var err error
	if raw := r.URL.Query().Get("page"); raw != "" {
		page, err = strconv.Atoi(raw)
		if err != nil {
			problem(w, http.StatusUnprocessableEntity, "invalid_request", "Invalid pagination")
			return searchservice.Options{}, false
		}
	}
	if raw := r.URL.Query().Get("per_page"); raw != "" {
		perPage, err = strconv.Atoi(raw)
		if err != nil {
			problem(w, http.StatusUnprocessableEntity, "invalid_request", "Invalid pagination")
			return searchservice.Options{}, false
		}
	}
	options, err := (searchservice.Options{Query: r.URL.Query().Get("q"), State: r.URL.Query().Get("state"),
		Source: searchservice.Source(strings.ToLower(strings.TrimSpace(r.URL.Query().Get("source")))), Stage: r.URL.Query().Get("stage"), Page: page, PerPage: perPage}).Normalize()
	if err != nil {
		problem(w, http.StatusUnprocessableEntity, "invalid_request", "Invalid search query")
		return searchservice.Options{}, false
	}
	return options, true
}

func (h handlers) writePage(w http.ResponseWriter, page searchservice.Page, err error) {
	if err == nil {
		for index := range page.Items {
			item := &page.Items[index]
			item.URL = fmt.Sprintf("%s/%s/%s/issues/%d", h.webOrigin, url.PathEscape(item.Organization),
				url.PathEscape(item.Repository), item.Number)
		}
		adminapi.WriteJSON(w, http.StatusOK, page)
		return
	}
	switch {
	case errors.Is(err, searchservice.ErrInvalidOptions), errors.Is(err, adminservice.ErrInvalidInput):
		problem(w, http.StatusUnprocessableEntity, "invalid_request", "Invalid search query")
	case errors.Is(err, adminservice.ErrNotFound):
		problem(w, http.StatusNotFound, "not_found", "Not found")
	case errors.Is(err, adminservice.ErrForbidden):
		problem(w, http.StatusForbidden, "forbidden", "Forbidden")
	default:
		problem(w, http.StatusInternalServerError, "internal_error", "Search failed")
	}
}

func problem(w http.ResponseWriter, status int, code, title string) {
	w.Header().Set("Cache-Control", "no-store")
	adminapi.WriteProblem(w, status, code, title)
}
