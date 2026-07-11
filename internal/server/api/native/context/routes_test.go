package contextapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	adminservice "github.com/higress-group/issue-spec/internal/server/admin"
	adminapi "github.com/higress-group/issue-spec/internal/server/api/native/admin"
	"github.com/higress-group/issue-spec/internal/server/api/routeset"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/auth/session"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/spa"
)

func TestContextRouteRequiresCompleteDependencies(t *testing.T) {
	if _, err := NewRouteSet(Dependencies{}); err == nil {
		t.Fatal("incomplete dependencies were accepted")
	}
}

func TestContextAuthenticationFailureIsNativeProblem(t *testing.T) {
	set, err := NewRouteSet(Dependencies{Service: fakeContextService{}, Takeover: fakeTakeover{}, Sessions: fakeCookies{},
		Authenticate:   adminapi.NativeAuthenticate(serverauth.Middleware{}),
		AllowedOrigins: map[string]struct{}{"https://issues.example.test": {}}})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	routeHandler(t, set, "native.context.get").ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/context", nil))
	if response.Code != http.StatusUnauthorized || !strings.HasPrefix(response.Header().Get("Content-Type"), "application/problem+json") ||
		!strings.Contains(response.Body.String(), `"code":"authentication_required"`) || response.Header().Get("X-Request-ID") == "" {
		t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
}

func TestCurrentContextUsesAuthenticatedPrincipal(t *testing.T) {
	userID := uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	service := fakeContextService{current: func(_ context.Context, principal serverauth.Principal, csrf string) (spa.CurrentContext, error) {
		if principal.User.ID != userID || csrf != session.DefaultCSRFCookieName {
			t.Fatalf("principal=%+v csrf=%q", principal, csrf)
		}
		return spa.CurrentContext{User: spa.UserContext{ID: userID, Login: "alice"}, Organizations: []spa.OrganizationContext{}}, nil
	}}
	set := testRouteSet(t, service, fakeTakeover{}, fakeCookies{})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/context", nil)
	response := httptest.NewRecorder()
	routeHandler(t, set, "native.context.get").ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"login":"alice"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRecoveryExchangeRequiresExactOriginAndSetsCookies(t *testing.T) {
	takeover := fakeTakeover{exchange: func(_ context.Context, token, requestID, _, _ string) (session.Created, error) {
		if token != "iss_rcv_once_secret" || requestID == "" {
			t.Fatalf("token=%q requestID=%q", token, requestID)
		}
		return session.Created{Token: "session-token", CSRFToken: "csrf-token"}, nil
	}}
	set := testRouteSet(t, fakeContextService{}, takeover, fakeCookies{})

	wrong := httptest.NewRequest(http.MethodPost, "/api/v1/session/recovery", strings.NewReader(`{"token":"iss_rcv_once_secret"}`))
	wrong.Header.Set("Origin", "https://evil.example")
	wrongResponse := httptest.NewRecorder()
	routeHandler(t, set, "native.session.recovery").ServeHTTP(wrongResponse, wrong)
	if wrongResponse.Code != http.StatusForbidden {
		t.Fatalf("wrong origin status=%d body=%s", wrongResponse.Code, wrongResponse.Body.String())
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/session/recovery", strings.NewReader(`{"token":"iss_rcv_once_secret"}`))
	request.Header.Set("Origin", "https://issues.example.test")
	response := httptest.NewRecorder()
	routeHandler(t, set, "native.session.recovery").ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || response.Header().Get("Cache-Control") != "no-store" || len(response.Result().Cookies()) != 2 {
		t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
}

func TestRecoveryFailuresUseOneNoStoreProblem(t *testing.T) {
	set := testRouteSet(t, fakeContextService{}, fakeTakeover{exchange: func(context.Context, string, string, string, string) (session.Created, error) {
		return session.Created{}, serverauth.ErrInvalidCredential
	}}, fakeCookies{})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/session/recovery", strings.NewReader(`{"token":"bad"}`))
	request.Header.Set("Origin", "https://issues.example.test")
	response := httptest.NewRecorder()
	routeHandler(t, set, "native.session.recovery").ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || response.Header().Get("Cache-Control") != "no-store" ||
		!strings.Contains(response.Body.String(), `"code":"invalid_recovery_credential"`) || strings.Contains(response.Body.String(), "bad") {
		t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
}

func TestRepositoryContextSuccessNotFoundAndInvalidScope(t *testing.T) {
	orgID := uuid.New()
	service := fakeContextService{repositories: func(_ context.Context, _ serverauth.Principal, got uuid.UUID) (spa.RepositoriesContext, error) {
		if got != orgID {
			t.Fatalf("org = %s", got)
		}
		return spa.RepositoriesContext{Repositories: []authz.RepositoryContextAccess{}}, nil
	}}
	set := testRouteSet(t, service, fakeTakeover{}, fakeCookies{})
	handler := routeHandler(t, set, "native.context.repositories")
	request := httptest.NewRequest(http.MethodGet, "/api/v1/context/orgs/"+orgID.String()+"/repos", nil)
	request.SetPathValue("org", orgID.String())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"repositories":[]`) {
		t.Fatalf("success status=%d body=%s", response.Code, response.Body.String())
	}

	missingSet := testRouteSet(t, fakeContextService{repositories: func(context.Context, serverauth.Principal, uuid.UUID) (spa.RepositoriesContext, error) {
		return spa.RepositoriesContext{}, adminservice.ErrNotFound
	}}, fakeTakeover{}, fakeCookies{})
	missing := httptest.NewRequest(http.MethodGet, "/api/v1/context/orgs/"+orgID.String()+"/repos", nil)
	missing.SetPathValue("org", orgID.String())
	missingResponse := httptest.NewRecorder()
	routeHandler(t, missingSet, "native.context.repositories").ServeHTTP(missingResponse, missing)
	if missingResponse.Code != http.StatusNotFound || !strings.Contains(missingResponse.Body.String(), `"code":"not_found"`) {
		t.Fatalf("missing status=%d body=%s", missingResponse.Code, missingResponse.Body.String())
	}

	invalid := httptest.NewRequest(http.MethodGet, "/api/v1/context/orgs/bad/repos", nil)
	invalid.SetPathValue("org", "bad")
	invalidResponse := httptest.NewRecorder()
	handler.ServeHTTP(invalidResponse, invalid)
	if invalidResponse.Code != http.StatusUnprocessableEntity || !strings.Contains(invalidResponse.Body.String(), `"code":"invalid_request"`) {
		t.Fatalf("invalid status=%d body=%s", invalidResponse.Code, invalidResponse.Body.String())
	}
}

func TestUserCandidatesSuccessAndInvalidLimit(t *testing.T) {
	orgID := uuid.New()
	service := fakeContextService{candidates: func(_ context.Context, _ serverauth.Principal, got uuid.UUID,
		purpose spa.CandidatePurpose, match spa.CandidateMatch, query string, limit int) (spa.UserCandidates, error) {
		if got != orgID || purpose != spa.PurposeMembership || match != spa.MatchExact || query != "alice" || limit != 7 {
			t.Fatalf("candidate request org=%s purpose=%s match=%s query=%s limit=%d", got, purpose, match, query, limit)
		}
		return spa.UserCandidates{Users: []spa.UserCandidate{{ID: uuid.New(), Login: "alice", Kind: "human"}}}, nil
	}}
	set := testRouteSet(t, service, fakeTakeover{}, fakeCookies{})
	handler := routeHandler(t, set, "native.context.user_candidates")
	request := httptest.NewRequest(http.MethodGet, "/api/v1/orgs/"+orgID.String()+"/user-candidates?purpose=membership&match=exact&query=alice&limit=7", nil)
	request.SetPathValue("org", orgID.String())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"login":"alice"`) {
		t.Fatalf("success status=%d body=%s", response.Code, response.Body.String())
	}

	invalid := httptest.NewRequest(http.MethodGet, "/api/v1/orgs/"+orgID.String()+"/user-candidates?purpose=membership&limit=nope", nil)
	invalid.SetPathValue("org", orgID.String())
	invalidResponse := httptest.NewRecorder()
	handler.ServeHTTP(invalidResponse, invalid)
	if invalidResponse.Code != http.StatusUnprocessableEntity || !strings.Contains(invalidResponse.Body.String(), `"code":"invalid_request"`) {
		t.Fatalf("invalid status=%d body=%s", invalidResponse.Code, invalidResponse.Body.String())
	}
}

type fakeContextService struct {
	current      func(context.Context, serverauth.Principal, string) (spa.CurrentContext, error)
	repositories func(context.Context, serverauth.Principal, uuid.UUID) (spa.RepositoriesContext, error)
	candidates   func(context.Context, serverauth.Principal, uuid.UUID, spa.CandidatePurpose, spa.CandidateMatch, string, int) (spa.UserCandidates, error)
}

func (f fakeContextService) Current(ctx context.Context, principal serverauth.Principal, csrf string) (spa.CurrentContext, error) {
	if f.current == nil {
		return spa.CurrentContext{}, errors.New("not configured")
	}
	return f.current(ctx, principal, csrf)
}

func (f fakeContextService) Repositories(ctx context.Context, principal serverauth.Principal, orgID uuid.UUID) (spa.RepositoriesContext, error) {
	if f.repositories != nil {
		return f.repositories(ctx, principal, orgID)
	}
	return spa.RepositoriesContext{}, nil
}

func (f fakeContextService) UserCandidates(ctx context.Context, principal serverauth.Principal, orgID uuid.UUID, purpose spa.CandidatePurpose, match spa.CandidateMatch, query string, limit int) (spa.UserCandidates, error) {
	if f.candidates != nil {
		return f.candidates(ctx, principal, orgID, purpose, match, query, limit)
	}
	return spa.UserCandidates{}, nil
}

type fakeTakeover struct {
	exchange func(context.Context, string, string, string, string) (session.Created, error)
}

func (f fakeTakeover) Exchange(ctx context.Context, token, requestID, userAgent, remoteAddress string) (session.Created, error) {
	if f.exchange == nil {
		return session.Created{}, nil
	}
	return f.exchange(ctx, token, requestID, userAgent, remoteAddress)
}

type fakeCookies struct{}

func (fakeCookies) Cookie(token string) *http.Cookie {
	return &http.Cookie{Name: session.DefaultCookieName, Value: token, Path: "/", HttpOnly: true}
}

func (fakeCookies) CSRFCookie(token string) *http.Cookie {
	return &http.Cookie{Name: session.DefaultCSRFCookieName, Value: token, Path: "/"}
}

func testRouteSet(t *testing.T, service ContextService, takeover SessionTakeover, cookies SessionCookies) routeset.RouteSet {
	t.Helper()
	authenticate := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal := serverauth.Principal{User: serverauth.User{ID: uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")}, Kind: serverauth.CredentialSession}
			next.ServeHTTP(w, r.WithContext(serverauth.WithPrincipal(r.Context(), principal)))
		})
	}
	set, err := NewRouteSet(Dependencies{Service: service, Takeover: takeover, Sessions: cookies,
		Authenticate: authenticate, AllowedOrigins: map[string]struct{}{"https://issues.example.test": {}}})
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func routeHandler(t *testing.T, set routeset.RouteSet, name string) http.Handler {
	t.Helper()
	for _, route := range set.Routes {
		if route.Name == name {
			return route.Handler
		}
	}
	t.Fatalf("route %q not found", name)
	return nil
}
