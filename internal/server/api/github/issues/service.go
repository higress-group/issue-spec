package issues

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/google/uuid"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/higress-group/issue-spec/internal/server/projection/artifacts"
	"github.com/higress-group/issue-spec/internal/server/store"
	"github.com/jackc/pgx/v5"
)

type RepositoryAuthorizer interface {
	EvaluateRepository(context.Context, authz.Subject, authz.RepositoryRequest) (authz.Decision, error)
	EvaluateRepositoryTx(context.Context, pgx.Tx, authz.Subject, authz.RepositoryRequest) (authz.Decision, error)
}

// MutationEvent is the immutable transactional handoff to PROCESS-007. RawBody
// and BodyHash are authoritative; parsed marker or command data is deliberately
// absent.
type MutationEvent struct {
	Key                   string
	Type                  string
	Scope                 models.RepoScope
	Issue                 models.Issue
	Comment               *models.CommentSnapshot
	RawBody               string
	BodyHash              [32]byte
	ActorUserID           uuid.UUID
	ActorCredentialKind   serverauth.CredentialKind
	RepresentationVersion int64
}

type MutationEventHook interface {
	Emit(context.Context, store.RepoStore, MutationEvent) error
}

type Service struct {
	store      *store.Store
	authorizer RepositoryAuthorizer
	projector  artifacts.Projector
	events     MutationEventHook
}

func NewService(database *store.Store, authorizer RepositoryAuthorizer, projector artifacts.Projector, events MutationEventHook) (*Service, error) {
	if database == nil || authorizer == nil || projector == nil || events == nil {
		return nil, errors.New("github issues: store, authorizer, projector and mutation event hook are required")
	}
	return &Service{store: database, authorizer: authorizer, projector: projector, events: events}, nil
}

type DecisionError struct{ Decision authz.Decision }

func (e *DecisionError) Error() string { return "github issues: authorization denied" }

func (s *Service) resolveRead(ctx context.Context, owner, repository string, subject authz.Subject) (models.RepositoryResource, error) {
	resource, err := s.store.ResolveRepository(ctx, owner, repository)
	if err != nil {
		return models.RepositoryResource{}, err
	}
	decision, err := s.authorizer.EvaluateRepository(ctx, subject, authz.RepositoryRequest{
		Scope: resource.Scope, Operation: authz.OperationRead,
	})
	if err != nil {
		return models.RepositoryResource{}, err
	}
	if !decision.Allowed {
		return models.RepositoryResource{}, &DecisionError{Decision: decision}
	}
	return resource, nil
}

func (s *Service) ListIssues(ctx context.Context, owner, repository string, subject authz.Subject, options models.IssueListOptions) (models.RepositoryResource, models.IssuePage, error) {
	resource, err := s.resolveRead(ctx, owner, repository, subject)
	if err != nil {
		return models.RepositoryResource{}, models.IssuePage{}, err
	}
	var page models.IssuePage
	err = s.store.WithTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx *store.Tx) error {
		var err error
		page, err = tx.ScopedRepo(resource.Scope).ListIssues(ctx, options)
		return err
	})
	return resource, page, err
}

func (s *Service) GetIssue(ctx context.Context, owner, repository string, number int64, subject authz.Subject) (models.RepositoryResource, models.IssueSnapshot, error) {
	resource, err := s.resolveRead(ctx, owner, repository, subject)
	if err != nil {
		return models.RepositoryResource{}, models.IssueSnapshot{}, err
	}
	var item models.IssueSnapshot
	err = s.store.WithTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx *store.Tx) error {
		var err error
		item, err = tx.ScopedRepo(resource.Scope).IssueSnapshotByNumber(ctx, number)
		return err
	})
	return resource, item, err
}

func (s *Service) CreateIssue(ctx context.Context, owner, repository string, subject authz.Subject, input models.NewIssue) (models.RepositoryResource, models.IssueSnapshot, error) {
	actor, ok := authenticatedActor(subject)
	if !ok {
		return models.RepositoryResource{}, models.IssueSnapshot{}, &DecisionError{Decision: authz.Decision{Exists: true, Visible: true}}
	}
	input.AuthorID = &actor.User.ID
	var resource models.RepositoryResource
	var snapshot models.IssueSnapshot
	err := s.store.WithinTx(ctx, func(tx *store.Tx) error {
		var err error
		resource, err = tx.ResolveRepository(ctx, owner, repository)
		if err != nil {
			return err
		}
		decision, err := s.authorizer.EvaluateRepositoryTx(ctx, tx.PGX(), subject, authz.RepositoryRequest{
			Scope: resource.Scope, Operation: authz.OperationContribute,
		})
		if err != nil {
			return err
		}
		if !decision.Allowed {
			return &DecisionError{Decision: decision}
		}
		if len(input.Labels) > 0 {
			decision, err = s.authorizer.EvaluateRepositoryTx(ctx, tx.PGX(), subject, authz.RepositoryRequest{
				Scope: resource.Scope, Operation: authz.OperationTriage,
			})
			if err != nil {
				return err
			}
			if !decision.Allowed {
				return &DecisionError{Decision: decision}
			}
		}
		repositoryStore := tx.ScopedRepo(resource.Scope)
		issue, err := repositoryStore.CreateIssue(ctx, input)
		if err != nil {
			return err
		}
		if _, err := repositoryStore.IncrementCollectionVersions(ctx, store.RepoCollectionIssues); err != nil {
			return err
		}
		if err := s.projector.ProjectIssue(ctx, repositoryStore, issue); err != nil {
			return err
		}
		if err := s.events.Emit(ctx, repositoryStore, MutationEvent{Key: mutationKey(issue.ID, issue.RepresentationVersion, "issue.created"), Type: "issue.created", Scope: resource.Scope,
			Issue: issue, RawBody: issue.Body, BodyHash: sha256.Sum256([]byte(issue.Body)),
			ActorUserID: actor.User.ID, ActorCredentialKind: actor.Kind, RepresentationVersion: issue.RepresentationVersion}); err != nil {
			return err
		}
		snapshot, err = repositoryStore.IssueSnapshotByNumber(ctx, issue.Number)
		return err
	})
	return resource, snapshot, err
}

func (s *Service) UpdateIssue(ctx context.Context, owner, repository string, number int64, subject authz.Subject, overlay func(models.Issue) (models.IssueUpdate, error)) (models.RepositoryResource, models.IssueSnapshot, error) {
	actor, ok := authenticatedActor(subject)
	if !ok {
		return models.RepositoryResource{}, models.IssueSnapshot{}, &DecisionError{Decision: authz.Decision{Exists: true, Visible: true}}
	}
	var resource models.RepositoryResource
	var snapshot models.IssueSnapshot
	err := s.store.WithinTx(ctx, func(tx *store.Tx) error {
		var err error
		resource, err = tx.ResolveRepository(ctx, owner, repository)
		if err != nil {
			return err
		}
		decision, err := s.authorizer.EvaluateRepositoryTx(ctx, tx.PGX(), subject, authz.RepositoryRequest{
			Scope: resource.Scope, Operation: authz.OperationTriage,
		})
		if err != nil {
			return err
		}
		if !decision.Allowed {
			return &DecisionError{Decision: decision}
		}
		repositoryStore := tx.ScopedRepo(resource.Scope)
		current, err := repositoryStore.IssueByNumber(ctx, number)
		if err != nil {
			return err
		}
		update, err := overlay(current)
		if err != nil {
			return err
		}
		updated, err := repositoryStore.UpdateIssueCAS(ctx, number, current.RepresentationVersion, update)
		if err != nil {
			return err
		}
		if _, err := repositoryStore.IncrementCollectionVersions(ctx, store.RepoCollectionIssues); err != nil {
			return err
		}
		if err := s.projector.ProjectIssue(ctx, repositoryStore, updated); err != nil {
			return err
		}
		action := "issue.edited"
		if current.State != updated.State {
			if updated.State == models.IssueStateOpen {
				action = "issue.reopened"
			} else {
				action = "issue.closed"
			}
		}
		if err := s.events.Emit(ctx, repositoryStore, MutationEvent{Key: mutationKey(updated.ID, updated.RepresentationVersion, action), Type: action, Scope: resource.Scope,
			Issue: updated, RawBody: updated.Body, BodyHash: sha256.Sum256([]byte(updated.Body)),
			ActorUserID: actor.User.ID, ActorCredentialKind: actor.Kind, RepresentationVersion: updated.RepresentationVersion}); err != nil {
			return err
		}
		snapshot, err = repositoryStore.IssueSnapshotByNumber(ctx, number)
		return err
	})
	return resource, snapshot, err
}

func (s *Service) ListComments(ctx context.Context, owner, repository string, subject authz.Subject, options models.CommentListOptions) (models.RepositoryResource, models.CommentPage, error) {
	resource, err := s.resolveRead(ctx, owner, repository, subject)
	if err != nil {
		return models.RepositoryResource{}, models.CommentPage{}, err
	}
	var page models.CommentPage
	err = s.store.WithTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx *store.Tx) error {
		repositoryStore := tx.ScopedRepo(resource.Scope)
		var err error
		page, err = repositoryStore.ListComments(ctx, options)
		if err != nil {
			return err
		}
		return repositoryStore.PopulateCommentReactionSummaries(ctx, page.Items)
	})
	return resource, page, err
}

func (s *Service) GetComment(ctx context.Context, owner, repository string, compatibilityID int64, subject authz.Subject) (models.RepositoryResource, models.CommentSnapshot, error) {
	resource, err := s.resolveRead(ctx, owner, repository, subject)
	if err != nil {
		return models.RepositoryResource{}, models.CommentSnapshot{}, err
	}
	var comment models.CommentSnapshot
	err = s.store.WithTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx *store.Tx) error {
		repositoryStore := tx.ScopedRepo(resource.Scope)
		var err error
		comment, err = repositoryStore.CommentByCompatibilityID(ctx, compatibilityID)
		if err != nil {
			return err
		}
		items := []models.CommentSnapshot{comment}
		if err := repositoryStore.PopulateCommentReactionSummaries(ctx, items); err != nil {
			return err
		}
		comment = items[0]
		return nil
	})
	return resource, comment, err
}

func (s *Service) CreateComment(ctx context.Context, owner, repository string, issueNumber int64, subject authz.Subject, body string) (models.RepositoryResource, models.CommentSnapshot, error) {
	actor, ok := authenticatedActor(subject)
	if !ok {
		return models.RepositoryResource{}, models.CommentSnapshot{}, &DecisionError{Decision: authz.Decision{Exists: true, Visible: true}}
	}
	var resource models.RepositoryResource
	var snapshot models.CommentSnapshot
	err := s.store.WithinTx(ctx, func(tx *store.Tx) error {
		var err error
		resource, err = tx.ResolveRepository(ctx, owner, repository)
		if err != nil {
			return err
		}
		decision, err := s.authorizer.EvaluateRepositoryTx(ctx, tx.PGX(), subject, authz.RepositoryRequest{
			Scope: resource.Scope, Operation: authz.OperationContribute,
		})
		if err != nil {
			return err
		}
		if !decision.Allowed {
			return &DecisionError{Decision: decision}
		}
		repositoryStore := tx.ScopedRepo(resource.Scope)
		snapshot, err = repositoryStore.CreateComment(ctx, models.NewComment{ID: uuid.New(), IssueNumber: issueNumber, AuthorID: &actor.User.ID, Body: body})
		if err != nil {
			return err
		}
		if err := s.projector.ProjectComment(ctx, repositoryStore, snapshot); err != nil {
			return err
		}
		snapshot.Reactions, err = repositoryStore.ReactionSummary(ctx, snapshot.Comment.ID)
		if err != nil {
			return err
		}
		issue, err := repositoryStore.IssueByNumber(ctx, issueNumber)
		if err != nil {
			return err
		}
		return s.events.Emit(ctx, repositoryStore, MutationEvent{Key: mutationKey(snapshot.Comment.ID, snapshot.Comment.RepresentationVersion, "issue_comment.created"), Type: "issue_comment.created", Scope: resource.Scope,
			Issue:   commentEventIssue(issue),
			Comment: &snapshot, RawBody: body, BodyHash: sha256.Sum256([]byte(body)),
			ActorUserID: actor.User.ID, ActorCredentialKind: actor.Kind, RepresentationVersion: snapshot.Comment.RepresentationVersion})
	})
	return resource, snapshot, err
}

func (s *Service) UpdateComment(ctx context.Context, owner, repository string, compatibilityID int64, subject authz.Subject, body string) (models.RepositoryResource, models.CommentSnapshot, error) {
	return s.updateComment(ctx, owner, repository, compatibilityID, subject, body, nil)
}

// UpdateCommentConditional applies the caller-observed representation version
// to the store CAS. A stale version fails before projection and event emission.
func (s *Service) UpdateCommentConditional(ctx context.Context, owner, repository string, compatibilityID, expected int64, subject authz.Subject, body string) (models.RepositoryResource, models.CommentSnapshot, error) {
	if expected <= 0 {
		return models.RepositoryResource{}, models.CommentSnapshot{}, store.ErrInvalidInput
	}
	return s.updateComment(ctx, owner, repository, compatibilityID, subject, body, &expected)
}

func (s *Service) updateComment(ctx context.Context, owner, repository string, compatibilityID int64, subject authz.Subject, body string, expected *int64) (models.RepositoryResource, models.CommentSnapshot, error) {
	actor, ok := authenticatedActor(subject)
	if !ok {
		return models.RepositoryResource{}, models.CommentSnapshot{}, &DecisionError{Decision: authz.Decision{Exists: true, Visible: true}}
	}
	var resource models.RepositoryResource
	var snapshot models.CommentSnapshot
	err := s.store.WithinTx(ctx, func(tx *store.Tx) error {
		var err error
		resource, err = tx.ResolveRepository(ctx, owner, repository)
		if err != nil {
			return err
		}
		repositoryStore := tx.ScopedRepo(resource.Scope)
		current, err := repositoryStore.CommentByCompatibilityID(ctx, compatibilityID)
		if err != nil {
			return err
		}
		operation := authz.OperationTriage
		if current.Comment.AuthorID != nil && *current.Comment.AuthorID == actor.User.ID {
			operation = authz.OperationContribute
		}
		decision, err := s.authorizer.EvaluateRepositoryTx(ctx, tx.PGX(), subject, authz.RepositoryRequest{
			Scope: resource.Scope, Operation: operation,
		})
		if err != nil {
			return err
		}
		if !decision.Allowed {
			return &DecisionError{Decision: decision}
		}
		expectedVersion := current.Comment.RepresentationVersion
		if expected != nil {
			expectedVersion = *expected
		}
		snapshot, err = repositoryStore.UpdateCommentCAS(ctx, compatibilityID, expectedVersion, body)
		if err != nil {
			return err
		}
		if err := s.projector.ProjectComment(ctx, repositoryStore, snapshot); err != nil {
			return err
		}
		snapshot.Reactions, err = repositoryStore.ReactionSummary(ctx, snapshot.Comment.ID)
		if err != nil {
			return err
		}
		issue, err := repositoryStore.IssueByNumber(ctx, snapshot.IssueNumber)
		if err != nil {
			return err
		}
		return s.events.Emit(ctx, repositoryStore, MutationEvent{Key: mutationKey(snapshot.Comment.ID, snapshot.Comment.RepresentationVersion, "issue_comment.edited"), Type: "issue_comment.edited", Scope: resource.Scope,
			Issue:   commentEventIssue(issue),
			Comment: &snapshot, RawBody: body, BodyHash: sha256.Sum256([]byte(body)),
			ActorUserID: actor.User.ID, ActorCredentialKind: actor.Kind, RepresentationVersion: snapshot.Comment.RepresentationVersion})
	})
	return resource, snapshot, err
}

// commentEventIssue keeps the issue identity and timestamps from the
// transaction-authoritative row while deliberately omitting its representation
// version. A comment mutation advances the issue's comment collection and
// updated timestamp, not the issue representation itself.
func commentEventIssue(issue models.Issue) models.Issue {
	return models.Issue{
		ID:        issue.ID,
		Scope:     issue.Scope,
		Number:    issue.Number,
		CreatedAt: issue.CreatedAt,
		UpdatedAt: issue.UpdatedAt,
	}
}

func authenticatedActor(subject authz.Subject) (serverauth.Principal, bool) {
	if subject.Principal == nil || subject.Principal.User.ID == uuid.Nil {
		return serverauth.Principal{}, false
	}
	return *subject.Principal, true
}

func mutationKey(id uuid.UUID, version int64, action string) string {
	return fmt.Sprintf("%s:v%d:%s", id, version, action)
}

func IsDecisionError(err error) (*DecisionError, bool) {
	var target *DecisionError
	ok := errors.As(err, &target)
	return target, ok
}
