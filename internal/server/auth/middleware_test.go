package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

type fakeSessions struct{ principal Principal }

func (f fakeSessions) Authenticate(_ context.Context, token string) (Principal, error) {
	if token != "valid-session" {
		return Principal{}, ErrInvalidCredential
	}
	return f.principal, nil
}

func (f fakeSessions) ValidateCSRF(_ Principal, token string) error {
	if token != "valid-csrf" {
		return ErrInvalidCSRF
	}
	return nil
}

type fakeBearer struct{ principal Principal }

func (f fakeBearer) AuthenticateBearer(_ context.Context, token string) (Principal, error) {
	if token != "valid-bearer" {
		return Principal{}, ErrInvalidCredential
	}
	return f.principal, nil
}

func TestMiddlewareCookieMutationsRequireOriginAndCSRFButBearerIsIndependent(t *testing.T) {
	user := User{ID: uuid.New(), Login: "alice", Status: "active"}
	middleware := Middleware{
		SessionCookieName: "session",
		AllowedOrigins:    map[string]struct{}{"https://issues.example.test": {}},
		Sessions:          fakeSessions{principal: Principal{User: user, Kind: CredentialSession}},
		Bearer:            fakeBearer{principal: Principal{User: user, Kind: CredentialPAT, Scopes: []string{"issues:write"}}},
	}
	handler := middleware.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := PrincipalFromContext(r.Context())
		if !ok {
			t.Error("principal missing from request context")
		}
		w.Header().Set("Credential-Kind", string(principal.Kind))
		w.WriteHeader(http.StatusNoContent)
	}))

	tests := []struct {
		name       string
		configure  func(*http.Request)
		wantStatus int
		wantKind   CredentialKind
	}{
		{name: "cookie missing origin", configure: func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: "session", Value: "valid-session"})
			r.Header.Set("X-CSRF-Token", "valid-csrf")
		}, wantStatus: http.StatusForbidden},
		{name: "cookie wrong csrf", configure: func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: "session", Value: "valid-session"})
			r.Header.Set("Origin", "https://issues.example.test")
			r.Header.Set("X-CSRF-Token", "wrong")
		}, wantStatus: http.StatusForbidden},
		{name: "cookie valid", configure: func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: "session", Value: "valid-session"})
			r.Header.Set("Origin", "https://issues.example.test")
			r.Header.Set("X-CSRF-Token", "valid-csrf")
		}, wantStatus: http.StatusNoContent, wantKind: CredentialSession},
		{name: "bearer ignores cookie csrf state", configure: func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: "session", Value: "valid-session"})
			r.Header.Set("Authorization", "Bearer valid-bearer")
		}, wantStatus: http.StatusNoContent, wantKind: CredentialPAT},
		{name: "invalid bearer never falls back to cookie", configure: func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: "session", Value: "valid-session"})
			r.Header.Set("Origin", "https://issues.example.test")
			r.Header.Set("X-CSRF-Token", "valid-csrf")
			r.Header.Set("Authorization", "Bearer invalid")
		}, wantStatus: http.StatusUnauthorized},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/mutation", nil)
			test.configure(request)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d, body=%s", response.Code, test.wantStatus, response.Body.String())
			}
			if test.wantKind != "" && response.Header().Get("Credential-Kind") != string(test.wantKind) {
				t.Fatalf("credential kind = %q, want %q", response.Header().Get("Credential-Kind"), test.wantKind)
			}
		})
	}
}

func TestBearerChainDoesNotHideNonCredentialFailures(t *testing.T) {
	databaseFailure := errors.New("database unavailable")
	chain := BearerChain{bearerFunc(func(context.Context, string) (Principal, error) {
		return Principal{}, databaseFailure
	}), bearerFunc(func(context.Context, string) (Principal, error) {
		return Principal{User: User{ID: uuid.New()}}, nil
	})}
	if _, err := chain.AuthenticateBearer(t.Context(), "token"); !errors.Is(err, databaseFailure) {
		t.Fatalf("BearerChain error = %v, want database failure", err)
	}
}

type bearerFunc func(context.Context, string) (Principal, error)

func (f bearerFunc) AuthenticateBearer(ctx context.Context, token string) (Principal, error) {
	return f(ctx, token)
}
