// Package webhooks exposes native tenant-scoped webhook subscription APIs.
package webhooks

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	apierrors "github.com/higress-group/issue-spec/internal/server/api/github/errors"
	adminapi "github.com/higress-group/issue-spec/internal/server/api/native/admin"
	"github.com/higress-group/issue-spec/internal/server/api/routeset"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/events/subscriptions"
)

type Dependencies struct {
	Service      *subscriptions.Service
	Authenticate adminapi.Authenticate
}

func NewRouteSet(deps Dependencies) (routeset.RouteSet, error) {
	if deps.Service == nil || deps.Authenticate == nil {
		return routeset.RouteSet{}, errors.New("native webhooks: service and authentication are required")
	}
	h := handlers{service: deps.Service}
	protect := func(next http.HandlerFunc) http.Handler {
		return adminapi.WithRequestID(deps.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			principal, ok := serverauth.PrincipalFromContext(r.Context())
			if !ok || principal.User.ID == uuid.Nil {
				adminapi.WriteProblem(w, http.StatusUnauthorized, "authentication_required", "Authentication required")
				return
			}
			next(w, r)
		})))
	}
	verifyRunner := func(next http.HandlerFunc) http.Handler {
		return adminapi.WithRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			next(w, r)
		}))
	}
	set := routeset.RouteSet{Name: "native-webhooks", Routes: []routeset.Route{
		{Name: "native.webhooks.list", Method: http.MethodGet, Pattern: "/api/v1/orgs/{org}/webhooks", Handler: protect(h.list)},
		{Name: "native.webhooks.create", Method: http.MethodPost, Pattern: "/api/v1/orgs/{org}/webhooks", Handler: protect(h.create)},
		{Name: "native.webhooks.get", Method: http.MethodGet, Pattern: "/api/v1/orgs/{org}/webhooks/{webhook}", Handler: protect(h.get)},
		{Name: "native.webhooks.update", Method: http.MethodPatch, Pattern: "/api/v1/orgs/{org}/webhooks/{webhook}", Handler: protect(h.update)},
		{Name: "native.webhooks.revoke", Method: http.MethodDelete, Pattern: "/api/v1/orgs/{org}/webhooks/{webhook}", Handler: protect(h.revoke)},
		{Name: "native.webhooks.rotate_secret", Method: http.MethodPost, Pattern: "/api/v1/orgs/{org}/webhooks/{webhook}/rotate-secret", Handler: protect(h.rotate)},
		{Name: "native.webhooks.suppressions", Method: http.MethodGet, Pattern: "/api/v1/orgs/{org}/webhooks/{webhook}/suppressions", Handler: protect(h.suppressions)},
		{Name: "native.webhooks.verify_runner", Method: http.MethodPost, Pattern: "/api/v1/orgs/{org}/webhooks/{webhook}/runner-verification", Handler: verifyRunner(h.verifyRunner)},
	}}
	return set, set.Validate()
}

type handlers struct{ service *subscriptions.Service }

type retryRequest struct {
	MaxAttempts    *int    `json:"max_attempts"`
	InitialBackoff *string `json:"initial_backoff"`
	MaxBackoff     *string `json:"max_backoff"`
}

type createRequest struct {
	RepositoryID   *uuid.UUID                   `json:"repository_id"`
	URL            string                       `json:"url"`
	EventTypes     []string                     `json:"event_types"`
	DeliveryFormat subscriptions.DeliveryFormat `json:"delivery_format"`
	SigningMode    subscriptions.SigningMode    `json:"signing_mode"`
	ContentPolicy  subscriptions.ContentPolicy  `json:"content_policy"`
	Retry          retryRequest                 `json:"retry"`
}

type updateRequest struct {
	ExpectedVersion       int64                        `json:"expected_version"`
	URL                   string                       `json:"url"`
	Active                bool                         `json:"active"`
	EventTypes            []string                     `json:"event_types"`
	DeliveryFormat        subscriptions.DeliveryFormat `json:"delivery_format"`
	SigningMode           subscriptions.SigningMode    `json:"signing_mode"`
	ContentPolicy         subscriptions.ContentPolicy  `json:"content_policy"`
	ClearDestinationQuery bool                         `json:"clear_destination_query"`
	Retry                 retryRequest                 `json:"retry"`
}

func (h handlers) create(w http.ResponseWriter, r *http.Request) {
	orgID, ok := pathUUID(w, r, "org")
	if !ok {
		return
	}
	var request createRequest
	if adminapi.DecodeJSON(w, r, &request) != nil {
		adminapi.WriteProblem(w, http.StatusBadRequest, "invalid_json", "Invalid request body")
		return
	}
	retry, err := parseRetry(request.Retry)
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := h.service.Create(r.Context(), actor(r), subject(r), subscriptions.CreateInput{
		OrganizationID: orgID, RepositoryID: request.RepositoryID, URL: request.URL,
		EventTypes: request.EventTypes, DeliveryFormat: request.DeliveryFormat,
		SigningMode: request.SigningMode, ContentPolicy: request.ContentPolicy, Retry: retry,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	adminapi.WriteJSON(w, http.StatusCreated, secretView(result))
}

func (h handlers) list(w http.ResponseWriter, r *http.Request) {
	orgID, ok := pathUUID(w, r, "org")
	if !ok {
		return
	}
	var repoID *uuid.UUID
	if raw := r.URL.Query().Get("repository_id"); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			adminapi.WriteProblem(w, http.StatusUnprocessableEntity, "invalid_request", "Invalid repository id")
			return
		}
		repoID = &parsed
	}
	items, err := h.service.List(r.Context(), subject(r), orgID, repoID)
	if err != nil {
		writeError(w, err)
		return
	}
	views := make([]any, len(items))
	for index := range items {
		views[index] = subscriptionView(items[index])
	}
	adminapi.WriteJSON(w, http.StatusOK, map[string]any{"subscriptions": views})
}

func (h handlers) get(w http.ResponseWriter, r *http.Request) {
	orgID, id, ok := pathIDs(w, r)
	if !ok {
		return
	}
	item, err := h.service.Get(r.Context(), subject(r), orgID, id)
	if err != nil {
		writeError(w, err)
		return
	}
	adminapi.WriteJSON(w, http.StatusOK, subscriptionView(item))
}

func (h handlers) suppressions(w http.ResponseWriter, r *http.Request) {
	orgID, id, ok := pathIDs(w, r)
	if !ok {
		return
	}
	items, err := h.service.ListSuppressions(r.Context(), subject(r), orgID, id)
	if err != nil {
		writeError(w, err)
		return
	}
	adminapi.WriteJSON(w, http.StatusOK, map[string]any{"suppressions": items})
}

func (h handlers) update(w http.ResponseWriter, r *http.Request) {
	orgID, id, ok := pathIDs(w, r)
	if !ok {
		return
	}
	var request updateRequest
	if adminapi.DecodeJSON(w, r, &request) != nil {
		adminapi.WriteProblem(w, http.StatusBadRequest, "invalid_json", "Invalid request body")
		return
	}
	retry, err := parseRetry(request.Retry)
	if err != nil {
		writeError(w, err)
		return
	}
	item, err := h.service.Update(r.Context(), actor(r), subject(r), orgID, id, subscriptions.UpdateInput{
		ExpectedVersion: request.ExpectedVersion, URL: request.URL, Active: request.Active,
		EventTypes: request.EventTypes, DeliveryFormat: request.DeliveryFormat,
		SigningMode: request.SigningMode, ContentPolicy: request.ContentPolicy,
		ClearDestinationQuery: request.ClearDestinationQuery, Retry: retry,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	adminapi.WriteJSON(w, http.StatusOK, subscriptionView(item))
}

func (h handlers) rotate(w http.ResponseWriter, r *http.Request) {
	orgID, id, ok := pathIDs(w, r)
	if !ok {
		return
	}
	result, err := h.service.RotateSecret(r.Context(), actor(r), subject(r), orgID, id)
	if err != nil {
		writeError(w, err)
		return
	}
	adminapi.WriteJSON(w, http.StatusCreated, secretView(result))
}

func (h handlers) verifyRunner(w http.ResponseWriter, r *http.Request) {
	orgID, id, ok := pathIDs(w, r)
	if !ok {
		return
	}
	credential := runnerBearerCredential(r.Header.Values("Authorization"))
	item, err := h.service.VerifyRunnerCredential(r.Context(), orgID, id, credential, time.Now().UTC())
	clear(credential)
	if err != nil {
		writeError(w, err)
		return
	}
	adminapi.WriteJSON(w, http.StatusOK, runnerVerificationView(item))
}

func (h handlers) revoke(w http.ResponseWriter, r *http.Request) {
	orgID, id, ok := pathIDs(w, r)
	if !ok {
		return
	}
	if err := h.service.Revoke(r.Context(), actor(r), subject(r), orgID, id); err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func parseRetry(request retryRequest) (subscriptions.RetryPolicy, error) {
	var policy subscriptions.RetryPolicy
	if request.MaxAttempts != nil {
		policy.MaxAttempts = *request.MaxAttempts
		if policy.MaxAttempts < 1 || policy.MaxAttempts > 100 {
			return policy, &subscriptions.ValidationError{Reason: subscriptions.ValidationInvalidRetryPolicy,
				Field: subscriptions.ValidationFieldRetryMaxAttempts}
		}
	}
	var err error
	if request.InitialBackoff != nil {
		policy.InitialBackoff, err = time.ParseDuration(*request.InitialBackoff)
		if err != nil || policy.InitialBackoff <= 0 {
			return policy, &subscriptions.ValidationError{Reason: subscriptions.ValidationInvalidRetryPolicy,
				Field: subscriptions.ValidationFieldRetryInitialBackoff}
		}
	}
	if request.MaxBackoff != nil {
		policy.MaxBackoff, err = time.ParseDuration(*request.MaxBackoff)
		if err != nil || policy.MaxBackoff <= 0 {
			return policy, &subscriptions.ValidationError{Reason: subscriptions.ValidationInvalidRetryPolicy,
				Field: subscriptions.ValidationFieldRetryMaxBackoff}
		}
	}
	return policy, nil
}

func subscriptionView(item subscriptions.Subscription) map[string]any {
	return map[string]any{"id": item.ID, "organization_id": item.OrganizationID,
		"repository_id": item.RepositoryID, "scope_type": item.ScopeType, "url": item.URL,
		"active": item.Active, "revoked_at": item.RevokedAt, "event_types": item.EventTypes,
		"delivery_format": item.DeliveryFormat, "signing_mode": item.SigningMode,
		"content_policy": item.ContentPolicy, "has_destination_query": item.HasDestinationQuery,
		"retry": map[string]any{"max_attempts": item.Retry.MaxAttempts,
			"initial_backoff": item.Retry.InitialBackoff.String(), "max_backoff": item.Retry.MaxBackoff.String()},
		"representation_version": item.RepresentationVersion, "created_at": item.CreatedAt, "updated_at": item.UpdatedAt}
}

func runnerVerificationView(item subscriptions.Subscription) map[string]any {
	return map[string]any{"id": item.ID, "organization_id": item.OrganizationID,
		"repository_id": item.RepositoryID, "scope_type": item.ScopeType, "active": item.Active,
		"revoked_at": item.RevokedAt, "event_types": item.EventTypes,
		"delivery_format": item.DeliveryFormat, "signing_mode": item.SigningMode}
}

func runnerBearerCredential(values []string) []byte {
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") || strings.Count(values[0], " ") != 1 {
		return nil
	}
	value := strings.TrimPrefix(values[0], "Bearer ")
	if value == "" || len(value) > 64<<10 {
		return nil
	}
	return []byte(value)
}

func secretView(result subscriptions.SecretResult) map[string]any {
	view := subscriptionView(result.Subscription)
	view["secret"] = result.Secret
	view["secret_version"] = result.SecretVersion
	return view
}

func subject(r *http.Request) authz.Subject {
	principal, _ := serverauth.PrincipalFromContext(r.Context())
	return authz.Authenticated(principal)
}

func actor(r *http.Request) subscriptions.Actor {
	principal, _ := serverauth.PrincipalFromContext(r.Context())
	return subscriptions.ActorFromPrincipal(principal, adminapi.RequestID(r))
}

func pathUUID(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue(name))
	if err != nil {
		adminapi.WriteProblem(w, http.StatusUnprocessableEntity, "invalid_request", "Invalid resource id")
		return uuid.Nil, false
	}
	return id, true
}

func pathIDs(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	orgID, ok := pathUUID(w, r, "org")
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}
	id, ok := pathUUID(w, r, "webhook")
	return orgID, id, ok
}

func writeError(w http.ResponseWriter, err error) {
	var validation *subscriptions.ValidationError
	switch {
	case errors.As(err, &validation):
		problem := apierrors.NewProblem(http.StatusUnprocessableEntity, string(validation.Reason),
			"Invalid webhook subscription", "", w.Header().Get("X-Request-ID"))
		problem.Meta = map[string]any{"field": validation.Field}
		apierrors.WriteProblem(w, problem)
	case errors.Is(err, subscriptions.ErrInvalidInput):
		adminapi.WriteProblem(w, http.StatusUnprocessableEntity, "invalid_request", "Invalid webhook subscription")
	case errors.Is(err, subscriptions.ErrNotFound):
		adminapi.WriteProblem(w, http.StatusNotFound, "not_found", "Resource not found")
	case errors.Is(err, subscriptions.ErrForbidden):
		adminapi.WriteProblem(w, http.StatusForbidden, "forbidden", "Forbidden")
	case errors.Is(err, subscriptions.ErrVersionConflict):
		adminapi.WriteProblem(w, http.StatusConflict, "version_conflict", "Resource version conflict")
	case errors.Is(err, subscriptions.ErrRevoked):
		adminapi.WriteProblem(w, http.StatusConflict, "webhook_revoked", "Webhook subscription is revoked")
	default:
		adminapi.WriteProblem(w, http.StatusInternalServerError, "internal_error", "Request failed")
	}
}
