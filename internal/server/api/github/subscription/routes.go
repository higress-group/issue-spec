// Package subscription exposes the authenticated repository-subscription read.
package subscription

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/api/github/codec"
	"github.com/higress-group/issue-spec/internal/server/api/github/conditional"
	"github.com/higress-group/issue-spec/internal/server/api/github/issues"
	"github.com/higress-group/issue-spec/internal/server/api/github/pagination"
	"github.com/higress-group/issue-spec/internal/server/api/routeset"
	"github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/higress-group/issue-spec/internal/server/store"
	"github.com/jackc/pgx/v5"
)

type Service struct {
	database   *store.Store
	authorizer issues.RepositoryAuthorizer
}

func NewService(database *store.Store, authorizer issues.RepositoryAuthorizer) (*Service, error) {
	if database == nil || authorizer == nil {
		return nil, errors.New("github subscription: store and authorizer are required")
	}
	return &Service{database: database, authorizer: authorizer}, nil
}

func (s *Service) Get(ctx context.Context, owner, repository string, subject authz.Subject) (models.RepositoryResource, models.RepositorySubscription, error) {
	actor, ok := actorID(subject)
	if !ok {
		return models.RepositoryResource{}, models.RepositorySubscription{}, &issues.DecisionError{Decision: authz.Decision{Exists: true, Visible: true}}
	}
	resource, err := s.database.ResolveRepository(ctx, owner, repository)
	if err != nil {
		return models.RepositoryResource{}, models.RepositorySubscription{}, err
	}
	decision, err := s.authorizer.EvaluateRepository(ctx, subject, authz.RepositoryRequest{Scope: resource.Scope, Operation: authz.OperationRead})
	if err != nil {
		return models.RepositoryResource{}, models.RepositorySubscription{}, err
	}
	if !decision.Allowed {
		return models.RepositoryResource{}, models.RepositorySubscription{}, &issues.DecisionError{Decision: decision}
	}
	var result models.RepositorySubscription
	err = s.database.WithTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx *store.Tx) error {
		var err error
		result, err = tx.ScopedRepo(resource.Scope).RepositorySubscription(ctx, actor)
		return err
	})
	return resource, result, err
}

func actorID(subject authz.Subject) (uuid.UUID, bool) {
	if subject.Principal == nil || subject.Principal.User.ID == uuid.Nil {
		return uuid.Nil, false
	}
	return subject.Principal.User.ID, true
}

type Dependencies struct {
	Service        *Service
	Presenter      codec.Presenter
	Authentication auth.Middleware
	Conditional    conditional.Policy
}

func NewRouteSet(deps Dependencies) (routeset.RouteSet, error) {
	if deps.Service == nil {
		return routeset.RouteSet{}, errors.New("github subscription: service is required")
	}
	authentication := issues.ConfigureCompatibilityAuthentication(deps.Authentication)
	h := handler{service: deps.Service, presenter: deps.Presenter, conditional: deps.Conditional}
	set := routeset.RouteSet{Name: "github-subscription", Routes: []routeset.Route{{
		Name: "github.repository.subscription", Method: http.MethodGet, Pattern: "/repos/{owner}/{repo}/subscription",
		Handler: issues.WithRequestID(authentication.Authenticate(http.HandlerFunc(h.get))),
	}}}
	return set, set.Validate()
}

type handler struct {
	service     *Service
	presenter   codec.Presenter
	conditional conditional.Policy
}

func (h handler) get(w http.ResponseWriter, r *http.Request) {
	resource, item, err := h.service.Get(r.Context(), r.PathValue("owner"), r.PathValue("repo"), issues.Subject(r))
	if err != nil {
		issues.WriteError(w, r, err)
		return
	}
	etag := pagination.StrongETag("repository-subscription", resource.Scope.OrgID, resource.Scope.RepoID,
		item.UserID, item.CollectionVersion, item.RepresentationVersion,
		item.Subscribed, item.Ignored, item.Reason)
	if pagination.WriteNotModified(w, r, etag, item.UpdatedAt, h.conditional.Rate()) {
		return
	}
	repoPath := "/repos/" + url.PathEscape(resource.Owner) + "/" + url.PathEscape(resource.Name)
	issues.WriteJSON(w, http.StatusOK, codec.Subscription{Subscribed: item.Subscribed, Ignored: item.Ignored,
		Reason: item.Reason, CreatedAt: item.CreatedAt, URL: h.presenter.Origins.API.MustURL(repoPath + "/subscription"),
		RepositoryURL: h.presenter.Origins.API.MustURL(repoPath)})
}
