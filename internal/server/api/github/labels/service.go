package labels

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

type Service struct {
	database   *store.Store
	authorizer issues.RepositoryAuthorizer
}

func NewService(database *store.Store, authorizer issues.RepositoryAuthorizer) (*Service, error) {
	if database == nil || authorizer == nil {
		return nil, errors.New("github labels: store and authorizer are required")
	}
	return &Service{database: database, authorizer: authorizer}, nil
}

func (s *Service) List(ctx context.Context, owner, repository string, subject authz.Subject, page, perPage int) (models.RepositoryResource, models.LabelPage, error) {
	resource, err := s.resolveRead(ctx, owner, repository, subject)
	if err != nil {
		return models.RepositoryResource{}, models.LabelPage{}, err
	}
	var result models.LabelPage
	err = s.database.WithTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx *store.Tx) error {
		var err error
		result, err = tx.ScopedRepo(resource.Scope).ListLabels(ctx, page, perPage)
		return err
	})
	return resource, result, err
}

func (s *Service) ListIssue(ctx context.Context, owner, repository string, number int64, subject authz.Subject) (models.RepositoryResource, models.Issue, []models.Label, error) {
	resource, err := s.resolveRead(ctx, owner, repository, subject)
	if err != nil {
		return models.RepositoryResource{}, models.Issue{}, nil, err
	}
	var issue models.Issue
	var labels []models.Label
	err = s.database.WithTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx *store.Tx) error {
		var err error
		issue, labels, err = tx.ScopedRepo(resource.Scope).IssueLabels(ctx, number)
		return err
	})
	return resource, issue, labels, err
}

func (s *Service) Create(ctx context.Context, owner, repository string, subject authz.Subject, input models.NewLabel) (models.RepositoryResource, models.Label, error) {
	var resource models.RepositoryResource
	var label models.Label
	err := s.mutate(ctx, owner, repository, subject, authz.OperationWrite, func(repositoryStore store.RepoStore, _ uuid.UUID) error {
		var err error
		label, err = repositoryStore.CreateLabel(ctx, input)
		return err
	}, &resource)
	return resource, label, err
}

func (s *Service) Update(ctx context.Context, owner, repository, currentName string, subject authz.Subject, overlay func(models.Label) models.LabelUpdate) (models.RepositoryResource, models.Label, error) {
	var resource models.RepositoryResource
	var label models.Label
	err := s.mutate(ctx, owner, repository, subject, authz.OperationWrite, func(repositoryStore store.RepoStore, _ uuid.UUID) error {
		current, err := repositoryStore.LabelByName(ctx, currentName)
		if err != nil {
			return err
		}
		update := overlay(current)
		if update.Name == current.Name && update.Color == current.Color && update.Description == current.Description {
			label = current
			return nil
		}
		label, err = repositoryStore.UpdateLabel(ctx, currentName, update)
		return err
	}, &resource)
	return resource, label, err
}

func (s *Service) AddToIssue(ctx context.Context, owner, repository string, number int64, subject authz.Subject, names []string) (models.RepositoryResource, models.Issue, []models.Label, error) {
	return s.changeIssue(ctx, owner, repository, number, subject, names, false)
}

func (s *Service) ReplaceIssue(ctx context.Context, owner, repository string, number int64, subject authz.Subject, names []string) (models.RepositoryResource, models.Issue, []models.Label, error) {
	return s.changeIssue(ctx, owner, repository, number, subject, names, true)
}

func (s *Service) changeIssue(ctx context.Context, owner, repository string, number int64, subject authz.Subject, names []string, replace bool) (models.RepositoryResource, models.Issue, []models.Label, error) {
	var resource models.RepositoryResource
	var issue models.Issue
	var labels []models.Label
	err := s.mutate(ctx, owner, repository, subject, authz.OperationTriage, func(repositoryStore store.RepoStore, actor uuid.UUID) error {
		var err error
		if replace {
			issue, labels, err = repositoryStore.ReplaceIssueLabels(ctx, number, names, actor)
		} else {
			issue, labels, err = repositoryStore.AddIssueLabels(ctx, number, names, actor)
		}
		return err
	}, &resource)
	return resource, issue, labels, err
}

func (s *Service) RemoveFromIssue(ctx context.Context, owner, repository string, number int64, subject authz.Subject, name string) (models.RepositoryResource, models.Issue, []models.Label, error) {
	var resource models.RepositoryResource
	var issue models.Issue
	var labels []models.Label
	err := s.mutate(ctx, owner, repository, subject, authz.OperationTriage, func(repositoryStore store.RepoStore, _ uuid.UUID) error {
		var err error
		issue, labels, err = repositoryStore.RemoveIssueLabel(ctx, number, name)
		return err
	}, &resource)
	return resource, issue, labels, err
}

func (s *Service) resolveRead(ctx context.Context, owner, repository string, subject authz.Subject) (models.RepositoryResource, error) {
	resource, err := s.database.ResolveRepository(ctx, owner, repository)
	if err != nil {
		return models.RepositoryResource{}, err
	}
	decision, err := s.authorizer.EvaluateRepository(ctx, subject, authz.RepositoryRequest{Scope: resource.Scope, Operation: authz.OperationRead})
	if err != nil {
		return models.RepositoryResource{}, err
	}
	if !decision.Allowed {
		return models.RepositoryResource{}, &issues.DecisionError{Decision: decision}
	}
	return resource, nil
}

func (s *Service) mutate(ctx context.Context, owner, repository string, subject authz.Subject, operation authz.Operation, fn func(store.RepoStore, uuid.UUID) error, resource *models.RepositoryResource) error {
	actor, ok := actorID(subject)
	if !ok {
		return &issues.DecisionError{Decision: authz.Decision{Exists: true, Visible: true}}
	}
	return s.database.WithinTx(ctx, func(tx *store.Tx) error {
		var err error
		*resource, err = tx.ResolveRepository(ctx, owner, repository)
		if err != nil {
			return err
		}
		decision, err := s.authorizer.EvaluateRepositoryTx(ctx, tx.PGX(), subject, authz.RepositoryRequest{Scope: resource.Scope, Operation: operation})
		if err != nil {
			return err
		}
		if !decision.Allowed {
			return &issues.DecisionError{Decision: decision}
		}
		return fn(tx.ScopedRepo(resource.Scope), actor)
	})
}

func actorID(subject authz.Subject) (uuid.UUID, bool) {
	if subject.Principal == nil || subject.Principal.User.ID == uuid.Nil {
		return uuid.Nil, false
	}
	return subject.Principal.User.ID, true
}
