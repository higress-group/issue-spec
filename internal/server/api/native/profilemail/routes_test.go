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

func TestConfirmationIsPublicTokenOnlyAndHasNoGETSurface(t *testing.T) {
	userID := uuid.New()
	service := &fakeService{profile: mailservice.Profile{UserID: userID}, confirmed: mailservice.Confirmed{UserID: userID, NotificationEmail: "private@example.test",
		VerifiedAt: time.Now(), RepresentationVersion: 3}}
	authenticateCalls := 0
	handler := profileMailHandler(t, service, true, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authenticateCalls++
			adminapi.WriteProblem(w, http.StatusUnauthorized, "authentication_required", "Authentication required")
		})
	})

	request := httptest.NewRequest(http.MethodGet, "/api/v1/profile/email/verification", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed || service.confirmCalls != 0 || authenticateCalls != 0 {
		t.Fatalf("GET = %d confirm=%d authenticate=%d body=%s", response.Code, service.confirmCalls, authenticateCalls, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/profile/email/verification", strings.NewReader(`{"token":"secret-token"}`))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.confirmCalls != 1 || service.confirmToken != "secret-token" || authenticateCalls != 0 {
		t.Fatalf("POST = %d confirm=%d authenticate=%d body=%s", response.Code, service.confirmCalls, authenticateCalls, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "secret-token") || strings.Contains(response.Body.String(), "private@example.test") {
		t.Fatalf("public confirmation exposed private data: %s", response.Body.String())
	}
}

func TestAccountMailMutationsRequireSessionOriginAndCSRF(t *testing.T) {
	userID := uuid.New()
	service := &fakeService{verification: mailservice.Verification{ID: uuid.New(), UserID: userID,
		PendingEmail: "person@example.test", ExpiresAt: time.Now().Add(time.Hour), RepresentationVersion: 1}}
	middleware := serverauth.Middleware{SessionCookieName: "session", AllowedOrigins: map[string]struct{}{"https://web.example.test": {}},
		Sessions: routeSessions{principal: serverauth.Principal{Kind: serverauth.CredentialSession, User: serverauth.User{ID: userID}}},
		Bearer:   routeBearer{}}
	handler := profileMailHandler(t, service, true, adminapi.NativeAuthenticate(middleware))

	request := httptest.NewRequest(http.MethodPut, "/api/v1/profile/email", strings.NewReader(`{"email":"person@example.test","expected_version":1}`))
	request.AddCookie(&http.Cookie{Name: "session", Value: "valid"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || service.setCalls != 0 {
		t.Fatalf("unprotected mutation = %d calls=%d", response.Code, service.setCalls)
	}

	request = httptest.NewRequest(http.MethodPut, "/api/v1/profile/email", strings.NewReader(`{"email":"person@example.test","expected_version":1}`))
	request.AddCookie(&http.Cookie{Name: "session", Value: "valid"})
	request.Header.Set("Origin", "https://web.example.test")
	request.Header.Set("X-CSRF-Token", "valid-csrf")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || service.setCalls != 1 {
		t.Fatalf("protected mutation = %d calls=%d body=%s", response.Code, service.setCalls, response.Body.String())
	}
}

func TestPublicConfirmationErrorsAreGenericAndNonEnumerating(t *testing.T) {
	tokenErrors := []error{mailservice.ErrInvalid, mailservice.ErrNotFound, mailservice.ErrConflict,
		mailservice.ErrEmailInUse, mailservice.ErrExpired, mailservice.ErrConsumed,
		mailservice.ErrSuperseded, mailservice.ErrAccountDisabled}
	var firstBody string
	for _, serviceErr := range tokenErrors {
		t.Run(serviceErr.Error(), func(t *testing.T) {
			service := &fakeService{confirmErr: serviceErr}
			handler := profileMailHandler(t, service, true, withPrincipal(serverauth.Principal{}))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/profile/email/verification",
				strings.NewReader(`{"token":"secret-token"}`)))
			assertProblem(t, response, http.StatusBadRequest, "invalid_verification")
			if strings.Contains(response.Body.String(), "secret-token") || strings.Contains(response.Body.String(), serviceErr.Error()) {
				t.Fatalf("public error leaked state: %s", response.Body.String())
			}
			if firstBody == "" {
				firstBody = response.Body.String()
			} else {
				var first, current map[string]any
				if err := json.Unmarshal([]byte(firstBody), &first); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(response.Body.Bytes(), &current); err != nil {
					t.Fatal(err)
				}
				delete(first, "request_id")
				delete(current, "request_id")
				if strings.TrimSpace(response.Body.String()) == "" || !mapsEqual(first, current) {
					t.Fatalf("non-generic response: first=%v current=%v", first, current)
				}
			}
		})
	}
}

func TestDisabledCapabilityIsStable(t *testing.T) {
	userID := uuid.New()
	authenticate := withPrincipal(serverauth.Principal{Kind: serverauth.CredentialSession, User: serverauth.User{ID: userID}})
	handler := profileMailHandler(t, nil, false, authenticate)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/profile/email", nil))
	assertProblem(t, response, http.StatusServiceUnavailable, "email_unavailable")

	service := &fakeService{confirmErr: mailservice.ErrUnavailable}
	handler = profileMailHandler(t, service, true, authenticate)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/profile/email/verification", strings.NewReader(`{"token":"secret-token"}`)))
	assertProblem(t, response, http.StatusServiceUnavailable, "email_unavailable")
}

func TestSetOnboardResendAndRemoveBindAuthenticatedUserAndVersions(t *testing.T) {
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

	request = httptest.NewRequest(http.MethodPost, "/api/v1/profile/onboarding", strings.NewReader(`{"name":"Preferred Person","email":"notify@example.test","expected_version":4}`))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || service.onboardingInput.UserID != userID || service.onboardingInput.PreferredName != "Preferred Person" ||
		service.onboardingInput.Email != "notify@example.test" || service.onboardingInput.ExpectedUserVersion != 4 {
		t.Fatalf("onboard = %d input=%+v body=%s", response.Code, service.onboardingInput, response.Body.String())
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
	profile         mailservice.Profile
	confirmed       mailservice.Confirmed
	verification    mailservice.Verification
	confirmErr      error
	confirmCalls    int
	confirmToken    string
	setCalls        int
	setInput        mailservice.SetInput
	onboardingInput mailservice.OnboardingInput
	resendInput     mailservice.ResendInput
	removeInput     mailservice.RemoveInput
}

func (s *fakeService) Get(context.Context, uuid.UUID) (mailservice.Profile, error) {
	return s.profile, nil
}
func (s *fakeService) Set(_ context.Context, input mailservice.SetInput) (mailservice.Verification, error) {
	s.setCalls++
	s.setInput = input
	return s.verification, nil
}
func (s *fakeService) Onboard(_ context.Context, input mailservice.OnboardingInput) (mailservice.Verification, error) {
	s.onboardingInput = input
	return s.verification, nil
}
func (s *fakeService) Resend(_ context.Context, input mailservice.ResendInput) (mailservice.Verification, error) {
	s.resendInput = input
	return s.verification, nil
}
func (s *fakeService) Confirm(_ context.Context, token string) (mailservice.Confirmed, error) {
	s.confirmCalls++
	s.confirmToken = token
	return s.confirmed, s.confirmErr
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

func mapsEqual(left, right map[string]any) bool {
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return string(leftJSON) == string(rightJSON)
}
