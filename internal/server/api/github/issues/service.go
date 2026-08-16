package issues

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/model"
	"github.com/higress-group/issue-spec/internal/preview"
	serverauth "github.com/higress-group/issue-spec/internal/server/auth"
	"github.com/higress-group/issue-spec/internal/server/authz"
	"github.com/higress-group/issue-spec/internal/server/emaildelivery"
	"github.com/higress-group/issue-spec/internal/server/models"
	"github.com/higress-group/issue-spec/internal/server/projection/artifacts"
	"github.com/higress-group/issue-spec/internal/server/projection/mentions"
	"github.com/higress-group/issue-spec/internal/server/store"
	"github.com/higress-group/issue-spec/internal/templates"
	"github.com/jackc/pgx/v5"
)

const proposalLabel = "issue-spec/proposal"

var issueMarker = regexp.MustCompile(`(?s)<!--\s*issue-spec:issue=([^\s>]+)\s+change=([^\s>]+)\s+version=([^\s>]+)\s*-->`)

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
	store         *store.Store
	authorizer    RepositoryAuthorizer
	projector     artifacts.Projector
	events        MutationEventHook
	notifications NotificationIntegration
}

// NotificationIntegration is the deliberately narrow shared mutation wiring.
// A disabled value preserves deployments without SMTP configuration and all
// pre-notification callers. The projectors always receive stores backed by the
// authoritative issue/comment transaction.
type NotificationIntegration struct {
	Enabled       bool
	OrdinaryIssue IssueCreatedNotificationProjector
	Completed     CompletedNotificationProjector
}

// IssueCreatedNotificationProjector keeps the API transaction package free of
// feature-package dependency cycles. Composition adapts the P4 projector to
// this exact transaction-bound call.
type IssueCreatedNotificationProjector interface {
	ProjectIssueCreated(context.Context, store.RepoStore, *emaildelivery.Store,
		models.RepositoryResource, models.Issue, uuid.UUID) error
}

type ChangeLifecycle struct {
	ChangeKey string
	Lifecycle string
}

type CompletedNotificationProjector interface {
	Capture(context.Context, pgx.Tx, models.RepoScope, uuid.UUID) (ChangeLifecycle, error)
	ProjectCompleted(context.Context, store.RepoStore, *emaildelivery.Store, models.RepositoryResource,
		models.Issue, *models.CommentSnapshot, uuid.UUID, ChangeLifecycle, ChangeLifecycle) error
}

func NewService(database *store.Store, authorizer RepositoryAuthorizer, projector artifacts.Projector, events MutationEventHook,
	optional ...NotificationIntegration) (*Service, error) {
	if database == nil || authorizer == nil || projector == nil || events == nil {
		return nil, errors.New("github issues: store, authorizer, projector and mutation event hook are required")
	}
	if err := validateNotificationIntegrations(optional); err != nil {
		return nil, errors.New("github issues: invalid notification integration")
	}
	var notifications NotificationIntegration
	if len(optional) == 1 {
		notifications = optional[0]
	}
	return &Service{store: database, authorizer: authorizer, projector: projector, events: events,
		notifications: notifications}, nil
}

func validateNotificationIntegrations(optional []NotificationIntegration) error {
	if len(optional) > 1 || (len(optional) == 1 && optional[0].Enabled &&
		(optional[0].OrdinaryIssue == nil || optional[0].Completed == nil)) {
		return errors.New("invalid notification integration")
	}
	return nil
}

type DecisionError struct{ Decision authz.Decision }

func (e *DecisionError) Error() string { return "github issues: authorization denied" }

var (
	ErrInvalidPreviewRequest    = errors.New("github issues: invalid preview request")
	ErrPreviewDigestMismatch    = errors.New("github issues: preview digest does not match current source")
	ErrTrustedAnswerRequired    = errors.New("github issues: ANSWER creation requires the trusted answer endpoint")
	ErrAnswerImmutable          = errors.New("github issues: ANSWER comments are immutable")
	ErrInvalidAnswerIntent      = errors.New("github issues: invalid answer intent")
	ErrInvalidQuestionAuthority = errors.New("github issues: invalid QUESTION authority")
	ErrQuestionChanged          = errors.New("github issues: QUESTION changed after confirmation")
)

type PreviewSource struct {
	Kind      string
	CommentID int64
}

const (
	PreviewSourceIssue   = "issue"
	PreviewSourceComment = "comment"
)

type AnswerIntent struct {
	QuestionID     string
	QuestionDigest string
	OptionIDs      []string
	Custom         string
}

type QuestionAuthority struct {
	Snapshot              model.QuestionSnapshot `json:"question"`
	RepresentationVersion int64                  `json:"representation_version"`
	BodyDigest            string                 `json:"body_digest"`
	// EffectiveAnswer is the backend-selected latest effective ANSWER for this
	// QUESTION, or null when no valid ANSWER exists yet. It is display context
	// for answer surfaces; ANSWER comments remain the stored authority.
	EffectiveAnswer *EffectiveAnswer `json:"effective_answer,omitempty"`
}

// EffectiveAnswer is the bounded view of one resolved ANSWER exposed to the
// Web UI next to QUESTION authority.
type EffectiveAnswer struct {
	ID        string                `json:"id"`
	CommentID int64                 `json:"comment_id"`
	Actor     string                `json:"actor"`
	CreatedAt time.Time             `json:"created_at"`
	Selection model.AnswerSelection `json:"selection"`
	SourceURL string                `json:"source_url"`
}

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

// PreviewDocument re-reads one exact issue body or comment through the normal
// repository read authorization boundary, then selects one executable preview
// and verifies the caller-observed digest against current stored bytes.
func (s *Service) PreviewDocument(ctx context.Context, owner, repository string, issueNumber int64,
	subject authz.Subject, source PreviewSource, previewID, digest string) (string, error) {
	if issueNumber <= 0 || strings.TrimSpace(previewID) == "" || !digestPattern.MatchString(digest) ||
		(source.Kind != PreviewSourceIssue && source.Kind != PreviewSourceComment) ||
		(source.Kind == PreviewSourceIssue && source.CommentID != 0) ||
		(source.Kind == PreviewSourceComment && source.CommentID <= 0) {
		return "", ErrInvalidPreviewRequest
	}
	resource, err := s.resolveRead(ctx, owner, repository, subject)
	if err != nil {
		return "", err
	}
	var body string
	err = s.store.WithTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx *store.Tx) error {
		repositoryStore := tx.ScopedRepo(resource.Scope)
		switch source.Kind {
		case PreviewSourceIssue:
			issue, err := repositoryStore.IssueByNumber(ctx, issueNumber)
			if err != nil {
				return err
			}
			body = issue.Body
		case PreviewSourceComment:
			comment, err := repositoryStore.CommentByCompatibilityID(ctx, source.CommentID)
			if err != nil {
				return err
			}
			if comment.IssueNumber != issueNumber {
				return store.ErrNotFound
			}
			body = comment.Comment.Body
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	selected, err := preview.Select(body, previewID)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidPreviewRequest, err)
	}
	if selected.Descriptor.Digest != digest {
		return "", ErrPreviewDigestMismatch
	}
	return selected.Source, nil
}

var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

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

func (s *Service) GetRepository(ctx context.Context, owner, repository string, subject authz.Subject) (models.RepositoryResource, error) {
	return s.resolveRead(ctx, owner, repository, subject)
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
		if len(input.Labels) > 0 && !canonicalProposalContribution(input) {
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
		if s.notifications.Enabled {
			queue, err := emaildelivery.NewStore(tx.PGX())
			if err != nil {
				return err
			}
			if err := s.notifications.OrdinaryIssue.ProjectIssueCreated(ctx, repositoryStore, queue,
				resource, issue, actor.User.ID); err != nil {
				return err
			}
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
		repositoryStore := tx.ScopedRepo(resource.Scope)
		current, err := repositoryStore.IssueByNumber(ctx, number)
		if err != nil {
			return err
		}
		before, err := s.captureLifecycle(ctx, tx, resource.Scope, current.ID)
		if err != nil {
			return err
		}
		update, err := overlay(current)
		if err != nil {
			return err
		}
		operation := authz.OperationTriage
		if current.AuthorID != nil && *current.AuthorID == actor.User.ID && update.State == current.State &&
			(update.Title != current.Title || update.Body != current.Body) {
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
		after, err := s.captureLifecycle(ctx, tx, resource.Scope, updated.ID)
		if err != nil {
			return err
		}
		if err := s.projectCompletedNotifications(ctx, tx, repositoryStore, resource, updated, nil,
			actor.User.ID, before, after); err != nil {
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

// canonicalProposalContribution is the sole label exception for contributors.
// It deliberately mirrors the existing issue projection marker shape in this
// service instead of introducing an artifact-aware authorization layer.
func canonicalProposalContribution(input models.NewIssue) bool {
	if len(input.Labels) != 1 || !strings.EqualFold(strings.TrimSpace(input.Labels[0]), proposalLabel) {
		return false
	}
	matches := issueMarker.FindAllStringSubmatch(model.CanonicalView(input.Body), -1)
	if len(matches) != 1 || !strings.EqualFold(strings.TrimSpace(matches[0][1]), "proposal") || strings.TrimSpace(matches[0][2]) == "" {
		return false
	}
	version, err := strconv.Atoi(strings.TrimSpace(matches[0][3]))
	return err == nil && version == 1
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

// GetQuestion re-reads one projected QUESTION through a bounded keyed lookup.
// The projection is only an index: SnapshotQuestion validates the exact current
// stored comment body before anything is returned as confirmation authority.
func (s *Service) GetQuestion(ctx context.Context, owner, repository string, issueNumber int64,
	subject authz.Subject, webOrigin, questionID string) (models.RepositoryResource, QuestionAuthority, error) {
	if issueNumber <= 0 || model.ValidateTypedIdentity("QUESTION", questionID) != nil {
		return models.RepositoryResource{}, QuestionAuthority{}, ErrInvalidAnswerIntent
	}
	resource, err := s.resolveRead(ctx, owner, repository, subject)
	if err != nil {
		return models.RepositoryResource{}, QuestionAuthority{}, err
	}
	var result QuestionAuthority
	err = s.store.WithTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx *store.Tx) error {
		repositoryStore := tx.ScopedRepo(resource.Scope)
		authority, err := repositoryStore.TypedCommentAuthorityByKey(
			ctx, issueNumber, questionID, "QUESTION", false)
		if err != nil {
			return err
		}
		result, err = questionAuthority(authority, webOrigin, resource)
		if err != nil {
			return err
		}
		observations, err := repositoryStore.TypedAnswerObservationsByIssue(ctx, issueNumber)
		if err != nil {
			return err
		}
		result.EffectiveAnswer = effectiveAnswerView(observations, questionID, webOrigin, resource, issueNumber)
		return nil
	})
	return resource, result, err
}

// effectiveAnswerView runs canonical effective-answer resolution over the
// issue's projected ANSWER comments and returns the bounded view for one
// QUESTION, or nil when no valid ANSWER exists.
func effectiveAnswerView(observations []store.TypedAnswerObservation, questionID, webOrigin string,
	resource models.RepositoryResource, issueNumber int64) *EffectiveAnswer {
	modelObservations := make([]model.AnswerObservation, 0, len(observations))
	for _, observation := range observations {
		modelObservations = append(modelObservations, model.AnswerObservation{
			ProviderID: strconv.FormatInt(observation.CompatibilityID, 10),
			Actor:      observation.ActorLogin, CreatedAt: observation.CreatedAt,
			UpdatedAt: observation.UpdatedAt, RepresentationVersion: observation.RepresentationVersion,
			Body: observation.Body,
		})
	}
	resolved, ok := model.ResolveEffectiveAnswers(modelObservations).Effective[questionID]
	if !ok {
		return nil
	}
	commentID, err := strconv.ParseInt(resolved.ProviderID, 10, 64)
	if err != nil || commentID <= 0 {
		return nil
	}
	sourceURL, err := typedCommentSourceURL(webOrigin, resource.Owner, resource.Name, issueNumber, commentID)
	if err != nil {
		sourceURL = ""
	}
	return &EffectiveAnswer{
		ID: resolved.ID, CommentID: commentID, Actor: resolved.Actor,
		CreatedAt: resolved.CreatedAt, Selection: resolved.Payload.Selection, SourceURL: sourceURL,
	}
}

// CreateAnswer is the sole self-hosted ANSWER creation path. It locks and
// revalidates the exact current QUESTION, builds canonical typed Markdown from
// a bounded intent, and then uses the normal comment transaction so projection,
// collection versions, notifications and mutation events remain atomic.
func (s *Service) CreateAnswer(ctx context.Context, owner, repository string, issueNumber int64,
	subject authz.Subject, webOrigin string, intent AnswerIntent) (models.RepositoryResource,
	models.CommentSnapshot, QuestionAuthority, error) {
	actor, ok := authenticatedActor(subject)
	if !ok {
		return models.RepositoryResource{}, models.CommentSnapshot{}, QuestionAuthority{},
			&DecisionError{Decision: authz.Decision{Exists: true, Visible: true}}
	}
	if issueNumber <= 0 || model.ValidateTypedIdentity("QUESTION", intent.QuestionID) != nil ||
		!digestPattern.MatchString(intent.QuestionDigest) || len(intent.OptionIDs) > 20 {
		return models.RepositoryResource{}, models.CommentSnapshot{}, QuestionAuthority{}, ErrInvalidAnswerIntent
	}
	var resource models.RepositoryResource
	var snapshot models.CommentSnapshot
	var question QuestionAuthority
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
		issue, err := repositoryStore.IssueByNumber(ctx, issueNumber)
		if err != nil {
			return err
		}
		authority, err := repositoryStore.TypedCommentAuthorityByKey(
			ctx, issueNumber, intent.QuestionID, "QUESTION", true)
		if err != nil {
			return err
		}
		question, err = questionAuthority(authority, webOrigin, resource)
		if err != nil {
			return err
		}
		if question.BodyDigest != intent.QuestionDigest {
			return ErrQuestionChanged
		}
		payload, err := model.BuildAnswerPayload(question.Snapshot, intent.OptionIDs, intent.Custom)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidAnswerIntent, err)
		}
		answerID, err := repositoryStore.AllocateIssueScopedTypedCommentID(ctx, issueNumber, "ANSWER")
		if err != nil {
			return err
		}
		if err := model.ValidateIssueScopedTypedIdentity("ANSWER", answerID, issueNumber); err != nil {
			return fmt.Errorf("allocate canonical ANSWER identity: %w", err)
		}
		commentID := uuid.New()
		body, err := templates.AnswerComment(templates.AnswerOptions{
			ID: answerID, Agent: actor.User.Login, Scope: intent.QuestionID, Payload: payload,
		})
		if err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidAnswerIntent, err)
		}
		snapshot, err = s.createCommentInTx(ctx, tx, repositoryStore, resource, issue, actor, commentID, body)
		return err
	})
	return resource, snapshot, question, err
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
		if answerShapedBody(body) {
			return ErrTrustedAnswerRequired
		}
		repositoryStore := tx.ScopedRepo(resource.Scope)
		issue, err := repositoryStore.IssueByNumber(ctx, issueNumber)
		if err != nil {
			return err
		}
		snapshot, err = s.createCommentInTx(ctx, tx, repositoryStore, resource, issue, actor, uuid.New(), body)
		return err
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

func (s *Service) DeleteComment(ctx context.Context, owner, repository string, compatibilityID int64,
	subject authz.Subject) (models.RepositoryResource, store.DeletedComment, error) {
	actor, ok := authenticatedActor(subject)
	if !ok {
		return models.RepositoryResource{}, store.DeletedComment{},
			&DecisionError{Decision: authz.Decision{Exists: true, Visible: true}}
	}
	var resource models.RepositoryResource
	var deleted store.DeletedComment
	err := s.store.WithinTx(ctx, func(tx *store.Tx) error {
		var err error
		resource, err = tx.ResolveRepository(ctx, owner, repository)
		if err != nil {
			return err
		}
		repositoryStore := tx.ScopedRepo(resource.Scope)
		current, err := repositoryStore.CommentByCompatibilityIDForUpdate(ctx, compatibilityID)
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
		if answerShapedBody(current.Comment.Body) {
			return ErrAnswerImmutable
		}
		deleted, err = repositoryStore.DeleteComment(ctx, compatibilityID)
		return err
	})
	return resource, deleted, err
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
		if answerShapedBody(current.Comment.Body) || answerShapedBody(body) {
			return ErrAnswerImmutable
		}
		issue, err := repositoryStore.IssueByNumber(ctx, current.IssueNumber)
		if err != nil {
			return err
		}
		before, err := s.captureLifecycle(ctx, tx, resource.Scope, issue.ID)
		if err != nil {
			return err
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
		after, err := s.captureLifecycle(ctx, tx, resource.Scope, issue.ID)
		if err != nil {
			return err
		}
		if err := s.projectCompletedNotifications(ctx, tx, repositoryStore, resource, issue, &snapshot,
			actor.User.ID, before, after); err != nil {
			return err
		}
		if err := s.projectCommentNotifications(ctx, tx, repositoryStore, actor.User.ID, snapshot); err != nil {
			return err
		}
		snapshot.Reactions, err = repositoryStore.ReactionSummary(ctx, snapshot.Comment.ID)
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

func (s *Service) createCommentInTx(ctx context.Context, tx *store.Tx, repositoryStore store.RepoStore,
	resource models.RepositoryResource, issue models.Issue, actor serverauth.Principal, commentID uuid.UUID,
	body string) (models.CommentSnapshot, error) {
	before, err := s.captureLifecycle(ctx, tx, resource.Scope, issue.ID)
	if err != nil {
		return models.CommentSnapshot{}, err
	}
	snapshot, err := repositoryStore.CreateComment(ctx, models.NewComment{
		ID: commentID, IssueNumber: issue.Number, AuthorID: &actor.User.ID, Body: body,
	})
	if err != nil {
		return models.CommentSnapshot{}, err
	}
	if err := s.projector.ProjectComment(ctx, repositoryStore, snapshot); err != nil {
		return models.CommentSnapshot{}, err
	}
	after, err := s.captureLifecycle(ctx, tx, resource.Scope, issue.ID)
	if err != nil {
		return models.CommentSnapshot{}, err
	}
	if err := s.projectCompletedNotifications(ctx, tx, repositoryStore, resource, issue, &snapshot,
		actor.User.ID, before, after); err != nil {
		return models.CommentSnapshot{}, err
	}
	if err := s.projectCommentNotifications(ctx, tx, repositoryStore, actor.User.ID, snapshot); err != nil {
		return models.CommentSnapshot{}, err
	}
	snapshot.Reactions, err = repositoryStore.ReactionSummary(ctx, snapshot.Comment.ID)
	if err != nil {
		return models.CommentSnapshot{}, err
	}
	err = s.events.Emit(ctx, repositoryStore, MutationEvent{
		Key:  mutationKey(snapshot.Comment.ID, snapshot.Comment.RepresentationVersion, "issue_comment.created"),
		Type: "issue_comment.created", Scope: resource.Scope, Issue: commentEventIssue(issue),
		Comment: &snapshot, RawBody: body, BodyHash: sha256.Sum256([]byte(body)),
		ActorUserID: actor.User.ID, ActorCredentialKind: actor.Kind,
		RepresentationVersion: snapshot.Comment.RepresentationVersion,
	})
	if err != nil {
		return models.CommentSnapshot{}, err
	}
	return snapshot, nil
}

func questionAuthority(authority store.TypedCommentAuthority, webOrigin string,
	resource models.RepositoryResource) (QuestionAuthority, error) {
	sourceURL, err := typedCommentSourceURL(webOrigin, resource.Owner, resource.Name,
		authority.IssueNumber, authority.CompatibilityID)
	if err != nil {
		return QuestionAuthority{}, fmt.Errorf("%w: %v", ErrInvalidQuestionAuthority, err)
	}
	snapshot, err := model.SnapshotQuestion(authority.Body, sourceURL)
	if err != nil {
		return QuestionAuthority{}, fmt.Errorf("%w: %v", ErrInvalidQuestionAuthority, err)
	}
	return QuestionAuthority{
		Snapshot: snapshot, RepresentationVersion: authority.RepresentationVersion,
		BodyDigest: model.RepresentationDigest(authority.Body),
	}, nil
}

func typedCommentSourceURL(webOrigin, owner, repository string, issueNumber, commentID int64) (string, error) {
	base, err := url.Parse(strings.TrimRight(strings.TrimSpace(webOrigin), "/"))
	if err != nil || base.Scheme == "" || base.Host == "" || base.User != nil ||
		(base.Scheme != "http" && base.Scheme != "https") || issueNumber <= 0 || commentID <= 0 {
		return "", errors.New("invalid Web origin or comment identity")
	}
	base = base.JoinPath(owner, repository, "issues", strconv.FormatInt(issueNumber, 10))
	base.RawQuery, base.Fragment = "", "issuecomment-"+strconv.FormatInt(commentID, 10)
	return base.String(), nil
}

func answerShapedBody(body string) bool {
	parsed := model.ParseTypedComment(body)
	return parsed.Type == "ANSWER" || strings.HasPrefix(parsed.ID, "ANSWER-")
}

func (s *Service) captureLifecycle(ctx context.Context, tx *store.Tx, scope models.RepoScope,
	issueID uuid.UUID) (ChangeLifecycle, error) {
	if !s.notifications.Enabled {
		return ChangeLifecycle{}, nil
	}
	return s.notifications.Completed.Capture(ctx, tx.PGX(), scope, issueID)
}

func (s *Service) projectCompletedNotifications(ctx context.Context, tx *store.Tx, repository store.RepoStore,
	resource models.RepositoryResource, issue models.Issue, comment *models.CommentSnapshot, actorID uuid.UUID,
	before, after ChangeLifecycle) error {
	if !s.notifications.Enabled {
		return nil
	}
	queue, err := emaildelivery.NewStore(tx.PGX())
	if err != nil {
		return err
	}
	return s.notifications.Completed.ProjectCompleted(ctx, repository, queue, resource, issue, comment,
		actorID, before, after)
}

func (s *Service) projectCommentNotifications(ctx context.Context, tx *store.Tx, repository store.RepoStore,
	actorID uuid.UUID, snapshot models.CommentSnapshot) error {
	if !s.notifications.Enabled {
		return nil
	}
	queue, err := emaildelivery.NewStore(tx.PGX())
	if err != nil {
		return err
	}
	projector, err := mentions.NewProjector(transactionMentionEligibility{tx: tx.PGX()})
	if err != nil {
		return err
	}
	return projector.ProjectComment(ctx, repository, queue, mentions.CommentMutation{
		Scope: repository.Scope(), CommentID: snapshot.Comment.ID, ActorUserID: actorID,
		RepresentationVersion: snapshot.Comment.RepresentationVersion,
	})
}

// transactionMentionEligibility evaluates the recipient's identity-derived
// repository read authority through the same locked transaction as the comment
// and delivery rows. It intentionally carries no credential or address data.
type transactionMentionEligibility struct{ tx pgx.Tx }

func (e transactionMentionEligibility) CanReadRepository(ctx context.Context, userID uuid.UUID, scope models.RepoScope) (bool, error) {
	if e.tx == nil || userID == uuid.Nil || scope.Validate() != nil {
		return false, store.ErrInvalidInput
	}
	var allowed bool
	err := e.tx.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM users u
		JOIN repos r ON r.organization_id = $2 AND r.id = $3
		JOIN orgs o ON o.id = r.organization_id
		WHERE u.id = $1 AND u.status = 'active'
		AND o.archived_at IS NULL AND r.archived_at IS NULL
		AND NOT EXISTS (SELECT 1 FROM service_accounts sa WHERE sa.user_id = u.id)
		AND (
			r.visibility IN ('public', 'internal')
			OR EXISTS (SELECT 1 FROM site_role_assignments sr WHERE sr.user_id = u.id AND sr.role = 'site_admin')
			OR EXISTS (SELECT 1 FROM org_memberships om WHERE om.organization_id = r.organization_id
				AND om.user_id = u.id AND om.state = 'active')
			OR EXISTS (SELECT 1 FROM repo_collaborators rc WHERE rc.organization_id = r.organization_id
				AND rc.repository_id = r.id AND rc.user_id = u.id)
		)
	)`, userID, scope.OrgID, scope.RepoID).Scan(&allowed)
	if err != nil {
		return false, fmt.Errorf("github issues: evaluate mention recipient: %w", err)
	}
	return allowed, nil
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
