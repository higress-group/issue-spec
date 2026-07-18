// Package reponotifications owns the deliberately small repository email
// subscription, ordinary-issue projection and delivery preparation boundary.
package reponotifications

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/server/api/github/issues"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/higress-group/issue-spec/internal/server/store"
	"github.com/jackc/pgx/v5"
)

var (
	ErrInvalid          = errors.New("repository notifications: invalid input")
	ErrMailDisabled     = errors.New("repository notifications: mail disabled")
	ErrRecipientMissing = errors.New("repository notifications: recipient ineligible")
)

type SubscriptionService struct {
	database   *store.Store
	authorizer issues.RepositoryAuthorizer
	enabled    bool
}

func NewSubscriptionService(database *store.Store, authorizer issues.RepositoryAuthorizer, enabled bool) (*SubscriptionService, error) {
	if database == nil || authorizer == nil {
		return nil, ErrInvalid
	}
	return &SubscriptionService{database: database, authorizer: authorizer, enabled: enabled}, nil
}

func (s *SubscriptionService) GetByName(ctx context.Context, owner, repository string, subject authz.Subject) (models.RepositoryResource, models.RepositorySubscription, error) {
	resource, err := s.database.ResolveRepository(ctx, owner, repository)
	if err != nil {
		return models.RepositoryResource{}, models.RepositorySubscription{}, err
	}
	return s.get(ctx, resource, subject)
}

func (s *SubscriptionService) GetByScope(ctx context.Context, scope models.RepoScope, subject authz.Subject) (models.RepositoryResource, models.RepositorySubscription, error) {
	resource, err := s.database.RepositoryNotificationResource(ctx, scope)
	if err != nil {
		return models.RepositoryResource{}, models.RepositorySubscription{}, err
	}
	return s.get(ctx, resource, subject)
}

func (s *SubscriptionService) get(ctx context.Context, resource models.RepositoryResource, subject authz.Subject) (models.RepositoryResource, models.RepositorySubscription, error) {
	actor, ok := subscriptionActor(subject)
	if !ok {
		return models.RepositoryResource{}, models.RepositorySubscription{}, denied(true, true)
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
		var loadErr error
		result, loadErr = tx.ScopedRepo(resource.Scope).RepositorySubscription(ctx, actor)
		return loadErr
	})
	return resource, result, err
}

func (s *SubscriptionService) SetByName(ctx context.Context, owner, repository string, subject authz.Subject, subscribed bool) (models.RepositoryResource, models.RepositorySubscription, bool, error) {
	return s.set(ctx, subject, subscribed, func(tx *store.Tx) (models.RepositoryResource, error) {
		return tx.ResolveRepository(ctx, owner, repository)
	})
}

func (s *SubscriptionService) SetByScope(ctx context.Context, scope models.RepoScope, subject authz.Subject, subscribed bool) (models.RepositoryResource, models.RepositorySubscription, bool, error) {
	if err := scope.Validate(); err != nil {
		return models.RepositoryResource{}, models.RepositorySubscription{}, false, ErrInvalid
	}
	return s.set(ctx, subject, subscribed, func(tx *store.Tx) (models.RepositoryResource, error) {
		return tx.RepositoryNotificationResource(ctx, scope)
	})
}

func (s *SubscriptionService) set(ctx context.Context, subject authz.Subject, subscribed bool, resolve func(*store.Tx) (models.RepositoryResource, error)) (models.RepositoryResource, models.RepositorySubscription, bool, error) {
	actor, ok := subscriptionActor(subject)
	if !ok {
		return models.RepositoryResource{}, models.RepositorySubscription{}, false, denied(true, true)
	}
	if !s.enabled {
		return models.RepositoryResource{}, models.RepositorySubscription{}, false, ErrMailDisabled
	}
	var resource models.RepositoryResource
	var result models.RepositorySubscription
	var changed bool
	err := s.database.WithinTx(ctx, func(tx *store.Tx) error {
		var err error
		resource, err = resolve(tx)
		if err != nil {
			return err
		}
		decision, err := s.authorizer.EvaluateRepositoryTx(ctx, tx.PGX(), subject,
			authz.RepositoryRequest{Scope: resource.Scope, Operation: authz.OperationRead})
		if err != nil {
			return err
		}
		if !decision.Allowed {
			return &issues.DecisionError{Decision: decision}
		}
		repositoryStore := tx.ScopedRepo(resource.Scope)
		eligible, err := repositoryStore.RepositoryNotificationActorEligible(ctx, actor)
		if err != nil {
			return err
		}
		if !eligible {
			return ErrRecipientMissing
		}
		result, changed, err = repositoryStore.SetManualRepositorySubscription(ctx, actor, subscribed)
		return err
	})
	return resource, result, changed, err
}

func subscriptionActor(subject authz.Subject) (uuid.UUID, bool) {
	if subject.Principal == nil || subject.Principal.User.ID == uuid.Nil {
		return uuid.Nil, false
	}
	return subject.Principal.User.ID, true
}

func denied(exists, visible bool) error {
	return &issues.DecisionError{Decision: authz.Decision{Exists: exists, Visible: visible}}
}
