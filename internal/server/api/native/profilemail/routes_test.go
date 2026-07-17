package profilemail

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	adminapi "github.com/higress-group/issue-spec/internal/server/api/native/admin"
	"github.com/higress-group/issue-spec/internal/server/api/routeset"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	mailservice "github.com/higress-group/issue-spec/internal/server/profilemail"
)

func TestConfirmationGETIsReadOnlyAndDoesNotEchoSecrets(t *testing.T) {
	userID := uuid.New()
	service := &fakeService{profile: mailservice.Profile{UserID: userID}, confirmation: mailservice.Confirmation{
		RequestID: uuid.New(), ExpiresAt: time.Now().Add(time.Hour), RepresentationVersion: 1,
	}, confirmed: mailservice.Confirmed{UserID: userID, NotificationEmail: "private@example.test",
		VerifiedAt: time.Now(), RepresentationVersion: 3}}
	handler := profileMailHandler(t, service, true, withPrincipal(serverauth.Principal{
		Kind: serverauth.CredentialSession, User: serverauth.User{ID: userID},
	}))

	request := httptest.NewRequest(http.MethodGet, "/api/v1/profile/email/verification?token=secret-token", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.inspectCalls != 1 || service.confirmCalls != 0 {
		t.Fatalf("GET = %d inspect=%d confirm=%d body=%s", response.Code, service.inspectCalls, service.confirmCalls, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "secret-token") || strings.Contains(response.Body.String(), "private@example.test") {
		t.Fatalf("GET echoed private confirmation data: %s", response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/profile/email/verification", strings.NewReader(`{"token":"secret-token"}`))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.confirmCalls != 1 {
		t.Fatalf("POST = %d confirm=%d body=%s", response.Code, service.confirmCalls, response.Body.String())
	}
}

func TestProfileMailMutationsRequireSessionOriginAndCSRF(t *testing.T) {
	userID := uuid.New()
	service := &fakeService{confirmed: mailservice.Confirmed{UserID: userID, NotificationEmail: "person@example.test",
		VerifiedAt: time.Now(), RepresentationVersion: 2}}
	middleware := serverauth.Middleware{SessionCookieName: "session", AllowedOrigins: map[string]struct{}{"https://web.example.test": {}},
		Sessions: routeSessions{principal: serverauth.Principal{Kind: serverauth.CredentialSession, User: serverauth.User{ID: userID}}},
		Bearer:   routeBearer{}}
	handler := profileMailHandler(t, service, true, adminapi.NativeAuthenticate(middleware))

	request := httptest.NewRequest(http.MethodPost, "/api/v1/profile/email/verification", strings.NewReader(`{"token":"secret-token"}`))
	request.AddCookie(&http.Cookie{Name: "session", Value: "valid"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || service.confirmCalls != 0 {
		t.Fatalf("unprotected mutation = %d calls=%d", response.Code, service.confirmCalls)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/profile/email/verification", strings.NewReader(`{"token":"secret-token"}`))
	request.AddCookie(&http.Cookie{Name: "session", Value: "valid"})
	request.Header.Set("Origin", "https://web.example.test")
	request.Header.Set("X-CSRF-Token", "valid-csrf")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.confirmCalls != 1 {
		t.Fatalf("protected mutation = %d calls=%d body=%s", response.Code, service.confirmCalls, response.Body.String())
	}
}

func TestDisabledCapabilityAndTerminalErrorsAreStable(t *testing.T) {
	userID := uuid.New()
	authenticate := withPrincipal(serverauth.Principal{Kind: serverauth.CredentialSession, User: serverauth.User{ID: userID}})
	handler := profileMailHandler(t, nil, false, authenticate)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/profile/email", nil))
	assertProblem(t, response, http.StatusServiceUnavailable, "email_unavailable")

	service := &fakeService{inspectErr: mailservice.ErrExpired}
	handler = profileMailHandler(t, service, true, authenticate)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/profile/email/verification?token=expired", nil))
	assertProblem(t, response, http.StatusGone, "verification_expired")
	if strings.Contains(response.Body.String(), "expired@example") || strings.Contains(response.Body.String(), "token") {
		t.Fatalf("terminal error leaked private data: %s", response.Body.String())
	}
}

func TestSetResendAndRemoveBindAuthenticatedUserAndVersions(t *testing.T) {
	userID := uuid.New()
	requestID := uuid.New()
	service := &fakeService{verification: mailservice.Verification{ID: requestID, UserID: userID,
		PendingEmail: "notify@example.test", ExpiresAt: time.Now().Add(time.Hour), RepresentationVersion: 1}}
	handler := profileMailHandler(t, service, true, withPrincipal(serverauth.Principal{
		Kind: serverauth.CredentialSession, User: serverauth.User{ID: userID},
	}))

	request := httptest.NewRequest(http.MethodPut, "/api/v1/profile/email", strings.NewReader(`{"email":"notify@example.test","expected_version":4}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || service.setInput.UserID != userID || service.setInput.Email != "notify@example.test" || service.setInput.ExpectedUserVersion != 4 {
		t.Fatalf("set = %d input=%+v body=%s", response.Code, service.setInput, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/profile/email/verification/resend", strings.NewReader(`{"expected_version":5,"expected_verification_version":2}`))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || service.resendInput.UserID != userID || service.resendInput.ExpectedUserVersion != 5 || service.resendInput.ExpectedVerificationVersion != 2 {
		t.Fatalf("resend = %d input=%+v body=%s", response.Code, service.resendInput, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodDelete, "/api/v1/profile/email", strings.NewReader(`{"expected_version":6}`))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || service.removeInput.UserID != userID || service.removeInput.ExpectedUserVersion != 6 {
		t.Fatalf("remove = %d input=%+v body=%s", response.Code, service.removeInput, response.Body.String())
	}
}

func profileMailHandler(t *testing.T, service Service, enabled bool, authenticate adminapi.Authenticate) http.Handler {
	t.Helper()
	set, err := NewRouteSet(Dependencies{Service: service, Enabled: enabled, Authenticate: authenticate})
	if err != nil {
		t.Fatal(err)
	}
	mux, err := routeset.NewMux(routeset.Policy{}, set)
	if err != nil {
		t.Fatal(err)
	}
	return mux
}

func withPrincipal(principal serverauth.Principal) adminapi.Authenticate {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(serverauth.WithPrincipal(r.Context(), principal)))
		})
	}
}

func assertProblem(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != status || body.Code != code {
		t.Fatalf("problem = %d/%q body=%s", response.Code, body.Code, response.Body.String())
	}
}

type fakeService struct {
	profile      mailservice.Profile
	confirmation mailservice.Confirmation
	confirmed    mailservice.Confirmed
	verification mailservice.Verification
	inspectErr   error
	inspectCalls int
	confirmCalls int
	setInput     mailservice.SetInput
	resendInput  mailservice.ResendInput
	removeInput  mailservice.RemoveInput
}

func (s *fakeService) Get(context.Context, uuid.UUID) (mailservice.Profile, error) {
	return s.profile, nil
}
func (s *fakeService) Set(_ context.Context, input mailservice.SetInput) (mailservice.Verification, error) {
	s.setInput = input
	return s.verification, nil
}
func (s *fakeService) Resend(_ context.Context, input mailservice.ResendInput) (mailservice.Verification, error) {
	s.resendInput = input
	return s.verification, nil
}
func (s *fakeService) InspectForUser(context.Context, uuid.UUID, string) (mailservice.Confirmation, error) {
	s.inspectCalls++
	return s.confirmation, s.inspectErr
}
func (s *fakeService) ConfirmForUser(context.Context, uuid.UUID, string) (mailservice.Confirmed, error) {
	s.confirmCalls++
	return s.confirmed, nil
}
func (s *fakeService) Remove(_ context.Context, input mailservice.RemoveInput) error {
	s.removeInput = input
	return nil
}

type routeSessions struct{ principal serverauth.Principal }

func (s routeSessions) Authenticate(context.Context, string) (serverauth.Principal, error) {
	return s.principal, nil
}
func (routeSessions) ValidateCSRF(_ serverauth.Principal, token string) error {
	if token != "valid-csrf" {
		return serverauth.ErrInvalidCSRF
	}
	return nil
}

type routeBearer struct{}

func (routeBearer) AuthenticateBearer(context.Context, string) (serverauth.Principal, error) {
	return serverauth.Principal{}, serverauth.ErrInvalidCredential
}

var _ Service = (*fakeService)(nil)
var _ serverauth.SessionAuthenticator = routeSessions{}
var _ serverauth.BearerAuthenticator = routeBearer{}
