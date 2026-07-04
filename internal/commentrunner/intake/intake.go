package intake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/higress-group/issue-spec/internal/commentrunner"
	crstate "github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/commentrunner/writeback"
	"github.com/higress-group/issue-spec/internal/github"
)

const (
	SourceNotification       = "notification"
	SourceRepositoryFallback = "repository_fallback"

	CommandStatusIgnored      = "ignored"
	CommandStatusRejected     = "rejected"
	CommandStatusUnauthorized = "unauthorized"
	CommandStatusDuplicate    = "duplicate"
	CommandStatusJobQueued    = "job_queued"
	CommandStatusCancelQueued = "cancel_queued"

	ReasonSessionNotFound      = "session_not_found"
	ReasonCancellationDisabled = "cancellation_disabled"

	queuedJobReactionContent = "eyes"
)

type Backend interface {
	github.RunnerOperations
	commentrunner.PermissionBackend
}

type Store interface {
	Load(context.Context) (crstate.RunnerState, error)
	Save(context.Context, crstate.RunnerState) error
}

type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

type Options struct {
	DryRun              bool
	AuthorizationPolicy commentrunner.AuthorizationPolicy
	Clock               Clock
}

type Result struct {
	OK            bool              `json:"ok"`
	DryRun        bool              `json:"dry_run"`
	StartedAt     time.Time         `json:"started_at"`
	FinishedAt    time.Time         `json:"finished_at"`
	Repositories  []RepositoryCycle `json:"repositories,omitempty"`
	Commands      []CommandReport   `json:"commands,omitempty"`
	Jobs          []JobCandidate    `json:"jobs,omitempty"`
	Cancellations []CancelCandidate `json:"cancellations,omitempty"`
	Diagnostics   []Diagnostic      `json:"diagnostics,omitempty"`
	Next          NextStep          `json:"next"`
}

type RepositoryCycle struct {
	Repo             string    `json:"repo"`
	NotificationSeen int       `json:"notification_seen,omitempty"`
	FallbackDue      bool      `json:"fallback_due"`
	FallbackNextAt   time.Time `json:"fallback_next_at,omitempty"`
}

type CommandReport struct {
	Source            string                            `json:"source"`
	Repo              string                            `json:"repo,omitempty"`
	Issue             int                               `json:"issue,omitempty"`
	CommentID         int64                             `json:"comment_id,omitempty"`
	CommentURL        string                            `json:"comment_url,omitempty"`
	Commenter         string                            `json:"commenter,omitempty"`
	Status            string                            `json:"status"`
	Verb              commentrunner.CommandVerb         `json:"verb,omitempty"`
	CommandID         string                            `json:"command_id,omitempty"`
	JobID             string                            `json:"job_id,omitempty"`
	CancellationID    string                            `json:"cancellation_id,omitempty"`
	PublicSessionID   string                            `json:"public_session_id,omitempty"`
	Created           bool                              `json:"created,omitempty"`
	Reason            string                            `json:"reason,omitempty"`
	Message           string                            `json:"message,omitempty"`
	ParseRejection    commentrunner.CommandRejection    `json:"parse_rejection,omitempty"`
	Authorization     commentrunner.AuthorizationResult `json:"authorization,omitempty"`
	FirstObservedAt   time.Time                         `json:"first_observed_at,omitempty"`
	FirstObservedHash string                            `json:"first_observed_body_hash,omitempty"`
}

type JobCandidate struct {
	JobID           string                    `json:"job_id"`
	CommandID       string                    `json:"command_id"`
	Repo            string                    `json:"repo"`
	Issue           int                       `json:"issue"`
	Verb            commentrunner.CommandVerb `json:"verb"`
	TriggerComment  int64                     `json:"trigger_comment_id"`
	Commenter       string                    `json:"commenter"`
	PublicSessionID string                    `json:"public_session_id,omitempty"`
	Created         bool                      `json:"created"`
}

type CancelCandidate struct {
	CancellationID  string `json:"cancellation_id"`
	Repo            string `json:"repo"`
	TriggerComment  int64  `json:"trigger_comment_id"`
	CancelingUser   string `json:"canceling_user"`
	PublicSessionID string `json:"public_session_id"`
	Created         bool   `json:"created"`
}

type Diagnostic struct {
	Source  string `json:"source,omitempty"`
	Repo    string `json:"repo,omitempty"`
	Issue   int    `json:"issue,omitempty"`
	Message string `json:"message"`
}

type NextStep struct {
	PollAfter        time.Duration `json:"poll_after"`
	PollAt           time.Time     `json:"poll_at"`
	FallbackAfter    time.Duration `json:"fallback_after"`
	FallbackAt       time.Time     `json:"fallback_at,omitempty"`
	Reason           string        `json:"reason,omitempty"`
	RateLimitResetAt time.Time     `json:"rate_limit_reset_at,omitempty"`
}

func RunOnce(ctx context.Context, cfg commentrunner.Config, backend Backend, store Store, opts Options) (Result, error) {
	cfg = cfg.Normalized()
	if err := cfg.Validate(); err != nil {
		return Result{}, err
	}
	if backend == nil {
		return Result{}, fmt.Errorf("intake backend is required")
	}
	if store == nil {
		return Result{}, fmt.Errorf("intake state store is required")
	}
	clock := opts.Clock
	if clock == nil {
		clock = realClock{}
	}
	now := clock.Now().UTC()
	policy := opts.AuthorizationPolicy
	if zeroAuthorizationPolicy(policy) {
		policy = commentrunner.DefaultAuthorizationPolicy()
	}
	if policy.RunnerLogin == "" {
		policy.RunnerLogin = cfg.RunnerIdentity
	}

	loaded, err := store.Load(ctx)
	if err != nil {
		return Result{}, err
	}
	st, err := cloneState(loaded)
	if err != nil {
		return Result{}, err
	}
	st.Normalize()

	result := Result{OK: true, DryRun: opts.DryRun, StartedAt: now}
	repoSet := map[string]bool{}
	for _, repo := range cfg.Repositories {
		repoSet[repo] = true
		ensureRepoState(&st, cfg, repo)
	}

	notifications, notificationMeta, err := pollNotifications(ctx, backend, st, cfg.Repositories)
	if err != nil {
		if hasResponseMetadata(notificationMeta) {
			applyNotificationMetadata(&st, cfg.Repositories, notificationMeta, now)
		}
		result.OK = false
		result.Diagnostics = append(result.Diagnostics, Diagnostic{Source: SourceNotification, Message: err.Error()})
	} else {
		applyNotificationMetadata(&st, cfg.Repositories, notificationMeta, now)
		intakeNotifications(ctx, backend, cfg, policy, &st, notifications, repoSet, now, &result)
	}

	for _, repo := range cfg.Repositories {
		cycle := RepositoryCycle{Repo: repo}
		for _, notification := range notifications {
			if strings.EqualFold(notification.Repository.FullName, repo) {
				cycle.NotificationSeen++
			}
		}
		repoState := st.Repositories[repo]
		cycle.FallbackDue = fallbackDue(repoState, now)
		if cycle.FallbackDue {
			intakeFallback(ctx, backend, cfg, policy, &st, repo, now, &result)
			repoState = st.Repositories[repo]
		}
		cycle.FallbackNextAt = repoState.FallbackCadence.NextPollAt
		result.Repositories = append(result.Repositories, cycle)
	}

	result.Next = computeNextStep(cfg, st, now)
	result.FinishedAt = clock.Now().UTC()
	if !opts.DryRun {
		if err := store.Save(ctx, st); err != nil {
			return result, err
		}
	}
	return result, nil
}

func cloneState(st crstate.RunnerState) (crstate.RunnerState, error) {
	data, err := json.Marshal(st)
	if err != nil {
		return crstate.RunnerState{}, err
	}
	var out crstate.RunnerState
	if err := json.Unmarshal(data, &out); err != nil {
		return crstate.RunnerState{}, err
	}
	out.Normalize()
	return out, nil
}

func zeroAuthorizationPolicy(policy commentrunner.AuthorizationPolicy) bool {
	return policy.RunnerLogin == "" && len(policy.AllowedUsers) == 0 && !policy.AllowAuthenticatedUser
}

func pollNotifications(ctx context.Context, backend Backend, st crstate.RunnerState, repos []string) ([]github.Notification, github.ResponseMetadata, error) {
	cursor := notificationCursor(st, repos)
	result, err := backend.PollNotifications(ctx, github.NotificationListOptions{
		ConditionalRequest: github.ConditionalRequest{ETag: cursor.ETag, LastModified: cursor.LastModified},
		All:                true,
	})
	if err != nil {
		return nil, result.Metadata, err
	}
	if result.Metadata.NotModified {
		return nil, result.Metadata, nil
	}
	return result.Notifications, result.Metadata, nil
}

func intakeNotifications(ctx context.Context, backend Backend, cfg commentrunner.Config, policy commentrunner.AuthorizationPolicy, st *crstate.RunnerState, notifications []github.Notification, repoSet map[string]bool, now time.Time, result *Result) {
	seenThreads := map[string]bool{}
	for _, notification := range notifications {
		repo := strings.TrimSpace(notification.Repository.FullName)
		if !repoSet[repo] {
			continue
		}
		issueNumber := notificationIssueNumber(notification)
		if issueNumber <= 0 {
			result.Diagnostics = append(result.Diagnostics, Diagnostic{Source: SourceNotification, Repo: repo, Message: "notification subject did not contain an issue or pull request number"})
			continue
		}
		key := repo + "#" + strconv.Itoa(issueNumber)
		if seenThreads[key] {
			continue
		}
		seenThreads[key] = true
		intakeIssueComments(ctx, backend, cfg, policy, st, repo, issueNumber, SourceNotification, now, result)
	}
}

func intakeIssueComments(ctx context.Context, backend Backend, cfg commentrunner.Config, policy commentrunner.AuthorizationPolicy, st *crstate.RunnerState, repo string, issueNumber int, source string, now time.Time, result *Result) {
	repoState := st.Repositories[repo]
	cursorKey := strconv.Itoa(issueNumber)
	cursor := repoState.NotificationThreadCursors[cursorKey]
	lastSuccessfulPollAt := cursor.LastSuccessfulPollAt
	pendingCursor := cursor
	page := github.RunnerPageOptions{}
	for {
		commentsResult, err := backend.ListIssueCommentsPage(ctx, repo, issueNumber, github.CommentListOptions{
			ConditionalRequest: github.ConditionalRequest{ETag: cursor.ETag, LastModified: cursor.LastModified},
			Page:               page,
		})
		if err != nil {
			if hasResponseMetadata(commentsResult.Metadata) {
				if cursor.Cursor == "" && page.CursorURL != "" {
					cursor.Cursor = page.CursorURL
				}
				cursor = updateCursorErrorMetadata(cursor, fmt.Sprintf("issue-comments:%s#%d", repo, issueNumber), commentsResult.Metadata, now)
				cursor.LastSuccessfulPollAt = lastSuccessfulPollAt
				repoState = st.Repositories[repo]
				repoState.NotificationThreadCursors[cursorKey] = cursor
				st.Repositories[repo] = repoState
			}
			result.OK = false
			result.Diagnostics = append(result.Diagnostics, Diagnostic{Source: source, Repo: repo, Issue: issueNumber, Message: err.Error()})
			return
		}
		nextURL := commentsResult.Metadata.Pagination.NextURL
		if commentsResult.Metadata.NotModified && nextURL == "" && page.CursorURL == "" && cursor.Cursor != "" {
			nextURL = cursor.Cursor
		}
		pendingCursor = updateCursor(pendingCursor, fmt.Sprintf("issue-comments:%s#%d", repo, issueNumber), commentsResult.Metadata, now)
		if !commentsResult.Metadata.NotModified {
			for _, comment := range commentsResult.Comments {
				if comment.IssueNumber == 0 {
					comment.IssueNumber = issueNumber
				}
				processComment(ctx, backend, cfg, policy, st, repo, comment, source, now, result)
			}
		}
		if nextURL == "" {
			repoState = st.Repositories[repo]
			repoState.NotificationThreadCursors[cursorKey] = pendingCursor
			st.Repositories[repo] = repoState
			return
		}
		page = github.RunnerPageOptions{CursorURL: nextURL}
		cursor = pendingCursor
	}
}

func intakeFallback(ctx context.Context, backend Backend, cfg commentrunner.Config, policy commentrunner.AuthorizationPolicy, st *crstate.RunnerState, repo string, now time.Time, result *Result) {
	repoState := st.Repositories[repo]
	cursor := repoState.RepositoryCommentCursor
	page := github.RunnerPageOptions{}
	for {
		commentsResult, err := backend.ListRepositoryIssueCommentsPage(ctx, repo, github.CommentListOptions{
			ConditionalRequest: github.ConditionalRequest{ETag: cursor.ETag, LastModified: cursor.LastModified},
			Page:               page,
			Since:              sinceFromCursor(cursor),
		})
		if err != nil {
			if hasResponseMetadata(commentsResult.Metadata) {
				cursor = updateCursor(cursor, "repo-comments:"+repo, commentsResult.Metadata, now)
				repoState.RepositoryCommentCursor = cursor
				st.Repositories[repo] = repoState
			}
			result.OK = false
			result.Diagnostics = append(result.Diagnostics, Diagnostic{Source: SourceRepositoryFallback, Repo: repo, Message: err.Error()})
			break
		}
		cursor = updateCursor(cursor, "repo-comments:"+repo, commentsResult.Metadata, now)
		if !commentsResult.Metadata.NotModified {
			for _, comment := range commentsResult.Comments {
				issueNumber := comment.IssueNumber
				if issueNumber == 0 {
					issueNumber = issueNumberFromURL(comment.IssueURL)
					comment.IssueNumber = issueNumber
				}
				if issueNumber <= 0 {
					result.Diagnostics = append(result.Diagnostics, Diagnostic{Source: SourceRepositoryFallback, Repo: repo, Message: fmt.Sprintf("comment %d did not include an issue number", comment.ID)})
					continue
				}
				processComment(ctx, backend, cfg, policy, st, repo, comment, SourceRepositoryFallback, now, result)
				if comment.ID > cursor.LastSeenID {
					cursor.LastSeenID = comment.ID
				}
				if comment.UpdatedAt.After(cursor.LastSeenAt) {
					cursor.LastSeenAt = comment.UpdatedAt.UTC()
				}
			}
		}
		if commentsResult.Metadata.Pagination.NextURL == "" {
			break
		}
		page = github.RunnerPageOptions{CursorURL: commentsResult.Metadata.Pagination.NextURL}
	}
	repoState = st.Repositories[repo]
	repoState.RepositoryCommentCursor = cursor
	repoState.FallbackCadence = crstate.FallbackCadence{
		Enabled:         true,
		IntervalSeconds: int(cfg.FallbackInterval.Duration.Seconds()),
		LastFallbackAt:  now,
		NextPollAt:      now.Add(cfg.FallbackInterval.Duration),
	}
	st.Repositories[repo] = repoState
}

func processComment(ctx context.Context, backend Backend, cfg commentrunner.Config, policy commentrunner.AuthorizationPolicy, st *crstate.RunnerState, repo string, comment github.Comment, source string, now time.Time, result *Result) {
	commenter := ""
	if comment.User != nil {
		commenter = comment.User.Login
	}
	trigger := commentrunner.TriggerComment{
		Repo:       repo,
		Issue:      comment.IssueNumber,
		CommentID:  comment.ID,
		CommentURL: comment.HTMLURL,
		Body:       comment.Body,
		Commenter:  commenter,
		UpdatedAt:  comment.UpdatedAt,
		ObservedAt: now,
	}
	seen := crstate.SeenComment{
		Host:                   cfg.Hostname,
		Repo:                   repo,
		IssueNumber:            comment.IssueNumber,
		CommentID:              comment.ID,
		HTMLURL:                comment.HTMLURL,
		APIURL:                 comment.URL,
		AuthorLogin:            commenter,
		FirstObservedAt:        now,
		FirstObservedUpdatedAt: comment.UpdatedAt,
		FirstObservedBodyHash:  commentrunner.BodyHash(comment.Body),
	}
	recorded, created, err := st.RecordSeenComment(seen)
	if err != nil {
		result.OK = false
		result.Diagnostics = append(result.Diagnostics, Diagnostic{Source: source, Repo: repo, Issue: comment.IssueNumber, Message: err.Error()})
		return
	}
	if !created {
		result.Commands = append(result.Commands, CommandReport{
			Source:            source,
			Repo:              recorded.Repo,
			Issue:             recorded.IssueNumber,
			CommentID:         recorded.CommentID,
			CommentURL:        recorded.HTMLURL,
			Commenter:         recorded.AuthorLogin,
			Status:            CommandStatusDuplicate,
			CommandID:         recorded.CommandCandidateID,
			JobID:             st.Idempotency.CommandJobs[recorded.CommandIdempotencyKey],
			CancellationID:    st.Idempotency.CancelRequests[recorded.CancelIdempotencyKey],
			Reason:            "already_observed",
			Message:           "comment was already observed; edits and duplicate deliveries are ignored",
			FirstObservedAt:   recorded.FirstObservedAt,
			FirstObservedHash: recorded.FirstObservedBodyHash,
		})
		return
	}

	parse := commentrunner.ParseCommandComment(trigger)
	switch parse.Status {
	case commentrunner.ParseStatusIgnored:
		result.Commands = append(result.Commands, baseReport(source, trigger, CommandStatusIgnored))
	case commentrunner.ParseStatusRejected:
		report := baseReport(source, trigger, CommandStatusRejected)
		report.ParseRejection = parse.Rejection
		report.Reason = string(parse.Rejection.Reason)
		report.Message = parse.Rejection.Message
		writeRejectedCommand(ctx, backend, cfg, st, recorded, report, now, result)
		result.Commands = append(result.Commands, report)
	case commentrunner.ParseStatusAccepted:
		processCandidate(ctx, backend, cfg, policy, st, recorded, parse.Candidate, source, now, result)
	}
}

func processCandidate(ctx context.Context, backend Backend, cfg commentrunner.Config, policy commentrunner.AuthorizationPolicy, st *crstate.RunnerState, seen crstate.SeenComment, candidate commentrunner.CommandCandidate, source string, now time.Time, result *Result) {
	authRepo := candidate.Repo
	cancelTargetJobID := ""
	if candidate.Verb == commentrunner.VerbResume {
		session, ok := st.GetPublicSession(candidate.Repo, candidate.PublicSessionID)
		if !ok {
			report := candidateReport(source, candidate, CommandStatusRejected)
			report.Reason = ReasonSessionNotFound
			report.Message = "public session id was not found in this repository"
			writeRejectedCommand(ctx, backend, cfg, st, seen, report, now, result)
			result.Commands = append(result.Commands, report)
			return
		}
		authRepo = session.Repo
	}
	if candidate.Verb == commentrunner.VerbCancel {
		if session, ok := st.GetPublicSession(candidate.Repo, candidate.PublicSessionID); ok {
			authRepo = session.Repo
		} else if job, ok := activeCancelTarget(st, candidate.Repo, candidate.PublicSessionID); ok {
			authRepo = job.Repo
			cancelTargetJobID = job.ID
		} else {
			report := candidateReport(source, candidate, CommandStatusRejected)
			report.Reason = ReasonSessionNotFound
			report.Message = "public session id was not found in this repository"
			writeRejectedCommand(ctx, backend, cfg, st, seen, report, now, result)
			result.Commands = append(result.Commands, report)
			return
		}
	}
	authz := commentrunner.AuthorizeCandidateForRepo(ctx, backend, candidate, authRepo, policy)
	if !authz.Allowed {
		report := candidateReport(source, candidate, CommandStatusUnauthorized)
		report.Authorization = authz
		report.Reason = string(authz.Reason)
		report.Message = authz.Message
		writeRejectedCommand(ctx, backend, cfg, st, seen, report, now, result)
		result.Commands = append(result.Commands, report)
		return
	}
	if candidate.Verb == commentrunner.VerbCancel {
		if !cfg.CancellationEnabled {
			report := candidateReport(source, candidate, CommandStatusRejected)
			report.Authorization = authz
			report.Reason = ReasonCancellationDisabled
			report.Message = "runner cancellation is disabled by configuration"
			writeRejectedCommand(ctx, backend, cfg, st, seen, report, now, result)
			result.Commands = append(result.Commands, report)
			return
		}
		queueCancellation(st, seen, candidate, source, authz, cancelTargetJobID, now, result)
		return
	}
	queueJob(ctx, backend, cfg, st, seen, candidate, source, authz, now, result)
}

func queueJob(ctx context.Context, backend Backend, cfg commentrunner.Config, st *crstate.RunnerState, seen crstate.SeenComment, candidate commentrunner.CommandCandidate, source string, authz commentrunner.AuthorizationResult, now time.Time, result *Result) {
	job := crstate.Job{
		ID:                    stableID("job", candidate.IdempotencyKey),
		Repo:                  candidate.Repo,
		IssueNumber:           candidate.Issue,
		PublicSessionID:       candidate.PublicSessionID,
		CoordinatorKind:       cfg.Agent.Kind,
		Model:                 cfg.Agent.Model,
		SessionCreatorLogin:   sessionCreator(candidate),
		TriggeringUserLogin:   candidate.Commenter,
		TriggerCommentID:      candidate.TriggerCommentID,
		CommandID:             candidate.ID,
		CommandName:           string(candidate.Verb),
		CommandPrompt:         candidate.Prompt,
		CommandIdempotencyKey: candidate.IdempotencyKey,
		Status:                crstate.StatusQueued,
		CreatedAt:             now,
		UpdatedAt:             now,
		FirstObservedComment:  seen,
		SourceLabels:          []string{source},
	}
	createdJob, created, err := st.CreateCommandJob(job)
	report := candidateReport(source, candidate, CommandStatusJobQueued)
	report.Authorization = authz
	if err != nil {
		result.OK = false
		report.Status = CommandStatusRejected
		report.Reason = "state_error"
		report.Message = err.Error()
		result.Commands = append(result.Commands, report)
		return
	}
	seen.ProducedCommandCandidate = true
	seen.CommandCandidateID = candidate.ID
	seen.CommandName = string(candidate.Verb)
	seen.CommandIdempotencyKey = candidate.IdempotencyKey
	st.SeenComments[crstate.SeenCommentKey(seen.Repo, seen.CommentID)] = seen

	report.JobID = createdJob.ID
	report.Created = created
	if !created {
		report.Status = CommandStatusDuplicate
		report.Reason = "idempotency_key_exists"
		report.Message = "command job already exists for this idempotency key"
	}
	result.Commands = append(result.Commands, report)
	result.Jobs = append(result.Jobs, JobCandidate{
		JobID:           createdJob.ID,
		CommandID:       candidate.ID,
		Repo:            candidate.Repo,
		Issue:           candidate.Issue,
		Verb:            candidate.Verb,
		TriggerComment:  candidate.TriggerCommentID,
		Commenter:       candidate.Commenter,
		PublicSessionID: candidate.PublicSessionID,
		Created:         created,
	})
	addQueuedJobReaction(ctx, backend, candidate, source, result)
}

func addQueuedJobReaction(ctx context.Context, backend Backend, candidate commentrunner.CommandCandidate, source string, result *Result) {
	if result == nil || result.DryRun {
		return
	}
	if strings.TrimSpace(candidate.Repo) == "" || candidate.TriggerCommentID == 0 {
		return
	}
	if _, err := backend.AddCommentReaction(ctx, candidate.Repo, candidate.TriggerCommentID, queuedJobReactionContent); err != nil {
		result.Diagnostics = append(result.Diagnostics, Diagnostic{
			Source:  source,
			Repo:    candidate.Repo,
			Issue:   candidate.Issue,
			Message: "queued job reaction: " + boundedOneLine(err.Error(), 512),
		})
	}
}

func queueCancellation(st *crstate.RunnerState, seen crstate.SeenComment, candidate commentrunner.CommandCandidate, source string, authz commentrunner.AuthorizationResult, targetJobID string, now time.Time, result *Result) {
	cancel := crstate.Cancellation{
		ID:                    stableID("cancel", candidate.IdempotencyKey),
		IdempotencyKey:        candidate.IdempotencyKey,
		Repo:                  candidate.Repo,
		TriggerCommentID:      candidate.TriggerCommentID,
		CancelingUserLogin:    candidate.Commenter,
		TargetPublicSessionID: candidate.PublicSessionID,
		TargetJobID:           targetJobID,
		Status:                crstate.StatusQueued,
		CreatedAt:             now,
	}
	created := true
	if existing, ok := st.FindCancellation(candidate.IdempotencyKey); ok {
		cancel = existing
		created = false
	} else if err := st.UpsertCancellation(cancel); err != nil {
		result.OK = false
		report := candidateReport(source, candidate, CommandStatusRejected)
		report.Authorization = authz
		report.Reason = "state_error"
		report.Message = err.Error()
		result.Commands = append(result.Commands, report)
		return
	}
	seen.ProducedCommandCandidate = true
	seen.CommandCandidateID = candidate.ID
	seen.CommandName = string(candidate.Verb)
	seen.CancelIdempotencyKey = candidate.IdempotencyKey
	st.SeenComments[crstate.SeenCommentKey(seen.Repo, seen.CommentID)] = seen

	report := candidateReport(source, candidate, CommandStatusCancelQueued)
	report.Authorization = authz
	report.CancellationID = cancel.ID
	report.Created = created
	if !created {
		report.Status = CommandStatusDuplicate
		report.Reason = "idempotency_key_exists"
		report.Message = "cancellation already exists for this idempotency key"
	}
	result.Commands = append(result.Commands, report)
	result.Cancellations = append(result.Cancellations, CancelCandidate{
		CancellationID:  cancel.ID,
		Repo:            candidate.Repo,
		TriggerComment:  candidate.TriggerCommentID,
		CancelingUser:   candidate.Commenter,
		PublicSessionID: candidate.PublicSessionID,
		Created:         created,
	})
}

func activeCancelTarget(st *crstate.RunnerState, repo, publicID string) (crstate.Job, bool) {
	if st == nil || strings.TrimSpace(repo) == "" || strings.TrimSpace(publicID) == "" {
		return crstate.Job{}, false
	}
	for _, status := range []crstate.LifecycleStatus{crstate.StatusRunning, crstate.StatusDispatched, crstate.StatusQueued} {
		for _, job := range st.ListJobs() {
			if job.Repo == repo && job.PublicSessionID == publicID && job.Status == status {
				return job, true
			}
		}
	}
	return crstate.Job{}, false
}

func writeRejectedCommand(ctx context.Context, backend Backend, cfg commentrunner.Config, st *crstate.RunnerState, seen crstate.SeenComment, report CommandReport, now time.Time, result *Result) {
	if result == nil || result.DryRun || st == nil {
		return
	}
	if strings.TrimSpace(report.Repo) == "" || report.Issue <= 0 || report.CommentID == 0 {
		return
	}
	key := rejectedStatusWritebackKey(report)
	seen.StatusWritebackIdempotencyKey = key
	if report.CommandID != "" {
		seen.ProducedCommandCandidate = true
		seen.CommandCandidateID = report.CommandID
		seen.CommandName = string(report.Verb)
	}
	st.SeenComments[crstate.SeenCommentKey(seen.Repo, seen.CommentID)] = seen

	job := rejectedWritebackJob(cfg, seen, report, key, now)
	service := &writeback.Service{
		GitHub: backend,
		Store:  runnerStateWritebackStore{state: st},
		Clock:  func() time.Time { return now },
	}
	if _, err := service.Write(ctx, writeback.Request{
		Job:         job,
		Status:      crstate.StatusRejected,
		Phase:       rejectedPhase(report),
		Diagnostics: []string{rejectedDiagnostic(report)},
	}); err != nil {
		result.OK = false
		result.Diagnostics = append(result.Diagnostics, Diagnostic{
			Source:  report.Source,
			Repo:    report.Repo,
			Issue:   report.Issue,
			Message: "rejected command writeback: " + boundedOneLine(err.Error(), 512),
		})
	}
}

type runnerStateWritebackStore struct {
	state *crstate.RunnerState
}

func (s runnerStateWritebackStore) Update(ctx context.Context, mutate func(*crstate.RunnerState) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.state == nil {
		return fmt.Errorf("intake writeback state is required")
	}
	if mutate == nil {
		return nil
	}
	if err := mutate(s.state); err != nil {
		return err
	}
	s.state.Normalize()
	return nil
}

func rejectedWritebackJob(cfg commentrunner.Config, seen crstate.SeenComment, report CommandReport, key string, now time.Time) crstate.Job {
	commandName := string(report.Verb)
	if commandName == "" {
		commandName = "rejected"
	}
	commandID := report.CommandID
	if commandID == "" {
		commandID = stableID("cmd", key)
	}
	createdAt := report.FirstObservedAt
	if createdAt.IsZero() {
		createdAt = seen.FirstObservedAt
	}
	if createdAt.IsZero() {
		createdAt = now
	}
	seen.StatusWritebackIdempotencyKey = key
	return crstate.Job{
		ID:                   stableID("job", key),
		Repo:                 report.Repo,
		IssueNumber:          report.Issue,
		PublicSessionID:      report.PublicSessionID,
		CoordinatorKind:      cfg.Agent.Kind,
		Model:                cfg.Agent.Model,
		TriggeringUserLogin:  report.Commenter,
		TriggerCommentID:     report.CommentID,
		CommandID:            commandID,
		CommandName:          commandName,
		StatusWritebackKey:   key,
		Status:               crstate.StatusRejected,
		CreatedAt:            createdAt,
		UpdatedAt:            now,
		FinishedAt:           now,
		FirstObservedComment: seen,
		SourceLabels:         []string{report.Source},
		Diagnostics:          []string{rejectedDiagnostic(report)},
	}
}

func rejectedStatusWritebackKey(report CommandReport) string {
	base := fmt.Sprintf("rejected-command-v1:%s:%d:%d:%s:%s", report.Repo, report.Issue, report.CommentID, report.Status, report.Reason)
	return "status:" + stableID("rejected", base)
}

func rejectedPhase(report CommandReport) string {
	switch {
	case report.Status == CommandStatusUnauthorized:
		return "command-unauthorized"
	case report.Reason == ReasonSessionNotFound:
		return "unknown-session"
	case report.Reason == ReasonCancellationDisabled:
		return "cancellation-disabled"
	default:
		return "command-rejected"
	}
}

func rejectedDiagnostic(report CommandReport) string {
	parts := []string{"command " + report.Status}
	if report.Reason != "" {
		parts = append(parts, "reason="+report.Reason)
	}
	if report.Authorization.Reason != "" {
		parts = append(parts, "auth="+string(report.Authorization.Reason))
	}
	if report.Message != "" {
		parts = append(parts, "message="+report.Message)
	}
	return boundedOneLine(strings.Join(parts, "; "), 512)
}

func boundedOneLine(value string, maxBytes int) string {
	value = strings.Join(strings.Fields(value), " ")
	if maxBytes <= 0 || len([]byte(value)) <= maxBytes {
		return value
	}
	for len([]byte(value)) > maxBytes-3 {
		_, size := utf8.DecodeLastRuneInString(value)
		if size <= 0 {
			return "..."
		}
		value = value[:len(value)-size]
	}
	return value + "..."
}

func ensureRepoState(st *crstate.RunnerState, cfg commentrunner.Config, repo string) {
	repoState := st.Repositories[repo]
	repoState.Host = cfg.Hostname
	repoState.Repo = repo
	repoState.Backend = string(cfg.GitHubBackend)
	repoState.RunnerLogin = cfg.RunnerIdentity
	if repoState.IssueCommentCursors == nil {
		repoState.IssueCommentCursors = map[string]crstate.CursorState{}
	}
	if repoState.NotificationThreadCursors == nil {
		repoState.NotificationThreadCursors = map[string]crstate.CursorState{}
	}
	if !repoState.FallbackCadence.Enabled {
		repoState.FallbackCadence.Enabled = true
		repoState.FallbackCadence.IntervalSeconds = int(cfg.FallbackInterval.Duration.Seconds())
	}
	st.Repositories[repo] = repoState
}

func notificationCursor(st crstate.RunnerState, repos []string) crstate.CursorState {
	for _, repo := range repos {
		cursor := st.Repositories[repo].NotificationCursor
		if cursor.ETag != "" || cursor.LastModified != "" {
			return cursor
		}
	}
	return crstate.CursorState{}
}

func applyNotificationMetadata(st *crstate.RunnerState, repos []string, meta github.ResponseMetadata, now time.Time) {
	for _, repo := range repos {
		repoState := st.Repositories[repo]
		repoState.NotificationCursor = updateCursor(repoState.NotificationCursor, "notifications", meta, now)
		st.Repositories[repo] = repoState
	}
}

func updateCursor(cursor crstate.CursorState, resource string, meta github.ResponseMetadata, now time.Time) crstate.CursorState {
	cursor.Resource = resource
	cursor.LastPollAt = now
	cursor.LastStatusCode = meta.StatusCode
	cursor.RateLimit = rateLimit(meta)
	if meta.ETag != "" {
		cursor.ETag = meta.ETag
	}
	if meta.LastModified != "" {
		cursor.LastModified = meta.LastModified
	}
	if meta.PollIntervalSeconds > 0 {
		cursor.XPollIntervalSeconds = meta.PollIntervalSeconds
	}
	if meta.Pagination.NextURL != "" {
		cursor.Cursor = meta.Pagination.NextURL
	} else {
		cursor.Cursor = ""
	}
	if !meta.NotModified && (meta.StatusCode == 0 || meta.StatusCode < 400) {
		cursor.LastSuccessfulPollAt = now
	}
	return cursor
}

func updateCursorErrorMetadata(cursor crstate.CursorState, resource string, meta github.ResponseMetadata, now time.Time) crstate.CursorState {
	cursor.Resource = resource
	cursor.LastPollAt = now
	cursor.LastStatusCode = meta.StatusCode
	cursor.RateLimit = rateLimit(meta)
	if meta.PollIntervalSeconds > 0 {
		cursor.XPollIntervalSeconds = meta.PollIntervalSeconds
	}
	return cursor
}

func rateLimit(meta github.ResponseMetadata) crstate.RateLimitState {
	return crstate.RateLimitState{
		Limit:             meta.RateLimit.Limit,
		Remaining:         meta.RateLimit.Remaining,
		ResetAt:           meta.RateLimit.ResetAt,
		Resource:          meta.RateLimit.Resource,
		RetryAfterSeconds: meta.RateLimit.RetryAfterSeconds,
	}
}

func hasResponseMetadata(meta github.ResponseMetadata) bool {
	return meta.StatusCode != 0 ||
		meta.ETag != "" ||
		meta.LastModified != "" ||
		meta.PollIntervalSeconds > 0 ||
		meta.RateLimit.Limit != 0 ||
		meta.RateLimit.Remaining != 0 ||
		meta.RateLimit.Used != 0 ||
		meta.RateLimit.ResetUnix != 0 ||
		!meta.RateLimit.ResetAt.IsZero() ||
		meta.RateLimit.Resource != "" ||
		meta.RateLimit.RetryAfterSeconds > 0 ||
		meta.Pagination.NextURL != "" ||
		meta.Pagination.PrevURL != "" ||
		meta.Pagination.FirstURL != "" ||
		meta.Pagination.LastURL != ""
}

func fallbackDue(repoState crstate.RepositoryState, now time.Time) bool {
	next := repoState.FallbackCadence.NextPollAt
	return next.IsZero() || !next.After(now)
}

func sinceFromCursor(cursor crstate.CursorState) *time.Time {
	if cursor.LastSeenAt.IsZero() {
		return nil
	}
	since := cursor.LastSeenAt.UTC()
	return &since
}

func computeNextStep(cfg commentrunner.Config, st crstate.RunnerState, now time.Time) NextStep {
	pollAfter := cfg.PollInterval.Duration
	var resetAt time.Time
	for _, repo := range cfg.Repositories {
		repoState := st.Repositories[repo]
		applyCursorBackoff(repoState.NotificationCursor, now, &pollAfter, &resetAt)
		applyCursorBackoff(repoState.RepositoryCommentCursor, now, &pollAfter, &resetAt)
		for _, cursor := range repoState.NotificationThreadCursors {
			applyCursorBackoff(cursor, now, &pollAfter, &resetAt)
		}
		for _, cursor := range repoState.IssueCommentCursors {
			applyCursorBackoff(cursor, now, &pollAfter, &resetAt)
		}
	}
	nextFallback := time.Time{}
	for _, repo := range cfg.Repositories {
		candidate := st.Repositories[repo].FallbackCadence.NextPollAt
		if candidate.IsZero() {
			continue
		}
		if nextFallback.IsZero() || candidate.Before(nextFallback) {
			nextFallback = candidate
		}
	}
	return NextStep{
		PollAfter:        pollAfter,
		PollAt:           now.Add(pollAfter),
		FallbackAfter:    cfg.FallbackInterval.Duration,
		FallbackAt:       nextFallback,
		Reason:           "poll interval, X-Poll-Interval, rate-limit reset, and Retry-After metadata evaluated",
		RateLimitResetAt: resetAt,
	}
}

func applyCursorBackoff(cursor crstate.CursorState, now time.Time, pollAfter *time.Duration, resetAt *time.Time) {
	if cursor.XPollIntervalSeconds > 0 {
		headerAfter := time.Duration(cursor.XPollIntervalSeconds) * time.Second
		if headerAfter > *pollAfter {
			*pollAfter = headerAfter
			*resetAt = time.Time{}
		}
	}
	if cursor.RateLimit.RetryAfterSeconds > 0 {
		retryAt := now.Add(time.Duration(cursor.RateLimit.RetryAfterSeconds) * time.Second)
		if !cursor.LastPollAt.IsZero() {
			retryAt = cursor.LastPollAt.Add(time.Duration(cursor.RateLimit.RetryAfterSeconds) * time.Second)
		}
		if retryAt.After(now) {
			wait := retryAt.Sub(now)
			if wait > *pollAfter {
				*pollAfter = wait
				*resetAt = time.Time{}
			}
		}
	}
	if cursor.RateLimit.Remaining == 0 && !cursor.RateLimit.ResetAt.IsZero() && cursor.RateLimit.ResetAt.After(now) {
		wait := cursor.RateLimit.ResetAt.Sub(now)
		if wait > *pollAfter {
			*pollAfter = wait
			*resetAt = cursor.RateLimit.ResetAt
		}
	}
}

func baseReport(source string, trigger commentrunner.TriggerComment, status string) CommandReport {
	return CommandReport{
		Source:            source,
		Repo:              trigger.Repo,
		Issue:             trigger.Issue,
		CommentID:         trigger.CommentID,
		CommentURL:        trigger.CommentURL,
		Commenter:         trigger.Commenter,
		Status:            status,
		FirstObservedAt:   trigger.ObservedAt,
		FirstObservedHash: commentrunner.BodyHash(trigger.Body),
	}
}

func candidateReport(source string, candidate commentrunner.CommandCandidate, status string) CommandReport {
	return CommandReport{
		Source:            source,
		Repo:              candidate.Repo,
		Issue:             candidate.Issue,
		CommentID:         candidate.TriggerCommentID,
		CommentURL:        candidate.CommentURL,
		Commenter:         candidate.Commenter,
		Status:            status,
		Verb:              candidate.Verb,
		CommandID:         candidate.ID,
		PublicSessionID:   candidate.PublicSessionID,
		FirstObservedAt:   candidate.FirstObservedAt,
		FirstObservedHash: candidate.FirstObservedBodyHash,
	}
}

func sessionCreator(candidate commentrunner.CommandCandidate) string {
	if candidate.Verb == commentrunner.VerbNew {
		return candidate.Commenter
	}
	return ""
}

func notificationIssueNumber(notification github.Notification) int {
	for _, raw := range []string{
		notification.Subject.URL,
		notification.Subject.LatestCommentURL,
		notification.URL,
	} {
		if n := issueNumberFromURL(raw); n > 0 {
			return n
		}
	}
	return 0
}

func issueNumberFromURL(raw string) int {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return 0
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for i := 0; i < len(parts)-1; i++ {
		switch parts[i] {
		case "issues", "pulls":
			n, err := strconv.Atoi(parts[i+1])
			if err == nil && n > 0 {
				return n
			}
		}
	}
	return 0
}

func stableID(prefix, key string) string {
	sum := sha256.Sum256([]byte(key))
	return prefix + "-" + hex.EncodeToString(sum[:])[:16]
}
