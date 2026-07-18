// Package profilemail exposes the private notification-email lifecycle plus a
// public, token-only confirmation action. Account mutations require a browser
// session; confirmation possession is its only credential.
package profilemail

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	adminapi "github.com/higress-group/issue-spec/internal/server/api/native/admin"
	"github.com/higress-group/issue-spec/internal/server/api/routeset"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	mailservice "github.com/higress-group/issue-spec/internal/server/profilemail"
)

type Service interface {
	Get(context.Context, uuid.UUID) (mailservice.Profile, error)
	Set(context.Context, mailservice.SetInput) (mailservice.Verification, error)
	Onboard(context.Context, mailservice.OnboardingInput) (mailservice.Verification, error)
	Resend(context.Context, mailservice.ResendInput) (mailservice.Verification, error)
	Confirm(context.Context, string) (mailservice.Confirmed, error)
	Remove(context.Context, mailservice.RemoveInput) error
}

type Dependencies struct {
	Service      Service
	Authenticate adminapi.Authenticate
	Enabled      bool
}

func NewRouteSet(deps Dependencies) (routeset.RouteSet, error) {
	if deps.Authenticate == nil || (deps.Enabled && deps.Service == nil) {
		return routeset.RouteSet{}, errors.New("native profile mail: authentication and enabled service are required")
	}
	h := handlers{deps: deps}
	protected := func(handler http.Handler) http.Handler {
		return adminapi.WithRequestID(deps.Authenticate(handler))
	}
	public := func(handler http.Handler) http.Handler {
		return adminapi.WithRequestID(handler)
	}
	set := routeset.RouteSet{Name: "native-profile-mail", Routes: []routeset.Route{
		{Name: "native.profile_mail.get", Method: http.MethodGet, Pattern: "/api/v1/profile/email", Handler: protected(http.HandlerFunc(h.get))},
		{Name: "native.profile_mail.set", Method: http.MethodPut, Pattern: "/api/v1/profile/email", Handler: protected(http.HandlerFunc(h.set))},
		{Name: "native.profile_mail.onboard", Method: http.MethodPost, Pattern: "/api/v1/profile/onboarding", Handler: protected(http.HandlerFunc(h.onboard))},
		{Name: "native.profile_mail.remove", Method: http.MethodDelete, Pattern: "/api/v1/profile/email", Handler: protected(http.HandlerFunc(h.remove))},
		{Name: "native.profile_mail.confirm", Method: http.MethodPost, Pattern: "/api/v1/profile/email/verification", Handler: public(http.HandlerFunc(h.confirm))},
		{Name: "native.profile_mail.resend", Method: http.MethodPost, Pattern: "/api/v1/profile/email/verification/resend", Handler: protected(http.HandlerFunc(h.resend))},
	}}
	return set, set.Validate()
}

func (h handlers) onboard(w http.ResponseWriter, r *http.Request) {
	principal, ok := h.browserPrincipal(w, r)
	if !ok || !h.available(w) {
		return
	}
	var request struct {
		PreferredName   string `json:"name"`
		Email           string `json:"email"`
		ExpectedVersion int64  `json:"expected_version"`
	}
	if err := adminapi.DecodeJSON(w, r, &request); err != nil {
		adminapi.WriteProblem(w, http.StatusBadRequest, "invalid_json", "Invalid request body")
		return
	}
	verification, err := h.deps.Service.Onboard(r.Context(), mailservice.OnboardingInput{
		UserID: principal.User.ID, PreferredName: request.PreferredName, Email: request.Email,
		ExpectedUserVersion: request.ExpectedVersion,
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	adminapi.WriteJSON(w, http.StatusAccepted, verificationResponse(verification))
}

type handlers struct{ deps Dependencies }

func (h handlers) get(w http.ResponseWriter, r *http.Request) {
	if !h.available(w) {
		return
	}
	profile, err := h.deps.Service.Get(r.Context(), adminapi.Principal(r).User.ID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	adminapi.WriteJSON(w, http.StatusOK, statusResponse(profile))
}

func (h handlers) set(w http.ResponseWriter, r *http.Request) {
	principal, ok := h.browserPrincipal(w, r)
	if !ok || !h.available(w) {
		return
	}
	var request struct {
		Email           string `json:"email"`
		ExpectedVersion int64  `json:"expected_version"`
	}
	if err := adminapi.DecodeJSON(w, r, &request); err != nil {
		adminapi.WriteProblem(w, http.StatusBadRequest, "invalid_json", "Invalid request body")
		return
	}
	verification, err := h.deps.Service.Set(r.Context(), mailservice.SetInput{UserID: principal.User.ID,
		Email: request.Email, ExpectedUserVersion: request.ExpectedVersion})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	adminapi.WriteJSON(w, http.StatusAccepted, verificationResponse(verification))
}

func (h handlers) resend(w http.ResponseWriter, r *http.Request) {
	principal, ok := h.browserPrincipal(w, r)
	if !ok || !h.available(w) {
		return
	}
	var request struct {
		ExpectedVersion             int64 `json:"expected_version"`
		ExpectedVerificationVersion int64 `json:"expected_verification_version"`
	}
	if err := adminapi.DecodeJSON(w, r, &request); err != nil {
		adminapi.WriteProblem(w, http.StatusBadRequest, "invalid_json", "Invalid request body")
		return
	}
	verification, err := h.deps.Service.Resend(r.Context(), mailservice.ResendInput{UserID: principal.User.ID,
		ExpectedUserVersion: request.ExpectedVersion, ExpectedVerificationVersion: request.ExpectedVerificationVersion})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	adminapi.WriteJSON(w, http.StatusAccepted, verificationResponse(verification))
}

func (h handlers) confirm(w http.ResponseWriter, r *http.Request) {
	if !h.available(w) {
		return
	}
	var request struct {
		Token string `json:"token"`
	}
	if err := adminapi.DecodeJSON(w, r, &request); err != nil || strings.TrimSpace(request.Token) == "" || len(request.Token) > 512 {
		writeConfirmationError(w, mailservice.ErrInvalid)
		return
	}
	_, err := h.deps.Service.Confirm(r.Context(), request.Token)
	if err != nil {
		writeConfirmationError(w, err)
		return
	}
	adminapi.WriteJSON(w, http.StatusOK, map[string]any{"status": "confirmed"})
}

func (h handlers) remove(w http.ResponseWriter, r *http.Request) {
	principal, ok := h.browserPrincipal(w, r)
	if !ok || !h.available(w) {
		return
	}
	var request struct {
		ExpectedVersion int64 `json:"expected_version"`
	}
	if err := adminapi.DecodeJSON(w, r, &request); err != nil {
		adminapi.WriteProblem(w, http.StatusBadRequest, "invalid_json", "Invalid request body")
		return
	}
	if err := h.deps.Service.Remove(r.Context(), mailservice.RemoveInput{UserID: principal.User.ID,
		ExpectedUserVersion: request.ExpectedVersion}); err != nil {
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h handlers) available(w http.ResponseWriter) bool {
	if !h.deps.Enabled || h.deps.Service == nil {
		adminapi.WriteProblem(w, http.StatusServiceUnavailable, "email_unavailable", "Notification email is not configured")
		return false
	}
	return true
}

func (h handlers) browserPrincipal(w http.ResponseWriter, r *http.Request) (serverauth.Principal, bool) {
	principal := adminapi.Principal(r)
	if principal.Kind != serverauth.CredentialSession {
		adminapi.WriteProblem(w, http.StatusForbidden, "browser_session_required", "Browser session required")
		return serverauth.Principal{}, false
	}
	return principal, true
}

func statusResponse(profile mailservice.Profile) map[string]any {
	response := map[string]any{"available": true, "onboarding_completed": profile.OnboardingCompletedAt != nil,
		"notification_email": profile.NotificationEmail, "notification_email_verified_at": profile.NotificationVerifiedAt,
		"representation_version": profile.RepresentationVersion}
	if profile.Pending != nil {
		response["pending"] = verificationResponse(*profile.Pending)
	} else {
		response["pending"] = nil
	}
	return response
}

func verificationResponse(verification mailservice.Verification) map[string]any {
	return map[string]any{"id": verification.ID, "email": verification.PendingEmail,
		"expires_at": verification.ExpiresAt, "sent_at": verification.SentAt,
		"representation_version": verification.RepresentationVersion}
}

func writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, mailservice.ErrInvalid):
		adminapi.WriteProblem(w, http.StatusUnprocessableEntity, "invalid_request", "Invalid request")
	case errors.Is(err, mailservice.ErrNotFound):
		adminapi.WriteProblem(w, http.StatusNotFound, "verification_not_found", "Verification link is invalid")
	case errors.Is(err, mailservice.ErrConflict):
		adminapi.WriteProblem(w, http.StatusConflict, "version_conflict", "Profile changed; reload and try again")
	case errors.Is(err, mailservice.ErrEmailInUse):
		adminapi.WriteProblem(w, http.StatusConflict, "email_in_use", "Notification email is already in use")
	case errors.Is(err, mailservice.ErrExpired):
		adminapi.WriteProblem(w, http.StatusGone, "verification_expired", "Verification link has expired")
	case errors.Is(err, mailservice.ErrConsumed):
		adminapi.WriteProblem(w, http.StatusConflict, "verification_consumed", "Verification link was already used")
	case errors.Is(err, mailservice.ErrSuperseded):
		adminapi.WriteProblem(w, http.StatusConflict, "verification_superseded", "Verification link was replaced")
	case errors.Is(err, mailservice.ErrRateLimited):
		w.Header().Set("Retry-After", "60")
		adminapi.WriteProblem(w, http.StatusTooManyRequests, "verification_rate_limited", "Please wait before requesting another message")
	case errors.Is(err, mailservice.ErrAccountDisabled):
		adminapi.WriteProblem(w, http.StatusForbidden, "account_disabled", "Account is disabled")
	default:
		adminapi.WriteProblem(w, http.StatusServiceUnavailable, "email_unavailable", "Notification email is temporarily unavailable")
	}
}

func writeConfirmationError(w http.ResponseWriter, err error) {
	if errors.Is(err, mailservice.ErrUnavailable) {
		adminapi.WriteProblem(w, http.StatusServiceUnavailable, "email_unavailable", "Notification email is temporarily unavailable")
		return
	}
	// Token state, recipient identity, account state, address conflicts and
	// concurrent consumption deliberately collapse to one public response.
	adminapi.WriteProblem(w, http.StatusBadRequest, "invalid_verification", "Verification link is invalid")
}
