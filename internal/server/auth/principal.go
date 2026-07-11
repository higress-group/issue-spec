package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

type CredentialKind string

const (
	CredentialSession   CredentialKind = "session"
	CredentialPAT       CredentialKind = "pat"
	CredentialDelegated CredentialKind = "delegated"
	CredentialRecovery  CredentialKind = "recovery"
)

type Principal struct {
	User           User
	Kind           CredentialKind
	CredentialID   uuid.UUID
	Scopes         []string
	RepositoryCaps []RepositoryCap
	RepoRestricted bool
	OrgID          uuid.UUID
	RepoID         uuid.UUID
	JobID          string
	Purpose        string
	Audience       string
	CSRFHash       []byte
	IdleExpiresAt  time.Time
	ExpiresAt      time.Time
}

type RepositoryCap struct {
	OrgID  uuid.UUID
	RepoID uuid.UUID
}

func (p Principal) HasScope(scope string) bool {
	for _, granted := range p.Scopes {
		if granted == scope {
			return true
		}
	}
	return false
}

func (p Principal) AllowsRepository(orgID, repoID uuid.UUID) bool {
	if !p.RepoRestricted {
		return true
	}
	for _, allowed := range p.RepositoryCaps {
		if allowed.OrgID == orgID && allowed.RepoID == repoID {
			return true
		}
	}
	return false
}

type principalContextKey struct{}

func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, principal)
}

func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(Principal)
	return principal, ok
}

type SessionAuthenticator interface {
	Authenticate(context.Context, string) (Principal, error)
	ValidateCSRF(Principal, string) error
}

type BearerAuthenticator interface {
	AuthenticateBearer(context.Context, string) (Principal, error)
}

type Middleware struct {
	SessionCookieName string
	AllowedOrigins    map[string]struct{}
	Sessions          SessionAuthenticator
	Bearer            BearerAuthenticator
	Unauthorized      func(http.ResponseWriter, *http.Request)
	Forbidden         func(http.ResponseWriter, *http.Request)
}

// Authenticate selects exactly one credential realm. An Authorization header
// is never allowed to fall back to a browser cookie, which keeps bearer clients
// independent from cookie CSRF state.
func (m Middleware) Authenticate(next http.Handler) http.Handler {
	return m.authenticate(next, false)
}

// AuthenticateOptional attaches a principal when the request presents a
// credential, but allows credential-free requests to continue. Invalid
// credentials still fail closed. This is intended for public read routes; the
// authorization layer remains responsible for deciding whether the anonymous
// request may see the selected resource.
func (m Middleware) AuthenticateOptional(next http.Handler) http.Handler {
	return m.authenticate(next, true)
}

func (m Middleware) authenticate(next http.Handler, optional bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var principal Principal
		var err error
		authorization := strings.TrimSpace(r.Header.Get("Authorization"))
		if authorization != "" {
			kind, token, ok := strings.Cut(authorization, " ")
			if !ok || !strings.EqualFold(kind, "Bearer") || strings.TrimSpace(token) == "" || m.Bearer == nil {
				m.writeUnauthorized(w, r)
				return
			}
			principal, err = m.Bearer.AuthenticateBearer(r.Context(), strings.TrimSpace(token))
		} else {
			cookieName := m.SessionCookieName
			if cookieName == "" {
				cookieName = "issue_spec_session"
			}
			cookie, cookieErr := r.Cookie(cookieName)
			if errors.Is(cookieErr, http.ErrNoCookie) && optional {
				next.ServeHTTP(w, r)
				return
			}
			if cookieErr != nil || m.Sessions == nil {
				m.writeUnauthorized(w, r)
				return
			}
			principal, err = m.Sessions.Authenticate(r.Context(), cookie.Value)
			if err == nil && mutationMethod(r.Method) {
				if _, ok := m.AllowedOrigins[r.Header.Get("Origin")]; !ok {
					m.writeForbidden(w, r)
					return
				}
				if err = m.Sessions.ValidateCSRF(principal, r.Header.Get("X-CSRF-Token")); err != nil {
					m.writeForbidden(w, r)
					return
				}
			}
		}
		if err != nil {
			m.writeUnauthorized(w, r)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), principal)))
	})
}

func mutationMethod(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}

func (m Middleware) writeUnauthorized(w http.ResponseWriter, r *http.Request) {
	if m.Unauthorized != nil {
		m.Unauthorized(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"message":"Requires authentication"}`))
}

func (m Middleware) writeForbidden(w http.ResponseWriter, r *http.Request) {
	if m.Forbidden != nil {
		m.Forbidden(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`{"message":"Forbidden"}`))
}
