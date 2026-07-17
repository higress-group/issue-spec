// Package reposubscriptions exposes both the compatible repository
// subscription route family and the UUID-native route consumed by the SPA.
package reposubscriptions

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/api/github/codec"
	"github.com/higress-group/issue-spec/internal/server/api/github/conditional"
	apierrors "github.com/higress-group/issue-spec/internal/server/api/github/errors"
	"github.com/higress-group/issue-spec/internal/server/api/github/issues"
	"github.com/higress-group/issue-spec/internal/server/api/github/pagination"
	adminapi "github.com/higress-group/issue-spec/internal/server/api/native/admin"
	"github.com/higress-group/issue-spec/internal/server/api/routeset"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/higress-group/issue-spec/internal/server/publicurl"
	notifications "github.com/higress-group/issue-spec/internal/server/reponotifications"
	"github.com/higress-group/issue-spec/internal/server/store"
)

type Service interface {
	GetByName(context.Context, string, string, authz.Subject) (models.RepositoryResource, models.RepositorySubscription, error)
	GetByScope(context.Context, models.RepoScope, authz.Subject) (models.RepositoryResource, models.RepositorySubscription, error)
	SetByName(context.Context, string, string, authz.Subject, bool) (models.RepositoryResource, models.RepositorySubscription, bool, error)
	SetByScope(context.Context, models.RepoScope, authz.Subject, bool) (models.RepositoryResource, models.RepositorySubscription, bool, error)
}

type Dependencies struct {
	Service        Service
	Origins        publicurl.Origins
	Authentication serverauth.Middleware
	Conditional    conditional.Policy
}

func NewRouteSet(deps Dependencies) (routeset.RouteSet, error) {
	if deps.Service == nil {
		return routeset.RouteSet{}, errors.New("repository subscriptions: service is required")
	}
	h := handlers{service: deps.Service, origins: deps.Origins, conditional: deps.Conditional}
	compatibility := issues.ConfigureCompatibilityAuthentication(deps.Authentication)
	compat := func(handler http.Handler) http.Handler {
		return issues.WithRequestID(compatibility.Authenticate(handler))
	}
	nativeAuthentication := adminapi.NativeAuthenticate(deps.Authentication)
	native := func(handler http.Handler) http.Handler {
		return adminapi.WithRequestID(nativeAuthentication(handler))
	}
	set := routeset.RouteSet{Name: "repository-email-subscriptions", Routes: []routeset.Route{
		{Name: "github.repository.subscription.get", Method: http.MethodGet, Pattern: "/repos/{owner}/{repo}/subscription", Handler: compat(http.HandlerFunc(h.compatGet))},
		{Name: "github.repository.subscription.put", Method: http.MethodPut, Pattern: "/repos/{owner}/{repo}/subscription", Handler: compat(http.HandlerFunc(h.compatPut))},
		{Name: "github.repository.subscription.delete", Method: http.MethodDelete, Pattern: "/repos/{owner}/{repo}/subscription", Handler: compat(http.HandlerFunc(h.compatDelete))},
		{Name: "native.repository.subscription.get", Method: http.MethodGet, Pattern: "/api/v1/orgs/{org}/repos/{repo}/subscription", Handler: native(http.HandlerFunc(h.nativeGet))},
		{Name: "native.repository.subscription.put", Method: http.MethodPut, Pattern: "/api/v1/orgs/{org}/repos/{repo}/subscription", Handler: native(http.HandlerFunc(h.nativePut))},
		{Name: "native.repository.subscription.delete", Method: http.MethodDelete, Pattern: "/api/v1/orgs/{org}/repos/{repo}/subscription", Handler: native(http.HandlerFunc(h.nativeDelete))},
	}}
	return set, set.Validate()
}

type handlers struct {
	service     Service
	origins     publicurl.Origins
	conditional conditional.Policy
}

func (h handlers) compatGet(w http.ResponseWriter, r *http.Request) {
	resource, item, err := h.service.GetByName(r.Context(), r.PathValue("owner"), r.PathValue("repo"), issues.Subject(r))
	if err != nil {
		h.writeCompatError(w, r, err)
		return
	}
	h.writeCompatible(w, r, resource, item, true)
}

func (h handlers) compatPut(w http.ResponseWriter, r *http.Request) {
	resource, item, _, err := h.service.SetByName(r.Context(), r.PathValue("owner"), r.PathValue("repo"), issues.Subject(r), true)
	if err != nil {
		h.writeCompatError(w, r, err)
		return
	}
	h.writeCompatible(w, r, resource, item, false)
}

func (h handlers) compatDelete(w http.ResponseWriter, r *http.Request) {
	_, _, _, err := h.service.SetByName(r.Context(), r.PathValue("owner"), r.PathValue("repo"), issues.Subject(r), false)
	if err != nil {
		h.writeCompatError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h handlers) nativeGet(w http.ResponseWriter, r *http.Request) {
	scope, ok := nativeScope(w, r)
	if !ok {
		return
	}
	resource, item, err := h.service.GetByScope(r.Context(), scope, issues.Subject(r))
	if err != nil {
		writeNativeError(w, r, err)
		return
	}
	h.writeNative(w, r, resource.Scope, item, true)
}

func (h handlers) nativePut(w http.ResponseWriter, r *http.Request) {
	scope, ok := nativeScope(w, r)
	if !ok {
		return
	}
	resource, item, _, err := h.service.SetByScope(r.Context(), scope, issues.Subject(r), true)
	if err != nil {
		writeNativeError(w, r, err)
		return
	}
	h.writeNative(w, r, resource.Scope, item, false)
}

func (h handlers) nativeDelete(w http.ResponseWriter, r *http.Request) {
	scope, ok := nativeScope(w, r)
	if !ok {
		return
	}
	if _, _, _, err := h.service.SetByScope(r.Context(), scope, issues.Subject(r), false); err != nil {
		writeNativeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h handlers) writeCompatible(w http.ResponseWriter, r *http.Request, resource models.RepositoryResource, item models.RepositorySubscription, conditionalGET bool) {
	etag := subscriptionETag(resource.Scope, item)
	if conditionalGET && pagination.WriteNotModified(w, r, etag, item.UpdatedAt, h.conditional.Rate()) {
		return
	}
	repoPath := "/repos/" + url.PathEscape(resource.Owner) + "/" + url.PathEscape(resource.Name)
	issues.WriteJSON(w, http.StatusOK, codec.Subscription{Subscribed: item.Subscribed, Ignored: item.Ignored,
		Reason: item.Reason, CreatedAt: item.CreatedAt, URL: h.origins.API.MustURL(repoPath + "/subscription"),
		RepositoryURL: h.origins.API.MustURL(repoPath)})
}

func (h handlers) writeNative(w http.ResponseWriter, r *http.Request, scope models.RepoScope, item models.RepositorySubscription, conditionalGET bool) {
	etag := subscriptionETag(scope, item)
	if conditionalGET && pagination.WriteNotModified(w, r, etag, item.UpdatedAt, h.conditional.Rate()) {
		return
	}
	response := map[string]any{"subscribed": item.Subscribed,
		"ignored": item.Ignored, "reason": item.Reason, "representation_version": item.RepresentationVersion,
		"collection_version": item.CollectionVersion}
	if !item.CreatedAt.IsZero() {
		response["created_at"] = item.CreatedAt
	}
	adminapi.WriteJSON(w, http.StatusOK, response)
}

func subscriptionETag(scope models.RepoScope, item models.RepositorySubscription) string {
	return pagination.StrongETag("repository-subscription", scope.OrgID, scope.RepoID, item.UserID,
		item.CollectionVersion, item.RepresentationVersion, item.Subscribed, item.Ignored, item.Reason)
}

func nativeScope(w http.ResponseWriter, r *http.Request) (models.RepoScope, bool) {
	orgID, err := uuid.Parse(r.PathValue("org"))
	if err != nil {
		adminapi.WriteProblem(w, http.StatusUnprocessableEntity, "invalid_request", "Invalid repository scope")
		return models.RepoScope{}, false
	}
	repoID, err := uuid.Parse(r.PathValue("repo"))
	if err != nil {
		adminapi.WriteProblem(w, http.StatusUnprocessableEntity, "invalid_request", "Invalid repository scope")
		return models.RepoScope{}, false
	}
	return models.RepoScope{OrgID: orgID, RepoID: repoID}, true
}

func (h handlers) writeCompatError(w http.ResponseWriter, r *http.Request, err error) {
	var decision *issues.DecisionError
	switch {
	case errors.As(err, &decision), errors.Is(err, store.ErrNotFound):
		issues.WriteError(w, r, err)
	case errors.Is(err, notifications.ErrRecipientMissing):
		apierrors.WriteGitHub(w, apierrors.Validation(issues.RequestID(r), []codec.Violation{{Resource: "RepositorySubscription", Field: "notification_email", Code: "missing", Message: "requires a verified notification email"}}))
	case errors.Is(err, notifications.ErrMailDisabled):
		apierrors.WriteGitHub(w, apierrors.GitHubError{Status: http.StatusServiceUnavailable, RequestID: issues.RequestID(r), Envelope: apierrors.Envelope{Message: "Email notifications unavailable", DocumentationURL: "https://docs.github.com/rest"}})
	default:
		issues.WriteError(w, r, err)
	}
}

func writeNativeError(w http.ResponseWriter, r *http.Request, err error) {
	var decision *issues.DecisionError
	switch {
	case errors.As(err, &decision):
		problem, _ := decision.Decision.NativeProblem(issues.RequestID(r))
		apierrors.WriteProblem(w, problem)
	case errors.Is(err, store.ErrNotFound):
		adminapi.WriteProblem(w, http.StatusNotFound, "not_found", "Not found")
	case errors.Is(err, notifications.ErrRecipientMissing):
		adminapi.WriteProblem(w, http.StatusUnprocessableEntity, "notification_email_required", "A verified notification email is required")
	case errors.Is(err, notifications.ErrMailDisabled):
		adminapi.WriteProblem(w, http.StatusServiceUnavailable, "email_unavailable", "Email notifications are unavailable")
	default:
		adminapi.WriteProblem(w, http.StatusServiceUnavailable, "subscription_unavailable", "Repository subscription is temporarily unavailable")
	}
}
