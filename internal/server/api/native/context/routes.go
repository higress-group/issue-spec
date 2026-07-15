// Package contextapi exposes the authenticated SPA context and the dedicated
// one-time recovery takeover route. It remains a composable RouteSet.
package contextapi

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	adminservice "github.com/higress-group/issue-spec/internal/server/admin"
	adminapi "github.com/higress-group/issue-spec/internal/server/api/native/admin"
	"github.com/higress-group/issue-spec/internal/server/api/routeset"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/auth/session"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/spa"
)

type Dependencies struct {
	Service              ContextService
	Takeover             SessionTakeover
	Sessions             SessionCookies
	Authenticate         adminapi.Authenticate
	AuthenticateOptional adminapi.Authenticate
	AllowedOrigins       map[string]struct{}
}

type ContextService interface {
	Current(context.Context, serverauth.Principal, string) (spa.CurrentContext, error)
	Repositories(context.Context, serverauth.Principal, uuid.UUID) (spa.RepositoriesContext, error)
	Repository(context.Context, authz.Subject, string, string) (spa.RepositoryContext, error)
	LegacyIssue(context.Context, serverauth.Principal, uuid.UUID, uuid.UUID, uuid.UUID) (spa.LegacyIssueContext, error)
	UserCandidates(context.Context, serverauth.Principal, uuid.UUID, spa.CandidatePurpose, spa.CandidateMatch, string, int) (spa.UserCandidates, error)
}

type SessionTakeover interface {
	Exchange(context.Context, string, string, string, string) (session.Created, error)
}

type SessionCookies interface {
	Cookie(string) *http.Cookie
	CSRFCookie(string) *http.Cookie
}

func NewRouteSet(deps Dependencies) (routeset.RouteSet, error) {
	if deps.Service == nil || deps.Takeover == nil || deps.Sessions == nil || deps.Authenticate == nil || deps.AuthenticateOptional == nil || len(deps.AllowedOrigins) == 0 {
		return routeset.RouteSet{}, errors.New("native context: service, takeover, sessions, authentication and origins are required")
	}
	h := handlers{deps: deps}
	protected := func(handler http.Handler) http.Handler {
		return adminapi.WithRequestID(deps.Authenticate(handler))
	}
	optional := func(handler http.Handler) http.Handler {
		return adminapi.WithRequestID(deps.AuthenticateOptional(handler))
	}
	set := routeset.RouteSet{Name: "native-context", Routes: []routeset.Route{
		{Name: "native.context.get", Method: http.MethodGet, Pattern: "/api/v1/context", Handler: protected(http.HandlerFunc(h.current))},
		{Name: "native.context.repositories", Method: http.MethodGet, Pattern: "/api/v1/context/orgs/{org}/repos", Handler: protected(http.HandlerFunc(h.repositories))},
		{Name: "native.context.repository", Method: http.MethodGet, Pattern: "/api/v1/context/repos/{owner}/{repo}", Handler: optional(http.HandlerFunc(h.repository))},
		{Name: "native.context.legacy_issue", Method: http.MethodGet, Pattern: "/api/v1/context/orgs/{org}/repos/{repo}/issues/{issue}", Handler: protected(http.HandlerFunc(h.legacyIssue))},
		{Name: "native.context.user_candidates", Method: http.MethodGet, Pattern: "/api/v1/orgs/{org}/user-candidates", Handler: protected(http.HandlerFunc(h.userCandidates))},
		{Name: "native.session.recovery", Method: http.MethodPost, Pattern: "/api/v1/session/recovery", Handler: adminapi.WithRequestID(http.HandlerFunc(h.recoverSession))},
	}}
	return set, set.Validate()
}

func (h handlers) repository(w http.ResponseWriter, r *http.Request) {
	subject := authz.Anonymous()
	if principal, ok := serverauth.PrincipalFromContext(r.Context()); ok && principal.User.ID != uuid.Nil {
		subject = authz.Authenticated(principal)
	}
	result, err := h.deps.Service.Repository(r.Context(), subject, r.PathValue("owner"), r.PathValue("repo"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	adminapi.WriteJSON(w, http.StatusOK, result)
}

type handlers struct{ deps Dependencies }

func (h handlers) current(w http.ResponseWriter, r *http.Request) {
	principal, ok := serverauth.PrincipalFromContext(r.Context())
	if !ok || principal.User.ID == uuid.Nil {
		adminapi.WriteProblem(w, http.StatusUnauthorized, "authentication_required", "Authentication required")
		return
	}
	result, err := h.deps.Service.Current(r.Context(), principal, session.DefaultCSRFCookieName)
	if err != nil {
		adminapi.WriteProblem(w, http.StatusInternalServerError, "internal_error", "Request failed")
		return
	}
	adminapi.WriteJSON(w, http.StatusOK, result)
}

func (h handlers) repositories(w http.ResponseWriter, r *http.Request) {
	principal, ok := serverauth.PrincipalFromContext(r.Context())
	if !ok || principal.User.ID == uuid.Nil {
		adminapi.WriteProblem(w, http.StatusUnauthorized, "authentication_required", "Authentication required")
		return
	}
	orgID, err := uuid.Parse(r.PathValue("org"))
	if err != nil {
		adminapi.WriteProblem(w, http.StatusUnprocessableEntity, "invalid_request", "Invalid organization")
		return
	}
	result, err := h.deps.Service.Repositories(r.Context(), principal, orgID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	adminapi.WriteJSON(w, http.StatusOK, result)
}

func (h handlers) legacyIssue(w http.ResponseWriter, r *http.Request) {
	principal, ok := serverauth.PrincipalFromContext(r.Context())
	if !ok || principal.User.ID == uuid.Nil {
		adminapi.WriteProblem(w, http.StatusUnauthorized, "authentication_required", "Authentication required")
		return
	}
	orgID, orgErr := uuid.Parse(r.PathValue("org"))
	repoID, repoErr := uuid.Parse(r.PathValue("repo"))
	issueID, issueErr := uuid.Parse(r.PathValue("issue"))
	if orgErr != nil || repoErr != nil || issueErr != nil {
		adminapi.WriteProblem(w, http.StatusUnprocessableEntity, "invalid_request", "Invalid legacy issue route")
		return
	}
	result, err := h.deps.Service.LegacyIssue(r.Context(), principal, orgID, repoID, issueID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	adminapi.WriteJSON(w, http.StatusOK, result)
}

func (h handlers) userCandidates(w http.ResponseWriter, r *http.Request) {
	principal, ok := serverauth.PrincipalFromContext(r.Context())
	if !ok || principal.User.ID == uuid.Nil {
		adminapi.WriteProblem(w, http.StatusUnauthorized, "authentication_required", "Authentication required")
		return
	}
	orgID, err := uuid.Parse(r.PathValue("org"))
	if err != nil {
		adminapi.WriteProblem(w, http.StatusUnprocessableEntity, "invalid_request", "Invalid organization")
		return
	}
	purpose := spa.CandidatePurpose(strings.TrimSpace(r.URL.Query().Get("purpose")))
	match := spa.CandidateMatch(strings.TrimSpace(r.URL.Query().Get("match")))
	if match == "" {
		match = spa.MatchPrefix
	}
	limit := 20
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil {
			adminapi.WriteProblem(w, http.StatusUnprocessableEntity, "invalid_request", "Invalid limit")
			return
		}
		limit = parsed
	}
	result, err := h.deps.Service.UserCandidates(r.Context(), principal, orgID, purpose, match,
		r.URL.Query().Get("query"), limit)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	adminapi.WriteJSON(w, http.StatusOK, result)
}

func (h handlers) recoverSession(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if _, ok := h.deps.AllowedOrigins[r.Header.Get("Origin")]; !ok {
		adminapi.WriteProblem(w, http.StatusForbidden, "forbidden_origin", "Forbidden origin")
		return
	}
	var request struct {
		Token string `json:"token"`
	}
	if err := adminapi.DecodeJSON(w, r, &request); err != nil {
		adminapi.WriteProblem(w, http.StatusBadRequest, "invalid_json", "Invalid request body")
		return
	}
	created, err := h.deps.Takeover.Exchange(r.Context(), request.Token, adminapi.RequestID(r), r.UserAgent(), remoteIP(r.RemoteAddr))
	if err != nil {
		if errors.Is(err, serverauth.ErrInvalidCredential) || errors.Is(err, serverauth.ErrExpiredCredential) ||
			errors.Is(err, serverauth.ErrRevokedCredential) {
			adminapi.WriteProblem(w, http.StatusUnauthorized, "invalid_recovery_credential", "Recovery credential rejected")
			return
		}
		adminapi.WriteProblem(w, http.StatusInternalServerError, "internal_error", "Session takeover failed")
		return
	}
	http.SetCookie(w, h.deps.Sessions.Cookie(created.Token))
	http.SetCookie(w, h.deps.Sessions.CSRFCookie(created.CSRFToken))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, adminservice.ErrNotFound):
		adminapi.WriteProblem(w, http.StatusNotFound, "not_found", "Not found")
	case errors.Is(err, adminservice.ErrForbidden):
		adminapi.WriteProblem(w, http.StatusForbidden, "forbidden", "Forbidden")
	case errors.Is(err, spa.ErrInvalidInput):
		adminapi.WriteProblem(w, http.StatusUnprocessableEntity, "invalid_request", "Invalid request")
	default:
		adminapi.WriteProblem(w, http.StatusInternalServerError, "internal_error", "Request failed")
	}
}

func remoteIP(address string) string {
	host, _, err := net.SplitHostPort(address)
	if err == nil {
		return host
	}
	return ""
}
