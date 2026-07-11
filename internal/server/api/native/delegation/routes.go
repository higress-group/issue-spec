// Package delegationapi exposes repository-bound runner credential exchange.
package delegationapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	adminapi "github.com/higress-group/issue-spec/internal/server/api/native/admin"
	"github.com/higress-group/issue-spec/internal/server/api/routeset"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/auth/delegation"
	"github.com/higress-group/issue-spec/internal/server/models"
)

const PurposeIssueAPI = "issue-api"

type Service interface {
	Issue(context.Context, delegation.IssueInput) (delegation.Created, error)
	RevokeJobAs(context.Context, serverauth.Principal, models.RepoScope, string, string) error
}

type Dependencies struct {
	Service      Service
	Authenticate adminapi.Authenticate
	Audience     string
	Subject      string
}

func NewRouteSet(deps Dependencies) (routeset.RouteSet, error) {
	if deps.Service == nil || deps.Authenticate == nil || !validBindingValue(deps.Audience) || !validBindingValue(deps.Subject) {
		return routeset.RouteSet{}, errors.New("native delegation: service, authentication, audience, and subject are required")
	}
	h := handlers{service: deps.Service, audience: strings.TrimSpace(deps.Audience), subject: strings.TrimSpace(deps.Subject)}
	protect := func(handler http.HandlerFunc) http.Handler {
		return adminapi.WithRequestID(deps.Authenticate(handler))
	}
	set := routeset.RouteSet{Name: "native-delegation", Routes: []routeset.Route{
		{Name: "native.delegation.exchange", Method: http.MethodPost, Pattern: "/api/v1/orgs/{org}/repos/{repo}/delegated-tokens/exchange", Handler: protect(h.exchange)},
		{Name: "native.delegation.revoke_job", Method: http.MethodDelete, Pattern: "/api/v1/orgs/{org}/repos/{repo}/jobs/{job}/delegated-tokens", Handler: protect(h.revokeJob)},
	}}
	return set, set.Validate()
}

type handlers struct {
	service  Service
	audience string
	subject  string
}

type exchangeRequest struct {
	JobID      string   `json:"job_id"`
	Purpose    string   `json:"purpose"`
	Audience   string   `json:"audience"`
	Subject    string   `json:"subject"`
	Scopes     []string `json:"scopes"`
	TTLSeconds int64    `json:"ttl_seconds"`
	Replace    bool     `json:"replace,omitempty"`
}

func (h handlers) exchange(w http.ResponseWriter, r *http.Request) {
	principal, scope, ok := requestScope(w, r)
	if !ok {
		return
	}
	var request exchangeRequest
	if err := adminapi.DecodeJSON(w, r, &request); err != nil {
		adminapi.WriteProblem(w, http.StatusBadRequest, "invalid_json", "Invalid request body")
		return
	}
	if strings.TrimSpace(request.Purpose) != PurposeIssueAPI || strings.TrimSpace(request.Audience) != h.audience ||
		strings.TrimSpace(request.Subject) != h.subject || !validJobID(request.JobID) ||
		request.TTLSeconds < int64(delegation.MinTTL/time.Second) || request.TTLSeconds > int64(delegation.MaxTTL/time.Second) {
		adminapi.WriteProblem(w, http.StatusUnprocessableEntity, "invalid_request", "Invalid credential exchange")
		return
	}
	created, err := h.service.Issue(r.Context(), delegation.IssueInput{
		Issuer: principal, Repo: scope, JobID: strings.TrimSpace(request.JobID), Purpose: PurposeIssueAPI,
		Audience: h.audience, Subject: h.subject, Scopes: request.Scopes,
		TTL: time.Duration(request.TTLSeconds) * time.Second, RequestID: adminapi.RequestID(r), Replace: request.Replace,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	// Plaintext is deliberately returned once and never represented by list or
	// diagnostic endpoints. WriteJSON also applies Cache-Control: no-store.
	adminapi.WriteJSON(w, http.StatusCreated, created)
}

func (h handlers) revokeJob(w http.ResponseWriter, r *http.Request) {
	principal, scope, ok := requestScope(w, r)
	if !ok {
		return
	}
	jobID := strings.TrimSpace(r.PathValue("job"))
	if !validJobID(jobID) {
		adminapi.WriteProblem(w, http.StatusUnprocessableEntity, "invalid_request", "Invalid job")
		return
	}
	if err := h.service.RevokeJobAs(r.Context(), principal, scope, jobID, adminapi.RequestID(r)); err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func requestScope(w http.ResponseWriter, r *http.Request) (serverauth.Principal, models.RepoScope, bool) {
	principal, ok := serverauth.PrincipalFromContext(r.Context())
	if !ok || principal.User.ID == uuid.Nil {
		adminapi.WriteProblem(w, http.StatusUnauthorized, "authentication_required", "Authentication required")
		return serverauth.Principal{}, models.RepoScope{}, false
	}
	orgID, orgErr := uuid.Parse(r.PathValue("org"))
	repoID, repoErr := uuid.Parse(r.PathValue("repo"))
	if orgErr != nil || repoErr != nil {
		adminapi.WriteProblem(w, http.StatusUnprocessableEntity, "invalid_request", "Invalid repository")
		return serverauth.Principal{}, models.RepoScope{}, false
	}
	return principal, models.RepoScope{OrgID: orgID, RepoID: repoID}, true
}

func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, serverauth.ErrInvalidCredential):
		adminapi.WriteProblem(w, http.StatusUnprocessableEntity, "invalid_request", "Invalid credential exchange")
	case errors.Is(err, serverauth.ErrConflict):
		adminapi.WriteProblem(w, http.StatusConflict, "active_lease_exists", "Active credential lease already exists")
	case errors.Is(err, serverauth.ErrInsufficientScope), errors.Is(err, serverauth.ErrNotFound):
		// Repository cap mismatches and absent resources intentionally share the
		// same concealed envelope.
		adminapi.WriteProblem(w, http.StatusNotFound, "not_found", "Not found")
	case errors.Is(err, serverauth.ErrRevokedCredential), errors.Is(err, serverauth.ErrDisabledAccount), errors.Is(err, serverauth.ErrExpiredCredential):
		adminapi.WriteProblem(w, http.StatusUnauthorized, "credential_rejected", "Credential rejected")
	default:
		adminapi.WriteProblem(w, http.StatusInternalServerError, "internal_error", "Request failed")
	}
}

func validBindingValue(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if char < 0x21 || char == 0x7f {
			return false
		}
	}
	return true
}

func validJobID(value string) bool {
	if !validBindingValue(value) {
		return false
	}
	for _, char := range strings.TrimSpace(value) {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || strings.ContainsRune("-_.:", char) {
			continue
		}
		return false
	}
	return true
}
