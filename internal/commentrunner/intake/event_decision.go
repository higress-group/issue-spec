package intake

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/higress-group/issue-spec/internal/commentrunner"
	crstate "github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/commentrunner/writeback"
	"github.com/higress-group/issue-spec/internal/github"
)

const SourceWebhook = "webhook"

var ErrEventDecisionLinkMismatch = errors.New("event decision linkage mismatch")

// EventDecision is a side-effect-free command decision built only from an
// authoritative refetched comment, current authorization, and a state
// snapshot. Apply mutates local RunnerState only; remote ack/writeback belongs
// to the reconciler after the containing StateStore.Update succeeds.
type EventDecision struct {
	Outcome             crstate.DeliveryOutcome
	Report              CommandReport
	Candidate           commentrunner.CommandCandidate
	Job                 crstate.Job
	Cancellation        crstate.Cancellation
	RejectionJob        crstate.Job
	Rejection           crstate.StatusWriteback
	RejectionPhase      string
	RejectionDiagnostic string
}

func DecideAuthoritativeComment(ctx context.Context, backend commentrunner.PermissionBackend, cfg commentrunner.Config,
	policy commentrunner.AuthorizationPolicy, snapshot crstate.RunnerState, comment github.Comment, now time.Time) (EventDecision, error) {
	cfg = cfg.Normalized()
	if backend == nil || strings.TrimSpace(cfg.Hostname) == "" || comment.ID <= 0 || comment.IssueNumber <= 0 ||
		comment.User == nil || strings.TrimSpace(comment.User.Login) == "" || comment.UpdatedAt.IsZero() || now.IsZero() {
		return EventDecision{}, fmt.Errorf("authoritative comment decision requires backend, scope, identity, revision, and observation time")
	}
	snapshot.Normalize()
	trigger := commentrunner.TriggerComment{Repo: cfgRepoForComment(cfg, comment), Issue: comment.IssueNumber,
		CommentID: comment.ID, CommentURL: comment.HTMLURL, Body: comment.Body, Commenter: comment.User.Login,
		UpdatedAt: comment.UpdatedAt, ObservedAt: now}
	if trigger.Repo == "" {
		return EventDecision{}, fmt.Errorf("authoritative comment repository is not configured")
	}
	seen := seenCommentFromTrigger(cfg, trigger, comment.URL)
	parse := commentrunner.ParseCommandComment(trigger)
	switch parse.Status {
	case commentrunner.ParseStatusIgnored:
		return EventDecision{Outcome: crstate.DeliveryOutcomeIgnored,
			Report: baseReport(SourceWebhook, trigger, CommandStatusIgnored)}, nil
	case commentrunner.ParseStatusRejected:
		report := baseReport(SourceWebhook, trigger, CommandStatusRejected)
		report.ParseRejection = parse.Rejection
		report.Reason, report.Message = string(parse.Rejection.Reason), parse.Rejection.Message
		return rejectedEventDecision(cfg, seen, report, now), nil
	case commentrunner.ParseStatusAccepted:
		return decideAcceptedEvent(ctx, backend, cfg, policy, &snapshot, seen, parse.Candidate, now), nil
	default:
		return EventDecision{}, fmt.Errorf("unsupported parser status %q", parse.Status)
	}
}

// cfgRepoForComment is intentionally strict. The reconciler supplies a config
// narrowed to exactly the trusted repository resolved from the event scope.
func cfgRepoForComment(cfg commentrunner.Config, _ github.Comment) string {
	if len(cfg.Repositories) != 1 {
		return ""
	}
	return strings.TrimSpace(cfg.Repositories[0])
}

func decideAcceptedEvent(ctx context.Context, backend commentrunner.PermissionBackend, cfg commentrunner.Config,
	policy commentrunner.AuthorizationPolicy, snapshot *crstate.RunnerState, seen crstate.SeenComment,
	candidate commentrunner.CommandCandidate, now time.Time) EventDecision {
	authRepo := candidate.Repo
	cancelTargetJobID := ""
	if candidate.Verb == commentrunner.VerbResume {
		session, ok := snapshot.GetPublicSession(candidate.Repo, candidate.PublicSessionID)
		if !ok {
			return rejectedCandidateDecision(cfg, seen, candidate, ReasonSessionNotFound,
				"public session id was not found in this repository", commentrunner.AuthorizationResult{}, now)
		}
		authRepo = session.Repo
	}
	if candidate.Verb == commentrunner.VerbCancel {
		if session, ok := snapshot.GetPublicSession(candidate.Repo, candidate.PublicSessionID); ok {
			authRepo = session.Repo
		} else if job, ok := activeCancelTarget(snapshot, candidate.Repo, candidate.PublicSessionID); ok {
			authRepo, cancelTargetJobID = job.Repo, job.ID
		} else {
			return rejectedCandidateDecision(cfg, seen, candidate, ReasonSessionNotFound,
				"public session id was not found in this repository", commentrunner.AuthorizationResult{}, now)
		}
	}
	authorization := commentrunner.AuthorizeCandidateForRepo(ctx, backend, candidate, authRepo, policy)
	if !authorization.Allowed {
		report := candidateReport(SourceWebhook, candidate, CommandStatusUnauthorized)
		report.Authorization, report.Reason, report.Message = authorization, string(authorization.Reason), authorization.Message
		return rejectedEventDecision(cfg, seen, report, now)
	}
	if candidate.Verb == commentrunner.VerbCancel {
		if !cfg.CancellationEnabled {
			return rejectedCandidateDecision(cfg, seen, candidate, ReasonCancellationDisabled,
				"runner cancellation is disabled by configuration", authorization, now)
		}
		seen.ProducedCommandCandidate, seen.CommandCandidateID = true, candidate.ID
		seen.CommandName, seen.CancelIdempotencyKey = string(candidate.Verb), candidate.IdempotencyKey
		cancel := crstate.Cancellation{ID: stableID("cancel", candidate.IdempotencyKey), IdempotencyKey: candidate.IdempotencyKey,
			Repo: candidate.Repo, IssueNumber: candidate.Issue, TriggerCommentID: candidate.TriggerCommentID,
			CancelingUserLogin: candidate.Commenter, TargetPublicSessionID: candidate.PublicSessionID,
			TargetJobID: cancelTargetJobID, Status: crstate.StatusQueued, CreatedAt: now}
		report := candidateReport(SourceWebhook, candidate, CommandStatusCancelQueued)
		report.Authorization, report.CancellationID = authorization, cancel.ID
		return EventDecision{Outcome: crstate.DeliveryOutcomeCancellation, Report: report, Candidate: candidate, Cancellation: cancel}
	}
	seen.ProducedCommandCandidate, seen.CommandCandidateID = true, candidate.ID
	seen.CommandName, seen.CommandIdempotencyKey = string(candidate.Verb), candidate.IdempotencyKey
	job := crstate.Job{ID: stableID("job", candidate.IdempotencyKey), Repo: candidate.Repo, IssueNumber: candidate.Issue,
		PublicSessionID: candidate.PublicSessionID, CoordinatorKind: cfg.Agent.Kind, Model: cfg.Agent.Model,
		SessionCreatorLogin: sessionCreator(candidate), TriggeringUserLogin: candidate.Commenter,
		TriggerCommentID: candidate.TriggerCommentID, CommandID: candidate.ID, CommandName: string(candidate.Verb),
		CommandPrompt: candidate.Prompt, CommandIdempotencyKey: candidate.IdempotencyKey, Status: crstate.StatusQueued,
		CreatedAt: now, UpdatedAt: now, FirstObservedComment: seen, SourceLabels: []string{SourceWebhook}}
	report := candidateReport(SourceWebhook, candidate, CommandStatusJobQueued)
	report.Authorization, report.JobID = authorization, job.ID
	return EventDecision{Outcome: crstate.DeliveryOutcomeJob, Report: report, Candidate: candidate, Job: job}
}

func rejectedCandidateDecision(cfg commentrunner.Config, seen crstate.SeenComment, candidate commentrunner.CommandCandidate,
	reason, message string, authorization commentrunner.AuthorizationResult, now time.Time) EventDecision {
	report := candidateReport(SourceWebhook, candidate, CommandStatusRejected)
	report.Authorization, report.Reason, report.Message = authorization, reason, message
	return rejectedEventDecision(cfg, seen, report, now)
}

func rejectedEventDecision(cfg commentrunner.Config, seen crstate.SeenComment, report CommandReport, now time.Time) EventDecision {
	key := rejectedStatusWritebackKey(report)
	job := rejectedWritebackJob(cfg, seen, report, key, now)
	rejection := crstate.StatusWriteback{IdempotencyKey: key, JobID: job.ID, Repo: report.Repo,
		IssueNumber: report.Issue, TriggerCommentID: report.CommentID, Status: crstate.StatusRejected}
	return EventDecision{Outcome: crstate.DeliveryOutcomeRejected, Report: report, RejectionJob: job,
		Rejection: rejection, RejectionPhase: rejectedPhase(report), RejectionDiagnostic: rejectedDiagnostic(report)}
}

func (d EventDecision) Apply(state *crstate.RunnerState) error {
	if state == nil {
		return fmt.Errorf("runner state is required")
	}
	state.Normalize()
	switch d.Outcome {
	case crstate.DeliveryOutcomeIgnored:
		return nil
	case crstate.DeliveryOutcomeJob:
		job, _, err := state.CreateCommandJob(d.Job)
		if err != nil {
			return err
		}
		if job.ID != d.Job.ID || job.Repo != d.Job.Repo || job.IssueNumber != d.Job.IssueNumber ||
			job.TriggerCommentID != d.Job.TriggerCommentID {
			return fmt.Errorf("%w: existing command job", ErrEventDecisionLinkMismatch)
		}
		return nil
	case crstate.DeliveryOutcomeCancellation:
		if existing, ok := state.FindCancellation(d.Cancellation.IdempotencyKey); ok {
			if existing.ID != d.Cancellation.ID || existing.Repo != d.Cancellation.Repo ||
				existing.IssueNumber != d.Cancellation.IssueNumber || existing.TriggerCommentID != d.Cancellation.TriggerCommentID {
				return fmt.Errorf("%w: existing cancellation", ErrEventDecisionLinkMismatch)
			}
			return nil
		}
		return state.UpsertCancellation(d.Cancellation)
	case crstate.DeliveryOutcomeRejected:
		if existing, ok := state.FindStatusWriteback(d.Rejection.IdempotencyKey); ok {
			if existing.Repo != d.Rejection.Repo || existing.IssueNumber != d.Rejection.IssueNumber ||
				existing.TriggerCommentID != d.Rejection.TriggerCommentID {
				return fmt.Errorf("%w: existing rejection", ErrEventDecisionLinkMismatch)
			}
			return nil
		}
		return state.UpsertStatusWriteback(d.Rejection)
	default:
		return fmt.Errorf("unsupported event decision outcome %q", d.Outcome)
	}
}

func (d EventDecision) ValidateLink(state crstate.RunnerState, repo string, issue int, commentID int64) error {
	state.Normalize()
	switch d.Outcome {
	case crstate.DeliveryOutcomeIgnored:
		return nil
	case crstate.DeliveryOutcomeJob:
		job, ok := state.Jobs[d.Job.ID]
		if !ok || job.Repo != repo || job.IssueNumber != issue || job.TriggerCommentID != commentID {
			return fmt.Errorf("%w: linked job", ErrEventDecisionLinkMismatch)
		}
	case crstate.DeliveryOutcomeCancellation:
		cancel, ok := state.Cancellations[d.Cancellation.ID]
		if !ok || cancel.Repo != repo || cancel.IssueNumber != issue || cancel.TriggerCommentID != commentID {
			return fmt.Errorf("%w: linked cancellation", ErrEventDecisionLinkMismatch)
		}
	case crstate.DeliveryOutcomeRejected:
		rejection, ok := state.StatusWritebacks[d.Rejection.IdempotencyKey]
		if !ok || rejection.Repo != repo || rejection.IssueNumber != issue || rejection.TriggerCommentID != commentID {
			return fmt.Errorf("%w: linked rejection", ErrEventDecisionLinkMismatch)
		}
	default:
		return fmt.Errorf("unsupported event decision outcome %q", d.Outcome)
	}
	return nil
}

func (d EventDecision) RejectionWritebackRequest() (writeback.Request, bool) {
	if d.Outcome != crstate.DeliveryOutcomeRejected {
		return writeback.Request{}, false
	}
	return writeback.Request{Job: d.RejectionJob, Status: crstate.StatusRejected,
		Phase: d.RejectionPhase, Diagnostics: []string{d.RejectionDiagnostic}}, true
}
