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
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/api/github/codec"
	adminapi "github.com/higress-group/issue-spec/internal/server/api/native/admin"
	"github.com/higress-group/issue-spec/internal/server/api/routeset"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/auth/githuboauth"
	"github.com/higress-group/issue-spec/internal/server/auth/pat"
	"github.com/higress-group/issue-spec/internal/server/auth/session"
	"github.com/higress-group/issue-spec/internal/server/models"
	mailservice "github.com/higress-group/issue-spec/internal/server/profilemail"
)

type LoginAdapter interface {
	ProviderID() uuid.UUID
	Kind() string
	Begin(context.Context, string) (serverauth.LoginStart, error)
	Complete(context.Context, string, string, string) (serverauth.ExternalIdentity, string, error)
}

type richerLoginAdapter interface {
	CompleteLogin(context.Context, string, string, string) (serverauth.LoginCompletion, error)
}

type AuthenticationDiagnostic struct {
	RequestID  string `json:"request_id"`
	Provider   string `json:"provider"`
	ReasonCode string `json:"reason_code"`
}

type DiagnosticObserver interface {
	ObserveAuthenticationDiagnostic(context.Context, AuthenticationDiagnostic)
}

type DiagnosticObserverFunc func(context.Context, AuthenticationDiagnostic)

func (f DiagnosticObserverFunc) ObserveAuthenticationDiagnostic(ctx context.Context, diagnostic AuthenticationDiagnostic) {
	f(ctx, diagnostic)
}

const loginTransactionCookieName = "issue_spec_login"

type IdentityAuthority interface {
	IdentitySiteAdmin(context.Context, serverauth.Principal) (bool, error)
}

type IdentityAuthorityFunc func(context.Context, serverauth.Principal) (bool, error)

func (f IdentityAuthorityFunc) IdentitySiteAdmin(ctx context.Context, principal serverauth.Principal) (bool, error) {
	return f(ctx, principal)
}

type Dependencies struct {
	Identity     *serverauth.IdentityService
	Sessions     *session.Service
	PATs         *pat.Service
	Authority    IdentityAuthority
	Middleware   serverauth.Middleware
	Adapters     map[string]LoginAdapter
	Avatars      *serverauth.AvatarService
	Diagnostics  DiagnosticObserver
	WebOrigin    string
	ProfileMail  ProfileMailReader
	EmailEnabled bool
}

type ProfileMailReader interface {
	Get(context.Context, uuid.UUID) (mailservice.Profile, error)
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
		{Name: "native.auth.avatar", Method: http.MethodGet, Pattern: "/api/v1/avatars/{login}", Handler: public(http.HandlerFunc(h.avatar))},
		{Name: "native.users.get", Method: http.MethodGet, Pattern: "/api/v1/users/{login}", Handler: public(http.HandlerFunc(h.publicProfile))},
		{Name: "native.profile.get", Method: http.MethodGet, Pattern: "/api/v1/profile", Handler: protected(http.HandlerFunc(h.profile))},
		{Name: "native.profile.update", Method: http.MethodPatch, Pattern: "/api/v1/profile", Handler: protected(http.HandlerFunc(h.updateProfile))},
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

func (h *handlers) avatar(w http.ResponseWriter, r *http.Request) {
	if h.deps.Avatars == nil {
		http.NotFound(w, r)
		return
	}
	avatar, err := h.deps.Avatars.FetchForLogin(r.Context(), r.PathValue("login"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("ETag", avatar.ETag)
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Header.Get("If-None-Match") == avatar.ETag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", avatar.ContentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(avatar.Data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(avatar.Data)
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
	start, err := adapter.Begin(r.Context(), returnTo)
	if err != nil {
		writeError(w, http.StatusBadGateway, "Authentication provider unavailable")
		return
	}
	if strings.TrimSpace(start.AuthorizationURL) == "" || strings.TrimSpace(start.BrowserNonce) == "" || start.ExpiresAt.IsZero() {
		writeError(w, http.StatusBadGateway, "Authentication provider unavailable")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	http.SetCookie(w, h.loginTransactionCookie(r.PathValue("provider"), start.BrowserNonce, start.ExpiresAt))
	http.Redirect(w, r, start.AuthorizationURL, http.StatusFound)
}

func (h *handlers) callback(w http.ResponseWriter, r *http.Request) {
	adapter, ok := h.deps.Adapters[r.PathValue("provider")]
	if !ok {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	binding, cookieErr := r.Cookie(loginTransactionCookieName)
	http.SetCookie(w, h.clearLoginTransactionCookie(r.PathValue("provider")))
	if cookieErr != nil || strings.TrimSpace(binding.Value) == "" {
		writeError(w, http.StatusUnauthorized, "Authentication failed")
		return
	}
	ctx := serverauth.WithAdmissionRequestID(r.Context(), adminapi.RequestID(r))
	var identity serverauth.ExternalIdentity
	var returnTo string
	var admission *serverauth.AdmissionEvidence
	var err error
	if richer, ok := adapter.(richerLoginAdapter); ok {
		completion, completeErr := richer.CompleteLogin(ctx, r.URL.Query().Get("state"), r.URL.Query().Get("code"), binding.Value)
		identity, returnTo, admission, err = completion.Identity, completion.ReturnTo, completion.Admission, completeErr
	} else {
		identity, returnTo, err = adapter.Complete(ctx, r.URL.Query().Get("state"), r.URL.Query().Get("code"), binding.Value)
	}
	if err != nil {
		h.observeAuthenticationFailure(r, adapter.Kind(), err)
		writeError(w, http.StatusUnauthorized, "Authentication failed")
		return
	}
	if (adapter.Kind() == "github-oauth" && admission == nil) ||
		(admission != nil && (!admission.Audited || admission.Policy == "" || admission.Decision != "allowed" || admission.RequestID != adminapi.RequestID(r))) {
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

func (h *handlers) observeAuthenticationFailure(r *http.Request, adapterKind string, err error) {
	if h.deps.Diagnostics == nil {
		return
	}
	reasonCode := ""
	if class, ok := githuboauth.AdmissionFailure(err); ok {
		reasonCode = string(class)
	} else if adapterKind == "github-oauth" {
		if class, ok := githuboauth.CallbackFailure(err); ok {
			reasonCode = string(class)
		}
	}
	if reasonCode == "" {
		return
	}
	h.deps.Diagnostics.ObserveAuthenticationDiagnostic(r.Context(), AuthenticationDiagnostic{
		RequestID: adminapi.RequestID(r), Provider: r.PathValue("provider"), ReasonCode: reasonCode,
	})
}

func (h *handlers) loginTransactionCookie(provider, value string, expires time.Time) *http.Cookie {
	base := h.deps.Sessions.Cookie("")
	maxAge := int(time.Until(expires).Seconds())
	if maxAge < 1 {
		maxAge = 1
	}
	return &http.Cookie{Name: loginTransactionCookieName, Value: value,
		Path: "/api/v1/auth/" + url.PathEscape(provider) + "/callback", Domain: base.Domain,
		Secure: base.Secure, HttpOnly: true, SameSite: http.SameSiteLaxMode,
		MaxAge: maxAge, Expires: expires}
}

func (h *handlers) clearLoginTransactionCookie(provider string) *http.Cookie {
	cookie := h.loginTransactionCookie(provider, "", time.Now().Add(time.Second))
	cookie.MaxAge = -1
	cookie.Expires = time.Unix(1, 0)
	return cookie
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
		"id": codec.StableNumericID(principal.User.ID.String()), "node_id": codec.NodeID("User", principal.User.ID.String()),
		"login": principal.User.Login, "name": principal.User.DisplayName, "email": principal.User.Email,
		"avatar_url": h.canonicalWebOrigin + "/api/v1/avatars/" + url.PathEscape(principal.User.Login),
		"type":       "User", "site_admin": siteAdmin,
	})
}

func (h *handlers) publicProfile(w http.ResponseWriter, r *http.Request) {
	profile, err := h.deps.Identity.PublicProfile(r.Context(), r.PathValue("login"))
	if err != nil {
		writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h.publicProfileResponse(profile))
}

func (h *handlers) profile(w http.ResponseWriter, r *http.Request) {
	principal, _ := serverauth.PrincipalFromContext(r.Context())
	profile, err := h.deps.Identity.Profile(r.Context(), principal.User.ID)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	response, err := h.privateProfileResponse(r.Context(), profile)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Profile email status unavailable")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

type profileUpdateRequest struct {
	Nickname        string `json:"nickname"`
	ExpectedVersion int64  `json:"expected_version"`
}

func (h *handlers) updateProfile(w http.ResponseWriter, r *http.Request) {
	principal, _ := serverauth.PrincipalFromContext(r.Context())
	if principal.Kind != serverauth.CredentialSession {
		writeError(w, http.StatusForbidden, "Browser session required")
		return
	}
	var request profileUpdateRequest
	if err := decodeJSON(w, r, &request); err != nil || len([]rune(strings.TrimSpace(request.Nickname))) > 80 || request.ExpectedVersion < 1 {
		writeError(w, http.StatusBadRequest, "Invalid request")
		return
	}
	profile, err := h.deps.Identity.UpdateNickname(r.Context(), principal.User.ID, request.Nickname,
		request.ExpectedVersion, adminapi.RequestID(r))
	if err != nil {
		writeAuthError(w, err)
		return
	}
	response, err := h.privateProfileResponse(r.Context(), profile)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "Profile email status unavailable")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *handlers) publicProfileResponse(profile serverauth.Profile) map[string]any {
	return map[string]any{
		"id": codec.StableNumericID(profile.ID.String()), "node_id": codec.NodeID("User", profile.ID.String()),
		"login": profile.Login, "display_name": profile.DisplayName,
		"avatar_url": h.canonicalWebOrigin + "/api/v1/avatars/" + url.PathEscape(profile.Login),
		"html_url":   h.canonicalWebOrigin + "/users/" + url.PathEscape(profile.Login),
		"type":       "User", "site_admin": false,
	}
}

func (h *handlers) privateProfileResponse(ctx context.Context, profile serverauth.Profile) (map[string]any, error) {
	response := h.publicProfileResponse(profile)
	response["identity_display_name"] = profile.IdentityDisplayName
	response["nickname"] = profile.Nickname
	response["representation_version"] = profile.RepresentationVersion
	response["notification_email_available"] = h.deps.EmailEnabled && h.deps.ProfileMail != nil
	response["onboarding_completed"] = !h.deps.EmailEnabled
	response["notification_email"] = nil
	response["notification_email_verified_at"] = nil
	response["pending_notification_email"] = nil
	response["allowed_email_domain_suffixes"] = []string{}
	if h.deps.EmailEnabled && h.deps.ProfileMail != nil {
		mailProfile, err := h.deps.ProfileMail.Get(ctx, profile.ID)
		if err != nil {
			return nil, err
		}
		response["onboarding_completed"] = mailProfile.OnboardingCompletedAt != nil
		response["notification_email"] = mailProfile.NotificationEmail
		response["notification_email_verified_at"] = mailProfile.NotificationVerifiedAt
		response["allowed_email_domain_suffixes"] = mailProfile.AllowedEmailDomainSuffixes
		if mailProfile.Pending != nil {
			response["pending_notification_email"] = map[string]any{"id": mailProfile.Pending.ID,
				"email": mailProfile.Pending.PendingEmail, "expires_at": mailProfile.Pending.ExpiresAt,
				"sent_at": mailProfile.Pending.SentAt, "representation_version": mailProfile.Pending.RepresentationVersion}
		}
		response["representation_version"] = mailProfile.RepresentationVersion
	}
	return response, nil
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
	case errors.Is(err, serverauth.ErrConflict):
		writeError(w, http.StatusConflict, "Conflict")
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
