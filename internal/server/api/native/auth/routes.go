// Package auth exposes native server authentication handlers as a RouteSet.
// The total server mux remains owned by the composition process.
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	adminapi "github.com/higress-group/issue-spec/internal/server/api/native/admin"
	"github.com/higress-group/issue-spec/internal/server/api/routeset"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/auth/pat"
	"github.com/higress-group/issue-spec/internal/server/auth/session"
	"github.com/higress-group/issue-spec/internal/server/models"
)

type LoginAdapter interface {
	ProviderID() uuid.UUID
	Kind() string
	Begin(context.Context, string) (string, error)
	Complete(context.Context, string, string) (serverauth.ExternalIdentity, string, error)
}

type IdentityAuthority interface {
	IdentitySiteAdmin(context.Context, serverauth.Principal) (bool, error)
}

type IdentityAuthorityFunc func(context.Context, serverauth.Principal) (bool, error)

func (f IdentityAuthorityFunc) IdentitySiteAdmin(ctx context.Context, principal serverauth.Principal) (bool, error) {
	return f(ctx, principal)
}

type Dependencies struct {
	Identity   *serverauth.IdentityService
	Sessions   *session.Service
	PATs       *pat.Service
	Authority  IdentityAuthority
	Middleware serverauth.Middleware
	Adapters   map[string]LoginAdapter
	WebOrigin  string
}

func NewRouteSet(deps Dependencies) (routeset.RouteSet, error) {
	if deps.Identity == nil || deps.Sessions == nil || deps.PATs == nil || deps.Authority == nil || deps.Middleware.Sessions == nil || deps.Middleware.Bearer == nil {
		return routeset.RouteSet{}, errors.New("native auth: incomplete dependencies")
	}
	origin, err := url.Parse(deps.WebOrigin)
	if err != nil || !origin.IsAbs() || origin.Host == "" || (origin.Scheme != "http" && origin.Scheme != "https") {
		return routeset.RouteSet{}, errors.New("native auth: web origin must be an absolute HTTP(S) URL")
	}
	if origin.User != nil || (origin.Path != "" && origin.Path != "/") || origin.RawQuery != "" || origin.Fragment != "" {
		return routeset.RouteSet{}, errors.New("native auth: web origin must not contain credentials, path, query, or fragment")
	}
	canonicalOrigin := origin.Scheme + "://" + origin.Host
	allowedOrigins := make(map[string]struct{}, len(deps.Middleware.AllowedOrigins)+1)
	for allowed := range deps.Middleware.AllowedOrigins {
		allowedOrigins[allowed] = struct{}{}
	}
	allowedOrigins[canonicalOrigin] = struct{}{}
	deps.Middleware.AllowedOrigins = allowedOrigins
	deps.Middleware.SessionCookieName = deps.Sessions.CookieName()
	h := &handlers{deps: deps, canonicalWebOrigin: canonicalOrigin}
	nativeProtected := adminapi.NativeAuthenticate(deps.Middleware)
	compatProtected := deps.Middleware.Authenticate
	public := func(handler http.Handler) http.Handler { return adminapi.WithRequestID(handler) }
	protected := func(handler http.Handler) http.Handler { return adminapi.WithRequestID(nativeProtected(handler)) }
	set := routeset.RouteSet{Name: "native-auth", Routes: []routeset.Route{
		{Name: "native.auth.providers", Method: http.MethodGet, Pattern: "/api/v1/auth/providers", Handler: public(http.HandlerFunc(h.providers))},
		{Name: "native.auth.login", Method: http.MethodGet, Pattern: "/api/v1/auth/{provider}/login", Handler: public(http.HandlerFunc(h.login))},
		{Name: "native.auth.callback", Method: http.MethodGet, Pattern: "/api/v1/auth/{provider}/callback", Handler: public(http.HandlerFunc(h.callback))},
		{Name: "native.session.rotate", Method: http.MethodPost, Pattern: "/api/v1/session/rotate", Handler: protected(http.HandlerFunc(h.rotateSession))},
		{Name: "native.session.logout", Method: http.MethodDelete, Pattern: "/api/v1/session", Handler: protected(http.HandlerFunc(h.logout))},
		{Name: "native.pats.list", Method: http.MethodGet, Pattern: "/api/v1/pats", Handler: protected(http.HandlerFunc(h.listPATs))},
		{Name: "native.pats.create", Method: http.MethodPost, Pattern: "/api/v1/pats", Handler: protected(http.HandlerFunc(h.createPAT))},
		{Name: "native.pats.rotate", Method: http.MethodPost, Pattern: "/api/v1/pats/{id}/rotate", Handler: protected(http.HandlerFunc(h.rotatePAT))},
		{Name: "native.pats.revoke", Method: http.MethodDelete, Pattern: "/api/v1/pats/{id}", Handler: protected(http.HandlerFunc(h.revokePAT))},
		{Name: "compat.user.get", Method: http.MethodGet, Pattern: "/user", Handler: adminapi.WithRequestID(compatProtected(http.HandlerFunc(h.user)))},
	}}
	return set, set.Validate()
}

type handlers struct {
	deps               Dependencies
	canonicalWebOrigin string
}

func (h *handlers) providers(w http.ResponseWriter, _ *http.Request) {
	names := make([]string, 0, len(h.deps.Adapters))
	for name := range h.deps.Adapters {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]map[string]string, 0, len(names))
	for _, name := range names {
		result = append(result, map[string]string{"name": name, "kind": h.deps.Adapters[name].Kind()})
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": result})
}

func (h *handlers) login(w http.ResponseWriter, r *http.Request) {
	adapter, ok := h.deps.Adapters[r.PathValue("provider")]
	if !ok {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	returnTo := r.URL.Query().Get("return_to")
	if returnTo != "" && !safeReturnTo(returnTo) {
		writeError(w, http.StatusBadRequest, "Invalid return path")
		return
	}
	location, err := adapter.Begin(r.Context(), returnTo)
	if err != nil {
		writeError(w, http.StatusBadGateway, "Authentication provider unavailable")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, location, http.StatusFound)
}

func (h *handlers) callback(w http.ResponseWriter, r *http.Request) {
	adapter, ok := h.deps.Adapters[r.PathValue("provider")]
	if !ok {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	identity, returnTo, err := adapter.Complete(r.Context(), r.URL.Query().Get("state"), r.URL.Query().Get("code"))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "Authentication failed")
		return
	}
	provider, err := h.deps.Identity.Provider(r.Context(), adapter.ProviderID())
	if err != nil || provider.Kind != adapter.Kind() {
		writeError(w, http.StatusUnauthorized, "Authentication failed")
		return
	}
	user, err := h.deps.Identity.ResolveOrProvision(r.Context(), provider, identity)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "Authentication failed")
		return
	}
	priorToken := ""
	if existing, cookieErr := r.Cookie(h.deps.Sessions.CookieName()); cookieErr == nil {
		priorToken = existing.Value
	}
	created, err := h.deps.Sessions.Replace(r.Context(), priorToken, user.ID, r.UserAgent(), remoteIP(r.RemoteAddr))
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Authentication unavailable")
		return
	}
	http.SetCookie(w, h.deps.Sessions.Cookie(created.Token))
	http.SetCookie(w, h.deps.Sessions.CSRFCookie(created.CSRFToken))
	w.Header().Set("Cache-Control", "no-store")
	if returnTo != "" && safeReturnTo(returnTo) {
		http.Redirect(w, r, h.canonicalWebOrigin+returnTo, http.StatusFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": user, "csrf_token": created.CSRFToken})
}

func (h *handlers) rotateSession(w http.ResponseWriter, r *http.Request) {
	principal, _ := serverauth.PrincipalFromContext(r.Context())
	if principal.Kind != serverauth.CredentialSession {
		writeError(w, http.StatusBadRequest, "Browser session required")
		return
	}
	cookie, err := r.Cookie(h.deps.Sessions.CookieName())
	if err != nil {
		writeError(w, http.StatusUnauthorized, "Requires authentication")
		return
	}
	created, err := h.deps.Sessions.Rotate(r.Context(), cookie.Value, r.UserAgent(), remoteIP(r.RemoteAddr))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "Requires authentication")
		return
	}
	http.SetCookie(w, h.deps.Sessions.Cookie(created.Token))
	http.SetCookie(w, h.deps.Sessions.CSRFCookie(created.CSRFToken))
	writeJSON(w, http.StatusOK, map[string]string{"csrf_token": created.CSRFToken})
}

func (h *handlers) logout(w http.ResponseWriter, r *http.Request) {
	principal, _ := serverauth.PrincipalFromContext(r.Context())
	if principal.Kind != serverauth.CredentialSession {
		writeError(w, http.StatusBadRequest, "Browser session required")
		return
	}
	cookie, err := r.Cookie(h.deps.Sessions.CookieName())
	if err != nil {
		writeError(w, http.StatusUnauthorized, "Requires authentication")
		return
	}
	if err := h.deps.Sessions.Revoke(r.Context(), cookie.Value); err != nil {
		writeError(w, http.StatusServiceUnavailable, "Logout unavailable")
		return
	}
	http.SetCookie(w, h.deps.Sessions.ClearCookie())
	http.SetCookie(w, h.deps.Sessions.ClearCSRFCookie())
	w.WriteHeader(http.StatusNoContent)
}

func (h *handlers) user(w http.ResponseWriter, r *http.Request) {
	principal, _ := serverauth.PrincipalFromContext(r.Context())
	siteAdmin, err := h.deps.Authority.IdentitySiteAdmin(r.Context(), principal)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Request failed")
		return
	}
	w.Header().Set("X-Accepted-OAuth-Scopes", "read:user")
	w.Header().Set("X-OAuth-Scopes", strings.Join(principal.Scopes, ", "))
	writeJSON(w, http.StatusOK, map[string]any{
		"id": principal.User.ID.String(), "node_id": "USER_" + principal.User.ID.String(),
		"login": principal.User.Login, "name": principal.User.DisplayName, "email": principal.User.Email,
		"type": "User", "site_admin": siteAdmin,
	})
}

type patRequest struct {
	Name         string   `json:"name"`
	Scopes       []string `json:"scopes"`
	Repositories []struct {
		OrganizationID uuid.UUID `json:"organization_id"`
		RepositoryID   uuid.UUID `json:"repository_id"`
	} `json:"repositories"`
	ExpiresAt *time.Time `json:"expires_at"`
}

func (h *handlers) createPAT(w http.ResponseWriter, r *http.Request) {
	principal, _ := serverauth.PrincipalFromContext(r.Context())
	if principal.Kind != serverauth.CredentialSession {
		writeError(w, http.StatusForbidden, "Browser session required")
		return
	}
	var request patRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request")
		return
	}
	repos := make([]models.RepoScope, len(request.Repositories))
	for i, repo := range request.Repositories {
		repos[i] = models.RepoScope{OrgID: repo.OrganizationID, RepoID: repo.RepositoryID}
	}
	if request.Repositories == nil {
		repos = nil
	}
	created, err := h.deps.PATs.Create(r.Context(), principal.User.ID, pat.CreateInput{
		Name: request.Name, Scopes: request.Scopes, Repositories: repos, ExpiresAt: request.ExpiresAt,
	})
	if err != nil {
		writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (h *handlers) listPATs(w http.ResponseWriter, r *http.Request) {
	principal, _ := serverauth.PrincipalFromContext(r.Context())
	if principal.Kind != serverauth.CredentialSession {
		writeError(w, http.StatusForbidden, "Browser session required")
		return
	}
	tokens, err := h.deps.PATs.List(r.Context(), principal.User.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Request failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": tokens})
}

func (h *handlers) rotatePAT(w http.ResponseWriter, r *http.Request) {
	principal, _ := serverauth.PrincipalFromContext(r.Context())
	if principal.Kind != serverauth.CredentialSession {
		writeError(w, http.StatusForbidden, "Browser session required")
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	created, err := h.deps.PATs.Rotate(r.Context(), principal.User.ID, id)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (h *handlers) revokePAT(w http.ResponseWriter, r *http.Request) {
	principal, _ := serverauth.PrincipalFromContext(r.Context())
	if principal.Kind != serverauth.CredentialSession {
		writeError(w, http.StatusForbidden, "Browser session required")
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	if err := h.deps.PATs.Revoke(r.Context(), principal.User.ID, id); err != nil {
		writeAuthError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func writeAuthError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, serverauth.ErrNotFound):
		writeError(w, http.StatusNotFound, "Not Found")
	case errors.Is(err, serverauth.ErrInsufficientScope), errors.Is(err, serverauth.ErrInvalidCredential), errors.Is(err, serverauth.ErrExpiredCredential):
		writeError(w, http.StatusUnprocessableEntity, "Invalid request")
	case errors.Is(err, serverauth.ErrDisabledAccount), errors.Is(err, serverauth.ErrRevokedCredential):
		writeError(w, http.StatusForbidden, "Forbidden")
	default:
		writeError(w, http.StatusInternalServerError, "Request failed")
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	code := "request_failed"
	switch status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		code = "invalid_request"
	case http.StatusUnauthorized:
		code = "authentication_failed"
	case http.StatusForbidden:
		code = "forbidden"
	case http.StatusNotFound:
		code = "not_found"
	case http.StatusConflict:
		code = "conflict"
	case http.StatusBadGateway:
		code = "provider_unavailable"
	case http.StatusServiceUnavailable:
		code = "authentication_unavailable"
	case http.StatusInternalServerError:
		code = "internal_error"
	}
	adminapi.WriteProblem(w, status, code, message)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func safeReturnTo(value string) bool {
	return strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "//") && !strings.ContainsAny(value, "\r\n")
}

func remoteIP(value string) string {
	host, _, err := net.SplitHostPort(value)
	if err == nil {
		return host
	}
	return strings.TrimSpace(value)
}
