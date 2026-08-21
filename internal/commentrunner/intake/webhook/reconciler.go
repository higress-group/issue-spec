package webhook

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/issue-spec/internal/commentrunner"
	"github.com/higress-group/issue-spec/internal/commentrunner/intake"
	runnerrepository "github.com/higress-group/issue-spec/internal/commentrunner/repository"
	"github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/commentrunner/writeback"
	"github.com/higress-group/issue-spec/internal/github"
	"github.com/higress-group/issue-spec/internal/server/events/outbox"
	"github.com/higress-group/issue-spec/internal/server/models"
)

type ReconcileBackend interface {
	github.CommentRevisionOperations
	commentrunner.PermissionBackend
	writeback.GitHubOperations
	GetIssueContext(context.Context, string, int, github.ConditionalRequest) (github.IssueContextResult, error)
	ListCommentReactionsPage(context.Context, string, int64, github.RunnerPageOptions) (github.CommentReactionsResult, error)
	AddCommentReaction(context.Context, string, int64, string) (github.RunnerReactionResult, error)
}

type ReconcilerClock interface{ Now() time.Time }

type ReconcilerConfig struct {
	Queue               DeliveryQueue
	Store               state.StateStore
	Backend             ReconcileBackend
	Scopes              RepositoryScopes
	Registry            runnerrepository.Registry
	Runner              commentrunner.Config
	AuthorizationPolicy commentrunner.AuthorizationPolicy
	WorkerID            string
	Workers             int
	LeaseDuration       time.Duration
	IdleDelay           time.Duration
	Clock               ReconcilerClock
	Observer            ReconcileObserver
}

type Reconciler struct{ config ReconcilerConfig }

type ReconcileResult struct {
	Claimed          bool                  `json:"claimed"`
	DeliveryID       string                `json:"delivery_id,omitempty"`
	EventID          string                `json:"event_id,omitempty"`
	Repository       string                `json:"repository,omitempty"`
	TriggerCommentID int64                 `json:"trigger_comment_id,omitempty"`
	PublicSessionID  string                `json:"public_session_id,omitempty"`
	Outcome          state.DeliveryOutcome `json:"outcome,omitempty"`
	JobID            string                `json:"job_id,omitempty"`
	CancellationID   string                `json:"cancellation_id,omitempty"`
	Completed        bool                  `json:"completed,omitempty"`
	Retried          bool                  `json:"retried,omitempty"`
	Failed           bool                  `json:"failed,omitempty"`
	Idempotent       bool                  `json:"idempotent,omitempty"`
	Reason           string                `json:"reason,omitempty"`
}

type ReconcileObserver interface {
	ObserveWebhookReconcile(ReconcileResult) error
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }

func NewReconciler(config ReconcilerConfig) (*Reconciler, error) {
	if config.Queue == nil || config.Store == nil || config.Backend == nil {
		return nil, errors.New("webhook reconciler requires queue, state store, and backend")
	}
	if config.Registry == nil && (len(config.Scopes.ByRepository) == 0 || len(config.Scopes.ByScope) != len(config.Scopes.ByRepository)) {
		return nil, errors.New("webhook reconciler requires complete one-to-one repository scopes")
	}
	if strings.TrimSpace(config.WorkerID) == "" {
		config.WorkerID = "runner-serve"
	}
	if config.Workers <= 0 {
		config.Workers = 1
	}
	if config.Workers > 32 {
		return nil, errors.New("webhook reconciler workers exceed safe bound")
	}
	if config.LeaseDuration <= 0 {
		config.LeaseDuration = 2 * time.Minute
	}
	if config.LeaseDuration < 10*time.Second || config.LeaseDuration > 10*time.Minute {
		return nil, errors.New("webhook reconciler lease duration is outside safe bounds")
	}
	if config.IdleDelay <= 0 {
		config.IdleDelay = 250 * time.Millisecond
	}
	if config.Clock == nil {
		config.Clock = systemClock{}
	}
	return &Reconciler{config: config}, nil
}

func (r *Reconciler) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errCh := make(chan error, r.config.Workers)
	var workers sync.WaitGroup
	for index := 0; index < r.config.Workers; index++ {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			workerID := fmt.Sprintf("%s-%d", r.config.WorkerID, index+1)
			for {
				result, err := r.processOne(ctx, workerID)
				if err != nil {
					select {
					case errCh <- err:
					case <-ctx.Done():
					}
					return
				}
				if result.Claimed {
					continue
				}
				timer := time.NewTimer(r.config.IdleDelay)
				select {
				case <-ctx.Done():
					if !timer.Stop() {
						<-timer.C
					}
					return
				case <-timer.C:
				}
			}
		}(index)
	}
	var result error
	select {
	case <-ctx.Done():
		result = nil
	case result = <-errCh:
		cancel()
	}
	workers.Wait()
	return result
}

func (r *Reconciler) ProcessOne(ctx context.Context) (ReconcileResult, error) {
	return r.processOne(ctx, r.config.WorkerID)
}

func (r *Reconciler) processOne(ctx context.Context, workerID string) (result ReconcileResult, returnErr error) {
	defer func() {
		if !result.Claimed || r.config.Observer == nil {
			return
		}
		if err := r.config.Observer.ObserveWebhookReconcile(result); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("persist webhook reconciliation diagnostics: %w", err))
		}
	}()
	now := r.config.Clock.Now().UTC()
	delivery, err := r.config.Queue.Claim(ctx, workerID, r.config.LeaseDuration, now)
	if errors.Is(err, ErrNoPending) {
		return ReconcileResult{}, nil
	}
	if err != nil {
		return ReconcileResult{}, fmt.Errorf("claim webhook delivery: %w", err)
	}
	result = ReconcileResult{Claimed: true, DeliveryID: delivery.DeliveryID, EventID: delivery.EventID}
	return r.reconcileClaim(ctx, delivery, result)
}

func (r *Reconciler) reconcileClaim(ctx context.Context, delivery state.WebhookDelivery,
	result ReconcileResult) (ReconcileResult, error) {
	envelope, err := validateClaimedEnvelope(delivery)
	if err != nil {
		return r.fail(ctx, delivery, result, "invalid_envelope")
	}
	if envelope.EventType != "issue_comment.created" && envelope.EventType != "issue_comment.edited" {
		return r.fail(ctx, delivery, result, "invalid_envelope")
	}
	result.TriggerCommentID = envelope.Comment.NumericID
	scope := models.RepoScope{OrgID: envelope.OrganizationID, RepoID: envelope.RepositoryID}
	repository := ""
	if r.config.Registry != nil {
		entry, resolveErr := r.config.Registry.ResolveScope(ctx, scope)
		if resolveErr != nil {
			if runnerrepository.IsRepositoryIneligible(resolveErr) {
				return r.ignoreIneligible(ctx, delivery, result, envelope.Comment.RepresentationVersion)
			}
			return r.release(ctx, delivery, result, "repository_authority_unavailable")
		}
		repository = entry.Repository
	} else {
		var ok bool
		repository, ok = r.config.Scopes.Repository(scope)
		if !ok {
			return r.fail(ctx, delivery, result, "binding_unavailable")
		}
	}
	result.Repository = repository
	remote, err := r.config.Backend.GetIssueComment(ctx, repository, envelope.Comment.NumericID)
	if err != nil {
		if retryAuthoritativeError(err) {
			return r.release(ctx, delivery, result, "authoritative_refetch")
		}
		return r.fail(ctx, delivery, result, "processing_failed")
	}
	comparison, err := compareAuthoritativeComment(envelope, remote)
	if err != nil {
		return r.fail(ctx, delivery, result, "invalid_envelope")
	}
	if comparison == authoritativeOlder {
		return r.release(ctx, delivery, result, "authoritative_older")
	}
	if comparison == authoritativeNewer {
		return r.recordTerminalDecision(ctx, delivery, result, DurableDecision{
			Outcome: state.DeliveryOutcomeSuperseded, AuthoritativeRevision: remote.RepresentationVersion,
		}, nil)
	}
	issue, err := r.config.Backend.GetIssueContext(ctx, repository, int(envelope.Issue.Number), github.ConditionalRequest{})
	if err != nil {
		if retryAuthoritativeError(err) {
			return r.release(ctx, delivery, result, "issue_refetch")
		}
		return r.fail(ctx, delivery, result, "processing_failed")
	}
	if issue.Issue.Number != int(envelope.Issue.Number) || !nodeIDMatches(issue.Issue.NodeID, "Issue", envelope.Issue.StableID) {
		return r.fail(ctx, delivery, result, "invalid_envelope")
	}
	if !strings.EqualFold(strings.TrimSpace(issue.Issue.State), "open") {
		return r.recordTerminalDecision(ctx, delivery, result, DurableDecision{
			Outcome: state.DeliveryOutcomeIgnored, AuthoritativeRevision: remote.RepresentationVersion,
		}, nil)
	}
	runnerConfig := r.config.Runner
	runnerConfig.Repositories = []string{repository}
	currentBeforeDecision, err := r.config.Store.Load(ctx)
	if err != nil {
		return r.release(ctx, delivery, result, "state_load")
	}
	decision, err := intake.DecideAuthoritativeComment(ctx, r.config.Backend, runnerConfig,
		r.config.AuthorizationPolicy, currentBeforeDecision, remote.Comment, r.config.Clock.Now().UTC())
	if err != nil {
		return r.release(ctx, delivery, result, "decision_unavailable")
	}
	if decision.Report.Authorization.Reason == commentrunner.AuthReasonPermissionLookupFailed ||
		decision.Report.Authorization.Reason == commentrunner.AuthReasonRunnerIdentityLookupFailed {
		return r.release(ctx, delivery, result, "authorization_unavailable")
	}
	result.Idempotent = eventDecisionAlreadyPresent(currentBeforeDecision, decision)
	switch decision.Outcome {
	case state.DeliveryOutcomeJob:
		result.PublicSessionID = decision.Job.PublicSessionID
	case state.DeliveryOutcomeCancellation:
		result.PublicSessionID = decision.Cancellation.TargetPublicSessionID
	}
	durable := durableDecision(decision, remote.RepresentationVersion)
	recorded, err := r.config.Queue.RecordDecision(ctx, delivery.DeliveryID, delivery.LeaseOwner,
		delivery.LeaseToken, r.config.Clock.Now().UTC(), durable, decision.Apply)
	if err != nil {
		if errors.Is(err, intake.ErrEventDecisionLinkMismatch) || errors.Is(err, ErrDecisionConflict) || errors.Is(err, ErrInvalid) {
			return r.fail(ctx, delivery, result, "processing_failed")
		}
		return r.release(ctx, delivery, result, "state_update")
	}
	result.Outcome, result.JobID, result.CancellationID = recorded.Outcome, recorded.JobID, recorded.CancellationID
	current, err := r.config.Store.Load(ctx)
	if err != nil {
		return r.release(ctx, delivery, result, "state_load")
	}
	if err := decision.ValidateLink(current, repository, remote.Comment.IssueNumber, remote.Comment.ID); err != nil {
		return r.fail(ctx, delivery, result, "processing_failed")
	}
	if !recorded.AckPending {
		return r.complete(ctx, delivery, result)
	}
	if err := r.acknowledge(ctx, decision, repository, remote.Comment); err != nil {
		return r.release(ctx, delivery, result, "ack_failed")
	}
	if err := r.config.Queue.MarkAcknowledged(ctx, delivery.DeliveryID, delivery.LeaseOwner,
		delivery.LeaseToken, r.config.Clock.Now().UTC()); err != nil {
		return r.release(ctx, delivery, result, "ack_state")
	}
	return r.complete(ctx, delivery, result)
}

func (r *Reconciler) ignoreIneligible(ctx context.Context, delivery state.WebhookDelivery,
	result ReconcileResult, revision int64) (ReconcileResult, error) {
	result, err := r.recordTerminalDecision(ctx, delivery, result, DurableDecision{
		Outcome: state.DeliveryOutcomeIgnored, AuthoritativeRevision: revision,
	}, nil)
	if err == nil {
		result.Reason = "repository_ineligible"
	}
	return result, err
}

func eventDecisionAlreadyPresent(current state.RunnerState, decision intake.EventDecision) bool {
	current.Normalize()
	switch decision.Outcome {
	case state.DeliveryOutcomeJob:
		_, ok := current.Jobs[decision.Job.ID]
		return ok
	case state.DeliveryOutcomeCancellation:
		_, ok := current.FindCancellation(decision.Cancellation.IdempotencyKey)
		return ok
	case state.DeliveryOutcomeRejected:
		_, ok := current.FindStatusWriteback(decision.Rejection.IdempotencyKey)
		return ok
	default:
		return false
	}
}

func (r *Reconciler) acknowledge(ctx context.Context, decision intake.EventDecision, repository string,
	comment github.Comment) error {
	if decision.Outcome == state.DeliveryOutcomeRejected {
		request, ok := decision.RejectionWritebackRequest()
		if !ok {
			return errors.New("rejection writeback request is missing")
		}
		_, err := (&writeback.Service{GitHub: r.config.Backend, Store: r.config.Store}).Write(ctx, request)
		return err
	}
	if decision.Outcome != state.DeliveryOutcomeJob && decision.Outcome != state.DeliveryOutcomeCancellation {
		return nil
	}
	runner, _, err := r.config.Backend.GetUser(ctx)
	if err != nil || strings.TrimSpace(runner.Login) == "" {
		return errors.New("runner identity lookup failed")
	}
	if expected := strings.TrimSpace(r.config.AuthorizationPolicy.RunnerLogin); expected != "" &&
		!strings.EqualFold(expected, runner.Login) {
		return errors.New("runner identity mismatch")
	}
	page := github.RunnerPageOptions{}
	for {
		reactions, err := r.config.Backend.ListCommentReactionsPage(ctx, repository, comment.ID, page)
		if err != nil {
			return err
		}
		for _, reaction := range reactions.Reactions {
			if reaction.User != nil && strings.EqualFold(strings.TrimSpace(reaction.User.Login), strings.TrimSpace(runner.Login)) &&
				strings.EqualFold(strings.TrimSpace(reaction.Content), "eyes") {
				return nil
			}
		}
		if reactions.Metadata.Pagination.NextURL == "" {
			break
		}
		page = github.RunnerPageOptions{CursorURL: reactions.Metadata.Pagination.NextURL}
	}
	_, err = r.config.Backend.AddCommentReaction(ctx, repository, comment.ID, "eyes")
	return err
}

func (r *Reconciler) recordTerminalDecision(ctx context.Context, delivery state.WebhookDelivery,
	result ReconcileResult, decision DurableDecision, mutate DecisionMutation) (ReconcileResult, error) {
	recorded, err := r.config.Queue.RecordDecision(ctx, delivery.DeliveryID, delivery.LeaseOwner,
		delivery.LeaseToken, r.config.Clock.Now().UTC(), decision, mutate)
	if err != nil {
		return r.release(ctx, delivery, result, "state_update")
	}
	result.Outcome = recorded.Outcome
	return r.complete(ctx, delivery, result)
}

func (r *Reconciler) complete(ctx context.Context, delivery state.WebhookDelivery,
	result ReconcileResult) (ReconcileResult, error) {
	if err := r.config.Queue.Complete(ctx, delivery.DeliveryID, delivery.LeaseOwner,
		delivery.LeaseToken, r.config.Clock.Now().UTC()); err != nil {
		return r.release(ctx, delivery, result, "complete_failed")
	}
	result.Completed, result.Reason = true, "completed"
	return result, nil
}

func (r *Reconciler) release(ctx context.Context, delivery state.WebhookDelivery,
	result ReconcileResult, reason string) (ReconcileResult, error) {
	err := r.config.Queue.Release(ctx, delivery.DeliveryID, delivery.LeaseOwner,
		delivery.LeaseToken, r.config.Clock.Now().UTC())
	if err != nil && !errors.Is(err, ErrLeaseLost) {
		return result, fmt.Errorf("release webhook delivery: %w", err)
	}
	result.Retried, result.Reason = true, reason
	return result, nil
}

func (r *Reconciler) fail(ctx context.Context, delivery state.WebhookDelivery,
	result ReconcileResult, diagnostic string) (ReconcileResult, error) {
	err := r.config.Queue.Fail(ctx, delivery.DeliveryID, delivery.LeaseOwner,
		delivery.LeaseToken, r.config.Clock.Now().UTC(), diagnostic)
	if err != nil && !errors.Is(err, ErrLeaseLost) {
		return result, fmt.Errorf("fail webhook delivery: %w", err)
	}
	result.Failed, result.Reason = true, diagnostic
	return result, nil
}

func durableDecision(decision intake.EventDecision, revision int64) DurableDecision {
	result := DurableDecision{Outcome: decision.Outcome, AuthoritativeRevision: revision}
	switch decision.Outcome {
	case state.DeliveryOutcomeJob:
		result.JobID, result.AckRequired = decision.Job.ID, true
	case state.DeliveryOutcomeCancellation:
		result.CancellationID, result.AckRequired = decision.Cancellation.ID, true
	case state.DeliveryOutcomeRejected:
		result.StatusWritebackKey, result.AckRequired = decision.Rejection.IdempotencyKey, true
	}
	return result
}

type authoritativeComparison int

const (
	authoritativeExact authoritativeComparison = iota
	authoritativeOlder
	authoritativeNewer
)

func compareAuthoritativeComment(envelope outbox.Envelope, remote github.RunnerCommentResult) (authoritativeComparison, error) {
	if envelope.Comment == nil || remote.Comment.ID != envelope.Comment.NumericID ||
		remote.Comment.IssueNumber != int(envelope.Issue.Number) || remote.Comment.User == nil ||
		!nodeIDMatches(remote.Comment.NodeID, "IssueComment", envelope.Comment.StableID) ||
		!remote.Comment.CreatedAt.Equal(envelope.Comment.CreatedAt) {
		return authoritativeExact, errors.New("authoritative comment identity mismatch")
	}
	if envelope.Author.UserID == nil || !nodeIDMatches(remote.Comment.User.NodeID, "User", *envelope.Author.UserID) {
		return authoritativeExact, errors.New("authoritative author identity mismatch")
	}
	if remote.RepresentationVersion < envelope.Comment.RepresentationVersion {
		return authoritativeOlder, nil
	}
	if remote.RepresentationVersion > envelope.Comment.RepresentationVersion {
		if remote.Comment.UpdatedAt.Before(envelope.Comment.UpdatedAt) {
			return authoritativeExact, errors.New("newer representation has an older timestamp")
		}
		return authoritativeNewer, nil
	}
	body := sha256.Sum256([]byte(remote.Comment.Body))
	if !remote.Comment.UpdatedAt.Equal(envelope.Comment.UpdatedAt) ||
		!strings.EqualFold(hex.EncodeToString(body[:]), envelope.BodyHash) ||
		!strings.EqualFold(strings.TrimSpace(remote.Comment.User.Login), strings.TrimSpace(envelope.Author.Login)) {
		return authoritativeExact, errors.New("authoritative comment revision mismatch")
	}
	return authoritativeExact, nil
}

func validateClaimedEnvelope(delivery state.WebhookDelivery) (outbox.Envelope, error) {
	var envelope outbox.Envelope
	digest := sha256.Sum256(delivery.RawEnvelope)
	if !strings.EqualFold(delivery.BodySHA256, hex.EncodeToString(digest[:])) {
		return envelope, errors.New("delivery body digest mismatch")
	}
	decoder := json.NewDecoder(strings.NewReader(string(delivery.RawEnvelope)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return envelope, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return envelope, errors.New("envelope contains trailing JSON")
	}
	if envelope.SchemaVersion != outbox.SchemaVersion || envelope.EventID == uuid.Nil || envelope.ActorUserID == uuid.Nil ||
		envelope.Author.UserID == nil || *envelope.Author.UserID == uuid.Nil || strings.TrimSpace(envelope.Author.Login) == "" ||
		envelope.OccurredAt.IsZero() || envelope.Issue.StableID == uuid.Nil || envelope.Issue.Number <= 0 ||
		envelope.Issue.CreatedAt.IsZero() || envelope.Issue.UpdatedAt.IsZero() ||
		envelope.EventID.String() != delivery.EventID ||
		envelope.EventKey != delivery.EventKey || envelope.EventType != delivery.EventType || envelope.Action != delivery.Action ||
		envelope.OrganizationID.String() != delivery.OrganizationID || envelope.RepositoryID.String() != delivery.RepositoryID ||
		envelope.Issue.StableID.String() != delivery.IssueID || envelope.Issue.Number != delivery.IssueNumber || envelope.Comment == nil ||
		envelope.Comment.StableID.String() != delivery.CommentID || envelope.Comment.RepresentationVersion != delivery.CommentRevision ||
		envelope.Author.Login != delivery.AuthorLogin || !strings.EqualFold(envelope.BodyHash, delivery.EnvelopeBodySHA256) {
		return envelope, errors.New("flattened delivery metadata mismatch")
	}
	body := sha256.Sum256([]byte(envelope.RawBody))
	if !strings.EqualFold(envelope.BodyHash, hex.EncodeToString(body[:])) || envelope.Comment.NumericID <= 0 ||
		envelope.Comment.RepresentationVersion <= 0 || envelope.Comment.CreatedAt.IsZero() || envelope.Comment.UpdatedAt.IsZero() {
		return envelope, errors.New("envelope revision is incomplete")
	}
	expectedAction := ""
	switch envelope.EventType {
	case "issue_comment.created":
		expectedAction = "created"
	case "issue_comment.edited":
		expectedAction = "edited"
	}
	if expectedAction != "" && envelope.Action != expectedAction {
		return envelope, errors.New("event action mismatch")
	}
	return envelope, nil
}

func nodeIDMatches(nodeID, kind string, stable uuid.UUID) bool {
	decoded, err := base64.RawStdEncoding.DecodeString(strings.TrimSpace(nodeID))
	if err != nil {
		return false
	}
	return string(decoded) == kind+":"+stable.String()
}

func retryAuthoritativeError(err error) bool {
	var apiError *github.APIError
	if errors.As(err, &apiError) {
		return apiError.StatusCode == http.StatusNotFound || apiError.StatusCode == http.StatusTooManyRequests ||
			apiError.StatusCode >= http.StatusInternalServerError
	}
	var urlError *url.Error
	if errors.As(err, &urlError) {
		return true
	}
	var networkError net.Error
	return errors.As(err, &networkError)
}
