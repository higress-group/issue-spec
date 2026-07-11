// Package admin provides shared fail-closed native administration HTTP support.
package admin

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"
	adminservice "github.com/higress-group/issue-spec/internal/server/admin"
	apierrors "github.com/higress-group/issue-spec/internal/server/api/github/errors"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
)

type Authenticate func(http.Handler) http.Handler

type Guard struct {
	authorizer   adminservice.Authorizer
	authenticate Authenticate
}

func NewGuard(authorizer adminservice.Authorizer, authenticate Authenticate) (Guard, error) {
	if authorizer == nil || authenticate == nil {
		return Guard{}, errors.New("native admin: authorizer and authentication middleware are required")
	}
	return Guard{authorizer: authorizer, authenticate: authenticate}, nil
}

func (g Guard) Protect(request func(*http.Request) (adminservice.AuthorizationRequest, error), next http.Handler) http.Handler {
	protected := g.authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := serverauth.PrincipalFromContext(r.Context())
		if !ok || principal.User.ID == uuid.Nil {
			WriteProblem(w, http.StatusUnauthorized, "authentication_required", "Authentication required")
			return
		}
		authorization, err := request(r)
		if err != nil {
			WriteProblem(w, http.StatusUnprocessableEntity, "invalid_request", "Invalid request")
			return
		}
		if err := g.authorizer.Authorize(r.Context(), principal, authorization); err != nil {
			WriteProblem(w, http.StatusForbidden, "forbidden", "Forbidden")
			return
		}
		next.ServeHTTP(w, r)
	}))
	return WithRequestID(protected)
}

func Principal(r *http.Request) serverauth.Principal {
	principal, _ := serverauth.PrincipalFromContext(r.Context())
	return principal
}

func Actor(r *http.Request) adminservice.Actor {
	return adminservice.ActorFromPrincipal(Principal(r), RequestID(r))
}

func RequestID(r *http.Request) string {
	if value := strings.TrimSpace(r.Header.Get("X-Request-ID")); validRequestID(value) {
		return value
	}
	return uuid.NewString()
}

// WithRequestID normalizes request identity before authentication,
// authorization, mutation and response handling so every layer observes the
// same value even when the client omitted or supplied an unsafe identifier.
func WithRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := RequestID(r)
		r.Header.Set("X-Request-ID", requestID)
		w.Header().Set("X-Request-ID", requestID)
		next.ServeHTTP(w, r)
	})
}

func validRequestID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || strings.ContainsRune("-_.:", char) {
			continue
		}
		return false
	}
	return true
}

func ParsePathUUID(r *http.Request, name string) (uuid.UUID, error) {
	return uuid.Parse(r.PathValue(name))
}

func DecodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
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

func WriteJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func WriteProblem(w http.ResponseWriter, status int, code, message string) {
	requestID := strings.TrimSpace(w.Header().Get("X-Request-ID"))
	if requestID == "" {
		requestID = uuid.NewString()
	}
	apierrors.WriteProblem(w, apierrors.NewProblem(status, code, message, "", requestID))
}

func WriteServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, adminservice.ErrNotFound):
		WriteProblem(w, http.StatusNotFound, "not_found", "Resource not found")
	case errors.Is(err, adminservice.ErrForbidden), errors.Is(err, adminservice.ErrLastOrganizationOwner):
		WriteProblem(w, http.StatusForbidden, "forbidden", "Forbidden")
	case errors.Is(err, adminservice.ErrVersionConflict):
		WriteProblem(w, http.StatusConflict, "version_conflict", "Resource version conflict")
	case errors.Is(err, adminservice.ErrConflict), errors.Is(err, adminservice.ErrBootstrapCompleted):
		WriteProblem(w, http.StatusConflict, "conflict", "Resource conflict")
	case errors.Is(err, adminservice.ErrInvalidBootstrapSecret):
		WriteProblem(w, http.StatusUnauthorized, "invalid_bootstrap_secret", "Bootstrap claim rejected")
	case errors.Is(err, adminservice.ErrInvalidInput):
		WriteProblem(w, http.StatusUnprocessableEntity, "invalid_request", "Invalid request")
	default:
		WriteProblem(w, http.StatusInternalServerError, "internal_error", "Request failed")
	}
}
