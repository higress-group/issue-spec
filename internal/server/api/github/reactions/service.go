package reactions

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
		return nil, errors.New("github reactions: store and authorizer are required")
	}
	return &Service{database: database, authorizer: authorizer}, nil
}

func (s *Service) List(ctx context.Context, owner, repository string, commentID int64, subject authz.Subject, page, perPage int) (models.RepositoryResource, models.ReactionPage, error) {
	resource, err := s.database.ResolveRepository(ctx, owner, repository)
	if err != nil {
		return models.RepositoryResource{}, models.ReactionPage{}, err
	}
	decision, err := s.authorizer.EvaluateRepository(ctx, subject, authz.RepositoryRequest{Scope: resource.Scope, Operation: authz.OperationRead})
	if err != nil {
		return models.RepositoryResource{}, models.ReactionPage{}, err
	}
	if !decision.Allowed {
		return models.RepositoryResource{}, models.ReactionPage{}, &issues.DecisionError{Decision: decision}
	}
	var result models.ReactionPage
	err = s.database.WithTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx *store.Tx) error {
		var err error
		result, err = tx.ScopedRepo(resource.Scope).ListCommentReactions(ctx, commentID, page, perPage)
		return err
	})
	return resource, result, err
}

func (s *Service) Create(ctx context.Context, owner, repository string, commentID int64, subject authz.Subject, content string) (models.RepositoryResource, models.ReactionMutation, error) {
	actor, ok := actorID(subject)
	if !ok {
		return models.RepositoryResource{}, models.ReactionMutation{}, unauthenticated()
	}
	var resource models.RepositoryResource
	var mutation models.ReactionMutation
	err := s.database.WithinTx(ctx, func(tx *store.Tx) error {
		var err error
		resource, err = tx.ResolveRepository(ctx, owner, repository)
		if err != nil {
			return err
		}
		if err := s.authorizeTx(ctx, tx, subject, resource.Scope, authz.OperationContribute); err != nil {
			return err
		}
		mutation, err = tx.ScopedRepo(resource.Scope).AddCommentReaction(ctx, commentID, actor, content)
		return err
	})
	return resource, mutation, err
}

func (s *Service) Delete(ctx context.Context, owner, repository string, commentID, reactionID int64, subject authz.Subject) (models.RepositoryResource, models.CommentSnapshot, error) {
	actor, ok := actorID(subject)
	if !ok {
		return models.RepositoryResource{}, models.CommentSnapshot{}, unauthenticated()
	}
	var resource models.RepositoryResource
	var comment models.CommentSnapshot
	err := s.database.WithinTx(ctx, func(tx *store.Tx) error {
		var err error
		resource, err = tx.ResolveRepository(ctx, owner, repository)
		if err != nil {
			return err
		}
		// Authorize repository visibility and credential caps before reading the
		// ownership-bearing reaction row.
		if err := s.authorizeTx(ctx, tx, subject, resource.Scope, authz.OperationRead); err != nil {
			return err
		}
		repositoryStore := tx.ScopedRepo(resource.Scope)
		parent, err := repositoryStore.CommentByCompatibilityID(ctx, commentID)
		if err != nil {
			return err
		}
		reaction, err := repositoryStore.ReactionByCompatibilityID(ctx, reactionID, true)
		if err != nil {
			return err
		}
		if reaction.CommentID != parent.Comment.ID {
			return store.ErrNotFound
		}
		operation := authz.OperationTriage
		if reaction.UserID != nil && *reaction.UserID == actor {
			operation = authz.OperationContribute
		}
		if err := s.authorizeTx(ctx, tx, subject, resource.Scope, operation); err != nil {
			return err
		}
		comment, err = repositoryStore.DeleteCommentReaction(ctx, reactionID)
		return err
	})
	return resource, comment, err
}

func (s *Service) authorizeTx(ctx context.Context, tx *store.Tx, subject authz.Subject, scope models.RepoScope, operation authz.Operation) error {
	decision, err := s.authorizer.EvaluateRepositoryTx(ctx, tx.PGX(), subject, authz.RepositoryRequest{Scope: scope, Operation: operation})
	if err != nil {
		return err
	}
	if !decision.Allowed {
		return &issues.DecisionError{Decision: decision}
	}
	return nil
}

func actorID(subject authz.Subject) (uuid.UUID, bool) {
	if subject.Principal == nil || subject.Principal.User.ID == uuid.Nil {
		return uuid.Nil, false
	}
	return subject.Principal.User.ID, true
}

func unauthenticated() error {
	return &issues.DecisionError{Decision: authz.Decision{Exists: true, Visible: true}}
}
